// Package blockinfile provides a module for managing blocks of text in files using markers.
package blockinfile

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

const defaultMarker = "# {mark} BOLT MANAGED BLOCK"

// Module manages a block of text between marker lines in a file.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "blockinfile"
}

// Example returns a usage example for the blockinfile module.
func (m *Module) Example() string {
	return `- name: Manage a config block
  blockinfile:
    path: /etc/ssh/sshd_config
    marker: "# {mark} TACK MANAGED"
    block: |
      Match User deploy
        PasswordAuthentication no`
}

// Run executes the blockinfile module.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	block := module.GetString(params, "block", "")
	create := module.GetBool(params, "create", false)
	backup := module.GetBool(params, "backup", false)

	beginMarker, endMarker := resolveMarkers(params)

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
		insertAfter := module.GetString(params, "insertafter", "")
		insertBefore := module.GetString(params, "insertbefore", "")
		newContent = ensureBlockPresent(content, block, beginMarker, endMarker, insertAfter, insertBefore)
	case "absent":
		newContent = ensureBlockAbsent(content, beginMarker, endMarker)
	default:
		return nil, fmt.Errorf("invalid state: %s (must be present or absent)", state)
	}

	if newContent == content && exists {
		return module.Unchanged("block already in desired state"), nil
	}

	if exists && backup {
		if err := module.CreateBackup(ctx, conn, path); err != nil {
			return nil, err
		}
	}

	if err := conn.Upload(ctx, bytes.NewReader([]byte(newContent)), path, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	if !exists {
		return module.Changed("file created with block"), nil
	}
	if state == "absent" {
		return module.Changed("block removed"), nil
	}
	return module.Changed("block added/updated"), nil
}

// Check implements the Checker interface for dry-run support.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	path, err := module.RequireString(params, "path")
	if err != nil {
		return nil, err
	}

	state := module.GetString(params, "state", "present")
	block := module.GetString(params, "block", "")
	create := module.GetBool(params, "create", false)

	beginMarker, endMarker := resolveMarkers(params)

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
		insertAfter := module.GetString(params, "insertafter", "")
		insertBefore := module.GetString(params, "insertbefore", "")
		newContent = ensureBlockPresent(content, block, beginMarker, endMarker, insertAfter, insertBefore)
	case "absent":
		newContent = ensureBlockAbsent(content, beginMarker, endMarker)
	}

	if newContent == content && exists {
		return module.NoChange("block already in desired state"), nil
	}

	cr := module.WouldChange("file would be modified")
	cr.OldContent = content
	cr.NewContent = newContent
	return cr, nil
}

// Description implements the Describer interface.
func (m *Module) Description() string {
	return "Manage a block of text between marker lines in a file"
}

// Parameters implements the Describer interface.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "path", Type: "string", Required: true, Description: "Path to the target file"},
		{Name: "block", Type: "string", Required: false, Description: "Multi-line content to manage between markers"},
		{Name: "marker", Type: "string", Required: false, Default: "# {mark} BOLT MANAGED BLOCK", Description: "Marker format with {mark} placeholder"},
		{Name: "marker_begin", Type: "string", Required: false, Default: "BEGIN", Description: "Text to replace {mark} in the begin marker"},
		{Name: "marker_end", Type: "string", Required: false, Default: "END", Description: "Text to replace {mark} in the end marker"},
		{Name: "state", Type: "string", Required: false, Default: "present", Description: "Whether the block should be present or absent"},
		{Name: "insertafter", Type: "string", Required: false, Description: "Insert after this regex match or EOF (default)"},
		{Name: "insertbefore", Type: "string", Required: false, Description: "Insert before this regex match or BOF"},
		{Name: "create", Type: "bool", Required: false, Default: "false", Description: "Create file if it does not exist"},
		{Name: "backup", Type: "bool", Required: false, Default: "false", Description: "Create backup before modifying"},
	}
}

