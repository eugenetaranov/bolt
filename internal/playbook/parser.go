package playbook

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tackhq/tack/internal/module"
)

// asBool converts a YAML value to a bool, handling both native booleans
// and YAML 1.1 string booleans (yes/no/on/off) that yaml.v3 parses as strings.
func asBool(v any) (bool, bool) {
	switch val := v.(type) {
	case bool:
		return val, true
	case string:
		switch strings.ToLower(val) {
		case "yes", "on", "true":
			return true, true
		case "no", "off", "false":
			return false, true
		}
	}
	return false, false
}

// knownTaskFields are fields that are task directives, not module names.
var knownTaskFields = map[string]bool{
	"name":          true,
	"when":          true,
	"register":      true,
	"notify":        true,
	"loop":          true,
	"with_items":    true,
	"loop_var":      true,
	"ignore_errors": true,
	"retries":       true,
	"delay":         true,
	"sudo":          true,
	"changed_when":  true,
	"failed_when":   true,
	"no_log":        true,
	"include":       true,
	"include_tasks": true,
	"vars":          true,
	// Block/rescue/always directives
	"block":  true,
	"rescue": true,
	"always": true,
	// Assert built-in task keyword
	"assert": true,
	// Tags
	"tags": true,
	// Module argument keys that Ansible allows at task level
	"args":    true,
	"creates": true,
	"removes": true,
	"chdir":   true,
}

// parseStringOrList converts a YAML value to a string slice.
// Accepts a single string or a list of strings; returns nil for other types.
func parseStringOrList(v any) []string {
	switch val := v.(type) {
	case string:
		return []string{val}
	case []any:
		var result []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
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
//
// Two top-level shapes are accepted:
//
//  1. A YAML sequence of plays (legacy): each element is a play.
//  2. A YAML mapping with a `plays:` sequence (new): the mapping carries
//     playbook-level defaults (`hosts`, `connection`, `sudo`, `vars`) that
//     are merged into each play under `plays:` at parse time.
//
// A YAML mapping at the root without a `plays:` key is treated as a single
// play (existing fallback) with no defaults applied.
func ParseRaw(data []byte, path string) (*Playbook, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid playbook format: %w", err)
	}

	// Unwrap document node.
	node := &root
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, fmt.Errorf("invalid playbook format: empty document")
		}
		node = node.Content[0]
	}

	var (
		rawPlays []map[string]any
		defaults *PlaybookDefaults
	)

	switch node.Kind {
	case yaml.SequenceNode:
		// Legacy sequence-of-plays format.
		if err := node.Decode(&rawPlays); err != nil {
			return nil, fmt.Errorf("invalid playbook format: %w", err)
		}

	case yaml.MappingNode:
		var rawTop map[string]any
		if err := node.Decode(&rawTop); err != nil {
			return nil, fmt.Errorf("invalid playbook format: %w", err)
		}
		if _, hasPlays := rawTop["plays"]; hasPlays {
			d, plays, err := parsePlaybookMapping(rawTop)
			if err != nil {
				return nil, err
			}
			defaults = d
			rawPlays = plays
		} else {
			// Single-play map at root (legacy fallback).
			rawPlays = []map[string]any{rawTop}
		}

	default:
		return nil, fmt.Errorf("invalid playbook format: expected a sequence of plays or a mapping with a 'plays:' key")
	}

	playbook := &Playbook{Path: path, Defaults: defaults}

	for i, rawPlay := range rawPlays {
		play, err := parseRawPlay(rawPlay)
		if err != nil {
			return nil, fmt.Errorf("play %d: %w", i+1, err)
		}
		applyPlaybookDefaults(play, defaults)
		if err := play.Validate(); err != nil {
			return nil, fmt.Errorf("play %d: %w", i+1, err)
		}
		playbook.Plays = append(playbook.Plays, play)
	}

	return playbook, nil
}

