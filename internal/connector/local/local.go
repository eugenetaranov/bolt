// Package local provides a connector for executing commands on the local machine.
package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"

	"github.com/tackhq/tack/internal/connector"
)

// Connector executes commands on the local machine.
type Connector struct {
	shell        string
	shellArgs    []string
	sudo         bool
	sudoPassword string
}

// Option configures the local connector.
type Option func(*Connector)

// WithSudo enables sudo for command execution.
func WithSudo() Option {
	return func(c *Connector) {
		c.sudo = true
	}
}

// WithSudoPassword sets the sudo password.
func WithSudoPassword(password string) Option {
	return func(c *Connector) {
		c.sudoPassword = password
	}
}

// WithShell sets a custom shell for command execution.
func WithShell(shell string, args ...string) Option {
	return func(c *Connector) {
		c.shell = shell
		c.shellArgs = args
	}
}

// New creates a new local connector.
func New(opts ...Option) *Connector {
	c := &Connector{}

	// Set default shell based on OS
	switch runtime.GOOS {
	case "windows":
		c.shell = "cmd"
		c.shellArgs = []string{"/C"}
	default:
		c.shell = "/bin/sh"
		c.shellArgs = []string{"-c"}
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Connect is a no-op for local connections.
func (c *Connector) Connect(ctx context.Context) error {
	// Verify we're on a supported platform
	switch runtime.GOOS {
	case "darwin", "linux":
		return nil
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Execute runs a command locally and returns the result.
func (c *Connector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	// Build the command. Any stdin bytes carry the sudo password, fed on the
	// process's stdin so it never appears in the argv (/proc/<pid>/cmdline).
	fullCmd, stdin := c.buildCommand(cmd)

	// Create the exec.Cmd
	args := make([]string, len(c.shellArgs)+1)
	copy(args, c.shellArgs)
	args[len(c.shellArgs)] = fullCmd
	execCmd := exec.CommandContext(ctx, c.shell, args...)

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	if stdin != nil {
		execCmd.Stdin = bytes.NewReader(stdin)
	}

	// Run the command
	err := execCmd.Run()

	result := &connector.Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	// Get exit code
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			// Command failed to start
			return nil, fmt.Errorf("failed to execute command: %w", err)
		}
	}

	return result, nil
}

// buildCommand wraps the command with sudo if configured, returning any stdin
// bytes (the sudo password) that must be fed to the process.
func (c *Connector) buildCommand(cmd string) (string, []byte) {
	isRoot := false
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		isRoot = true
	}
	return connector.SudoWrap(cmd, c.sudo, c.sudoPassword, isRoot)
}

// SetSudo enables or disables sudo for subsequent commands.
func (c *Connector) SetSudo(enabled bool, password string) {
	c.sudo = enabled
	c.sudoPassword = password
}

// Upload writes content from src to a local file at dst.
// When sudo is enabled and the current user is not root, writes to a
// temp file first, then moves it into place via a sudo shell command.
func (c *Connector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// When sudo is active and we're not root, write to a temp file first.
	needsSudo := c.sudo
	if needsSudo {
		if u, err := user.Current(); err == nil && u.Uid == "0" {
			needsSudo = false
		}
	}

	if needsSudo {
		tmpFile, err := os.CreateTemp("", "tack-upload-*")
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)

		if _, err := io.Copy(tmpFile, src); err != nil {
			tmpFile.Close()
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		tmpFile.Close()

		modeStr := fmt.Sprintf("%04o", mode)
		cmd := fmt.Sprintf("mv %s %s && chmod %s %s",
			connector.ShellQuote(tmpPath), connector.ShellQuote(dst),
			modeStr, connector.ShellQuote(dst))
		result, err := c.Execute(ctx, cmd)
		if err != nil {
			return fmt.Errorf("failed to move uploaded file to %s: %w", dst, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("failed to move uploaded file to %s: %s", dst, result.Stderr)
		}
		return nil
	}

	// Direct write — no sudo needed
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", dst, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("failed to write to %s: %w", dst, err)
	}

	return nil
}

// Download reads content from a local file at src to dst.
func (c *Connector) Download(ctx context.Context, src string, dst io.Writer) error {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", src, err)
	}
	defer f.Close()

	if _, err := io.Copy(dst, f); err != nil {
		return fmt.Errorf("failed to read from %s: %w", src, err)
	}

	return nil
}

// Close is a no-op for local connections.
func (c *Connector) Close() error {
	return nil
}

// String returns a description of the connection.
func (c *Connector) String() string {
	u, err := user.Current()
	if err != nil {
		return "local"
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}

	if c.sudo {
		return fmt.Sprintf("local://%s@%s (sudo)", u.Username, hostname)
	}
	return fmt.Sprintf("local://%s@%s", u.Username, hostname)
}

// Ensure Connector implements the connector.Connector interface.
var _ connector.Connector = (*Connector)(nil)
