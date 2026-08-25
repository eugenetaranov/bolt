// Package group provides a module for managing system groups on Linux.
package group

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module manages system groups.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "group"
}

// Example returns a usage example for the group module.
func (m *Module) Example() string {
	return `- name: Ensure a group exists
  group:
    name: deploy
    state: present`
}

// groupInfo holds the current state of a group.
type groupInfo struct {
	Exists bool
	GID    int
}

// getGroupInfo queries the target for current group state.
// Parses `getent group <name>` output format: name:password:gid:members
func getGroupInfo(ctx context.Context, conn connector.Connector, name string) (*groupInfo, error) {
	result, err := conn.Execute(ctx, fmt.Sprintf("getent group %s", connector.ShellQuote(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to query group: %w", err)
	}

	if result.ExitCode != 0 {
		return &groupInfo{Exists: false}, nil
	}

	line := strings.TrimSpace(result.Stdout)
	if line == "" {
		return &groupInfo{Exists: false}, nil
	}

	fields := strings.Split(line, ":")
	if len(fields) < 3 {
		return nil, fmt.Errorf("unexpected getent output: %s", line)
	}

	gid, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("invalid gid %q: %w", fields[2], err)
	}

	return &groupInfo{Exists: true, GID: gid}, nil
}

// Run executes the group module.
//
// Parameters:
//   - name (string, required): Group name
//   - state (string): Desired state - present, absent (default: "present")
//   - gid (int): Group ID
//   - system (bool): Create a system group (default: false)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	name, err := module.RequireString(params, "name")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	switch state {
	case "present", "absent":
	default:
		return nil, fmt.Errorf("invalid state '%s': must be present or absent", state)
	}

	info, err := getGroupInfo(ctx, conn, name)
	if err != nil {
		return nil, err
	}

	if state == "absent" {
		if !info.Exists {
			return module.Unchanged("group does not exist"), nil
		}
		if _, err := connector.Run(ctx, conn, fmt.Sprintf("groupdel %s", connector.ShellQuote(name))); err != nil {
			return nil, fmt.Errorf("groupdel failed: %w", err)
		}
		return module.Changed(fmt.Sprintf("group '%s' removed", name)), nil
	}

	// state == "present"
	system := module.GetBool(params, "system", false)
	gidParam := module.GetInt(params, "gid", -1)

	if !info.Exists {
		cmd := "groupadd"
		if gidParam >= 0 {
			cmd += fmt.Sprintf(" -g %d", gidParam)
		}
		if system {
			cmd += " -r"
		}
		cmd += " " + connector.ShellQuote(name)

		if _, err := connector.Run(ctx, conn, cmd); err != nil {
			return nil, fmt.Errorf("groupadd failed: %w", err)
		}
		return module.Changed(fmt.Sprintf("group '%s' created", name)), nil
	}

	// Group exists — check if modification needed
	if gidParam >= 0 && gidParam != info.GID {
		cmd := fmt.Sprintf("groupmod -g %d %s", gidParam, connector.ShellQuote(name))
		if _, err := connector.Run(ctx, conn, cmd); err != nil {
			return nil, fmt.Errorf("groupmod failed: %w", err)
		}
		return module.Changed(fmt.Sprintf("group '%s' gid changed to %d", name, gidParam)), nil
	}

	return module.Unchanged(fmt.Sprintf("group '%s' already in desired state", name)), nil
}

// Check determines whether the group module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	name, err := module.RequireString(params, "name")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	switch state {
	case "present", "absent":
	default:
		return nil, fmt.Errorf("invalid state '%s': must be present or absent", state)
	}

	info, err := getGroupInfo(ctx, conn, name)
	if err != nil {
		return nil, err
	}

	if state == "absent" {
		if info.Exists {
			return module.WouldChange(fmt.Sprintf("would remove group '%s'", name)), nil
		}
		return module.NoChange("group does not exist"), nil
	}

	// state == "present"
	gidParam := module.GetInt(params, "gid", -1)

	if !info.Exists {
		return module.WouldChange(fmt.Sprintf("would create group '%s'", name)), nil
	}

	if gidParam >= 0 && gidParam != info.GID {
		return module.WouldChange(fmt.Sprintf("would change group '%s' gid from %d to %d", name, info.GID, gidParam)), nil
	}

	return module.NoChange(fmt.Sprintf("group '%s' already in desired state", name)), nil
}

// Description returns a short summary of the group module.
func (m *Module) Description() string {
	return "Manage system groups on Linux using groupadd/groupmod/groupdel."
}

// Parameters returns the parameter documentation for the group module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "name", Type: "string", Required: true, Description: "Group name"},
		{Name: "state", Type: "string", Default: "present", Description: "Desired state: present, absent"},
		{Name: "gid", Type: "int", Description: "Group ID"},
		{Name: "system", Type: "bool", Default: "false", Description: "Create a system group"},
	}
}

// Interface compliance
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
)
