# unarchive-module Specification

## Purpose
TBD - created by archiving change source-install-modules. Update Purpose after archive.
## Requirements
### Requirement: Extract a local archive
The `unarchive` module SHALL extract the archive at `src` (a path on the target) into the `dest` directory on the target. Extraction SHALL run on the target so ownership, permissions, and symlinks are preserved.

#### Scenario: Extract a tarball
- **WHEN** `src: /tmp/app.tar.gz` and `dest: /opt/app` and `creates` does not yet exist
- **THEN** the module SHALL extract the archive into `/opt/app` and report `Changed: true`

#### Scenario: Missing source archive
- **WHEN** `src` does not exist on the target
- **THEN** the module SHALL return an error

### Requirement: Format detection
The `unarchive` module SHALL select the extraction method from the `src` extension: `.zip` via `unzip`; `.tar`, `.tar.gz`/`.tgz`, `.tar.bz2`, `.tar.xz`, `.tar.zst` via `tar` with the matching decompression flag.

#### Scenario: Zip archive
- **WHEN** `src` ends in `.zip`
- **THEN** the module SHALL extract it using `unzip` into `dest`

#### Scenario: Compressed tar
- **WHEN** `src` ends in `.tar.xz`
- **THEN** the module SHALL extract it using `tar` with xz decompression

#### Scenario: Unsupported extension
- **WHEN** `src` has an unrecognized archive extension
- **THEN** the module SHALL return an error

### Requirement: Required extraction tooling
The `unarchive` module SHALL verify that the needed tool (`tar` or `unzip`) is available on the target using the shared availability helper, surfacing sudo-auth failures rather than reporting a missing tool.

#### Scenario: tar not installed
- **WHEN** extracting a tar archive and `tar` is not available on the target
- **THEN** the module SHALL return an error indicating `tar` must be installed

### Requirement: Idempotency via creates
The `unarchive` module SHALL skip extraction when `creates` is set and that path exists on the target. When `creates` is omitted, it SHALL extract on every run.

#### Scenario: Already extracted
- **WHEN** `creates: /opt/app/bin/app` is set and that path exists
- **THEN** the module SHALL report `Changed: false` and not extract

#### Scenario: Not yet extracted
- **WHEN** `creates` is set and the path does not exist
- **THEN** the module SHALL extract and report `Changed: true`

### Requirement: Extraction options
The `unarchive` module SHALL support `strip_components` (drop leading path components), `extra_opts` (additional flags passed to the extractor), and `owner`/`group`/`mode` applied to `dest`.

#### Scenario: Strip leading directory
- **WHEN** `strip_components: 1`
- **THEN** the module SHALL pass `--strip-components=1` to `tar`

### Requirement: Check mode
The `unarchive` module SHALL support check mode, reporting whether it would extract based on the `creates` guard, without modifying the target.

#### Scenario: Would extract in check mode
- **WHEN** running in check mode and `creates` does not exist
- **THEN** the module SHALL report it would extract and make no change

