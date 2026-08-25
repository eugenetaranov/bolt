# Tack

A Go-based configuration management and system bootstrapping tool inspired by Ansible. Single binary, no dependencies.

## Installation

```bash
brew install tackhq/tap/tack          # Homebrew
go install github.com/tackhq/tack/cmd/tack@latest  # Go
```

Or download from the [releases page](https://github.com/tackhq/tack/releases).

## Quick Start

```bash
tack run examples/playbooks/setup-dev.yaml --check   # preview changes
tack run examples/playbooks/setup-dev.yaml            # apply
```

```yaml
# setup.yaml
name: Setup Development Machine
hosts: localhost
connection: local

tasks:
  - name: Install packages
    brew:
      name: [git, go, ripgrep]
      state: present
    when: facts.os_family == 'Darwin'

  - name: Create projects directory
    file:
      path: "{{ env.HOME }}/projects"
      state: directory
```

## Features

- **Simple YAML playbooks** with Ansible-compatible role structure
- **Idempotent modules** -- apt, brew, yum, file, copy, command, cron, systemd, template, lineinfile, blockinfile, iptables, get_url, unarchive, make, wait_for, assert
- **Cross-platform** -- macOS (brew) and Linux (apt, yum, systemd)
- **Multiple connectors** -- Local, Docker, SSH, AWS SSM with tag-based discovery
- **Plan/apply workflow** -- preview changes before applying, `--auto-approve` for CI
- **Parallel execution** -- `--forks N` for concurrent multi-host runs
- **Block/rescue/always** -- structured error handling with rollback and guaranteed cleanup
- **Task inclusion** -- `include_tasks` with scoped `vars:`, `loop:`, conditional `when:`, circular detection
- **Tag-based filtering** -- `--tags` and `--skip-tags` for selective task execution, with `always`/`never` special tags
- **Variable system** -- interpolation, filters, registered outputs, vars_files, vault encryption
- **JSON output** -- `--output json` for machine-readable output in CI pipelines
- **System facts** -- OS, arch, network, EC2 metadata (via IMDSv2)
- **Remote sources** -- run playbooks from git repos, S3, or HTTP URLs
- **No dependencies** -- single static binary

## Connectors

| Connector | Example | Description |
|-----------|---------|-------------|
| **Local** | `connection: local` | Run on the current machine (default) |
| **Docker** | `-c docker://container` | Run inside a Docker container |
| **SSH** | `-c ssh://user@host:port` | Connect via SSH; reads `~/.ssh/config` |
| **SSM** | `--ssm-tags env=prod` | AWS SSM; tag-based instance discovery, S3 file transfer |

Connection type is auto-detected from flags when not specified. See [Connectors docs](docs/connectors.md) for full configuration and environment variables.

## Multi-Host & Parallel Execution

```bash
# Serial (default)
tack run deploy.yaml --hosts web1,web2,web3

# Parallel -- up to 5 hosts at once
tack run deploy.yaml --hosts web1,web2,web3,web4,web5 --forks 5

# With inventory groups
tack run deploy.yaml -i inventory.yaml --hosts webservers --forks 10
```

Output is buffered per-host and flushed in order. Errors on one host don't stop others. Use `TACK_FORKS` env var for CI.

## Playbook Examples

### Remote Host via SSH

```yaml
name: Configure Web Server
hosts: web1
connection: ssh
ssh:
  user: deploy
  key: ~/.ssh/deploy_key

tasks:
  - name: Install nginx
    apt:
      name: nginx
      state: present

  - name: Enable nginx
    systemd:
      name: nginx
      state: started
      enabled: true
```

```bash
tack run server-setup.yaml
tack run server-setup.yaml --hosts web1,web2,web3 --forks 3
```

### AWS SSM with Tag-Based Discovery

```yaml
name: Patch App Servers
connection: ssm
ssm:
  region: us-east-1
  bucket: my-ssm-transfer-bucket
  tags:
    env: production
    role: app-server

tasks:
  - name: Install security updates
    apt:
      name: "*"
      state: latest
```

### Remote Playbook Sources

```bash
tack run git@github.com:user/repo.git//path/to/playbook.yaml
tack run https://github.com/user/repo.git@v1.0//playbook.yaml
tack run s3://bucket/path/to/playbook.yaml
```

### Playbook-Level Defaults (Multiple Plays)

A playbook is either a sequence of plays (as shown above) or a mapping with
`hosts`, `connection`, `sudo`, `vars` declared once and a `plays:` array
underneath. Plays inherit any field they don't set themselves:

```yaml
hosts: webservers
connection: ssh
sudo: true
plays:
  - name: Install
    tasks:
      - apt: { name: nginx, state: present }

  - name: Deploy
    tasks:
      - copy:
          src: ./app.tar.gz
          dest: /opt/app/
```

Play-level values always win on conflict. See `examples/playbook-defaults.yaml`.

## Task Inclusion

Use `include_tasks` to include shared task files from your playbooks. This eliminates YAML duplication and enables reusable task libraries.

```yaml
tasks:
  # Basic include
  - name: Setup common packages
    include_tasks: tasks/common.yml

  # Include with scoped variables
  - name: Install nginx
    include_tasks: tasks/install-package.yml
    vars:
      package_name: nginx
      version: "1.24"

  # Conditional include
  - name: Debian-specific setup
    include_tasks: "{{ facts.os_family }}/packages.yml"
    when: facts.os_family == "Debian"

  # Loop-driven include
  - name: Configure services
    include_tasks: tasks/configure-service.yml
    loop:
      - nginx
      - redis
      - postgres
    loop_var: service_name
```

`include:` and `include_tasks:` are equivalent -- `include_tasks:` is the preferred form for consistency with Ansible conventions. Tasks are loaded and executed at runtime, supporting `when:` conditions, variable-interpolated paths, and `loop:` iteration.

Variables passed via `vars:` are scoped to the included tasks and do not persist after the include completes. Registered variables from included tasks do persist.

Circular includes are detected and reported with a clear error chain. Maximum nesting depth is 64.

See [`examples/include-tasks/`](examples/include-tasks/) for a complete example.

## Block / Rescue / Always

Group tasks with structured error handling -- attempt a block, run rescue on failure, and always run cleanup:

```yaml
tasks:
  - name: Deploy with rollback
    block:
      - name: Pull latest code
        command:
          cmd: git -C /opt/app pull origin main
      - name: Run migrations
        command:
          cmd: /opt/app/migrate.sh
      - name: Restart service
        command:
          cmd: systemctl restart app
    rescue:
      - name: Rollback code
        command:
          cmd: git -C /opt/app reset --hard HEAD~1
      - name: Restart previous version
        command:
          cmd: systemctl restart app
    always:
      - name: Send deploy notification
        command:
          cmd: /opt/app/notify.sh
```

**Execution flow:**
1. `block:` tasks run sequentially. If all succeed, `rescue:` is skipped.
2. If any `block:` task fails, remaining block tasks stop and `rescue:` runs.
3. `always:` runs regardless of block/rescue outcome.
4. If `rescue:` succeeds, the block is considered recovered (no error propagated).

**Block-level directives:**
- `when:` gates the entire block (including rescue and always)
- `sudo:` is inherited by all tasks within block/rescue/always unless overridden
- `name:` provides descriptive output in plan and execution
- Blocks can be nested (block within rescue, etc.)

See [`examples/block-rescue/`](examples/block-rescue/) for a complete example.

## Tags

Selectively run or skip tasks using tags:

```bash
tack run deploy.yaml --tags deploy          # only deploy-tagged tasks
tack run deploy.yaml --skip-tags debug      # skip debug tasks
tack run deploy.yaml --tags deploy,config   # OR logic: deploy or config
tack run deploy.yaml --check --tags deploy  # plan mode respects tags
```

Tags can be applied to tasks, blocks, plays, and role references:

```yaml
name: Deploy
hosts: webservers
tags: [infra]  # play-level: inherited by all tasks

roles:
  - role: webserver
    tags: [web]  # role-level: inherited by all role tasks

tasks:
  - name: Install nginx
    apt:
      name: nginx
    tags: [packages, web]

  - name: Deploy block
    tags: deploy  # block-level: inherited by child tasks
    block:
      - name: Pull code
        command:
          cmd: git pull
      - name: Restart
        command:
          cmd: systemctl restart app
```

**Special tags:**
- `always` -- task runs even when `--tags` filter is active (unless explicitly in `--skip-tags`)
- `never` -- task is skipped by default, runs only when explicitly included via `--tags`

**Tag inheritance:** A task's effective tags are the union of its own tags plus inherited tags from its play, role, and enclosing block(s). Tags are additive.

**Handlers:** Notified handlers always run regardless of `--tags`, but respect `--skip-tags`.

See [`examples/playbooks/tags-demo.yaml`](examples/playbooks/tags-demo.yaml) for a complete example.

### Filtering by role

When a play pulls in several roles, `-r`/`--roles` restricts the run to a chosen subset by name -- handy for iterating on a single role during development:

```bash
tack run site.yaml -r web              # only run the 'web' role's tasks
tack run site.yaml --roles web,db      # OR logic: run the 'web' and 'db' roles
tack run site.yaml -r web --tags deploy # AND with tags: 'web' role's deploy-tagged tasks
```

While `--roles` is active, tasks from other roles and play-level tasks are skipped (reported as `skipped (role)`). An unknown role name simply matches nothing rather than erroring. The filter composes with `--tags`/`--skip-tags` -- a task must pass both to run.

## Inventory

Define hosts, groups, SSH config, and variables in a reusable file:

```yaml
# inventory.yaml
hosts:
  web1:
    ssh: { user: deploy, key: ~/.ssh/id_deploy }
    vars: { region: us-east-1 }
  web2:
    ssh: { user: deploy }

groups:
  webservers:
    hosts: [web1, web2]
    vars: { app_port: 8080 }
```

```bash
tack run deploy.yaml -i inventory.yaml --hosts webservers
```

### Dynamic Inventory

Tack supports dynamic inventory sources beyond static YAML files. If `-i` points to an executable, Tack runs it with `--list` and parses the JSON/YAML output. Plugin-based sources are configured via YAML files with a `plugin:` key.

**Script inventory** -- any executable that outputs Tack inventory format:
```bash
tack run deploy.yaml -i ./my-inventory-script.sh
```

**HTTP inventory** -- fetch from a REST API:
```yaml
# cmdb-inventory.yml
plugin: http
url: https://cmdb.example.com/api/inventory
headers:
  Authorization: "Bearer {{ env.CMDB_TOKEN }}"
```

**EC2 inventory** -- discover AWS instances by tags:
```yaml
# ec2-inventory.yml
plugin: ec2
regions: [us-east-1, us-west-2]
filters:
  tag:env: production
group_by: [tag:role, tag:env]
host_key: private_ip  # or instance_id (for SSM), public_ip
```

**Multiple sources** -- merge inventories with multiple `-i` flags:
```bash
tack run deploy.yaml -i ec2-inventory.yml -i overrides.yml
```

**Inspect inventory** -- debug resolved hosts and groups:
```bash
tack inventory --list -i ec2-inventory.yml
tack inventory --host web1 -i inventory.yaml
```

See [`examples/dynamic-inventory/`](examples/dynamic-inventory/) for complete samples and [`examples/inventory.yaml`](examples/inventory.yaml) for a static inventory sample.

## Available Modules

| Module | Description |
|--------|-------------|
| `apt` | Manage packages on Debian/Ubuntu |
| `brew` | Manage Homebrew packages on macOS |
| `yum` | Manage packages on RHEL/CentOS/Fedora (auto-detects dnf) |
| `command` | Execute shell commands |
| `copy` | Copy files or write inline content |
| `cron` | Manage cron entries idempotently (Linux user crontabs and `/etc/cron.d` drop-ins) |
| `file` | Manage files, directories, and symlinks |
| `git` | Check out git repositories idempotently by branch/tag/SHA |
| `lineinfile` | Ensure a specific line is present or absent in a file |
| `blockinfile` | Manage a block of text between marker lines in a file |
| `systemd` | Manage systemd services (start, stop, enable, mask, daemon-reload) |
| `iptables` | Manage iptables/ip6tables firewall rules on Linux (idempotent, optional reboot persistence) |
| `get_url` | Download a file onto the target (curl/wget) with checksum verification |
| `unarchive` | Extract a local archive (tar family or zip) on the target |
| `make` | Build from source (configure/make/install) with a required idempotency guard |
| `template` | Render Go templates with variable substitution |
| `user` | Manage system users on Linux (create, modify, remove) |
| `group` | Manage system groups on Linux (create, modify, remove) |
| `wait_for` | Wait for port, path, command, or URL before proceeding |
| `assert` | Validate preconditions (built-in keyword); fail fast with a clear message |

Run `tack module <name>` for detailed parameter docs.

## Tooling

```bash
tack generate --packages nginx --files /etc/nginx/nginx.conf -c ssh://root@web1  # capture live state
tack scaffold myrole          # create role boilerplate
tack test myrole              # test in Docker container (reused for idempotency checks)
tack validate playbook.yaml   # syntax check
tack vault init secrets.yaml      # create encrypted vault file
```

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Installation, first playbook, CLI reference |
| [Playbooks](docs/playbooks.md) | Tasks, handlers, loops, conditionals |
| [Roles](docs/roles.md) | Reusable role structure |
| [Modules](docs/modules.md) | All modules with parameters and examples |
| [Variables & Facts](docs/variables.md) | Interpolation, filters, system/network/EC2 facts |
| [Connectors](docs/connectors.md) | Local, Docker, SSH, SSM configuration |
| [Development](docs/development.md) | Building, testing, project structure |
| [llms.txt](llms.txt) | LLM-optimized reference (for AI code generation) |

## License

MIT License
