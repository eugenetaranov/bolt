// Package output provides formatted output for playbook execution.
package output

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tackhq/tack/internal/playbook"
	"github.com/pmezard/go-difflib/difflib"
	"golang.org/x/term"
)

// Colors for terminal output.
const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorCyan    = "\033[36m"
	colorGray    = "\033[90m"
	colorBold    = "\033[1m"
)

// Stats holds execution statistics for output.
type Stats interface {
	GetOK() int
	GetChanged() int
	GetFailed() int
	GetSkipped() int
	GetDuration() time.Duration
}

// Output handles formatted output.
type Output struct {
	w           io.Writer
	useColor    bool
	debug       bool
	verbose     bool
	diff        bool
	interactive bool
	spin        *spinner
	hostLabel   string // "host [conn]" of the open HostStart banner
	spinStyle   string // "" / "dots" (default braille) or "shimmer" (color sweep)
}

// New creates a new output handler. When w is a terminal, task execution is
// rendered with a live spinner; otherwise output is plain and line-buffered.
func New(w io.Writer) *Output {
	o := &Output{
		w:        w,
		useColor: true,
	}
	if f, ok := w.(*os.File); ok {
		o.interactive = term.IsTerminal(int(f.Fd()))
	}
	o.spinStyle = os.Getenv("TACK_SPINNER") // "shimmer" for the color-sweep style
	return o
}

// SetSpinnerStyle overrides the spinner style ("dots" or "shimmer").
func (o *Output) SetSpinnerStyle(style string) {
	o.spinStyle = style
}

// spinnerOn reports whether live spinner animation should be used. Requires an
// interactive terminal and color output (--no-color implies plain output).
func (o *Output) spinnerOn() bool {
	return o.interactive && o.useColor
}

// SetColor enables or disables color output.
func (o *Output) SetColor(enabled bool) {
	o.useColor = enabled
}

// ColorEnabled returns whether color output is enabled.
func (o *Output) ColorEnabled() bool {
	return o.useColor
}

// SetDebug enables or disables debug output.
func (o *Output) SetDebug(enabled bool) {
	o.debug = enabled
}

// SetVerbose enables or disables verbose output (full diffs in plan).
func (o *Output) SetVerbose(enabled bool) {
	o.verbose = enabled
}

// SetDiff enables or disables diff display in plan output.
func (o *Output) SetDiff(enabled bool) {
	o.diff = enabled
}

// DiffEnabled returns true if diff or verbose mode is active.
func (o *Output) DiffEnabled() bool {
	return o.diff || o.verbose
}

// color returns the string wrapped in color codes if enabled.
func (o *Output) color(c, s string) string {
	if !o.useColor {
		return s
	}
	return c + s + colorReset
}

// formatTags renders a task's effective tags as a "[a,b]" suffix, or "" when
// there are none.
func formatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, ",") + "]"
}

// tagSuffix returns a leading-space, dimmed tag suffix for inline display, or
// "" when the task has no tags.
func (o *Output) tagSuffix(tags []string) string {
	s := formatTags(tags)
	if s == "" {
		return ""
	}
	return " " + o.color(colorGray, s)
}

// PlaybookStart prints the playbook start banner.
func (o *Output) PlaybookStart(path string) {
	o.printf("\n%s %s\n", o.color(colorBold, "PLAYBOOK"), path)
	if o.debug {
		o.printf("%s\n", strings.Repeat("-", 60))
	}
}

// PlaybookEnd prints the playbook summary.
func (o *Output) PlaybookEnd(stats Stats) {
	o.printf("\n%s ", o.color(colorBold, "RECAP"))

	ok := o.color(colorGreen, fmt.Sprintf("ok=%d", stats.GetOK()))
	changed := o.color(colorYellow, fmt.Sprintf("changed=%d", stats.GetChanged()))
	failed := o.color(colorRed, fmt.Sprintf("failed=%d", stats.GetFailed()))
	skipped := o.color(colorCyan, fmt.Sprintf("skipped=%d", stats.GetSkipped()))

	o.printf("%s %s %s %s", ok, changed, failed, skipped)
	o.printf(" %s\n", o.color(colorGray, fmt.Sprintf("(%.2fs)", stats.GetDuration().Seconds())))
}

// HostStart writes the per-host banner without a trailing newline; the
// line is closed by either HostFactsResult (when facts are gathered) or
// HostStartDone (when gather_facts: false).
func (o *Output) HostStart(host, connType string) {
	o.hostLabel = host + " [" + connType + "]"
	o.printf("\n%s %s", o.color(colorBold, "HOST"), o.hostLabel)
}

