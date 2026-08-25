# get-url-module Specification

## Purpose
TBD - created by archiving change source-install-modules. Update Purpose after archive.
## Requirements
### Requirement: Download a file on the target
The `get_url` module SHALL download the resource at `url` and place it at `dest` on the target. The download SHALL run on the target itself using `curl` (preferred) or `wget`, fetching to a temporary file in `dest`'s directory and atomically moving it into place after verification, so a failed download never leaves a partial or bad file at `dest`.

#### Scenario: Download a new file
- **WHEN** `url` and `dest` are given and `dest` does not exist on the target
- **THEN** the module SHALL fetch the URL on the target, place it at `dest`, and report `Changed: true`

#### Scenario: Download failure
- **WHEN** the URL returns a non-success status or is unreachable
- **THEN** the module SHALL return an error and SHALL NOT create or overwrite `dest`

#### Scenario: No downloader available
- **WHEN** neither `curl` nor `wget` is available on the target
- **THEN** the module SHALL return an error instructing the user to install `curl` or `wget`

### Requirement: Checksum verification
The `get_url` module SHALL verify the downloaded content against `checksum` when provided, in the form `algo:hash` (supporting `sha256`, `sha512`, `sha1`, `md5`; a bare hash is treated as `sha256`). Verification SHALL occur on the target against the temporary file, before it is moved to `dest`. A mismatch SHALL be an error.

#### Scenario: Checksum matches
- **WHEN** `checksum: "sha256:<hash>"` matches the downloaded content
- **THEN** the module SHALL move the file into `dest` and report `Changed: true`

#### Scenario: Checksum mismatch
- **WHEN** the downloaded content does not match `checksum`
- **THEN** the module SHALL discard the temporary file, return an error, and SHALL NOT write `dest`

#### Scenario: Unsupported algorithm
- **WHEN** `checksum` uses an unknown algorithm prefix
- **THEN** the module SHALL return an error

### Requirement: Idempotency
The `get_url` module SHALL avoid re-downloading when the target already has the desired file. When `dest` exists and a `checksum` is given, it SHALL compute the remote digest and report `Changed: false` if it matches. When `dest` exists and no `checksum` is given, it SHALL report `Changed: false` unless `force: true`.

#### Scenario: Existing file matches checksum
- **WHEN** `dest` exists on the target and its digest matches `checksum`
- **THEN** the module SHALL report `Changed: false` and perform no download

#### Scenario: Existing file, no checksum
- **WHEN** `dest` exists and no `checksum` is provided and `force` is not set
- **THEN** the module SHALL report `Changed: false`

#### Scenario: Force re-download
- **WHEN** `force: true`
- **THEN** the module SHALL download and overwrite `dest` and report `Changed: true`

### Requirement: File attributes
The `get_url` module SHALL apply `mode`, `owner`, and `group` to `dest` when provided.

#### Scenario: Apply mode and owner
- **WHEN** `mode: "0755"` and `owner: root` are given
- **THEN** the placed file SHALL have mode 0755 and be owned by root

### Requirement: Check mode
The `get_url` module SHALL support check mode, reporting whether it would download without transferring the file.

#### Scenario: Would download in check mode
- **WHEN** running in check mode and `dest` is absent (or the checksum would not match)
- **THEN** the module SHALL report it would download and make no change

#### Scenario: No change in check mode
- **WHEN** running in check mode and `dest` already satisfies the desired state
- **THEN** the module SHALL report no change

