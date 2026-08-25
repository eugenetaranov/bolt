// Package copy provides a module for copying files to target systems.
package copy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
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

// Description returns a short summary of the copy module.
func (m *Module) Description() string {
	return "Copy a file to the target from a controller source or inline content."
}

// Parameters returns the parameter documentation for the copy module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "dest", Type: "string", Required: true, Description: "Destination path on the target"},
		{Name: "src", Type: "string", Description: "Source file path on the controller (mutually exclusive with content)"},
		{Name: "content", Type: "string", Description: "Inline content to write (mutually exclusive with src)"},
		{Name: "mode", Type: "string", Default: "0644", Description: "File permissions in octal"},
		{Name: "owner", Type: "string", Description: "Owner username"},
		{Name: "group", Type: "string", Description: "Group name"},
		{Name: "backup", Type: "bool", Default: "false", Description: "Create a backup before overwriting"},
		{Name: "force", Type: "bool", Default: "true", Description: "Overwrite even if the destination exists"},
		{Name: "create_dirs", Type: "bool", Default: "false", Description: "Create parent directories if needed"},
		{Name: "validate", Type: "string", Description: "Command to validate the file before finalizing (%s = temp path)"},
	}
}

// Example returns a usage example for the copy module.
func (m *Module) Example() string {
	return `- name: Copy a config file
  copy:
    src: files/app.conf
    dest: /etc/app/app.conf
    owner: root
    group: root
    mode: "0644"`
}

// Ensure Module implements the documentation interfaces.
var (
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

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
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}

	src := module.GetString(params, "src", "")
	content := module.GetString(params, "content", "")
	mode := module.GetString(params, "mode", "0644")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	backup := module.GetBool(params, "backup", false)
	force := module.GetBool(params, "force", true)
	createDirs := module.GetBool(params, "create_dirs", false)
	validate := module.GetString(params, "validate", "")

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
		srcPath := module.ResolveRolePath(src, params, "files")

		// Read from local file
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read source file '%s': %w", srcPath, err)
		}
		srcContent = data
	} else {
		srcContent = []byte(content)
	}

	// Check if destination exists and whether we should skip
	srcChecksum := module.Checksum(srcContent)
	destExists, destChecksum, err := module.GetRemoteChecksum(ctx, conn, dest)
	if err != nil {
		return nil, fmt.Errorf("failed to check destination: %w", err)
	}
	if destExists && srcChecksum == destChecksum {
		attrChanged, err := module.EnsureAttributes(ctx, conn, dest, mode, owner, group, false)
		if err != nil {
			return nil, err
		}
		if attrChanged {
			return module.Changed("attributes updated"), nil
		}
		return module.Unchanged("file already exists with correct content and attributes"), nil
	}
	if destExists && !force {
		return module.Unchanged("destination exists and force=false"), nil
	}

	// Create parent directories if needed
	if createDirs {
		if err := createParentDirs(ctx, conn, dest); err != nil {
			return nil, err
		}
	}

	// Without validation, use the shared deploy helper
	if validate == "" {
		return module.DeployFile(ctx, conn, module.DeployOpts{
			Content: srcContent,
			Dest:    dest,
			Mode:    mode,
			Owner:   owner,
			Group:   group,
			Backup:  backup,
			Label:   "file",
		})
	}

	// Validation flow: upload to temp, validate, move into place
	if destExists && backup {
		if err := module.CreateBackup(ctx, conn, dest); err != nil {
			return nil, fmt.Errorf("failed to create backup: %w", err)
		}
	}

	targetPath := fmt.Sprintf("/tmp/tack-copy-%d", time.Now().UnixNano())
	modeInt, err := module.ParseMode(mode)
	if err != nil {
		return nil, fmt.Errorf("invalid mode: %w", err)
	}

	if err := conn.Upload(ctx, bytes.NewReader(srcContent), targetPath, modeInt); err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	validateCmd := strings.ReplaceAll(validate, "%s", connector.ShellQuote(targetPath))
	result, err := conn.Execute(ctx, validateCmd)
	if err != nil {
		_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(targetPath)))
		return nil, fmt.Errorf("validation command failed: %w", err)
	}
	if result.ExitCode != 0 {
		_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(targetPath)))
		return nil, fmt.Errorf("validation failed: %s", result.Stderr)
	}

	if _, err := connector.Run(ctx, conn, fmt.Sprintf("mv %s %s", connector.ShellQuote(targetPath), connector.ShellQuote(dest))); err != nil {
		return nil, fmt.Errorf("failed to move validated file: %w", err)
	}

	if _, err := module.EnsureAttributes(ctx, conn, dest, mode, owner, group, false); err != nil {
		return nil, err
	}

	msg := "file created"
	if destExists {
		msg = "file updated"
	}
	return module.ChangedWithData(msg, map[string]any{
		"dest":     dest,
		"checksum": srcChecksum,
	}), nil
}

// createParentDirs creates parent directories for a path.
func createParentDirs(ctx context.Context, conn connector.Connector, path string) error {
	dir := filepath.Dir(path)
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("mkdir -p %s", connector.ShellQuote(dir))); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	return nil
}

// Check determines whether the copy module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}

	src := module.GetString(params, "src", "")
	content := module.GetString(params, "content", "")
	mode := module.GetString(params, "mode", "0644")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	force := module.GetBool(params, "force", true)

	if src == "" && content == "" {
		return nil, fmt.Errorf("either 'src' or 'content' parameter is required")
	}
	if src != "" && content != "" {
		return nil, fmt.Errorf("'src' and 'content' are mutually exclusive")
	}

	var srcContent []byte
	if src != "" {
		srcPath := module.ResolveRolePath(src, params, "files")
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read source file '%s': %w", srcPath, err)
		}
		srcContent = data
	} else {
		srcContent = []byte(content)
	}

	// Check force before the shared deploy check
	destExists, _, err := module.GetRemoteChecksum(ctx, conn, dest)
	if err != nil {
		return nil, fmt.Errorf("failed to check destination: %w", err)
	}
	if destExists && !force {
		return module.NoChange("destination exists and force=false"), nil
	}

	diffEnabled, _ := params["_diff_enabled"].(bool)
	return module.CheckDeployFile(ctx, conn, srcContent, dest, mode, owner, group, module.CheckOptions{DiffEnabled: diffEnabled})
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)