// factsBanner returns the full "HOST <label> - gathering facts" line prefix
// used by the spinner and the final fact-result line.
func (o *Output) factsBanner() string {
	return fmt.Sprintf("%s %s - gathering facts", o.color(colorBold, "HOST"), o.hostLabel)
}

// hostBanner returns the plain "HOST <label>" line (no phase suffix).
func (o *Output) hostBanner() string {
	return fmt.Sprintf("%s %s", o.color(colorBold, "HOST"), o.hostLabel)
}

// HostConnectStart shows a live "connecting" spinner on the open HostStart
// banner while the connection is established — useful when a host is slow to
// answer. No-op outside interactive mode.
func (o *Output) HostConnectStart(_ string) {
	if o.spinnerOn() {
		o.startLineSpinner(o.hostBanner() + " - connecting")
	}
}

// HostConnectDone resolves the connecting phase. On success it clears the
// spinner and restores the plain HOST banner so the fact-gathering phase can
// continue on the same line; on failure it marks the banner with ✗ and prints
// the error on a follow-up line.
func (o *Output) HostConnectDone(_ string, ok bool, errMsg string) {
	if o.spin != nil {
		o.stopSpinner()
	}
	if ok {
		if o.spinnerOn() {
			fmt.Fprintf(o.w, "\r%s\033[K", o.hostBanner())
		}
		return
	}
	mark := o.color(colorRed, "✗")
	if o.spinnerOn() {
		fmt.Fprintf(o.w, "\r%s - connecting %s\033[K\n", o.hostBanner(), mark)
	} else {
		o.printf(" - connecting %s\n", mark)
	}
	if errMsg != "" {
		o.Error("%s", errMsg)
	}
}

// HostFactsStart signals that fact-gathering has begun on the open HostStart
// banner. In interactive mode it completes the line to
// "HOST <label> - gathering facts" and animates a spinner until
// HostFactsResult; otherwise it is a no-op (HostFactsResult prints the whole
// suffix in one shot, preserving plain/buffered output).
func (o *Output) HostFactsStart(_ string) {
	if o.spinnerOn() {
		o.startLineSpinner(o.factsBanner())
	}
}

// HostFactsResult closes the fact-gathering banner with a ✓/✗. When a spinner
// is running it stops it and reprints the whole line in place; otherwise it
// appends the suffix to the open HostStart line. On failure the error message
// is printed on a follow-up line via Error.
func (o *Output) HostFactsResult(_ string, ok bool, errMsg string) {
	mark := o.color(colorGreen, "✓")
	if !ok {
		mark = o.color(colorRed, "✗")
	}
	if o.spin != nil {
		o.stopSpinner()
		o.printf("\r%s %s\033[K\n", o.factsBanner(), mark)
	} else {
		o.printf(" - gathering facts %s\n", mark)
	}
	if !ok && errMsg != "" {
		o.Error("%s", errMsg)
	}
}

// HostStartDone closes the open HostStart banner with a newline. Used when
// gather_facts: false skips the fact step so the banner is still terminated.
func (o *Output) HostStartDone(_ string) {
	fmt.Fprintln(o.w)
}

// PlayStart prints the play start banner. Anonymous plays (no name field)
// emit nothing; the host identity is conveyed by the HOST line (single-host)
// or the PlayHosts summary line (multi-host) that follows.
func (o *Output) PlayStart(play *playbook.Play) {
	if play.Name == "" {
		return
	}
	o.printf("\n%s %s\n", o.color(colorBold, "PLAY"), play.Name)
}

// playHostsVisibleCap caps how many host names are listed inline on the
// HOSTS summary banner; the rest are summarized as "(and N more)".
const playHostsVisibleCap = 5

// PlayHosts emits the multi-host summary banner. Callers should invoke
// this only for plays with two or more hosts.
func (o *Output) PlayHosts(hosts []string) {
	if len(hosts) < 2 {
		return
	}
	visible := hosts
	overflow := 0
	if len(hosts) > playHostsVisibleCap {
		visible = hosts[:playHostsVisibleCap]
		overflow = len(hosts) - playHostsVisibleCap
	}
	list := strings.Join(visible, ", ")
	if overflow > 0 {
		list = fmt.Sprintf("%s (and %d more)", list, overflow)
	}
	o.printf("%s %s\n", o.color(colorBold, "HOSTS"), list)
}

// TaskStart is called when a task begins. In interactive mode it starts a live
// spinner; otherwise it is a no-op (the result line is printed in TaskResult).
func (o *Output) TaskStart(name, moduleName string) {
	if o.spinnerOn() {
		o.startSpinner(name)
	}
}

// statusDisplay holds the visual representation of a task status.
type statusDisplay struct {
	indicator string
	color     string
	text      string
}

