// Package file provides a module for managing files and directories.
package file

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

// State represents the desired state of a file or directory.
type State string

const (
	StateFile      State = "file"      // Ensure file exists (error if doesn't exist)
	StateDirectory State = "directory" // Ensure directory exists
	StateLink      State = "link"      // Ensure symlink exists
	StateAbsent    State = "absent"    // Ensure path does not exist
	StateTouch     State = "touch"     // Create empty file or update timestamp
)

// Module manages files and directories on the target system.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "file"
}

// Run executes the file module.
//
// Parameters:
//   - path (string, required): Path to the file or directory
//   - state (string): Desired state - file, directory, link, absent, touch (default: file)
//   - mode (string): File permissions in octal (e.g., "0755", "0644")
//   - owner (string): Owner username
//   - group (string): Group name
//   - src (string): Source path for symlinks (required when state=link)
//   - recurse (bool): Recursively set attributes on directory contents (default: false)
//   - force (bool): Force symlink creation even if destination exists (default: false)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Extract parameters
	path, err := requireString(params, "path")
	if err != nil {
		return nil, err
	}

	stateStr := getString(params, "state", "file")
	state := State(stateStr)

	mode := getString(params, "mode", "")
	owner := getString(params, "owner", "")
	group := getString(params, "group", "")
	src := getString(params, "src", "")
	recurse := getBool(params, "recurse", false)
	force := getBool(params, "force", false)

	// Validate state
	switch state {
	case StateFile, StateDirectory, StateLink, StateAbsent, StateTouch:
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be file, directory, link, absent, or touch", state)
	}

	// Validate symlink parameters
	if state == StateLink && src == "" {
		return nil, fmt.Errorf("'src' parameter is required when state=link")
	}

	// Get current file info
	info, err := getFileInfo(ctx, conn, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	var changed bool
	var messages []string

	// Handle state
	switch state {
	case StateAbsent:
		if info.Exists {
			if err := removePath(ctx, conn, path, info.IsDir); err != nil {
				return nil, err
			}
			changed = true
			messages = append(messages, "path removed")
		} else {
			return module.Unchanged("path already absent"), nil
		}

	case StateDirectory:
		if !info.Exists {
			if err := createDirectory(ctx, conn, path, mode); err != nil {
				return nil, err
			}
			changed = true
			messages = append(messages, "directory created")
		} else if !info.IsDir {
			return nil, fmt.Errorf("path exists but is not a directory")
		}

	case StateFile:
		if !info.Exists {
			return nil, fmt.Errorf("path does not exist; use state=touch to create")
		}
		if info.IsDir {
			return nil, fmt.Errorf("path is a directory, not a file")
		}

	case StateTouch:
		if !info.Exists {
			if err := touchFile(ctx, conn, path); err != nil {
				return nil, err
			}
			changed = true
			messages = append(messages, "file created")
		} else {
			if err := touchFile(ctx, conn, path); err != nil {
				return nil, err
			}
			changed = true
			messages = append(messages, "timestamp updated")
		}

	case StateLink:
		linkChanged, err := ensureSymlink(ctx, conn, src, path, force, info)
		if err != nil {
			return nil, err
		}
		if linkChanged {
			changed = true
			messages = append(messages, "symlink created")
		}
	}

	// Apply mode if specified (and not absent)
	if state != StateAbsent && mode != "" {
		modeChanged, err := ensureMode(ctx, conn, path, mode, recurse && state == StateDirectory)
		if err != nil {
			return nil, err
		}
		if modeChanged {
			changed = true
			messages = append(messages, "mode changed")
		}
	}

	// Apply ownership if specified (and not absent)
	if state != StateAbsent && (owner != "" || group != "") {
		ownerChanged, err := ensureOwnership(ctx, conn, path, owner, group, recurse && state == StateDirectory)
		if err != nil {
			return nil, err
		}
		if ownerChanged {
			changed = true
			messages = append(messages, "ownership changed")
		}
	}

	if !changed {
		return module.Unchanged("no changes needed"), nil
	}

	return module.Changed(strings.Join(messages, ", ")), nil
}

// fileInfo holds information about a path.
type fileInfo struct {
	Exists  bool
	IsDir   bool
	IsLink  bool
	Mode    string
	Owner   string
	Group   string
	LinkDst string
}

