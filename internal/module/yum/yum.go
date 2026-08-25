// Package yum provides a module for managing packages on RPM-based systems (RHEL, CentOS, Fedora, Amazon Linux, Rocky Linux).
package yum

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

// State represents the desired package state.
type State string

const (
	StatePresent State = "present" // Ensure package is installed
	StateAbsent  State = "absent"  // Ensure package is not installed
	StateLatest  State = "latest"  // Ensure package is installed and up-to-date
)

// Module manages packages on RPM-based systems via yum or dnf.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "yum"
}

// Run executes the yum module.
//
// Parameters:
//   - name (string|[]string): Package name(s) to manage
//   - state (string): Desired state - present, absent, latest (default: present)
//   - update_cache (bool): Run yum makecache before operations (default: false)
//   - upgrade (string): Upgrade mode - none, yes (default: none)
//   - autoremove (bool): Remove unused dependency packages (default: false)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	pkgMgr, err := detectPackageManager(ctx, conn)
	if err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "present")
	state := State(stateStr)
	updateCache := module.GetBool(params, "update_cache", false)
	upgrade := module.GetString(params, "upgrade", "none")
	autoremove := module.GetBool(params, "autoremove", false)

	// Validate state
	switch state {
	case StatePresent, StateAbsent, StateLatest:
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be present, absent, or latest", state)
	}

	// Validate upgrade mode
	switch upgrade {
	case "none", "yes":
		// Valid
	default:
		return nil, fmt.Errorf("invalid upgrade mode '%s': must be none or yes", upgrade)
	}

	var changed bool
	var messages []string

	// Update cache if requested
	names := module.GetStringSlice(params, "name")
	if updateCache {
		if err := runMakecache(ctx, conn, pkgMgr); err != nil {
			return nil, fmt.Errorf("failed to update cache: %w", err)
		}
		if len(names) == 0 {
			messages = append(messages, "cache updated")
			changed = true
		}
	}

	// Run upgrade if requested
	if upgrade == "yes" {
		upgraded, err := runUpgradeAll(ctx, conn, pkgMgr)
		if err != nil {
			return nil, fmt.Errorf("failed to upgrade: %w", err)
		}
		if upgraded {
			messages = append(messages, "upgrade completed")
			changed = true
		}
	}

	if len(names) == 0 {
		if !updateCache && upgrade == "none" {
			return nil, fmt.Errorf("'name' parameter is required when not using update_cache or upgrade")
		}
		// Handle autoremove
		if autoremove {
			removed, err := runAutoremove(ctx, conn, pkgMgr)
			if err != nil {
				return nil, err
			}
			if removed {
				messages = append(messages, "autoremove completed")
				changed = true
			}
		}
		if changed {
			return module.Changed(strings.Join(messages, ", ")), nil
		}
		return module.Unchanged("no changes needed"), nil
	}

	// Get package states
	installed, err := getInstalledState(ctx, conn, names)
	if err != nil {
		return nil, fmt.Errorf("failed to get package states: %w", err)
	}

	// Determine actions needed
	var toInstall, toRemove, toUpgrade []string

	switch state {
	case StatePresent:
		for _, name := range names {
			if !installed[name] {
				toInstall = append(toInstall, name)
			}
		}
	case StateAbsent:
		for _, name := range names {
			if installed[name] {
				toRemove = append(toRemove, name)
			}
		}
	case StateLatest:
		updatable, err := getUpdatable(ctx, conn, pkgMgr, names)
		if err != nil {
			return nil, fmt.Errorf("failed to check for updates: %w", err)
		}
		for _, name := range names {
			if !installed[name] {
				toInstall = append(toInstall, name)
			} else if updatable[name] {
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	// Install packages
	if len(toInstall) > 0 {
		if err := installPackages(ctx, conn, pkgMgr, toInstall); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("installed: %s", strings.Join(toInstall, ", ")))
		changed = true
	}

	// Remove packages
	if len(toRemove) > 0 {
		if err := removePackages(ctx, conn, pkgMgr, toRemove); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("removed: %s", strings.Join(toRemove, ", ")))
		changed = true
	}

	// Upgrade packages
	if len(toUpgrade) > 0 {
		if err := upgradePackages(ctx, conn, pkgMgr, toUpgrade); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("upgraded: %s", strings.Join(toUpgrade, ", ")))
		changed = true
	}

	// Handle autoremove
	if autoremove {
		removed, err := runAutoremove(ctx, conn, pkgMgr)
		if err != nil {
			return nil, err
		}
		if removed {
			messages = append(messages, "autoremove completed")
			changed = true
		}
	}

	if !changed {
		return module.Unchanged("packages already in desired state"), nil
	}

	return module.Changed(strings.Join(messages, "; ")), nil
}

