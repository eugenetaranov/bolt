// Package user provides a module for managing system users on Linux.
package user

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module manages system users.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "user"
}

// Example returns a usage example for the user module.
func (m *Module) Example() string {
	return `- name: Ensure a user exists
  user:
    name: deploy
    state: present
    shell: /bin/bash
    groups: [sudo]`
}

// userInfo holds the current state of a user.
type userInfo struct {
	Exists bool
	UID    int
	GID    int
	Home   string
	Shell  string
	Groups []string // supplementary groups
}

// getUserInfo queries the target for current user state.
// Parses `getent passwd <name>` output format: name:password:uid:gid:gecos:home:shell
func getUserInfo(ctx context.Context, conn connector.Connector, name string) (*userInfo, error) {
	result, err := conn.Execute(ctx, fmt.Sprintf("getent passwd %s", connector.ShellQuote(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	if result.ExitCode != 0 {
		return &userInfo{Exists: false}, nil
	}

	line := strings.TrimSpace(result.Stdout)
	if line == "" {
		return &userInfo{Exists: false}, nil
	}

	fields := strings.Split(line, ":")
	if len(fields) < 7 {
		return nil, fmt.Errorf("unexpected getent output: %s", line)
	}

	uid, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("invalid uid %q: %w", fields[2], err)
	}

	gid, err := strconv.Atoi(fields[3])
	if err != nil {
		return nil, fmt.Errorf("invalid gid %q: %w", fields[3], err)
	}

	info := &userInfo{
		Exists: true,
		UID:    uid,
		GID:    gid,
		Home:   fields[5],
		Shell:  fields[6],
	}

	// Get supplementary groups
	groups, err := getUserGroups(ctx, conn, name)
	if err != nil {
		return nil, err
	}
	info.Groups = groups

	return info, nil
}

// resolveGID resolves a primary-group reference (a name or a numeric gid) to
// its numeric gid on the target. Returns (gid, true) on success.
func resolveGID(ctx context.Context, conn connector.Connector, group string) (int, bool) {
	if gid, err := strconv.Atoi(group); err == nil {
		return gid, true
	}
	res, err := conn.Execute(ctx, fmt.Sprintf("getent group %s", connector.ShellQuote(group)))
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	fields := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(fields) >= 3 {
		if gid, err := strconv.Atoi(fields[2]); err == nil {
			return gid, true
		}
	}
	return 0, false
}

// getUserGroups parses `id -Gn <name>` output to get supplementary group membership.
// Returns only supplementary groups (excludes primary group).
func getUserGroups(ctx context.Context, conn connector.Connector, name string) ([]string, error) {
	// Get primary group name
	primaryResult, err := conn.Execute(ctx, fmt.Sprintf("id -gn %s", connector.ShellQuote(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to get primary group: %w", err)
	}
	primaryGroup := strings.TrimSpace(primaryResult.Stdout)

	// Get all groups
	result, err := conn.Execute(ctx, fmt.Sprintf("id -Gn %s", connector.ShellQuote(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to get user groups: %w", err)
	}

	if result.ExitCode != 0 {
		return nil, nil
	}

	var groups []string
	for _, g := range strings.Fields(strings.TrimSpace(result.Stdout)) {
		if g != primaryGroup {
			groups = append(groups, g)
		}
	}
	sort.Strings(groups)
	return groups, nil
}

// Run executes the user module.
//
// Parameters:
//   - name (string, required): Username
//   - state (string): Desired state - present, absent (default: "present")
//   - uid (int): User ID
//   - shell (string): Login shell
//   - home (string): Home directory path
//   - groups ([]string): Supplementary groups (appended)
//   - system (bool): Create a system user (default: false)
//   - password (string): Pre-hashed password
//   - remove (bool): Remove home directory on state=absent (default: false)
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

	info, err := getUserInfo(ctx, conn, name)
	if err != nil {
		return nil, err
	}

	if state == "absent" {
		if !info.Exists {
			return module.Unchanged("user does not exist"), nil
		}
		cmd := "userdel"
		if module.GetBool(params, "remove", false) {
			cmd += " -r"
		}
		cmd += " " + connector.ShellQuote(name)
		if _, err := connector.Run(ctx, conn, cmd); err != nil {
			return nil, fmt.Errorf("userdel failed: %w", err)
		}
		return module.Changed(fmt.Sprintf("user '%s' removed", name)), nil
	}

	// state == "present"
	shell := module.GetString(params, "shell", "")
	home := module.GetString(params, "home", "")
	uidParam := module.GetInt(params, "uid", -1)
	system := module.GetBool(params, "system", false)
	password := module.GetString(params, "password", "")
	groups := module.GetStringSlice(params, "groups")
	group := module.GetString(params, "group", "") // primary group (name or gid)

	if !info.Exists {
		cmd := "useradd"
		if shell != "" {
			cmd += fmt.Sprintf(" -s %s", connector.ShellQuote(shell))
		}
		if home != "" {
			cmd += fmt.Sprintf(" -d %s", connector.ShellQuote(home))
		}
		if uidParam >= 0 {
			cmd += fmt.Sprintf(" -u %d", uidParam)
		}
		if group != "" {
			cmd += fmt.Sprintf(" -g %s", connector.ShellQuote(group))
		}
		if len(groups) > 0 {
			cmd += fmt.Sprintf(" -G %s", strings.Join(groups, ","))
		}
		if system {
			cmd += " -r"
		}
		if password != "" {
			cmd += fmt.Sprintf(" -p %s", connector.ShellQuote(password))
		}
		cmd += " " + connector.ShellQuote(name)

		if _, err := connector.Run(ctx, conn, cmd); err != nil {
			return nil, fmt.Errorf("useradd failed: %w", err)
		}
		return module.Changed(fmt.Sprintf("user '%s' created", name)), nil
	}

	// User exists — check if modification needed
	var modifications []string
	var modArgs []string

	if shell != "" && shell != info.Shell {
		modArgs = append(modArgs, "-s", connector.ShellQuote(shell))
		modifications = append(modifications, fmt.Sprintf("shell changed to %s", shell))
	}

	if home != "" && home != info.Home {
		modArgs = append(modArgs, "-d", connector.ShellQuote(home))
		modifications = append(modifications, fmt.Sprintf("home changed to %s", home))
	}

	if uidParam >= 0 && uidParam != info.UID {
		modArgs = append(modArgs, "-u", strconv.Itoa(uidParam))
		modifications = append(modifications, fmt.Sprintf("uid changed to %d", uidParam))
	}

	if group != "" {
		if gid, ok := resolveGID(ctx, conn, group); !ok || gid != info.GID {
			modArgs = append(modArgs, "-g", connector.ShellQuote(group))
			modifications = append(modifications, "primary group changed to "+group)
		}
	}

	if password != "" {
		// Always set password when specified — we can't compare hashes from /etc/shadow without root
		modArgs = append(modArgs, "-p", connector.ShellQuote(password))
		modifications = append(modifications, "password updated")
	}

	if len(groups) > 0 {
		desired := make([]string, len(groups))
		copy(desired, groups)
		sort.Strings(desired)

		if !stringSliceEqual(desired, info.Groups) {
			modArgs = append(modArgs, "-aG", strings.Join(groups, ","))
			modifications = append(modifications, fmt.Sprintf("groups updated to include %s", strings.Join(groups, ",")))
		}
	}

	if len(modArgs) == 0 {
		return module.Unchanged(fmt.Sprintf("user '%s' already in desired state", name)), nil
	}

	cmd := "usermod " + strings.Join(modArgs, " ") + " " + connector.ShellQuote(name)
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return nil, fmt.Errorf("usermod failed: %w", err)
	}

	return module.Changed(fmt.Sprintf("user '%s': %s", name, strings.Join(modifications, ", "))), nil
}

// Check determines whether the user module would make changes without applying them.
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

	info, err := getUserInfo(ctx, conn, name)
	if err != nil {
		return nil, err
	}

	if state == "absent" {
		if info.Exists {
			return module.WouldChange(fmt.Sprintf("would remove user '%s'", name)), nil
		}
		return module.NoChange("user does not exist"), nil
	}

	// state == "present"
	if !info.Exists {
		return module.WouldChange(fmt.Sprintf("would create user '%s'", name)), nil
	}

	shell := module.GetString(params, "shell", "")
	home := module.GetString(params, "home", "")
	uidParam := module.GetInt(params, "uid", -1)
	password := module.GetString(params, "password", "")
	groups := module.GetStringSlice(params, "groups")
	group := module.GetString(params, "group", "")

	var changes []string

	if shell != "" && shell != info.Shell {
		changes = append(changes, "shell")
	}
	if home != "" && home != info.Home {
		changes = append(changes, "home")
	}
	if uidParam >= 0 && uidParam != info.UID {
		changes = append(changes, "uid")
	}
	if group != "" {
		if gid, ok := resolveGID(ctx, conn, group); !ok || gid != info.GID {
			changes = append(changes, "primary group")
		}
	}
	if password != "" {
		changes = append(changes, "password")
	}
	if len(groups) > 0 {
		desired := make([]string, len(groups))
		copy(desired, groups)
		sort.Strings(desired)
		if !stringSliceEqual(desired, info.Groups) {
			changes = append(changes, "groups")
		}
	}

	if len(changes) > 0 {
		return module.WouldChange(fmt.Sprintf("would modify user '%s': %s", name, strings.Join(changes, ", "))), nil
	}

	return module.NoChange(fmt.Sprintf("user '%s' already in desired state", name)), nil
}

// Description returns a short summary of the user module.
func (m *Module) Description() string {
	return "Manage system users on Linux using useradd/usermod/userdel."
}

// Parameters returns the parameter documentation for the user module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "name", Type: "string", Required: true, Description: "Username"},
		{Name: "state", Type: "string", Default: "present", Description: "Desired state: present, absent"},
		{Name: "uid", Type: "int", Description: "User ID"},
		{Name: "shell", Type: "string", Description: "Login shell (e.g., /bin/bash)"},
		{Name: "home", Type: "string", Description: "Home directory path"},
		{Name: "group", Type: "string", Description: "Primary group (name or gid)"},
		{Name: "groups", Type: "[]string", Description: "Supplementary groups (appended to existing)"},
		{Name: "system", Type: "bool", Default: "false", Description: "Create a system user"},
		{Name: "password", Type: "string", Description: "Pre-hashed password (e.g., SHA-512 crypt format)"},
		{Name: "remove", Type: "bool", Default: "false", Description: "Remove home directory when state=absent"},
	}
}

// stringSliceEqual compares two sorted string slices.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Interface compliance
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
)
