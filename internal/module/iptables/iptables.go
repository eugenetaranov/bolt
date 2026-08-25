// Package iptables provides a module for managing individual iptables and
// ip6tables firewall rules on Linux targets.
package iptables

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

func init() {
	module.Register(&Module{})
}

// Module manages iptables/ip6tables firewall rules.
type Module struct{}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "iptables"
}

// config holds resolved, validated parameters for one invocation.
type config struct {
	binary      string // "iptables" or "ip6tables"
	table       string
	chain       string
	protocol    string
	source      string
	destination string
	sourcePort  string
	destPort    string
	inIface     string
	outIface    string
	ctstate     string
	jump        string
	action      string // "append" or "insert"
	ruleNum     int
	comment     string
	ipVersion   string // "ipv4" or "ipv6"
	state       string // "present" or "absent"
	persist     bool
}

// Run executes the iptables module.
//
// Parameters:
//   - chain (string, required): INPUT, OUTPUT, FORWARD, or a custom chain
//   - table (string): filter, nat, mangle, raw, security (default: filter)
//   - protocol (string): tcp, udp, icmp, all
//   - source (string): source address/CIDR
//   - destination (string): destination address/CIDR
//   - source_port (string|int): source port or range (requires protocol)
//   - destination_port (string|int): destination port or range (requires protocol)
//   - in_interface (string): inbound interface
//   - out_interface (string): outbound interface
//   - ctstate (string): conntrack states, e.g. "NEW,ESTABLISHED"
//   - jump (string): target: ACCEPT, DROP, REJECT, LOG, or a custom chain
//   - action (string): append or insert (default: append)
//   - rule_num (int): insert position (with action: insert)
//   - comment (string): rule comment (tagged via -m comment)
//   - ip_version (string): ipv4 or ipv6 (default: ipv4)
//   - state (string): present or absent (default: present)
//   - persist (bool): persist rules across reboot after a change (default: false)
func (m *Module) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*module.Result, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}

	if err := ensureUsable(ctx, conn, cfg.binary); err != nil {
		return nil, err
	}

	exists, err := ruleExists(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}

	var msg string
	switch cfg.state {
	case "present":
		if exists {
			return module.Unchanged(fmt.Sprintf("rule already present in %s/%s", cfg.table, cfg.chain)), nil
		}
		if _, err := connector.Run(ctx, conn, cfg.command(cfg.addOp(), true)); err != nil {
			return nil, fmt.Errorf("failed to add rule: %w", err)
		}
		msg = fmt.Sprintf("added rule to %s/%s", cfg.table, cfg.chain)
	case "absent":
		if !exists {
			return module.Unchanged(fmt.Sprintf("rule already absent in %s/%s", cfg.table, cfg.chain)), nil
		}
		if _, err := connector.Run(ctx, conn, cfg.command("-D", false)); err != nil {
			return nil, fmt.Errorf("failed to delete rule: %w", err)
		}
		msg = fmt.Sprintf("removed rule from %s/%s", cfg.table, cfg.chain)
	}

	// Persist only after an actual change was made.
	if cfg.persist {
		if warn := persistRules(ctx, conn, cfg); warn != "" {
			msg += "; " + warn
		}
	}

	return module.Changed(msg), nil
}

// Check reports whether the module would add or remove a rule, without
// modifying the ruleset. The iptables -C probe used here is read-only.
func (m *Module) Check(ctx context.Context, conn connector.Connector, params map[string]any) (*module.CheckResult, error) {
	cfg, err := parseConfig(params)
	if err != nil {
		return nil, err
	}
	if err := ensureUsable(ctx, conn, cfg.binary); err != nil {
		return nil, err
	}
	exists, err := ruleExists(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}

	switch cfg.state {
	case "present":
		if exists {
			return module.NoChange(fmt.Sprintf("rule already present in %s/%s", cfg.table, cfg.chain)), nil
		}
		return module.WouldChange(fmt.Sprintf("would add rule to %s/%s", cfg.table, cfg.chain)), nil
	default: // absent
		if !exists {
			return module.NoChange(fmt.Sprintf("rule already absent in %s/%s", cfg.table, cfg.chain)), nil
		}
		return module.WouldChange(fmt.Sprintf("would remove rule from %s/%s", cfg.table, cfg.chain)), nil
	}
}

