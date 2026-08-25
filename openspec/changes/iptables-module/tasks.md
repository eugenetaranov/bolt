## 1. Package scaffold

- [ ] 1.1 Create `internal/module/iptables/iptables.go` with a `Module` type, `Name() string` returning `"iptables"`, and `init()` registering it via `module.Register`
- [ ] 1.2 Add `Description()` and `Parameters()` (module.Describer) documenting all params: chain, table, protocol, source, destination, source_port, destination_port, in_interface, out_interface, ctstate, jump, action, rule_num, comment, ip_version, state, persist

## 2. Parameter parsing & validation

- [ ] 2.1 Parse params via `module` helpers; default `table=filter`, `action=append`, `ip_version=ipv4`, `state=present`, `persist=false`
- [ ] 2.2 Validate `state` (present/absent), `action` (append/insert), `ip_version` (ipv4/ipv6); return descriptive errors on invalid values
- [ ] 2.3 Require `chain`; error if a port is set without `protocol`
- [ ] 2.4 Resolve binary name: `iptables` for ipv4, `ip6tables` for ipv6

## 3. Rule spec construction

- [ ] 3.1 Implement `buildRuleSpec(params)` returning the ordered match args (`-p`, `-s`, `-d`, `-i`, `-o`, `--sport`, `--dport`, `-m conntrack --ctstate`, `-m comment --comment`, `-j`), each value passed through `connector.ShellQuote`
- [ ] 3.2 Ensure the same rule spec is reused for check (`-C`), add (`-A`/`-I`), and delete (`-D`); include `-t <table>` and `-w` on every invocation

## 4. Platform & availability guards

- [ ] 4.1 Reject non-Linux targets using gathered facts (`os_type != "Linux"`) with a clear Linux-only error
- [ ] 4.2 Probe the resolved binary with `module.CommandAvailable`; surface its error (missing binary vs. sudo-auth hint) unchanged

## 5. Present / absent logic

- [ ] 5.1 `state: present` — run `-C`; if present report `Changed:false`, else run `-A` (or `-I <chain> [rule_num]` when `action:insert`) and report `Changed:true`
- [ ] 5.2 `state: absent` — run `-C`; if absent report `Changed:false`, else run `-D` and report `Changed:true`
- [ ] 5.3 Distinguish `-C` "rule absent" (expected non-zero exit) from real command errors (surface the latter)

## 6. Persistence (distro-aware)

- [ ] 6.1 Add a distro-family classifier from facts `os_release` `ID`/`ID_LIKE` → {debian, rhel, arch, unknown}
- [ ] 6.2 Implement `persist(ctx, conn, facts, ipVersion)` dispatching on family; run only when a change was made; create target dir (`/etc/iptables`) as needed
- [ ] 6.3 Debian family: prefer `netfilter-persistent save` (probe via `module.CommandAvailable`); else write `iptables-save`/`ip6tables-save` → `/etc/iptables/rules.v4` / `rules.v6`
- [ ] 6.4 RHEL family (rhel/centos/fedora/rocky/almalinux): write `iptables-save`/`ip6tables-save` → `/etc/sysconfig/iptables` / `/etc/sysconfig/ip6tables`
- [ ] 6.5 Arch family: write `iptables-save`/`ip6tables-save` → `/etc/iptables/iptables.rules` / `/etc/iptables/ip6tables.rules`
- [ ] 6.6 Unknown family: try `netfilter-persistent`, else apply the rule and append a warning naming the package to install (`iptables-persistent` / `iptables-services` / distro `iptables` unit); never fail solely for lack of persistence
- [ ] 6.7 Skip persistence entirely when no change was made

## 7. Check mode

- [ ] 7.1 Implement `Check` (module.Checker): run `-C` read-only and return WouldChange (add/remove) or NoChange without mutating the ruleset
- [ ] 7.2 Add interface assertions: `var _ module.Module`, `module.Checker`, `module.Describer`

## 8. Tests

- [ ] 8.1 Unit tests with a mock connector (pattern from `internal/module/yum/yum_test.go`): rule-spec construction for representative param sets (tcp dport, source CIDR, ctstate, comment, nat table, ipv6)
- [ ] 8.2 Idempotency tests: `-C` exit 0 → `Changed:false`; `-C` non-zero → add and `Changed:true`; absent symmetry
- [ ] 8.3 Validation error tests: bad state/action/ip_version, port-without-protocol, non-Linux target
- [ ] 8.4 Persistence unit tests (mock connector + injected facts): Debian netfilter-persistent path, Debian save-file fallback, RHEL sysconfig path, Arch iptables.rules path, unknown-distro warning, no-change skip
- [ ] 8.5 Check-mode tests: would-add, would-remove, no-change

## 9. Cross-distro integration tests

- [ ] 9.1 Add a table-driven testcontainers suite under `tests/integration/` (build-tagged, `-short` skips) parameterized over distro images
- [ ] 9.2 Ubuntu (`ubuntu:24.04`) — install iptables, apply a rule, assert via `iptables -S`, re-run for idempotency, remove, and verify `persist:true` writes `/etc/iptables/rules.v4`
- [ ] 9.3 Debian (`debian:12`) — same assertions as Ubuntu
- [ ] 9.4 Fedora (`fedora:latest`) — install iptables, apply/idempotent/remove, verify `persist:true` writes `/etc/sysconfig/iptables`
- [ ] 9.5 CentOS Stream (`quay.io/centos/centos:stream9`) — same as Fedora; guard the test to skip gracefully if the image cannot be pulled (CentOS Linux is EOL — use Stream, and `t.Skip` on pull failure so CI stays green)
- [ ] 9.6 Arch Linux (`archlinux:latest`) — install iptables, apply/idempotent/remove, verify `persist:true` writes `/etc/iptables/iptables.rules`
- [ ] 9.7 Include one IPv6 case (`ip_version: ipv6`) on at least one distro asserting via `ip6tables -S`

## 10. Documentation

- [ ] 10.1 Add `iptables` to the module list in `README.md` with an example (allow SSH with comment; persist)
- [ ] 10.2 Add an `llms.txt` entry
- [ ] 10.3 Add an example playbook snippet (e.g. `examples/`) demonstrating present/absent, insert, ipv6, and persist

## 11. Verification

- [ ] 11.1 `make build`, `make test`, `make lint` all pass
- [ ] 11.2 `openspec validate iptables-module --strict` passes
- [ ] 11.3 Manual end-to-end against a Linux host: apply a rule (verify with `iptables -S`), re-run (Changed:false), remove it, and confirm `persist:true` survives a simulated `iptables-restore`
