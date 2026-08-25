## Why

Tack can install packages, manage files, and run services, but has no way to manage host firewall rules — a fundamental part of bootstrapping a secure system. Operators today must drop to `command` tasks with hand-rolled `iptables -C || iptables -A` shell logic, which is error-prone, non-idempotent, and loses check-mode/diff support. A first-class `iptables` module lets playbooks declare firewall rules idempotently on any Linux target.

## What Changes

- Add a new `iptables` module that ensures a single firewall rule is present or absent, mirroring how `apt`/`brew`/`yum` are separate modules rather than one abstraction.
- Idempotency via `iptables -C` (check) before `-A`/`-I`/`-D`; rules are tagged with `-m comment --comment` for traceability and reliable matching.
- IPv4 and IPv6 support through `ip_version: ipv4|ipv6` (default `ipv4`), dispatching to `iptables` vs `ip6tables`.
- Optional reboot persistence via `persist: true` — prefer `netfilter-persistent save`, falling back to writing `iptables-save`/`ip6tables-save` output to `/etc/iptables/rules.v4` / `rules.v6`, handling distro differences.
- Check/plan mode support (`Check`) so `tack plan` reports would-add / would-remove without mutating rules.
- Availability probing through the shared `module.CommandAvailable` helper so a sudo-auth failure surfaces correctly instead of being mislabeled as "iptables not installed".
- Linux-only: on non-Linux targets (e.g. macOS) the module errors clearly with an unsupported-platform message.
- Parameters: `chain`, `table` (filter/nat/mangle, default filter), `protocol`, `source`, `destination`, `source_port`, `destination_port`, `in_interface`, `out_interface`, `ctstate`, `jump` (target), `action` (append/insert, default append), `rule_num`, `comment`, `ip_version`, `state` (present/absent), `persist`.
- Documentation: README module entry, `llms.txt` entry, and an example playbook snippet.
- Explicitly out of scope for v1: whole-ruleset/declarative policy loading (`iptables-restore`), default chain policy management (`-P`), `ufw`/`firewalld`/native `nftables` backends, and NAT convenience shorthands beyond raw `table: nat` rules.

## Capabilities

### New Capabilities
- `iptables-module`: Idempotent management of individual iptables/ip6tables firewall rules (present/absent) with comment tagging, IPv4/IPv6 dispatch, optional reboot persistence, and check-mode support.

### Modified Capabilities

None.

## Impact

- New package: `internal/module/iptables/` (module implementation + tests), registered via `init()` like other modules.
- Reuses `module.CommandAvailable` (`internal/module/probe.go`), `module` param helpers (`internal/module/params.go`), and `connector.ShellQuote`.
- No new external Go dependencies.
- Requires root/sudo on the target (rules and persistence are privileged operations).
- Docs: README module list, `llms.txt`, example playbook.
- Tests: unit tests with a mock connector for command construction and idempotency logic; optional Docker integration test under `tests/integration/`.
