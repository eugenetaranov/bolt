package connector

import (
	"strings"
	"testing"
)

// TestSudoWrap covers the four wrapping cases and, crucially, guards that the
// sudo password is never interpolated into the wrapped command (which becomes
// the target process's argv). Regression test for the argv-leak vulnerability.
func TestSudoWrap(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		sudoEnabled bool
		password    string
		isRoot      bool
		wantWrapped string
		wantStdin   string
	}{
		{
			name:        "no sudo passthrough",
			cmd:         "whoami",
			sudoEnabled: false,
			wantWrapped: "whoami",
			wantStdin:   "",
		},
		{
			name:        "root skips sudo",
			cmd:         "whoami",
			sudoEnabled: true,
			isRoot:      true,
			wantWrapped: "whoami",
			wantStdin:   "",
		},
		{
			name:        "sudo without password (NOPASSWD)",
			cmd:         "whoami",
			sudoEnabled: true,
			wantWrapped: "sudo sh -c 'whoami'",
			wantStdin:   "",
		},
		{
			name:        "sudo with password feeds stdin",
			cmd:         "whoami",
			sudoEnabled: true,
			password:    "s3cr3t",
			wantWrapped: "sudo -S -p '' sh -c 'whoami'",
			wantStdin:   "s3cr3t\n",
		},
		{
			name:        "single quotes in command are escaped",
			cmd:         "echo 'hi'",
			sudoEnabled: true,
			password:    "pw",
			wantWrapped: `sudo -S -p '' sh -c 'echo '"'"'hi'"'"''`,
			wantStdin:   "pw\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped, stdin := SudoWrap(tt.cmd, tt.sudoEnabled, tt.password, tt.isRoot)
			if wrapped != tt.wantWrapped {
				t.Errorf("wrapped = %q, want %q", wrapped, tt.wantWrapped)
			}
			if string(stdin) != tt.wantStdin {
				t.Errorf("stdin = %q, want %q", string(stdin), tt.wantStdin)
			}
			// The password must never appear in the wrapped command (argv).
			if tt.password != "" && strings.Contains(wrapped, tt.password) {
				t.Errorf("password leaked into wrapped command argv: %q", wrapped)
			}
		})
	}
}

// TestWrapBecome covers become_user / become_method escalation forms.
func TestWrapBecome(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		cfg         BecomeConfig
		isRoot      bool
		wantWrapped string
		wantStdin   string
	}{
		{
			name:        "disabled passthrough",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: false},
			wantWrapped: "id",
		},
		{
			name:        "sudo to non-root user with password",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, User: "postgres", Password: "pw"},
			wantWrapped: "sudo -S -p '' -u 'postgres' sh -c 'id'",
			wantStdin:   "pw\n",
		},
		{
			name:        "sudo to non-root user NOPASSWD",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, User: "www-data"},
			wantWrapped: "sudo -u 'www-data' sh -c 'id'",
		},
		{
			name:        "root connection switching to non-root still wraps",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, User: "postgres"},
			isRoot:      true,
			wantWrapped: "sudo -u 'postgres' sh -c 'id'",
		},
		{
			name:        "root connection to root is a passthrough",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true},
			isRoot:      true,
			wantWrapped: "id",
		},
		{
			name:        "su method",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, Method: BecomeSu, User: "deploy"},
			wantWrapped: "su - 'deploy' -c 'id'",
		},
		{
			name:        "su method defaults to root",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, Method: BecomeSu},
			wantWrapped: "su - 'root' -c 'id'",
		},
		{
			name:        "doas to user",
			cmd:         "id",
			cfg:         BecomeConfig{Enabled: true, Method: BecomeDoas, User: "deploy"},
			wantWrapped: "doas -u 'deploy' sh -c 'id'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped, stdin := WrapBecome(tt.cmd, tt.cfg, tt.isRoot)
			if wrapped != tt.wantWrapped {
				t.Errorf("wrapped = %q, want %q", wrapped, tt.wantWrapped)
			}
			if string(stdin) != tt.wantStdin {
				t.Errorf("stdin = %q, want %q", string(stdin), tt.wantStdin)
			}
			if tt.cfg.Password != "" && strings.Contains(wrapped, tt.cfg.Password) {
				t.Errorf("password leaked into argv: %q", wrapped)
			}
		})
	}
}
