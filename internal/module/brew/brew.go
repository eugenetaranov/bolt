// Package brew provides a module for managing Homebrew packages on macOS.
package brew

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

// Module manages Homebrew packages on macOS.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "brew"
}

// Run executes the brew module.
//
// Parameters:
//   - name (string|[]string): Package name(s) to manage
//   - state (string): Desired state - present, absent, latest (default: present)
//   - cask (bool): Install as cask (GUI application) instead of formula (default: false)
//   - upgrade_all (bool): Upgrade all installed packages (default: false)
//   - update_homebrew (bool): Run brew update before operations (default: false)
//   - options ([]string): Additional options to pass to brew install
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Check if Homebrew is available
	if err := checkHomebrew(ctx, conn); err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "present")
	state := State(stateStr)
	cask := module.GetBool(params, "cask", false)
	upgradeAll := module.GetBool(params, "upgrade_all", false)
	updateHomebrew := module.GetBool(params, "update_homebrew", false)
	options := module.GetStringSlice(params, "options")

	// Validate state
	switch state {
	case StatePresent, StateAbsent, StateLatest:
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be present, absent, or latest", state)
	}

	var changed bool
	var messages []string

	// Update Homebrew if requested
	if updateHomebrew {
		updated, err := runBrewUpdate(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("failed to update homebrew: %w", err)
		}
		if updated {
			messages = append(messages, "homebrew updated")
			changed = true
		}
	}

	// Upgrade all packages if requested
	if upgradeAll {
		upgraded, err := runBrewUpgradeAll(ctx, conn, cask)
		if err != nil {
			return nil, fmt.Errorf("failed to upgrade packages: %w", err)
		}
		if upgraded {
			messages = append(messages, "packages upgraded")
			changed = true
		}
	}

	// Get package names
	names := module.GetStringSlice(params, "name")
	if len(names) == 0 {
		if !upgradeAll && !updateHomebrew {
			return nil, fmt.Errorf("'name' parameter is required when not using upgrade_all or update_homebrew")
		}
		if changed {
			return module.Changed(strings.Join(messages, ", ")), nil
		}
		return module.Unchanged("no changes needed"), nil
	}

	// Get currently installed packages
	installed, err := getInstalledPackages(ctx, conn, cask)
	if err != nil {
		return nil, fmt.Errorf("failed to get installed packages: %w", err)
	}

	// Process each package
	var toInstall, toRemove, toUpgrade []string

	for _, name := range names {
		isInstalled := installed[name]

		switch state {
		case StatePresent:
			if !isInstalled {
				toInstall = append(toInstall, name)
			}
		case StateAbsent:
			if isInstalled {
				toRemove = append(toRemove, name)
			}
		case StateLatest:
			if !isInstalled {
				toInstall = append(toInstall, name)
			} else {
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	// Install packages
	if len(toInstall) > 0 {
		if err := installPackages(ctx, conn, toInstall, cask, options); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("installed: %s", strings.Join(toInstall, ", ")))
		changed = true
	}

	// Remove packages
	if len(toRemove) > 0 {
		if err := removePackages(ctx, conn, toRemove, cask); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("removed: %s", strings.Join(toRemove, ", ")))
		changed = true
	}

	// Upgrade packages
	if len(toUpgrade) > 0 {
		upgraded, err := upgradePackages(ctx, conn, toUpgrade, cask)
		if err != nil {
			return nil, err
		}
		if len(upgraded) > 0 {
			messages = append(messages, fmt.Sprintf("upgraded: %s", strings.Join(upgraded, ", ")))
			changed = true
		}
	}

	if !changed {
		return module.Unchanged("packages already in desired state"), nil
	}

	return module.Changed(strings.Join(messages, "; ")), nil
}

// checkHomebrew verifies that Homebrew is installed.
func checkHomebrew(ctx context.Context, conn connector.Connector) error {
	ok, err := module.CommandAvailable(ctx, conn, "brew")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("homebrew is not installed")
	}
	return nil
}

// runBrewUpdate runs brew update and reports whether anything changed.
func runBrewUpdate(ctx context.Context, conn connector.Connector) (bool, error) {
	result, err := connector.Run(ctx, conn, "brew update")
	if err != nil {
		return false, fmt.Errorf("brew update failed: %w", err)
	}
	// brew prints "Already up-to-date." when nothing changed
	output := strings.TrimSpace(result.Stdout)
	if output == "Already up-to-date." || output == "" {
		return false, nil
	}
	return true, nil
}

