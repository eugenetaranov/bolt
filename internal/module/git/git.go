// Package git implements the `git` module for idempotent repository
// management on targets. It clones, fetches, and checks out a repository at
// a pinned branch/tag/SHA by comparing the current HEAD against the resolved
// target SHA and skipping work when they already match.
package git

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module manages a git repository checkout on the target host.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string { return "git" }

// shaPattern matches a 7-40 char hex string (a SHA or abbreviated SHA).
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// config holds resolved, validated parameters for one invocation.
type config struct {
	repo          string
	dest          string
	version       string // raw value; may be "" (use remote HEAD)
	versionIsSHA  bool   // true if `version` matches shaPattern
	force         bool
	depth         int
	acceptHostKey bool
	update        bool
	clone         bool
	bare          bool
	singleBranch  bool
	recursive     bool
	keyFile       string
}

// parseAndValidate extracts and validates all params.
func parseAndValidate(params map[string]any) (*config, error) {
	repo, err := module.RequireString(params, "repo")
	if err != nil {
		return nil, err
	}
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dest) {
		return nil, fmt.Errorf("parameter 'dest' must be an absolute path (got %q)", dest)
	}

	c := &config{
		repo:          repo,
		dest:          dest,
		version:       strings.TrimSpace(module.GetString(params, "version", "")),
		force:         module.GetBool(params, "force", false),
		depth:         module.GetInt(params, "depth", 0),
		acceptHostKey: module.GetBool(params, "accept_hostkey", false),
		update:        module.GetBool(params, "update", true),
		clone:         module.GetBool(params, "clone", true),
		bare:          module.GetBool(params, "bare", false),
		singleBranch:  module.GetBool(params, "single_branch", false),
		recursive:     module.GetBool(params, "recursive", false),
		keyFile:       module.GetString(params, "key_file", ""),
	}

	if _, provided := params["version"]; provided && c.version == "" {
		return nil, fmt.Errorf("parameter 'version' must be a non-empty string when provided")
	}
	if c.depth < 0 {
		return nil, fmt.Errorf("parameter 'depth' must be >= 0 (got %d)", c.depth)
	}
	c.versionIsSHA = c.version != "" && shaPattern.MatchString(c.version)
	return c, nil
}

// sshCommand composes a GIT_SSH_COMMAND env value from the config's ssh options.
// Returns an empty string if neither keyFile nor acceptHostKey are set.
func (c *config) sshCommand() string {
	parts := []string{"ssh"}
	changed := false
	if c.acceptHostKey {
		parts = append(parts, "-o", "StrictHostKeyChecking=accept-new")
		changed = true
	}
	if c.keyFile != "" {
		parts = append(parts, "-i", c.keyFile, "-o", "IdentitiesOnly=yes")
		changed = true
	}
	if !changed {
		return ""
	}
	return strings.Join(parts, " ")
}

// envPrefix returns a shell command prefix setting GIT_SSH_COMMAND, or an
// empty string if there is nothing to set.
func envPrefix(sshCmd string) string {
	if sshCmd == "" {
		return ""
	}
	return fmt.Sprintf("GIT_SSH_COMMAND=%s ", connector.ShellQuote(sshCmd))
}

// ensureGit verifies git is installed on the target.
func ensureGit(ctx context.Context, conn connector.Connector) error {
	ok, err := module.CommandAvailable(ctx, conn, "git")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("git binary not found on target: install git before running this module")
	}
	return nil
}

