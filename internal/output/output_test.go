package output

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tackhq/tack/internal/playbook"
)

func TestNewOutput(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)

	if o == nil {
		t.Fatal("expected non-nil Output")
	}
	if o.w != &buf {
		t.Error("writer not set correctly")
	}
	if !o.useColor {
		t.Error("expected useColor to be true by default")
	}
}

func TestSetColor(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)

	o.SetColor(false)
	if o.useColor {
		t.Error("expected useColor to be false")
	}

	o.SetColor(true)
	if !o.useColor {
		t.Error("expected useColor to be true")
	}
}

func TestSetDebug(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)

	o.SetDebug(true)
	if !o.debug {
		t.Error("expected debug to be true")
	}

	o.SetDebug(false)
	if o.debug {
		t.Error("expected debug to be false")
	}
}

func TestColorOutput(t *testing.T) {
	t.Run("color enabled", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(true)

		result := o.color(colorGreen, "test")
		if !strings.Contains(result, "\033[32m") {
			t.Error("expected color code in output")
		}
		if !strings.Contains(result, "\033[0m") {
			t.Error("expected reset code in output")
		}
	})

	t.Run("color disabled", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)

		result := o.color(colorGreen, "test")
		if result != "test" {
			t.Errorf("expected plain 'test', got %q", result)
		}
	})
}

func TestTaskResult(t *testing.T) {
	tests := []struct {
		name     string
		taskName string
		status   string
		debug    bool
		message  string
		wantIn   []string
	}{
		{
			name:     "ok status",
			taskName: "Test Task",
			status:   "ok",
			debug:    false,
			message:  "",
			wantIn:   []string{"✓", "Test Task"},
		},
		{
			name:     "changed status",
			taskName: "Changed Task",
			status:   "changed",
			debug:    false,
			message:  "",
			wantIn:   []string{"✓", "Changed Task"},
		},
		{
			name:     "skipped status (shown with debug)",
			taskName: "Skipped Task",
			status:   "skipped",
			debug:    true, // skipped is hidden by default; debug/verbose reveals it
			message:  "",
			wantIn:   []string{"○", "Skipped Task"},
		},
		{
			name:     "failed status",
			taskName: "Failed Task",
			status:   "failed",
			debug:    false,
			message:  "",
			wantIn:   []string{"✗", "Failed Task"},
		},
		{
			name:     "debug with message",
			taskName: "Debug Task",
			status:   "ok",
			debug:    true,
			message:  "some details",
			wantIn:   []string{"✓", "Debug Task", "→", "some details"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf)
			o.SetColor(false)
			o.SetDebug(tt.debug)

			o.TaskResult(tt.taskName, tt.status, false, tt.message, nil)

			output := buf.String()
			for _, want := range tt.wantIn {
				if !strings.Contains(output, want) {
					t.Errorf("expected output to contain %q, got %q", want, output)
				}
			}
		})
	}
}

func TestInfo(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	o.Info("test %s %d", "message", 42)

	output := buf.String()
	if !strings.Contains(output, "INFO") {
		t.Error("expected INFO prefix")
	}
	if !strings.Contains(output, "test message 42") {
		t.Errorf("expected formatted message, got %q", output)
	}
}

func TestWarn(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	o.Warn("warning %s", "here")

	output := buf.String()
	if !strings.Contains(output, "WARN") {
		t.Error("expected WARN prefix")
	}
	if !strings.Contains(output, "warning here") {
		t.Errorf("expected formatted message, got %q", output)
	}
}

func TestError(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	o.Error("error: %v", "failed")

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Error("expected ERROR prefix")
	}
	if !strings.Contains(output, "error: failed") {
		t.Errorf("expected formatted message, got %q", output)
	}
}

func TestDebugOutput(t *testing.T) {
	t.Run("debug enabled", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)
		o.SetDebug(true)

		o.Debug("debug %s", "info")

		output := buf.String()
		if !strings.Contains(output, "DEBUG") {
			t.Error("expected DEBUG prefix when debug enabled")
		}
	})

	t.Run("debug disabled", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)
		o.SetDebug(false)

		o.Debug("debug %s", "info")

		output := buf.String()
		if output != "" {
			t.Errorf("expected empty output when debug disabled, got %q", output)
		}
	})
}