// resolveStatus maps a status string to its display properties.
func resolveStatus(status string) statusDisplay {
	switch {
	case strings.HasPrefix(status, "ok"):
		return statusDisplay{"✓", colorGreen, "ok"}
	case strings.HasPrefix(status, "changed"):
		return statusDisplay{"✓", colorYellow, "changed"}
	case strings.HasPrefix(status, "skipped"):
		return statusDisplay{"○", colorCyan, "skipped"}
	case strings.HasPrefix(status, "failed"):
		return statusDisplay{"✗", colorRed, "FAILED"}
	default:
		return statusDisplay{"?", colorGray, status}
	}
}

// TaskResult prints the task result in a single line.
// Format: [status] module | host | task name
func (o *Output) TaskResult(name, status string, changed bool, message string, tags []string) {
	// Skipped tasks are hidden during apply unless verbose (consistent with the
	// plan). The RECAP still counts them.
	if strings.HasPrefix(status, "skipped") && !o.verbose && !o.debug {
		if o.spin != nil {
			o.stopSpinner()
			fmt.Fprint(o.w, "\r\033[K")
		}
		return
	}

	sd := resolveStatus(status)
	suffix := o.tagSuffix(tags)

	if o.spinnerOn() {
		// Stop the spinner and overwrite its line in place with the final glyph.
		o.stopSpinner()
		fmt.Fprintf(o.w, "\r  %s %s%s\033[K\n", o.color(sd.color, sd.indicator), name, suffix)
	} else {
		// Print compact single line
		o.printf("  %s %s%s\n", o.color(sd.color, sd.indicator), name, suffix)
	}

	// Print the result detail. Changed and failed tasks always show what
	// happened; routine ok/skipped details are shown only with -v/--debug to
	// keep steady-state runs terse.
	if message != "" && o.showTaskDetail(status) {
		o.printf("    %s %s\n", o.color(colorGray, "→"), message)
	}
}

// showTaskDetail reports whether a task's result message should be printed
// under its status line.
func (o *Output) showTaskDetail(status string) bool {
	if o.debug || o.verbose {
		return true
	}
	return strings.HasPrefix(status, "changed") || strings.HasPrefix(status, "failed")
}

// Section prints a section header.
func (o *Output) Section(name string) {
	o.printf("\n%s\n", o.color(colorBold, name))
}

// Info prints an informational message.
func (o *Output) Info(format string, args ...any) {
	o.printf("%s %s\n", o.color(colorBlue, "INFO"), fmt.Sprintf(format, args...))
}

// Warn prints a warning message.
func (o *Output) Warn(format string, args ...any) {
	o.printf("%s %s\n", o.color(colorYellow, "WARN"), fmt.Sprintf(format, args...))
}

// Error prints an error message.
func (o *Output) Error(format string, args ...any) {
	o.printf("%s %s\n", o.color(colorRed, "ERROR"), fmt.Sprintf(format, args...))
}

// Debug prints a debug message (only in debug mode).
func (o *Output) Debug(format string, args ...any) {
	if o.debug {
		o.printf("%s %s\n", o.color(colorGray, "DEBUG"), fmt.Sprintf(format, args...))
	}
}

func (o *Output) printf(format string, args ...any) {
	// Clear any in-progress spinner so stray output never collides with a frame.
	if o.spin != nil {
		o.stopSpinner()
	}
	fmt.Fprintf(o.w, format, args...)
}

// PlannedTask describes a task as evaluated during the plan phase.
type PlannedTask struct {
	// Host identifies the target host this plan entry was computed for.
	// Empty in single-host plays where attribution is implicit. The
	// multi-host renderer uses Host as a per-line prefix; the single-host
	// renderer ignores it.
	Host string

	Name      string
	Module    string
	Status    string // "will_run", "will_skip", "conditional", "will_change", "no_change", "always_runs"
	Reason    string // skip reason or condition text
	LoopCount int    // >0 if looped
	Params    map[string]any

	// Content comparison fields (populated from CheckResult).
	OldChecksum string
	NewChecksum string
	OldContent  string
	NewContent  string

	// Indent is the nesting depth for block tasks (0 = top-level).
	Indent int

	// IsSection marks this entry as a section header (e.g. "BLOCK:", "RESCUE:", "ALWAYS:").
	IsSection bool

	// Tags is the task's effective tag set (own + inherited play/role/block),
	// rendered as a dimmed [a,b] suffix so users can see what -t would match.
	Tags []string

	// NoLog suppresses this task's params and diff content from rendered output
	// (the task set no_log: true because it handles secrets).
	NoLog bool
}

