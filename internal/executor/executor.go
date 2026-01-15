// Package executor runs playbooks against target hosts.
package executor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eugenetaranov/bolt/internal/connector"
	"github.com/eugenetaranov/bolt/internal/connector/docker"
	"github.com/eugenetaranov/bolt/internal/connector/local"
	"github.com/eugenetaranov/bolt/internal/module"
	"github.com/eugenetaranov/bolt/internal/output"
	"github.com/eugenetaranov/bolt/internal/playbook"
	"github.com/eugenetaranov/bolt/pkg/facts"
)

// Executor runs playbooks.
type Executor struct {
	// Output handles formatted output.
	Output *output.Output

	// DryRun only shows what would be done without making changes.
	DryRun bool

	// Debug enables detailed output.
	Debug bool

	// connectors caches connectors by host.
	connectors map[string]connector.Connector
}

// New creates a new executor.
func New() *Executor {
	return &Executor{
		Output:     output.New(os.Stdout),
		connectors: make(map[string]connector.Connector),
	}
}

// RunResult holds the result of a playbook run.
type RunResult struct {
	// Success is true if all plays completed successfully.
	Success bool

	// Stats holds execution statistics.
	Stats *Stats
}

// Stats holds execution statistics.
type Stats struct {
	Plays     int
	Tasks     int
	OK        int
	Changed   int
	Failed    int
	Skipped   int
	StartTime time.Time
	EndTime   time.Time
}

// Duration returns the total execution time.
func (s *Stats) Duration() time.Duration {
	return s.EndTime.Sub(s.StartTime)
}

// GetOK returns the OK count (implements output.Stats).
func (s *Stats) GetOK() int { return s.OK }

// GetChanged returns the Changed count (implements output.Stats).
func (s *Stats) GetChanged() int { return s.Changed }

// GetFailed returns the Failed count (implements output.Stats).
func (s *Stats) GetFailed() int { return s.Failed }

// GetSkipped returns the Skipped count (implements output.Stats).
func (s *Stats) GetSkipped() int { return s.Skipped }

// GetDuration returns the duration (implements output.Stats).
func (s *Stats) GetDuration() time.Duration { return s.Duration() }

// PlayContext holds state for a play execution.
type PlayContext struct {
	// Play is the current play.
	Play *playbook.Play

	// Vars holds all variables (play vars + facts + registered).
	Vars map[string]any

	// Facts holds gathered system facts.
	Facts map[string]any

	// Registered holds task results stored via register.
	Registered map[string]any

	// NotifiedHandlers tracks which handlers should run.
	NotifiedHandlers map[string]bool

	// Connector is the connection to the target.
	Connector connector.Connector
}

// Run executes a playbook.
func (e *Executor) Run(ctx context.Context, pb *playbook.Playbook) (*RunResult, error) {
	stats := &Stats{
		StartTime: time.Now(),
		Plays:     len(pb.Plays),
	}

	result := &RunResult{
		Success: true,
		Stats:   stats,
	}

	e.Output.PlaybookStart(pb.Path)

	for _, play := range pb.Plays {
		if err := e.runPlay(ctx, play, stats); err != nil {
			result.Success = false
			e.Output.Error("Play failed: %v", err)
			break
		}
	}

	stats.EndTime = time.Now()
	e.Output.PlaybookEnd(stats)

	return result, nil
}

// runPlay executes a single play.
func (e *Executor) runPlay(ctx context.Context, play *playbook.Play, stats *Stats) error {
	e.Output.PlayStart(play)

	// Create play context
	pctx := &PlayContext{
		Play:             play,
		Vars:             make(map[string]any),
		Facts:            make(map[string]any),
		Registered:       make(map[string]any),
		NotifiedHandlers: make(map[string]bool),
	}

	// Copy play vars
	for k, v := range play.Vars {
		pctx.Vars[k] = v
	}

	// Add environment variables
	pctx.Vars["env"] = getEnvMap()

	// Get connector for this play
	conn, err := e.getConnector(play)
	if err != nil {
		return fmt.Errorf("failed to create connector: %w", err)
	}
	pctx.Connector = conn

	// Connect
	if err := conn.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Gather facts if enabled
	if play.ShouldGatherFacts() {
		e.Output.TaskStart("Gathering Facts", "")
		f, err := facts.Gather(ctx, conn)
		if err != nil {
			e.Output.TaskResult("Gathering Facts", "failed", false, err.Error())
			return fmt.Errorf("failed to gather facts: %w", err)
		}
		pctx.Facts = f
		pctx.Vars["facts"] = f
		e.Output.TaskResult("Gathering Facts", "ok", false, "")
	}

	// Execute tasks
	for _, task := range play.Tasks {
		stats.Tasks++

		taskResult, err := e.runTask(ctx, pctx, task)
		if err != nil {
			stats.Failed++
			if !task.IgnoreErrors {
				return err
			}
			e.Output.TaskResult(task.String(), "failed (ignored)", false, err.Error())
			continue
		}

		switch taskResult.Status {
		case "ok":
			stats.OK++
		case "changed":
			stats.Changed++
		case "skipped":
			stats.Skipped++
		}
	}

	// Run notified handlers
	if err := e.runHandlers(ctx, pctx, stats); err != nil {
		return err
	}

	return nil
}

