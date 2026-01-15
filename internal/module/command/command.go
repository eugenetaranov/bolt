// Package command provides a module for executing shell commands.
package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/eugenetaranov/bolt/internal/connector"
	"github.com/eugenetaranov/bolt/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module executes shell commands on the target system.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "command"
}

// Run executes the command module.
//
// Parameters:
//   - cmd (string, required): The command to execute
//   - chdir (string): Change to this directory before running
//   - creates (string): Skip if this file/path exists (for idempotency)
//   - removes (string): Only run if this file/path exists (for idempotency)
//   - warn (bool): Whether to warn about common issues (default: true)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Extract parameters
	cmd, err := requireString(params, "cmd")
	if err != nil {
		return nil, err
	}

	chdir := getString(params, "chdir", "")
	creates := getString(params, "creates", "")
	removes := getString(params, "removes", "")

	// Check 'creates' condition - skip if file exists
	if creates != "" {
		exists, err := fileExists(ctx, conn, creates)
		if err != nil {
			return nil, fmt.Errorf("failed to check 'creates' path: %w", err)
		}
		if exists {
			return module.Unchanged(fmt.Sprintf("skipped, '%s' exists", creates)), nil
		}
	}

	// Check 'removes' condition - only run if file exists
	if removes != "" {
		exists, err := fileExists(ctx, conn, removes)
		if err != nil {
			return nil, fmt.Errorf("failed to check 'removes' path: %w", err)
		}
		if !exists {
			return module.Unchanged(fmt.Sprintf("skipped, '%s' does not exist", removes)), nil
		}
	}

	// Build the command with chdir if specified
	fullCmd := cmd
	if chdir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", shellQuote(chdir), cmd)
	}

	// Execute the command
	result, err := conn.Execute(ctx, fullCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	// Check for non-zero exit code
	if result.ExitCode != 0 {
		return nil, &CommandError{
			Cmd:      cmd,
			ExitCode: result.ExitCode,
			Stdout:   result.Stdout,
			Stderr:   result.Stderr,
		}
	}

	return module.ChangedWithData("command executed successfully", map[string]any{
		"cmd":       cmd,
		"stdout":    strings.TrimSpace(result.Stdout),
		"stderr":    strings.TrimSpace(result.Stderr),
		"exit_code": result.ExitCode,
	}), nil
}

// CommandError represents a command execution failure.
type CommandError struct {
	Cmd      string
	ExitCode int
	Stdout   string
	Stderr   string
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("command failed with exit code %d: %s", e.ExitCode, e.Cmd)
	if e.Stderr != "" {
		msg += fmt.Sprintf("\nstderr: %s", strings.TrimSpace(e.Stderr))
	}
	return msg
}

// fileExists checks if a file or directory exists on the target.
func fileExists(ctx context.Context, conn connector.Connector, path string) (bool, error) {
	result, err := conn.Execute(ctx, fmt.Sprintf("test -e %s", shellQuote(path)))
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// shellQuote quotes a string for safe use in shell commands.
func shellQuote(s string) string {
	// Use single quotes and escape any single quotes in the string
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// Helper functions for parameter extraction

func requireString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("required parameter '%s' is missing", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parameter '%s' must be a string", key)
	}
	if s == "" {
		return "", fmt.Errorf("parameter '%s' cannot be empty", key)
	}
	return s, nil
}

func getString(params map[string]any, key, defaultValue string) string {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	s, ok := v.(string)
	if !ok {
		return defaultValue
	}
	return s
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)