// resolveVersion returns the SHA (and resolved ref name when applicable) for
// the configured version. When version is unset, resolves HEAD via
// --symref. When it looks like a SHA, returns it as-is. Otherwise invokes
// `git ls-remote <repo> <version>` and disambiguates branches/tags.
func resolveVersion(ctx context.Context, conn connector.Connector, repo, version, sshCmd string) (sha, ref string, err error) {
	env := envPrefix(sshCmd)
	if version == "" {
		cmd := fmt.Sprintf("%sgit ls-remote --symref %s HEAD", env, connector.ShellQuote(repo))
		result, execErr := connector.Run(ctx, conn, cmd)
		if execErr != nil {
			return "", "", fmt.Errorf("failed to resolve remote HEAD for %s: %w", repo, execErr)
		}
		// Expected output:
		//   ref: refs/heads/<branch>\tHEAD
		//   <sha>\tHEAD
		for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ref: ") {
				rest := strings.TrimPrefix(line, "ref: ")
				if tab := strings.IndexByte(rest, '\t'); tab >= 0 {
					ref = strings.TrimSpace(rest[:tab])
				} else {
					ref = strings.TrimSpace(rest)
				}
				continue
			}
			if tab := strings.IndexByte(line, '\t'); tab >= 0 {
				sha = strings.TrimSpace(line[:tab])
			}
		}
		if sha == "" {
			return "", "", fmt.Errorf("could not resolve remote HEAD for %s", repo)
		}
		return sha, ref, nil
	}

	if shaPattern.MatchString(version) {
		return version, "", nil
	}

	cmd := fmt.Sprintf("%sgit ls-remote %s %s", env, connector.ShellQuote(repo), connector.ShellQuote(version))
	result, execErr := connector.Run(ctx, conn, cmd)
	if execErr != nil {
		return "", "", fmt.Errorf("failed to ls-remote %s: %w", repo, execErr)
	}

	var tagSHA, headSHA, otherSHA, otherRef string
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		lineSHA := strings.TrimSpace(line[:tab])
		lineRef := strings.TrimSpace(line[tab+1:])
		// Prefer annotated-tag-peeled refs when present: they end in `^{}`.
		switch {
		case strings.HasPrefix(lineRef, "refs/tags/") && strings.HasSuffix(lineRef, "^{}"):
			tagSHA = lineSHA
		case strings.HasPrefix(lineRef, "refs/tags/") && tagSHA == "":
			tagSHA = lineSHA
		case strings.HasPrefix(lineRef, "refs/heads/"):
			headSHA = lineSHA
		default:
			otherSHA = lineSHA
			otherRef = lineRef
		}
		_ = otherRef
	}
	switch {
	case tagSHA != "":
		return tagSHA, "refs/tags/" + version, nil
	case headSHA != "":
		return headSHA, "refs/heads/" + version, nil
	case otherSHA != "":
		return otherSHA, otherRef, nil
	}
	return "", "", fmt.Errorf("could not resolve ref %q in %s", version, repo)
}

// isGitRepo reports whether dest contains a git repository (worktree or bare).
func isGitRepo(ctx context.Context, conn connector.Connector, dest string) (bool, error) {
	cmd := fmt.Sprintf("test -e %s/.git -o -e %s/HEAD", connector.ShellQuote(dest), connector.ShellQuote(dest))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("failed to inspect %s: %w", dest, err)
	}
	return result.ExitCode == 0, nil
}

// currentSHA returns the current HEAD SHA in dest.
func currentSHA(ctx context.Context, conn connector.Connector, dest string) (string, error) {
	cmd := fmt.Sprintf("git -C %s rev-parse HEAD", connector.ShellQuote(dest))
	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read HEAD in %s: %w", dest, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

// isDirty reports whether the worktree at dest has uncommitted changes.
func isDirty(ctx context.Context, conn connector.Connector, dest string) (bool, string, error) {
	cmd := fmt.Sprintf("git -C %s status --porcelain", connector.ShellQuote(dest))
	result, err := connector.Run(ctx, conn, cmd)
	if err != nil {
		return false, "", fmt.Errorf("failed to check worktree state in %s: %w", dest, err)
	}
	out := strings.TrimSpace(result.Stdout)
	return out != "", out, nil
}

// currentRemoteURL returns the URL of the `origin` remote in dest.
func currentRemoteURL(ctx context.Context, conn connector.Connector, dest string) (string, error) {
	cmd := fmt.Sprintf("git -C %s remote get-url origin", connector.ShellQuote(dest))
	result, err := conn.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read origin for %s: %w", dest, err)
	}
	if result.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(result.Stdout), nil
}

