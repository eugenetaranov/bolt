// Package docker provides a connector for executing commands in Docker containers.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/eugenetaranov/bolt/internal/connector"
)

// Connector executes commands inside Docker containers.
type Connector struct {
	container string
	user      string
	workdir   string
	env       map[string]string
}

// Option configures the Docker connector.
type Option func(*Connector)

// WithUser sets the user for command execution.
func WithUser(user string) Option {
	return func(c *Connector) {
		c.user = user
	}
}

// WithWorkdir sets the working directory for command execution.
func WithWorkdir(dir string) Option {
	return func(c *Connector) {
		c.workdir = dir
	}
}

// WithEnv adds an environment variable for command execution.
func WithEnv(key, value string) Option {
	return func(c *Connector) {
		if c.env == nil {
			c.env = make(map[string]string)
		}
		c.env[key] = value
	}
}

// New creates a new Docker connector for the specified container.
func New(container string, opts ...Option) *Connector {
	c := &Connector{
		container: container,
		env:       make(map[string]string),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Connect verifies the container exists and is running.
func (c *Connector) Connect(ctx context.Context) error {
	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker command not found: %w", err)
	}

	// Check if container exists and is running
	cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", c.container)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("container '%s' not found or not accessible: %w", c.container, err)
	}

	if strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("container '%s' is not running", c.container)
	}

	return nil
}

// Execute runs a command inside the container.
func (c *Connector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	args := c.buildExecArgs(cmd)

	execCmd := exec.CommandContext(ctx, "docker", args...)

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	err := execCmd.Run()

	result := &connector.Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to execute command in container: %w", err)
		}
	}

	return result, nil
}

// buildExecArgs builds the docker exec command arguments.
func (c *Connector) buildExecArgs(cmd string) []string {
	args := []string{"exec"}

	// Add interactive flag for proper stdin handling
	args = append(args, "-i")

	// Add user if specified
	if c.user != "" {
		args = append(args, "-u", c.user)
	}

	// Add working directory if specified
	if c.workdir != "" {
		args = append(args, "-w", c.workdir)
	}

	// Add environment variables
	for k, v := range c.env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Add container and command
	args = append(args, c.container, "/bin/sh", "-c", cmd)

	return args
}

// Upload copies content to a file inside the container.
func (c *Connector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	// Docker cp doesn't support stdin directly, so we need a temp file
	tmpFile, err := os.CreateTemp("", "bolt-upload-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Write content to temp file
	if _, err := io.Copy(tmpFile, src); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	// Set permissions on temp file
	if err := os.Chmod(tmpPath, os.FileMode(mode)); err != nil {
		return fmt.Errorf("failed to set temp file mode: %w", err)
	}

	// Copy to container
	cmd := exec.CommandContext(ctx, "docker", "cp", tmpPath, fmt.Sprintf("%s:%s", c.container, dst))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy file to container: %s: %w", string(output), err)
	}

	// Set permissions inside container
	chmodCmd := fmt.Sprintf("chmod %o %s", mode, dst)
	if _, err := c.Execute(ctx, chmodCmd); err != nil {
		return fmt.Errorf("failed to set file permissions in container: %w", err)
	}

	return nil
}

// Download copies content from a file inside the container.
func (c *Connector) Download(ctx context.Context, src string, dst io.Writer) error {
	// Docker cp doesn't support stdout directly, so we need a temp file
	tmpFile, err := os.CreateTemp("", "bolt-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Copy from container
	cmd := exec.CommandContext(ctx, "docker", "cp", fmt.Sprintf("%s:%s", c.container, src), tmpPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy file from container: %s: %w", string(output), err)
	}

	// Read temp file and write to dst
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(dst, f); err != nil {
		return fmt.Errorf("failed to read downloaded file: %w", err)
	}

	return nil
}

// Close is a no-op for Docker connections.
func (c *Connector) Close() error {
	return nil
}

// String returns a description of the connection.
func (c *Connector) String() string {
	desc := fmt.Sprintf("docker://%s", c.container)
	if c.user != "" {
		desc = fmt.Sprintf("docker://%s@%s", c.user, c.container)
	}
	return desc
}

// Ensure Connector implements the connector.Connector interface.
var _ connector.Connector = (*Connector)(nil)