// TaskResult holds the result of a task execution.
type TaskResult struct {
	Status  string // ok, changed, skipped, failed
	Changed bool
	Data    map[string]any
	Error   error
}

// runTask executes a single task.
func (e *Executor) runTask(ctx context.Context, pctx *PlayContext, task *playbook.Task) (*TaskResult, error) {
	taskName := task.String()

	// Check 'when' condition
	if task.When != "" {
		shouldRun, err := e.evaluateCondition(task.When, pctx)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate 'when' condition: %w", err)
		}
		if !shouldRun {
			e.Output.TaskResult(taskName, "skipped", false, "when condition not met")
			return &TaskResult{Status: "skipped"}, nil
		}
	}

	// Handle loops
	if len(task.Loop) > 0 {
		return e.runTaskLoop(ctx, pctx, task)
	}

	// Run single task
	return e.runSingleTask(ctx, pctx, task)
}

// runSingleTask executes a task once.
func (e *Executor) runSingleTask(ctx context.Context, pctx *PlayContext, task *playbook.Task) (*TaskResult, error) {
	taskName := task.String()
	e.Output.TaskStart(taskName, task.Module)

	// Expand shorthand syntax
	playbook.ExpandShorthand(task)

	// Resolve module
	mod := module.Get(task.Module)
	if mod == nil {
		err := fmt.Errorf("unknown module: %s", task.Module)
		e.Output.TaskResult(taskName, "failed", false, err.Error())
		return nil, err
	}

	// Interpolate variables in params
	params, err := e.interpolateParams(task.Params, pctx)
	if err != nil {
		e.Output.TaskResult(taskName, "failed", false, err.Error())
		return nil, fmt.Errorf("failed to interpolate parameters: %w", err)
	}

	// Handle dry run
	if e.DryRun {
		e.Output.TaskResult(taskName, "skipped (dry run)", false, "")
		return &TaskResult{Status: "skipped"}, nil
	}

	// Execute with retries
	var result *module.Result
	var lastErr error
	maxAttempts := task.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			e.Output.Info("Retry %d/%d for task: %s", attempt, maxAttempts, taskName)
			time.Sleep(time.Duration(task.Delay) * time.Second)
		}

		result, lastErr = mod.Run(ctx, pctx.Connector, params)
		if lastErr == nil {
			break
		}
	}

	if lastErr != nil {
		e.Output.TaskResult(taskName, "failed", false, lastErr.Error())
		return &TaskResult{Status: "failed", Error: lastErr}, lastErr
	}

	// Store registered result
	if task.Register != "" {
		pctx.Registered[task.Register] = map[string]any{
			"changed": result.Changed,
			"message": result.Message,
			"data":    result.Data,
		}
		pctx.Vars[task.Register] = pctx.Registered[task.Register]
	}

	// Handle notify
	if result.Changed && len(task.Notify) > 0 {
		for _, handler := range task.Notify {
			pctx.NotifiedHandlers[handler] = true
		}
	}

	// Determine status
	status := "ok"
	if result.Changed {
		status = "changed"
	}

	e.Output.TaskResult(taskName, status, result.Changed, result.Message)

	return &TaskResult{
		Status:  status,
		Changed: result.Changed,
		Data:    result.Data,
	}, nil
}

// runTaskLoop executes a task for each item in a loop.
func (e *Executor) runTaskLoop(ctx context.Context, pctx *PlayContext, task *playbook.Task) (*TaskResult, error) {
	loopVar := task.GetLoopVar()
	var anyChanged bool

	for i, item := range task.Loop {
		// Set loop variable
		pctx.Vars[loopVar] = item
		pctx.Vars["loop_index"] = i

		result, err := e.runSingleTask(ctx, pctx, task)
		if err != nil {
			return result, err
		}

		if result.Changed {
			anyChanged = true
		}
	}

	// Clean up loop variables
	delete(pctx.Vars, loopVar)
	delete(pctx.Vars, "loop_index")

	status := "ok"
	if anyChanged {
		status = "changed"
	}

	return &TaskResult{Status: status, Changed: anyChanged}, nil
}

