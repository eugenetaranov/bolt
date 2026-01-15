// Package template provides a module for rendering templates to target systems.
package template

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/eugenetaranov/bolt/internal/connector"
	"github.com/eugenetaranov/bolt/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module renders templates to the target system.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "template"
}

// Run executes the template module.
//
// Parameters:
//   - src (string, required): Template file path (relative paths resolve to role's templates/ dir)
//   - dest (string, required): Destination path on the target
//   - mode (string): File permissions in octal (e.g., "0644")
//   - owner (string): Owner username
//   - group (string): Group name
//   - backup (bool): Create backup before overwriting (default: false)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	// Extract parameters
	src, err := requireString(params, "src")
	if err != nil {
		return nil, err
	}

	dest, err := requireString(params, "dest")
	if err != nil {
		return nil, err
	}

	mode := getString(params, "mode", "0644")
	owner := getString(params, "owner", "")
	group := getString(params, "group", "")
	backup := getBool(params, "backup", false)

	// Get template variables (injected by executor)
	templateVars := getMap(params, "_template_vars")

	// Resolve template path - check if it's relative and we have a role path
	templatePath := src
	if !filepath.IsAbs(src) {
		// Check for role path (injected by executor for role tasks)
		if rolePath := getString(params, "_role_path", ""); rolePath != "" {
			// Look in role's templates directory
			roleTemplatePath := filepath.Join(rolePath, "templates", src)
			if _, err := os.Stat(roleTemplatePath); err == nil {
				templatePath = roleTemplatePath
			}
		}
	}

	// Read template file
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read template file '%s': %w", templatePath, err)
	}

	// Render template
	renderedContent, err := renderTemplate(src, string(templateContent), templateVars)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	// Calculate checksum of rendered content
	srcChecksum := checksum(renderedContent)

	// Check if destination exists and compare checksums
	destExists, destChecksum, err := getRemoteChecksum(ctx, conn, dest)
	if err != nil {
		return nil, fmt.Errorf("failed to check destination: %w", err)
	}

	// If destination exists with same content, check if we need to update mode/owner
	if destExists && srcChecksum == destChecksum {
		// File content matches, check attributes
		attrChanged, err := ensureAttributes(ctx, conn, dest, mode, owner, group)
		if err != nil {
			return nil, err
		}
		if attrChanged {
			return module.Changed("attributes updated"), nil
		}
		return module.Unchanged("template already rendered with correct content and attributes"), nil
	}

	// Create backup if needed
	if destExists && backup {
		if err := createBackup(ctx, conn, dest); err != nil {
			return nil, fmt.Errorf("failed to create backup: %w", err)
		}
	}

	// Upload the rendered content
	modeInt, err := parseMode(mode)
	if err != nil {
		return nil, fmt.Errorf("invalid mode: %w", err)
	}

	if err := conn.Upload(ctx, bytes.NewReader(renderedContent), dest, modeInt); err != nil {
		return nil, fmt.Errorf("failed to upload rendered template: %w", err)
	}

	// Set attributes
	if _, err := ensureAttributes(ctx, conn, dest, mode, owner, group); err != nil {
		return nil, err
	}

	var msg string
	if destExists {
		msg = "template updated"
	} else {
		msg = "template rendered"
	}

	return module.ChangedWithData(msg, map[string]any{
		"dest":     dest,
		"checksum": srcChecksum,
	}), nil
}