// doClone runs `git clone` with the configured flags. It creates the parent
// directory if necessary.
func doClone(ctx context.Context, conn connector.Connector, c *config, sshCmd string) error {
	parent := filepath.Dir(c.dest)
	if parent != "" && parent != "." && parent != "/" {
		if _, err := connector.Run(ctx, conn, fmt.Sprintf("mkdir -p %s", connector.ShellQuote(parent))); err != nil {
			return fmt.Errorf("failed to create parent %s: %w", parent, err)
		}
	}

	var flags []string
	if c.bare {
		flags = append(flags, "--bare")
	}
	if c.depth > 0 {
		flags = append(flags, fmt.Sprintf("--depth=%d", c.depth))
	}
	// `--single-branch --branch=<ref>` only makes sense when version is a
	// branch/tag name (not a SHA or unset).
	if c.singleBranch && c.version != "" && !c.versionIsSHA {
		flags = append(flags, "--single-branch", fmt.Sprintf("--branch=%s", c.version))
	} else if !c.singleBranch && c.version != "" && !c.versionIsSHA {
		// Also honor --branch (without --single-branch) so a fresh clone lands
		// with HEAD at the desired branch/tag.
		flags = append(flags, fmt.Sprintf("--branch=%s", c.version))
	}
	env := envPrefix(sshCmd)
	cmd := fmt.Sprintf("%sgit clone %s %s %s",
		env,
		strings.Join(flags, " "),
		connector.ShellQuote(c.repo),
		connector.ShellQuote(c.dest))
	// collapse double spaces when flags are empty
	cmd = strings.ReplaceAll(cmd, "  ", " ")
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// doFetch runs `git -C dest fetch origin [--depth=N]`. When targetSHA is a
// specific SHA, it attempts `git fetch origin <sha>` first and falls back to
// `git fetch --unshallow origin` when that fails on a shallow clone,
// appending a warning to warnings.
func doFetch(ctx context.Context, conn connector.Connector, c *config, targetSHA string, sshCmd string, warnings *[]string) error {
	env := envPrefix(sshCmd)
	dest := connector.ShellQuote(c.dest)

	if c.versionIsSHA && c.depth > 0 {
		// Try to fetch just the SHA at the requested depth.
		cmd := fmt.Sprintf("%sgit -C %s fetch --depth=%d origin %s", env, dest, c.depth, connector.ShellQuote(targetSHA))
		result, err := conn.Execute(ctx, cmd)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		// Fall back to --unshallow when the server rejects the SHA fetch.
		unshallow := fmt.Sprintf("%sgit -C %s fetch --unshallow origin", env, dest)
		if _, err := connector.Run(ctx, conn, unshallow); err != nil {
			// If the repo wasn't shallow to begin with, retry as a plain fetch.
			plain := fmt.Sprintf("%sgit -C %s fetch origin", env, dest)
			if _, err2 := connector.Run(ctx, conn, plain); err2 != nil {
				return fmt.Errorf("git fetch failed: %w", err)
			}
		}
		*warnings = append(*warnings, fmt.Sprintf("shallow fetch of SHA %s failed; repository was un-shallowed to complete the update", targetSHA))
		return nil
	}

	var cmd string
	if c.depth > 0 {
		cmd = fmt.Sprintf("%sgit -C %s fetch --depth=%d origin", env, dest, c.depth)
	} else {
		cmd = fmt.Sprintf("%sgit -C %s fetch origin", env, dest)
	}
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("git fetch failed: %w", err)
	}
	return nil
}

// doCheckout does `git -C dest checkout --detach <sha>`.
func doCheckout(ctx context.Context, conn connector.Connector, dest, sha string) error {
	cmd := fmt.Sprintf("git -C %s checkout --detach %s", connector.ShellQuote(dest), connector.ShellQuote(sha))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("git checkout failed: %w", err)
	}
	return nil
}

// doReset resets and cleans the worktree before a forced checkout.
func doReset(ctx context.Context, conn connector.Connector, dest string) error {
	q := connector.ShellQuote(dest)
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("git -C %s reset --hard", q)); err != nil {
		return fmt.Errorf("git reset --hard failed: %w", err)
	}
	if _, err := connector.Run(ctx, conn, fmt.Sprintf("git -C %s clean -fdx", q)); err != nil {
		return fmt.Errorf("git clean -fdx failed: %w", err)
	}
	return nil
}

// doSubmodules runs `git submodule update --init --recursive` inside dest.
func doSubmodules(ctx context.Context, conn connector.Connector, dest, sshCmd string) error {
	env := envPrefix(sshCmd)
	cmd := fmt.Sprintf("%sgit -C %s submodule update --init --recursive", env, connector.ShellQuote(dest))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Errorf("git submodule update failed: %w", err)
	}
	return nil
}