// Check determines whether the yum module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	pkgMgr, err := detectPackageManager(ctx, conn)
	if err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "present")
	state := State(stateStr)
	updateCache := module.GetBool(params, "update_cache", false)
	upgrade := module.GetString(params, "upgrade", "none")

	// upgrade can't be cheaply predicted
	if upgrade != "none" {
		return module.UncertainChange("upgrade always runs"), nil
	}

	names := module.GetStringSlice(params, "name")
	if len(names) == 0 {
		if updateCache {
			return module.UncertainChange("update_cache always runs"), nil
		}
		return module.NoChange("no packages specified"), nil
	}

	installed, err := getInstalledState(ctx, conn, names)
	if err != nil {
		return nil, fmt.Errorf("failed to get package states: %w", err)
	}

	var toInstall, toRemove, toUpgrade []string

	switch state {
	case StatePresent:
		for _, name := range names {
			if !installed[name] {
				toInstall = append(toInstall, name)
			}
		}
	case StateAbsent:
		for _, name := range names {
			if installed[name] {
				toRemove = append(toRemove, name)
			}
		}
	case StateLatest:
		updatable, err := getUpdatable(ctx, conn, pkgMgr, names)
		if err != nil {
			return nil, fmt.Errorf("failed to check for updates: %w", err)
		}
		for _, name := range names {
			if !installed[name] {
				toInstall = append(toInstall, name)
			} else if updatable[name] {
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	var parts []string
	if len(toInstall) > 0 {
		parts = append(parts, fmt.Sprintf("install: %s", strings.Join(toInstall, ", ")))
	}
	if len(toRemove) > 0 {
		parts = append(parts, fmt.Sprintf("remove: %s", strings.Join(toRemove, ", ")))
	}
	if len(toUpgrade) > 0 {
		parts = append(parts, fmt.Sprintf("upgrade: %s", strings.Join(toUpgrade, ", ")))
	}

	if len(parts) > 0 {
		return module.WouldChange("would " + strings.Join(parts, "; ")), nil
	}

	return module.NoChange("packages already in desired state"), nil
}

// detectPackageManager checks whether dnf or yum is available, preferring dnf.
func detectPackageManager(ctx context.Context, conn connector.Connector) (string, error) {
	for _, mgr := range []string{"dnf", "yum"} {
		ok, err := module.CommandAvailable(ctx, conn, mgr)
		if err != nil {
			return "", err
		}
		if ok {
			return mgr, nil
		}
	}
	return "", fmt.Errorf("neither dnf nor yum is available (not an RPM-based system?)")
}

// getInstalledState checks which packages are installed using rpm -q.
func getInstalledState(ctx context.Context, conn connector.Connector, names []string) (map[string]bool, error) {
	installed := make(map[string]bool)
	for _, name := range names {
		installed[name] = false
	}

	for _, name := range names {
		cmd := fmt.Sprintf("rpm -q %s", connector.ShellQuote(name))
		result, err := conn.Execute(ctx, cmd)
		if err != nil {
			return nil, err
		}
		if result.ExitCode == 0 {
			installed[name] = true
		}
	}

	return installed, nil
}

// getUpdatable checks which installed packages have updates available.
func getUpdatable(ctx context.Context, conn connector.Connector, pkgMgr string, names []string) (map[string]bool, error) {
	updatable := make(map[string]bool)

	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = connector.ShellQuote(name)
	}

	cmd := fmt.Sprintf("%s check-update %s 2>/dev/null || true", pkgMgr, strings.Join(quoted, " "))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}

	// yum/dnf check-update exits 100 when updates are available, 0 when none
	// Output format: package.arch  version  repo
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip header lines and metadata lines
		if strings.HasPrefix(line, "Last metadata") || strings.HasPrefix(line, "Obsoleting") || strings.Contains(line, "=") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			// Package name may include .arch suffix (e.g., nginx.x86_64)
			pkgName := fields[0]
			if idx := strings.LastIndex(pkgName, "."); idx > 0 {
				pkgName = pkgName[:idx]
			}
			for _, name := range names {
				if pkgName == name {
					updatable[name] = true
				}
			}
		}
	}

	return updatable, nil
}