// runBrewUpgradeAll upgrades all installed packages.
func runBrewUpgradeAll(ctx context.Context, conn connector.Connector, cask bool) (bool, error) {
	cmd := "brew upgrade"
	if cask {
		cmd = "brew upgrade --cask"
	}

	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return false, fmt.Errorf("brew upgrade failed: %w", err)
	}

	// Check if anything was upgraded (output contains package names)
	return strings.TrimSpace(result.Stdout) != "", nil
}

// getInstalledPackages returns a map of installed package names.
func getInstalledPackages(ctx context.Context, conn connector.Connector, cask bool) (map[string]bool, error) {
	cmd := "brew list --formula -1"
	if cask {
		cmd = "brew list --cask -1"
	}

	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return nil, fmt.Errorf("brew list failed: %w", err)
	}

	installed := make(map[string]bool)
	for _, line := range strings.Split(result.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			installed[name] = true
		}
	}

	return installed, nil
}

// installPackages installs the specified packages.
func installPackages(ctx context.Context, conn connector.Connector, names []string, cask bool, options []string) error {
	cmd := "brew install"
	if cask {
		cmd = "brew install --cask"
	}

	if len(options) > 0 {
		cmd += " " + strings.Join(options, " ")
	}

	for _, name := range names {
		cmd += " " + connector.ShellQuote(name)
	}

	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("brew install failed: %w", err)
	}

	return nil
}

// removePackages removes the specified packages.
func removePackages(ctx context.Context, conn connector.Connector, names []string, cask bool) error {
	cmd := "brew uninstall"
	if cask {
		cmd = "brew uninstall --cask"
	}

	for _, name := range names {
		cmd += " " + connector.ShellQuote(name)
	}

	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("brew uninstall failed: %w", err)
	}

	return nil
}

// upgradePackages upgrades the specified packages if updates are available.
func upgradePackages(ctx context.Context, conn connector.Connector, names []string, cask bool) ([]string, error) {
	// Check which packages have updates available
	outdated, err := getOutdatedPackages(ctx, conn, cask)
	if err != nil {
		return nil, err
	}

	// Filter to only packages that are outdated
	var toUpgrade []string
	for _, name := range names {
		if outdated[name] {
			toUpgrade = append(toUpgrade, name)
		}
	}

	if len(toUpgrade) == 0 {
		return nil, nil
	}

	cmd := "brew upgrade"
	if cask {
		cmd = "brew upgrade --cask"
	}

	for _, name := range toUpgrade {
		cmd += " " + connector.ShellQuote(name)
	}

	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return nil, fmt.Errorf("brew upgrade failed: %w", err)
	}

	return toUpgrade, nil
}

// getOutdatedPackages returns a map of packages that have updates available.
func getOutdatedPackages(ctx context.Context, conn connector.Connector, cask bool) (map[string]bool, error) {
	cmd := "brew outdated --formula -q"
	if cask {
		cmd = "brew outdated --cask -q"
	}

	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return nil, fmt.Errorf("brew outdated failed: %w", err)
	}

	outdated := make(map[string]bool)
	for _, line := range strings.Split(result.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			outdated[name] = true
		}
	}

	return outdated, nil
}

// Check determines whether the brew module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	if err := checkHomebrew(ctx, conn); err != nil {
		return nil, err
	}

	stateStr := module.GetString(params, "state", "present")
	state := State(stateStr)
	cask := module.GetBool(params, "cask", false)
	upgradeAll := module.GetBool(params, "upgrade_all", false)
	updateHomebrew := module.GetBool(params, "update_homebrew", false)

	if updateHomebrew {
		return module.UncertainChange("update_homebrew always runs"), nil
	}
	if upgradeAll {
		return module.UncertainChange("upgrade_all always runs"), nil
	}

	names := module.GetStringSlice(params, "name")
	if len(names) == 0 {
		return module.NoChange("no packages specified"), nil
	}

	installed, err := getInstalledPackages(ctx, conn, cask)
	if err != nil {
		return nil, fmt.Errorf("failed to get installed packages: %w", err)
	}

	var toInstall, toRemove, toUpgrade []string

	for _, name := range names {
		isInstalled := installed[name]
		switch state {
		case StatePresent:
			if !isInstalled {
				toInstall = append(toInstall, name)
			}
		case StateAbsent:
			if isInstalled {
				toRemove = append(toRemove, name)
			}
		case StateLatest:
			if !isInstalled {
				toInstall = append(toInstall, name)
			} else {
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	// For latest state, check which packages are actually outdated
	if state == StateLatest && len(toUpgrade) > 0 {
		outdated, err := getOutdatedPackages(ctx, conn, cask)
		if err != nil {
			return nil, err
		}
		var actualUpgrade []string
		for _, name := range toUpgrade {
			if outdated[name] {
				actualUpgrade = append(actualUpgrade, name)
			}
		}
		toUpgrade = actualUpgrade
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

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)
