# Modules Reference

Modules are the units of work in Bolt. Each module performs a specific action like installing packages, managing files, or running commands.

## Available Modules

| Module | Description |
|--------|-------------|
| [apt](#apt) | Manage packages on Debian/Ubuntu |
| [brew](#brew) | Manage Homebrew packages on macOS |
| [command](#command) | Execute shell commands |
| [copy](#copy) | Copy files to targets |
| [file](#file) | Manage files and directories |

---

## apt

Manage packages on Debian/Ubuntu systems using apt-get.

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string/list | no* | - | Package name(s) |
| `state` | string | no | `present` | `present`, `absent`, `latest`, `purged` |
| `update_cache` | bool | no | `false` | Run `apt-get update` first |
| `cache_valid_time` | int | no | `0` | Skip update if cache newer than N seconds |
| `upgrade` | string | no | `none` | `none`, `yes`, `safe`, `full`, `dist` |
| `install_recommends` | bool | no | `true` | Install recommended packages |
| `autoremove` | bool | no | `false` | Remove unused dependencies |
| `deb` | string | no | - | Path or URL to .deb file |

*Required unless using `update_cache`, `upgrade`, or `deb`

### States

| State | Description |
|-------|-------------|
| `present` | Ensure package is installed |
| `absent` | Remove package (keep config files) |
| `latest` | Install and upgrade to latest version |
| `purged` | Remove package and config files |

### Examples

```yaml
# Install a single package
- name: Install nginx
  apt:
    name: nginx
    state: present

# Install multiple packages
- name: Install development tools
  apt:
    name:
      - build-essential
      - git
      - curl
    state: present

# Update cache and install
- name: Install with fresh cache
  apt:
    name: nginx
    state: present
    update_cache: true
    cache_valid_time: 3600

# Upgrade all packages
- name: Full system upgrade
  apt:
    upgrade: dist
    autoremove: true

# Install from .deb file
- name: Install local package
  apt:
    deb: /tmp/package.deb

# Remove package completely
- name: Purge old package
  apt:
    name: apache2
    state: purged
```

---

## brew

Manage Homebrew packages on macOS.

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string/list | no* | - | Package name(s) |
| `state` | string | no | `present` | `present`, `absent`, `latest` |
| `cask` | bool | no | `false` | Install as cask (GUI application) |
| `update_homebrew` | bool | no | `false` | Run `brew update` first |
| `upgrade_all` | bool | no | `false` | Upgrade all packages |
| `options` | list | no | - | Additional install options |

*Required unless using `update_homebrew` or `upgrade_all`

### Examples

```yaml
# Install CLI tool
- name: Install ripgrep
  brew:
    name: ripgrep
    state: present

# Install multiple tools
- name: Install development tools
  brew:
    name:
      - git
      - go
      - node
    state: present

# Install GUI application (cask)
- name: Install VS Code
  brew:
    name: visual-studio-code
    cask: true

# Install multiple casks
- name: Install GUI apps
  brew:
    name:
      - docker
      - slack
      - 1password
    cask: true
    state: present

# Update Homebrew and upgrade all
- name: Update everything
  brew:
    update_homebrew: true
    upgrade_all: true

# Keep package at latest version
- name: Ensure go is latest
  brew:
    name: go
    state: latest
```

---

## command

Execute shell commands on the target.

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `cmd` | string | **yes** | - | Command to execute |
| `chdir` | string | no | - | Change to directory before running |
| `creates` | string | no | - | Skip if this path exists |
| `removes` | string | no | - | Only run if this path exists |

### Idempotency

Use `creates` and `removes` for idempotent commands:

- `creates` - Skip command if the path already exists
- `removes` - Only run if the path exists

### Examples

```yaml
# Simple command
- name: Show current user
  command:
    cmd: whoami

# Change directory first
- name: Build project
  command:
    cmd: make build
    chdir: /opt/myapp

# Idempotent with creates
- name: Initialize database
  command:
    cmd: ./init-db.sh
    creates: /var/lib/myapp/db.sqlite

# Only run if file exists
- name: Remove old logs
  command:
    cmd: rm -rf /var/log/myapp/*.old
    removes: /var/log/myapp

# Capture output
- name: Get version
  command:
    cmd: cat /etc/os-release
  register: os_info
```

### Result Data

When using `register`, the result contains:

```yaml
result:
  changed: true
  data:
    cmd: "whoami"
    stdout: "alice"
    stderr: ""
    exit_code: 0
```

---

## copy

Copy files or content to the target.

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `dest` | string | **yes** | - | Destination path |
| `src` | string | no* | - | Source file path |
| `content` | string | no* | - | Inline content to write |
| `mode` | string | no | `0644` | File permissions |
| `owner` | string | no | - | Owner username |
| `group` | string | no | - | Group name |
| `backup` | bool | no | `false` | Create backup before overwriting |
| `force` | bool | no | `true` | Overwrite if exists |
| `create_dirs` | bool | no | `false` | Create parent directories |
| `validate` | string | no | - | Validation command (`%s` = temp path) |

*Either `src` or `content` is required (mutually exclusive)

### Examples

```yaml
# Copy a file
- name: Copy nginx config
  copy:
    src: ./files/nginx.conf
    dest: /etc/nginx/nginx.conf
    mode: "0644"
    owner: root
    group: root
    backup: true

# Write inline content
- name: Create config file
  copy:
    dest: /etc/myapp/config.yaml
    content: |
      database:
        host: localhost
        port: 5432
      logging:
        level: info
    mode: "0600"

# With validation
- name: Copy SSH config
  copy:
    src: ./sshd_config
    dest: /etc/ssh/sshd_config
    validate: "/usr/sbin/sshd -t -f %s"
    backup: true

# Create with parent directories
- name: Create nested config
  copy:
    dest: /opt/myapp/config/settings.yaml
    content: "# Settings"
    create_dirs: true
```

### Using with Roles

When using the copy module within a role, relative `src` paths automatically resolve to the role's `files/` directory:

```yaml
# In roles/webserver/tasks/main.yaml
- name: Copy nginx config
  copy:
    src: nginx.conf              # Looks in roles/webserver/files/nginx.conf
    dest: /etc/nginx/nginx.conf
```

This allows you to organize static files within your role:

```
roles/webserver/
├── tasks/
│   └── main.yaml
└── files/
    ├── nginx.conf
    └── index.html
```

### Idempotency

The copy module uses SHA256 checksums to detect changes. It will:
- Skip if content already matches
- Only update attributes if content is same but mode/owner differs

---

## file

Manage files, directories, and symlinks.

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | **yes** | - | Path to manage |
| `state` | string | no | `file` | `file`, `directory`, `link`, `absent`, `touch` |
| `mode` | string | no | - | Permissions (e.g., `0755`) |
| `owner` | string | no | - | Owner username |
| `group` | string | no | - | Group name |
| `src` | string | no | - | Source for symlinks |
| `recurse` | bool | no | `false` | Apply attributes recursively |
| `force` | bool | no | `false` | Force symlink creation |

### States

| State | Description |
|-------|-------------|
| `file` | Ensure file exists (error if missing) |
| `directory` | Create directory if missing |
| `link` | Create symlink (requires `src`) |
| `absent` | Remove file or directory |
| `touch` | Create empty file or update timestamp |

### Examples

```yaml
# Create directory
- name: Create app directory
  file:
    path: /opt/myapp
    state: directory
    mode: "0755"
    owner: appuser
    group: appgroup

# Create nested directories
- name: Create config structure
  file:
    path: /opt/myapp/config/ssl
    state: directory
    mode: "0700"

# Create symlink
- name: Link current version
  file:
    path: /opt/myapp/current
    src: /opt/myapp/releases/v1.2.3
    state: link

# Force symlink (replace existing)
- name: Update symlink
  file:
    path: /opt/myapp/current
    src: /opt/myapp/releases/v1.2.4
    state: link
    force: true

# Set permissions recursively
- name: Fix permissions
  file:
    path: /var/www/html
    mode: "0755"
    owner: www-data
    group: www-data
    recurse: true
    state: directory

# Remove file or directory
- name: Clean up temp files
  file:
    path: /tmp/myapp-cache
    state: absent

# Touch file (create or update timestamp)
- name: Update marker file
  file:
    path: /var/run/myapp.updated
    state: touch
```

---

## Writing Custom Modules

Modules implement the `Module` interface:

```go
type Module interface {
    Name() string
    Run(ctx context.Context, conn connector.Connector, params map[string]any) (*Result, error)
}

type Result struct {
    Changed bool
    Message string
    Data    map[string]any
}
```

Register modules in `init()`:

```go
func init() {
    module.Register(&MyModule{})
}
```

See existing modules in `internal/module/` for examples.
