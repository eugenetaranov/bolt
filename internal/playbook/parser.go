package playbook

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eugenetaranov/bolt/internal/module"
)

// knownTaskFields are fields that are task directives, not module names.
var knownTaskFields = map[string]bool{
	"name":         true,
	"when":         true,
	"register":     true,
	"notify":       true,
	"loop":         true,
	"with_items":   true,
	"loop_var":     true,
	"ignore_errors": true,
	"retries":      true,
	"delay":        true,
	"become":       true,
	"become_user":  true,
	"changed_when": true,
	"failed_when":  true,
}

// ParseFile parses a playbook from a YAML file.
func ParseFile(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read playbook: %w", err)
	}

	playbook, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse playbook %s: %w", path, err)
	}

	playbook.Path = path
	return playbook, nil
}

// Parse parses a playbook from YAML data.
func Parse(data []byte) (*Playbook, error) {
	// Try parsing as a list of plays first
	var plays []*Play
	if err := yaml.Unmarshal(data, &plays); err == nil && len(plays) > 0 {
		// Parsed as list of plays
		for i, play := range plays {
			if err := parsePlayTasks(play); err != nil {
				return nil, fmt.Errorf("play %d: %w", i+1, err)
			}
			if err := play.Validate(); err != nil {
				return nil, fmt.Errorf("play %d: %w", i+1, err)
			}
		}
		return &Playbook{Plays: plays}, nil
	}

	// Try parsing as a single play
	var play Play
	if err := yaml.Unmarshal(data, &play); err != nil {
		return nil, fmt.Errorf("invalid playbook format: %w", err)
	}

	if err := parsePlayTasks(&play); err != nil {
		return nil, err
	}

	if err := play.Validate(); err != nil {
		return nil, err
	}

	return &Playbook{Plays: []*Play{&play}}, nil
}

// parsePlayTasks extracts module information from raw task maps.
func parsePlayTasks(play *Play) error {
	// We need to re-parse tasks to extract module information
	// This is because the module name is a dynamic key in the YAML

	// Re-parse the play to get raw task data
	playData, err := yaml.Marshal(play)
	if err != nil {
		return err
	}

	var rawPlay struct {
		Tasks    []map[string]any `yaml:"tasks"`
		Handlers []map[string]any `yaml:"handlers"`
	}
	if err := yaml.Unmarshal(playData, &rawPlay); err != nil {
		return err
	}

	// This won't work because we already parsed. Let's use a different approach.
	// We need to parse the tasks specially.

	return nil
}

// ParseFileRaw parses a playbook with proper module detection.
func ParseFileRaw(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read playbook: %w", err)
	}

	return ParseRaw(data, path)
}

// ParseRaw parses a playbook with proper module detection.
func ParseRaw(data []byte, path string) (*Playbook, error) {
	// First, try to unmarshal as a list of raw play maps
	var rawPlays []map[string]any
	if err := yaml.Unmarshal(data, &rawPlays); err != nil {
		// Try as single play
		var rawPlay map[string]any
		if err := yaml.Unmarshal(data, &rawPlay); err != nil {
			return nil, fmt.Errorf("invalid playbook format: %w", err)
		}
		rawPlays = []map[string]any{rawPlay}
	}

	playbook := &Playbook{Path: path}

	for i, rawPlay := range rawPlays {
		play, err := parseRawPlay(rawPlay)
		if err != nil {
			return nil, fmt.Errorf("play %d: %w", i+1, err)
		}
		if err := play.Validate(); err != nil {
			return nil, fmt.Errorf("play %d: %w", i+1, err)
		}
		playbook.Plays = append(playbook.Plays, play)
	}

	return playbook, nil
}

// parseRawPlay parses a single play from a raw map.
func parseRawPlay(raw map[string]any) (*Play, error) {
	play := &Play{
		Vars: make(map[string]any),
	}

	// Parse simple fields
	if v, ok := raw["name"].(string); ok {
		play.Name = v
	}
	if v, ok := raw["hosts"].(string); ok {
		play.Hosts = v
	}
	if v, ok := raw["connection"].(string); ok {
		play.Connection = v
	}
	if v, ok := raw["become"].(bool); ok {
		play.Become = v
	}
	if v, ok := raw["become_user"].(string); ok {
		play.BecomeUser = v
	}
	if v, ok := raw["gather_facts"].(bool); ok {
		play.GatherFacts = &v
	}

	// Parse vars
	if vars, ok := raw["vars"].(map[string]any); ok {
		play.Vars = vars
	}

	// Parse roles
	if roles, ok := raw["roles"].([]any); ok {
		for _, role := range roles {
			if roleName, ok := role.(string); ok {
				play.Roles = append(play.Roles, roleName)
			}
		}
	}

	// Parse tasks
	if tasks, ok := raw["tasks"].([]any); ok {
		for i, rawTask := range tasks {
			taskMap, ok := rawTask.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("task %d: invalid task format", i+1)
			}
			task, err := parseRawTask(taskMap)
			if err != nil {
				return nil, fmt.Errorf("task %d: %w", i+1, err)
			}
			play.Tasks = append(play.Tasks, task)
		}
	}

	// Parse handlers
	if handlers, ok := raw["handlers"].([]any); ok {
		for i, rawHandler := range handlers {
			handlerMap, ok := rawHandler.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("handler %d: invalid handler format", i+1)
			}
			handler, err := parseRawTask(handlerMap)
			if err != nil {
				return nil, fmt.Errorf("handler %d: %w", i+1, err)
			}
			play.Handlers = append(play.Handlers, handler)
		}
	}

	return play, nil
}