// renderTemplate renders a Go template with the given variables.
func renderTemplate(name, content string, vars map[string]any) ([]byte, error) {
	// Create template with custom delimiters to match {{ }} syntax
	// and add useful functions
	tmpl := template.New(name).Funcs(template.FuncMap{
		"default": func(def, val any) any {
			if val == nil || val == "" {
				return def
			}
			return val
		},
		"lower": strings.ToLower,
		"upper": strings.ToUpper,
		"trim":  strings.TrimSpace,
		"join": func(sep string, items []any) string {
			strs := make([]string, len(items))
			for i, item := range items {
				strs[i] = fmt.Sprintf("%v", item)
			}
			return strings.Join(strs, sep)
		},
	})

	// Parse the template
	tmpl, err := tmpl.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// checksum calculates SHA256 checksum of data.
func checksum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// getRemoteChecksum gets the SHA256 checksum of a remote file.
func getRemoteChecksum(ctx context.Context, conn connector.Connector, path string) (exists bool, sum string, err error) {
	cmd := fmt.Sprintf(`if [ -f %[1]s ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			sha256sum %[1]s | cut -d' ' -f1
		elif command -v shasum >/dev/null 2>&1; then
			shasum -a 256 %[1]s | cut -d' ' -f1
		else
			echo "NO_SHA"
		fi
	else
		echo "NO_FILE"
	fi`, shellQuote(path))

	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, "", err
	}

	output := strings.TrimSpace(result.Stdout)
	switch output {
	case "NO_FILE":
		return false, "", nil
	case "NO_SHA":
		return true, "", nil
	default:
		return true, output, nil
	}
}

// ensureAttributes sets mode and ownership on a file.
func ensureAttributes(ctx context.Context, conn connector.Connector, path, mode, owner, group string) (bool, error) {
	var changed bool

	// Set mode
	if mode != "" {
		result, err := conn.Execute(ctx, fmt.Sprintf("chmod %s %s", mode, shellQuote(path)))
		if err != nil {
			return false, fmt.Errorf("failed to set mode: %w", err)
		}
		if result.ExitCode != 0 {
			return false, fmt.Errorf("chmod failed: %s", result.Stderr)
		}
		changed = true
	}

	// Set ownership
	if owner != "" || group != "" {
		var ownership string
		if owner != "" && group != "" {
			ownership = fmt.Sprintf("%s:%s", owner, group)
		} else if owner != "" {
			ownership = owner
		} else {
			ownership = fmt.Sprintf(":%s", group)
		}

		result, err := conn.Execute(ctx, fmt.Sprintf("chown %s %s", ownership, shellQuote(path)))
		if err != nil {
			return false, fmt.Errorf("failed to set ownership: %w", err)
		}
		if result.ExitCode != 0 {
			return false, fmt.Errorf("chown failed: %s", result.Stderr)
		}
		changed = true
	}

	return changed, nil
}

// createBackup creates a timestamped backup of a file.
func createBackup(ctx context.Context, conn connector.Connector, path string) error {
	timestamp := time.Now().Format("20060102150405")
	backupPath := fmt.Sprintf("%s.%s.bak", path, timestamp)

	result, err := conn.Execute(ctx, fmt.Sprintf("cp -p %s %s", shellQuote(path), shellQuote(backupPath)))
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("backup failed: %s", result.Stderr)
	}
	return nil
}

// parseMode converts an octal mode string to uint32.
func parseMode(mode string) (uint32, error) {
	mode = strings.TrimLeft(mode, "0")
	if mode == "" {
		mode = "0"
	}

	var m uint32
	_, err := fmt.Sscanf("0"+mode, "%o", &m)
	if err != nil {
		return 0, err
	}
	return m, nil
}

// shellQuote quotes a string for safe use in shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// Helper functions for parameter extraction

func requireString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("required parameter '%s' is missing", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parameter '%s' must be a string", key)
	}
	if s == "" {
		return "", fmt.Errorf("parameter '%s' cannot be empty", key)
	}
	return s, nil
}

func getString(params map[string]any, key, defaultValue string) string {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	s, ok := v.(string)
	if !ok {
		return defaultValue
	}
	return s
}

func getBool(params map[string]any, key string, defaultValue bool) bool {
	v, ok := params[key]
	if !ok {
		return defaultValue
	}
	b, ok := v.(bool)
	if !ok {
		return defaultValue
	}
	return b
}

func getMap(params map[string]any, key string) map[string]any {
	v, ok := params[key]
	if !ok {
		return make(map[string]any)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return make(map[string]any)
	}
	return m
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)
