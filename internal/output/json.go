package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tackhq/tack/internal/playbook"
)

// jsonSchemaVersion is the version of the newline-delimited JSON event
// schema. Bump on additive or breaking changes; downstream consumers should
// branch on this. v2 added the `host` field on plan_task/task_start/
// task_result events for multi-host attribution.
const jsonSchemaVersion = 2

// JSONEmitter emits newline-delimited JSON events to stdout.
// Errors are written to stderr. The approval prompt is auto-approved.
type JSONEmitter struct {
	w      io.Writer
	errW   io.Writer
	debug  bool
	diff   bool
	// currentHost tags task_start/task_result events with their host. Set
	// by HostStart and consumed by subsequent task event emissions until
	// the next HostStart.
	currentHost string
}

// NewJSONEmitter creates a JSONEmitter writing events to w and errors to errW.
func NewJSONEmitter(w io.Writer, errW io.Writer) *JSONEmitter {
	return &JSONEmitter{w: w, errW: errW}
}

// Verify JSONEmitter implements Emitter at compile time.
var _ Emitter = (*JSONEmitter)(nil)

func (j *JSONEmitter) emit(event map[string]any) {
	event["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(event)
	if err != nil {
		fmt.Fprintf(j.errW, "json marshal error: %v\n", err)
		return
	}
	fmt.Fprintf(j.w, "%s\n", data)
}

// PlaybookStart emits a playbook_start event.
func (j *JSONEmitter) PlaybookStart(path string) {
	j.emit(map[string]any{
		"type":     "playbook_start",
		"playbook": path,
		"version":  jsonSchemaVersion,
	})
}

// PlaybookEnd emits a playbook_recap event.
func (j *JSONEmitter) PlaybookEnd(stats Stats) {
	j.emit(map[string]any{
		"type":     "playbook_recap",
		"ok":       stats.GetOK(),
		"changed":  stats.GetChanged(),
		"failed":   stats.GetFailed(),
		"skipped":  stats.GetSkipped(),
		"duration": stats.GetDuration().Seconds(),
		"success":  stats.GetFailed() == 0,
	})
}

// PlayStart emits a play_start event.
func (j *JSONEmitter) PlayStart(play *playbook.Play) {
	name := play.Name
	if name == "" {
		name = strings.Join(play.Hosts, ", ")
	}
	j.emit(map[string]any{
		"type":  "play_start",
		"play":  name,
		"hosts": play.Hosts,
	})
}

// HostStart emits a host_start event and tags subsequent task events with
// the host name until the next HostStart.
func (j *JSONEmitter) HostStart(host, connType string) {
	j.currentHost = host
	j.emit(map[string]any{
		"type":       "host_start",
		"host":       host,
		"connection": connType,
	})
}

// HostFactsStart is a no-op in JSON mode — the host_facts event emitted by
// HostFactsResult already records the outcome; there's no spinner to animate.
func (j *JSONEmitter) HostFactsStart(_ string) {}

// HostFactsResult emits a host_facts event recording the outcome of
// fact-gathering for the current host. JSON consumers used to derive this
// from the legacy "Gathering Facts" task_start/task_result pair; the new
// event is more direct.
func (j *JSONEmitter) HostFactsResult(host string, ok bool, errMsg string) {
	event := map[string]any{
		"type": "host_facts",
		"host": host,
		"ok":   ok,
	}
	if !ok && errMsg != "" {
		event["error"] = errMsg
	}
	j.emit(event)
}

// HostStartDone is a no-op in JSON mode — host_start was already emitted
// as a discrete event.
func (j *JSONEmitter) HostStartDone(_ string) {}

// PlayHosts is a no-op in JSON mode — play_start already carries the
// full hosts slice for downstream consumers.
func (j *JSONEmitter) PlayHosts(_ []string) {}

// TaskStart emits a task_start event tagged with the current host.
func (j *JSONEmitter) TaskStart(name, moduleName string) {
	event := map[string]any{
		"type":   "task_start",
		"task":   name,
		"module": moduleName,
	}
	if j.currentHost != "" {
		event["host"] = j.currentHost
	}
	j.emit(event)
}

// TaskResult emits a task_result event tagged with the current host.
func (j *JSONEmitter) TaskResult(name, status string, changed bool, message string, tags []string) {
	event := map[string]any{
		"type":    "task_result",
		"task":    name,
		"status":  status,
		"changed": changed,
		"message": message,
		"tags":    tags,
	}
	if j.currentHost != "" {
		event["host"] = j.currentHost
	}
	j.emit(event)
}

// DisplayPlan emits plan_task events for each planned task.
func (j *JSONEmitter) DisplayPlan(tasks []PlannedTask, dryRun bool) {
	for _, t := range tasks {
		j.emit(j.planTaskEvent(t))
	}
}

// DisplayMultiHostPlan emits plan_task events for each planned task in a
// multi-host plan. Hosts are not pre-filtered for no-op suppression — the
// JSON consumer can decide what to render. Each event carries `host`.
func (j *JSONEmitter) DisplayMultiHostPlan(tasks []PlannedTask, hosts []string, dryRun bool) {
	for _, t := range tasks {
		j.emit(j.planTaskEvent(t))
	}
}

// planTaskEvent constructs a plan_task event payload. Includes `host` when
// the PlannedTask carries one (multi-host plans).
func (j *JSONEmitter) planTaskEvent(t PlannedTask) map[string]any {
	event := map[string]any{
		"type":   "plan_task",
		"task":   t.Name,
		"module": t.Module,
		"action": t.Status,
	}
	if t.Host != "" {
		event["host"] = t.Host
	}
	if t.NoLog {
		event["no_log"] = true
	}
	if params := sanitizeParams(t.Params, t.NoLog); len(params) > 0 {
		event["params"] = params
	}
	if t.Reason != "" {
		event["reason"] = t.Reason
	}
	if len(t.Tags) > 0 {
		event["tags"] = t.Tags
	}
	if t.OldChecksum != "" {
		event["old_checksum"] = t.OldChecksum
	}
	if t.NewChecksum != "" {
		event["new_checksum"] = t.NewChecksum
	}
	// Content diffs are omitted for no_log tasks so secrets never reach output.
	if j.diff && !t.NoLog {
		if t.OldContent != "" {
			event["old_content"] = t.OldContent
		}
		if t.NewContent != "" {
			event["new_content"] = t.NewContent
		}
	}
	return event
}

// PromptApproval always returns true in JSON mode (auto-approve).
// The target argument is accepted for interface compatibility and ignored
// — the JSON emitter never prints a prompt.
func (j *JSONEmitter) PromptApproval(_ string) bool {
	return true
}

// Section is a no-op in JSON mode.
func (j *JSONEmitter) Section(_ string) {}

// Info emits an info event.
func (j *JSONEmitter) Info(format string, args ...any) {
	j.emit(map[string]any{
		"type":    "info",
		"message": fmt.Sprintf(format, args...),
	})
}

// Warn emits a warning event.
func (j *JSONEmitter) Warn(format string, args ...any) {
	j.emit(map[string]any{
		"type":    "warning",
		"message": fmt.Sprintf(format, args...),
	})
}

// Error emits an error event to stdout and writes the message to stderr.
func (j *JSONEmitter) Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	j.emit(map[string]any{
		"type":    "error",
		"message": msg,
	})
	fmt.Fprintf(j.errW, "error: %s\n", msg)
}

