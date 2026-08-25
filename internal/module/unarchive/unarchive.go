// Package unarchive provides a module for extracting archives on the target.
package unarchive

import (
	"context"
	"fmt"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module extracts a local archive on the target into a directory.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "unarchive"
}

type config struct {
	src       string
	dest      string
	creates   string
	owner     string
	group     string
	mode      string
	strip     int
	extraOpts []string
	kind      string // "tar" or "zip"
	tarDecomp string // decompression flag for tar (may be empty)
	tool      string // "tar" or "unzip"
}

// Run executes the unarchive module.
//
// Parameters:
//   - src (string, required): archive path on the target
//   - dest (string, required): destination directory on the target
//   - creates (string): skip extraction if this path exists (idempotency)
//   - owner (string): owner applied to dest
//   - group (string): group applied to dest
//   - mode (string): mode applied to dest
//   - strip_components (int): drop leading path components (tar only)
//   - extra_opts ([]string): extra flags passed to the extractor
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}

	// Idempotency: skip when the marker already exists.
	if cfg.creates != "" {
		exists, err := pathExists(ctx, conn, cfg.creates)
		if err != nil {
			return nil, err
		}
		if exists {
			return module.Unchanged(fmt.Sprintf("%s already exists", cfg.creates)), nil
		}
	}

	if exists, err := pathExists(ctx, conn, cfg.src); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("source archive %s does not exist on the target", cfg.src)
	}

	if ok, err := module.CommandAvailable(ctx, conn, cfg.tool); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s is required to extract %s but is not available on the target", cfg.tool, cfg.src)
	}

	if _, err := connector.Run(ctx, conn, fmt.Sprintf("mkdir -p %s", connector.ShellQuote(cfg.dest))); err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", cfg.dest, err)
	}

	if _, err := connector.Run(ctx, conn, cfg.extractCommand()); err != nil {
		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	if _, err := module.EnsureAttributes(ctx, conn, cfg.dest, cfg.mode, cfg.owner, cfg.group, false); err != nil {
		return nil, err
	}

	return module.Changed(fmt.Sprintf("extracted %s to %s", cfg.src, cfg.dest)), nil
}

// Check reports whether unarchive would extract without doing so.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}
	if cfg.creates != "" {
		exists, err := pathExists(ctx, conn, cfg.creates)
		if err != nil {
			return nil, err
		}
		if exists {
			return module.NoChange(fmt.Sprintf("%s already exists", cfg.creates)), nil
		}
	}
	return module.WouldChange(fmt.Sprintf("would extract %s to %s", cfg.src, cfg.dest)), nil
}

// extractCommand builds the tar/unzip command for the target.
func (cfg config) extractCommand() string {
	if cfg.kind == "zip" {
		parts := []string{"unzip", "-o"}
		parts = append(parts, cfg.extraOpts...)
		parts = append(parts, cfg.src, "-d", cfg.dest)
		return quoteAll(parts)
	}

	parts := []string{"tar", "-x"}
	if cfg.tarDecomp != "" {
		parts = append(parts, cfg.tarDecomp)
	}
	parts = append(parts, "-C", cfg.dest)
	if cfg.strip > 0 {
		parts = append(parts, fmt.Sprintf("--strip-components=%d", cfg.strip))
	}
	parts = append(parts, cfg.extraOpts...)
	parts = append(parts, "-f", cfg.src)
	return quoteAll(parts)
}

// parseConfig validates params and detects the archive format.
func parseConfig(params map[string]any) (config, error) {
	src, err := module.RequireString(params, "src")
	if err != nil {
		return config{}, err
	}
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return config{}, err
	}

	cfg := config{
		src:       src,
		dest:      dest,
		creates:   module.GetString(params, "creates", ""),
		owner:     module.GetString(params, "owner", ""),
		group:     module.GetString(params, "group", ""),
		mode:      module.GetString(params, "mode", ""),
		strip:     module.GetInt(params, "strip_components", 0),
		extraOpts: module.GetStringSlice(params, "extra_opts"),
	}

	if err := cfg.detectFormat(); err != nil {
		return config{}, err
	}
	if cfg.kind == "zip" && cfg.strip > 0 {
		return config{}, fmt.Errorf("strip_components is not supported for zip archives")
	}

	return cfg, nil
}

// detectFormat sets kind, tool, and tarDecomp from the src extension.
func (cfg *config) detectFormat() error {
	l := strings.ToLower(cfg.src)
	switch {
	case strings.HasSuffix(l, ".zip"):
		cfg.kind, cfg.tool = "zip", "unzip"
	case strings.HasSuffix(l, ".tar"):
		cfg.kind, cfg.tool, cfg.tarDecomp = "tar", "tar", ""
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		cfg.kind, cfg.tool, cfg.tarDecomp = "tar", "tar", "-z"
	case strings.HasSuffix(l, ".tar.bz2"), strings.HasSuffix(l, ".tbz2"):
		cfg.kind, cfg.tool, cfg.tarDecomp = "tar", "tar", "-j"
	case strings.HasSuffix(l, ".tar.xz"), strings.HasSuffix(l, ".txz"):
		cfg.kind, cfg.tool, cfg.tarDecomp = "tar", "tar", "-J"
	case strings.HasSuffix(l, ".tar.zst"), strings.HasSuffix(l, ".tzst"):
		cfg.kind, cfg.tool, cfg.tarDecomp = "tar", "tar", "--zstd"
	default:
		return fmt.Errorf("unsupported archive format for %s (want .zip, .tar, .tar.gz/.tgz, .tar.bz2, .tar.xz, or .tar.zst)", cfg.src)
	}
	return nil
}

// pathExists reports whether a path exists on the target.
func pathExists(ctx context.Context, conn connector.Connector, path string) (bool, error) {
	res, err := conn.Execute(ctx, fmt.Sprintf("test -e %s", connector.ShellQuote(path)))
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// quoteAll shell-quotes each token and joins with spaces.
func quoteAll(parts []string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = connector.ShellQuote(p)
	}
	return strings.Join(q, " ")
}

// Ensure Module implements the expected interfaces.
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

// Description returns a short summary of the unarchive module.
func (m *Module) Description() string {
	return "Extract a local archive (tar family or zip) on the target into a directory."
}

// Parameters returns the parameter documentation for the unarchive module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "src", Type: "string", Required: true, Description: "Archive path on the target"},
		{Name: "dest", Type: "string", Required: true, Description: "Destination directory on the target"},
		{Name: "creates", Type: "string", Description: "Skip extraction if this path exists (idempotency)"},
		{Name: "owner", Type: "string", Description: "Owner applied to dest"},
		{Name: "group", Type: "string", Description: "Group applied to dest"},
		{Name: "mode", Type: "string", Description: "Mode applied to dest"},
		{Name: "strip_components", Type: "int", Default: "0", Description: "Drop leading path components (tar only)"},
		{Name: "extra_opts", Type: "[]string", Description: "Extra flags passed to the extractor"},
	}
}

// Example returns a usage example for the unarchive module.
func (m *Module) Example() string {
	return `- name: Extract an application tarball
  unarchive:
    src: /tmp/app-1.2.0.tar.gz
    dest: /opt/app
    strip_components: 1
    creates: /opt/app/bin/app`
}
