package iptables

import (
	"context"
	"fmt"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

// persistRules saves the current ruleset so it survives a reboot, selecting the
// mechanism from the target's distro family. It returns a non-empty warning
// string when the rules were applied but could not be fully persisted; it never
// fails the task solely for lack of persistence.
func persistRules(ctx context.Context, conn connector.Connector, cfg config) string {
	saveCmd := "iptables-save"
	if cfg.ipVersion == "ipv6" {
		saveCmd = "ip6tables-save"
	}

	switch detectDistroFamily(ctx, conn) {
	case "debian":
		if warn, done := tryNetfilterPersistent(ctx, conn); done {
			return warn
		}
		path := "/etc/iptables/rules.v4"
		if cfg.ipVersion == "ipv6" {
			path = "/etc/iptables/rules.v6"
		}
		return saveToFile(ctx, conn, saveCmd, path)

	case "rhel":
		path := "/etc/sysconfig/iptables"
		if cfg.ipVersion == "ipv6" {
			path = "/etc/sysconfig/ip6tables"
		}
		return saveToFile(ctx, conn, saveCmd, path)

	case "arch":
		path := "/etc/iptables/iptables.rules"
		if cfg.ipVersion == "ipv6" {
			path = "/etc/iptables/ip6tables.rules"
		}
		return saveToFile(ctx, conn, saveCmd, path)

	default:
		if warn, done := tryNetfilterPersistent(ctx, conn); done {
			return warn
		}
		return "warning: rule applied but not persisted (unknown distro; install iptables-persistent, iptables-services, or enable the distro iptables service)"
	}
}

// tryNetfilterPersistent runs `netfilter-persistent save` when available.
// The second return value reports whether netfilter-persistent handled
// persistence (so the caller should not fall through to a save file).
func tryNetfilterPersistent(ctx context.Context, conn connector.Connector) (string, bool) {
	avail, err := module.CommandAvailable(ctx, conn, "netfilter-persistent")
	if err != nil || !avail {
		return "", false
	}
	if _, err := connector.Run(ctx, conn, "netfilter-persistent save"); err != nil {
		return fmt.Sprintf("warning: 'netfilter-persistent save' failed: %v", err), true
	}
	return "", true
}

// saveToFile writes `iptables-save`/`ip6tables-save` output to path, creating
// the parent directory when needed. The redirection runs under the connector's
// privilege context (sudo when enabled), so writes to /etc succeed.
func saveToFile(ctx context.Context, conn connector.Connector, saveCmd, path string) string {
	dir := path
	if i := strings.LastIndex(path, "/"); i > 0 {
		dir = path[:i]
	}
	cmd := fmt.Sprintf("mkdir -p %s && %s > %s",
		connector.ShellQuote(dir), saveCmd, connector.ShellQuote(path))
	if _, err := connector.Run(ctx, conn, cmd); err != nil {
		return fmt.Sprintf("warning: failed to persist rules to %s: %v", path, err)
	}
	return ""
}

// detectDistroFamily classifies the target as debian, rhel, arch, or unknown
// from /etc/os-release (ID / ID_LIKE).
func detectDistroFamily(ctx context.Context, conn connector.Connector) string {
	res, err := conn.Execute(ctx, "cat /etc/os-release 2>/dev/null")
	if err != nil || res.ExitCode != 0 {
		return "unknown"
	}
	return classifyDistro(res.Stdout)
}

// classifyDistro maps os-release ID/ID_LIKE tokens to a family.
func classifyDistro(osRelease string) string {
	tokens := map[string]bool{}
	for _, line := range strings.Split(osRelease, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			tokens[strings.ToLower(unquote(v))] = true
		}
		if v, ok := strings.CutPrefix(line, "ID_LIKE="); ok {
			for _, t := range strings.Fields(strings.ToLower(unquote(v))) {
				tokens[t] = true
			}
		}
	}

	has := func(names ...string) bool {
		for _, n := range names {
			if tokens[n] {
				return true
			}
		}
		return false
	}

	switch {
	case has("debian", "ubuntu", "raspbian", "linuxmint", "pop", "devuan"):
		return "debian"
	case has("rhel", "fedora", "centos", "rocky", "almalinux", "amzn", "ol"):
		return "rhel"
	case has("arch", "manjaro", "endeavouros"):
		return "arch"
	default:
		return "unknown"
	}
}

// unquote strips surrounding single or double quotes from an os-release value.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
