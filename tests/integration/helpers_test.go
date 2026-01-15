package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

// execInContainer runs a command in the container and returns stdout
func execInContainer(ctx context.Context, container testcontainers.Container, cmd []string) (int, string, error) {
	exitCode, reader, err := container.Exec(ctx, cmd)
	if err != nil {
		return exitCode, "", err
	}

	// Demux the Docker stream (stdout/stderr are multiplexed)
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, reader)

	return exitCode, stdout.String(), nil
}

// assertFileExists checks that a file exists in the container
func assertFileExists(t *testing.T, ctx context.Context, container testcontainers.Container, path string) {
	t.Helper()
	exitCode, _, err := execInContainer(ctx, container, []string{"test", "-e", path})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "file %s should exist", path)
}

// assertFileContains checks that a file contains all expected substrings
func assertFileContains(t *testing.T, ctx context.Context, container testcontainers.Container, path string, expected []string) {
	t.Helper()
	exitCode, content, err := execInContainer(ctx, container, []string{"cat", path})
	require.NoError(t, err)
	require.Equal(t, 0, exitCode, "failed to read file %s", path)

	for _, substr := range expected {
		assert.Contains(t, content, substr, "file %s should contain %q", path, substr)
	}
}

// assertFileMode checks that a file has the expected permission mode
func assertFileMode(t *testing.T, ctx context.Context, container testcontainers.Container, path string, expectedMode string) {
	t.Helper()
	exitCode, mode, err := execInContainer(ctx, container, []string{"stat", "-c", "%a", path})
	require.NoError(t, err)
	require.Equal(t, 0, exitCode, "failed to stat file %s", path)

	assert.Equal(t, expectedMode, strings.TrimSpace(mode), "file %s should have mode %s", path, expectedMode)
}

// assertIsDirectory checks that a path is a directory
func assertIsDirectory(t *testing.T, ctx context.Context, container testcontainers.Container, path string) {
	t.Helper()
	exitCode, _, err := execInContainer(ctx, container, []string{"test", "-d", path})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "%s should be a directory", path)
}

// assertIsFile checks that a path is a regular file
func assertIsFile(t *testing.T, ctx context.Context, container testcontainers.Container, path string) {
	t.Helper()
	exitCode, _, err := execInContainer(ctx, container, []string{"test", "-f", path})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "%s should be a regular file", path)
}

// assertSymlink checks that a path is a symlink pointing to the expected target
func assertSymlink(t *testing.T, ctx context.Context, container testcontainers.Container, path, expectedTarget string) {
	t.Helper()

	// Check it's a symlink
	exitCode, _, err := execInContainer(ctx, container, []string{"test", "-L", path})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "%s should be a symlink", path)

	// Check target
	exitCode, target, err := execInContainer(ctx, container, []string{"readlink", path})
	require.NoError(t, err)
	require.Equal(t, 0, exitCode, "failed to read symlink %s", path)

	assert.Equal(t, expectedTarget, strings.TrimSpace(target), "symlink %s should point to %s", path, expectedTarget)
}

// assertCommandOutput runs a command and checks its stdout contains expected strings
func assertCommandOutput(t *testing.T, ctx context.Context, container testcontainers.Container, cmd []string, expectedStdout []string) {
	t.Helper()
	exitCode, output, err := execInContainer(ctx, container, cmd)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "command %v should succeed", cmd)

	for _, expected := range expectedStdout {
		assert.Contains(t, output, expected, "command output should contain %q", expected)
	}
}
