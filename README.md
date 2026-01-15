# Bolt

A Go-based configuration management and system bootstrapping tool inspired by Ansible.

Bolt uses simple YAML playbooks to automate system setup and configuration on macOS and Linux systems. It's distributed as a single binary with no runtime dependencies.

## Installation

**Quick install** (requires Go 1.21+):

```bash
curl -fsSL https://raw.githubusercontent.com/eugenetaranov/bolt/main/install.sh | bash
```

Or build manually:

```bash
git clone https://github.com/eugenetaranov/bolt.git
cd bolt
make build
sudo make install
```

## Features

- **Simple YAML playbooks** - Declarative configuration with familiar syntax
- **Idempotent operations** - Safe to run multiple times
- **Cross-platform** - Supports macOS and Linux
- **Multiple connectors** - Local, Docker, SSH (planned), AWS SSM (planned)
- **Built-in modules** - Package management, file operations, commands
- **Variable interpolation** - Dynamic configuration with `{{ variables }}`
- **System facts** - Auto-detected OS, architecture, and environment info
- **No dependencies** - Single static binary

## Quick Start

**Try it now** (after cloning):

```bash
# Build and run example playbook
make build && ./bin/bolt run examples/playbooks/setup-dev.yaml --dry-run
```

**Try with Docker:**

```bash
# Build, start container, run playbook, cleanup
make build && \
docker run -d --name bolt-test alpine sleep 3600 && \
./bin/bolt run examples/playbooks/docker-test.yaml && \
docker rm -f bolt-test
```

**CLI usage:**

```bash
# Run a playbook
bolt run playbook.yaml

# Dry run (see what would happen)
bolt run playbook.yaml --dry-run

# Validate syntax without running
bolt validate playbook.yaml

# List available modules
bolt modules
```

## Examples

### Local Machine Setup

```yaml
name: Setup Development Machine
hosts: localhost
connection: local

vars:
  projects_dir: "{{ env.HOME }}/projects"

tasks:
  - name: Install packages
    brew:
      name: [git, go, ripgrep]
      state: present
    when: facts.os_family == 'Darwin'

  - name: Create projects directory
    file:
      path: "{{ projects_dir }}"
      state: directory
      mode: "0755"
```

### Docker Container

```yaml
name: Configure Container
hosts: my-container
connection: docker

tasks:
  - name: Install packages
    command:
      cmd: apt-get update && apt-get install -y curl vim

  - name: Create app directory
    file:
      path: /app
      state: directory
      mode: "0755"

  - name: Copy config file
    copy:
      dest: /app/config.yaml
      content: |
        server:
          port: 8080
        logging:
          level: info
```

```bash
# Start container, run playbook
docker run -d --name my-container ubuntu:22.04 sleep 600
bolt run container-setup.yaml
```

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Installation and first steps |
| [Playbooks](docs/playbooks.md) | Playbook structure, tasks, handlers, loops |
| [Modules](docs/modules.md) | Available modules reference |
| [Variables & Facts](docs/variables.md) | Variable interpolation and system facts |
| [Connectors](docs/connectors.md) | Connection methods (local, Docker, SSH, SSM) |

## Available Modules

| Module | Description |
|--------|-------------|
| `apt` | Manage packages on Debian/Ubuntu |
| `brew` | Manage Homebrew packages on macOS |
| `command` | Execute shell commands |
| `copy` | Copy files or write content |
| `file` | Manage files, directories, and symlinks |

## Project Structure

```
bolt/
├── cmd/bolt/           # CLI entrypoint
├── internal/
│   ├── connector/      # Connection backends (local, docker, ssh, ssm)
│   ├── executor/       # Playbook execution engine
│   ├── module/         # Task modules (apt, brew, file, etc.)
│   ├── output/         # Formatted terminal output
│   └── playbook/       # YAML parsing
├── pkg/facts/          # System fact gathering
├── docs/               # Documentation
└── examples/           # Example playbooks
```

## Development

```bash
# Build for current platform
make build

# Build for all platforms (cross-compile)
make build-all

# Run tests
make test

# Run linter
make lint
```

## Requirements

- **Running**: macOS or Linux
- **Building**: Go 1.21+

## License

MIT License
