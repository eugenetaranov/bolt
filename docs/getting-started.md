# Getting Started

This guide will help you install Bolt and run your first playbook.

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/eugenetaranov/bolt.git
cd bolt

# Build
make build

# Install (optional)
sudo make install
```

### Binary Location

After building, the binary is located at `./bin/bolt`. You can either:
- Add `./bin` to your PATH
- Copy the binary to `/usr/local/bin` with `make install`
- Run directly with `./bin/bolt`

## Verify Installation

```bash
bolt --version
# bolt version dev (commit: abc123, built: 2024-01-15T10:00:00Z)

bolt modules
# Available modules:
#   - apt
#   - brew
#   - command
#   - copy
#   - file
```

## Your First Playbook

Create a file named `hello.yaml`:

```yaml
name: Hello Bolt
hosts: localhost
connection: local
gather_facts: true

tasks:
  - name: Show system info
    command:
      cmd: echo "Hello from {{ facts.os_type }} on {{ facts.architecture }}"
    register: result

  - name: Create a test file
    file:
      path: /tmp/bolt-test
      state: touch

  - name: Write content to file
    copy:
      dest: /tmp/bolt-test
      content: |
        Bolt was here!
        OS: {{ facts.os_type }}
        User: {{ facts.user }}
```

## Running the Playbook

### Basic Run

```bash
bolt run hello.yaml
```

Output:
```
PLAYBOOK: hello.yaml
============================================================

PLAY [Hello Bolt] ****************************************
TASK [Gathering Facts]
    ok: [localhost]
TASK [Show system info]
    changed: [localhost]
TASK [Create a test file]
    changed: [localhost]
TASK [Write content to file]
    changed: [localhost]

============================================================
PLAY RECAP

localhost            : ok=4    changed=3    failed=0    skipped=0

Total time: 0.15s
```

### Dry Run Mode

See what would happen without making changes:

```bash
bolt run hello.yaml --dry-run
```

### Debug Output

Get detailed information about each task:

```bash
bolt run hello.yaml --debug
```

### Validate Without Running

Check playbook syntax without executing:

```bash
bolt validate hello.yaml
```

## CLI Reference

```
Usage:
  bolt [command]

Available Commands:
  run         Run a playbook
  validate    Validate a playbook
  modules     List available modules
  help        Help about any command

Flags:
  -h, --help       help for bolt
  -n, --dry-run    Show what would be done without making changes
      --no-color   Disable colored output
      --debug      Enable debug output
      --version    version for bolt
```

## Next Steps

- Learn about [Playbook Structure](playbooks.md)
- Explore [Available Modules](modules.md)
- Understand [Variables and Facts](variables.md)
