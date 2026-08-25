package output

import (
	"github.com/tackhq/tack/internal/playbook"
)

// Emitter is the interface for output backends (text, JSON, etc.).
type Emitter interface {
	PlaybookStart(path string)
	PlaybookEnd(stats Stats)
	PlayStart(play *playbook.Play)
	// PlayHosts emits a one-line summary of all targeted hosts. It is called
	// once on the main thread for plays targeting two or more hosts; emitters
	// may render it however suits their output mode (text mode prints
	// "HOSTS h1, h2, h3" with a "(and N more)" overflow for >5 hosts).
	PlayHosts(hosts []string)
	// HostStart begins a per-host banner. Text-mode emitters write the line
	// without a trailing newline; the line is closed by the next of:
	// HostFactsResult (when facts are gathered) or HostStartDone (when
	// gather_facts is false). Other emitters may treat HostStart as a single
	// terminating line.
	HostStart(host, connType string)
	// HostFactsStart signals that fact-gathering has begun on the open
	// HostStart banner. Interactive text emitters complete the line and start
	// a live spinner; other emitters may treat it as a no-op.
	HostFactsStart(host string)
	// HostFactsResult appends fact-gathering status to the open HostStart
	// banner and terminates the line. On failure, errMsg is rendered on a
	// follow-up line via the emitter's standard error path.
	HostFactsResult(host string, ok bool, errMsg string)
	// HostStartDone closes the open HostStart banner with a newline when
	// fact-gathering is skipped (gather_facts: false). Emitters that did not
	// hold the HostStart line open may treat this as a no-op.
	HostStartDone(host string)
	TaskStart(name, moduleName string)
	// TaskResult reports a task's outcome. tags is the task's effective tag set
	// (own + inherited play/role/block tags); emitters render it inline so users
	// can see what -t/--skip-tags would match. Empty when the task has no tags.
	TaskResult(name, status string, changed bool, message string, tags []string)
	// DisplayPlan renders a single-host plan. Used for plays targeting one
	// host or local connection. Output for the single-host serial flow is
	// byte-identical to historical behavior.
	DisplayPlan(tasks []PlannedTask, dryRun bool)
	// DisplayMultiHostPlan renders a consolidated plan for multi-host plays
	// with per-line host attribution. The hosts slice defines render order;
	// tasks are grouped by their PlannedTask.Host. Hosts whose plan contains
	// no will_run/will_change/always_runs/conditional entries are omitted
	// from the body and only counted in the footer's "unchanged" total.
	DisplayMultiHostPlan(tasks []PlannedTask, hosts []string, dryRun bool)
	// PromptApproval asks the user to confirm applying changes. The target
	// argument is a human-readable description of which host(s) the changes
	// will hit (e.g. "web1.prod (ssh)" or "4 hosts (web1, web2, web3, web4)")
	// and is included in the prompt line so users can identify the target
	// without scrolling above the prompt.
	PromptApproval(target string) bool
	Section(name string)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
	Debug(format string, args ...any)
	SetColor(enabled bool)
	SetDebug(enabled bool)
	SetVerbose(enabled bool)
	SetDiff(enabled bool)
	DiffEnabled() bool
}

// Verify Output implements Emitter at compile time.
var _ Emitter = (*Output)(nil)