// parsePlaybookMapping pulls playbook-level defaults out of a mapping-format
// playbook and returns the raw plays slice. The caller has already verified
// that `plays` is present in raw.
func parsePlaybookMapping(raw map[string]any) (*PlaybookDefaults, []map[string]any, error) {
	playsRaw, ok := raw["plays"].([]any)
	if !ok {
		return nil, nil, fmt.Errorf("invalid playbook format: 'plays' must be a sequence of plays")
	}

	rawPlays := make([]map[string]any, 0, len(playsRaw))
	for i, item := range playsRaw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid playbook format: plays[%d] must be a mapping", i)
		}
		rawPlays = append(rawPlays, m)
	}

	defaults := &PlaybookDefaults{}

	if v, ok := raw["hosts"]; ok {
		hosts := parseStringOrList(v)
		if hosts == nil {
			return nil, nil, fmt.Errorf("invalid playbook format: 'hosts' must be a string or list of strings")
		}
		defaults.Hosts = hosts
	}

	if v, ok := raw["connection"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, nil, fmt.Errorf("invalid playbook format: 'connection' must be a string")
		}
		defaults.Connection = s
	}

	if v, ok := raw["sudo"]; ok {
		b, ok := asBool(v)
		if !ok {
			return nil, nil, fmt.Errorf("invalid playbook format: 'sudo' must be a boolean")
		}
		defaults.Sudo = b
	}

	if v, ok := raw["vars"]; ok {
		vars, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid playbook format: 'vars' must be a mapping")
		}
		defaults.Vars = vars
	}

	return defaults, rawPlays, nil
}

// applyPlaybookDefaults merges playbook-level defaults into a play, leaving
// fields the play already set untouched. Only `Hosts`, `Connection`, `Sudo`,
// and `Vars` participate in inheritance — see design.md for rationale.
func applyPlaybookDefaults(play *Play, defaults *PlaybookDefaults) {
	if defaults == nil {
		return
	}
	if len(play.Hosts) == 0 && len(defaults.Hosts) > 0 {
		play.Hosts = append([]string(nil), defaults.Hosts...)
	}
	if play.Connection == "" && defaults.Connection != "" {
		play.Connection = defaults.Connection
	}
	if defaults.Sudo && !play.Sudo {
		// Playbook-level `sudo: false` is intentionally a no-op (see design.md):
		// we cannot distinguish "unset" from "false" on a plain bool, so only
		// `sudo: true` propagates.
		play.Sudo = true
	}
	if len(defaults.Vars) > 0 {
		merged := make(map[string]any, len(defaults.Vars)+len(play.Vars))
		for k, v := range defaults.Vars {
			merged[k] = v
		}
		for k, v := range play.Vars {
			merged[k] = v
		}
		play.Vars = merged
	}
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
	switch v := raw["hosts"].(type) {
	case string:
		play.Hosts = []string{v}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				play.Hosts = append(play.Hosts, s)
			}
		}
	}
	if v, ok := raw["connection"].(string); ok {
		play.Connection = v
	}
	if v, ok := asBool(raw["sudo"]); ok {
		play.Sudo = v
	}
	if v, ok := raw["gather_facts"].(bool); ok {
		play.GatherFacts = &v
	}

	// Parse vars
	if vars, ok := raw["vars"].(map[string]any); ok {
		play.Vars = vars
	}

	// Parse sudo_password
	if v, ok := raw["sudo_password"].(string); ok {
		play.SudoPassword = v
	}

	// Parse ssh config block
	if sshRaw, ok := raw["ssh"].(map[string]any); ok {
		play.SSH = parseSSHConfig(sshRaw)
	}

	// Parse ssm config block
	if ssmRaw, ok := raw["ssm"].(map[string]any); ok {
		play.SSM = parseSSMConfig(ssmRaw)
	}

	// Parse vars_files
	if vf, ok := raw["vars_files"].([]any); ok {
		for _, item := range vf {
			if s, ok := item.(string); ok {
				play.VarsFiles = append(play.VarsFiles, s)
			}
		}
	}

	// Parse vault_file
	if v, ok := raw["vault_file"].(string); ok {
		play.VaultFile = v
	}

	// Parse tags on play
	play.Tags = parseStringOrList(raw["tags"])

	// Parse roles (string or map form)
	if roles, ok := raw["roles"].([]any); ok {
		for _, role := range roles {
			switch r := role.(type) {
			case string:
				play.Roles = append(play.Roles, RoleRef{Name: r})
			case map[string]any:
				ref := RoleRef{}
				if name, ok := r["role"].(string); ok {
					ref.Name = name
				}
				ref.Tags = parseStringOrList(r["tags"])
				if vars, ok := r["vars"].(map[string]any); ok {
					ref.Vars = vars
				}
				play.Roles = append(play.Roles, ref)
			}
		}
	}

	// Parse tasks
	if items, ok := raw["tasks"].([]any); ok {
		tasks, err := parseTaskList(items, "task")
		if err != nil {
			return nil, err
		}
		play.Tasks = tasks
	}

	// Parse handlers
	if items, ok := raw["handlers"].([]any); ok {
		handlers, err := parseTaskList(items, "handler")
		if err != nil {
			return nil, err
		}
		play.Handlers = handlers
	}

	return play, nil
}

