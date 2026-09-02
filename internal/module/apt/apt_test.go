package apt

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/connector"
)

// mockConn records executed commands and returns canned responses per matcher.
type mockConn struct {
	cmds     []string
	handlers []handler
}

type handler struct {
	match string
	res   *connector.Result
}

func (m *mockConn) on(match, stdout string, exit int) *mockConn {
	m.handlers = append(m.handlers, handler{match: match, res: &connector.Result{Stdout: stdout, ExitCode: exit}})
	return m
}

func (m *mockConn) Connect(context.Context) error { return nil }
func (m *mockConn) Close() error                  { return nil }
func (m *mockConn) String() string                { return "mock" }
func (m *mockConn) SetSudo(bool, string)          {}
func (m *mockConn) Upload(context.Context, io.Reader, string, uint32) error { return nil }
func (m *mockConn) Download(context.Context, string, io.Writer) error       { return nil }
func (m *mockConn) Execute(_ context.Context, cmd string) (*connector.Result, error) {
	m.cmds = append(m.cmds, cmd)
	for _, h := range m.handlers {
		if strings.Contains(cmd, h.match) {
			return h.res, nil
		}
	}
	return &connector.Result{ExitCode: 0}, nil
}

func (m *mockConn) find(sub string) string {
	for _, c := range m.cmds {
		if strings.Contains(c, sub) {
			return c
		}
	}
	return ""
}

func baseMock() *mockConn {
	m := &mockConn{}
	m.on("command -v apt-get", "/usr/bin/apt-get", 0)
	return m
}

func TestApt_VersionPin_InstallsExactVersion(t *testing.T) {
	m := baseMock()
	// nginx installed at an older version → pin should reinstall the exact one.
	m.on("dpkg-query", "nginx|install ok installed|1.20.0\n", 0)

	mod := &Module{}
	res, err := mod.Run(context.Background(), m, map[string]any{
		"name":            "nginx=1.24.0",
		"default_release": "bookworm-backports",
		"allow_downgrade": true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Changed {
		t.Fatalf("expected changed (version mismatch), got %q", res.Message)
	}
	install := m.find("apt-get install")
	if !strings.Contains(install, "'nginx=1.24.0'") {
		t.Errorf("install should pin the version, got: %s", install)
	}
	if !strings.Contains(install, "-t 'bookworm-backports'") {
		t.Errorf("install should pass default_release, got: %s", install)
	}
	if !strings.Contains(install, "--allow-downgrades") {
		t.Errorf("install should pass allow-downgrades, got: %s", install)
	}
}

func TestApt_VersionPin_AlreadyAtVersion_NoChange(t *testing.T) {
	m := baseMock()
	m.on("dpkg-query", "nginx|install ok installed|1.24.0\n", 0)

	mod := &Module{}
	res, err := mod.Run(context.Background(), m, map[string]any{"name": "nginx=1.24.0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Changed {
		t.Errorf("expected no change when already at pinned version, got %q", res.Message)
	}
	if m.find("apt-get install") != "" {
		t.Errorf("should not install when already at pinned version")
	}
}

func TestApt_Hold_MarksHeldIdempotently(t *testing.T) {
	m := baseMock()
	m.on("dpkg-query", "nginx|install ok installed|1.24.0\n", 0)
	m.on("apt-mark showhold", "", 0) // nothing held yet

	mod := &Module{}
	res, err := mod.Run(context.Background(), m, map[string]any{"name": "nginx", "hold": true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Changed {
		t.Fatalf("expected change (newly held), got %q", res.Message)
	}
	if h := m.find("apt-mark hold"); !strings.Contains(h, "'nginx'") {
		t.Errorf("expected apt-mark hold nginx, got: %q", h)
	}
}

func TestApt_Hold_AlreadyHeld_NoChange(t *testing.T) {
	m := baseMock()
	m.on("dpkg-query", "nginx|install ok installed|1.24.0\n", 0)
	m.on("apt-mark showhold", "nginx\n", 0)

	mod := &Module{}
	res, err := mod.Run(context.Background(), m, map[string]any{"name": "nginx", "hold": true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Changed {
		t.Errorf("expected no change when already held, got %q", res.Message)
	}
}

// Without the hold key, existing hold state must not be touched.
func TestApt_HoldUnset_LeavesHoldsAlone(t *testing.T) {
	m := baseMock()
	m.on("dpkg-query", "nginx|install ok installed|1.24.0\n", 0)
	m.on("apt-mark showhold", "nginx\n", 0)

	mod := &Module{}
	_, err := mod.Run(context.Background(), m, map[string]any{"name": "nginx"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.find("apt-mark hold") != "" || m.find("apt-mark unhold") != "" {
		t.Errorf("hold state should be untouched when 'hold' is unset")
	}
}
