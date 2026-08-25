// Package waitfor provides a module that waits for a condition to be met.
package waitfor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module waits for a condition to be met before continuing.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "wait_for"
}

// Example returns a usage example for the wait_for module.
func (m *Module) Example() string {
	return `- name: Wait for the web port
  wait_for:
    type: port
    host: localhost
    port: 443
    timeout: 60`
}

// checkResult holds the outcome of a single condition check.
type checkResult struct {
	success bool
	// data holds extra info from the check (e.g., stdout, status_code).
	data map[string]any
	// fatal means the error should stop polling immediately.
	fatal bool
	err   error
}

// Run executes the wait_for module.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	typ, err := module.RequireString(params, "type")
	if err != nil {
		return nil, err
	}

	timeout := module.GetInt(params, "timeout", 300)
	interval := module.GetInt(params, "interval", 5)
	state := module.GetString(params, "state", "started")

	// Validate type-specific params and build the checker.
	checker, err := buildChecker(typ, state, params, conn)
	if err != nil {
		return nil, err
	}

	// Run the polling loop.
	return poll(ctx, checker, typ, state, params, timeout, interval)
}

// checkerFunc is called on each poll iteration.
type checkerFunc func(ctx context.Context) checkResult

// buildChecker validates type-specific parameters and returns a checker function.
func buildChecker(typ, state string, params map[string]any, conn connector.Connector) (checkerFunc, error) {
	switch typ {
	case "port":
		return buildPortChecker(state, params)
	case "path":
		return buildPathChecker(state, params, conn)
	case "command":
		return buildCommandChecker(params, conn)
	case "url":
		return buildURLChecker(params)
	default:
		return nil, fmt.Errorf("unsupported wait_for type %q (must be port, path, command, or url)", typ)
	}
}

// poll runs the checker in a loop until success or timeout.
func poll(ctx context.Context, checker checkerFunc, typ, state string, params map[string]any, timeout, interval int) (*module.Result, error) {
	timeoutDur := time.Duration(timeout) * time.Second
	intervalDur := time.Duration(interval) * time.Second

	ctx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	ticker := time.NewTicker(intervalDur)
	defer ticker.Stop()

	start := time.Now()
	attempts := 0

	// Check immediately before first tick.
	attempts++
	cr := checker(ctx)
	if cr.fatal {
		return nil, cr.err
	}
	if cr.success {
		return buildResult(start, attempts, cr.data), nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s", timeoutMessage(typ, state, params))
		case <-ticker.C:
			attempts++
			cr := checker(ctx)
			if cr.fatal {
				return nil, cr.err
			}
			if cr.success {
				return buildResult(start, attempts, cr.data), nil
			}
		}
	}
}

func buildResult(start time.Time, attempts int, extra map[string]any) *module.Result {
	data := map[string]any{
		"elapsed":  time.Since(start).Seconds(),
		"attempts": attempts,
	}
	for k, v := range extra {
		data[k] = v
	}
	return module.ChangedWithData("condition met", data)
}

func timeoutMessage(typ, state string, params map[string]any) string {
	switch typ {
	case "port":
		host := module.GetString(params, "host", "localhost")
		port := module.GetInt(params, "port", 0)
		if state == "stopped" {
			return fmt.Sprintf("timeout waiting for port %d on %s to close", port, host)
		}
		return fmt.Sprintf("timeout waiting for port %d on %s", port, host)
	case "path":
		path := module.GetString(params, "path", "")
		if state == "stopped" {
			return fmt.Sprintf("timeout waiting for path %s to be absent", path)
		}
		return fmt.Sprintf("timeout waiting for path %s to exist", path)
	case "command":
		return "timeout waiting for command to succeed"
	case "url":
		u := module.GetString(params, "url", "")
		return fmt.Sprintf("timeout waiting for url %s", u)
	default:
		return "timeout waiting for condition"
	}
}

// --- Port checker ---