// parseConfig validates params and returns a resolved config.
func parseConfig(params map[string]any) (config, error) {
	cfg := config{
		table:       module.GetString(params, "table", "filter"),
		chain:       module.GetString(params, "chain", ""),
		protocol:    module.GetString(params, "protocol", ""),
		source:      module.GetString(params, "source", ""),
		destination: module.GetString(params, "destination", ""),
		sourcePort:  scalarString(params, "source_port"),
		destPort:    scalarString(params, "destination_port"),
		inIface:     module.GetString(params, "in_interface", ""),
		outIface:    module.GetString(params, "out_interface", ""),
		ctstate:     module.GetString(params, "ctstate", ""),
		jump:        module.GetString(params, "jump", ""),
		action:      module.GetString(params, "action", "append"),
		ruleNum:     module.GetInt(params, "rule_num", 0),
		comment:     module.GetString(params, "comment", ""),
		ipVersion:   module.GetString(params, "ip_version", "ipv4"),
		state:       module.GetString(params, "state", "present"),
		persist:     module.GetBool(params, "persist", false),
	}

	if cfg.chain == "" {
		return cfg, fmt.Errorf("'chain' parameter is required")
	}

	switch cfg.ipVersion {
	case "ipv4":
		cfg.binary = "iptables"
	case "ipv6":
		cfg.binary = "ip6tables"
	default:
		return cfg, fmt.Errorf("invalid ip_version '%s': must be ipv4 or ipv6", cfg.ipVersion)
	}

	switch cfg.state {
	case "present", "absent":
	default:
		return cfg, fmt.Errorf("invalid state '%s': must be present or absent", cfg.state)
	}

	switch cfg.action {
	case "append", "insert":
	default:
		return cfg, fmt.Errorf("invalid action '%s': must be append or insert", cfg.action)
	}

	switch cfg.table {
	case "filter", "nat", "mangle", "raw", "security":
	default:
		return cfg, fmt.Errorf("invalid table '%s': must be filter, nat, mangle, raw, or security", cfg.table)
	}

	if (cfg.sourcePort != "" || cfg.destPort != "") && cfg.protocol == "" {
		return cfg, fmt.Errorf("'protocol' is required when a port is specified")
	}

	return cfg, nil
}

// addOp returns the iptables operation flag for adding a rule.
func (cfg config) addOp() string {
	if cfg.action == "insert" {
		return "-I"
	}
	return "-A"
}

// matchArgs returns the ordered rule-match arguments (without the operation,
// chain, or insert position) so that -C, -A, -I, and -D refer to the same rule.
func (cfg config) matchArgs() []string {
	var a []string
	add := func(vals ...string) { a = append(a, vals...) }
	if cfg.protocol != "" {
		add("-p", cfg.protocol)
	}
	if cfg.source != "" {
		add("-s", cfg.source)
	}
	if cfg.destination != "" {
		add("-d", cfg.destination)
	}
	if cfg.inIface != "" {
		add("-i", cfg.inIface)
	}
	if cfg.outIface != "" {
		add("-o", cfg.outIface)
	}
	if cfg.sourcePort != "" {
		add("--sport", cfg.sourcePort)
	}
	if cfg.destPort != "" {
		add("--dport", cfg.destPort)
	}
	if cfg.ctstate != "" {
		add("-m", "conntrack", "--ctstate", cfg.ctstate)
	}
	if cfg.comment != "" {
		add("-m", "comment", "--comment", cfg.comment)
	}
	if cfg.jump != "" {
		add("-j", cfg.jump)
	}
	return a
}

// command builds a shell-safe iptables command for the given operation.
// When includePos is true and action is insert with a rule_num, the position
// is included (only meaningful for -I).
func (cfg config) command(op string, includePos bool) string {
	parts := []string{cfg.binary, "-w", "-t", cfg.table, op, cfg.chain}
	if includePos && op == "-I" && cfg.ruleNum > 0 {
		parts = append(parts, strconv.Itoa(cfg.ruleNum))
	}
	parts = append(parts, cfg.matchArgs()...)

	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = connector.ShellQuote(p)
	}
	return strings.Join(quoted, " ")
}

