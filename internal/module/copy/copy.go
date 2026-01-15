// Package copy provides a module for copying files to target systems.
package copy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eugenetaranov/bolt/internal/connector"
	"github.com/eugenetaranov/bolt/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module copies files to the target system.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "copy"
}

// Run executes the copy module.
//
// Parameters:
//   - dest (string, required): Destination path on the target
//   - src (string): Source file path on the controller (mutually exclusive with content)
//   - content (string): Inline content to write (mutually exclusive with src)
//   - mode (string): File permissions in octal (e.g., "0644")
//   - owner (string): Owner username
//   - group (string): Group name
//   - backup (bool): Create backup before overwriting (default: false)
//   - force (bool): Overwrite even if destination exists (default: true)
//   - create_dirs (bool): Create parent directories if needed (default: false)
//   - validate (string): Command to validate file before finalizing (%s = temp file path)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Extract parameters
	dest, err := requireString(params, "dest")
	if err != nil {
		return nil, err
	}

	src := getString(params, "src", "")
	content := getString(params, "content", "")
	mode := getString(params, "mode", "0644")
	owner := getString(params, "owner", "")
	group := getString(params, "group", "")
	backup := getBool(params, "backup", false)
	force := getBool(params, "force", true)
	createDirs := getBool(params, "create_dirs", false)
	validate := getString(params, "validate", "")

	// Validate parameters
	if src == "" && content == "" {
		return nil, fmt.Errorf("either 'src' or 'content' parameter is required")
	}
	if src != "" && content != "" {
		return nil, fmt.Errorf("'src' and 'content' are mutually exclusive")
	}

	// Get source content
	var srcContent []byte
	if src != "" {
		// Read from local file
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("failed to read source file: %w", err)
		}
		srcContent = data
	} else {
		srcContent = []byte(content)
	}

	// Calculate checksum of source
	srcChecksum := checksum(srcContent)

	// Check if destination exists and compare checksums
	destExists, destChecksum, err := getRemoteChecksum(ctx, conn, dest)
	if err != nil {
		return nil, fmt.Errorf("failed to check destination: %w", err)
	}

	// If destination exists with same content, check if we need to update mode/owner
	if destExists && srcChecksum == destChecksum {
		// File content matches, check attributes
		attrChanged, err := ensureAttributes(ctx, conn, dest, mode, owner, group)
		if err != nil {
			return nil, err
		}
		if attrChanged {
			return module.Changed("attributes updated"), nil
		}
		return module.Unchanged("file already exists with correct content and attributes"), nil
	}

	// If destination exists and force=false, skip
	if destExists && !force {
		return module.Unchanged("destination exists and force=false"), nil
	}

	// Create parent directories if needed
	if createDirs {
		if err := createParentDirs(ctx, conn, dest); err != nil {
			return nil, err
		}
	}

	// Create backup if needed
	if destExists && backup {
		if err := createBackup(ctx, conn, dest); err != nil {
			return nil, fmt.Errorf("failed to create backup: %w", err)
		}
	}

	// Upload to temp file first if validation is needed
	targetPath := dest
	if validate != "" {
		targetPath = fmt.Sprintf("/tmp/bolt-copy-%d", time.Now().UnixNano())
	}

	// Upload the file
	modeInt, err := parseMode(mode)
	if err != nil {
		return nil, fmt.Errorf("invalid mode: %w", err)
	}

	if err := conn.Upload(ctx, bytes.NewReader(srcContent), targetPath, modeInt); err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	// Run validation if specified
	if validate != "" {
		validateCmd := strings.ReplaceAll(validate, "%s", shellQuote(targetPath))
		result, err := conn.Execute(ctx, validateCmd)
		if err != nil {
			// Clean up temp file (ignore error)
			_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", shellQuote(targetPath)))
			return nil, fmt.Errorf("validation command failed: %w", err)
		}
		if result.ExitCode != 0 {
			// Clean up temp file (ignore error)
			_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", shellQuote(targetPath)))
			return nil, fmt.Errorf("validation failed: %s", result.Stderr)
		}

		// Move temp file to destination
		result, err = conn.Execute(ctx, fmt.Sprintf("mv %s %s", shellQuote(targetPath), shellQuote(dest)))
		if err != nil {
			return nil, fmt.Errorf("failed to move validated file: %w", err)
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("failed to move validated file: %s", result.Stderr)
		}
	}

	// Set attributes
	if _, err := ensureAttributes(ctx, conn, dest, mode, owner, group); err != nil {
		return nil, err
	}

	var msg string
	if destExists {
		msg = "file updated"
	} else {
		msg = "file created"
	}

	return module.ChangedWithData(msg, map[string]any{
		"dest":     dest,
		"checksum": srcChecksum,
	}), nil
}

