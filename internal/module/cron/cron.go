package cron

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module manages individual cron entries idempotently on Linux targets.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "cron"
}

// Example returns a usage example for the cron module.
func (m *Module) Example() string {
	return `- name: Nightly backup
  cron:
    name: nightly-backup
    job: /usr/local/bin/backup.sh
    hour: "2"
    minute: "0"`
}

// config holds resolved, validated parameters for one invocation.
type config struct {
	name        string
	job         string
	state       string // "present" | "absent"
	minute      string
	hour        string
	day         string
	month       string
	weekday     string
	specialTime string
	user        string
	cronFile    string // absolute path or ""
	disabled    bool
	env         bool
}

// parseAndValidate extracts and validates all parameters.
func parseAndValidate(params map[string]any) (*config, error) {
	c := &config{
		name:        module.GetString(params, "name", ""),
		job:         module.GetString(params, "job", ""),
		state:       module.GetString(params, "state", "present"),
		minute:      module.GetString(params, "minute", "*"),
		hour:        module.GetString(params, "hour", "*"),
		day:         module.GetString(params, "day", "*"),
		month:       module.GetString(params, "month", "*"),
		weekday:     module.GetString(params, "weekday", "*"),
		specialTime: module.GetString(params, "special_time", ""),
		user:        module.GetString(params, "user", ""),
		cronFile:    module.GetString(params, "cron_file", ""),
		disabled:    module.GetBool(params, "disabled", false),
		env:         module.GetBool(params, "env", false),
	}

	if err := isValidName(c.name); err != nil {
		return nil, fmt.Errorf("invalid name: %w", err)
	}

	if c.state != "present" && c.state != "absent" {
		return nil, fmt.Errorf("invalid state %q: must be present or absent", c.state)
	}

	// Per design Decision 4: `user` means different things based on cron_file:
	//   - cron_file unset: the user whose crontab to edit (crontab -u)
	//   - cron_file set:   the user field written into the drop-in line
	// Both usages are compatible; no mutex enforced here.

	// special_time validation
	if c.specialTime != "" {
		if _, ok := specialTimeSet[c.specialTime]; !ok {
			return nil, fmt.Errorf("invalid special_time %q: must be one of reboot, yearly, annually, monthly, weekly, daily, hourly", c.specialTime)
		}
	}

	// special_time vs time fields mutual exclusion
	if c.specialTime != "" && hasTimeFields(params) {
		return nil, fmt.Errorf("special_time and time fields (minute/hour/day/month/weekday) are mutually exclusive")
	}

	// env vs time fields / special_time
	if c.env {
		if c.specialTime != "" || hasTimeFields(params) {
			return nil, fmt.Errorf("env mode cannot be combined with schedule fields or special_time")
		}
		if !envLinePattern.MatchString(c.job) {
			return nil, fmt.Errorf("env mode requires job to match KEY=VALUE (got %q)", c.job)
		}
	}

	// state=present requires a job (unless env, which has its own check above)
	if c.state == "present" && c.job == "" {
		return nil, fmt.Errorf("job is required when state is present")
	}

	// cron_file validation
	if c.cronFile != "" {
		if !filepath.IsAbs(c.cronFile) {
			return nil, fmt.Errorf("cron_file must be an absolute path (got %q)", c.cronFile)
		}
		base := filepath.Base(c.cronFile)
		if !dropInNamePattern.MatchString(base) {
			return nil, fmt.Errorf("cron_file basename %q must match [A-Za-z0-9_-]+ (no dots or extensions)", base)
		}
		// Default user for drop-in files is "root" (written into the line).
		if c.user == "" {
			c.user = "root"
		}
	}

	return c, nil
}

// hasTimeFields returns true if any of the schedule fields are explicitly present in params.
func hasTimeFields(params map[string]any) bool {
	for _, k := range []string{"minute", "hour", "day", "month", "weekday"} {
		if _, ok := params[k]; ok {
			return true
		}
	}
	return false
}

// isLinuxTarget returns nil if the target's uname -s reports Linux, otherwise an error.
func isLinuxTarget(ctx context.Context, conn connector.Connector) error {
	result, err := connector.Run(ctx, conn, "uname -s")
	if err != nil {
		return fmt.Errorf("failed to detect target OS: %w", err)
	}
	os := strings.TrimSpace(result.Stdout)
	if os != "Linux" {
		return fmt.Errorf("cron module is only supported on Linux targets (got %s); consider launchd on macOS or systemd-timers", os)
	}
	return nil
}

// readBackend reads the current crontab content for the given config's backend.
// Returns content (possibly empty) and nil error when the crontab does not exist.
func (c *config) readBackend(ctx context.Context, conn connector.Connector) (string, error) {
	if c.cronFile != "" {
		return readDropIn(ctx, conn, c.cronFile)
	}
	return readUserCrontab(ctx, conn, c.user)
}