// DisplayPlan renders the plan table showing what tasks will run.
func (o *Output) DisplayPlan(tasks []PlannedTask, dryRun bool) {
	o.PlanStart(dryRun)
	for _, t := range tasks {
		o.PlanLine(t)
	}
	o.PlanEnd(tasks, dryRun)
}

// isFilteredPlanSkip reports whether a task was excluded by a --tags/--skip-tags
// selection. These are omitted from the human-readable plan and reported only as
// a count in the summary, since the user explicitly asked to run a tag subset.
// (Role filtering is left visible — see TestRolesFilterReportsSkippedInPlan.)
func isFilteredPlanSkip(t PlannedTask) bool {
	return t.Status == "will_skip" && t.Reason == "skipped (tag)"
}

// PlanStart prints the plan header. Part of the streaming plan API
// (PlanStart → PlanLine* → PlanEnd) that lets the executor emit each planned
// task as its check completes; DisplayPlan wraps these for batch callers.
func (o *Output) PlanStart(dryRun bool) {
	label := "PLAN"
	if dryRun {
		label = "PLAN (dry run)"
	}
	o.printf("\n%s\n", o.color(colorBold, label))
}

// PlanCheck shows a live "checking <name>" spinner while a task's plan check
// runs (interactive only). PlanLine clears it when the resolved line is ready.
func (o *Output) PlanCheck(name string) {
	if o.spinnerOn() {
		o.startSpinner(name)
	}
}

// PlanLine renders one planned task. Tasks filtered out by the user's
// --tags/--roles selection are omitted (counted in the summary instead). A
// running PlanCheck spinner is stopped and its line cleared first.
func (o *Output) PlanLine(t PlannedTask) {
	if o.spin != nil {
		o.stopSpinner()
		fmt.Fprint(o.w, "\r\033[K") // clear the "checking …" spinner line
	}
	if o.hidePlanSkip(t) {
		return
	}
	o.renderPlanTask(t)
}

// hidePlanSkip reports whether a planned task should be omitted from the plan
// body. Skipped tasks (tag/role filtered or when-condition) are hidden by
// default to keep the plan focused on what will actually run; they are only
// counted in the summary. Use -v/--verbose (or --debug) to see them.
func (o *Output) hidePlanSkip(t PlannedTask) bool {
	return t.Status == "will_skip" && !o.verbose && !o.debug
}

// renderPlanTask prints a single planned task line plus its params and diff.
func (o *Output) renderPlanTask(t PlannedTask) {
	var indicator, col string
	switch t.Status {
	case "will_change":
		indicator, col = "+", colorYellow
	case "no_change":
		indicator, col = "=", colorGreen
	case "always_runs":
		indicator, col = "~", colorYellow
	case "will_run":
		indicator, col = "+", colorYellow
	case "will_skip":
		indicator, col = "○", colorCyan
	case "conditional":
		indicator, col = "?", colorYellow
	}

	module := ""
	if t.Module != "" {
		module = fmt.Sprintf("%s: ", t.Module)
	}

	suffix := ""
	if t.Status == "will_skip" && t.Reason != "" {
		suffix = t.Reason
	} else if t.Status == "conditional" && t.Reason != "" {
		suffix = fmt.Sprintf("when: %s", t.Reason)
	} else if t.Status == "no_change" && t.Reason != "" {
		suffix = t.Reason
	} else if t.Status == "always_runs" && t.Reason != "" {
		suffix = t.Reason
	} else if t.Status == "will_change" && t.Reason != "" {
		suffix = t.Reason
	}
	if t.LoopCount > 0 {
		if suffix != "" {
			suffix += " "
		}
		suffix += fmt.Sprintf("%d items", t.LoopCount)
	}

	// Section headers (block/rescue/always) get special rendering.
	if t.IsSection {
		indent := strings.Repeat("  ", t.Indent)
		o.printf("%s%s\n", indent, o.color(colorBold, t.Name))
		return
	}

	indent := strings.Repeat("  ", t.Indent)
	line := fmt.Sprintf("%s  %s %s%s", indent, indicator, module, t.Name)
	if suffix != "" {
		line += " - " + suffix
	}
	o.printf("%s%s\n", o.color(col, strings.TrimRight(line, " ")), o.tagSuffix(t.Tags))

	// Show task parameters.
	paramIndent := strings.Repeat("  ", t.Indent) + "      "
	for _, paramLine := range formatTaskParams(t.Module, t.Params, t.NoLog) {
		o.printf("%s%s\n", paramIndent, o.color(colorGray, paramLine))
	}

	// Show checksums or diff when content differs. Diffs are suppressed for
	// no_log tasks so rendered content can't leak secrets.
	showDiff := (o.verbose || o.diff) && !t.NoLog
	destPath := extractDestPath(t.Module, t.Params)

	if t.OldChecksum != "" && t.NewChecksum != "" && t.OldChecksum != t.NewChecksum {
		if showDiff && t.OldContent != "" && t.NewContent != "" {
			o.printDiff(destPath, destPath, t.OldContent, t.NewContent)
		} else {
			o.printf("      %s\n", o.color(colorRed, "old: "+t.OldChecksum))
			o.printf("      %s\n", o.color(colorGreen, "new: "+t.NewChecksum))
		}
	} else if t.OldChecksum == "" && t.NewChecksum != "" {
		if showDiff && t.NewContent != "" {
			o.printDiff("/dev/null", destPath, "", t.NewContent)
		} else {
			o.printf("      %s\n", o.color(colorYellow, "new: "+t.NewChecksum))
		}
	}
}

