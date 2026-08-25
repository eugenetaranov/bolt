## 1. get_url module

- [x] 1.1 Create `internal/module/geturl/geturl.go`: `Module` (name `get_url`), `init()` registration, `Describer` + `Exampler`
- [x] 1.2 Parse/validate params: `url`, `dest` (both required), `checksum`, `mode`, `owner`, `group`, `force`, `timeout`, `headers`
- [x] 1.3 Parse `checksum` as `algo:hash` (sha256/sha512/sha1/md5; bare = sha256); reject unknown algos
- [x] 1.4 Idempotency: when `dest` exists, compute remote digest (reuse the checksum helper in `internal/module/fileutil.go`); skip on match, or skip on existence when no checksum and not `force`
- [x] 1.5 Probe the target for `curl` then `wget` via `module.CommandAvailable`; error clearly if neither is present
- [x] 1.6 Download on the target to a temp file in `dest`'s directory: `curl -fSL --retry 3 --max-time <timeout> [-H 'K: V'] -o <tmp> <url>` (or `wget` equivalent), shell-quoting all values
- [x] 1.7 Verify the checksum on the target (fileutil helper) against the temp file; on mismatch remove it and error; on success `mv` into `dest` and apply `mode`/`owner`/`group`
- [x] 1.8 Implement `Check` (would-download vs no-change); interface assertions

## 2. unarchive module

- [x] 2.1 Create `internal/module/unarchive/unarchive.go`: `Module` (name `unarchive`), registration, `Describer` + `Exampler`
- [x] 2.2 Parse/validate params: `src`, `dest` (required), `creates`, `owner`, `group`, `mode`, `strip_components`, `extra_opts`
- [x] 2.3 Detect format by extension (.zip / .tar / .tar.gz|.tgz / .tar.bz2 / .tar.xz / .tar.zst); error on unknown
- [x] 2.4 Probe required tool (`tar`/`unzip`) via `module.CommandAvailable`; clear "install X" error
- [x] 2.5 Verify `src` exists; ensure `dest` dir exists (mkdir -p); apply `owner`/`group`/`mode` to `dest`
- [x] 2.6 Idempotency: skip when `creates` is set and exists; extract otherwise
- [x] 2.7 Build and run extraction command on the target: `tar -x <z|j|J|--zstd> -C dest --strip-components=N <extra_opts> -f src`, or `unzip -o src -d dest`; shell-quote all values
- [x] 2.8 Implement `Check` (would-extract based on `creates`); interface assertions

## 3. make module

- [x] 3.1 Create `internal/module/make/make.go`: `Module` (name `make`), registration, `Describer` + `Exampler`
- [x] 3.2 Parse/validate params: `chdir` (required), `configure`, `target`, `install`, `env`, `jobs`, `creates`, `unless`
- [x] 3.3 Require exactly one of `creates`/`unless`; error otherwise. Require `chdir`
- [x] 3.4 Guard evaluation: `creates` path exists → no change; `unless` expr exits 0 → no change
- [x] 3.5 Execute steps in `chdir` with `env` exported: optional `configure`, then `make -j<jobs> <target>`, then install (`true`→`make install`, string→verbatim, omitted→skip); surface the failing step's output on error
- [x] 3.6 Implement `Check` (evaluate guard; would-build vs no-change); interface assertions

## 4. Unit tests

- [x] 4.1 get_url: idempotency (existing+matching checksum → no change; missing → change; force), checksum mismatch/unknown-algo errors, mode/owner application, check mode — with an HTTP test server (`httptest`) and mock connector
- [x] 4.2 unarchive: format→command mapping (each extension), tool-missing error, creates idempotency, strip_components/extra_opts wiring, check mode
- [x] 4.3 make: missing-guard rejection, missing-chdir rejection, guard-satisfied skip (creates + unless), step ordering (configure/make/install), env export, check mode

## 5. Registration & docs

- [x] 5.1 Add blank imports for the three packages in `cmd/tack/main.go`
- [x] 5.2 Verify they appear in `tack modules` and render schema+example via `tack module <name>` (guarded by `TestAllModulesDocumented`)
- [x] 5.3 README: add `get_url`, `unarchive`, `make` to the feature list and module table
- [x] 5.4 llms.txt: add reference entries (param tables + examples) for all three
- [x] 5.5 examples: add `examples/build-from-source.yaml` showing get_url → unarchive → make, and note the build-once-deploy-artifact guidance for fleets

## 6. Integration test

- [x] 6.1 Add a Docker integration test (`tests/integration/`) exercising the pipeline: `get_url` a small tarball from a local `httptest`/in-container source, `unarchive` it, then `make` a trivial project (guarded by `creates`); assert idempotent re-run and the built artifact exists
- [x] 6.2 Guard for required tooling (`tar`, `make`, a compiler) — install best-effort or skip gracefully if unavailable

## 7. Verification

- [x] 7.1 `make build`, `make test`, `make lint` pass
- [x] 7.2 `openspec validate source-install-modules --strict` passes
- [ ] 7.3 Manual end-to-end: fetch a real release tarball, unarchive, and build a small autotools/make project on a Linux host; confirm re-run reports no change
