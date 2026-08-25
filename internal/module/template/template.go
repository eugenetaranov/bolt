// Package template provides a module for rendering templates to target systems.
package template

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
	"github.com/tackhq/tack/pkg/ssmparams"
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

// Description returns a short summary of the template module.
func (m *Module) Description() string {
	return "Render a template with play/task variables and copy the result to the target."
}

// Parameters returns the parameter documentation for the template module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "src", Type: "string", Required: true, Description: "Template source path on the controller"},
		{Name: "dest", Type: "string", Required: true, Description: "Destination path on the target"},
		{Name: "mode", Type: "string", Default: "0644", Description: "File permissions in octal"},
		{Name: "owner", Type: "string", Description: "Owner username"},
		{Name: "group", Type: "string", Description: "Group name"},
		{Name: "backup", Type: "bool", Default: "false", Description: "Create a backup before overwriting"},
	}
}

// Example returns a usage example for the template module.
func (m *Module) Example() string {
	return `- name: Render a config template
  template:
    src: templates/app.conf.j2
    dest: /etc/app/app.conf
    owner: root
    mode: "0644"`
}

// Ensure Module implements the documentation interfaces.
var (
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

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
	src, err := module.RequireString(params, "src")
	if err != nil {
		return nil, err
	}

	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}

	mode := module.GetString(params, "mode", "0644")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	backup := module.GetBool(params, "backup", false)

	// Get template variables and SSM params client (injected by executor)
	templateVars := module.GetMap(params, "_template_vars")
	ssmClient, _ := params["_ssm_params"].(*ssmparams.Client)

	templatePath := module.ResolveRolePath(src, params, "templates")

	// Read template file
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read template file '%s': %w", templatePath, err)
	}

	// Render template
	renderedContent, err := renderTemplate(ctx, src, string(templateContent), templateVars, ssmClient)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	return module.DeployFile(ctx, conn, module.DeployOpts{
		Content: renderedContent,
		Dest:    dest,
		Mode:    mode,
		Owner:   owner,
		Group:   group,
		Backup:  backup,
		Label:   "template",
	})
}

// renderTemplate renders a Go template with the given variables.
func renderTemplate(ctx context.Context, name, content string, vars map[string]any, ssmClient *ssmparams.Client) ([]byte, error) {
	// Create template with useful functions
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
		"ssm_param": func(path string) (string, error) {
			if ssmClient == nil {
				return "", fmt.Errorf("ssm_param: no SSM client available")
			}
			return ssmClient.Get(ctx, path)
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

// Check determines whether the template module would make changes without applying them.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	src, err := module.RequireString(params, "src")
	if err != nil {
		return nil, err
	}

	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}

	mode := module.GetString(params, "mode", "0644")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	templateVars := module.GetMap(params, "_template_vars")
	ssmClient, _ := params["_ssm_params"].(*ssmparams.Client)

	templatePath := module.ResolveRolePath(src, params, "templates")

	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read template file '%s': %w", templatePath, err)
	}

	renderedContent, err := renderTemplate(ctx, src, string(templateContent), templateVars, ssmClient)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	diffEnabled, _ := params["_diff_enabled"].(bool)
	return module.CheckDeployFile(ctx, conn, renderedContent, dest, mode, owner, group, module.CheckOptions{DiffEnabled: diffEnabled})
}

// Ensure Module implements the module.Module interface.
var _ module.Module = (*Module)(nil)

// Ensure Module implements the module.Checker interface.
var _ module.Checker = (*Module)(nil)