// writeBackend writes the content to the crontab for the given config's backend.
// If content is empty and the backend is a drop-in, the file is deleted.
func (c *config) writeBackend(ctx context.Context, conn connector.Connector, content string) error {
	if c.cronFile != "" {
		if strings.TrimSpace(content) == "" {
			return deleteDropIn(ctx, conn, c.cronFile)
		}
		return writeDropIn(ctx, conn, c.cronFile, content)
	}
	return writeUserCrontab(ctx, conn, c.user, content)
}

// backendLabel returns a human-readable identifier for the backend.
func (c *config) backendLabel() string {
	if c.cronFile != "" {
		return c.cronFile
	}
	if c.user != "" {
		return "crontab for user " + c.user
	}
	return "crontab"
}

// Run executes the cron module.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	c, err := parseAndValidate(params)
	if err != nil {
		return nil, err
	}
	if err := isLinuxTarget(ctx, conn); err != nil {
		return nil, err
	}

	oldContent, err := c.readBackend(ctx, conn)
	if err != nil {
		return nil, err
	}

	newContent, action, err := computeNewContent(c, oldContent)
	if err != nil {
		return nil, err
	}

	if newContent == oldContent {
		return resultUnchanged(c, action), nil
	}
	if err := c.writeBackend(ctx, conn, newContent); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", c.backendLabel(), err)
	}
	return resultChanged(c, action), nil
}

// Check implements the Checker interface for dry-run and --diff support.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	c, err := parseAndValidate(params)
	if err != nil {
		return nil, err
	}
	if err := isLinuxTarget(ctx, conn); err != nil {
		return nil, err
	}

	oldContent, err := c.readBackend(ctx, conn)
	if err != nil {
		return nil, err
	}

	newContent, action, err := computeNewContent(c, oldContent)
	if err != nil {
		return nil, err
	}

	if newContent == oldContent {
		return module.NoChange(fmt.Sprintf("cron entry %q %s", c.name, action)), nil
	}
	cr := module.WouldChange(fmt.Sprintf("cron entry %q would be %s", c.name, action))
	cr.OldContent = oldContent
	cr.NewContent = newContent
	return cr, nil
}

// computeNewContent applies the requested state to the existing content and
// returns (newContent, action-description, err). Action values:
//   "created" | "updated" | "removed" | "disabled" | "enabled" | "unchanged" | "already-absent"
func computeNewContent(c *config, oldContent string) (string, string, error) {
	lines := splitLines(oldContent)

	if c.state == "absent" {
		newLines, removed := applyAbsent(lines, c.name)
		if !removed {
			return oldContent, "already-absent", nil
		}
		return joinLines(newLines), "removed", nil
	}

	// state=present
	schedule := renderSchedulePrefix(c.specialTime, c.minute, c.hour, c.day, c.month, c.weekday)
	entry := renderEntryLine(c.env, schedule, c.user, c.job, c.cronFile != "")
	marker, entryLine := renderManagedBlock(c.name, entry, c.disabled)

	// Figure out prior action label by comparing pre- and post- slices.
	prev, existed := locateManaged(lines, c.name)
	newLines := applyPresent(lines, c.name, marker, entryLine)
	newContent := joinLines(newLines)

	if newContent == oldContent {
		return oldContent, "unchanged", nil
	}
	if !existed {
		return newContent, "created", nil
	}
	// Distinguish disabled/enabled toggle from content update.
	if prev.entryIdx >= 0 {
		prevEntry := lines[prev.entryIdx]
		prevDisabled := strings.HasPrefix(prevEntry, "# ")
		if prevDisabled != c.disabled {
			if c.disabled {
				return newContent, "disabled", nil
			}
			return newContent, "enabled", nil
		}
	}
	return newContent, "updated", nil
}

// resultChanged constructs a Changed result populated with Data for register.
func resultChanged(c *config, action string) *module.Result {
	return &module.Result{
		Changed: true,
		Message: fmt.Sprintf("cron entry %q %s", c.name, action),
		Data: map[string]any{
			"action": action,
			"file":   c.backendLabel(),
			"name":   c.name,
		},
	}
}

// resultUnchanged constructs an Unchanged result populated with Data for register.
func resultUnchanged(c *config, action string) *module.Result {
	if action == "" {
		action = "unchanged"
	}
	return &module.Result{
		Changed: false,
		Message: fmt.Sprintf("cron entry %q %s", c.name, action),
		Data: map[string]any{
			"action": action,
			"file":   c.backendLabel(),
			"name":   c.name,
		},
	}
}

// Description implements the Describer interface.
func (m *Module) Description() string {
	return "Manage individual cron entries idempotently (user crontabs and /etc/cron.d drop-ins)"
}