// ruleExists reports whether the rule is present, using iptables -C.
func ruleExists(ctx context.Context, conn connector.Connector, cfg config) (bool, error) {
	res, err := conn.Execute(ctx, cfg.command("-C", false))
	if err != nil {
		return false, fmt.Errorf("failed to check rule: %w", err)
	}
	// -C exits 0 when the rule exists; a non-zero exit means it is absent.
	// Availability/sudo have already been validated by ensureUsable, so a
	// non-zero here is the expected "no matching rule" signal.
	return res.ExitCode == 0, nil
}

// ensureUsable verifies the target is Linux and the iptables binary is usable
// (surfacing sudo-auth failures via module.CommandAvailable).
func ensureUsable(ctx context.Context, conn connector.Connector, binary string) error {
	avail, err := module.CommandAvailable(ctx, conn, binary)
	if err != nil {
		return err
	}
	if avail {
		return nil
	}
	// Binary missing: distinguish a non-Linux target from a genuinely
	// missing package so the error is actionable.
	if linux, derr := targetIsLinux(ctx, conn); derr == nil && !linux {
		return fmt.Errorf("iptables module is Linux-only; target is not a Linux system")
	}
	return fmt.Errorf("%s not found on target; install iptables (Debian/Ubuntu: 'apt install iptables', RHEL: 'yum install iptables')", binary)
}

// targetIsLinux reports whether `uname -s` on the target is Linux.
func targetIsLinux(ctx context.Context, conn connector.Connector) (bool, error) {
	res, err := conn.Execute(ctx, "uname -s")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(res.Stdout), "Linux"), nil
}

// scalarString returns the string form of a param that may be a string or a
// number (YAML decodes bare ports as int).
func scalarString(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// Ensure Module implements the expected interfaces.
var (
	_ module.Module    = (*Module)(nil)
	_ module.Checker   = (*Module)(nil)
	_ module.Describer = (*Module)(nil)
	_ module.Exampler  = (*Module)(nil)
)

// Description returns a short summary of the iptables module.
func (m *Module) Description() string {
	return "Manage individual iptables/ip6tables firewall rules on Linux (idempotent, with optional reboot persistence)."
}

// Parameters returns the parameter documentation for the iptables module.
func (m *Module) Parameters() []module.ParamDoc {
	return []module.ParamDoc{
		{Name: "chain", Type: "string", Required: true, Description: "INPUT, OUTPUT, FORWARD, or a custom chain"},
		{Name: "table", Type: "string", Default: "filter", Description: "filter, nat, mangle, raw, security"},
		{Name: "protocol", Type: "string", Description: "tcp, udp, icmp, all"},
		{Name: "source", Type: "string", Description: "Source address or CIDR"},
		{Name: "destination", Type: "string", Description: "Destination address or CIDR"},
		{Name: "source_port", Type: "string|int", Description: "Source port or range (requires protocol)"},
		{Name: "destination_port", Type: "string|int", Description: "Destination port or range (requires protocol)"},
		{Name: "in_interface", Type: "string", Description: "Inbound interface"},
		{Name: "out_interface", Type: "string", Description: "Outbound interface"},
		{Name: "ctstate", Type: "string", Description: "Conntrack states, e.g. NEW,ESTABLISHED"},
		{Name: "jump", Type: "string", Description: "Target: ACCEPT, DROP, REJECT, LOG, or a custom chain"},
		{Name: "action", Type: "string", Default: "append", Description: "append or insert"},
		{Name: "rule_num", Type: "int", Description: "Insert position (with action: insert)"},
		{Name: "comment", Type: "string", Description: "Rule comment (tagged via -m comment)"},
		{Name: "ip_version", Type: "string", Default: "ipv4", Description: "ipv4 (iptables) or ipv6 (ip6tables)"},
		{Name: "state", Type: "string", Default: "present", Description: "present or absent"},
		{Name: "persist", Type: "bool", Default: "false", Description: "Persist rules across reboot after a change"},
	}
}

// Example returns a usage example for the iptables module.
func (m *Module) Example() string {
	return `- name: Allow SSH
  iptables:
    chain: INPUT
    protocol: tcp
    destination_port: 22
    jump: ACCEPT
    comment: "allow ssh"
    persist: true`
}
