// Package connector defines the interface for executing commands on target systems.
package connector

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Result holds the output from command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Connector is the interface for connecting to and executing commands on targets.
type Connector interface {
	// Connect establishes a connection to the target.
	Connect(ctx context.Context) error

	// Execute runs a command on the target and returns the result.
	Execute(ctx context.Context, cmd string) (*Result, error)

	// Upload copies a file from local source to remote destination.
	Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error

	// Download copies a file from remote source to local destination.
	Download(ctx context.Context, src string, dst io.Writer) error

	// SetSudo enables or disables sudo for subsequent commands.
	SetSudo(enabled bool, password string)

	// Close terminates the connection.
	Close() error

	// String returns a human-readable description of the connection.
	String() string
}

// Run executes a command and returns an error if the command fails (non-zero exit code).
// Returns the Result so callers needing stdout can use it.
func Run(ctx context.Context, conn Connector, cmd string) (*Result, error) {
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("%s", strings.TrimSpace(result.Stderr))
	}
	return result, nil
}

// ShellQuote wraps a string in single quotes for safe shell usage.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// BecomeConfig describes privilege escalation for a command.
type BecomeConfig struct {
	// Enabled turns escalation on.
	Enabled bool
	// Method is "sudo" (default), "su", or "doas".
	Method string
	// User is the target user; empty means root.
	User string
	// Password is the escalation password (used only by the sudo method, fed
	// via stdin). su/doas read the password from a TTY and therefore require
	// passwordless configuration on the target.
	Password string
}

// Becomer is an optional connector capability for configuring privilege
// escalation with a target user and method. Connectors that only support
// sudo-to-root can implement just SetSudo; those that implement Becomer also
// honor become_user / become_method.
type Becomer interface {
	SetBecome(cfg BecomeConfig)
}

// EnvSetter is an optional connector capability for setting environment
// variables applied to subsequent commands (the `environment:` directive).
type EnvSetter interface {
	SetEnv(env map[string]string)
}

// PrependEnv prefixes cmd with `export K=V;` assignments so the variables apply
// to the whole command — surviving compound commands and the `sudo … sh -c`
// wrapping WrapBecome adds. Keys are sorted for deterministic output.
func PrependEnv(cmd string, env map[string]string) string {
	if len(env) == 0 {
		return cmd
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(ShellQuote(env[k]))
		b.WriteString("; ")
	}
	b.WriteString(cmd)
	return b.String()
}

// Method constants for BecomeConfig.Method.
const (
	BecomeSudo = "sudo"
	BecomeSu   = "su"
	BecomeDoas = "doas"
)

// SudoWrap wraps cmd with sudo when enabled (skipping when already root).
// It is retained for callers that only need sudo-to-root; it delegates to
// WrapBecome.
func SudoWrap(cmd string, sudoEnabled bool, password string, isRoot bool) (wrapped string, stdin []byte) {
	return WrapBecome(cmd, BecomeConfig{Enabled: sudoEnabled, Password: password}, isRoot)
}

// WrapBecome wraps cmd for privilege escalation per cfg. It returns the wrapped
// command and, for the sudo method with a password, the password bytes (with a
// trailing newline) to feed via stdin — keeping the secret out of argv
// (/proc/<pid>/cmdline) and off disk.
//
// Escalation is skipped when disabled, or when the connection is already root
// and the target user is also root. When the connection is root but the target
// is a non-root user, the command is still wrapped to switch users.
func WrapBecome(cmd string, cfg BecomeConfig, isRoot bool) (wrapped string, stdin []byte) {
	if !cfg.Enabled {
		return cmd, nil
	}
	targetRoot := cfg.User == "" || cfg.User == "root"
	if isRoot && targetRoot {
		return cmd, nil
	}

	escaped := strings.ReplaceAll(cmd, "'", "'\"'\"'")
	method := cfg.Method
	if method == "" {
		method = BecomeSudo
	}

	switch method {
	case BecomeSu:
		user := cfg.User
		if user == "" {
			user = "root"
		}
		// su reads the password from the controlling TTY, so su become expects
		// passwordless switching (typically root -> user).
		return fmt.Sprintf("su - %s -c '%s'", ShellQuote(user), escaped), nil

	case BecomeDoas:
		userFlag := ""
		if !targetRoot {
			userFlag = "-u " + ShellQuote(cfg.User) + " "
		}
		// doas prompts on the TTY; passwordless (NOPASSWD) is required.
		return fmt.Sprintf("doas %ssh -c '%s'", userFlag, escaped), nil

	default: // BecomeSudo
		userFlag := ""
		if !targetRoot {
			userFlag = "-u " + ShellQuote(cfg.User) + " "
		}
		if cfg.Password != "" {
			return fmt.Sprintf("sudo -S -p '' %ssh -c '%s'", userFlag, escaped), []byte(cfg.Password + "\n")
		}
		return fmt.Sprintf("sudo %ssh -c '%s'", userFlag, escaped), nil
	}
}

// Config holds common configuration for connectors.
type Config struct {
	// Host is the target hostname or IP address.
	Host string

	// User is the username for authentication.
	User string

	// Timeout is the connection timeout in seconds.
	Timeout int
}
