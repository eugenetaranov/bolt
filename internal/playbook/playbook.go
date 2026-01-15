// Package playbook defines the structure and parsing of Bolt playbooks.
package playbook

import (
	"fmt"
	"strings"
)

// Playbook represents a complete playbook with one or more plays.
type Playbook struct {
	// Path is the file path the playbook was loaded from.
	Path string

	// Plays is the list of plays in the playbook.
	Plays []*Play
}

// Play represents a single play targeting a set of hosts.
type Play struct {
	// Name is an optional description of the play.
	Name string `yaml:"name"`

	// Hosts specifies which hosts to target (host, group, or pattern).
	Hosts string `yaml:"hosts"`

	// Connection specifies how to connect (local, ssh, ssm).
	Connection string `yaml:"connection"`

	// Vars defines variables available to all tasks in the play.
	Vars map[string]any `yaml:"vars"`

	// Roles is the list of roles to include in the play.
	Roles []string `yaml:"roles"`

	// Tasks is the list of tasks to execute.
	Tasks []*Task `yaml:"tasks"`

	// Handlers are tasks triggered by notify.
	Handlers []*Task `yaml:"handlers"`

	// Become enables privilege escalation.
	Become bool `yaml:"become"`

	// BecomeUser is the user to become (default: root).
	BecomeUser string `yaml:"become_user"`

	// GatherFacts controls whether to gather system facts (default: true).
	GatherFacts *bool `yaml:"gather_facts"`
}

// Task represents a single task in a play.
type Task struct {
	// Name is a description of the task.
	Name string `yaml:"name"`

	// Module is the name of the module to execute.
	Module string `yaml:"-"`

	// Params are the parameters to pass to the module.
	Params map[string]any `yaml:"-"`

	// RolePath is the path to the role this task belongs to (empty for play tasks).
	RolePath string `yaml:"-"`

	// When is a conditional expression; task runs only if true.
	When string `yaml:"when"`

	// Register stores the task result in a variable with this name.
	Register string `yaml:"register"`

	// Notify lists handlers to trigger if the task changes something.
	Notify []string `yaml:"-"`

	// Loop iterates the task over a list of items.
	Loop []any `yaml:"-"`

	// LoopVar is the variable name for the current item (default: "item").
	LoopVar string `yaml:"loop_var"`

	// IgnoreErrors continues execution even if the task fails.
	IgnoreErrors bool `yaml:"ignore_errors"`

	// Retries is the number of times to retry on failure.
	Retries int `yaml:"retries"`

	// Delay is seconds to wait between retries.
	Delay int `yaml:"delay"`

	// Become enables privilege escalation for this task.
	Become *bool `yaml:"become"`

	// BecomeUser is the user to become for this task.
	BecomeUser string `yaml:"become_user"`

	// Changed controls when the task reports as changed.
	// Can be a boolean or a conditional expression.
	ChangedWhen string `yaml:"changed_when"`

	// Failed controls when the task reports as failed.
	FailedWhen string `yaml:"failed_when"`
}

// Role represents an Ansible-compatible role with tasks, handlers, and variables.
type Role struct {
	// Name is the role name (directory name).
	Name string

	// Path is the absolute path to the role directory.
	Path string

	// Tasks loaded from tasks/main.yaml.
	Tasks []*Task

	// Handlers loaded from handlers/main.yaml.
	Handlers []*Task

	// Vars loaded from vars/main.yaml (higher priority).
	Vars map[string]any

	// Defaults loaded from defaults/main.yaml (lower priority).
	Defaults map[string]any
}

// ShouldGatherFacts returns whether facts should be gathered for this play.
func (p *Play) ShouldGatherFacts() bool {
	if p.GatherFacts == nil {
		return true // default
	}
	return *p.GatherFacts
}

// GetConnection returns the connection type, defaulting to "local".
func (p *Play) GetConnection() string {
	if p.Connection == "" {
		return "local"
	}
	return p.Connection
}

// GetBecomeUser returns the become user, defaulting to "root".
func (p *Play) GetBecomeUser() string {
	if p.BecomeUser == "" {
		return "root"
	}
	return p.BecomeUser
}

// ShouldBecome returns whether privilege escalation is enabled for this task.
func (t *Task) ShouldBecome(playBecome bool) bool {
	if t.Become != nil {
		return *t.Become
	}
	return playBecome
}

// GetBecomeUser returns the become user for this task.
func (t *Task) GetBecomeUser(playBecomeUser string) string {
	if t.BecomeUser != "" {
		return t.BecomeUser
	}
	return playBecomeUser
}

// GetLoopVar returns the loop variable name, defaulting to "item".
func (t *Task) GetLoopVar() string {
	if t.LoopVar == "" {
		return "item"
	}
	return t.LoopVar
}

// Validate checks the play for common errors.
func (p *Play) Validate() error {
	if p.Hosts == "" {
		return fmt.Errorf("play is missing required 'hosts' field")
	}

	conn := p.GetConnection()
	switch conn {
	case "local", "docker", "ssh", "ssm":
		// Valid
	default:
		return fmt.Errorf("invalid connection type: %s (must be local, docker, ssh, or ssm)", conn)
	}

	for i, task := range p.Tasks {
		if err := task.Validate(); err != nil {
			taskName := task.Name
			if taskName == "" {
				taskName = fmt.Sprintf("task %d", i+1)
			}
			return fmt.Errorf("%s: %w", taskName, err)
		}
	}

	for i, handler := range p.Handlers {
		if err := handler.Validate(); err != nil {
			handlerName := handler.Name
			if handlerName == "" {
				handlerName = fmt.Sprintf("handler %d", i+1)
			}
			return fmt.Errorf("%s: %w", handlerName, err)
		}
		if handler.Name == "" {
			return fmt.Errorf("handler %d: handlers must have a name for notify to reference", i+1)
		}
	}

	return nil
}

// Validate checks the task for common errors.
func (t *Task) Validate() error {
	if t.Module == "" {
		return fmt.Errorf("task has no module specified")
	}

	if t.Retries < 0 {
		return fmt.Errorf("retries cannot be negative")
	}

	if t.Delay < 0 {
		return fmt.Errorf("delay cannot be negative")
	}

	return nil
}

// String returns a human-readable description of the task.
func (t *Task) String() string {
	if t.Name != "" {
		return t.Name
	}
	return fmt.Sprintf("%s: %v", t.Module, summarizeParams(t.Params))
}

// summarizeParams creates a brief summary of task parameters.
func summarizeParams(params map[string]any) string {
	if len(params) == 0 {
		return "{}"
	}

	var parts []string
	for k, v := range params {
		switch val := v.(type) {
		case string:
			if len(val) > 30 {
				val = val[:27] + "..."
			}
			parts = append(parts, fmt.Sprintf("%s=%q", k, val))
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		if len(parts) >= 3 {
			parts = append(parts, "...")
			break
		}
	}

	return "{" + strings.Join(parts, ", ") + "}"
}