// PlanEnd prints the summary footer. Filtered (tag/role) tasks are reported as
// "N filtered" rather than counted toward "to skip".
func (o *Output) PlanEnd(tasks []PlannedTask, _ bool) {
	var willRun, willSkip, conditional, willChange, noChange, alwaysRuns, filtered int
	for _, t := range tasks {
		if isFilteredPlanSkip(t) {
			filtered++
			continue
		}
		switch t.Status {
		case "will_change":
			willChange++
		case "no_change":
			noChange++
		case "always_runs":
			alwaysRuns++
		case "will_run":
			willRun++
		case "will_skip":
			willSkip++
		case "conditional":
			conditional++
		}
	}

	var summaryParts []string
	if willChange > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to change", willChange))
	}
	if noChange > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d ok", noChange))
	}
	toRun := alwaysRuns + willRun
	if toRun > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to run", toRun))
	}
	if conditional > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d conditional", conditional))
	}
	if willSkip > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to skip", willSkip))
	}
	if filtered > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d filtered", filtered))
	}
	if len(summaryParts) == 0 {
		summaryParts = append(summaryParts, "nothing to do")
	}

	tail := ""
	if (willSkip+filtered) > 0 && !o.verbose && !o.debug {
		tail = " " + o.color(colorGray, "(use -v to show skipped)")
	}
	o.printf("\n%s %s%s\n",
		o.color(colorBold, "Plan:"),
		strings.Join(summaryParts, ", ")+".", tail)
}

// hostColumnMax caps the host-prefix column width so pathological hostnames
// (e.g. fully-qualified DNS names) don't blow past terminal width. Hostnames
// longer than this are truncated with a single-character ellipsis.
const hostColumnMax = 30

// formatHostPrefix returns the host name padded to colWidth. If the host is
// longer than colWidth it is truncated to colWidth-1 chars + "…".
func formatHostPrefix(host string, colWidth int) string {
	if len(host) > colWidth {
		if colWidth <= 1 {
			return "…"
		}
		return host[:colWidth-1] + "…"
	}
	if len(host) < colWidth {
		return host + strings.Repeat(" ", colWidth-len(host))
	}
	return host
}

// hostColumnWidth returns the column width for the host prefix. It pads to
// the longest hostname among hosts that contribute lines to the rendered
// plan, capped at hostColumnMax.
func hostColumnWidth(hosts []string) int {
	maxLen := 0
	for _, h := range hosts {
		n := len(h)
		if n > maxLen {
			maxLen = n
		}
	}
	if maxLen > hostColumnMax {
		maxLen = hostColumnMax
	}
	return maxLen
}

// hostHasChanges returns true if at least one PlannedTask for the given host
// is non-no-op (anything other than no_change / will_skip and excluding
// section headers).
func hostHasChanges(tasks []PlannedTask, host string) bool {
	for _, t := range tasks {
		if t.Host != host || t.IsSection {
			continue
		}
		switch t.Status {
		case "will_change", "will_run", "always_runs", "conditional":
			return true
		}
	}
	return false
}

