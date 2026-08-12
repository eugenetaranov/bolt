package playbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tackhq/tack/internal/source"
)

// isRemoteRoleRef reports whether name looks like a remote source
// reference (git, HTTPS/HTTP, or S3 URL) rather than a local filesystem
// path or bare role name.
func isRemoteRoleRef(name string) bool {
	return strings.HasPrefix(name, "git@") ||
		strings.HasPrefix(name, "s3://") ||
		strings.HasPrefix(name, "https://") ||
		strings.HasPrefix(name, "http://")
}

// resolveRemoteRoleSource resolves ref to a source.Source. source.Resolve
// requires an explicit "//path" separating a git/HTTPS repo URL from an
// in-repo path, but most role repos ARE the role with no subdirectory —
// so on that specific failure, retry treating the whole repo as the role.
var resolveRemoteRoleSource = func(ref string) (source.Source, error) {
	src, err := source.Resolve(ref)
	if err == nil {
		return src, nil
	}
	if retrySrc, retryErr := source.Resolve(ref + "//."); retryErr == nil {
		return retrySrc, nil
	}
	return nil, err
}

// deriveRemoteRoleName produces a short, human-readable name from a
// remote role ref, used for Role.Name / Task.RoleName (and thus the
// --roles CLI filter) instead of the full URL.
func deriveRemoteRoleName(ref string) string {
	r := ref
	// Skip past the scheme (https://, http://) before searching for the
	// repo//path separator, so we don't mistake the scheme's own "//" for
	// it (mirrors internal/source.splitRepoPath's searchFrom logic).
	searchFrom := 0
	switch {
	case strings.HasPrefix(r, "https://"):
		searchFrom = len("https://")
	case strings.HasPrefix(r, "http://"):
		searchFrom = len("http://")
	}
	if idx := strings.Index(r[searchFrom:], "//"); idx >= 0 {
		idx += searchFrom
		if path := strings.TrimSuffix(r[idx+2:], "/"); path != "" && path != "." {
			r = path
		} else {
			r = r[:idx]
		}
	}
	r = strings.TrimSuffix(r, ".git")
	if i := strings.LastIndexAny(r, "/:"); i >= 0 {
		r = r[i+1:]
	}
	return r
}

// noopCleanup is used for roles that don't need any temporary directory
// torn down (local roles).
func noopCleanup() {}

// LoadRole loads a role, fetching it first if name is a remote reference
// (git, HTTPS/HTTP, or S3 URL); otherwise it looks for the role at
// rolesDir/name/ as before. The returned cleanup function removes any
// temporary directory created for a remote fetch (a no-op for local
// roles) and must not be called until the role's files are no longer
// needed — copy/template tasks resolve file paths against the role
// directory at execution time, not just at load time, so cleanup should
// be deferred for the lifetime of the play, not called right after
// LoadRole/LoadRoles return.
func LoadRole(ctx context.Context, name, rolesDir string) (*Role, func(), error) {
	if isRemoteRoleRef(name) {
		return loadRemoteRole(ctx, name)
	}
	return loadLocalRole(name, rolesDir)
}

// loadLocalRole resolves name to a local directory under rolesDir (or
// relative to the playbook directory for path-like names) and loads it.
func loadLocalRole(name, rolesDir string) (*Role, func(), error) {
	// If the role name is a path (contains separator or starts with .),
	// resolve relative to the playbook directory (parent of rolesDir)
	// rather than inside the roles/ subdirectory.
	var rolePath string
	switch {
	case filepath.IsAbs(name):
		rolePath = name
	case name != filepath.Base(name):
		// Path-like name (e.g. "../tack-roles/docker", "./custom/role")
		rolePath = filepath.Join(filepath.Dir(rolesDir), name)
	default:
		rolePath = filepath.Join(rolesDir, name)
	}

	info, err := os.Stat(rolePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("role '%s' not found at %s", name, rolePath)
		}
		return nil, nil, fmt.Errorf("error accessing role '%s': %w", name, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("role '%s' is not a directory", name)
	}

	role, err := buildRole(name, rolePath)
	if err != nil {
		return nil, nil, err
	}
	return role, noopCleanup, nil
}