// mockStats implements the Stats interface for testing
type mockStats struct {
	ok, changed, failed, skipped int
	duration                     time.Duration
}

func (m *mockStats) GetOK() int                 { return m.ok }
func (m *mockStats) GetChanged() int            { return m.changed }
func (m *mockStats) GetFailed() int             { return m.failed }
func (m *mockStats) GetSkipped() int            { return m.skipped }
func (m *mockStats) GetDuration() time.Duration { return m.duration }

func TestPlaybookEnd(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	stats := &mockStats{
		ok:       5,
		changed:  3,
		failed:   1,
		skipped:  2,
		duration: 2500 * time.Millisecond,
	}

	o.PlaybookEnd(stats)

	output := buf.String()
	if !strings.Contains(output, "RECAP") {
		t.Error("expected RECAP in output")
	}
	if !strings.Contains(output, "ok=5") {
		t.Error("expected ok=5 in output")
	}
	if !strings.Contains(output, "changed=3") {
		t.Error("expected changed=3 in output")
	}
	if !strings.Contains(output, "failed=1") {
		t.Error("expected failed=1 in output")
	}
	if !strings.Contains(output, "skipped=2") {
		t.Error("expected skipped=2 in output")
	}
	if !strings.Contains(output, "2.50s") {
		t.Error("expected duration in output")
	}
}

func TestHostStartAndFactsResult(t *testing.T) {
	t.Run("success appends inline checkmark and newline", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)

		o.HostStart("web1.prod", "ssh")
		o.HostFactsResult("web1.prod", true, "")

		got := buf.String()
		want := "\nHOST web1.prod [ssh] - gathering facts ✓\n"
		if got != want {
			t.Errorf("HostStart+HostFactsResult: got %q, want %q", got, want)
		}
	})

	t.Run("failure appends cross and prints error on follow-up line", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)

		o.HostStart("web1", "ssh")
		o.HostFactsResult("web1", false, "connection refused")

		got := buf.String()
		if !strings.Contains(got, "HOST web1 [ssh] - gathering facts ✗\n") {
			t.Errorf("expected failed banner, got %q", got)
		}
		if !strings.Contains(got, "connection refused") {
			t.Errorf("expected error message in output, got %q", got)
		}
	})
}

func TestHostStartDoneClosesBanner(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	o.HostStart("web1", "ssh")
	o.HostStartDone("web1")

	got := buf.String()
	want := "\nHOST web1 [ssh]\n"
	if got != want {
		t.Errorf("HostStart+HostStartDone: got %q, want %q", got, want)
	}
}

func TestPlayStartOnlyEmitsForNamedPlay(t *testing.T) {
	t.Run("named play prints PLAY banner", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)

		o.PlayStart(&playbook.Play{Name: "Configure web servers", Hosts: []string{"web1", "web2"}})

		got := buf.String()
		if !strings.Contains(got, "PLAY Configure web servers") {
			t.Errorf("expected PLAY banner, got %q", got)
		}
	})

	t.Run("anonymous play emits nothing", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)

		o.PlayStart(&playbook.Play{Hosts: []string{"web1", "web2"}})

		if got := buf.String(); got != "" {
			t.Errorf("expected no output for anonymous play, got %q", got)
		}
	})
}

