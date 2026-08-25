## Context

Tack modules are idempotent, single-responsibility operations that implement `module.Module` (`Run(ctx, conn, params) (*Result, error)`) and optionally `module.Checker` for plan mode. Firewall management is a gap. iptables is the most universal rule-management CLI on Linux servers and works on both legacy (`iptables-legacy`) and modern nftables-backed systems (via the `iptables-nft` compat shim), so it is the pragmatic first backend.

The hard problems with an iptables module are (a) idempotency — iptables is imperative and ordered, so "ensure this rule exists" must be a check-then-act, and (b) persistence — runtime rules vanish on reboot, which surprises users.

## Goals / Non-Goals

**Goals:**
- Idempotently ensure a single rule is present or absent, with reliable matching.
- Support IPv4 and IPv6 from one module.
- Optionally persist rules across reboot.
- Full check/plan mode support (no mutation).
- Clear, actionable errors (missing binary vs. sudo-auth vs. unsupported platform).

**Non-Goals (v1):**
- Whole-ruleset/declarative policy via `iptables-restore`.
- Default chain policy (`iptables -P`) management.
- `ufw`, `firewalld`, and native `nftables` backends (future separate modules).
- Managing custom chain creation/flush as first-class operations.

## Decisions

### Decision: Per-rule idempotency via `iptables -C`
Build the rule spec once as an ordered argument list, then use it verbatim with `-C` (check), `-A`/`-I` (add), and `-D` (delete). iptables guarantees `-C` matches the exact rule that `-A` would create, so building args once and reusing them is the reliable way to detect existence.
- `state: present` → run `-C`; if it exits 0 the rule exists (`Changed: false`); otherwise `-A` (or `-I <rule_num>`) and report `Changed: true`.
- `state: absent` → run `-C`; if it exits non-zero the rule is absent (`Changed: false`); otherwise `-D` and report `Changed: true`.
- **Alternative considered:** parsing `iptables -S` output and diffing. Rejected — brittle across iptables versions and normalization (e.g. `--dport 22` vs `22`, address CIDR canonicalization). `-C` delegates matching to iptables itself.

### Decision: Comment tagging with `-m comment --comment`
When `comment` is set, append `-m comment --comment <value>` to the rule spec. The comment is part of the match, so `-C` distinguishes two otherwise-identical rules that differ only by comment. Comments make rules traceable in `iptables -S` and are strongly recommended in docs.

### Decision: `ip_version` dispatch
`ip_version: ipv4` (default) → `iptables`; `ipv6` → `ip6tables`. All other logic is identical; only the binary name changes. Persistence writes to `rules.v4` vs `rules.v6` accordingly.

### Decision: Distro-aware persistence strategy (`persist: true`)
Persistence paths and mechanisms differ per distro family, and path-sniffing alone is ambiguous (Debian and Arch both use `/etc/iptables/` but with different filenames). So the module selects the mechanism from the gathered OS facts (`os_release` `ID` / `ID_LIKE`), with a portable fallback. After a successful mutating change:

1. **Debian family** (`ID` ∈ {debian, ubuntu, …} or `ID_LIKE` contains `debian`):
   - If `netfilter-persistent` is available → `netfilter-persistent save` (iptables-persistent; saves both families).
   - Else write `iptables-save`/`ip6tables-save` → `/etc/iptables/rules.v4` / `rules.v6`.
