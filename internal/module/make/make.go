// Package makemod provides a module for building software from source on the
// target (configure/make/install) with a mandatory idempotency guard.
package makemod

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module runs a build in a source directory, guarded for idempotency.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "make"
}

type config struct {
	chdir     string
	configure string
	target    string
	install   string // resolved install command ("" = skip)
	env       map[string]string
	jobs      int
	creates   string
	unless    string
}

// Run executes the make module.
//
// Parameters:
//   - chdir (string, required): source directory to build in
//   - configure (string): configure command to run first
//   - target (string): make target
//   - install (bool|string): true => "make install"; string => run verbatim
//   - env (map): environment variables exported for all steps
//   - jobs (int): parallelism (-j)
//   - creates (string): skip if this path exists (one guard required)
//   - unless (string): skip if this shell expression exits 0 (one guard required)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}

	skip, err := guardSatisfied(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}
	if skip {
		return module.Unchanged("build already satisfied"), nil
	}

	if _, err := connector.Run(ctx, conn, cfg.buildCommand()); err != nil {
		return nil, fmt.Errorf("build failed: %w", err)
	}
	return module.Changed(fmt.Sprintf("built in %s", cfg.chdir)), nil
}

// Check reports whether make would build without doing so.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}
	skip, err := guardSatisfied(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}
	if skip {
		return module.NoChange("build already satisfied"), nil
	}
	return module.WouldChange("would build in " + cfg.chdir), nil
}

// guardSatisfied evaluates the creates/unless guard.
func guardSatisfied(ctx context.Context, conn connector.Connector, cfg config) (bool, error) {
	if cfg.creates != "" {
		res, err := conn.Execute(ctx, fmt.Sprintf("test -e %s", connector.ShellQuote(cfg.creates)))
		if err != nil {
			return false, err
		}
		return res.ExitCode == 0, nil
	}
	// unless
	res, err := conn.Execute(ctx, cfg.unless)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// buildCommand assembles the full shell command: cd, export env, then the
// configure/make/install steps chained with &&.
func (cfg config) buildCommand() string {
	steps := make([]string, 0, 3)
	if cfg.configure != "" {
		steps = append(steps, cfg.configure)
	}

	makeStep := "make"
	if cfg.jobs > 0 {
		makeStep += fmt.Sprintf(" -j%d", cfg.jobs)
	}
	if cfg.target != "" {
		makeStep += " " + cfg.target
	}
	steps = append(steps, makeStep)

	if cfg.install != "" {
		steps = append(steps, cfg.install)
	}

	cmd := "cd " + connector.ShellQuote(cfg.chdir)
	if len(cfg.env) > 0 {
		var assigns []string
		for _, k := range sortedKeys(cfg.env) {
			assigns = append(assigns, k+"="+connector.ShellQuote(cfg.env[k]))
		}
		cmd += " && export " + strings.Join(assigns, " ")
	}
	cmd += " && " + strings.Join(steps, " && ")
	return cmd
}

// parseConfig validates params.
func parseConfig(params map[string]any) (config, error) {
	chdir, err := module.RequireString(params, "chdir")
	if err != nil {
		return config{}, err
	}

	cfg := config{
		chdir:     chdir,
		configure: module.GetString(params, "configure", ""),
		target:    module.GetString(params, "target", ""),
		env:       stringMap(module.GetMap(params, "env")),
		jobs:      module.GetInt(params, "jobs", 0),
		creates:   module.GetString(params, "creates", ""),
		unless:    module.GetString(params, "unless", ""),
	}

	if cfg.creates != "" && cfg.unless != "" {
		return config{}, fmt.Errorf("specify only one of 'creates' or 'unless'")
	}
	if cfg.creates == "" && cfg.unless == "" {
		return config{}, fmt.Errorf("make requires an idempotency guard: set 'creates' or 'unless'")
	}

	switch v := params["install"].(type) {
	case bool:
		if v {
			cfg.install = "make install"
		}
	case string:
		cfg.install = v
	case nil:
		// skip install
	default:
		return config{}, fmt.Errorf("'install' must be a bool or a string")
	}

	return cfg, nil
}

// stringMap converts a map[string]any to map[string]string.
func stringMap(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// sortedKeys returns the keys of m in sorted order (deterministic commands).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Ensure Module implements the expected interfaces.
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

// Description returns a short summary of the make module.
func (m *Module) Description() string {
	return "Build from source on the target (configure/make/install); requires a creates or unless guard."
}

// Parameters returns the parameter documentation for the make module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "chdir", Type: "string", Required: true, Description: "Source directory to build in"},
		{Name: "configure", Type: "string", Description: "Configure command to run first"},
		{Name: "target", Type: "string", Description: "make target"},
		{Name: "install", Type: "bool|string", Description: "true => \"make install\"; string => run verbatim"},
		{Name: "env", Type: "map", Description: "Environment variables exported for all steps"},
		{Name: "jobs", Type: "int", Default: "0", Description: "Parallelism (-j)"},
		{Name: "creates", Type: "string", Description: "Skip if this path exists (one guard required)"},
		{Name: "unless", Type: "string", Description: "Skip if this shell expression exits 0 (one guard required)"},
	}
}

// Example returns a usage example for the make module.
func (m *Module) Example() string {
	return `- name: Build and install from source
  make:
    chdir: /usr/local/src/app-1.2.0
    configure: "./configure --prefix=/usr/local"
    jobs: 4
    install: true
    creates: /usr/local/bin/app`
}
