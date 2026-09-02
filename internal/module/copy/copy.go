// Package copy provides a module for copying files to target systems.
package copy

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
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
	return "Copy a file or directory tree to the target from a controller source or inline content."
}

// Parameters returns the parameter documentation for the copy module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "dest", Type: "string", Required: true, Description: "Destination path on the target"},
		{Name: "src", Type: "string", Description: "Source file or directory path on the controller (mutually exclusive with content)"},
		{Name: "content", Type: "string", Description: "Inline content to write (mutually exclusive with src)"},
		{Name: "mode", Type: "string", Default: "0644", Description: "File permissions in octal"},
		{Name: "dir_mode", Type: "string", Default: "0755", Description: "Directory permissions in octal (directory sync only)"},
		{Name: "owner", Type: "string", Description: "Owner username"},
		{Name: "group", Type: "string", Description: "Group name"},
		{Name: "backup", Type: "bool", Default: "false", Description: "Create a backup before overwriting"},
		{Name: "force", Type: "bool", Default: "true", Description: "Overwrite even if the destination exists"},
		{Name: "create_dirs", Type: "bool", Default: "false", Description: "Create parent directories if needed"},
		{Name: "delete", Type: "bool", Default: "false", Description: "Remove target files not present in src (directory sync only)"},
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
	dirMode := module.GetString(params, "dir_mode", "0755")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	backup := module.GetBool(params, "backup", false)
	force := module.GetBool(params, "force", true)
	createDirs := module.GetBool(params, "create_dirs", false)
	deleteExtra := module.GetBool(params, "delete", false)
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

		info, err := os.Stat(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat source '%s': %w", srcPath, err)
		}
		if info.IsDir() {
			if validate != "" {
				return nil, fmt.Errorf("'validate' is not supported when 'src' is a directory")
			}
			return runDirSync(ctx, conn, src, srcPath, dest, mode, dirMode, owner, group, backup, deleteExtra)
		}

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
func createParentDirs(ctx context.Context, conn connector.Connector, dest string) error {
	dir := filepath.Dir(dest)
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("mkdir -p %s", connector.ShellQuote(dir))); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	return nil
}

// syncRoot resolves the remote root path for a directory sync, honoring the
// trailing-slash convention: a src ending in "/" syncs its *contents* into
// dest, while a src without a trailing slash syncs the directory itself into
// dest/<basename>.
func syncRoot(srcParam, srcPath, dest string) string {
	if strings.HasSuffix(srcParam, "/") {
		return path.Clean(dest)
	}
	return path.Join(dest, filepath.Base(srcPath))
}

// runDirSync recursively syncs a local directory tree to the target: creating
// remote directories, uploading each file idempotently (checksum-skip via
// module.DeployFile), recreating symlinks, applying a uniform file mode /
// dir_mode / owner / group, and optionally pruning target entries no longer
// present in the source.
func runDirSync(ctx context.Context, conn connector.Connector, srcParam, srcPath, dest, mode, dirMode, owner, group string, backup, deleteExtra bool) (*module.Result, error) {
	remoteRoot := syncRoot(srcParam, srcPath, dest)

	if _, err := connector.Run(ctx, conn, "mkdir -p "+connector.ShellQuote(remoteRoot)); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}

	// expected tracks every remote path the source implies, for prune.
	expected := map[string]bool{remoteRoot: true}
	var fileCount, changed int

	walkErr := filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcPath, p)
		if err != nil {
			return err
		}
		if rel == "." {
			// The sync root itself; ensure its attributes match.
			if _, err := module.EnsureAttributes(ctx, conn, remoteRoot, dirMode, owner, group, false); err != nil {
				return err
			}
			return nil
		}

		remote := path.Join(remoteRoot, filepath.ToSlash(rel))
		expected[remote] = true

		switch {
		case d.IsDir():
			if _, err := connector.Run(ctx, conn, "mkdir -p "+connector.ShellQuote(remote)); err != nil {
				return fmt.Errorf("failed to create directory '%s': %w", remote, err)
			}
			attrChanged, err := module.EnsureAttributes(ctx, conn, remote, dirMode, owner, group, false)
			if err != nil {
				return err
			}
			if attrChanged {
				changed++
			}

		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("failed to read symlink '%s': %w", p, err)
			}
			cur, err := conn.Execute(ctx, "readlink "+connector.ShellQuote(remote))
			if err != nil {
				return fmt.Errorf("failed to inspect '%s': %w", remote, err)
			}
			if cur.ExitCode == 0 && strings.TrimSpace(cur.Stdout) == target {
				break
			}
			if _, err := connector.Run(ctx, conn, fmt.Sprintf("ln -sfn %s %s", connector.ShellQuote(target), connector.ShellQuote(remote))); err != nil {
				return fmt.Errorf("failed to create symlink '%s': %w", remote, err)
			}
			changed++

		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("failed to read '%s': %w", p, err)
			}
			res, err := module.DeployFile(ctx, conn, module.DeployOpts{
				Content: data,
				Dest:    remote,
				Mode:    mode,
				Owner:   owner,
				Group:   group,
				Backup:  backup,
				Label:   "file",
			})
			if err != nil {
				return err
			}
			fileCount++
			if res.Changed {
				changed++
			}

		default:
			// Skip special files (sockets, devices, named pipes).
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	deleted := 0
	if deleteExtra {
		n, err := pruneExtras(ctx, conn, remoteRoot, expected)
		if err != nil {
			return nil, err
		}
		deleted = n
	}

	if changed == 0 && deleted == 0 {
		return module.Unchanged(fmt.Sprintf("directory already in sync (%d files)", fileCount)), nil
	}
	return module.Changed(fmt.Sprintf("synced %d files, %d changed, %d deleted", fileCount, changed, deleted)), nil
}