func TestPlayHosts(t *testing.T) {
	tests := []struct {
		name  string
		hosts []string
		want  string
	}{
		{"single host emits nothing", []string{"web1"}, ""},
		{"empty hosts emits nothing", nil, ""},
		{"two hosts inline", []string{"web1", "web2"}, "HOSTS web1, web2\n"},
		{"five hosts inline", []string{"web1", "web2", "web3", "web4", "web5"}, "HOSTS web1, web2, web3, web4, web5\n"},
		{"six hosts overflow", []string{"web1", "web2", "web3", "web4", "web5", "web6"}, "HOSTS web1, web2, web3, web4, web5 (and 1 more)\n"},
		{"twelve hosts overflow", []string{"web1", "web2", "web3", "web4", "web5", "web6", "web7", "web8", "web9", "web10", "web11", "web12"}, "HOSTS web1, web2, web3, web4, web5 (and 7 more)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(&buf)
			o.SetColor(false)

			o.PlayHosts(tt.hosts)

			if got := buf.String(); got != tt.want {
				t.Errorf("PlayHosts(%v) = %q, want %q", tt.hosts, got, tt.want)
			}
		})
	}
}

func TestSetDiff(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)

	o.SetDiff(true)
	if !o.diff {
		t.Error("expected diff to be true")
	}
	if !o.DiffEnabled() {
		t.Error("expected DiffEnabled() to be true")
	}

	o.SetDiff(false)
	if o.diff {
		t.Error("expected diff to be false")
	}
}

func TestDiffEnabledViaVerbose(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)

	o.SetVerbose(true)
	if !o.DiffEnabled() {
		t.Error("expected DiffEnabled() to be true when verbose is set")
	}
}