// parseTaskList parses a list of raw task/handler maps into Task structs.
func parseTaskList(items []any, itemType string) ([]*Task, error) {
	var tasks []*Task
	for i, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s %d: invalid %s format", itemType, i+1, itemType)
		}
		task, err := parseRawTask(m)
		if err != nil {
			return nil, fmt.Errorf("%s %d: %w", itemType, i+1, err)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
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
	if v, ok := asBool(raw["sudo"]); ok {
		task.Sudo = &v
	}
	if v, ok := raw["changed_when"].(string); ok {
		task.ChangedWhen = v
	}
	if v, ok := raw["failed_when"].(string); ok {
		task.FailedWhen = v
	}
	if v, ok := asBool(raw["no_log"]); ok {
		task.NoLog = v
	}
	if v, ok := raw["include"].(string); ok {
		task.Include = v
	}
	if v, ok := raw["include_tasks"].(string); ok {
		task.Include = v
	}

	// Parse tags on task/block
	task.Tags = parseStringOrList(raw["tags"])

	// Parse block/rescue/always
	if blockRaw, ok := raw["block"].([]any); ok {
		blockTasks, err := parseTaskList(blockRaw, "block task")
		if err != nil {
			return nil, fmt.Errorf("block: %w", err)
		}
		task.Block = blockTasks
	}
	if rescueRaw, ok := raw["rescue"].([]any); ok {
		rescueTasks, err := parseTaskList(rescueRaw, "rescue task")
		if err != nil {
			return nil, fmt.Errorf("rescue: %w", err)
		}
		task.Rescue = rescueTasks
	}
	if alwaysRaw, ok := raw["always"].([]any); ok {
		alwaysTasks, err := parseTaskList(alwaysRaw, "always task")
		if err != nil {
			return nil, fmt.Errorf("always: %w", err)
		}
		task.Always = alwaysTasks
	}

	// Parse assert built-in task keyword
	if assertRaw, ok := raw["assert"]; ok {
		spec, err := parseAssertSpec(assertRaw)
		if err != nil {
			return nil, fmt.Errorf("assert: %w", err)
		}
		task.Assert = spec
	}

	// Parse vars on include/include_tasks directives
	if vars, ok := raw["vars"].(map[string]any); ok {
		task.IncludeVars = vars
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
		switch v := loop.(type) {
		case []any:
			task.Loop = v
		case string:
			task.LoopExpr = v
		}
	} else if loop, ok := raw["with_items"]; ok {
		switch v := loop.(type) {
		case []any:
			task.Loop = v
		case string:
			task.LoopExpr = v
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

	// Merge top-level module argument keys into params.
	// Ansible allows creates/removes/chdir/args at the task level
	// as shorthand for module parameters.
	if v, ok := raw["args"]; ok {
		if argsMap, ok := v.(map[string]any); ok {
			for k, v := range argsMap {
				task.Params[k] = v
			}
		}
	}
	for _, key := range []string{"creates", "removes", "chdir"} {
		if v, ok := raw[key]; ok {
			task.Params[key] = v
		}
	}

	return task, nil
}

// parseAssertSpec parses the `assert:` task block into an AssertSpec.
// It accepts a mapping with `that`, `fail_msg`, `success_msg`, `quiet` keys.
func parseAssertSpec(raw any) (*AssertSpec, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected mapping with 'that' key")
	}
	spec := &AssertSpec{}

	that, ok := m["that"]
	if !ok {
		return nil, fmt.Errorf("'that' is required")
	}
	switch v := that.(type) {
	case string:
		spec.That = []string{v}
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("'that' list is empty")
		}
		spec.That = make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("'that' element %d is not a string (got %T)", i+1, item)
			}
			spec.That = append(spec.That, s)
		}
	default:
		return nil, fmt.Errorf("'that' must be a string or list of strings (got %T)", that)
	}

	if v, ok := m["fail_msg"].(string); ok {
		spec.FailMsg = v
	}
	if v, ok := m["success_msg"].(string); ok {
		spec.SuccessMsg = v
	}
	if v, ok := asBool(m["quiet"]); ok {
		spec.Quiet = v
	}

	return spec, nil
}

