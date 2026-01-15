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

	"github.com/eugenetaranov/bolt/internal/connector"
)

// Connector executes commands on the local machine.
type Connector struct {
	shell     string
	shellArgs []string
	sudo      bool
	sudoUser  string
}

// Option configures the local connector.
type Option func(*Connector)

// WithSudo enables sudo for command execution.
func WithSudo(user string) Option {
	return func(c *Connector) {
		c.sudo = true
		c.sudoUser = user
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
	// Build the command
	fullCmd := c.buildCommand(cmd)

	// Create the exec.Cmd
	args := append(c.shellArgs, fullCmd)
	execCmd := exec.CommandContext(ctx, c.shell, args...)

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

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

// buildCommand wraps the command with sudo if configured.
func (c *Connector) buildCommand(cmd string) string {
	if !c.sudo {
		return cmd
	}

	if c.sudoUser != "" {
		return fmt.Sprintf("sudo -u %s -- %s", c.sudoUser, cmd)
	}
	return fmt.Sprintf("sudo -- %s", cmd)
}

// Upload writes content from src to a local file at dst.
func (c *Connector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Create the destination file
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", dst, err)
	}
	defer f.Close()

	// Copy content
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

	if c.sudo && c.sudoUser != "" {
		return fmt.Sprintf("local://%s@%s (sudo as %s)", u.Username, hostname, c.sudoUser)
	}
	if c.sudo {
		return fmt.Sprintf("local://%s@%s (sudo)", u.Username, hostname)
	}
	return fmt.Sprintf("local://%s@%s", u.Username, hostname)
}

// Ensure Connector implements the connector.Connector interface.
var _ connector.Connector = (*Connector)(nil)