// loadRemoteRole fetches ref (a git, HTTPS/HTTP, or S3 URL) to a
// temporary local directory and loads it as a role.
func loadRemoteRole(ctx context.Context, ref string) (*Role, func(), error) {
	src, err := resolveRemoteRoleSource(ref)
	if err != nil {
		return nil, nil, fmt.Errorf("role '%s': %w", ref, err)
	}

	rolePath, cleanup, err := src.Fetch(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("role '%s': failed to fetch: %w", ref, err)
	}

	info, err := os.Stat(rolePath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("role '%s': fetched path not found: %w", ref, err)
	}
	if !info.IsDir() {
		cleanup()
		return nil, nil, fmt.Errorf("role '%s': fetched path is not a directory (a role must be a directory, not a single file)", ref)
	}

	name := deriveRemoteRoleName(ref)
	role, err := buildRole(name, rolePath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return role, cleanup, nil
}

// buildRole assembles a Role from an already-resolved local directory,
// loading its tasks, handlers, defaults, and vars.
func buildRole(name, rolePath string) (*Role, error) {
	role := &Role{
		Name:     name,
		Path:     rolePath,
		Vars:     make(map[string]any),
		Defaults: make(map[string]any),
	}

	// Load tasks/main.yaml (optional but common)
	tasks, err := loadRoleTasks(rolePath)
	if err != nil {
		return nil, fmt.Errorf("role '%s': %w", name, err)
	}
	// Set RolePath on all tasks so they can find role files, and RoleName so
	// the --roles CLI filter can match them.
	for _, task := range tasks {
		task.RolePath = rolePath
		task.RoleName = name
	}
	role.Tasks = tasks

	// Load handlers/main.yaml (optional)
	handlers, err := loadRoleHandlers(rolePath)
	if err != nil {
		return nil, fmt.Errorf("role '%s': %w", name, err)
	}
	// Set RolePath and RoleName on all handlers
	for _, handler := range handlers {
		handler.RolePath = rolePath
		handler.RoleName = name
	}
	role.Handlers = handlers

	// Load defaults/main.yaml (optional)
	defaults, err := loadRoleVarsFile(filepath.Join(rolePath, "defaults", "main.yaml"))
	if err != nil {
		return nil, fmt.Errorf("role '%s': %w", name, err)
	}
	role.Defaults = defaults

	// Load vars/main.yaml (optional)
	vars, err := loadRoleVarsFile(filepath.Join(rolePath, "vars", "main.yaml"))
	if err != nil {
		return nil, fmt.Errorf("role '%s': %w", name, err)
	}
	role.Vars = vars

	return role, nil
}

// loadRoleTasks loads tasks from tasks/main.yaml in the role directory.
func loadRoleTasks(rolePath string) ([]*Task, error) {
	tasksFile := filepath.Join(rolePath, "tasks", "main.yaml")
	return LoadTasksFile(tasksFile)
}

// loadRoleHandlers loads handlers from handlers/main.yaml in the role directory.
func loadRoleHandlers(rolePath string) ([]*Task, error) {
	handlersFile := filepath.Join(rolePath, "handlers", "main.yaml")
	return LoadTasksFile(handlersFile)
}

// LoadTasksFile loads a list of tasks from a YAML file.
// Returns empty slice if file doesn't exist.
func LoadTasksFile(path string) ([]*Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // File doesn't exist, return empty
		}
		return nil, fmt.Errorf("error reading %s: %w", path, err)
	}

	// Parse as list of raw task maps
	var rawTasks []map[string]any
	if err := yaml.Unmarshal(data, &rawTasks); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", path, err)
	}

	tasks := make([]*Task, 0, len(rawTasks))
	for i, raw := range rawTasks {
		task, err := parseRawTask(raw)
		if err != nil {
			return nil, fmt.Errorf("task %d in %s: %w", i+1, path, err)
		}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

// loadRoleVarsFile loads variables from a YAML file.
// Returns empty map if file doesn't exist.
func loadRoleVarsFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil // File doesn't exist, return empty
		}
		return nil, fmt.Errorf("error reading %s: %w", path, err)
	}

	var vars map[string]any
	if err := yaml.Unmarshal(data, &vars); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", path, err)
	}

	if vars == nil {
		vars = make(map[string]any)
	}

	return vars, nil
}

// LoadRoles loads all roles specified in the play. rolesDir is the base
// directory to search for local roles (typically ./roles relative to the
// playbook); refs that are remote (git, HTTPS/HTTP, or S3) URLs are
// fetched instead. The returned cleanup function tears down every
// temporary directory created for remote roles — see LoadRole's doc for
// why it must be deferred for the lifetime of the play, not called right
// after LoadRoles returns.
func LoadRoles(ctx context.Context, refs []RoleRef, rolesDir string) ([]*Role, func(), error) {
	if len(refs) == 0 {
		return nil, noopCleanup, nil
	}

	roles := make([]*Role, 0, len(refs))
	var cleanups []func()
	cleanupAll := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, ref := range refs {
		role, cleanup, err := LoadRole(ctx, ref.Name, rolesDir)
		if err != nil {
			cleanupAll()
			return nil, nil, err
		}
		cleanups = append(cleanups, cleanup)

		// Apply role-level tags to all tasks and handlers in the role
		if len(ref.Tags) > 0 {
			for _, task := range role.Tasks {
				task.Tags = append(ref.Tags, task.Tags...)
			}
			for _, handler := range role.Handlers {
				handler.Tags = append(ref.Tags, handler.Tags...)
			}
		}
		roles = append(roles, role)
	}

	return roles, cleanupAll, nil
}

// MergeRoleVars merges role defaults, role vars, and play vars in the correct precedence order.
// Precedence (lowest to highest): role defaults < role vars < play vars
func MergeRoleVars(roles []*Role, playVars map[string]any) map[string]any {
	merged := make(map[string]any)

	// First, merge all role defaults (lowest priority)
	for _, role := range roles {
		for k, v := range role.Defaults {
			merged[k] = v
		}
	}

	// Then, merge all role vars
	for _, role := range roles {
		for k, v := range role.Vars {
			merged[k] = v
		}
	}

	// Finally, merge play vars (highest priority)
	for k, v := range playVars {
		merged[k] = v
	}

	return merged
}

// ExpandRoleTasks prepends role tasks to play tasks.
// Role tasks are added in the order roles are listed.
func ExpandRoleTasks(roles []*Role, playTasks []*Task) []*Task {
	var allTasks []*Task

	// Add role tasks first
	for _, role := range roles {
		allTasks = append(allTasks, role.Tasks...)
	}

	// Then add play tasks
	allTasks = append(allTasks, playTasks...)

	return allTasks
}

// ExpandRoleHandlers merges role handlers with play handlers.
// Role handlers are added first, then play handlers.
func ExpandRoleHandlers(roles []*Role, playHandlers []*Task) []*Task {
	var allHandlers []*Task

	// Add role handlers first
	for _, role := range roles {
		allHandlers = append(allHandlers, role.Handlers...)
	}

	// Then add play handlers
	allHandlers = append(allHandlers, playHandlers...)

	return allHandlers
}
