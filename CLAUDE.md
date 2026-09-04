# Tack - System Bootstrapping Tool

## Project Overview
Tack is a Go-based configuration management and system bootstrapping tool inspired by Ansible. It supports local execution, SSH, and AWS SSM connectors.

## Build & Run
- `make build` - Build the binary
- `make test` - Run tests
- `make lint` - Run linter (golangci-lint)
- `go run ./cmd/tack` - Run directly

## Project Structure
- `cmd/tack/` - CLI entrypoint using Cobra
- `internal/connector/` - Connection backends (local, SSH, SSM)
- `internal/module/` - Task modules (apt, brew, file, copy, command)
- `internal/playbook/` - YAML playbook parsing and execution
- `internal/inventory/` - Host inventory management (static YAML, plugin framework)
- `internal/inventory/script/` - Script/executable inventory plugin
- `internal/inventory/http/` - HTTP REST API inventory plugin
- `internal/inventory/ec2/` - AWS EC2 tag-based inventory plugin
- `internal/executor/` - Task orchestration
- `pkg/facts/` - System fact gathering (OS, arch, etc.)

## Key Interfaces
- `Connector` - Abstraction for executing commands on targets
- `Module` - Abstraction for idempotent system operations
- `inventory.Plugin` - Abstraction for dynamic inventory sources (script, http, ec2)

## Design Principles
1. **Idempotency** - All modules must be idempotent
2. **Declarative** - Describe desired state, not imperative steps
3. **Cross-platform** - Support macOS (brew, launchd) and Linux (apt, systemd)
4. **Extensible** - Easy to add new connectors and modules

## Conventions
- Use `internal/` for private packages
- Each module in its own subdirectory under `internal/module/`
- YAML for playbooks and inventory files
- Context for cancellation and timeouts on all operations
- Modules return `(Result, error)` with Changed bool for idempotency tracking

## Dependencies
- `github.com/spf13/cobra` - CLI framework
- `gopkg.in/yaml.v3` - YAML parsing
- `golang.org/x/crypto/ssh` - SSH connector
- `github.com/aws/aws-sdk-go-v2` - AWS SSM connector

<!-- GSD:project-start source:PROJECT.md -->
## Project

**Tack — Encrypted Vault Variables**

Tack is a Go-based configuration management and system bootstrapping tool inspired by Ansible. It supports local execution, SSH, Docker, and AWS SSM connectors with idempotent modules for package management, file operations, templates, and services. This milestone adds encrypted variable files (vault) — allowing users to store secrets in YAML files encrypted with AES-256-GCM and use them transparently in playbooks.

**Core Value:** Secrets must never be stored in plaintext or leak to logs/process listings — encrypted vault variables are decrypted in-memory only, on demand, and merged into play vars seamlessly.

### Constraints

- **Tech stack**: Pure Go, no CGo, no external encryption tools required
- **Security**: Password never stored on disk, vault content never written to temp files in plaintext, memory zeroed after use
- **Compatibility**: Vault file format must be versioned for future algorithm changes
- **UX**: Interactive prompt must work with terminal password masking (already using `golang.org/x/term`)
<!-- GSD:project-end -->

<!-- GSD:stack-start source:codebase/STACK.md -->
## Technology Stack