// Debug emits a debug event (only when debug mode is enabled).
func (j *JSONEmitter) Debug(format string, args ...any) {
	if j.debug {
		j.emit(map[string]any{
			"type":    "debug",
			"message": fmt.Sprintf(format, args...),
		})
	}
}

// SetColor is a no-op for JSON output.
func (j *JSONEmitter) SetColor(_ bool) {}

// SetDebug enables or disables debug events.
func (j *JSONEmitter) SetDebug(enabled bool) {
	j.debug = enabled
}

// SetVerbose is a no-op for JSON output.
func (j *JSONEmitter) SetVerbose(_ bool) {}

// SetDiff enables diff data in JSON plan events.
func (j *JSONEmitter) SetDiff(enabled bool) {
	j.diff = enabled
}

// SetDryRun is a no-op for JSON output: machine consumers read the plan_task
// events and the recap stats directly.
func (j *JSONEmitter) SetDryRun(bool) {}

// DiffEnabled returns whether diff mode is active.
func (j *JSONEmitter) DiffEnabled() bool {
	return j.diff
}

// NewEmitter creates the appropriate Emitter based on the output mode.
func NewEmitter(mode string) (Emitter, error) {
	switch mode {
	case "text", "":
		return New(os.Stdout), nil
	case "json":
		return NewJSONEmitter(os.Stdout, os.Stderr), nil
	default:
		return nil, fmt.Errorf("invalid output mode %q (valid: text, json)", mode)
	}
}
