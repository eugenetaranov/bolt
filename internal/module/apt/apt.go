// Package apt provides a module for managing packages on Debian/Ubuntu systems.
package apt

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
	StatePurged  State = "purged"  // Ensure package and config files are removed
)

// Module manages apt packages on Debian/Ubuntu systems.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "apt"
}

// Run executes the apt module.
//
// Parameters:
//   - name (string|[]string): Package name(s) to manage
//   - state (string): Desired state - present, absent, latest, purged (default: present)
//   - update_cache (bool): Run apt-get update before operations (default: false)
//   - upgrade (string): Upgrade mode - none, yes, safe, full, dist (default: none)
//   - cache_valid_time (int): Cache validity in seconds; skip update if cache is newer (default: 0)
//   - install_recommends (bool): Install recommended packages (default: true)
//   - autoremove (bool): Remove unused dependency packages (default: false)
//   - deb (string): Path or URL to .deb file to install
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Check if apt is available
	if err := checkApt(ctx, conn); err != nil {
		return nil, err
	}

	stateStr := getString(params, "state", "present")
	state := State(stateStr)
	updateCache := getBool(params, "update_cache", false)
	upgrade := getString(params, "upgrade", "none")
	cacheValidTime := getInt(params, "cache_valid_time", 0)
	installRecommends := getBool(params, "install_recommends", true)
	autoremove := getBool(params, "autoremove", false)
	debFile := getString(params, "deb", "")

	// Validate state
	switch state {
	case StatePresent, StateAbsent, StateLatest, StatePurged:
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be present, absent, latest, or purged", state)
	}

	// Validate upgrade mode
	switch upgrade {
	case "none", "yes", "safe", "full", "dist":
		// Valid
	default:
		return nil, fmt.Errorf("invalid upgrade mode '%s': must be none, yes, safe, full, or dist", upgrade)
	}

	var changed bool
	var messages []string

	// Update cache if requested
	if updateCache {
		updated, err := runAptUpdate(ctx, conn, cacheValidTime)
		if err != nil {
			return nil, fmt.Errorf("failed to update cache: %w", err)
		}
		if updated {
			messages = append(messages, "cache updated")
			changed = true
		}
	}

	// Run upgrade if requested
	if upgrade != "none" {
		upgraded, err := runAptUpgrade(ctx, conn, upgrade)
		if err != nil {
			return nil, fmt.Errorf("failed to upgrade: %w", err)
		}
		if upgraded {
			messages = append(messages, fmt.Sprintf("%s upgrade completed", upgrade))
			changed = true
		}
	}

	// Install .deb file if specified
	if debFile != "" {
		installed, err := installDebFile(ctx, conn, debFile)
		if err != nil {
			return nil, err
		}
		if installed {
			messages = append(messages, fmt.Sprintf("installed %s", debFile))
			changed = true
		}
	}

	// Get package names
	names := getPackageNames(params)
	if len(names) == 0 {
		if !updateCache && upgrade == "none" && debFile == "" {
			return nil, fmt.Errorf("'name' parameter is required when not using update_cache, upgrade, or deb")
		}
		// Handle autoremove
		if autoremove {
			removed, err := runAutoremove(ctx, conn)
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
	pkgStates, err := getPackageStates(ctx, conn, names)
	if err != nil {
		return nil, fmt.Errorf("failed to get package states: %w", err)
	}

	// Determine actions needed
	var toInstall, toRemove, toUpgrade, toPurge []string

	for _, name := range names {
		pkgState := pkgStates[name]

		switch state {
		case StatePresent:
			if !pkgState.Installed {
				toInstall = append(toInstall, name)
			}
		case StateAbsent:
			if pkgState.Installed {
				toRemove = append(toRemove, name)
			}
		case StatePurged:
			if pkgState.Installed || pkgState.ConfigFiles {
				toPurge = append(toPurge, name)
			}
		case StateLatest:
			if !pkgState.Installed {
				toInstall = append(toInstall, name)
			} else if pkgState.Upgradable {
				toUpgrade = append(toUpgrade, name)
			}
		}
	}

	// Install packages
	if len(toInstall) > 0 {
		if err := installPackages(ctx, conn, toInstall, installRecommends); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("installed: %s", strings.Join(toInstall, ", ")))
		changed = true
	}

	// Remove packages
	if len(toRemove) > 0 {
		if err := removePackages(ctx, conn, toRemove, false); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("removed: %s", strings.Join(toRemove, ", ")))
		changed = true
	}

	// Purge packages
	if len(toPurge) > 0 {
		if err := removePackages(ctx, conn, toPurge, true); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("purged: %s", strings.Join(toPurge, ", ")))
		changed = true
	}

	// Upgrade packages
	if len(toUpgrade) > 0 {
		if err := installPackages(ctx, conn, toUpgrade, installRecommends); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("upgraded: %s", strings.Join(toUpgrade, ", ")))
		changed = true
	}

	// Handle autoremove
	if autoremove {
		removed, err := runAutoremove(ctx, conn)
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

// packageState holds the state of a package.
type packageState struct {
	Installed   bool
	Upgradable  bool
	ConfigFiles bool // Package removed but config files remain
}

// checkApt verifies that apt is available.
func checkApt(ctx context.Context, conn connector.Connector) error {
	result, err := conn.Execute(ctx, "command -v apt-get")
	if err != nil {
		return fmt.Errorf("failed to check for apt: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("apt-get is not available (not a Debian/Ubuntu system?)")
	}
	return nil
}

// runAptUpdate runs apt-get update.
func runAptUpdate(ctx context.Context, conn connector.Connector, cacheValidTime int) (bool, error) {
	// Check cache age if cacheValidTime is set
	if cacheValidTime > 0 {
		cmd := fmt.Sprintf(`find /var/lib/apt/lists -maxdepth 0 -mmin +%d 2>/dev/null | grep -q . && echo "stale" || echo "fresh"`,
			cacheValidTime/60)
		result, err := conn.Execute(ctx, cmd)
		if err == nil && strings.TrimSpace(result.Stdout) == "fresh" {
			return false, nil
		}
	}

	result, err := conn.Execute(ctx, "DEBIAN_FRONTEND=noninteractive apt-get update -qq")
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("apt-get update failed: %s", result.Stderr)
	}
	return true, nil
}

// runAptUpgrade runs apt-get upgrade with the specified mode.
func runAptUpgrade(ctx context.Context, conn connector.Connector, mode string) (bool, error) {
	var cmd string
	switch mode {
	case "yes", "safe":
		cmd = "DEBIAN_FRONTEND=noninteractive apt-get upgrade -y -qq"
	case "full":
		cmd = "DEBIAN_FRONTEND=noninteractive apt-get full-upgrade -y -qq"
	case "dist":
		cmd = "DEBIAN_FRONTEND=noninteractive apt-get dist-upgrade -y -qq"
	default:
		return false, nil
	}

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("apt-get upgrade failed: %s", result.Stderr)
	}

	// Check if anything was upgraded
	return strings.Contains(result.Stdout, "upgraded") || strings.Contains(result.Stderr, "upgraded"), nil
}

