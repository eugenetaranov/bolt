// Package output provides formatted output for playbook execution.
package output

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/eugenetaranov/bolt/internal/playbook"
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
	w        io.Writer
	useColor bool
	debug    bool
}

// New creates a new output handler.
func New(w io.Writer) *Output {
	return &Output{
		w:        w,
		useColor: true,
	}
}

// SetColor enables or disables color output.
func (o *Output) SetColor(enabled bool) {
	o.useColor = enabled
}

// SetDebug enables or disables debug output.
func (o *Output) SetDebug(enabled bool) {
	o.debug = enabled
}

// SetVerbose is an alias for SetDebug for backward compatibility.
func (o *Output) SetVerbose(enabled bool) {
	o.debug = enabled
}

// color returns the string wrapped in color codes if enabled.
func (o *Output) color(c, s string) string {
	if !o.useColor {
		return s
	}
	return c + s + colorReset
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

// PlayStart prints the play start banner.
func (o *Output) PlayStart(play *playbook.Play) {
	name := play.Name
	if name == "" {
		name = play.Hosts
	}
	o.printf("\n%s %s\n", o.color(colorBold, "PLAY"), name)
}

// TaskStart is called when a task begins (no output in compact mode).
func (o *Output) TaskStart(name, moduleName string) {
	// In compact mode, we don't print anything on task start
	// Output is printed in TaskResult
}

// TaskResult prints the task result in a single line.
// Format: [status] module | host | task name
func (o *Output) TaskResult(name, status string, changed bool, message string) {
	// Determine status indicator and color
	var indicator string
	var statusColor string

	switch {
	case strings.HasPrefix(status, "ok"):
		indicator = "✓"
		statusColor = colorGreen
	case strings.HasPrefix(status, "changed"):
		indicator = "✓"
		statusColor = colorYellow
	case strings.HasPrefix(status, "skipped"):
		indicator = "○"
		statusColor = colorCyan
	case strings.HasPrefix(status, "failed"):
		indicator = "✗"
		statusColor = colorRed
	default:
		indicator = "?"
		statusColor = colorGray
	}

	// Print compact single line
	o.printf("  %s %s\n", o.color(statusColor, indicator), name)

	// In debug mode, print additional details
	if o.debug && message != "" {
		o.printf("    %s %s\n", o.color(colorGray, "→"), message)
	}
}

// TaskResultDetailed prints detailed task result (for debug mode).
func (o *Output) TaskResultDetailed(name, module, host, status, message string, data map[string]any) {
	// Determine status indicator and color
	var indicator string
	var statusColor string
	var statusText string

	switch {
	case strings.HasPrefix(status, "ok"):
		indicator = "✓"
		statusColor = colorGreen
		statusText = "ok"
	case strings.HasPrefix(status, "changed"):
		indicator = "✓"
		statusColor = colorYellow
		statusText = "changed"
	case strings.HasPrefix(status, "skipped"):
		indicator = "○"
		statusColor = colorCyan
		statusText = "skipped"
	case strings.HasPrefix(status, "failed"):
		indicator = "✗"
		statusColor = colorRed
		statusText = "FAILED"
	default:
		indicator = "?"
		statusColor = colorGray
		statusText = status
	}

	// Print compact line: [indicator] [module] name (host) - status
	moduleStr := ""
	if module != "" {
		moduleStr = o.color(colorGray, fmt.Sprintf("[%s] ", module))
	}
	hostStr := o.color(colorGray, fmt.Sprintf("(%s)", host))

	o.printf("  %s %s%s %s %s\n",
		o.color(statusColor, indicator),
		moduleStr,
		name,
		hostStr,
		o.color(statusColor, statusText))

	// In debug mode, print additional details
	if o.debug {
		if message != "" {
			o.printf("      %s %s\n", o.color(colorGray, "msg:"), message)
		}
		for k, v := range data {
			if k == "stdout" || k == "stderr" {
				if s, ok := v.(string); ok && s != "" {
					lines := strings.Split(strings.TrimSpace(s), "\n")
					o.printf("      %s\n", o.color(colorGray, k+":"))
					for _, line := range lines {
						o.printf("        %s\n", line)
					}
				}
			}
		}
	}
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
	fmt.Fprintf(o.w, format, args...)
}
