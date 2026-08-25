# iptables-module Specification

## Purpose
TBD - created by archiving change iptables-module. Update Purpose after archive.
## Requirements
### Requirement: Ensure a rule is present
The `iptables` module SHALL ensure a single firewall rule exists when `state` is `present` (or omitted, as `present` is the default). It SHALL check for the rule with `iptables -C` before adding it, and SHALL report `Changed: false` when the rule already exists.

#### Scenario: Add a new rule
- **WHEN** `chain: INPUT`, `protocol: tcp`, `destination_port: 22`, `jump: ACCEPT`, `state: present` and the rule does not exist
- **THEN** the module SHALL append the rule and report `Changed: true`

#### Scenario: Rule already present
- **WHEN** the same rule is applied again and it already exists
- **THEN** the module SHALL report `Changed: false` and make no modification

### Requirement: Ensure a rule is absent
The `iptables` module SHALL ensure a rule does not exist when `state` is `absent`. It SHALL check for the rule with `iptables -C` before deleting it, and SHALL report `Changed: false` when the rule is already absent.

#### Scenario: Remove an existing rule
- **WHEN** `state: absent` and the specified rule currently exists
- **THEN** the module SHALL delete the rule with `iptables -D` and report `Changed: true`

#### Scenario: Rule already absent
- **WHEN** `state: absent` and the specified rule does not exist
- **THEN** the module SHALL report `Changed: false`

### Requirement: Append or insert positioning
The `iptables` module SHALL append rules by default (`action: append`) and SHALL insert rules when `action: insert`. When `rule_num` is provided with `action: insert`, the rule SHALL be inserted at that 1-based position.

#### Scenario: Append by default
- **WHEN** `action` is omitted and the rule is new
- **THEN** the module SHALL add the rule with `iptables -A <chain>`

#### Scenario: Insert at the top
- **WHEN** `action: insert` and `rule_num` is omitted and the rule is new
- **THEN** the module SHALL add the rule with `iptables -I <chain>` at position 1

#### Scenario: Insert at a position
- **WHEN** `action: insert`, `rule_num: 3` and the rule is new
- **THEN** the module SHALL add the rule with `iptables -I <chain> 3`

### Requirement: Rule matching fields
The `iptables` module SHALL construct the rule from the provided match fields — `table` (default `filter`), `chain`, `protocol`, `source`, `destination`, `source_port`, `destination_port`, `in_interface`, `out_interface`, `ctstate`, and `jump` — in a fixed canonical order so that the `-C`, add, and delete commands refer to the identical rule.

#### Scenario: Build a rule with multiple match fields
- **WHEN** `chain: INPUT`, `protocol: tcp`, `source: 10.0.0.0/8`, `destination_port: 443`, `ctstate: "NEW,ESTABLISHED"`, `jump: ACCEPT`
- **THEN** the module SHALL build a rule spec containing `-p tcp -s 10.0.0.0/8 --dport 443 -m conntrack --ctstate NEW,ESTABLISHED -j ACCEPT` and use that same spec for check, add, and delete

#### Scenario: Non-default table
- **WHEN** `table: nat`, `chain: POSTROUTING`, `jump: MASQUERADE`
- **THEN** the module SHALL include `-t nat` in every iptables invocation for the rule

#### Scenario: Port without protocol is rejected
- **WHEN** `destination_port: 80` is set but `protocol` is omitted
- **THEN** the module SHALL return an error instructing the user to set `protocol`

### Requirement: Comment tagging
The `iptables` module SHALL add `-m comment --comment <value>` to the rule when `comment` is provided. The comment SHALL be part of the rule spec so that rules differing only by comment are treated as distinct.

#### Scenario: Rule with a comment
- **WHEN** `comment: "allow ssh"` is provided
- **THEN** the module SHALL include `-m comment --comment 'allow ssh'` in the check, add, and delete commands

### Requirement: IPv4 and IPv6 dispatch
The `iptables` module SHALL operate on IPv4 rules via `iptables` by default and on IPv6 rules via `ip6tables` when `ip_version: ipv6`. `ip_version` SHALL default to `ipv4` and SHALL reject any value other than `ipv4` or `ipv6`.

#### Scenario: IPv6 rule
- **WHEN** `ip_version: ipv6`, `chain: INPUT`, `protocol: tcp`, `destination_port: 22`, `jump: ACCEPT`
- **THEN** the module SHALL use `ip6tables` for the check and add commands