// DisplayMultiHostPlan renders a consolidated plan for plays targeting more
// than one host. Each task line is prefixed with the host name, padded for
// column alignment. Hosts with zero non-no-op tasks contribute no body lines
// and are only counted in the "(M unchanged)" footer suffix.
func (o *Output) DisplayMultiHostPlan(tasks []PlannedTask, hosts []string, dryRun bool) {
	label := "PLAN"
	if dryRun {
		label = "PLAN (dry run)"
	}
	o.printf("\n%s\n", o.color(colorBold, label))

	// Determine which hosts have body content. Hosts with no changes are
	// suppressed from the per-line listing and only counted in the footer.
	changing := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if hostHasChanges(tasks, h) {
			changing[h] = true
		}
	}

	// Compute column width from the changing hosts only. If no host has
	// changes, column width is 0 (footer-only output).
	var changingHosts []string
	for _, h := range hosts {
		if changing[h] {
			changingHosts = append(changingHosts, h)
		}
	}
	colWidth := hostColumnWidth(changingHosts)

	// Group tasks by host. Walk hosts in caller-specified order so output is
	// deterministic.
	byHost := make(map[string][]PlannedTask, len(hosts))
	for _, t := range tasks {
		byHost[t.Host] = append(byHost[t.Host], t)
	}

	// Counters span all hosts (including no-op hosts whose tasks didn't
	// render). The footer summarizes the play, not just the visible body.
	var willRun, willSkip, conditional, willChange, noChange, alwaysRuns, filtered int
	for _, t := range tasks {
		if t.IsSection {
			continue
		}
		if isFilteredPlanSkip(t) {
			filtered++
			continue
		}
		switch t.Status {
		case "will_change":
			willChange++
		case "no_change":
			noChange++
		case "always_runs":
			alwaysRuns++
		case "will_run":
			willRun++
		case "will_skip":
			willSkip++
		case "conditional":
			conditional++
		}
	}

	// Render per-host bodies (skip hosts with no changes). Tasks filtered out
	// by --tags/--skip-tags are omitted (counted in the footer).
	for _, host := range hosts {
		if !changing[host] {
			continue
		}
		prefix := formatHostPrefix(host, colWidth) + ": "
		for _, t := range byHost[host] {
			if o.hidePlanSkip(t) {
				continue
			}
			o.renderMultiHostPlanLine(t, prefix, colWidth)
		}
	}

	// Footer
	var summaryParts []string
	if willChange > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to change", willChange))
	}
	if noChange > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d ok", noChange))
	}
	toRun := alwaysRuns + willRun
	if toRun > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to run", toRun))
	}
	if conditional > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d conditional", conditional))
	}
	if willSkip > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d to skip", willSkip))
	}
	if filtered > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d filtered", filtered))
	}
	if len(summaryParts) == 0 {
		summaryParts = append(summaryParts, "nothing to do")
	}

	unchanged := len(hosts) - len(changingHosts)
	footer := fmt.Sprintf("%s across %d hosts", strings.Join(summaryParts, ", "), len(hosts))
	if unchanged > 0 {
		footer += fmt.Sprintf(" (%d unchanged)", unchanged)
	}
	o.printf("\n%s %s.\n", o.color(colorBold, "Plan:"), footer)
}

// renderMultiHostPlanLine renders a single PlannedTask as a host-prefixed
// line plus its parameters and checksum/diff continuation. The prefix is the
// pre-padded "<host>: " string. colWidth is used to pad continuation lines.
func (o *Output) renderMultiHostPlanLine(t PlannedTask, prefix string, colWidth int) {
	// Continuation lines (params, diffs, checksums) align under the task
	// indicator: one column for the host prefix, then the standard 6-space
	// param indent used by DisplayPlan.
	contIndent := strings.Repeat(" ", colWidth+2) + strings.Repeat("  ", t.Indent) + "      "

	indicator, col := planIndicatorAndColor(t.Status)

	if t.IsSection {
		indent := strings.Repeat("  ", t.Indent)
		o.printf("%s%s%s\n", prefix, indent, o.color(colorBold, t.Name))
		return
	}

	module := ""
	if t.Module != "" {
		module = fmt.Sprintf("%s: ", t.Module)
	}

	suffix := planLineSuffix(t)

	indent := strings.Repeat("  ", t.Indent)
	line := fmt.Sprintf("%s%s  %s %s%s", prefix, indent, indicator, module, t.Name)
	if suffix != "" {
		line += " - " + suffix
	}
	o.printf("%s%s\n", o.color(col, strings.TrimRight(line, " ")), o.tagSuffix(t.Tags))

	// Show task parameters
	for _, paramLine := range formatTaskParams(t.Module, t.Params, t.NoLog) {
		o.printf("%s%s\n", contIndent, o.color(colorGray, paramLine))
	}

	// Show checksums or diff when content differs (suppressed for no_log).
	showDiff := (o.verbose || o.diff) && !t.NoLog
	destPath := extractDestPath(t.Module, t.Params)

	if t.OldChecksum != "" && t.NewChecksum != "" && t.OldChecksum != t.NewChecksum {
		if showDiff && t.OldContent != "" && t.NewContent != "" {
			o.printDiffWithIndent(contIndent, destPath, destPath, t.OldContent, t.NewContent)
		} else {
			o.printf("%s%s\n", contIndent, o.color(colorRed, "old: "+t.OldChecksum))
			o.printf("%s%s\n", contIndent, o.color(colorGreen, "new: "+t.NewChecksum))
		}
	} else if t.OldChecksum == "" && t.NewChecksum != "" {
		if showDiff && t.NewContent != "" {
			o.printDiffWithIndent(contIndent, "/dev/null", destPath, "", t.NewContent)
		} else {
			o.printf("%s%s\n", contIndent, o.color(colorYellow, "new: "+t.NewChecksum))
		}
	}
}

