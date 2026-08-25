package geturl

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

func TestParseConfigValidation(t *testing.T) {
	cases := map[string]map[string]any{
		"missing url":  {"dest": "/x"},
		"missing dest": {"url": "http://x"},
		"unknown algo": {"url": "http://x", "dest": "/x", "checksum": "crc32:abc"},
		"empty hash":   {"url": "http://x", "dest": "/x", "checksum": "sha256:"},
	}
	for name, p := range cases {
		if _, err := parseConfig(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// Bare hash is treated as sha256.
	cfg, err := parseConfig(map[string]any{"url": "http://x", "dest": "/x", "checksum": "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.algo != "sha256" || cfg.hash != "abc123" {
		t.Errorf("bare hash should be sha256, got %s:%s", cfg.algo, cfg.hash)
	}
}

func TestRunDownloadsNewFile(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v curl"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/bin/curl\n"}
		case strings.Contains(cmd, "stat"):
			return &connector.Result{ExitCode: 0, Stdout: "0644 root root\n"}
		case strings.Contains(cmd, ".tack-download") && strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "abc\n"} // post-download matches
		case strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "NO_FILE\n"} // dest pre-check: absent
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"url": "https://example.com/f.bin", "dest": "/opt/f.bin", "checksum": "sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected Changed=true")
	}
	if conn.find("'curl'", "'-o'", "/opt/f.bin.tack-download", "https://example.com/f.bin") == "" {
		t.Errorf("expected curl download command, got: %v", conn.cmds)
	}
	if conn.find("mv -f", "/opt/f.bin.tack-download", "/opt/f.bin") == "" {
		t.Errorf("expected mv into place, got: %v", conn.cmds)
	}
}

func TestRunIdempotentChecksumMatch(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "stat"):
			return &connector.Result{ExitCode: 0, Stdout: "0644 root root\n"}
		case strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "abc\n"} // dest already matches
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"url": "https://x/f", "dest": "/opt/f", "checksum": "sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected Changed=false when checksum already matches")
	}
	if conn.find("'curl'", "'-o'") != "" {
		t.Errorf("should not download when already satisfied")
	}
}

func TestRunChecksumMismatch(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v curl"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/bin/curl\n"}
		case strings.Contains(cmd, ".tack-download") && strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "deadbeef\n"} // wrong
		case strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "NO_FILE\n"}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"url": "https://x/f", "dest": "/opt/f", "checksum": "sha256:abc",
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
	if conn.find("rm -f", ".tack-download") == "" {
		t.Errorf("expected temp file cleanup on mismatch, got: %v", conn.cmds)
	}
}

func TestRunForceRedownloads(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v curl"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/bin/curl\n"}
		case strings.Contains(cmd, "stat"):
			return &connector.Result{ExitCode: 0, Stdout: "0644 root root\n"}
		case strings.Contains(cmd, ".tack-download") && strings.Contains(cmd, "sha256sum"):
			return &connector.Result{ExitCode: 0, Stdout: "abc\n"}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"url": "https://x/f", "dest": "/opt/f", "checksum": "sha256:abc", "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || conn.find("'curl'", "'-o'") == "" {
		t.Errorf("force should always download, got changed=%v cmds=%v", res.Changed, conn.cmds)
	}
}

func TestRunNoDownloader(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v curl"), strings.Contains(cmd, "command -v wget"):
			return &connector.Result{ExitCode: 1}
		case strings.Contains(cmd, "NO_FILE"): // GetRemoteChecksum existence probe
			return &connector.Result{ExitCode: 0, Stdout: "NO_FILE\n"}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"url": "https://x/f", "dest": "/opt/f", // no checksum
	})
	if err == nil || !strings.Contains(err.Error(), "curl") {
		t.Fatalf("expected no-downloader error, got: %v", err)
	}
}

func TestCheckMode(t *testing.T) {
	// Would download: dest absent.
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		return &connector.Result{ExitCode: 0, Stdout: "NO_FILE\n"}
	}}
	r, err := (&Module{}).Check(context.Background(), conn, map[string]any{"url": "https://x/f", "dest": "/opt/f", "checksum": "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.WouldChange {
		t.Errorf("expected WouldChange=true for missing dest")
	}
}
