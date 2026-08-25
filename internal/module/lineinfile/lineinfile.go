// Package lineinfile provides a module for ensuring specific lines exist in files.
package lineinfile

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module ensures a specific line is present or absent in a file.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "lineinfile"
}

// Example returns a usage example for the lineinfile module.
func (m *Module) Example() string {
	return `- name: Ensure a line is present
  lineinfile:
    path: /etc/sysctl.conf
    line: net.ipv4.ip_forward=1
    regexp: "^net.ipv4.ip_forward="`
}

// Run executes the lineinfile module.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	line := module.GetString(params, "line", "")
	regexpStr := module.GetString(params, "regexp", "")
	insertAfter := module.GetString(params, "insertafter", "")
	insertBefore := module.GetString(params, "insertbefore", "")
	create := module.GetBool(params, "create", false)
	backup := module.GetBool(params, "backup", false)

	if state == "present" && line == "" {
		return nil, fmt.Errorf("parameter 'line' is required when state is present")
	}

	// Read existing file content
	content, exists, err := readRemoteFile(ctx, conn, path)
	if err != nil {
		return nil, err
	}

	if !exists {
		if state == "absent" {
			return module.Unchanged("file does not exist, nothing to remove"), nil
		}
		if !create {
			return nil, fmt.Errorf("file %s does not exist (use create: true to create it)", path)
		}
		content = ""
	}

	var newContent string
	switch state {
	case "present":
		newContent, err = ensurePresent(content, line, regexpStr, insertAfter, insertBefore)
	case "absent":
		newContent, err = ensureAbsent(content, line, regexpStr)
	default:
		return nil, fmt.Errorf("invalid state: %s (must be present or absent)", state)
	}
	if err != nil {
		return nil, err
	}

	if newContent == content && exists {
		return module.Unchanged("line already in desired state"), nil
	}

	// Backup before modifying
	if exists && backup {
		if err := module.CreateBackup(ctx, conn, path); err != nil {
			return nil, err
		}
	}

	// Write the file
	if err := conn.Upload(ctx, bytes.NewReader([]byte(newContent)), path, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	if !exists {
		return module.Changed("file created with line"), nil
	}
	if state == "absent" {
		return module.Changed("line(s) removed"), nil
	}
	return module.Changed("line added/updated"), nil
}

// Check implements the Checker interface for dry-run support.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	line := module.GetString(params, "line", "")
	regexpStr := module.GetString(params, "regexp", "")
	insertAfter := module.GetString(params, "insertafter", "")
	insertBefore := module.GetString(params, "insertbefore", "")
	create := module.GetBool(params, "create", false)

	content, exists, err := readRemoteFile(ctx, conn, path)
	if err != nil {
		return nil, err
	}

	if !exists {
		if state == "absent" {
			return module.NoChange("file does not exist"), nil
		}
		if !create {
			return module.WouldChange("file does not exist"), nil
		}
		content = ""
	}

	var newContent string
	switch state {
	case "present":
		newContent, err = ensurePresent(content, line, regexpStr, insertAfter, insertBefore)
	case "absent":
		newContent, err = ensureAbsent(content, line, regexpStr)
	}
	if err != nil {
		return nil, err
	}

	if newContent == content && exists {
		return module.NoChange("line already in desired state"), nil
	}

	cr := module.WouldChange("file would be modified")
	cr.OldContent = content
	cr.NewContent = newContent
	return cr, nil
}

// Description implements the Describer interface.
func (m *Module) Description() string {
	return "Ensure a specific line is present or absent in a file"
}

// Parameters implements the Describer interface.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "path", Type: "string", Required: true, Description: "Path to the target file"},
		{Name: "line", Type: "string", Required: false, Description: "The line to ensure in the file (required when state=present)"},
		{Name: "regexp", Type: "string", Required: false, Description: "Regular expression to match lines for replacement or removal"},
		{Name: "state", Type: "string", Required: false, Default: "present", Description: "Whether the line should be present or absent"},
		{Name: "insertafter", Type: "string", Required: false, Description: "Insert after this regex match or EOF (default)"},
		{Name: "insertbefore", Type: "string", Required: false, Description: "Insert before this regex match or BOF"},
		{Name: "create", Type: "bool", Required: false, Default: "false", Description: "Create file if it does not exist"},
		{Name: "backup", Type: "bool", Required: false, Default: "false", Description: "Create backup before modifying"},
	}
}

