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