// findExtras lists remote paths under root that are not in the expected set,
// deepest-first so files sort before their parent directories. A missing or
// unusable find returns no extras rather than an error.
func findExtras(ctx context.Context, conn connector.Connector, root string, expected map[string]bool) ([]string, error) {
	if root == "" || root == "/" {
		return nil, fmt.Errorf("refusing to prune unsafe root %q", root)
	}
	res, err := conn.Execute(ctx, fmt.Sprintf("find %s -mindepth 1 -depth 2>/dev/null", connector.ShellQuote(root)))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, nil
	}
	var extras []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || expected[entry] {
			continue
		}
		extras = append(extras, entry)
	}
	return extras, nil
}

// pruneExtras removes remote paths under root that are not in the expected set.
func pruneExtras(ctx context.Context, conn connector.Connector, root string, expected map[string]bool) (int, error) {
	extras, err := findExtras(ctx, conn, root, expected)
	if err != nil {
		return 0, err
	}
	for _, entry := range extras {
		if _, err := connector.Run(ctx, conn, "rm -rf "+connector.ShellQuote(entry)); err != nil {
			return 0, fmt.Errorf("failed to remove '%s': %w", entry, err)
		}
	}
	return len(extras), nil
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
	dirMode := module.GetString(params, "dir_mode", "0755")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	force := module.GetBool(params, "force", true)
	deleteExtra := module.GetBool(params, "delete", false)

	if src == "" && content == "" {
		return nil, fmt.Errorf("either 'src' or 'content' parameter is required")
	}
	if src != "" && content != "" {
		return nil, fmt.Errorf("'src' and 'content' are mutually exclusive")
	}

	var srcContent []byte
	if src != "" {
		srcPath := module.ResolveRolePath(src, params, "files")
		info, err := os.Stat(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat source '%s': %w", srcPath, err)
		}
		if info.IsDir() {
			return checkDirSync(ctx, conn, src, srcPath, dest, mode, dirMode, owner, group, deleteExtra)
		}
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

// checkDirSync reports whether a directory sync would make changes without
// applying them.
func checkDirSync(ctx context.Context, conn connector.Connector, srcParam, srcPath, dest, mode, dirMode, owner, group string, deleteExtra bool) (*module.CheckResult, error) {
	remoteRoot := syncRoot(srcParam, srcPath, dest)
	expected := map[string]bool{remoteRoot: true}
	var wouldChange, fileCount int

	walkErr := filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcPath, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		remote := path.Join(remoteRoot, filepath.ToSlash(rel))
		expected[remote] = true

		switch {
		case d.IsDir():
			res, err := conn.Execute(ctx, fmt.Sprintf("test -d %s", connector.ShellQuote(remote)))
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				wouldChange++
				return nil
			}
			differ, err := module.CheckAttributes(ctx, conn, remote, dirMode, owner, group)
			if err != nil {
				return err
			}
			if differ {
				wouldChange++
			}

		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("failed to read symlink '%s': %w", p, err)
			}
			cur, err := conn.Execute(ctx, "readlink "+connector.ShellQuote(remote))
			if err != nil {
				return err
			}
			if cur.ExitCode != 0 || strings.TrimSpace(cur.Stdout) != target {
				wouldChange++
			}

		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("failed to read '%s': %w", p, err)
			}
			fileCount++
			cr, err := module.CheckDeployFile(ctx, conn, data, remote, mode, owner, group)
			if err != nil {
				return err
			}
			if cr.WouldChange || cr.Uncertain {
				wouldChange++
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	if deleteExtra {
		extras, err := findExtras(ctx, conn, remoteRoot, expected)
		if err != nil {
			return nil, err
		}
		wouldChange += len(extras)
	}

	if wouldChange == 0 {
		return module.NoChange(fmt.Sprintf("directory already in sync (%d files)", fileCount)), nil
	}
	return module.WouldChange(fmt.Sprintf("%d path(s) would change", wouldChange)), nil
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)