// runHandlers executes notified handlers.
func (e *Executor) runHandlers(ctx context.Context, pctx *PlayContext, stats *Stats) error {
	if len(pctx.NotifiedHandlers) == 0 {
		return nil
	}

	e.Output.Section("RUNNING HANDLERS")

	for _, handler := range pctx.Play.Handlers {
		if !pctx.NotifiedHandlers[handler.Name] {
			continue
		}

		stats.Tasks++

		result, err := e.runSingleTask(ctx, pctx, handler)
		if err != nil {
			stats.Failed++
			return fmt.Errorf("handler '%s' failed: %w", handler.Name, err)
		}

		switch result.Status {
		case "ok":
			stats.OK++
		case "changed":
			stats.Changed++
		}
	}

	return nil
}

// getConnector returns a connector for the play.
func (e *Executor) getConnector(play *playbook.Play) (connector.Connector, error) {
	connType := play.GetConnection()

	switch connType {
	case "local":
		var opts []local.Option
		if play.Become {
			opts = append(opts, local.WithSudo(play.GetBecomeUser()))
		}
		return local.New(opts...), nil

	case "docker":
		// For docker, hosts is the container name/ID
		container := play.Hosts
		var opts []docker.Option
		if play.Become && play.BecomeUser != "" {
			opts = append(opts, docker.WithUser(play.GetBecomeUser()))
		}
		return docker.New(container, opts...), nil

	case "ssh":
		return nil, fmt.Errorf("SSH connector not yet implemented")

	case "ssm":
		return nil, fmt.Errorf("SSM connector not yet implemented")

	default:
		return nil, fmt.Errorf("unknown connection type: %s", connType)
	}
}

// evaluateCondition evaluates a when condition.
func (e *Executor) evaluateCondition(condition string, pctx *PlayContext) (bool, error) {
	// Simple condition evaluation
	// Supports: variable truthiness, comparisons, and registered results

	condition = strings.TrimSpace(condition)

	// Check for negation
	if strings.HasPrefix(condition, "not ") {
		result, err := e.evaluateCondition(condition[4:], pctx)
		return !result, err
	}

	// Check for registered variable .changed
	if strings.HasSuffix(condition, ".changed") {
		varName := strings.TrimSuffix(condition, ".changed")
		if reg, ok := pctx.Registered[varName]; ok {
			if regMap, ok := reg.(map[string]any); ok {
				if changed, ok := regMap["changed"].(bool); ok {
					return changed, nil
				}
			}
		}
		return false, nil
	}

	// Check for == comparison
	if strings.Contains(condition, "==") {
		parts := strings.SplitN(condition, "==", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])

		leftVal := e.resolveValue(left, pctx)
		rightVal := e.resolveValue(right, pctx)

		return fmt.Sprintf("%v", leftVal) == fmt.Sprintf("%v", rightVal), nil
	}

	// Check for != comparison
	if strings.Contains(condition, "!=") {
		parts := strings.SplitN(condition, "!=", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])

		leftVal := e.resolveValue(left, pctx)
		rightVal := e.resolveValue(right, pctx)

		return fmt.Sprintf("%v", leftVal) != fmt.Sprintf("%v", rightVal), nil
	}

	// Simple variable truthiness
	val := e.resolveValue(condition, pctx)
	return isTruthy(val), nil
}

// resolveValue resolves a value that might be a variable reference.
func (e *Executor) resolveValue(s string, pctx *PlayContext) any {
	s = strings.TrimSpace(s)

	// String literal
	if (strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) ||
		(strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) {
		return s[1 : len(s)-1]
	}

	// Boolean literals
	if s == "true" || s == "True" {
		return true
	}
	if s == "false" || s == "False" {
		return false
	}

	// Variable lookup
	if val, ok := pctx.Vars[s]; ok {
		return val
	}

	// Dotted variable lookup (e.g., facts.os)
	if strings.Contains(s, ".") {
		parts := strings.Split(s, ".")
		var current any = pctx.Vars
		for _, part := range parts {
			if m, ok := current.(map[string]any); ok {
				current = m[part]
			} else {
				return nil
			}
		}
		return current
	}

	return s
}

// isTruthy returns whether a value is considered truthy.
func isTruthy(v any) bool {
	if v == nil {
		return false
	}

	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val != "" && val != "false" && val != "False" && val != "no"
	case int, int64, float64:
		return val != 0
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}

// getEnvMap returns environment variables as a map.
func getEnvMap() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.Index(e, "="); idx > 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}
	return env
}