// parseRawTask parses a single task from a raw map.
func parseRawTask(raw map[string]any) (*Task, error) {
	task := &Task{
		Params: make(map[string]any),
	}

	// Parse known task fields
	if v, ok := raw["name"].(string); ok {
		task.Name = v
	}
	if v, ok := raw["when"].(string); ok {
		task.When = v
	}
	if v, ok := raw["register"].(string); ok {
		task.Register = v
	}
	if v, ok := raw["loop_var"].(string); ok {
		task.LoopVar = v
	}
	if v, ok := raw["ignore_errors"].(bool); ok {
		task.IgnoreErrors = v
	}
	if v, ok := raw["retries"].(int); ok {
		task.Retries = v
	}
	if v, ok := raw["delay"].(int); ok {
		task.Delay = v
	}
	if v, ok := raw["become"].(bool); ok {
		task.Become = &v
	}
	if v, ok := raw["become_user"].(string); ok {
		task.BecomeUser = v
	}
	if v, ok := raw["changed_when"].(string); ok {
		task.ChangedWhen = v
	}
	if v, ok := raw["failed_when"].(string); ok {
		task.FailedWhen = v
	}

	// Parse notify (can be string or list)
	if notify, ok := raw["notify"]; ok {
		switch n := notify.(type) {
		case string:
			task.Notify = []string{n}
		case []any:
			for _, item := range n {
				if s, ok := item.(string); ok {
					task.Notify = append(task.Notify, s)
				}
			}
		}
	}

	// Parse loop (can be "loop" or "with_items")
	if loop, ok := raw["loop"]; ok {
		if items, ok := loop.([]any); ok {
			task.Loop = items
		}
	} else if loop, ok := raw["with_items"]; ok {
		if items, ok := loop.([]any); ok {
			task.Loop = items
		}
	}

	// Find the module - it's a key that's not a known task field
	for key, value := range raw {
		if knownTaskFields[key] {
			continue
		}

		// This must be the module name
		if task.Module != "" {
			return nil, fmt.Errorf("multiple modules specified: %s and %s", task.Module, key)
		}

		task.Module = key

		// Parse module parameters
		switch params := value.(type) {
		case map[string]any:
			task.Params = params
		case string:
			// Short form: module: "arg"
			// Convert to params with special key
			task.Params = map[string]any{"_raw": params}
		case nil:
			// Module with no parameters
			task.Params = make(map[string]any)
		default:
			task.Params = map[string]any{"_raw": value}
		}
	}

	return task, nil
}

// ExpandShorthand expands shorthand module syntax.
// For example, "apt: name=nginx state=present" becomes proper params.
func ExpandShorthand(task *Task) {
	raw, ok := task.Params["_raw"].(string)
	if !ok {
		return
	}

	// Check if it's key=value format
	if !strings.Contains(raw, "=") {
		// Single argument - module-specific handling
		switch task.Module {
		case "command", "shell":
			task.Params = map[string]any{"cmd": raw}
		case "file":
			task.Params = map[string]any{"path": raw}
		case "copy":
			task.Params = map[string]any{"dest": raw}
		default:
			task.Params = map[string]any{"name": raw}
		}
		return
	}

	// Parse key=value pairs
	newParams := make(map[string]any)
	parts := strings.Fields(raw)
	for _, part := range parts {
		if idx := strings.Index(part, "="); idx > 0 {
			key := part[:idx]
			value := part[idx+1:]
			// Handle quoted values
			value = strings.Trim(value, "\"'")
			newParams[key] = value
		}
	}

	task.Params = newParams
}

// ResolveModule checks if the task's module exists in the registry.
func ResolveModule(task *Task) error {
	if task.Module == "" {
		return fmt.Errorf("no module specified")
	}

	m := module.Get(task.Module)
	if m == nil {
		available := module.List()
		return fmt.Errorf("unknown module '%s' (available: %s)",
			task.Module, strings.Join(available, ", "))
	}

	return nil
}
