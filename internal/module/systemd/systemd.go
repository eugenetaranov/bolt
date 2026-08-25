// Package systemd provides a module for managing systemd services.
package systemd

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

// Module manages systemd services on the target system.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "systemd"
}

// Description returns a short summary of the systemd module.
func (m *Module) Description() string {
	return "Manage systemd services: state (started/stopped/restarted/reloaded) and enablement."
}

// Parameters returns the parameter documentation for the systemd module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "name", Type: "string", Required: true, Description: "Service unit name"},
		{Name: "state", Type: "string", Description: "Desired state: started, stopped, restarted, reloaded"},
		{Name: "enabled", Type: "bool", Description: "Whether the service should start on boot"},
		{Name: "masked", Type: "bool", Description: "Whether the service should be masked"},
		{Name: "daemon_reload", Type: "bool", Default: "false", Description: "Run daemon-reload before applying"},
	}
}

// Example returns a usage example for the systemd module.
func (m *Module) Example() string {
	return `- name: Enable and start nginx
  systemd:
    name: nginx
    state: started
    enabled: true`
}

// Ensure Module implements the documentation interfaces.
var (
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

// Run executes the systemd module.
//
// Parameters:
//   - name (string, required): Service unit name (e.g., "docker", "nginx.service")
//   - state (string): Desired runtime state - started, stopped, restarted, reloaded (default: "")
//   - enabled (bool): Whether the service should be enabled at boot (default: nil/unset)
//   - daemon_reload (bool): Run daemon-reload before other operations (default: false)
//   - masked (bool): Whether the service should be masked (default: nil/unset)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	name, err := module.RequireString(params, "name")
	if err != nil {
		return nil, err
	}
	name = normalizeUnit(name)

	state := module.GetString(params, "state", "")
	daemonReload := module.GetBool(params, "daemon_reload", false)

	// Validate state
	switch state {
	case "", "started", "stopped", "restarted", "reloaded":
		// Valid
	default:
		return nil, fmt.Errorf("invalid state '%s': must be started, stopped, restarted, or reloaded", state)
	}

	if err := checkSystemd(ctx, conn); err != nil {
		return nil, err
	}

	var changed bool
	var messages []string

	// Daemon reload (side-effect, not a state change)
	if daemonReload {
		if err := runDaemonReload(ctx, conn); err != nil {
			return nil, err
		}
		messages = append(messages, "daemon reloaded")
	}

	// Handle masked
	if v, ok := params["masked"]; ok {
		masked, _ := v.(bool)
		maskChanged, err := ensureMasked(ctx, conn, name, masked)
		if err != nil {
			return nil, err
		}
		if maskChanged {
			if masked {
				messages = append(messages, "masked")
			} else {
				messages = append(messages, "unmasked")
			}
			changed = true
		}
	}

	// Handle enabled
	if v, ok := params["enabled"]; ok {
		enabled, _ := v.(bool)
		enableChanged, err := ensureEnabled(ctx, conn, name, enabled)
		if err != nil {
			return nil, err
		}
		if enableChanged {
			if enabled {
				messages = append(messages, "enabled")
			} else {
				messages = append(messages, "disabled")
			}
			changed = true
		}
	}

	// Handle state
	switch state {
	case "started":
		active, err := isActive(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if !active {
			if err := runSystemctl(ctx, conn, "start", name); err != nil {
				return nil, err
			}
			messages = append(messages, "started")
			changed = true
		}
	case "stopped":
		active, err := isActive(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if active {
			if err := runSystemctl(ctx, conn, "stop", name); err != nil {
				return nil, err
			}
			messages = append(messages, "stopped")
			changed = true
		}
	case "restarted":
		if err := runSystemctl(ctx, conn, "restart", name); err != nil {
			return nil, err
		}
		messages = append(messages, "restarted")
		changed = true
	case "reloaded":
		if err := runSystemctl(ctx, conn, "reload", name); err != nil {
			return nil, err
		}
		messages = append(messages, "reloaded")
		changed = true
	}

	if !changed {
		return module.Unchanged("no changes needed"), nil
	}
	return module.Changed(strings.Join(messages, ", ")), nil
}

// Check determines whether the systemd module would make changes.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	name, err := module.RequireString(params, "name")
	if err != nil {
		return nil, err
	}
	name = normalizeUnit(name)

	state := module.GetString(params, "state", "")
	daemonReload := module.GetBool(params, "daemon_reload", false)

	if err := checkSystemd(ctx, conn); err != nil {
		return nil, err
	}

	var parts []string

	if daemonReload {
		parts = append(parts, "daemon-reload")
	}

	// Check masked
	if v, ok := params["masked"]; ok {
		masked, _ := v.(bool)
		currentlyMasked, err := isMasked(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if masked && !currentlyMasked {
			parts = append(parts, "would mask")
		} else if !masked && currentlyMasked {
			parts = append(parts, "would unmask")
		}
	}

	// Check enabled
	if v, ok := params["enabled"]; ok {
		enabled, _ := v.(bool)
		currentlyEnabled, err := isEnabled(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if enabled && !currentlyEnabled {
			parts = append(parts, "would enable")
		} else if !enabled && currentlyEnabled {
			parts = append(parts, "would disable")
		}
	}

	// Check state
	switch state {
	case "started":
		active, err := isActive(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if !active {
			parts = append(parts, "would start")
		}
	case "stopped":
		active, err := isActive(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		if active {
			parts = append(parts, "would stop")
		}
	case "restarted":
		parts = append(parts, "would restart")
	case "reloaded":
		parts = append(parts, "would reload")
	}

	// Filter out daemon-reload — it's a side-effect, not a state change
	hasRealChange := false
	for _, p := range parts {
		if p != "daemon-reload" {
			hasRealChange = true
			break
		}
	}

	if hasRealChange {
		return module.WouldChange(strings.Join(parts, ", ")), nil
	}
	if len(parts) > 0 {
		return module.NoChange("service already in desired state (daemon-reload only)"), nil
	}
	return module.NoChange("service already in desired state"), nil
}

// normalizeUnit ensures the unit name has a .service suffix.
func normalizeUnit(name string) string {
	if !strings.Contains(name, ".") {
		return name + ".service"
	}
	return name
}

// checkSystemd verifies that systemctl is available.
func checkSystemd(ctx context.Context, conn connector.Connector) error {
	ok, err := module.CommandAvailable(ctx, conn, "systemctl")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("systemctl is not available (not a systemd system?)")
	}
	return nil
}

func runDaemonReload(ctx context.Context, conn connector.Connector) error {
	if _, err := connector.Run(ctx, conn, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload failed: %w", err)
	}
	return nil
}

func runSystemctl(ctx context.Context, conn connector.Connector, action, unit string) error {
	cmd := fmt.Sprintf("systemctl %s %s", action, connector.ShellQuote(unit))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("systemctl %s %s failed: %w", action, unit, err)
	}
	return nil
}

func isActive(ctx context.Context, conn connector.Connector, unit string) (bool, error) {
	cmd := fmt.Sprintf("systemctl is-active %s", connector.ShellQuote(unit))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(result.Stdout) == "active", nil
}

func unitEnabledStatus(ctx context.Context, conn connector.Connector, unit string) (string, error) {
	cmd := fmt.Sprintf("systemctl is-enabled %s", connector.ShellQuote(unit))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func isEnabled(ctx context.Context, conn connector.Connector, unit string) (bool, error) {
	status, err := unitEnabledStatus(ctx, conn, unit)
	return status == "enabled", err
}

func isMasked(ctx context.Context, conn connector.Connector, unit string) (bool, error) {
	status, err := unitEnabledStatus(ctx, conn, unit)
	return status == "masked", err
}

func ensureEnabled(ctx context.Context, conn connector.Connector, unit string, enabled bool) (bool, error) {
	current, err := isEnabled(ctx, conn, unit)
	if err != nil {
		return false, err
	}
	if current == enabled {
		return false, nil
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	if err := runSystemctl(ctx, conn, action, unit); err != nil {
		return false, err
	}
	return true, nil
}

func ensureMasked(ctx context.Context, conn connector.Connector, unit string, masked bool) (bool, error) {
	current, err := isMasked(ctx, conn, unit)
	if err != nil {
		return false, err
	}
	if current == masked {
		return false, nil
	}
	action := "unmask"
	if masked {
		action = "mask"
	}
	if err := runSystemctl(ctx, conn, action, unit); err != nil {
		return false, err
	}
	return true, nil
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)
