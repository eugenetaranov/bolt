package output

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestTaskResult_NonInteractive verifies the legacy plain output is unchanged
// when the writer is not a terminal (the common case: pipes, CI, buffers).
func TestTaskResult_NonInteractive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf) // bytes.Buffer is not a *os.File → interactive=false
	o.SetColor(false)

	o.TaskStart("install nginx", "apt") // no-op
	o.TaskResult("install nginx", "changed", true, "", nil)

	got := buf.String()
	if want := "  ✓ install nginx\n"; got != want {
		t.Fatalf("non-interactive output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "\033[K") {
		t.Fatalf("non-interactive output must not contain spinner control codes: %q", got)
	}
}

// TestTaskResult_Interactive verifies the spinner path: the line is redrawn in
// place (carriage return + clear-to-EOL) and ends with the final glyph + name.
func TestTaskResult_Interactive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.interactive = true // force interactive without a real TTY

	done := make(chan struct{})
	go func() {
		o.TaskStart("install nginx", "apt")
		// Let the spinner draw at least one frame.
		time.Sleep(120 * time.Millisecond)
		o.TaskResult("install nginx", "changed", true, "", nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TaskStart/TaskResult did not complete; spinner goroutine likely not joined")
	}

	got := buf.String()
	for _, want := range []string{"\r", "\033[K", "✓", "install nginx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("interactive output missing %q; got %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("interactive result line should end in newline; got %q", got)
	}
	// At least one animation frame should have been drawn.
	if !strings.ContainsAny(got, strings.Join(spinnerFrames, "")) {
		t.Fatalf("expected at least one spinner frame; got %q", got)
	}
}

// TestPrintfStopsSpinner ensures a stray printf (Info/Warn/etc.) halts an active
// spinner instead of racing with it.
func TestPrintfStopsSpinner(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.interactive = true

	o.startSpinner("working")
	o.Info("heads up")

	if o.spin != nil {
		t.Fatal("printf should have stopped the active spinner")
	}
}

// TestHostFacts_NonInteractive verifies plain output is unchanged: the banner,
// the "gathering facts" suffix, and the ✓ are printed on one line with no
// spinner control codes.
func TestHostFacts_NonInteractive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf) // not a terminal → interactive=false
	o.SetColor(false)

	o.HostStart("192.168.1.113", "ssh")
	o.HostFactsStart("192.168.1.113") // no-op in plain mode
	o.HostFactsResult("192.168.1.113", true, "")

	got := buf.String()
	if want := "\nHOST 192.168.1.113 [ssh] - gathering facts ✓\n"; got != want {
		t.Fatalf("plain host-facts output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "\033[K") {
		t.Fatalf("plain output must not contain spinner control codes: %q", got)
	}
}

// TestHostFacts_Interactive verifies the spinner path: the "gathering facts"
// line animates in place (carriage return + clear-to-EOL) and resolves to the
// full banner ending with the ✓.
func TestHostFacts_Interactive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.interactive = true // force interactive without a real TTY

	o.HostStart("192.168.1.113", "ssh")
	o.HostFactsStart("192.168.1.113")
	time.Sleep(120 * time.Millisecond) // let the spinner draw at least one frame
	o.HostFactsResult("192.168.1.113", true, "")

	got := buf.String()
	if !strings.Contains(got, "\r") || !strings.Contains(got, "\033[K") {
		t.Fatalf("interactive output should redraw in place: %q", got)
	}
	if !strings.Contains(got, "gathering facts") {
		t.Fatalf("expected the whole 'gathering facts' line: %q", got)
	}
	if !strings.Contains(got, "✓") {
		t.Fatalf("expected the final ✓: %q", got)
	}
	if !strings.HasSuffix(got, "\033[K\n") {
		t.Fatalf("final line should clear to EOL and end with a newline: %q", got)
	}
	// A spinner frame must have been drawn while gathering.
	if !strings.ContainsAny(got, strings.Join(spinnerFrames, "")) {
		t.Fatalf("expected a spinner frame in output: %q", got)
	}
}

// TestStartProgress_NonInteractive verifies the progress spinner is silent
// (no control codes) when the writer is not a terminal.
func TestStartProgress_NonInteractive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf) // not a terminal
	stop := o.StartProgress(func() string { return "planning 1/2 hosts" })
	stop()
	if buf.Len() != 0 {
		t.Fatalf("non-interactive progress must produce no output, got %q", buf.String())
	}
}

// TestStartProgress_Interactive verifies the progress spinner draws the dynamic
// label and clears the line when stopped.
func TestStartProgress_Interactive(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.interactive = true

	n := 0
	stop := o.StartProgress(func() string {
		n++
		return "gathering facts + planning 1/2 hosts"
	})
	time.Sleep(120 * time.Millisecond) // let it draw a frame or two
	stop()

	got := buf.String()
	if !strings.Contains(got, "gathering facts + planning 1/2 hosts") {
		t.Errorf("expected the dynamic label in output: %q", got)
	}
	if !strings.HasSuffix(got, "\r\033[K") {
		t.Errorf("progress should clear the line on stop: %q", got)
	}
	if n == 0 {
		t.Error("labelFn should have been called at least once")
	}
}

// TestTaskResult_Detail verifies the result-detail line: changed/failed tasks
// show their message by default; routine ok/skipped stay terse unless verbose.
func TestTaskResult_Detail(t *testing.T) {
	render := func(status, message string, verbose bool) string {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)
		o.SetVerbose(verbose)
		o.TaskResult("do thing", status, status == "changed", message, nil)
		return buf.String()
	}

	if got := render("changed", "installed nginx", false); !strings.Contains(got, "→ installed nginx") {
		t.Errorf("changed task should show detail by default: %q", got)
	}
	if got := render("failed", "boom", false); !strings.Contains(got, "→ boom") {
		t.Errorf("failed task should show detail by default: %q", got)
	}
	if got := render("ok", "already present", false); strings.Contains(got, "already present") {
		t.Errorf("ok task should be terse by default: %q", got)
	}
	if got := render("skipped", "when not met", false); strings.Contains(got, "when not met") {
		t.Errorf("skipped task should be terse by default: %q", got)
	}
	if got := render("ok", "already present", true); !strings.Contains(got, "→ already present") {
		t.Errorf("verbose should show ok detail: %q", got)
	}
	// Empty message never adds a detail line.
	if got := render("changed", "", false); strings.Contains(got, "→") {
		t.Errorf("empty message should add no detail line: %q", got)
	}
}

func TestGlyph_StyleDispatch(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetSpinnerStyle("flower")
	if !strings.Contains(o.glyph(0)+o.glyph(5), "❋") {
		t.Errorf("flower style should render a bloom glyph, got %q", o.glyph(5))
	}
	o.SetSpinnerStyle("")
	if strings.Contains(o.glyph(0), "❋") {
		t.Errorf("default style should render braille: %q", o.glyph(0))
	}
}