// Run executes the git module.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	c, err := parseAndValidate(params)
	if err != nil {
		return nil, err
	}
	if err := ensureGit(ctx, conn); err != nil {
		return nil, err
	}
	sshCmd := c.sshCommand()

	exists, err := isGitRepo(ctx, conn, c.dest)
	if err != nil {
		return nil, err
	}
	if !exists && !c.clone {
		return nil, fmt.Errorf("dest %s is not a git repository and clone=false", c.dest)
	}

	// update:false on an existing repo means "don't touch it".
	if exists && !c.update {
		sha, shaErr := currentSHA(ctx, conn, c.dest)
		if shaErr != nil {
			return nil, shaErr
		}
		remoteURL, _ := currentRemoteURL(ctx, conn, c.dest)
		return &module.Result{
			Changed: false,
			Message: fmt.Sprintf("repo already present at %s (update=false)", c.dest),
			Data: map[string]any{
				"before_sha":       sha,
				"after_sha":        sha,
				"remote_url":       remoteURL,
				"version_resolved": sha,
				"warnings":         []string{},
			},
		}, nil
	}

	warnings := []string{}

	// Resolve target SHA
	targetSHA, _, err := resolveVersion(ctx, conn, c.repo, c.version, sshCmd)
	if err != nil {
		return nil, err
	}

	if !exists {
		// Fresh clone path
		if err := doClone(ctx, conn, c, sshCmd); err != nil {
			return nil, err
		}
		// If version was a SHA (or we need to pin an explicit detached HEAD), checkout.
		if c.version != "" && c.versionIsSHA && !c.bare {
			if err := doCheckout(ctx, conn, c.dest, targetSHA); err != nil {
				return nil, err
			}
		}
		// Determine after_sha
		var afterSHA string
		if c.bare {
			// Report resolved SHA for bare clones (no worktree to rev-parse).
			afterSHA = targetSHA
		} else {
			afterSHA, err = currentSHA(ctx, conn, c.dest)
			if err != nil {
				return nil, err
			}
		}
		if c.recursive && !c.bare {
			if err := doSubmodules(ctx, conn, c.dest, sshCmd); err != nil {
				return nil, err
			}
		}
		remoteURL, _ := currentRemoteURL(ctx, conn, c.dest)
		return &module.Result{
			Changed: true,
			Message: fmt.Sprintf("cloned %s to %s", c.repo, c.dest),
			Data: map[string]any{
				"before_sha":       "",
				"after_sha":        afterSHA,
				"remote_url":       remoteURL,
				"version_resolved": targetSHA,
				"warnings":         warnings,
			},
		}, nil
	}

	// Existing repo path
	var beforeSHA string
	if c.bare {
		// Bare repos: use rev-parse HEAD (works on bare too).
		beforeSHA, err = currentSHA(ctx, conn, c.dest)
	} else {
		beforeSHA, err = currentSHA(ctx, conn, c.dest)
	}
	if err != nil {
		return nil, err
	}
	remoteURL, _ := currentRemoteURL(ctx, conn, c.dest)

	// Fast path: already at desired SHA
	if beforeSHA == targetSHA || (c.versionIsSHA && strings.HasPrefix(beforeSHA, c.version)) {
		return &module.Result{
			Changed: false,
			Message: fmt.Sprintf("repo at %s already at %s", c.dest, beforeSHA),
			Data: map[string]any{
				"before_sha":       beforeSHA,
				"after_sha":        beforeSHA,
				"remote_url":       remoteURL,
				"version_resolved": targetSHA,
				"warnings":         warnings,
			},
		}, nil
	}

	// Dirty-worktree check (skip for bare).
	if !c.bare {
		dirty, paths, err := isDirty(ctx, conn, c.dest)
		if err != nil {
			return nil, err
		}
		if dirty {
			if !c.force {
				return nil, fmt.Errorf("worktree at %s is dirty (use force:true to override):\n%s", c.dest, paths)
			}
			if err := doReset(ctx, conn, c.dest); err != nil {
				return nil, err
			}
		}
	}

	// Fetch + checkout
	if err := doFetch(ctx, conn, c, targetSHA, sshCmd, &warnings); err != nil {
		return nil, err
	}
	if !c.bare {
		if err := doCheckout(ctx, conn, c.dest, targetSHA); err != nil {
			return nil, err
		}
	}
	var afterSHA string
	if c.bare {
		afterSHA = targetSHA
	} else {
		afterSHA, err = currentSHA(ctx, conn, c.dest)
		if err != nil {
			return nil, err
		}
	}
	if c.recursive && !c.bare {
		if err := doSubmodules(ctx, conn, c.dest, sshCmd); err != nil {
			return nil, err
		}
	}

	return &module.Result{
		Changed: true,
		Message: fmt.Sprintf("updated %s from %s to %s", c.dest, beforeSHA, afterSHA),
		Data: map[string]any{
			"before_sha":       beforeSHA,
			"after_sha":        afterSHA,
			"remote_url":       remoteURL,
			"version_resolved": targetSHA,
			"warnings":         warnings,
		},
	}, nil
}