// parseSSHConfig parses a raw SSH config block from a play.
func parseSSHConfig(raw map[string]any) *SSHConfig {
	cfg := &SSHConfig{}
	if v, ok := raw["user"].(string); ok {
		cfg.User = v
	}
	if v, ok := raw["port"].(int); ok {
		cfg.Port = v
	}
	if v, ok := raw["key"].(string); ok {
		cfg.Key = v
	}
	if v, ok := raw["password"].(string); ok {
		cfg.Password = v
	}
	if v, ok := asBool(raw["host_key_checking"]); ok {
		cfg.HostKeyChecking = &v
	}
	return cfg
}

// parseSSMConfig parses a raw SSM config block from a play.
func parseSSMConfig(raw map[string]any) *SSMConfig {
	cfg := &SSMConfig{}
	if v, ok := raw["region"].(string); ok {
		cfg.Region = v
	}
	if v, ok := raw["bucket"].(string); ok {
		cfg.Bucket = v
	}
	if instances, ok := raw["instances"].([]any); ok {
		for _, item := range instances {
			if s, ok := item.(string); ok {
				cfg.Instances = append(cfg.Instances, s)
			}
		}
	}
	if tags, ok := raw["tags"].(map[string]any); ok {
		cfg.Tags = make(map[string]string, len(tags))
		for k, v := range tags {
			if s, ok := v.(string); ok {
				cfg.Tags[k] = s
			}
		}
	}
	if v, ok := asBool(raw["attach_s3_policy"]); ok {
		cfg.AttachS3Policy = v
	}
	return cfg
}

// ExpandShorthand expands shorthand module syntax.
// For example, "apt: name=nginx state=present" becomes proper params.
func ExpandShorthand(task *Task) {
	raw, ok := task.Params["_raw"].(string)
	if !ok {
		return
	}

	// Collect any extra keys (creates, removes, chdir) already in params
	// so they survive the replacement below.
	extra := make(map[string]any)
	for k, v := range task.Params {
		if k != "_raw" {
			extra[k] = v
		}
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
		for k, v := range extra {
			task.Params[k] = v
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
	for k, v := range extra {
		newParams[k] = v
	}

	task.Params = newParams
}

// ResolveModule checks if the task's module exists in the registry.
// Built-in task keywords (assert, block, include_tasks) have no module and
// are always valid at this layer.
func ResolveModule(task *Task) error {
	if task.IsAssert() || task.IsBlock() || task.Include != "" {
		return nil
	}
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