## Languages
- Go 1.24.0 - All application code (`go.mod` line 3)
- Shell (bash) - Fact-gathering scripts embedded in Go (`pkg/facts/facts.go`), test helpers (`tests/setup-keys.sh`)
- YAML - Playbooks, inventory files, CI/CD configuration
## Runtime
- Go 1.24 (CI uses `GO_VERSION: '1.24'` in `.github/workflows/ci.yaml`)
- CGO disabled for release builds (`CGO_ENABLED=0` in `.goreleaser.yaml`)
- Go modules
- Lockfile: `go.sum` present
## Frameworks
- `github.com/spf13/cobra` v1.8.0 - CLI framework, command hierarchy in `cmd/tack/main.go`
- `gopkg.in/yaml.v3` v3.0.1 - YAML parsing for playbooks and inventory
- Go standard `testing` package
- `github.com/stretchr/testify` v1.11.1 - Test assertions
- `github.com/testcontainers/testcontainers-go` v0.40.0 - Docker-based integration tests
- `make` - Build orchestration (`Makefile`)
- `golangci-lint` v1.64 - Linting (invoked in CI via `golangci/golangci-lint-action@v6`)
- GoReleaser v2 - Release automation (`.goreleaser.yaml`)
## Key Dependencies
- `golang.org/x/crypto` v0.43.0 - SSH client implementation (`internal/connector/ssh/ssh.go`)
- `github.com/aws/aws-sdk-go-v2` v1.41.2 - AWS SSM and S3 integration (`internal/connector/ssm/ssm.go`)
- `github.com/pkg/sftp` v1.13.10 - SFTP file transfers over SSH (`internal/connector/ssh/ssh.go`)
- `github.com/docker/docker` v28.5.1 - Docker API for container management
- `aws-sdk-go-v2/config` v1.32.10 - AWS credential/config loading
- `aws-sdk-go-v2/service/ec2` v1.293.0 - EC2 tag-based instance discovery
- `aws-sdk-go-v2/service/s3` v1.96.2 - S3 file transfer for SSM connector
- `aws-sdk-go-v2/service/ssm` v1.68.1 - Systems Manager command execution and parameter store
- `github.com/kevinburke/ssh_config` v1.6.0 - Parse `~/.ssh/config` for SSH defaults (`internal/connector/ssh/ssh.go`)
- `golang.org/x/term` v0.40.0 - Terminal password prompting (`cmd/tack/main.go`)
- `github.com/spf13/pflag` v1.0.5 - Flag parsing (indirect, via cobra)
## Configuration
- No `.env` files; configuration is via CLI flags and environment variables
- Environment variable prefix: `TACK_` (e.g., `TACK_CONNECTION`, `TACK_SSH_USER`, `TACK_SSH_KEY`, `TACK_SSH_PORT`, `TACK_SSH_PASSWORD`, `TACK_SSH_INSECURE`, `TACK_HOSTS`, `TACK_SSM_INSTANCES`, `TACK_SSM_TAGS`, `TACK_SSM_REGION`, `TACK_SSM_BUCKET`, `TACK_SUDO_PASSWORD`)
- AWS credentials: standard AWS SDK credential chain (env vars, shared config, IAM roles)
- `Makefile` - Primary build entry point
- `.goreleaser.yaml` - Release builds with ldflags for version embedding
- Version injected via ldflags: `-X main.version`, `-X main.commit`, `-X main.date` (see `Makefile` lines 12, `cmd/tack/main.go` lines 36-40)
## Platform Requirements
- Linux amd64, arm64
- macOS (Darwin) amd64, arm64
- Go 1.24+
- `make`
- `golangci-lint` (for linting)
- Docker (for integration tests and `tack test` command)
- `git` (for source fetching feature)
- `aws` CLI (for S3 source fetching via `internal/source/s3.go`)
- Single static binary (CGO_ENABLED=0)
- Homebrew tap: `tackhq/tap/tack` (configured in `.goreleaser.yaml`)
- GitHub Releases via GoReleaser
## CI/CD
- `build` - Compile binary, verify `--version`
- `test` - Unit tests with race detector and coverage (`go test -v -short -race`)
- `lint` - golangci-lint v1.64
- `validate` - Validate example playbooks
- `integration` - Integration tests (`tests/integration/`)
- Triggered on `v*` tags
- Waits for CI to pass, then runs GoReleaser
- Publishes to GitHub Releases and Homebrew tap
- Requires `GITHUB_TOKEN` and `HOMEBREW_TAP_TOKEN` secrets
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