// Parameters implements the Describer interface.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "name", Type: "string", Required: true, Description: "Managed marker identifier; must be unique per crontab"},
		{Name: "job", Type: "string", Required: false, Description: "Command to run (required when state=present); for env=true, a KEY=VALUE line"},
		{Name: "state", Type: "string", Required: false, Default: "present", Description: "present or absent"},
		{Name: "minute", Type: "string", Required: false, Default: "*", Description: "Minute schedule field"},
		{Name: "hour", Type: "string", Required: false, Default: "*", Description: "Hour schedule field"},
		{Name: "day", Type: "string", Required: false, Default: "*", Description: "Day-of-month schedule field"},
		{Name: "month", Type: "string", Required: false, Default: "*", Description: "Month schedule field"},
		{Name: "weekday", Type: "string", Required: false, Default: "*", Description: "Day-of-week schedule field"},
		{Name: "special_time", Type: "string", Required: false, Description: "One of: reboot, yearly, annually, monthly, weekly, daily, hourly (mutually exclusive with time fields)"},
		{Name: "user", Type: "string", Required: false, Description: "User whose crontab to manage; or the user field written into a drop-in line (defaults to root for drop-ins)"},
		{Name: "cron_file", Type: "string", Required: false, Description: "Absolute path to a /etc/cron.d drop-in file (mutually exclusive with user-crontab mode)"},
		{Name: "disabled", Type: "bool", Required: false, Default: "false", Description: "Comment out the managed line while preserving the marker"},
		{Name: "env", Type: "bool", Required: false, Default: "false", Description: "Manage a KEY=VALUE environment line instead of a scheduled job"},
	}
}

// ---- Backends ----

// readUserCrontab returns the content of a user's crontab.
// A missing crontab ("no crontab for ...") returns "" with no error.
func readUserCrontab(ctx context.Context, conn connector.Connector, user string) (string, error) {
	cmd := "crontab -l"
	if user != "" {
		cmd = fmt.Sprintf("crontab -u %s -l", connector.ShellQuote(user))
	}
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read crontab: %w", err)
	}
	if result.ExitCode != 0 {
		// "no crontab for <user>" is reported on stderr with non-zero exit.
		if strings.Contains(strings.ToLower(result.Stderr), "no crontab for") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l failed: %s", strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}

// writeUserCrontab replaces the user's crontab with the given content via
// `crontab -` (or `crontab -u <user> -`). Empty content clears the crontab.
func writeUserCrontab(ctx context.Context, conn connector.Connector, user, content string) error {
	// Use a heredoc so the content is fed on stdin through the shell.
	// The EOF marker is unique enough to avoid collisions.
	const eof = "__TACK_CRON_EOF__"
	target := "crontab -"
	if user != "" {
		target = fmt.Sprintf("crontab -u %s -", connector.ShellQuote(user))
	}
	// The heredoc body must end with a newline before the delimiter.
	body := content
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	cmd := fmt.Sprintf("%s <<'%s'\n%s%s", target, eof, body, eof)
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("crontab write failed: %w", err)
	}
	return nil
}

// readDropIn reads a /etc/cron.d drop-in file. A missing file returns "" with no error.
func readDropIn(ctx context.Context, conn connector.Connector, path string) (string, error) {
	var buf bytes.Buffer
	if err := conn.Download(ctx, path, &buf); err != nil {
		// Best-effort: probe for existence via stat; if missing, treat as empty.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no such file") || strings.Contains(msg, "not exist") {
			return "", nil
		}
		// Fall through to a direct test (cat exit code) to distinguish "missing" from
		// real errors regardless of connector-specific error text.
		check, execErr := conn.Execute(ctx, fmt.Sprintf("test -e %s", connector.ShellQuote(path)))
		if execErr == nil && check.ExitCode != 0 {
			return "", nil
		}
		return "", fmt.Errorf("failed to download %s: %w", path, err)
	}
	return buf.String(), nil
}

// writeDropIn writes content to a drop-in file atomically: upload to <path>.tack.tmp,
// then mv it into place with mode 0644.
func writeDropIn(ctx context.Context, conn connector.Connector, path, content string) error {
	tmp := path + ".tack.tmp"
	if err := conn.Upload(ctx, strings.NewReader(content), tmp, 0o644); err != nil {
		return fmt.Errorf("failed to upload temp file: %w", err)
	}
	cmd := fmt.Sprintf("mv %s %s", connector.ShellQuote(tmp), connector.ShellQuote(path))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		// Best effort: clean up the temp file on failure.
		_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(tmp)))
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// deleteDropIn removes a drop-in file.
func deleteDropIn(ctx context.Context, conn connector.Connector, path string) error {
	cmd := fmt.Sprintf("rm -f %s", connector.ShellQuote(path))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	return nil
}

// Ensure interface satisfaction.
var _ module.Module = (*Module)(nil)
var _ module.Checker = (*Module)(nil)
var _ module.Describer = (*Module)(nil)
