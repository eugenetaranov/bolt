// Package file provides a module for managing files and directories.
package file

import (
	"context"
	"fmt"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
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

// Example returns a usage example for the file module.
func (m *Module) Example() string {
	return `- name: Ensure a directory exists
  file:
    path: /etc/app
    state: directory
    owner: root
    mode: "0755"`
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
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "file")
	state := State(stateStr)

	mode := module.GetString(params, "mode", "")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	src := module.GetString(params, "src", "")
	recurse := module.GetBool(params, "recurse", false)
	force := module.GetBool(params, "force", false)

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

	// Apply attributes if specified (and not absent)
	if state != StateAbsent && (mode != "" || owner != "" || group != "") {
		attrChanged, err := module.EnsureAttributes(ctx, conn, path, mode, owner, group, recurse && state == StateDirectory)
		if err != nil {
			return nil, err
		}
		if attrChanged {
			changed = true
			messages = append(messages, "attributes changed")
		}
	}

	if !changed {
		return module.Unchanged("no changes needed"), nil
	}

	return module.Changed(strings.Join(messages, ", ")), nil
}

// fileInfo holds information about a path.
type fileInfo struct {
	Exists    bool
	IsDir     bool
	IsLink    bool
	Mode      string
	OctalMode string
	Owner     string
	Group     string
	LinkDst   string
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
		if stat -c "%%A" /dev/null >/dev/null 2>&1; then
			stat -c "%%a" %[1]s
			stat -c "%%A:%%U:%%G" %[1]s
		else
			stat -f "%%OLp" %[1]s
			stat -f "%%Sp:%%Su:%%Sg" %[1]s
		fi
		echo "$type:$linktarget"
	else
		echo "NOTEXIST"
	fi`, connector.ShellQuote(path))

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
		// Parse octal mode line (e.g., "755")
		info.OctalMode = strings.TrimSpace(lines[0])
	}

	if len(lines) >= 2 {
		// Parse permissions line (e.g., "drwxr-xr-x:alice:staff" or "-rw-r--r--:alice:staff")
		parts := strings.Split(lines[1], ":")
		if len(parts) >= 3 {
			info.Mode = parts[0]
			info.Owner = parts[1]
			info.Group = parts[2]
		}
	}

	if len(lines) >= 3 {
		// Parse type and link target
		parts := strings.Split(lines[2], ":")
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
	cmd := fmt.Sprintf("mkdir -p %s", connector.ShellQuote(path))
	if mode != "" {
		cmd = fmt.Sprintf("mkdir -p -m %s %s", mode, connector.ShellQuote(path))
	}

	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return nil
}

// touchFile creates an empty file or updates its timestamp.
func touchFile(ctx context.Context, conn connector.Connector, path string) error {
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("touch %s", connector.ShellQuote(path))); err != nil {
		return fmt.Errorf("failed to touch file: %w", err)
	}
	return nil
}

// removePath removes a file or directory.
func removePath(ctx context.Context, conn connector.Connector, path string, isDir bool) error {
	cmd := fmt.Sprintf("rm -f %s", connector.ShellQuote(path))
	if isDir {
		cmd = fmt.Sprintf("rm -rf %s", connector.ShellQuote(path))
	}

	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("failed to remove path: %w", err)
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
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("ln -s %s %s", connector.ShellQuote(src), connector.ShellQuote(dst))); err != nil {
		return false, fmt.Errorf("failed to create symlink: %w", err)
	}

	return true, nil
}

// Check determines whether the file module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "file")
	state := State(stateStr)
	mode := module.GetString(params, "mode", "")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	src := module.GetString(params, "src", "")

	switch state {
	case StateFile, StateDirectory, StateLink, StateAbsent, StateTouch:
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be file, directory, link, absent, or touch", state)
	}

	info, err := getFileInfo(ctx, conn, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	switch state {
	case StateAbsent:
		if info.Exists {
			return module.WouldChange("path would be removed"), nil
		}
		return module.NoChange("path already absent"), nil

	case StateDirectory:
		if !info.Exists {
			return module.WouldChange("directory would be created"), nil
		}
		if !info.IsDir {
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
		return module.WouldChange("touch always updates timestamp"), nil

	case StateLink:
		if src == "" {
			return nil, fmt.Errorf("'src' parameter is required when state=link")
		}
		if !info.IsLink || info.LinkDst != src {
			return module.WouldChange("symlink would be created/updated"), nil
		}
	}

	// Check mode/owner attributes for states that support them
	if state != StateAbsent && (mode != "" || owner != "" || group != "") {
		attrDiffer, err := module.CheckAttributes(ctx, conn, path, mode, owner, group)
		if err != nil {
			return nil, err
		}
		if attrDiffer {
			return module.WouldChange("attributes differ"), nil
		}
	}

	return module.NoChange("no changes needed"), nil
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)

// Ensure Module implements the module.Describer interface.
var _ module.Describer = (*Module)(nil)

// Description returns a short summary of the file module.
func (m *Module) Description() string {
	return "Manage files, directories, and symlinks on the target system."
}

// Parameters returns the parameter documentation for the file module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "path", Type: "string", Required: true, Description: "Path to the file or directory"},
		{Name: "state", Type: "string", Default: "file", Description: "Desired state: file, directory, link, absent, touch"},
		{Name: "mode", Type: "string", Description: "File permissions in octal (e.g. 0755, 0644)"},
		{Name: "owner", Type: "string", Description: "Owner username"},
		{Name: "group", Type: "string", Description: "Group name"},
		{Name: "src", Type: "string", Description: "Source path for symlinks (required when state=link)"},
		{Name: "recurse", Type: "bool", Default: "false", Description: "Recursively set attributes on directory contents"},
		{Name: "force", Type: "bool", Default: "false", Description: "Force symlink creation even if destination exists"},
	}
}