func buildPortChecker(state string, params map[string]any) (checkerFunc, error) {
	port := module.GetInt(params, "port", 0)
	if port == 0 {
		if _, err := module.RequireString(params, "port"); err != nil {
			return nil, err
		}
	}
	host := module.GetString(params, "host", "localhost")
	addr := fmt.Sprintf("%s:%d", host, port)

	return func(ctx context.Context) checkResult {
		dialTimeout := 5 * time.Second
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if conn != nil {
			conn.Close()
		}
		reachable := err == nil

		if state == "stopped" {
			return checkResult{success: !reachable}
		}
		return checkResult{success: reachable}
	}, nil
}

// --- Path checker ---

func buildPathChecker(state string, params map[string]any, conn connector.Connector) (checkerFunc, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) checkResult {
		result, err := conn.Execute(ctx, fmt.Sprintf("test -e %s", connector.ShellQuote(path)))
		if err != nil {
			// Transport error — treat as non-fatal retry.
			return checkResult{success: false}
		}
		exists := result.ExitCode == 0

		if state == "stopped" {
			return checkResult{success: !exists}
		}
		return checkResult{success: exists}
	}, nil
}

// --- Command checker ---

func buildCommandChecker(params map[string]any, conn connector.Connector) (checkerFunc, error) {
	cmd, err := module.RequireString(params, "cmd")
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) checkResult {
		result, err := conn.Execute(ctx, cmd)
		if err != nil {
			// Transport-level error — fatal, stop polling.
			return checkResult{fatal: true, err: fmt.Errorf("command transport error: %w", err)}
		}
		if result.ExitCode == 0 {
			return checkResult{
				success: true,
				data: map[string]any{
					"stdout": strings.TrimSpace(result.Stdout),
					"stderr": strings.TrimSpace(result.Stderr),
				},
			}
		}
		return checkResult{success: false}
	}, nil
}

// --- URL checker ---

func buildURLChecker(params map[string]any) (checkerFunc, error) {
	rawURL, err := module.RequireString(params, "url")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https scheme")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	return func(ctx context.Context) checkResult {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return checkResult{success: false}
		}
		resp, err := client.Do(req)
		if err != nil {
			// Connection error — retryable.
			return checkResult{success: false}
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return checkResult{
				success: true,
				data: map[string]any{
					"status_code": resp.StatusCode,
				},
			}
		}
		return checkResult{success: false}
	}, nil
}

// Check returns an uncertain result since wait_for cannot predict future state.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	// Validate params even in check mode.
	typ, err := module.RequireString(params, "type")
	if err != nil {
		return nil, err
	}
	state := module.GetString(params, "state", "started")
	if _, err := buildChecker(typ, state, params, conn); err != nil {
		return nil, err
	}
	return module.UncertainChange("wait_for cannot predict future state"), nil
}

// Description returns a human-readable description of the module.
func (m *Module) Description() string {
	return "Wait for a condition to be met (port open, file exists, command succeeds, or URL responds)"
}

// Parameters returns documentation for the module's parameters.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "type", Type: "string", Required: true, Description: "Condition type: port, path, command, or url"},
		{Name: "host", Type: "string", Required: false, Default: "localhost", Description: "Host to check (port type only)"},
		{Name: "port", Type: "int", Required: false, Description: "TCP port number (required for port type)"},
		{Name: "path", Type: "string", Required: false, Description: "Filesystem path (required for path type)"},
		{Name: "cmd", Type: "string", Required: false, Description: "Shell command (required for command type)"},
		{Name: "url", Type: "string", Required: false, Description: "HTTP(S) URL (required for url type)"},
		{Name: "timeout", Type: "int", Required: false, Default: "300", Description: "Maximum wait time in seconds"},
		{Name: "interval", Type: "int", Required: false, Default: "5", Description: "Poll interval in seconds"},
		{Name: "state", Type: "string", Required: false, Default: "started", Description: "Desired state: started or stopped (port and path types)"},
	}
}

// Interface compliance.
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
)