// readRemoteFile reads a file from the remote system via the connector.
// Returns content, exists, error.
func readRemoteFile(ctx context.Context, conn connector.Connector, path string) (string, bool, error) {
	result, err := conn.Execute(ctx, fmt.Sprintf("cat %s 2>/dev/null", connector.ShellQuote(path)))
	if err != nil {
		return "", false, fmt.Errorf("failed to read file: %w", err)
	}
	if result.ExitCode != 0 {
		return "", false, nil
	}
	return result.Stdout, true, nil
}

// ensurePresent ensures a line is present in the content.
func ensurePresent(content, line, regexpStr, insertAfter, insertBefore string) (string, error) {
	lines := splitLines(content)

	// If regexp is provided, find and replace the last match
	if regexpStr != "" {
		re, err := regexp.Compile(regexpStr)
		if err != nil {
			return "", fmt.Errorf("invalid regexp: %w", err)
		}

		lastMatch := -1
		for i, l := range lines {
			if re.MatchString(l) {
				lastMatch = i
			}
		}

		if lastMatch >= 0 {
			if lines[lastMatch] == line {
				return content, nil // Already correct
			}
			lines[lastMatch] = line
			return joinLines(lines), nil
		}
		// No match found — fall through to insert
	} else {
		// No regexp — check for exact match
		for _, l := range lines {
			if l == line {
				return content, nil // Already present
			}
		}
	}

	// Insert the line at the appropriate position
	return insertLine(lines, line, insertAfter, insertBefore)
}

// ensureAbsent removes matching lines from the content.
func ensureAbsent(content, line, regexpStr string) (string, error) {
	lines := splitLines(content)
	var result []string

	if regexpStr != "" {
		re, err := regexp.Compile(regexpStr)
		if err != nil {
			return "", fmt.Errorf("invalid regexp: %w", err)
		}
		for _, l := range lines {
			if !re.MatchString(l) {
				result = append(result, l)
			}
		}
	} else {
		for _, l := range lines {
			if l != line {
				result = append(result, l)
			}
		}
	}

	return joinLines(result), nil
}

// insertLine inserts a line at the position determined by insertafter/insertbefore.
func insertLine(lines []string, line, insertAfter, insertBefore string) (string, error) {
	// insertbefore BOF
	if strings.EqualFold(insertBefore, "BOF") {
		result := append([]string{line}, lines...)
		return joinLines(result), nil
	}

	// insertbefore with regex
	if insertBefore != "" && !strings.EqualFold(insertBefore, "BOF") {
		re, err := regexp.Compile(insertBefore)
		if err != nil {
			return "", fmt.Errorf("invalid insertbefore regexp: %w", err)
		}
		for i, l := range lines {
			if re.MatchString(l) {
				result := make([]string, 0, len(lines)+1)
				result = append(result, lines[:i]...)
				result = append(result, line)
				result = append(result, lines[i:]...)
				return joinLines(result), nil
			}
		}
		// No match — fall through to append at end
	}

	// insertafter with regex (not EOF)
	if insertAfter != "" && !strings.EqualFold(insertAfter, "EOF") {
		re, err := regexp.Compile(insertAfter)
		if err != nil {
			return "", fmt.Errorf("invalid insertafter regexp: %w", err)
		}
		lastMatch := -1
		for i, l := range lines {
			if re.MatchString(l) {
				lastMatch = i
			}
		}
		if lastMatch >= 0 {
			result := make([]string, 0, len(lines)+1)
			result = append(result, lines[:lastMatch+1]...)
			result = append(result, line)
			result = append(result, lines[lastMatch+1:]...)
			return joinLines(result), nil
		}
		// No match — fall through to append at end
	}

	// Default: append at end (EOF)
	lines = append(lines, line)
	return joinLines(lines), nil
}

// splitLines splits content into lines, handling the trailing newline correctly.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	// Remove trailing newline to avoid empty last element
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// joinLines joins lines back together with newlines, ensuring a trailing newline.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
