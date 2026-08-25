## Why

Installing software that isn't in a package repository — release tarballs, static binaries, `.deb`s from another distro, or projects built from source — is awkward in Tack today. Users fall back to raw `command` tasks with hand-rolled `curl`/`tar`/`configure && make` shell, which is non-idempotent, skips checksum verification, and loses check-mode. Filling the fetch → unpack → build pipeline with first-class, idempotent modules makes these workflows declarative and safe, and composes with the existing `apt: { deb: ... }`, `command`, and `git` modules.

## What Changes

- Add a **`get_url`** module: download a file **on the target, directly to `dest`** (via `curl`/`wget`), with checksum verification and idempotency.
  - Params: `url` (required), `dest` (required), `checksum` ("algo:hash"), `mode`, `owner`, `group`, `force`, `timeout`, `headers`.
  - Idempotent: if `dest` exists and (when `checksum` given) matches, report no change; otherwise download. `force` re-downloads unconditionally.
  - Downloads to a temp file in `dest`'s directory, verifies the checksum on the target, then atomically moves it into place — a failed fetch never leaves a bad file at `dest`.
- Add an **`unarchive`** module: extract a local archive on the target into a directory, idempotently.
  - Params: `src` (required, path on target), `dest` (required directory), `creates` (marker path for idempotency), `owner`, `group`, `mode`, `strip_components`, `extra_opts`.
  - Extraction runs **on the target** via `tar`/`unzip` (so ownership, permissions, and symlinks are preserved); formats detected by extension: `.zip`, `.tar`, `.tar.gz`/`.tgz`, `.tar.bz2`, `.tar.xz`, `.tar.zst`.
  - Idempotent: skip when `creates` exists.
- Add a **`make`** module: run a build (configure/make/install) in a source directory, guarded for idempotency.
  - Params: `chdir` (required), `configure`, `target`, `install` (bool or command), `env`, `jobs`, and `creates` **or** `unless` (one required) as the idempotency guard.
  - Refuses to run unconditionally: without a guard it errors, so it never rebuilds on every run.
- Register all three in `cmd/tack` so they appear in `tack modules` / `tack module <name>`, each with `Describer` + `Exampler` docs.
- Documentation: README module table, `llms.txt` entries, and an `examples/` playbook showing the fetch → unpack → build pipeline.

Non-goals (this change): remote-URL fetching inside `unarchive` (compose `get_url` + `unarchive`); dependency/toolchain installation (use `apt`/`yum`); package-manager-style version resolution for source builds; Windows.

## Capabilities

### New Capabilities
- `get-url-module`: Idempotent file download to the target with optional checksum verification.
- `unarchive-module`: Idempotent extraction of local archives (tar family + zip) into a directory.
- `make-module`: Guarded build-from-source execution (configure/make/install) with mandatory idempotency guard.

### Modified Capabilities

None.

## Impact

- New packages: `internal/module/geturl/`, `internal/module/unarchive/`, `internal/module/make/` (each with tests), registered via `init()`.
- Reuses `module` param helpers, the remote-checksum helper in `internal/module/fileutil.go`, `module.CommandAvailable`, `connector.ShellQuote`, and the connector `Execute` surface.
- No new external Go dependencies. Work happens on the target via its own tools: `get_url` uses `curl`/`wget`; `unarchive` uses `tar`/`unzip`; `make` uses the target's build toolchain.
- `get_url` downloads on the target (like other modules operate there), so the target needs `curl` or `wget`; the control host does not proxy the bytes.
- Docs: README, `llms.txt`, example playbook.
- Tests: unit tests per module (mock connector) plus a Docker integration test exercising the fetch → unpack → build pipeline.
