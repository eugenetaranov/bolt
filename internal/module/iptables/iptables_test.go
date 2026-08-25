package iptables

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/connector"
)

// mockConn records executed commands and returns programmed results via a
// handler. When the handler returns nil for a command, a success result is used.
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

// linuxAvailable is a handler where iptables is available and Linux.
func availHandler(ruleExitCode int) func(string) *connector.Result {
	return func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v iptables"),
			strings.Contains(cmd, "command -v ip6tables"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/sbin/iptables\n"}
		case strings.Contains(cmd, "'-C'"):
			return &connector.Result{ExitCode: ruleExitCode}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}
}

func TestName(t *testing.T) {
	if (&Module{}).Name() != "iptables" {
		t.Fatalf("unexpected name")
	}
}

func TestRunPresentAddsRule(t *testing.T) {
	conn := &mockConn{handler: availHandler(1)} // rule absent
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain":            "INPUT",
		"protocol":         "tcp",
		"destination_port": 22, // int, YAML-style
		"jump":             "ACCEPT",
		"comment":          "allow ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected Changed=true")
	}
	add := conn.find("'-A'", "'INPUT'", "'--dport'", "'22'", "'-j'", "'ACCEPT'")
	if add == "" {
		t.Fatalf("expected an -A command with the rule spec, got: %v", conn.cmds)
	}
	if !strings.Contains(add, "'-m' 'comment' '--comment' 'allow ssh'") {
		t.Errorf("expected comment tagging in: %s", add)
	}
	if !strings.Contains(add, "'-w'") {
		t.Errorf("expected -w lock flag in: %s", add)
	}
}

func TestRunPresentIdempotent(t *testing.T) {
	conn := &mockConn{handler: availHandler(0)} // rule present
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "protocol": "tcp", "destination_port": "22", "jump": "ACCEPT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected Changed=false when rule already present")
	}
	if conn.find("'-A'") != "" {
		t.Errorf("should not add when rule exists")
	}
}

func TestRunAbsentRemovesRule(t *testing.T) {
	conn := &mockConn{handler: availHandler(0)} // rule present
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "protocol": "tcp", "destination_port": "22", "jump": "ACCEPT",
		"state": "absent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected Changed=true")
	}
	if conn.find("'-D'", "'INPUT'") == "" {
		t.Errorf("expected a -D command, got: %v", conn.cmds)
	}
}

func TestRunAbsentIdempotent(t *testing.T) {
	conn := &mockConn{handler: availHandler(1)} // rule absent
	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "jump": "DROP", "state": "absent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("expected Changed=false when rule already absent")
	}
}

func TestRunInsertWithRuleNum(t *testing.T) {
	conn := &mockConn{handler: availHandler(1)}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "jump": "ACCEPT", "action": "insert", "rule_num": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.find("'-I'", "'INPUT'", "'3'") == "" {
		t.Errorf("expected -I INPUT 3, got: %v", conn.cmds)
	}
}

func TestRunIPv6UsesIp6tables(t *testing.T) {
	conn := &mockConn{handler: availHandler(1)}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "protocol": "tcp", "destination_port": "22", "jump": "ACCEPT",
		"ip_version": "ipv6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.find("'ip6tables'", "'-A'") == "" {
		t.Errorf("expected ip6tables invocation, got: %v", conn.cmds)
	}
}

func TestRunNatTable(t *testing.T) {
	conn := &mockConn{handler: availHandler(1)}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "POSTROUTING", "table": "nat", "jump": "MASQUERADE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.find("'-t'", "'nat'", "'MASQUERADE'") == "" {
		t.Errorf("expected -t nat in commands, got: %v", conn.cmds)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]map[string]any{
		"missing chain":     {"jump": "ACCEPT"},
		"bad ip_version":    {"chain": "INPUT", "ip_version": "ipv5"},
		"bad state":         {"chain": "INPUT", "state": "enabled"},
		"bad action":        {"chain": "INPUT", "action": "prepend"},
		"bad table":         {"chain": "INPUT", "table": "bogus"},
		"port w/o protocol": {"chain": "INPUT", "destination_port": 80},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			conn := &mockConn{handler: availHandler(1)}
			if _, err := (&Module{}).Run(context.Background(), conn, params); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestRunNonLinuxTarget(t *testing.T) {
	conn := &mockConn{handler: func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v iptables"):
			return &connector.Result{ExitCode: 1} // not found
		case strings.Contains(cmd, "uname -s"):
			return &connector.Result{ExitCode: 0, Stdout: "Darwin\n"}
		default:
			return &connector.Result{ExitCode: 0}
		}
	}}
	_, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"chain": "INPUT", "jump": "ACCEPT",
	})
	if err == nil || !strings.Contains(err.Error(), "Linux-only") {
		t.Fatalf("expected Linux-only error, got: %v", err)
	}
}

func TestCheckMode(t *testing.T) {
	t.Run("would add", func(t *testing.T) {
		conn := &mockConn{handler: availHandler(1)}
		r, err := (&Module{}).Check(context.Background(), conn, map[string]any{"chain": "INPUT", "jump": "ACCEPT"})
		if err != nil {
			t.Fatal(err)
		}
		if !r.WouldChange {
			t.Errorf("expected WouldChange=true")
		}
		if conn.find("'-A'") != "" {
			t.Errorf("check mode must not modify the ruleset")
		}
	})
	t.Run("no change", func(t *testing.T) {
		conn := &mockConn{handler: availHandler(0)}
		r, err := (&Module{}).Check(context.Background(), conn, map[string]any{"chain": "INPUT", "jump": "ACCEPT"})
		if err != nil {
			t.Fatal(err)
		}
		if r.WouldChange {
			t.Errorf("expected WouldChange=false")
		}
	})
	t.Run("would remove", func(t *testing.T) {
		conn := &mockConn{handler: availHandler(0)}
		r, err := (&Module{}).Check(context.Background(), conn, map[string]any{"chain": "INPUT", "jump": "ACCEPT", "state": "absent"})
		if err != nil {
			t.Fatal(err)
		}
		if !r.WouldChange {
			t.Errorf("expected WouldChange=true")
		}
	})
}

