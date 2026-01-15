package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	boltBinaryPath string
	projectRoot    string
)

func TestMain(m *testing.M) {
	var err error
	projectRoot, err = findProjectRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find project root: %v\n", err)
		os.Exit(1)
	}

	// Build bolt binary
	boltBinaryPath = filepath.Join(projectRoot, "bin", "bolt")
	fmt.Println("Building bolt binary...")
	cmd := exec.Command("go", "build", "-o", boltBinaryPath, "./cmd/bolt")
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build bolt: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func findProjectRoot() (string, error) {
	// Start from current directory and look for go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (go.mod)")
		}
		dir = parent
	}
}

func setupTestContainer(t *testing.T, ctx context.Context) testcontainers.Container {
	t.Helper()

	// Remove any existing container with the same name
	cleanupExistingContainer()

	dockerfilePath := filepath.Join(projectRoot, "tests", "integration")

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    dockerfilePath,
			Dockerfile: "Dockerfile",
		},
		Name:       "bolt-integration-test",
		Cmd:        []string{"sleep", "600"},
		WaitingFor: wait.ForExec([]string{"echo", "ready"}).WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "failed to start test container")

	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
	})

	return container
}

func cleanupExistingContainer() {
	cmd := exec.Command("docker", "rm", "-f", "bolt-integration-test")
	_ = cmd.Run() // Ignore errors - container may not exist
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	// Setup container
	container := setupTestContainer(t, ctx)

	// Run bolt playbook
	playbookPath := filepath.Join(projectRoot, "tests", "integration", "testdata", "playbook.yaml")
	cmd := exec.Command(boltBinaryPath, "run", playbookPath)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "bolt playbook failed: %s", string(output))
	t.Logf("Playbook output:\n%s", string(output))

	// Run validation subtests
	t.Run("RolesSupport", func(t *testing.T) {
		testRolesSupport(t, ctx, container)
	})

	t.Run("CommandModule", func(t *testing.T) {
		testCommandModule(t, ctx, container)
	})

	t.Run("FileModule", func(t *testing.T) {
		testFileModule(t, ctx, container)
	})

	t.Run("CopyModule", func(t *testing.T) {
		testCopyModule(t, ctx, container)
	})
}

func testRolesSupport(t *testing.T, ctx context.Context, container testcontainers.Container) {
	// Verify role tasks executed (role marker file with interpolated variable)
	t.Run("RoleTasksExecuted", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/role-marker.txt")
		assertFileContains(t, ctx, container, "/tmp/role-marker.txt", []string{
			"Created by testrole with port 8080", // 8080 comes from role defaults
		})
	})

	// Verify file copied from role's files directory
	t.Run("RoleFileCopied", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/role-config.txt")
		assertFileContains(t, ctx, container, "/tmp/role-config.txt", []string{
			"This is a role config file",
			"Port: 8080",
			"Environment: production",
		})
		assertFileMode(t, ctx, container, "/tmp/role-config.txt", "644")
	})

	// Verify role created directory with correct permissions
	t.Run("RoleDirectoryCreated", func(t *testing.T) {
		assertIsDirectory(t, ctx, container, "/tmp/role-test-dir")
		assertFileMode(t, ctx, container, "/tmp/role-test-dir", "750")
	})
}

func testCommandModule(t *testing.T, ctx context.Context, container testcontainers.Container) {
	// Verify marker file created by command module
	assertFileExists(t, ctx, container, "/tmp/command-marker.txt")
	assertFileContains(t, ctx, container, "/tmp/command-marker.txt", []string{"command-created"})

	// Verify command output via cat
	assertCommandOutput(t, ctx, container, []string{"cat", "/tmp/command-marker.txt"}, []string{"command-created"})
}

func testFileModule(t *testing.T, ctx context.Context, container testcontainers.Container) {
	// Test directory creation
	t.Run("Directory", func(t *testing.T) {
		assertIsDirectory(t, ctx, container, "/tmp/bolt-test-dir")
		assertFileMode(t, ctx, container, "/tmp/bolt-test-dir", "755")
	})

	// Test nested directory creation
	t.Run("NestedDirectory", func(t *testing.T) {
		assertIsDirectory(t, ctx, container, "/tmp/bolt-test-dir/nested/deep")
		assertFileMode(t, ctx, container, "/tmp/bolt-test-dir/nested/deep", "700")
	})

	// Test touch (empty file creation)
	t.Run("Touch", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/bolt-test-dir/touched.txt")
		assertIsFile(t, ctx, container, "/tmp/bolt-test-dir/touched.txt")
		assertFileMode(t, ctx, container, "/tmp/bolt-test-dir/touched.txt", "644")
	})

	// Test symlink creation
	t.Run("Symlink", func(t *testing.T) {
		// Verify the link target exists
		assertFileExists(t, ctx, container, "/tmp/bolt-test-dir/link-target.txt")
		assertFileContains(t, ctx, container, "/tmp/bolt-test-dir/link-target.txt", []string{"symlink target content"})

		// Verify symlink
		assertSymlink(t, ctx, container, "/tmp/bolt-test-dir/test-link", "/tmp/bolt-test-dir/link-target.txt")

		// Verify symlink resolution works
		assertCommandOutput(t, ctx, container, []string{"cat", "/tmp/bolt-test-dir/test-link"}, []string{"symlink target content"})
	})
}

func testCopyModule(t *testing.T, ctx context.Context, container testcontainers.Container) {
	// Test basic copy with content
	t.Run("BasicCopy", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/bolt-test-dir/copied.txt")
		assertIsFile(t, ctx, container, "/tmp/bolt-test-dir/copied.txt")
		assertFileMode(t, ctx, container, "/tmp/bolt-test-dir/copied.txt", "640")
		assertFileContains(t, ctx, container, "/tmp/bolt-test-dir/copied.txt", []string{
			"This file was created by bolt copy module",
			"Line 2 of the file",
		})
	})

	// Test executable script
	t.Run("ExecutableScript", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/bolt-test-dir/executable.sh")
		assertIsFile(t, ctx, container, "/tmp/bolt-test-dir/executable.sh")
		assertFileMode(t, ctx, container, "/tmp/bolt-test-dir/executable.sh", "755")
		assertFileContains(t, ctx, container, "/tmp/bolt-test-dir/executable.sh", []string{
			"#!/bin/bash",
			"I am executable",
		})
	})

	// Test config file
	t.Run("ConfigFile", func(t *testing.T) {
		assertFileExists(t, ctx, container, "/tmp/bolt-test-dir/config.yaml")
		assertFileContains(t, ctx, container, "/tmp/bolt-test-dir/config.yaml", []string{
			"port: 8080",
			"host: localhost",
			"level: info",
		})
	})
}
