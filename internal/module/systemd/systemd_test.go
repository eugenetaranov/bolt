package systemd

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/connector"
)

type mockConnector struct {
	commands map[string]*connector.Result
	executed []string
}

func newMockConnector() *mockConnector {
	m := &mockConnector{commands: make(map[string]*connector.Result)}
	// systemctl must appear available for checkSystemd to pass.
	m.onCmd("command -v systemctl", "/usr/bin/systemctl", 0)
	// daemon-reload succeeds by default.
	m.onCmd("systemctl daemon-reload", "", 0)
	return m
}

func (m *mockConnector) Connect(ctx context.Context) error     { return nil }
func (m *mockConnector) Close() error                          { return nil }
func (m *mockConnector) String() string                        { return "mock" }
func (m *mockConnector) SetSudo(enabled bool, password string) {}
func (m *mockConnector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	return nil
}
func (m *mockConnector) Download(ctx context.Context, src string, dst io.Writer) error {
	return nil
}

func (m *mockConnector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	m.executed = append(m.executed, cmd)
	if r, ok := m.commands[cmd]; ok {
		return r, nil
	}
	for pattern, r := range m.commands {
		if strings.Contains(cmd, pattern) {
			return r, nil
		}
	}
	return &connector.Result{ExitCode: 1, Stderr: "command not found"}, nil
}

func (m *mockConnector) onCmd(cmd string, stdout string, exitCode int) {
	m.commands[cmd] = &connector.Result{Stdout: stdout, ExitCode: exitCode}
}

func (m *mockConnector) ran(substr string) bool {
	for _, c := range m.executed {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// TestDaemonReloadOnly verifies that a task with only daemon_reload: true (and
// no name) succeeds and runs `systemctl daemon-reload` without touching any unit.
func TestDaemonReloadOnly(t *testing.T) {
	ctx := context.Background()
	conn := newMockConnector()

	res, err := (&Module{}).Run(ctx, conn, map[string]any{"daemon_reload": true})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res == nil {
		t.Fatal("expected a result")
	}
	if !conn.ran("systemctl daemon-reload") {
		t.Errorf("expected daemon-reload to run; executed: %v", conn.executed)
	}
	// No unit should be touched: no start/stop/enable/is-active/etc.
	for _, forbidden := range []string{"is-active", "is-enabled", "start", "stop", "restart", "reload ", "enable", "mask"} {
		if conn.ran(forbidden) {
			t.Errorf("did not expect %q to run for daemon-reload-only; executed: %v", forbidden, conn.executed)
		}
	}
}

// TestDaemonReloadOnlyCheck verifies the check-mode branch handles a missing name.
func TestDaemonReloadOnlyCheck(t *testing.T) {
	ctx := context.Background()
	conn := newMockConnector()

	res, err := (&Module{}).Check(ctx, conn, map[string]any{"daemon_reload": true})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res.WouldChange {
		t.Errorf("daemon-reload alone should not be reported as a state change: %+v", res)
	}
	if !strings.Contains(res.Message, "daemon-reload") {
		t.Errorf("expected message to mention daemon-reload, got %q", res.Message)
	}
}

// TestStateRequiresName verifies name is still required when a state-changing
// action is requested.
func TestStateRequiresName(t *testing.T) {
	ctx := context.Background()

	cases := []map[string]any{
		{"state": "started"},
		{"enabled": true},
		{"masked": true},
		{"state": "started", "daemon_reload": true},
	}
	for _, params := range cases {
		conn := newMockConnector()
		_, err := (&Module{}).Run(ctx, conn, params)
		if err == nil {
			t.Errorf("expected error for params %v, got nil", params)
			continue
		}
		if !strings.Contains(err.Error(), "name") {
			t.Errorf("expected a missing-name error for %v, got %v", params, err)
		}
	}
}

// TestNameWithDaemonReloadUnchanged verifies the existing behavior of a named
// unit combined with daemon_reload is preserved: daemon-reload runs, and with no
// state change requested the result is Unchanged.
func TestNameWithDaemonReloadUnchanged(t *testing.T) {
	ctx := context.Background()
	conn := newMockConnector()

	res, err := (&Module{}).Run(ctx, conn, map[string]any{
		"name":          "nginx",
		"daemon_reload": true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res.Changed {
		t.Errorf("daemon_reload is a side-effect, not a state change; expected Changed=false, got %+v", res)
	}
	if !conn.ran("systemctl daemon-reload") {
		t.Errorf("expected daemon-reload to run; executed: %v", conn.executed)
	}
}

// TestNameWithDaemonReloadAndState verifies a named unit with daemon_reload and a
// state change still starts the unit and reports changed.
func TestNameWithDaemonReloadAndState(t *testing.T) {
	ctx := context.Background()
	conn := newMockConnector()
	conn.onCmd("systemctl is-active 'nginx.service'", "inactive\n", 3)
	conn.onCmd("systemctl start 'nginx.service'", "", 0)

	res, err := (&Module{}).Run(ctx, conn, map[string]any{
		"name":          "nginx",
		"state":         "started",
		"daemon_reload": true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Changed {
		t.Errorf("expected Changed=true, got %+v", res)
	}
	if !conn.ran("systemctl daemon-reload") {
		t.Errorf("expected daemon-reload to run; executed: %v", conn.executed)
	}
	if !conn.ran("systemctl start 'nginx.service'") {
		t.Errorf("expected the unit to be started; executed: %v", conn.executed)
	}
}
