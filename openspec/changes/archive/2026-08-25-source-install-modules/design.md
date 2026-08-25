## Context

Tack modules implement `module.Module` (`Run`) and optionally `module.Checker`. Connectors expose `Execute` (run a command) and `Upload`/`Download` (transfer a file) uniformly across local/ssh/ssm/docker. The `copy` and `template` modules already push content to targets via `Upload`. There is a remote-checksum helper in `internal/module/fileutil.go` (uses `sha256sum`, falling back to `shasum`). These three modules fill the fetch → unpack → build gap while reusing that surface.

## Goals / Non-Goals

**Goals:**
- Idempotent, check-mode-aware `get_url`, `unarchive`, and `make`.
- Zero mandatory target tooling for `get_url` (works on minimal targets).
- Pure-Go archive handling for the common formats.
- `make` that is safe by construction — it cannot rebuild unconditionally.

**Non-Goals:**
- `unarchive` fetching from URLs (compose with `get_url`).
- Installing build toolchains/deps (use `apt`/`yum`).
- Version resolution / upgrade semantics for source builds.
- Windows targets.

## Decisions

### Decision: `get_url` downloads on the target, directly to the destination host
The download runs on the target itself (where the file will be used), not through the control host. The module shells to `curl` (preferred) or `wget`, whichever is available, fetching to a temp path in `dest`'s directory, verifying the checksum on the target, then atomically moving it into place. Rationale:
- The bytes never make a round-trip through the controller — efficient for large artifacts and matches user expectation that the file lands where it's executed.
- Consistent with how the other modules operate (commands on the target).
- Download-to-temp + verify + `mv` means a failed/corrupt fetch never leaves a bad file at `dest`.

Command shape: `curl -fSL --retry 3 --max-time <timeout> [-H 'K: V' ...] -o <tmp> <url>` (or `wget -q -T <timeout> [--header ...] -O <tmp> <url>`), then remote checksum, then `mv <tmp> <dest>` + chmod/chown.

Trade-off: the target must have `curl` or `wget`. Probe with `module.CommandAvailable` (preserving the sudo-auth hint) and error clearly ("install curl or wget") when neither exists.
- **Alternative considered:** controller-side `net/http` fetch + `connector.Upload`. Rejected — routes large files through the controller and downloads to a place other than where they're used; a future `via_controller: true` fallback could serve truly minimal targets if needed.

### Decision: `get_url` idempotency via target-side checksum
- If `dest` exists on the target and a `checksum` is given, compute the remote digest (reuse the `fileutil.go` helper) and skip when it matches.
- If `dest` exists and no `checksum` is given, skip unless `force: true` (existence-based).
- `force: true` always re-downloads.
This keeps re-runs cheap: with a checksum, no download happens when the file is already correct. The existence/digest check runs before any fetch, so an already-correct file triggers no network I/O.

### Decision: `unarchive` is local-source only, pure-Go where possible
`src` is a path already on the target. Format is detected by extension:
- `.zip` → `archive/zip`; `.tar`, `.tar.gz`/`.tgz` → `archive/tar` (+ `compress/gzip`) — all pure Go, extracted by streaming the archive from the target via `connector.Download` and writing entries back with `Upload`? No — extraction must happen **on the target**. So pure-Go extraction runs by shelling `tar`/`unzip` on the target after all. Revised below.

**Revised decision:** extraction happens on the target by invoking `tar`/`unzip` there, because the files must land on the target filesystem with correct ownership/paths. Pure-Go stdlib would require reading the archive to the controller and re-uploading every entry — slow and lossy for permissions/symlinks. So:
- Require `tar` (tar family) or `unzip` (zip) on the target; probe with `module.CommandAvailable` and surface a clear "install tar/unzip" error (with sudo-hint behavior preserved).
- Command: `tar -x` with `-z/-j/-J/--zstd` chosen by extension, `-C dest`, `--strip-components=N`, plus `extra_opts`; or `unzip -o src -d dest`.
This keeps ownership/symlinks/perms correct and handles every compression variant the target's `tar` supports.
- **Alternative considered:** pure-Go extraction on the controller + re-upload. Rejected — loses perms/symlinks, doubles transfer, and can't stream to arbitrary target paths cleanly.

### Decision: `unarchive` idempotency via `creates`
Extraction is inherently non-idempotent, so a `creates` marker (a path that exists after a successful extract) is the idempotency signal: skip when it exists. When `creates` is omitted, the module extracts every run (documented; report `Changed: true`). Check mode reports would-extract based on `creates`.

### Decision: `make` requires an explicit idempotency guard
`make` runs `[configure] && make [target] [&& install]` in `chdir`. It **requires** exactly one of `creates` (skip if path exists) or `unless` (skip if the shell expression exits 0). Without a guard it returns a validation error. This is the single rule that separates `make` from `command` and prevents rebuild-every-run. Steps:
- `configure`: when a string, run it verbatim; when omitted, skip.
- build: `make -j<jobs> <target>` (jobs defaults to nproc-derived or unset).
- `install`: `true` → `make install`; a string → run it; omitted → skip.
- `env` map is exported for all steps.
Commands run under the connector's privilege context; `install` to system paths needs sudo like any other privileged task.

### Decision: registration & docs
All three register via `init()` and implement `Describer` + `Exampler`, so `tack module <name>` shows schema + sample (consistent with the recent module-docs work). `TestAllModulesDocumented` already enforces this.

## Risks / Trade-offs

- **`curl`/`wget` absent on the target** → `get_url` fails. Mitigation: probe both via `module.CommandAvailable` and give a clear "install curl or wget" error; a `via_controller` fallback is a possible follow-up. (`curl` or `wget` is present on all mainstream distros/images.)
- **`tar`/`unzip` absence on minimal targets** → `unarchive` fails. Mitigation: clear error via `module.CommandAvailable`; `tar` is near-universal.
- **`make` misuse (no guard)** → would rebuild every run. Mitigation: guard is mandatory (validation error otherwise).
- **On-target compilation is a fleet anti-pattern** → toolchains on prod hosts, non-reproducible. Mitigation: docs frame `make` as a homelab/one-host convenience and recommend build-once-deploy-artifact (`get_url` + `apt: deb:`) for fleets.
- **Checksum formats** → support `sha256:`/`sha512:`/`sha1:`/`md5:` prefixes; default bare hash to sha256. Reject unknown algos.

## Migration Plan

Purely additive: three new module packages + registrations. No changes to existing modules or playbook syntax. Rollback = remove the packages/registrations.

## Open Questions

- Should `get_url` gain a `via_controller: true` fallback (controller `net/http` fetch + upload) for targets lacking both `curl` and `wget`? Deferred until a real need appears.
- Should `unarchive` auto-`get_url` when `src` is a URL? Kept as a non-goal to keep modules single-purpose; revisit if the compose pattern proves noisy.