func TestDisplayPlan_DiffFlag(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.SetDiff(true)

	tasks := []PlannedTask{{
		Name:        "Copy config",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/etc/app.conf"},
		OldChecksum: "aaa",
		NewChecksum: "bbb",
		OldContent:  "line1\nline2\n",
		NewContent:  "line1\nline3\n",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	if !strings.Contains(out, "--- /etc/app.conf") {
		t.Error("expected old file path header in diff output")
	}
	if !strings.Contains(out, "+++ /etc/app.conf") {
		t.Error("expected new file path header in diff output")
	}
	if !strings.Contains(out, "-line2") {
		t.Error("expected removed line in diff output")
	}
	if !strings.Contains(out, "+line3") {
		t.Error("expected added line in diff output")
	}
}

func TestDisplayPlan_DiffVerboseBackwardCompat(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.SetVerbose(true) // verbose should still show diffs

	tasks := []PlannedTask{{
		Name:        "Copy config",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/etc/app.conf"},
		OldChecksum: "aaa",
		NewChecksum: "bbb",
		OldContent:  "old\n",
		NewContent:  "new\n",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	if !strings.Contains(out, "--- /etc/app.conf") {
		t.Error("expected diff output with --verbose flag")
	}
}

func TestDisplayPlan_NewFile(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.SetDiff(true)

	tasks := []PlannedTask{{
		Name:        "Create config",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/etc/new.conf"},
		NewChecksum: "abc123",
		NewContent:  "server=localhost\nport=8080\n",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	if !strings.Contains(out, "--- /dev/null") {
		t.Error("expected /dev/null for old path on new file")
	}
	if !strings.Contains(out, "+++ /etc/new.conf") {
		t.Error("expected new file path header")
	}
	if !strings.Contains(out, "+server=localhost") {
		t.Error("expected all lines as additions for new file")
	}
}

func TestDisplayPlan_BinaryFile(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.SetDiff(true)

	tasks := []PlannedTask{{
		Name:        "Copy binary",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/usr/bin/app"},
		OldChecksum: "aaa",
		NewChecksum: "bbb",
		OldContent:  "ELF\x00\x00\x00binary content",
		NewContent:  "ELF\x00\x00\x00new binary content",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	if !strings.Contains(out, "Binary files differ") {
		t.Error("expected 'Binary files differ' for binary content")
	}
}

func TestDisplayPlan_LargeFile(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.SetDiff(true)

	largeContent := strings.Repeat("x", 65*1024) // > 64KB

	tasks := []PlannedTask{{
		Name:        "Copy large file",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/tmp/large"},
		OldChecksum: "aaa",
		NewChecksum: "bbb",
		OldContent:  largeContent,
		NewContent:  largeContent + "extra",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	if !strings.Contains(out, "(file too large for diff)") {
		t.Error("expected '(file too large for diff)' for oversized content")
	}
}

func TestDisplayPlan_NoDiffFlag(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	// Neither diff nor verbose set

	tasks := []PlannedTask{{
		Name:        "Copy config",
		Module:      "copy",
		Status:      "will_change",
		Params:      map[string]any{"dest": "/etc/app.conf"},
		OldChecksum: "aaa",
		NewChecksum: "bbb",
		OldContent:  "old\n",
		NewContent:  "new\n",
	}}
	o.DisplayPlan(tasks, false)
	out := buf.String()

	// Should show checksums, not diff
	if strings.Contains(out, "---") {
		t.Error("expected no diff headers without --diff or --verbose")
	}
	if !strings.Contains(out, "old: aaa") {
		t.Error("expected old checksum in output")
	}
	if !strings.Contains(out, "new: bbb") {
		t.Error("expected new checksum in output")
	}
}

func TestIsBinary(t *testing.T) {
	if !isBinary("hello\x00world") {
		t.Error("expected binary detection for null byte")
	}
	if isBinary("hello world") {
		t.Error("expected non-binary for plain text")
	}
	if isBinary("") {
		t.Error("expected non-binary for empty string")
	}
}

func TestIsApproval(t *testing.T) {
	accepted := []string{"y", "Y", "yes", "Yes", "YES", "yEs"}
	for _, input := range accepted {
		if !IsApproval(input) {
			t.Errorf("expected %q to be accepted", input)
		}
	}

	rejected := []string{"", "n", "no", "No", "NO", "ok", "sure", "ye", "yess", " "}
	for _, input := range rejected {
		if IsApproval(input) {
			t.Errorf("expected %q to be rejected", input)
		}
	}
}

// withStdin temporarily replaces os.Stdin with a pipe whose read-end the
// test owns. Returns a cleanup function that restores the original Stdin.
func withStdin(t *testing.T, input string) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe write end: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = orig
		_ = r.Close()
	}
}

func TestPromptApproval_PromptIncludesTarget(t *testing.T) {
	cleanup := withStdin(t, "yes\n")
	defer cleanup()

	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false) // strip ANSI for predictable string matching

	if !o.PromptApproval("web1.prod (ssh)") {
		t.Error("expected approval for 'yes' input")
	}
	got := buf.String()
	if !strings.Contains(got, "Apply these changes to web1.prod (ssh)?") {
		t.Errorf("prompt should contain target; got %q", got)
	}
	if !strings.Contains(got, "(yes/no):") {
		t.Errorf("prompt should still ask (yes/no); got %q", got)
	}
}

func TestPromptApproval_NoInputReturnsFalse(t *testing.T) {
	cleanup := withStdin(t, "")
	defer cleanup()

	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)

	if o.PromptApproval("4 hosts (web1, web2, web3, web4)") {
		t.Error("expected non-approval when stdin is empty")
	}
	if !strings.Contains(buf.String(), "Apply these changes to 4 hosts (web1, web2, web3, web4)?") {
		t.Errorf("prompt should contain multi-host target; got %q", buf.String())
	}
}

// TestTaskResult_SkippedHidden verifies skipped task results are hidden during
// apply by default and revealed with verbose/debug.
func TestTaskResult_SkippedHidden(t *testing.T) {
	render := func(status string, verbose, debug bool) string {
		var buf bytes.Buffer
		o := New(&buf)
		o.SetColor(false)
		o.SetVerbose(verbose)
		o.SetDebug(debug)
		o.TaskResult("Some Task", status, false, "reason", nil)
		return buf.String()
	}

	for _, s := range []string{"skipped", "skipped (tag)", "skipped (role)"} {
		if got := render(s, false, false); got != "" {
			t.Errorf("%q should be hidden by default, got %q", s, got)
		}
		if got := render(s, true, false); !strings.Contains(got, "Some Task") {
			t.Errorf("%q should be shown with verbose, got %q", s, got)
		}
	}
	// Non-skipped results are always shown.
	if got := render("changed", false, false); !strings.Contains(got, "Some Task") {
		t.Errorf("changed task must always show, got %q", got)
	}
}
