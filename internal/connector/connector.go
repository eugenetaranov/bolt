// Package connector defines the interface for executing commands on target systems.
package connector

import (
	"context"
	"io"
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

	// Close terminates the connection.
	Close() error

	// String returns a human-readable description of the connection.
	String() string
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
