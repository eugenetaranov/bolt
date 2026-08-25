package unarchive

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

func TestFormatDetection(t *testing.T) {
	cases := []struct {
		src      string
		wantTool string
		wantFlag string
	}{
		{"/a.tar", "tar", ""},
		{"/a.tar.gz", "tar", "-z"},
		{"/a.tgz", "tar", "-z"},
		{"/a.tar.bz2", "tar", "-j"},
		{"/a.tar.xz", "tar", "-J"},
		{"/a.tar.zst", "tar", "--zstd"},
		{"/a.zip", "unzip", ""},
	}
	for _, c := range cases {
		cfg, err := parseConfig(map[string]any{"src": c.src, "dest": "/d"})
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if cfg.tool != c.wantTool || cfg.tarDecomp != c.wantFlag {
			t.Errorf("%s: tool=%s flag=%q, want %s/%q", c.src, cfg.tool, cfg.tarDecomp, c.wantTool, c.wantFlag)
		}
	}
}

func TestParseConfigErrors(t *testing.T) {
	if _, err := parseConfig(map[string]any{"src": "/a.rar", "dest": "/d"}); err == nil {
		t.Error("expected error for unsupported format")
	}
	if _, err := parseConfig(map[string]any{"src": "/a.zip", "dest": "/d", "strip_components": 1}); err == nil {
		t.Error("expected error for strip_components with zip")
	}
}

func TestExtractCommand(t *testing.T) {
	cfg, _ := parseConfig(map[string]any{
		"src": "/tmp/app.tar.xz", "dest": "/opt/app", "strip_components": 1, "extra_opts": []any{"--no-same-owner"},
	})
	cmd := cfg.extractCommand()
	for _, want := range []string{"'tar'", "'-x'", "'-J'", "'-C' '/opt/app'", "--strip-components=1", "--no-same-owner", "'-f' '/tmp/app.tar.xz'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("extract command %q missing %q", cmd, want)
		}
	}

	zipCfg, _ := parseConfig(map[string]any{"src": "/tmp/a.zip", "dest": "/opt/a"})
	if zc := zipCfg.extractCommand(); !strings.Contains(zc, "'unzip' '-o'") || !strings.Contains(zc, "'-d' '/opt/a'") {
		t.Errorf("unexpected unzip command: %q", zc)
	}
}

func TestRunIdempotentCreates(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		if strings.Contains(cmd, "test -e") && strings.Contains(cmd, "/opt/app/bin/app") {
			return &connector.Result{ExitCode: 0} // creates exists
		}
		return &connector.Result{ExitCode: 0}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"src": "/tmp/app.tar.gz", "dest": "/opt/app", "creates": "/opt/app/bin/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected no change when creates exists")
	}
	if conn.find("'tar'", "'-x'") != "" {
		t.Errorf("should not extract when creates exists")
	}
}

func TestRunExtracts(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "test -e") && strings.Contains(cmd, "creates-marker"):
			return &connector.Result{ExitCode: 1} // creates absent
		case strings.Contains(cmd, "test -e") && strings.Contains(cmd, "/tmp/app.tar.gz"):
			return &connector.Result{ExitCode: 0} // src exists
		case strings.Contains(cmd, "command -v tar"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/bin/tar\n"}
		case strings.Contains(cmd, "stat"):
			return &connector.Result{ExitCode: 0, Stdout: "0755 root root\n"}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"src": "/tmp/app.tar.gz", "dest": "/opt/app", "creates": "/opt/app/creates-marker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected Changed=true")
	}
	if conn.find("mkdir -p", "/opt/app") == "" {
		t.Errorf("expected mkdir -p dest, got: %v", conn.cmds)
	}
	if conn.find("'tar'", "'-x'", "'-z'", "/tmp/app.tar.gz") == "" {
		t.Errorf("expected tar extraction, got: %v", conn.cmds)
	}
}

func TestRunToolMissing(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "test -e"):
			return &connector.Result{ExitCode: 0} // src exists, no creates set
		case strings.Contains(cmd, "command -v tar"):
			return &connector.Result{ExitCode: 1} // tar missing
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"src": "/tmp/app.tar.gz", "dest": "/opt/app",
	})
	if err == nil || !strings.Contains(err.Error(), "tar") {
		t.Fatalf("expected tar-missing error, got: %v", err)
	}
}

func TestCheckMode(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		return &connector.Result{ExitCode: 1} // creates absent
	}}
	r, err := (&Module{}).Check(context.Background(), conn, map[string]any{
		"src": "/tmp/a.zip", "dest": "/opt/a", "creates": "/opt/a/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.WouldChange {
		t.Errorf("expected WouldChange=true")
	}
	if conn.find("unzip") != "" {
		t.Errorf("check mode must not extract")
	}
}
