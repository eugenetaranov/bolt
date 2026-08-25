package user

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
	return &mockConnector{
		commands: make(map[string]*connector.Result),
	}
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

func TestName(t *testing.T) {
	mod := &Module{}
	if mod.Name() != "user" {
		t.Errorf("expected 'user', got %q", mod.Name())
	}
}

func TestGetUserInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("user exists", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy docker\n", 0)

		info, err := getUserInfo(ctx, conn, "deploy")
		if err != nil {
			t.Fatal(err)
		}
		if !info.Exists {
			t.Error("expected user to exist")
		}
		if info.UID != 1000 {
			t.Errorf("expected uid 1000, got %d", info.UID)
		}
		if info.Home != "/home/deploy" {
			t.Errorf("expected home /home/deploy, got %s", info.Home)
		}
		if info.Shell != "/bin/bash" {
			t.Errorf("expected shell /bin/bash, got %s", info.Shell)
		}
		if len(info.Groups) != 1 || info.Groups[0] != "docker" {
			t.Errorf("expected groups [docker], got %v", info.Groups)
		}
	})

	t.Run("user does not exist", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'nouser'", "", 2)

		info, err := getUserInfo(ctx, conn, "nouser")
		if err != nil {
			t.Fatal(err)
		}
		if info.Exists {
			t.Error("expected user to not exist")
		}
	})
}

func TestRunCreateUser(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("create with defaults", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "", 2)
		conn.onCmd("useradd 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{"name": "deploy"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("create with all options", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'app'", "", 2)
		conn.onCmd("useradd -s '/bin/bash' -d '/opt/app' -u 1500 -G docker,wheel -r 'app'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":   "app",
			"shell":  "/bin/bash",
			"home":   "/opt/app",
			"uid":    1500,
			"groups": []any{"docker", "wheel"},
			"system": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("create with password", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "", 2)
		conn.onCmd("useradd -p '$6$hash' 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":     "deploy",
			"password": "$6$hash",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})
}

func TestRunUserIdempotent(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("user already exists with matching attributes", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":  "deploy",
			"shell": "/bin/bash",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Changed {
			t.Error("expected no change")
		}
	})
}

func TestRunUserModify(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("change shell", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("usermod -s '/bin/zsh' 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":  "deploy",
			"shell": "/bin/zsh",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("add to groups", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("usermod -aG docker,wheel 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":   "deploy",
			"groups": []any{"docker", "wheel"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("groups already match", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy docker\n", 0)

		result, err := mod.Run(ctx, conn, map[string]any{
			"name":   "deploy",
			"groups": []any{"docker"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Changed {
			t.Error("expected no change")
		}
	})
}

func TestRunUserRemove(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("remove without home", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("userdel 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{"name": "deploy", "state": "absent"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("remove with home", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("userdel -r 'deploy'", "", 0)

		result, err := mod.Run(ctx, conn, map[string]any{"name": "deploy", "state": "absent", "remove": true})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Changed {
			t.Error("expected changed")
		}
	})

	t.Run("remove non-existent user", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "", 2)

		result, err := mod.Run(ctx, conn, map[string]any{"name": "deploy", "state": "absent"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Changed {
			t.Error("expected no change")
		}
	})
}

func TestRunUserValidation(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("missing name", func(t *testing.T) {
		conn := newMockConnector()
		_, err := mod.Run(ctx, conn, map[string]any{})
		if err == nil {
			t.Error("expected error for missing name")
		}
	})

	t.Run("invalid state", func(t *testing.T) {
		conn := newMockConnector()
		_, err := mod.Run(ctx, conn, map[string]any{"name": "deploy", "state": "invalid"})
		if err == nil {
			t.Error("expected error for invalid state")
		}
	})
}

func TestCheckUser(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}

	t.Run("would create", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "", 2)

		cr, err := mod.Check(ctx, conn, map[string]any{"name": "deploy"})
		if err != nil {
			t.Fatal(err)
		}
		if !cr.WouldChange {
			t.Error("expected would change")
		}
	})

	t.Run("no change needed", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)

		cr, err := mod.Check(ctx, conn, map[string]any{"name": "deploy", "shell": "/bin/bash"})
		if err != nil {
			t.Fatal(err)
		}
		if cr.WouldChange {
			t.Error("expected no change")
		}
	})

	t.Run("would modify", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)

		cr, err := mod.Check(ctx, conn, map[string]any{"name": "deploy", "shell": "/bin/zsh"})
		if err != nil {
			t.Fatal(err)
		}
		if !cr.WouldChange {
			t.Error("expected would change")
		}
	})

	t.Run("would remove", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)

		cr, err := mod.Check(ctx, conn, map[string]any{"name": "deploy", "state": "absent"})
		if err != nil {
			t.Fatal(err)
		}
		if !cr.WouldChange {
			t.Error("expected would change")
		}
	})
}

func TestStringSliceEqual(t *testing.T) {
	tests := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{}, []string{}, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a"}, nil, false},
		{[]string{"a", "b"}, []string{"a"}, false},
	}
	for _, tt := range tests {
		got := stringSliceEqual(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("stringSliceEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func execedContains(m *mockConnector, sub string) bool {
	for _, c := range m.executed {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestRunCreateUser_PrimaryGroup(t *testing.T) {
	conn := newMockConnector()
	conn.onCmd("getent passwd 'scans'", "", 2) // doesn't exist
	conn.onCmd("useradd -s '/usr/sbin/nologin' -g 'scans' -r 'scans'", "", 0)

	res, err := (&Module{}).Run(context.Background(), conn, map[string]any{
		"name": "scans", "system": true, "group": "scans", "shell": "/usr/sbin/nologin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("expected Changed=true")
	}
	if !execedContains(conn, "useradd") || !execedContains(conn, "-g 'scans'") {
		t.Errorf("expected useradd with -g 'scans', got: %v", conn.executed)
	}
}

func TestRunUser_PrimaryGroupModifyAndIdempotent(t *testing.T) {
	// Existing user with primary gid 1000. Desired group "staff" (gid 2000) -> modify.
	t.Run("modify", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("getent group 'staff'", "staff:x:2000:\n", 0)
		conn.onCmd("usermod -g 'staff' 'deploy'", "", 0)

		res, err := (&Module{}).Run(context.Background(), conn, map[string]any{"name": "deploy", "group": "staff"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Changed || !execedContains(conn, "usermod -g 'staff'") {
			t.Errorf("expected usermod -g 'staff', changed=%v cmds=%v", res.Changed, conn.executed)
		}
	})

	// Desired group resolves to the user's existing primary gid -> no change.
	t.Run("idempotent", func(t *testing.T) {
		conn := newMockConnector()
		conn.onCmd("getent passwd 'deploy'", "deploy:x:1000:1000::/home/deploy:/bin/bash\n", 0)
		conn.onCmd("id -gn 'deploy'", "deploy\n", 0)
		conn.onCmd("id -Gn 'deploy'", "deploy\n", 0)
		conn.onCmd("getent group 'deploy'", "deploy:x:1000:\n", 0)

		res, err := (&Module{}).Run(context.Background(), conn, map[string]any{"name": "deploy", "group": "deploy"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Changed {
			t.Errorf("expected no change; cmds=%v", conn.executed)
		}
		if execedContains(conn, "usermod") {
			t.Errorf("should not run usermod when primary group already matches")
		}
	})
}