func TestClassifyDistro(t *testing.T) {
	cases := []struct {
		osRelease string
		want      string
	}{
		{"ID=ubuntu\nID_LIKE=debian\n", "debian"},
		{"ID=debian\n", "debian"},
		{"ID=fedora\n", "rhel"},
		{`ID="centos"` + "\n" + `ID_LIKE="rhel fedora"` + "\n", "rhel"},
		{"ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", "rhel"},
		{"ID=arch\n", "arch"},
		{"ID=manjaro\nID_LIKE=arch\n", "arch"},
		{"ID=alpine\n", "unknown"},
	}
	for _, c := range cases {
		if got := classifyDistro(c.osRelease); got != c.want {
			t.Errorf("classifyDistro(%q) = %q, want %q", c.osRelease, got, c.want)
		}
	}
}

// persistHandler makes iptables available, the rule absent, returns the given
// os-release, and reports whether netfilter-persistent is available.
func persistHandler(osRelease string, netfilterAvail bool) func(string) *connector.Result {
	return func(cmd string) *connector.Result {
		switch {
		case strings.Contains(cmd, "command -v netfilter-persistent"):
			if netfilterAvail {
				return &connector.Result{ExitCode: 0, Stdout: "/usr/sbin/netfilter-persistent\n"}
			}
			return &connector.Result{ExitCode: 1}
		case strings.Contains(cmd, "command -v iptables"):
			return &connector.Result{ExitCode: 0, Stdout: "/usr/sbin/iptables\n"}
		case strings.Contains(cmd, "cat /etc/os-release"):
			return &connector.Result{ExitCode: 0, Stdout: osRelease}
		case strings.Contains(cmd, "'-C'"):
			return &connector.Result{ExitCode: 1} // absent -> a change happens
		default:
			return &connector.Result{ExitCode: 0}
		}
	}
}

func TestPersistence(t *testing.T) {
	base := map[string]any{"chain": "INPUT", "jump": "ACCEPT", "persist": true}

	t.Run("debian netfilter-persistent", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=ubuntu\nID_LIKE=debian\n", true)}
		if _, err := (&Module{}).Run(context.Background(), conn, base); err != nil {
			t.Fatal(err)
		}
		if conn.find("netfilter-persistent save") == "" {
			t.Errorf("expected netfilter-persistent save, got: %v", conn.cmds)
		}
		if conn.find("rules.v4") != "" {
			t.Errorf("should not fall back to save file when netfilter-persistent is present")
		}
	})

	t.Run("debian save-file fallback", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=debian\n", false)}
		if _, err := (&Module{}).Run(context.Background(), conn, base); err != nil {
			t.Fatal(err)
		}
		if conn.find("iptables-save", "/etc/iptables/rules.v4") == "" {
			t.Errorf("expected save to /etc/iptables/rules.v4, got: %v", conn.cmds)
		}
	})

	t.Run("rhel sysconfig", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=fedora\n", false)}
		if _, err := (&Module{}).Run(context.Background(), conn, base); err != nil {
			t.Fatal(err)
		}
		if conn.find("iptables-save", "/etc/sysconfig/iptables") == "" {
			t.Errorf("expected save to /etc/sysconfig/iptables, got: %v", conn.cmds)
		}
	})

	t.Run("arch rules file", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=arch\n", false)}
		if _, err := (&Module{}).Run(context.Background(), conn, base); err != nil {
			t.Fatal(err)
		}
		if conn.find("iptables-save", "/etc/iptables/iptables.rules") == "" {
			t.Errorf("expected save to /etc/iptables/iptables.rules, got: %v", conn.cmds)
		}
	})

	t.Run("unknown distro warns", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=alpine\n", false)}
		res, err := (&Module{}).Run(context.Background(), conn, base)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Message, "not persisted") {
			t.Errorf("expected a not-persisted warning, got: %q", res.Message)
		}
	})

	t.Run("ipv6 save-file path", func(t *testing.T) {
		conn := &mockConn{handler: persistHandler("ID=debian\n", false)}
		params := map[string]any{"chain": "INPUT", "jump": "ACCEPT", "persist": true, "ip_version": "ipv6"}
		if _, err := (&Module{}).Run(context.Background(), conn, params); err != nil {
			t.Fatal(err)
		}
		if conn.find("ip6tables-save", "/etc/iptables/rules.v6") == "" {
			t.Errorf("expected ip6tables-save to rules.v6, got: %v", conn.cmds)
		}
	})

	t.Run("no persistence when unchanged", func(t *testing.T) {
		conn := &mockConn{handler: func(cmd string) *connector.Result {
			switch {
			case strings.Contains(cmd, "command -v iptables"):
				return &connector.Result{ExitCode: 0, Stdout: "/usr/sbin/iptables\n"}
			case strings.Contains(cmd, "'-C'"):
				return &connector.Result{ExitCode: 0} // already present
			default:
				return &connector.Result{ExitCode: 0}
			}
		}}
		res, err := (&Module{}).Run(context.Background(), conn, base)
		if err != nil {
			t.Fatal(err)
		}
		if res.Changed {
			t.Fatalf("expected no change")
		}
		if conn.find("cat /etc/os-release") != "" || conn.find("iptables-save") != "" {
			t.Errorf("persistence must be skipped when nothing changed, got: %v", conn.cmds)
		}
	})
}
