// Package brew provides a module for managing Homebrew packages on macOS.
package brew

import (
	"context"
	"fmt"
	"strings"

	"github.com/eugenetaranov/bolt/internal/connector"
	"github.com/eugenetaranov/bolt/internal/module"
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

	stateStr := getString(params, "state", "present")
	state := State(stateStr)
	cask := getBool(params, "cask", false)
	upgradeAll := getBool(params, "upgrade_all", false)
	updateHomebrew := getBool(params, "update_homebrew", false)
	options := getStringSlice(params, "options")

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
		if err := runBrewUpdate(ctx, conn); err != nil {
			return nil, fmt.Errorf("failed to update homebrew: %w", err)
		}
		messages = append(messages, "homebrew updated")
		changed = true
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
	names := getPackageNames(params)
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
	result, err := conn.Execute(ctx, "command -v brew")
	if err != nil {
		return fmt.Errorf("failed to check for homebrew: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("homebrew is not installed")
	}
	return nil
}

// runBrewUpdate runs brew update.
func runBrewUpdate(ctx context.Context, conn connector.Connector) error {
	result, err := conn.Execute(ctx, "brew update")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("brew update failed: %s", result.Stderr)
	}
	return nil
}

// runBrewUpgradeAll upgrades all installed packages.
func runBrewUpgradeAll(ctx context.Context, conn connector.Connector, cask bool) (bool, error) {
	cmd := "brew upgrade"
	if cask {
		cmd = "brew upgrade --cask"
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("brew upgrade failed: %s", result.Stderr)
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

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
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
		cmd += " " + shellQuote(name)
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to install packages: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("brew install failed: %s", result.Stderr)
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
		cmd += " " + shellQuote(name)
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to remove packages: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("brew uninstall failed: %s", result.Stderr)
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
		cmd += " " + shellQuote(name)
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to upgrade packages: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("brew upgrade failed: %s", result.Stderr)
	}

	return toUpgrade, nil
}

// getOutdatedPackages returns a map of packages that have updates available.
func getOutdatedPackages(ctx context.Context, conn connector.Connector, cask bool) (map[string]bool, error) {
	cmd := "brew outdated --formula -q"
	if cask {
		cmd = "brew outdated --cask -q"
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
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

// getPackageNames extracts package names from params.
// Supports both single string and string slice.
func getPackageNames(params map[string]any) []string {
	v, ok := params["name"]
	if !ok {
		return nil
	}

	// Single string
	if s, ok := v.(string); ok {
		if s == "" {
			return nil
		}
		return []string{s}
	}

	// String slice
	if slice, ok := v.([]any); ok {
		var names []string
		for _, item := range slice {
			if s, ok := item.(string); ok && s != "" {
				names = append(names, s)
			}
		}
		return names
	}

	// Already a string slice
	if slice, ok := v.([]string); ok {
		return slice
	}

	return nil
}

// shellQuote quotes a string for safe use in shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// Helper functions for parameter extraction

func getString(params map[string]any, key, defaultValue string) string {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	s, ok := v.(string)
	if !ok {
		return defaultValue
	}
	return s
}

func getBool(params map[string]any, key string, defaultValue bool) bool {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	b, ok := v.(bool)
	if !ok {
		return defaultValue
	}
	return b
}

func getStringSlice(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}

	if slice, ok := v.([]any); ok {
		var result []string
		for _, item := range slice {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}

	if slice, ok := v.([]string); ok {
		return slice
	}

	return nil
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)