// planIndicatorAndColor returns the symbol + color for a planned task status.
// Mirrors the switch inside DisplayPlan; extracted so the multi-host renderer
// stays in sync. (Counter increments stay inline at the call site since
// counting is interleaved with rendering in DisplayPlan.)
func planIndicatorAndColor(status string) (string, string) {
	switch status {
	case "will_change":
		return "+", colorYellow
	case "no_change":
		return "=", colorGreen
	case "always_runs":
		return "~", colorYellow
	case "will_run":
		return "+", colorYellow
	case "will_skip":
		return "○", colorCyan
	case "conditional":
		return "?", colorYellow
	default:
		return "?", colorGray
	}
}

// planLineSuffix derives the "- reason" suffix shown after a planned task name.
func planLineSuffix(t PlannedTask) string {
	suffix := ""
	switch t.Status {
	case "will_skip":
		if t.Reason != "" {
			suffix = t.Reason
		}
	case "conditional":
		if t.Reason != "" {
			suffix = fmt.Sprintf("when: %s", t.Reason)
		}
	case "no_change", "always_runs", "will_change":
		if t.Reason != "" {
			suffix = t.Reason
		}
	}
	if t.LoopCount > 0 {
		if suffix != "" {
			suffix += " "
		}
		suffix += fmt.Sprintf("%d items", t.LoopCount)
	}
	return suffix
}

// printDiffWithIndent is printDiff with a custom continuation-line indent.
// Used by the multi-host renderer where indent depends on the host column
// width. Single-host DisplayPlan keeps using printDiff with its hard-coded
// 6-space indent.
func (o *Output) printDiffWithIndent(indent, oldPath, newPath, oldContent, newContent string) {
	if isBinary(oldContent) || isBinary(newContent) {
		o.printf("%s%s\n", indent, o.color(colorYellow, "Binary files differ"))
		return
	}
	if len(oldContent) > maxDiffSize || len(newContent) > maxDiffSize {
		o.printf("%s%s\n", indent, o.color(colorYellow, "(file too large for diff)"))
		return
	}
	o.printf("%s%s\n", indent, o.color(colorRed, "--- "+oldPath))
	o.printf("%s%s\n", indent, o.color(colorGreen, "+++ "+newPath))
	for _, diffLine := range unifiedDiff(oldContent, newContent) {
		var diffColor string
		switch {
		case strings.HasPrefix(diffLine, "+"):
			diffColor = colorGreen
		case strings.HasPrefix(diffLine, "-"):
			diffColor = colorRed
		case strings.HasPrefix(diffLine, "@@"):
			diffColor = colorCyan
		default:
			diffColor = colorGray
		}
		o.printf("%s%s\n", indent, o.color(diffColor, diffLine))
	}
}

// redactedPlaceholder is shown in place of any value hidden for secret safety.
const redactedPlaceholder = "<redacted>"

// NoLogPlaceholder is shown in place of a whole task's params/message/error
// when the task sets no_log: true.
const NoLogPlaceholder = "(output hidden by no_log)"

// sensitiveKeyPattern matches parameter keys whose values are likely secrets
// and must never be printed, regardless of no_log.
var sensitiveKeyPattern = regexp.MustCompile(`(?i)pass|secret|token|key|credential`)

// isSensitiveKey reports whether a parameter key likely holds a secret.
func isSensitiveKey(key string) bool {
	return sensitiveKeyPattern.MatchString(key)
}