// runMakecache runs yum/dnf makecache.
func runMakecache(ctx context.Context, conn connector.Connector, pkgMgr string) error {
	cmd := fmt.Sprintf("%s makecache -q", pkgMgr)
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("%s makecache failed: %w", pkgMgr, err)
	}
	return nil
}

// installPackages installs the specified packages.
func installPackages(ctx context.Context, conn connector.Connector, pkgMgr string, names []string) error {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = connector.ShellQuote(name)
	}
	cmd := fmt.Sprintf("%s install -y %s", pkgMgr, strings.Join(quoted, " "))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("%s install failed: %w", pkgMgr, err)
	}
	return nil
}

// removePackages removes the specified packages.
func removePackages(ctx context.Context, conn connector.Connector, pkgMgr string, names []string) error {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = connector.ShellQuote(name)
	}
	cmd := fmt.Sprintf("%s remove -y %s", pkgMgr, strings.Join(quoted, " "))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("%s remove failed: %w", pkgMgr, err)
	}
	return nil
}

// upgradePackages upgrades the specified packages to latest.
func upgradePackages(ctx context.Context, conn connector.Connector, pkgMgr string, names []string) error {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = connector.ShellQuote(name)
	}
	// dnf uses "upgrade", yum uses "update" — both accept "update" for compatibility
	action := "upgrade"
	if pkgMgr == "yum" {
		action = "update"
	}
	cmd := fmt.Sprintf("%s %s -y %s", pkgMgr, action, strings.Join(quoted, " "))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("%s %s failed: %w", pkgMgr, action, err)
	}
	return nil
}

// runUpgradeAll upgrades all installed packages.
func runUpgradeAll(ctx context.Context, conn connector.Connector, pkgMgr string) (bool, error) {
	action := "upgrade"
	if pkgMgr == "yum" {
		action = "update"
	}
	cmd := fmt.Sprintf("%s %s -y", pkgMgr, action)
	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return false, fmt.Errorf("%s %s failed: %w", pkgMgr, action, err)
	}

	// Check if anything was actually upgraded
	output := result.Stdout + result.Stderr
	return strings.Contains(output, "Upgraded") || strings.Contains(output, "Updated") || strings.Contains(output, "Installed"), nil
}

// runAutoremove removes unused dependency packages.
func runAutoremove(ctx context.Context, conn connector.Connector, pkgMgr string) (bool, error) {
	cmd := fmt.Sprintf("%s autoremove -y", pkgMgr)
	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return false, fmt.Errorf("%s autoremove failed: %w", pkgMgr, err)
	}

	output := result.Stdout + result.Stderr
	return strings.Contains(output, "Removed") || strings.Contains(output, "Erased"), nil
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)

// Ensure Module implements the module.Describer interface.
var _ module.Describer = (*Module)(nil)

// Description returns a short summary of the yum module.
func (m *Module) Description() string {
	return "Manage packages on RPM-based systems (RHEL, CentOS, Fedora, Rocky Linux) using yum/dnf."
}

// Parameters returns the parameter documentation for the yum module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "name", Type: "string|[]string", Required: true, Description: "Package name(s) to manage"},
		{Name: "state", Type: "string", Default: "present", Description: "Desired state: present, absent, latest"},
		{Name: "update_cache", Type: "bool", Default: "false", Description: "Run yum makecache before operations"},
		{Name: "upgrade", Type: "string", Default: "none", Description: "Upgrade mode: none, yes"},
		{Name: "autoremove", Type: "bool", Default: "false", Description: "Remove unused dependency packages"},
	}
}