// checksum calculates SHA256 checksum of data.
func checksum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// getRemoteChecksum gets the SHA256 checksum of a remote file.
func getRemoteChecksum(ctx context.Context, conn connector.Connector, path string) (exists bool, sum string, err error) {
	// Check if file exists and get checksum
	cmd := fmt.Sprintf(`if [ -f %[1]s ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			sha256sum %[1]s | cut -d' ' -f1
		elif command -v shasum >/dev/null 2>&1; then
			shasum -a 256 %[1]s | cut -d' ' -f1
		else
			echo "NO_SHA"
		fi
	else
		echo "NO_FILE"
	fi`, shellQuote(path))

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, "", err
	}

	output := strings.TrimSpace(result.Stdout)
	switch output {
	case "NO_FILE":
		return false, "", nil
	case "NO_SHA":
		// Can't compute checksum, assume different
		return true, "", nil
	default:
		return true, output, nil
	}
}

// ensureAttributes sets mode and ownership on a file.
func ensureAttributes(ctx context.Context, conn connector.Connector, path, mode, owner, group string) (bool, error) {
	var changed bool

	// Set mode
	if mode != "" {
		result, err := conn.Execute(ctx, fmt.Sprintf("chmod %s %s", mode, shellQuote(path)))
		if err != nil {
			return false, fmt.Errorf("failed to set mode: %w", err)
		}
		if result.ExitCode != 0 {
			return false, fmt.Errorf("chmod failed: %s", result.Stderr)
		}
		changed = true
	}

	// Set ownership
	if owner != "" || group != "" {
		var ownership string
		if owner != "" && group != "" {
			ownership = fmt.Sprintf("%s:%s", owner, group)
		} else if owner != "" {
			ownership = owner
		} else {
			ownership = fmt.Sprintf(":%s", group)
		}

		result, err := conn.Execute(ctx, fmt.Sprintf("chown %s %s", ownership, shellQuote(path)))
		if err != nil {
			return false, fmt.Errorf("failed to set ownership: %w", err)
		}
		if result.ExitCode != 0 {
			return false, fmt.Errorf("chown failed: %s", result.Stderr)
		}
		changed = true
	}

	return changed, nil
}

// createParentDirs creates parent directories for a path.
func createParentDirs(ctx context.Context, conn connector.Connector, path string) error {
	// Extract directory from path
	cmd := fmt.Sprintf("mkdir -p \"$(dirname %s)\"", shellQuote(path))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("mkdir failed: %s", result.Stderr)
	}
	return nil
}

// createBackup creates a timestamped backup of a file.
func createBackup(ctx context.Context, conn connector.Connector, path string) error {
	timestamp := time.Now().Format("20060102150405")
	backupPath := fmt.Sprintf("%s.%s.bak", path, timestamp)

	result, err := conn.Execute(ctx, fmt.Sprintf("cp -p %s %s", shellQuote(path), shellQuote(backupPath)))
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("backup failed: %s", result.Stderr)
	}
	return nil
}

// parseMode converts an octal mode string to uint32.
func parseMode(mode string) (uint32, error) {
	// Remove leading zeros for parsing
	mode = strings.TrimLeft(mode, "0")
	if mode == "" {
		mode = "0"
	}

	var m uint32
	_, err := fmt.Sscanf("0"+mode, "%o", &m)
	if err != nil {
		return 0, err
	}
	return m, nil
}

// shellQuote quotes a string for safe use in shell commands.
func shellQuote(s string) string {
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

func getBool(params map[string]any, key string, defaultValue bool) bool {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	b, ok := v.(bool)
	if !ok {
		return defaultValue
	}
	return b
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)