// formatTaskParams returns display lines for key parameters of a task, based on
// module type. Values under secret-looking keys are always redacted; when noLog
// is set the whole parameter set is suppressed.
func formatTaskParams(module string, params map[string]any, noLog bool) []string {
	if noLog {
		return []string{NoLogPlaceholder}
	}
	if len(params) == 0 {
		return nil
	}

	// Module-specific key lists (order matters for display)
	moduleKeys := map[string][]string{
		"command":  {"cmd"},
		"shell":    {"cmd"},
		"copy":     {"dest", "src", "mode"},
		"file":     {"path", "state", "mode", "owner"},
		"template": {"src", "dest", "mode"},
		"apt":      {"name", "state", "update_cache"},
		"brew":     {"name", "state", "cask"},
	}

	var lines []string

	if keys, ok := moduleKeys[module]; ok {
		for _, k := range keys {
			if v, exists := params[k]; exists {
				lines = append(lines, fmt.Sprintf("%s: %s", k, displayParamValue(k, v)))
			}
		}
	} else {
		// Generic fallback: show all params except internal ones (sorted for determinism)
		paramKeys := make([]string, 0, len(params))
		for k := range params {
			if !strings.HasPrefix(k, "_") {
				paramKeys = append(paramKeys, k)
			}
		}
		sort.Strings(paramKeys)
		for _, k := range paramKeys {
			lines = append(lines, fmt.Sprintf("%s: %s", k, displayParamValue(k, params[k])))
		}
	}

	return lines
}

// displayParamValue formats a parameter value for display, redacting secrets.
func displayParamValue(key string, v any) string {
	if isSensitiveKey(key) {
		return redactedPlaceholder
	}
	return truncateParamValue(v)
}

// sanitizeParams returns a copy of params safe to serialize (e.g. into JSON):
// nil when noLog is set, otherwise with internal keys dropped and secret-looking
// keys redacted.
func sanitizeParams(params map[string]any, noLog bool) map[string]any {
	if noLog || len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		if strings.HasPrefix(k, "_") {
			continue
		}
		if isSensitiveKey(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = v
	}
	return out
}

// truncateParamValue formats a parameter value for display, truncating long strings.
func truncateParamValue(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

const maxDiffSize = 64 * 1024 // 64KB threshold for diff display

// isBinary returns true if content appears to be a binary file (contains null bytes in first 8KB).
func isBinary(content string) bool {
	limit := 8192
	if len(content) < limit {
		limit = len(content)
	}
	return strings.Contains(content[:limit], "\x00")
}

// extractDestPath returns the destination file path from task params.
func extractDestPath(module string, params map[string]any) string {
	for _, key := range []string{"dest", "path"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return module + " target"
}

// printDiff renders a colored unified diff with file path headers.
// Falls back to a summary for binary or oversized files.
func (o *Output) printDiff(oldPath, newPath, oldContent, newContent string) {
	// Binary detection
	if isBinary(oldContent) || isBinary(newContent) {
		o.printf("      %s\n", o.color(colorYellow, "Binary files differ"))
		return
	}

	// Size threshold
	if len(oldContent) > maxDiffSize || len(newContent) > maxDiffSize {
		o.printf("      %s\n", o.color(colorYellow, "(file too large for diff)"))
		return
	}

	o.printf("      %s\n", o.color(colorRed, "--- "+oldPath))
	o.printf("      %s\n", o.color(colorGreen, "+++ "+newPath))
	for _, diffLine := range unifiedDiff(oldContent, newContent) {
		var diffColor string
		switch {
		case strings.HasPrefix(diffLine, "+"):
			diffColor = colorGreen
		case strings.HasPrefix(diffLine, "-"):
			diffColor = colorRed
		case strings.HasPrefix(diffLine, "@@"):
			diffColor = colorCyan
		default:
			diffColor = colorGray
		}
		o.printf("      %s\n", o.color(diffColor, diffLine))
	}
}

// unifiedDiff produces unified diff output with ±3 lines of context using Myers diff.
func unifiedDiff(old, new string) []string {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(old),
		B:        difflib.SplitLines(new),
		Context:  3,
	}

	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil || text == "" {
		return nil
	}

	var result []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		// Skip the --- and +++ headers (we add our own)
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		result = append(result, line)
	}
	return result
}

// IsApproval returns true if the input matches an accepted approval response
// (case-insensitive "y" or "yes").
func IsApproval(input string) bool {
	return strings.EqualFold(input, "y") || strings.EqualFold(input, "yes")
}

// PromptApproval asks the user to confirm applying changes. The target
// argument names the host(s) the changes will be applied to and is shown
// in the prompt line — see Emitter.PromptApproval for the format.
// Returns true if the user types "y" or "yes" (case-insensitive).
// Responds immediately to SIGINT/SIGTERM.
func (o *Output) PromptApproval(target string) bool {
	o.printf("\n%s ", o.color(colorBold, fmt.Sprintf("Apply these changes to %s?", target)))
	fmt.Fprint(o.w, "(yes/no): ")

	// Read input in a goroutine so we can race against signals
	resultCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			resultCh <- strings.TrimSpace(scanner.Text())
		} else {
			resultCh <- ""
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case text := <-resultCh:
		return IsApproval(text)
	case <-sigCh:
		fmt.Fprintln(o.w)
		return false
	}
}
