// Package geturl provides a module for downloading files onto the target.
package geturl

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

// Module downloads a file onto the target via curl or wget.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "get_url"
}

// config holds resolved parameters for one invocation.
type config struct {
	url     string
	dest    string
	algo    string // "" when no checksum given
	hash    string
	mode    string
	owner   string
	group   string
	force   bool
	timeout int
	headers map[string]string
}

// Run executes the get_url module.
//
// Parameters:
//   - url (string, required): source URL
//   - dest (string, required): destination path on the target
//   - checksum (string): "algo:hash" (sha256/sha512/sha1/md5; bare = sha256)
//   - mode (string): file mode, e.g. "0755"
//   - owner (string): owner username
//   - group (string): group name
//   - force (bool): re-download even if dest exists (default: false)
//   - timeout (int): max seconds for the transfer (0 = no cap)
//   - headers (map): extra request headers
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}

	// Idempotency: skip the download when dest already satisfies the request.
	if !cfg.force {
		satisfied, err := destSatisfied(ctx, conn, cfg)
		if err != nil {
			return nil, err
		}
		if satisfied {
			attrChanged, err := module.EnsureAttributes(ctx, conn, cfg.dest, cfg.mode, cfg.owner, cfg.group, false)
			if err != nil {
				return nil, err
			}
			if attrChanged {
				return module.Changed("attributes updated"), nil
			}
			return module.Unchanged("file already present" + checksumSuffix(cfg)), nil
		}
	}

	if err := ensureDownloader(ctx, conn); err != nil {
		return nil, err
	}

	if err := download(ctx, conn, cfg); err != nil {
		return nil, err
	}

	if _, err := module.EnsureAttributes(ctx, conn, cfg.dest, cfg.mode, cfg.owner, cfg.group, false); err != nil {
		return nil, err
	}

	return module.Changed(fmt.Sprintf("downloaded %s", cfg.dest)), nil
}

// Check reports whether get_url would download without doing so.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}
	if cfg.force {
		return module.WouldChange("would download " + cfg.dest + " (force)"), nil
	}
	satisfied, err := destSatisfied(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}
	if !satisfied {
		return module.WouldChange("would download " + cfg.dest), nil
	}
	attrDiffer, err := module.CheckAttributes(ctx, conn, cfg.dest, cfg.mode, cfg.owner, cfg.group)
	if err != nil {
		return nil, err
	}
	if attrDiffer {
		return module.WouldChange("would update attributes"), nil
	}
	return module.NoChange("file already present with correct attributes"), nil
}

// destSatisfied reports whether dest already exists and (when a checksum is
// given) matches it.
func destSatisfied(ctx context.Context, conn connector.Connector, cfg config) (bool, error) {
	if cfg.algo == "" {
		// No checksum: existence is enough.
		exists, _, err := module.GetRemoteChecksum(ctx, conn, cfg.dest)
		return exists, err
	}
	actual, exists, err := remoteDigest(ctx, conn, cfg.dest, cfg.algo)
	if err != nil {
		return false, err
	}
	return exists && strings.EqualFold(actual, cfg.hash), nil
}

// download fetches the URL to a temp file on the target, verifies the checksum,
// and atomically moves it into place.
func download(ctx context.Context, conn connector.Connector, cfg config) error {
	tmp := cfg.dest + ".tack-download"
	fetchCmd, err := fetchCommand(ctx, conn, cfg, tmp)
	if err != nil {
		return err
	}

	if _, err := connector.Run(ctx, conn, fetchCmd); err != nil {
		_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(tmp)))
		return fmt.Errorf("download failed: %w", err)
	}

	if cfg.algo != "" {
		actual, _, err := remoteDigest(ctx, conn, tmp, cfg.algo)
		if err != nil {
			_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(tmp)))
			return err
		}
		if !strings.EqualFold(actual, cfg.hash) {
			_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(tmp)))
			return fmt.Errorf("checksum mismatch: expected %s:%s, got %s", cfg.algo, cfg.hash, actual)
		}
	}

	mv := fmt.Sprintf("mv -f %s %s", connector.ShellQuote(tmp), connector.ShellQuote(cfg.dest))
	if _, err := connector.Run(ctx, conn, mv); err != nil {
		return fmt.Errorf("failed to move download into place: %w", err)
	}
	return nil
}