## Naming
- **Packages**: lowercase, single-word (`connector`, `module`, `playbook`, `executor`)
- **Connector implementations**: each in own subpackage (`ssh/`, `ssm/`, `local/`, `docker/`)
- **Module implementations**: each in own subpackage (`apt/`, `brew/`, `file/`, `copy/`, `command/`, `template/`, `systemd/`)
- **Types**: PascalCase, exported (`Connector`, `Module`, `Result`, `Play`, `Task`)
- **Interface implementations**: typically named `Connector` or `Module` within their package (e.g., `ssh.Connector`, `apt.Module`)
- **Test files**: standard `_test.go` suffix, same package
## Error Handling
- Wrap errors with `fmt.Errorf("context: %w", err)` for error chains
- Module `Run()` returns `(*Result, error)` — errors for system failures, Result.Changed for state changes
- Connector `Execute()` returns `(*Result, error)` — non-zero exit codes are not errors; callers check `ExitCode`
- `connector.Run()` helper converts non-zero exit codes to errors for convenience
- Required parameters use `module.RequireString()` which returns descriptive errors
## Module Pattern
## Parameter Handling
- Params are `map[string]any` (YAML-decoded)
- Type-safe extraction via helper functions in `module/params.go`:
- Role-relative paths resolved via `ResolveRolePath()`
## Result Helpers
- `module.Changed(msg)` / `module.Unchanged(msg)` — factory functions
- `module.ChangedWithData(msg, data)` — for registered output
- `module.WouldChange(msg)` / `module.NoChange(msg)` / `module.UncertainChange(msg)` — check mode
## YAML Conventions
- Playbooks are YAML arrays of plays
- Tasks specify module as a top-level key alongside params (shorthand) or via `module:` + `params:` keys
- Shorthand expansion handled by `playbook.ExpandShorthand()`
- `{{ variable }}` syntax for interpolation (not Go templates — custom regex-based)
- `when:` conditions support simple comparisons (`==`, `!=`, `is defined`, `is not defined`, `in`)
## Connector Pattern
- All connectors implement the same interface
- Sudo handled uniformly via `connector.BuildSudoCommand()`
- SSH connector reads `~/.ssh/config` and `known_hosts`
- SSM connector uses AWS SDK v2 with interface-based mocking (`ssmAPI`, `s3API`, `ec2API`)
## Code Organization
- `internal/` for all private packages (standard Go convention)
- `pkg/` for packages potentially reusable outside tack (`facts`, `ssmparams`)
- `cmd/tack/` single CLI binary
- `tests/integration/` for Docker-based integration tests
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

## High-Level Design
```
```
## Key Abstractions
### Connector Interface (`internal/connector/connector.go`)
- `Connect(ctx)` — establish connection
- `Execute(ctx, cmd)` — run command, return stdout/stderr/exit code
- `Upload(ctx, src, dst, mode)` — push file to target
- `Download(ctx, src, dst)` — pull file from target
- `SetSudo(enabled, password)` — configure privilege escalation
- `Close()` — tear down connection
### Module Interface (`internal/module/module.go`)
- `Name()` — unique identifier (e.g., "apt", "file", "copy")
- `Run(ctx, conn, params)` — execute with `map[string]any` params, return `(*Result, error)`
### Source Interface (`internal/source/source.go`)
- `Fetch(ctx)` — returns local path + cleanup function
- Supports: local files, git repos (SSH/HTTPS/GitHub URLs), S3 buckets, HTTP URLs
## Data Flow
## Concurrency Model
## Variable System
- Play-level vars defined in YAML
- Per-host vars from inventory
- System facts gathered at runtime (`pkg/facts`)
- Registered task outputs (`register:` directive)
- Extra vars from CLI (`-e key=value`)
- SSM Parameter Store lookups (`ssm_param()`)
- `{{ variable }}` interpolation via regex replacement
## Plan/Apply Model
<!-- GSD:architecture-end -->

<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd:profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
