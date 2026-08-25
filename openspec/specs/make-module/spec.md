# make-module Specification

## Purpose
TBD - created by archiving change source-install-modules. Update Purpose after archive.
## Requirements
### Requirement: Mandatory idempotency guard
The `make` module SHALL require exactly one idempotency guard: `creates` (skip when the path exists on the target) or `unless` (skip when the shell expression exits 0). If neither is provided, the module SHALL return a validation error and run nothing. This prevents rebuilding on every run.

#### Scenario: Missing guard is rejected
- **WHEN** neither `creates` nor `unless` is provided
- **THEN** the module SHALL return an error and execute no build step

#### Scenario: creates satisfied
- **WHEN** `creates: /usr/local/bin/app` is set and that path exists
- **THEN** the module SHALL report `Changed: false` and run no build step

#### Scenario: unless satisfied
- **WHEN** `unless: "test -x /usr/local/bin/app"` exits 0
- **THEN** the module SHALL report `Changed: false` and run no build step

### Requirement: Build execution
When the guard is not satisfied, the `make` module SHALL run, in `chdir`: the optional `configure` command, then `make [target]` (with `-j<jobs>` when `jobs` is set), then the optional install step. It SHALL report `Changed: true` on success.

#### Scenario: Configure, build, install
- **WHEN** `chdir: /usr/local/src/app`, `configure: "./configure --prefix=/usr/local"`, `install: true`, and the guard is unsatisfied
- **THEN** the module SHALL run `./configure ...`, then `make`, then `make install`, and report `Changed: true`

#### Scenario: Build a specific target
- **WHEN** `target: all` and `jobs: 4`
- **THEN** the module SHALL run `make -j4 all`

#### Scenario: Custom install command
- **WHEN** `install: "make DESTDIR=/opt install"`
- **THEN** the module SHALL run that command verbatim as the install step

#### Scenario: Build failure
- **WHEN** any step exits non-zero
- **THEN** the module SHALL return an error surfacing the failing step's output

### Requirement: Environment and working directory
The `make` module SHALL require `chdir` and SHALL export the `env` map (when provided) to all steps.

#### Scenario: Missing chdir is rejected
- **WHEN** `chdir` is not provided
- **THEN** the module SHALL return an error

#### Scenario: Environment applied
- **WHEN** `env: {CFLAGS: "-O2"}` is provided
- **THEN** each build step SHALL run with `CFLAGS=-O2` in its environment

### Requirement: Check mode
The `make` module SHALL support check mode, evaluating the guard and reporting whether it would build, without running any build step.

#### Scenario: Would build in check mode
- **WHEN** running in check mode and the guard is unsatisfied
- **THEN** the module SHALL report it would build and run no step

#### Scenario: No change in check mode
- **WHEN** running in check mode and the guard is satisfied
- **THEN** the module SHALL report no change