#### Scenario: Invalid ip_version
- **WHEN** `ip_version: ipv5`
- **THEN** the module SHALL return an error

### Requirement: Distro-aware reboot persistence
The `iptables` module SHALL persist rules across reboot when `persist: true`, but only after a change is actually made. It SHALL select the persistence mechanism from the target's OS facts (`os_release` `ID` / `ID_LIKE`) so the correct paths and tooling are used per distro family, and SHALL fall back to a warning when no mechanism is available. The module SHALL create the target directory when it does not exist.

#### Scenario: Debian/Ubuntu via netfilter-persistent
- **WHEN** `persist: true`, a change was made, the target is a Debian-family distro, and `netfilter-persistent` is available
- **THEN** the module SHALL run `netfilter-persistent save`

#### Scenario: Debian/Ubuntu save-file fallback
- **WHEN** `persist: true`, a change was made, the target is a Debian-family distro, `netfilter-persistent` is not available, and `ip_version: ipv4`
- **THEN** the module SHALL write `iptables-save` output to `/etc/iptables/rules.v4` (and `rules.v6` for `ip_version: ipv6`)

#### Scenario: RHEL/CentOS/Fedora via sysconfig
- **WHEN** `persist: true`, a change was made, the target is a RHEL-family distro (rhel, centos, fedora, rocky, almalinux), and `ip_version: ipv4`
- **THEN** the module SHALL write `iptables-save` output to `/etc/sysconfig/iptables` (and `/etc/sysconfig/ip6tables` for `ip_version: ipv6`)

#### Scenario: Arch Linux via iptables.rules
- **WHEN** `persist: true`, a change was made, the target is Arch Linux, and `ip_version: ipv4`
- **THEN** the module SHALL write `iptables-save` output to `/etc/iptables/iptables.rules` (and `/etc/iptables/ip6tables.rules` for `ip_version: ipv6`)

#### Scenario: Persist requested but no change made
- **WHEN** `persist: true` and the rule already matched the desired state
- **THEN** the module SHALL report `Changed: false` and SHALL NOT run any persistence step

#### Scenario: Unknown distro with no mechanism available
- **WHEN** `persist: true`, a change was made, the distro family is unknown, and `netfilter-persistent` is not available
- **THEN** the module SHALL apply the rule and include a warning in the result naming the package to install (`iptables-persistent`, `iptables-services`, or the distro `iptables` unit)

### Requirement: Concurrency-safe invocation
The `iptables` module SHALL pass the `-w` (wait for xtables lock) flag to all iptables/ip6tables invocations so concurrent runs do not fail on lock contention.

#### Scenario: Wait for lock
- **WHEN** the module runs any iptables command
- **THEN** the command SHALL include `-w`

### Requirement: Availability and privilege probing
The `iptables` module SHALL verify that the target binary (`iptables` or `ip6tables`) is available using the shared `module.CommandAvailable` helper, so that a sudo authentication failure is surfaced with its real error rather than being reported as a missing binary.

#### Scenario: Binary not installed
- **WHEN** `iptables` is not present on the target
- **THEN** the module SHALL return an error stating iptables is not available

#### Scenario: Sudo authentication failure during probe
- **WHEN** the probe fails because sudo requires a password that was not supplied
- **THEN** the module SHALL surface the sudo error and hint (via `module.CommandAvailable`) rather than reporting a missing binary

### Requirement: Linux-only platform guard
The `iptables` module SHALL run only on Linux targets. On a non-Linux target it SHALL return a clear error indicating the module is Linux-only and SHALL make no changes.

#### Scenario: macOS target
- **WHEN** the target's `os_type` is `Darwin`
- **THEN** the module SHALL return an unsupported-platform error and report no change

### Requirement: Check mode
The `iptables` module SHALL implement check/plan mode, reporting whether it would add or remove a rule without modifying the ruleset.

#### Scenario: Would add a rule in check mode
- **WHEN** running in check mode with `state: present` and the rule does not exist
- **THEN** the module SHALL report it would add the rule and SHALL NOT modify the ruleset

#### Scenario: Would remove a rule in check mode
- **WHEN** running in check mode with `state: absent` and the rule exists
- **THEN** the module SHALL report it would remove the rule and SHALL NOT modify the ruleset

#### Scenario: No change in check mode
- **WHEN** running in check mode and the rule is already in the desired state
- **THEN** the module SHALL report no change

