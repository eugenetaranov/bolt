package makemod

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/connector"
)

type mockConn struct {
	handler func(cmd string) *connector.Result
	cmds    []string
}

func (m *mockConn) Connect(ctx context.Context) error     { return nil }
func (m *mockConn) Close() error                          { return nil }
func (m *mockConn) String() string                        { return "mock" }
func (m *mockConn) SetSudo(enabled bool, password string) {}
func (m *mockConn) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	return nil
}
func (m *mockConn) Download(ctx context.Context, src string, dst io.Writer) error { return nil }
func (m *mockConn) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	m.cmds = append(m.cmds, cmd)
	if m.handler != nil {
		if r := m.handler(cmd); r != nil {
			return r, nil
		}
	}
	return &connector.Result{ExitCode: 0}, nil
}
func (m *mockConn) find(subs ...string) string {
	for _, c := range m.cmds {
		ok := true
		for _, s := range subs {
			if !strings.Contains(c, s) {
				ok = false
				break
			}
		}
		if ok {
			return c
		}
	}
	return ""
}

func TestParseConfigGuards(t *testing.T) {
	if _, err := parseConfig(map[string]any{"creates": "/x"}); err == nil {
		t.Error("expected error for missing chdir")
	}
	if _, err := parseConfig(map[string]any{"chdir": "/s"}); err == nil {
		t.Error("expected error when no guard given")
	}
	if _, err := parseConfig(map[string]any{"chdir": "/s", "creates": "/x", "unless": "true"}); err == nil {
		t.Error("expected error when both guards given")
	}
	if _, err := parseConfig(map[string]any{"chdir": "/s", "creates": "/x", "install": 42}); err == nil {
		t.Error("expected error for non-bool/string install")
	}
}

func TestInstallResolution(t *testing.T) {
	c, _ := parseConfig(map[string]any{"chdir": "/s", "creates": "/x", "install": true})
	if c.install != "make install" {
		t.Errorf("install:true => %q", c.install)
	}
	c, _ = parseConfig(map[string]any{"chdir": "/s", "creates": "/x", "install": "make DESTDIR=/o install"})
	if c.install != "make DESTDIR=/o install" {
		t.Errorf("install string => %q", c.install)
	}
	c, _ = parseConfig(map[string]any{"chdir": "/s", "creates": "/x"})
	if c.install != "" {
		t.Errorf("no install => %q", c.install)
	}
}

func TestBuildCommand(t *testing.T) {
	c, _ := parseConfig(map[string]any{
		"chdir": "/usr/local/src/app", "configure": "./configure --prefix=/usr/local",
		"target": "all", "jobs": 4, "install": true, "creates": "/usr/local/bin/app",
		"env": map[string]any{"CFLAGS": "-O2"},
	})
	cmd := c.buildCommand()
	for _, want := range []string{
		"cd '/usr/local/src/app'",
		"export CFLAGS='-O2'",
		"./configure --prefix=/usr/local",
		"make -j4 all",
		"make install",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("build command %q missing %q", cmd, want)
		}
	}
	// Order: configure before make before install.
	if strings.Index(cmd, "configure") > strings.Index(cmd, "make -j4") {
		t.Errorf("configure should precede make: %q", cmd)
	}
	if strings.Index(cmd, "make -j4") > strings.Index(cmd, "make install") {
		t.Errorf("make should precede install: %q", cmd)
	}
}

func TestRunGuardSatisfiedCreates(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		if strings.Contains(cmd, "test -e") {
			return &connector.Result{ExitCode: 0} // creates exists
		}
		return &connector.Result{ExitCode: 0}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{"chdir": "/s", "creates": "/usr/local/bin/app"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected no change when creates exists")
	}
	if conn.find("cd '/s'", "make") != "" {
		t.Errorf("should not build when guard satisfied")
	}
}

func TestRunGuardSatisfiedUnless(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		if strings.Contains(cmd, "test -x /usr/local/bin/app") {
			return &connector.Result{ExitCode: 0} // unless passes
		}
		return &connector.Result{ExitCode: 0}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{"chdir": "/s", "unless": "test -x /usr/local/bin/app"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected no change when unless passes")
	}
}

func TestRunBuilds(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		if strings.Contains(cmd, "test -e") {
			return &connector.Result{ExitCode: 1} // creates absent -> build
		}
		return &connector.Result{ExitCode: 0}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chdir": "/usr/local/src/app", "install": true, "creates": "/usr/local/bin/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected Changed=true")
	}
	if conn.find("cd '/usr/local/src/app'", "make", "make install") == "" {
		t.Errorf("expected build command, got: %v", conn.cmds)
	}
}

func TestCheckMode(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		return &connector.Result{ExitCode: 1} // creates absent
	}}
	r, err := (&Module{}).Check(context.Background(), conn, map[string]any{"chdir": "/s", "creates": "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.WouldChange {
		t.Errorf("expected WouldChange=true")
	}
	if conn.find("make") != "" {
		t.Errorf("check mode must not build")
	}
}