// resolveMarkers computes the begin and end marker strings from params.
func resolveMarkers(params map[string]any) (string, string) {
	marker := module.GetString(params, "marker", defaultMarker)
	markerBegin := module.GetString(params, "marker_begin", "BEGIN")
	markerEnd := module.GetString(params, "marker_end", "END")

	beginMarker := strings.ReplaceAll(marker, "{mark}", markerBegin)
	endMarker := strings.ReplaceAll(marker, "{mark}", markerEnd)
	return beginMarker, endMarker
}

// readRemoteFile reads a file from the remote system via the connector.
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

// ensureBlockPresent ensures the block content exists between markers.
func ensureBlockPresent(content, block, beginMarker, endMarker, insertAfter, insertBefore string) string {
	lines := splitLines(content)

	// Build the block lines (markers + content)
	var blockLines []string
	blockLines = append(blockLines, beginMarker)
	if block != "" {
		blockLines = append(blockLines, splitLines(block)...)
	}
	blockLines = append(blockLines, endMarker)

	// Find existing markers
	beginIdx := -1
	endIdx := -1
	for i, l := range lines {
		if l == beginMarker {
			beginIdx = i
		}
		if l == endMarker && beginIdx >= 0 {
			endIdx = i
			break
		}
	}

	if beginIdx >= 0 && endIdx >= 0 {
		// Replace content between markers (inclusive)
		result := make([]string, 0, len(lines))
		result = append(result, lines[:beginIdx]...)
		result = append(result, blockLines...)
		result = append(result, lines[endIdx+1:]...)
		return joinLines(result)
	}

	// No existing markers — insert the block
	return insertBlock(lines, blockLines, insertAfter, insertBefore)
}

// ensureBlockAbsent removes the markers and everything between them.
func ensureBlockAbsent(content, beginMarker, endMarker string) string {
	lines := splitLines(content)

	beginIdx := -1
	endIdx := -1
	for i, l := range lines {
		if l == beginMarker {
			beginIdx = i
		}
		if l == endMarker && beginIdx >= 0 {
			endIdx = i
			break
		}
	}

	if beginIdx < 0 || endIdx < 0 {
		return content // Markers not found
	}

	result := make([]string, 0, len(lines))
	result = append(result, lines[:beginIdx]...)
	result = append(result, lines[endIdx+1:]...)
	return joinLines(result)
}

// insertBlock inserts a block at the position determined by insertafter/insertbefore.
func insertBlock(lines, blockLines []string, insertAfter, insertBefore string) string {
	if strings.EqualFold(insertBefore, "BOF") {
		result := make([]string, 0, len(blockLines)+len(lines))
		result = append(result, blockLines...)
		result = append(result, lines...)
		return joinLines(result)
	}

	if insertBefore != "" && !strings.EqualFold(insertBefore, "BOF") {
		re, err := regexp.Compile(insertBefore)
		if err == nil {
			for i, l := range lines {
				if re.MatchString(l) {
					result := make([]string, 0, len(lines)+len(blockLines))
					result = append(result, lines[:i]...)
					result = append(result, blockLines...)
					result = append(result, lines[i:]...)
					return joinLines(result)
				}
			}
		}
	}

	if insertAfter != "" && !strings.EqualFold(insertAfter, "EOF") {
		re, err := regexp.Compile(insertAfter)
		if err == nil {
			lastMatch := -1
			for i, l := range lines {
				if re.MatchString(l) {
					lastMatch = i
				}
			}
			if lastMatch >= 0 {
				result := make([]string, 0, len(lines)+len(blockLines))
				result = append(result, lines[:lastMatch+1]...)
				result = append(result, blockLines...)
				result = append(result, lines[lastMatch+1:]...)
				return joinLines(result)
			}
		}
	}

	// Default: append at end
	result := make([]string, 0, len(lines)+len(blockLines))
	result = append(result, lines...)
	result = append(result, blockLines...)
	return joinLines(result)
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