// Check performs a read-only dry-run.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	c, err := parseAndValidate(params)
	if err != nil {
		return nil, err
	}
	if err := ensureGit(ctx, conn); err != nil {
		return nil, err
	}
	sshCmd := c.sshCommand()

	exists, err := isGitRepo(ctx, conn, c.dest)
	if err != nil {
		return nil, err
	}
	if !exists && !c.clone {
		return nil, fmt.Errorf("dest %s is not a git repository and clone=false", c.dest)
	}

	targetSHA, _, err := resolveVersion(ctx, conn, c.repo, c.version, sshCmd)
	if err != nil {
		return nil, err
	}

	if !exists {
		cr := module.WouldChange(fmt.Sprintf("would clone %s to %s at %s", c.repo, c.dest, targetSHA))
		cr.OldChecksum = ""
		cr.NewChecksum = targetSHA
		return cr, nil
	}

	beforeSHA, err := currentSHA(ctx, conn, c.dest)
	if err != nil {
		return nil, err
	}

	if !c.update {
		return module.NoChange(fmt.Sprintf("repo at %s, update=false", c.dest)), nil
	}

	if beforeSHA == targetSHA || (c.versionIsSHA && strings.HasPrefix(beforeSHA, c.version)) {
		return module.NoChange(fmt.Sprintf("repo at %s already at %s", c.dest, beforeSHA)), nil
	}

	cr := module.WouldChange(fmt.Sprintf("would update %s from %s to %s", c.dest, beforeSHA, targetSHA))
	cr.OldChecksum = beforeSHA
	cr.NewChecksum = targetSHA
	return cr, nil
}

// Description implements the Describer interface.
func (m *Module) Description() string {
	return "Manage git repository checkouts on targets idempotently — clone, fetch, and checkout at a pinned branch/tag/SHA"
}

// Parameters implements the Describer interface.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "repo", Type: "string", Required: true, Description: "Repository URL (SSH or HTTPS)"},
		{Name: "dest", Type: "string", Required: true, Description: "Absolute path on target where the repo should live"},
		{Name: "version", Type: "string", Required: false, Description: "Branch, tag, or SHA to check out; defaults to remote default branch"},
		{Name: "force", Type: "bool", Required: false, Default: "false", Description: "Reset a dirty worktree before checkout"},
		{Name: "depth", Type: "int", Required: false, Default: "0", Description: "Shallow clone depth (0 = full clone)"},
		{Name: "update", Type: "bool", Required: false, Default: "true", Description: "Fetch and checkout when repo already exists"},
		{Name: "clone", Type: "bool", Required: false, Default: "true", Description: "Clone when dest is missing"},
		{Name: "bare", Type: "bool", Required: false, Default: "false", Description: "Create a bare clone (no worktree)"},
		{Name: "single_branch", Type: "bool", Required: false, Default: "false", Description: "Clone only the target branch"},
		{Name: "recursive", Type: "bool", Required: false, Default: "false", Description: "Initialize submodules recursively after checkout"},
		{Name: "accept_hostkey", Type: "bool", Required: false, Default: "false", Description: "Auto-add host to known_hosts on first connect (TOFU)"},
		{Name: "key_file", Type: "string", Required: false, Description: "Path on target to SSH private key to use for the clone/fetch"},
	}
}

// Ensure interface satisfaction.
var _ module.Module = (*Module)(nil)
var _ module.Checker = (*Module)(nil)
var _ module.Describer = (*Module)(nil)