// fetchCommand builds a curl or wget command to fetch cfg.url to tmp.
func fetchCommand(ctx context.Context, conn connector.Connector, cfg config, tmp string) (string, error) {
	haveCurl, _ := module.CommandAvailable(ctx, conn, "curl")
	if haveCurl {
		parts := []string{"curl", "-fSL", "--retry", "3"}
		if cfg.timeout > 0 {
			parts = append(parts, "--max-time", fmt.Sprintf("%d", cfg.timeout))
		}
		for _, k := range sortedKeys(cfg.headers) {
			parts = append(parts, "-H", fmt.Sprintf("%s: %s", k, cfg.headers[k]))
		}
		parts = append(parts, "-o", tmp, cfg.url)
		return quoteAll(parts), nil
	}

	parts := []string{"wget", "-q"}
	if cfg.timeout > 0 {
		parts = append(parts, fmt.Sprintf("--timeout=%d", cfg.timeout))
	}
	for _, k := range sortedKeys(cfg.headers) {
		parts = append(parts, fmt.Sprintf("--header=%s: %s", k, cfg.headers[k]))
	}
	parts = append(parts, "-O", tmp, cfg.url)
	return quoteAll(parts), nil
}

// ensureDownloader verifies curl or wget is available on the target.
func ensureDownloader(ctx context.Context, conn connector.Connector) error {
	if ok, err := module.CommandAvailable(ctx, conn, "curl"); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := module.CommandAvailable(ctx, conn, "wget"); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("neither curl nor wget is available on the target; install one to use get_url")
}

// remoteDigest computes the digest of path on the target using the given algo.
func remoteDigest(ctx context.Context, conn connector.Connector, path, algo string) (sum string, exists bool, err error) {
	tool, ok := digestTool(algo)
	if !ok {
		return "", false, fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
	q := connector.ShellQuote(path)
	cmd := fmt.Sprintf(`if [ -f %[1]s ]; then %[2]s %[1]s | awk '{print $1}'; else echo NO_FILE; fi`, q, tool)
	res, err := conn.Execute(ctx, cmd)
	if err != nil {
		return "", false, err
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "NO_FILE" || out == "" {
		return "", false, nil
	}
	return out, true, nil
}

// digestTool maps an algorithm to its coreutils tool.
func digestTool(algo string) (string, bool) {
	switch algo {
	case "sha256":
		return "sha256sum", true
	case "sha512":
		return "sha512sum", true
	case "sha1":
		return "sha1sum", true
	case "md5":
		return "md5sum", true
	default:
		return "", false
	}
}

// parseConfig validates params.
func parseConfig(params map[string]any) (config, error) {
	url, err := module.RequireString(params, "url")
	if err != nil {
		return config{}, err
	}
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return config{}, err
	}

	cfg := config{
		url:     url,
		dest:    dest,
		mode:    module.GetString(params, "mode", ""),
		owner:   module.GetString(params, "owner", ""),
		group:   module.GetString(params, "group", ""),
		force:   module.GetBool(params, "force", false),
		timeout: module.GetInt(params, "timeout", 0),
		headers: stringMap(module.GetMap(params, "headers")),
	}

	if cs := strings.TrimSpace(module.GetString(params, "checksum", "")); cs != "" {
		algo, hash := "sha256", cs
		if i := strings.Index(cs, ":"); i >= 0 {
			algo, hash = strings.ToLower(cs[:i]), cs[i+1:]
		}
		if _, ok := digestTool(algo); !ok {
			return config{}, fmt.Errorf("unsupported checksum algorithm %q (use sha256, sha512, sha1, or md5)", algo)
		}
		if strings.TrimSpace(hash) == "" {
			return config{}, fmt.Errorf("checksum is missing a hash value")
		}
		cfg.algo, cfg.hash = algo, strings.TrimSpace(hash)
	}

	return cfg, nil
}

// checksumSuffix returns a short message fragment noting checksum verification.
func checksumSuffix(cfg config) string {
	if cfg.algo != "" {
		return " and matches checksum"
	}
	return ""
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

// Description returns a short summary of the get_url module.
func (m *Module) Description() string {
	return "Download a file onto the target (via curl/wget) with optional checksum verification."
}

// Parameters returns the parameter documentation for the get_url module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "url", Type: "string", Required: true, Description: "Source URL"},
		{Name: "dest", Type: "string", Required: true, Description: "Destination path on the target"},
		{Name: "checksum", Type: "string", Description: "\"algo:hash\" (sha256/sha512/sha1/md5; bare = sha256)"},
		{Name: "mode", Type: "string", Description: "File mode, e.g. \"0755\""},
		{Name: "owner", Type: "string", Description: "Owner username"},
		{Name: "group", Type: "string", Description: "Group name"},
		{Name: "force", Type: "bool", Default: "false", Description: "Re-download even if dest exists"},
		{Name: "timeout", Type: "int", Default: "0", Description: "Max seconds for the transfer (0 = no cap)"},
		{Name: "headers", Type: "map", Description: "Extra request headers"},
	}
}

// Example returns a usage example for the get_url module.
func (m *Module) Example() string {
	return `- name: Download a release tarball
  get_url:
    url: https://example.com/app-1.2.0.tar.gz
    dest: /tmp/app-1.2.0.tar.gz
    checksum: "sha256:<hash>"
    mode: "0644"`
}
