# Bolt - System Bootstrapping Tool

## Project Overview
Bolt is a Go-based configuration management and system bootstrapping tool inspired by Ansible. It supports local execution, SSH, and AWS SSM connectors.

## Build & Run
- `make build` - Build the binary
- `make test` - Run tests
- `make lint` - Run linter (golangci-lint)
- `go run ./cmd/bolt` - Run directly

## Project Structure
- `cmd/bolt/` - CLI entrypoint using Cobra
- `internal/connector/` - Connection backends (local, SSH, SSM)
- `internal/module/` - Task modules (apt, brew, file, copy, command)
- `internal/playbook/` - YAML playbook parsing and execution
- `internal/inventory/` - Host inventory management
- `internal/executor/` - Task orchestration
- `pkg/facts/` - System fact gathering (OS, arch, etc.)

## Key Interfaces
- `Connector` - Abstraction for executing commands on targets
- `Module` - Abstraction for idempotent system operations

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