// getPackageStates returns the state of the specified packages.
func getPackageStates(ctx context.Context, conn connector.Connector, names []string) (map[string]*packageState, error) {
	states := make(map[string]*packageState)
	for _, name := range names {
		states[name] = &packageState{}
	}

	// Query dpkg for installed packages
	// Status can be: installed, config-files, not-installed
	cmd := fmt.Sprintf("dpkg-query -W -f='${Package}|${Status}\\n' %s 2>/dev/null || true",
		strings.Join(names, " "))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}

	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}

		name := parts[0]
		status := parts[1]

		if state, ok := states[name]; ok {
			if strings.Contains(status, "install ok installed") {
				state.Installed = true
			} else if strings.Contains(status, "config-files") {
				state.ConfigFiles = true
			}
		}
	}

	// Check for upgradable packages
	result, err = conn.Execute(ctx, "apt list --upgradable 2>/dev/null | tail -n +2")
	if err == nil {
		for _, line := range strings.Split(result.Stdout, "\n") {
			// Format: package/source version [upgradable from: version]
			if idx := strings.Index(line, "/"); idx > 0 {
				pkgName := line[:idx]
				if state, ok := states[pkgName]; ok && state.Installed {
					state.Upgradable = true
				}
			}
		}
	}

	return states, nil
}

// installPackages installs the specified packages.
func installPackages(ctx context.Context, conn connector.Connector, names []string, installRecommends bool) error {
	recommends := "--no-install-recommends"
	if installRecommends {
		recommends = "--install-recommends"
	}

	cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s %s",
		recommends, strings.Join(names, " "))

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to install packages: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("apt-get install failed: %s", result.Stderr)
	}

	return nil
}

// removePackages removes the specified packages.
func removePackages(ctx context.Context, conn connector.Connector, names []string, purge bool) error {
	action := "remove"
	if purge {
		action = "purge"
	}

	cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get %s -y -qq %s",
		action, strings.Join(names, " "))

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to remove packages: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("apt-get %s failed: %s", action, result.Stderr)
	}

	return nil
}

// installDebFile installs a .deb file.
func installDebFile(ctx context.Context, conn connector.Connector, path string) (bool, error) {
	// Download if it's a URL
	localPath := path
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		localPath = "/tmp/bolt-pkg.deb"
		cmd := fmt.Sprintf("curl -fsSL -o %s %s", shellQuote(localPath), shellQuote(path))
		result, err := conn.Execute(ctx, cmd)
		if err != nil {
			return false, fmt.Errorf("failed to download deb file: %w", err)
		}
		if result.ExitCode != 0 {
			return false, fmt.Errorf("failed to download deb file: %s", result.Stderr)
		}
	}

	// Install the .deb file
	cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive dpkg -i %s || apt-get install -f -y -qq",
		shellQuote(localPath))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to install deb file: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("dpkg install failed: %s", result.Stderr)
	}

	return true, nil
}

// runAutoremove removes unused dependency packages.
func runAutoremove(ctx context.Context, conn connector.Connector) (bool, error) {
	result, err := conn.Execute(ctx, "DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq")
	if err != nil {
		return false, fmt.Errorf("failed to autoremove: %w", err)
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("apt-get autoremove failed: %s", result.Stderr)
	}

	return strings.Contains(result.Stdout, "Removing") || strings.Contains(result.Stderr, "Removing"), nil
}

// getPackageNames extracts package names from params.
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

	// Slice of any
	if slice, ok := v.([]any); ok {
		var names []string
		for _, item := range slice {
			if s, ok := item.(string); ok && s != "" {
				names = append(names, s)
			}
		}
		return names
	}

	// String slice
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

func getInt(params map[string]any, key string, defaultValue int) int {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return defaultValue
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)
