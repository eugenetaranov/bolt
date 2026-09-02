// Package authorizedkey provides a module for managing a user's SSH
// authorized_keys file.
package authorizedkey

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

// Module manages entries in a user's ~/.ssh/authorized_keys.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string { return "authorized_key" }

// Description returns a short summary of the module.
func (m *Module) Description() string {
	return "Manage SSH public keys in a user's authorized_keys file."
}

// Example returns a usage example.
func (m *Module) Example() string {
	return `- name: Authorize a deploy key
  authorized_key:
    user: deploy
    key: "ssh-ed25519 AAAA... deploy@ci"
    state: present`
}

// Parameters documents the module parameters.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "user", Type: "string", Required: true, Description: "User whose authorized_keys to manage"},
		{Name: "key", Type: "string|[]string", Required: true, Description: "One or more SSH public keys"},
		{Name: "state", Type: "string", Default: "present", Description: "present or absent"},
		{Name: "exclusive", Type: "bool", Default: "false", Description: "Replace all keys with exactly the given set"},
		{Name: "path", Type: "string", Description: "Override the authorized_keys path"},
	}
}

var (
	_ module.Module    = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

// Run applies the desired authorized_keys state.
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	user, err := module.RequireString(params, "user")
	if err != nil {
		return nil, err
	}
	keys := module.GetStringSlice(params, "key")
	if len(keys) == 0 {
		return nil, fmt.Errorf("'key' parameter is required")
	}
	state := module.GetString(params, "state", "present")
	if state != "present" && state != "absent" {
		return nil, fmt.Errorf("invalid state %q: must be present or absent", state)
	}
	exclusive := module.GetBool(params, "exclusive", false)

	home, err := userHome(ctx, conn, user)
	if err != nil {
		return nil, err
	}
	sshDir := home + "/.ssh"
	path := module.GetString(params, "path", sshDir+"/authorized_keys")

	current := readLines(ctx, conn, path)
	desired := desiredLines(current, keys, state, exclusive)

	if equalLines(current, desired) {
		return module.Unchanged("authorized_keys already in desired state"), nil
	}

	// Ensure ~/.ssh exists with safe ownership/permissions.
	if _, err := connector.Run(ctx, conn, fmt.Sprintf(
		"mkdir -p %[1]s && chmod 700 %[1]s && chown %[2]s %[1]s",
		connector.ShellQuote(sshDir), connector.ShellQuote(user))); err != nil {
		return nil, fmt.Errorf("failed to prepare .ssh directory: %w", err)
	}

	content := strings.Join(desired, "\n")
	if content != "" {
		content += "\n"
	}
	return module.DeployFile(ctx, conn, module.DeployOpts{
		Content: []byte(content),
		Dest:    path,
		Mode:    "0600",
		Owner:   user,
		Group:   user,
		Label:   "authorized_keys",
	})
}

// Check reports whether Run would change anything.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	user, err := module.RequireString(params, "user")
	if err != nil {
		return nil, err
	}
	keys := module.GetStringSlice(params, "key")
	if len(keys) == 0 {
		return nil, fmt.Errorf("'key' parameter is required")
	}
	state := module.GetString(params, "state", "present")
	exclusive := module.GetBool(params, "exclusive", false)

	home, err := userHome(ctx, conn, user)
	if err != nil {
		return nil, err
	}
	path := module.GetString(params, "path", home+"/.ssh/authorized_keys")

	current := readLines(ctx, conn, path)
	desired := desiredLines(current, keys, state, exclusive)
	if equalLines(current, desired) {
		return module.NoChange("authorized_keys already in desired state"), nil
	}
	return module.WouldChange("authorized_keys would be updated"), nil
}

var _ module.Checker = (*Module)(nil)

// userHome resolves a user's home directory via getent passwd.
func userHome(ctx context.Context, conn connector.Connector, user string) (string, error) {
	res, err := conn.Execute(ctx, "getent passwd "+connector.ShellQuote(user))
	if err != nil {
		return "", fmt.Errorf("failed to look up user %q: %w", user, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("user %q does not exist", user)
	}
	fields := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(fields) < 6 || fields[5] == "" {
		return "", fmt.Errorf("could not determine home directory for %q", user)
	}
	return fields[5], nil
}

// readLines returns the non-empty, non-comment lines of a remote file.
func readLines(ctx context.Context, conn connector.Connector, path string) []string {
	res, err := conn.Execute(ctx, "cat "+connector.ShellQuote(path)+" 2>/dev/null || true")
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		l = strings.TrimRight(l, "\r ")
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

// keyData extracts the base64 body of an SSH public key line, ignoring any
// options prefix and trailing comment. Returns "" if no key token is found.
func keyData(line string) string {
	fields := strings.Fields(line)
	for i, f := range fields {
		if isKeyType(f) && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func isKeyType(s string) bool {
	return strings.HasPrefix(s, "ssh-") ||
		strings.HasPrefix(s, "ecdsa-") ||
		strings.HasPrefix(s, "sk-ssh-") ||
		strings.HasPrefix(s, "sk-ecdsa-")
}

// desiredLines computes the target file contents for the requested state.
func desiredLines(current, keys []string, state string, exclusive bool) []string {
	if state == "present" && exclusive {
		// Exactly the given keys, de-duplicated by key data, in order.
		var out []string
		seen := map[string]bool{}
		for _, k := range keys {
			k = strings.TrimSpace(k)
			d := keyData(k)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, k)
		}
		return out
	}

	// Index the requested keys by their data.
	reqData := map[string]bool{}
	for _, k := range keys {
		if d := keyData(k); d != "" {
			reqData[d] = true
		}
	}

	if state == "absent" {
		var out []string
		for _, l := range current {
			if d := keyData(l); d != "" && reqData[d] {
				continue // drop
			}
			out = append(out, l)
		}
		return out
	}

	// present (non-exclusive): keep current, append any missing requested keys.
	present := map[string]bool{}
	for _, l := range current {
		if d := keyData(l); d != "" {
			present[d] = true
		}
	}
	out := append([]string{}, current...)
	for _, k := range keys {
		k = strings.TrimSpace(k)
		d := keyData(k)
		if d == "" || present[d] {
			continue
		}
		present[d] = true
		out = append(out, k)
	}
	return out
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