2. **RHEL family** (`ID` ∈ {rhel, centos, fedora, rocky, almalinux, …} or `ID_LIKE` contains `rhel`/`fedora`):
   - Write `iptables-save`/`ip6tables-save` → `/etc/sysconfig/iptables` / `/etc/sysconfig/ip6tables` (the files `iptables-services` restores at boot). If the `iptables`/`ip6tables` service unit exists it will restore these; the module does not enable it (service management stays the operator's job / a `systemd` task).
3. **Arch family** (`ID` = arch or `ID_LIKE` contains `arch`):
   - Write `iptables-save`/`ip6tables-save` → `/etc/iptables/iptables.rules` / `/etc/iptables/ip6tables.rules` (the paths `iptables.service`/`ip6tables.service` restore).
4. **Unknown distro**: try `netfilter-persistent save` if present; otherwise apply the rule and return a **non-fatal warning** in the result naming which package to install (`iptables-persistent`, `iptables-services`, or the distro `iptables` unit).

A single helper (`persist(ctx, conn, facts, ipVersion)`) encapsulates this dispatch and creates the target directory (`/etc/iptables`) when needed. Persistence runs only when a change was actually made; `persist: true` on an unchanged rule is a no-op reporting `Changed: false`.
- **Alternative considered:** sniff by which paths/tools exist, ignoring facts. Rejected — `/etc/iptables/` is shared by Debian and Arch with different filenames, so facts are needed to disambiguate.
- **Alternative considered:** always `netfilter-persistent`. Rejected — absent on RHEL/Arch.

### Decision: Availability & platform checks
- Probe the target binary (`iptables` or `ip6tables`) with `module.CommandAvailable` so a sudo-auth failure surfaces the real error + hint (consistent with the recent apt/brew/yum fix) rather than "iptables not installed".
- Reject non-Linux targets early using gathered facts (`os_type != "Linux"`) with a clear "iptables module is Linux-only" error, so macOS control-host or macOS targets fail fast.

### Decision: Rule-spec argument construction
Assemble args in a fixed canonical order to keep `-C`/`-A`/`-D` identical: `-t <table>`, `<chain positioning>`, `-p <protocol>`, `-s <source>`, `-d <destination>`, `-i <in_interface>`, `-o <out_interface>`, `--sport <source_port>`, `--dport <destination_port>`, `-m conntrack --ctstate <ctstate>`, `-m comment --comment <comment>`, `-j <jump>`. Ports imply `-p`; if a port is given without `protocol`, error asking the user to set `protocol`. Each value is passed as a discrete, shell-quoted argument via `connector.ShellQuote`.

## Risks / Trade-offs

- **Rule ordering with `append`** → Appending an ACCEPT after a broad DROP has no effect. Mitigation: document ordering; support `action: insert` + `rule_num`. The module manages presence, not global ordering — that stays the operator's responsibility (documented).
- **`-C` false negatives from normalization** → e.g. iptables rewrites `--dport 80` internally; but since we use the same spec for `-C` and `-A`, both sides normalize identically. Residual risk on very old iptables that lacked `-C`; documented as requiring iptables ≥ 1.4.11 (universally met on supported distros).
- **Persistence file location varies** → Debian/Ubuntu (`/etc/iptables/rules.v{4,6}`), RHEL family (`/etc/sysconfig/{ip,ip6}tables`), and Arch (`/etc/iptables/{iptables,ip6tables}.rules`) all differ, and `/etc/iptables/` is shared by Debian and Arch with different filenames. Mitigation: dispatch by OS facts (`ID`/`ID_LIKE`) rather than path-sniffing, and for unknown distros surface a warning naming the package to install rather than silently writing an unread file.
- **Concurrent iptables access** → parallel plays could race on the ruleset. Out of scope; iptables itself serializes via `xtables` lock (`-w`); the module SHALL pass `-w` to wait for the lock.
- **Locking the operator out** → a bad SSH DROP rule can sever the connection. Mitigation: documented warning; check/plan mode lets operators preview; not something the module can fully prevent.

## Migration Plan

Purely additive — a new module package registered at init. No changes to existing modules or playbook syntax. Rollback is removing the package/registration. Existing playbooks are unaffected.

## Open Questions

- Should `persist` default eventually flip to `true`? Kept `false` in v1 to avoid surprising writes to `/etc/`.
- Should the module also enable the distro persistence service (`systemctl enable iptables`) when persisting? v1 only writes the save file; enabling the unit stays a separate `systemd` task to keep responsibilities narrow.
