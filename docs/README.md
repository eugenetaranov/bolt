# Bolt Documentation

Bolt is a Go-based configuration management and system bootstrapping tool inspired by Ansible. It uses simple YAML playbooks to automate system setup and configuration.

## Table of Contents

- [Getting Started](getting-started.md) - Installation and first steps
- [Playbooks](playbooks.md) - Playbook structure and syntax
- [Roles](roles.md) - Reusable role structure
- [Modules](modules.md) - Available modules reference
- [Variables & Facts](variables.md) - Variable interpolation and system facts
- [Connectors](connectors.md) - Connection methods (local, SSH, SSM)

## Quick Example

```yaml
# setup.yaml
name: Setup Development Machine
hosts: localhost
connection: local

tasks:
  - name: Install packages
    brew:
      name:
        - git
        - go
        - ripgrep
      state: present

  - name: Create projects directory
    file:
      path: ~/projects
      state: directory
      mode: "0755"
```

Run it:

```bash
bolt run setup.yaml
```

## Key Features

- **Simple YAML syntax** - Easy to read and write playbooks
- **Idempotent operations** - Safe to run multiple times
- **Cross-platform** - Supports macOS and Linux
- **Multiple connectors** - Local, SSH, and AWS SSM
- **Built-in modules** - Package management, file operations, and more
- **Variable interpolation** - Dynamic configuration with `{{ variables }}`
- **Conditional execution** - Run tasks based on facts or conditions
- **No dependencies** - Single binary, no runtime required

## Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Playbook   │────▶│  Executor   │────▶│  Connector  │
│   (YAML)    │     │             │     │ (local/ssh) │
└─────────────┘     └─────────────┘     └─────────────┘
                           │
                           ▼
                    ┌─────────────┐
                    │   Modules   │
                    │ (apt, brew, │
                    │  file, etc) │
                    └─────────────┘
```

## License

MIT License
