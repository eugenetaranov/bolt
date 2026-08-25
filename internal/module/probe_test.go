package module

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/connector"
)

// mockConnector implements connector.Connector for testing CommandAvailable.
// It returns a fixed result for every Execute call.
type mockConnector struct {
	result *connector.Result
	err    error
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
	return m.result, m.err
}

func TestCommandAvailable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		result      *connector.Result
		wantOK      bool
		wantErr     bool
		errContains string
	}{
		{
			name:   "present",
			result: &connector.Result{Stdout: "/usr/bin/apt-get\n", ExitCode: 0},
			wantOK: true,
		},
		{
			name:   "genuinely not found (empty stderr)",
			result: &connector.Result{ExitCode: 1},
			wantOK: false,
		},
		{
			name:   "shell command-not-found is not a probe error",
			result: &connector.Result{ExitCode: 1, Stderr: "command not found"},
			wantOK: false,
		},
		{
			name:        "sudo password required surfaces hint",
			result:      &connector.Result{ExitCode: 1, Stderr: "sudo: a password is required"},
			wantOK:      false,
			wantErr:     true,
			errContains: "--sudo-password",
		},
		{
			name:        "sudo no tty surfaces hint",
			result:      &connector.Result{ExitCode: 1, Stderr: "sudo: no tty present and no askpass program specified"},
			wantOK:      false,
			wantErr:     true,
			errContains: "--sudo-password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &mockConnector{result: tt.result}
			ok, err := CommandAvailable(ctx, conn, "apt-get")
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				// The real stderr should be preserved in the message.
				if !strings.Contains(err.Error(), "sudo") {
					t.Errorf("error %q should preserve the real stderr", err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
