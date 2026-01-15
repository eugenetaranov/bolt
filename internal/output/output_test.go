package output

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
			name:     "skipped status",
			taskName: "Skipped Task",
			status:   "skipped",
			debug:    false,
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

			o.TaskResult(tt.taskName, tt.status, false, tt.message)

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

func (m *mockStats) GetOK() int              { return m.ok }
func (m *mockStats) GetChanged() int         { return m.changed }
func (m *mockStats) GetFailed() int          { return m.failed }
func (m *mockStats) GetSkipped() int         { return m.skipped }
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
