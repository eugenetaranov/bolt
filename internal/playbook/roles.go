package playbook

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LoadRole loads a role from the specified roles directory.
// It looks for the role at rolesDir/name/ and loads tasks, handlers, vars, and defaults.
func LoadRole(name, rolesDir string) (*Role, error) {
	rolePath := filepath.Join(rolesDir, name)

	// Check role directory exists
	info, err := os.Stat(rolePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("role '%s' not found at %s", name, rolePath)
		}
		return nil, fmt.Errorf("error accessing role '%s': %w", name, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("role '%s' is not a directory", name)
	}

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
	// Set RolePath on all tasks so they can find role files
	for _, task := range tasks {
		task.RolePath = rolePath
	}
	role.Tasks = tasks

	// Load handlers/main.yaml (optional)
	handlers, err := loadRoleHandlers(rolePath)
	if err != nil {
		return nil, fmt.Errorf("role '%s': %w", name, err)
	}
	// Set RolePath on all handlers
	for _, handler := range handlers {
		handler.RolePath = rolePath
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
	return loadTasksFile(tasksFile)
}

// loadRoleHandlers loads handlers from handlers/main.yaml in the role directory.
func loadRoleHandlers(rolePath string) ([]*Task, error) {
	handlersFile := filepath.Join(rolePath, "handlers", "main.yaml")
	return loadTasksFile(handlersFile)
}

// loadTasksFile loads a list of tasks from a YAML file.
// Returns empty slice if file doesn't exist.
func loadTasksFile(path string) ([]*Task, error) {
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

// LoadRoles loads all roles specified in the play.
// rolesDir is the base directory to search for roles (typically ./roles relative to playbook).
func LoadRoles(roleNames []string, rolesDir string) ([]*Role, error) {
	if len(roleNames) == 0 {
		return nil, nil
	}

	roles := make([]*Role, 0, len(roleNames))
	for _, name := range roleNames {
		role, err := LoadRole(name, rolesDir)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}

	return roles, nil
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