// getFileInfo retrieves information about a path.
func getFileInfo(ctx context.Context, conn connector.Connector, path string) (*fileInfo, error) {
	// Use stat to get file info
	// Format: type:mode:owner:group:linktarget
	cmd := fmt.Sprintf(`if [ -e %[1]s ] || [ -L %[1]s ]; then
		type="file"
		[ -d %[1]s ] && type="dir"
		[ -L %[1]s ] && type="link"
		linktarget=""
		[ -L %[1]s ] && linktarget=$(readlink %[1]s)
		stat -f "%%Sp:%%Su:%%Sg" %[1]s 2>/dev/null || stat -c "%%A:%%U:%%G" %[1]s 2>/dev/null
		echo "$type:$linktarget"
	else
		echo "NOTEXIST"
	fi`, shellQuote(path))

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}

	output := strings.TrimSpace(result.Stdout)
	if output == "NOTEXIST" || output == "" {
		return &fileInfo{Exists: false}, nil
	}

	lines := strings.Split(output, "\n")
	info := &fileInfo{Exists: true}

	if len(lines) >= 1 {
		// Parse permissions line (e.g., "drwxr-xr-x:alice:staff" or "-rw-r--r--:alice:staff")
		parts := strings.Split(lines[0], ":")
		if len(parts) >= 3 {
			info.Mode = parts[0]
			info.Owner = parts[1]
			info.Group = parts[2]
		}
	}

	if len(lines) >= 2 {
		// Parse type and link target
		parts := strings.Split(lines[1], ":")
		if len(parts) >= 1 {
			switch parts[0] {
			case "dir":
				info.IsDir = true
			case "link":
				info.IsLink = true
				if len(parts) >= 2 {
					info.LinkDst = parts[1]
				}
			}
		}
	}

	return info, nil
}

// createDirectory creates a directory with optional mode.
func createDirectory(ctx context.Context, conn connector.Connector, path, mode string) error {
	cmd := fmt.Sprintf("mkdir -p %s", shellQuote(path))
	if mode != "" {
		cmd = fmt.Sprintf("mkdir -p -m %s %s", mode, shellQuote(path))
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to create directory: %s", result.Stderr)
	}
	return nil
}

// touchFile creates an empty file or updates its timestamp.
func touchFile(ctx context.Context, conn connector.Connector, path string) error {
	result, err := conn.Execute(ctx, fmt.Sprintf("touch %s", shellQuote(path)))
	if err != nil {
		return fmt.Errorf("failed to touch file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to touch file: %s", result.Stderr)
	}
	return nil
}

// removePath removes a file or directory.
func removePath(ctx context.Context, conn connector.Connector, path string, isDir bool) error {
	cmd := fmt.Sprintf("rm -f %s", shellQuote(path))
	if isDir {
		cmd = fmt.Sprintf("rm -rf %s", shellQuote(path))
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to remove path: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to remove path: %s", result.Stderr)
	}
	return nil
}

// ensureSymlink ensures a symlink exists pointing to src.
func ensureSymlink(ctx context.Context, conn connector.Connector, src, dst string, force bool, info *fileInfo) (bool, error) {
	// Check if symlink already correct
	if info.IsLink && info.LinkDst == src {
		return false, nil
	}

	// If something exists and not forcing, error
	if info.Exists && !force {
		return false, fmt.Errorf("destination exists and force=false")
	}

	// Remove existing if forcing
	if info.Exists && force {
		if err := removePath(ctx, conn, dst, info.IsDir); err != nil {
			return false, err
		}
	}

	// Create symlink
	result, err := conn.Execute(ctx, fmt.Sprintf("ln -s %s %s", shellQuote(src), shellQuote(dst)))
	if err != nil {
		return false, fmt.Errorf("failed to create symlink: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("failed to create symlink: %s", result.Stderr)
	}

	return true, nil
}

// ensureMode ensures a path has the correct mode.
func ensureMode(ctx context.Context, conn connector.Connector, path, mode string, recurse bool) (bool, error) {
	cmd := fmt.Sprintf("chmod %s %s", mode, shellQuote(path))
	if recurse {
		cmd = fmt.Sprintf("chmod -R %s %s", mode, shellQuote(path))
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to set mode: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("failed to set mode: %s", result.Stderr)
	}

	// Note: We always report changed since checking current mode is complex
	// A more sophisticated implementation would compare modes first
	return true, nil
}

// ensureOwnership ensures a path has the correct owner and group.
func ensureOwnership(ctx context.Context, conn connector.Connector, path, owner, group string, recurse bool) (bool, error) {
	var ownership string
	if owner != "" && group != "" {
		ownership = fmt.Sprintf("%s:%s", owner, group)
	} else if owner != "" {
		ownership = owner
	} else if group != "" {
		ownership = fmt.Sprintf(":%s", group)
	} else {
		return false, nil
	}

	cmd := fmt.Sprintf("chown %s %s", ownership, shellQuote(path))
	if recurse {
		cmd = fmt.Sprintf("chown -R %s %s", ownership, shellQuote(path))
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to set ownership: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("failed to set ownership: %s", result.Stderr)
	}

	return true, nil
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
