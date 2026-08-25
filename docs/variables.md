# Variables and Facts

Tack supports variable interpolation using `{{ variable }}` syntax.

> **Note:** This is Tack's own interpolation, NOT Jinja2. See [filters](#available-filters) for what's supported.

## Variable Sources

Variables come from several sources (in order of precedence, highest first):

1. **Registered results** - Task outputs stored via `register`
2. **Loop variables** - `item` and `loop_index` during loops
3. **Play variables** - Defined in `vars` section
4. **Facts** - Gathered system information
5. **CLI extra vars** - Passed via `-e key=value`
6. **Vault variables** - From encrypted `vault_file`
7. **vars_files** - External YAML files loaded via `vars_files`
8. **Role defaults** - From `defaults/main.yaml` in roles
9. **Environment** - Available via `env.VARNAME`

### vars_files

Load variables from external YAML files:

```yaml
vars_files:
  - vars/common.yaml
  - vars/{{ facts.os_family | lower }}.yaml
  - ?vars/local.yaml          # ? prefix = skip if file doesn't exist
```

### vault_file

Load encrypted variables (see `tack vault init` to create):

```yaml
vault_file: secrets.yaml
```

## Basic Interpolation

```yaml
vars:
  app_name: myapp
  app_port: 8080

tasks:
  - name: Create directory
    file:
      path: /opt/{{ app_name }}
      state: directory

  - name: Write config
    copy:
      dest: /opt/{{ app_name }}/config.yaml
      content: |
        name: {{ app_name }}
        port: {{ app_port }}
```

## Dotted Paths

Access nested values using dot notation:

```yaml
tasks:
  - name: Show OS family
    command:
      cmd: echo "Running on {{ facts.os_family }}"

  - name: Use home directory
    file:
      path: "{{ env.HOME }}/projects"
      state: directory
```

## Filters

Transform values using filters with the pipe (`|`) syntax:

```yaml
tasks:
  - name: Use default value
    command:
      cmd: echo "{{ custom_var | default('fallback') }}"

  - name: Convert to uppercase
    command:
      cmd: echo "{{ app_name | upper }}"
```

### Available Filters

| Filter | Description | Example |
|--------|-------------|---------|
| `default(value)` | Use fallback if undefined/empty | `{{ var \| default('none') }}` |
| `lower` | Convert to lowercase | `{{ name \| lower }}` |
| `upper` | Convert to uppercase | `{{ name \| upper }}` |
| `trim` | Remove whitespace | `{{ input \| trim }}` |
| `string` | Convert to string | `{{ number \| string }}` |
| `int` | Convert to integer | `{{ port \| int }}` |
| `bool` | Convert to boolean | `{{ flag \| bool }}` |
| `first` | First item of list | `{{ items \| first }}` |
| `last` | Last item of list | `{{ items \| last }}` |
| `length` | Length of string/list | `{{ items \| length }}` |
| `join(sep)` | Join list with separator | `{{ items \| join(',') }}` |

### Filter Examples

```yaml
vars:
  packages:
    - nginx
    - redis
    - postgresql

tasks:
  - name: Show package count
    command:
      cmd: echo "Installing {{ packages | length }} packages"

  - name: Show as comma-separated
    command:
      cmd: echo "Packages: {{ packages | join(', ') }}"

  - name: Use first package
    apt:
      name: "{{ packages | first }}"

  - name: With default
    command:
      cmd: echo "Env is {{ environment | default('development') }}"
```

## System Facts

When `gather_facts: true` (the default), Tack collects system information.

### Available Facts

#### System

| Fact | Description | Example Value |
|------|-------------|---------------|
| `facts.os_type` | OS kernel name (uname -s) | `Darwin`, `Linux` |
| `facts.os_family` | OS family (derived) | `Darwin`, `Debian`, `RedHat`, `Alpine`, `Arch`, `Suse` |
| `facts.distribution` | Linux distribution (from /etc/os-release) | `ubuntu`, `fedora`, `alpine` |
| `facts.distribution_version` | Distribution version | `22.04`, `39` |
| `facts.os_name` | Full OS name | `macOS`, `Ubuntu 22.04.3 LTS` |
| `facts.os_version` | OS version (macOS only) | `14.0` |
| `facts.architecture` | Raw CPU architecture (uname -m) | `x86_64`, `aarch64` |
| `facts.arch` | Normalized architecture | `amd64`, `arm64`, `arm` |
| `facts.kernel` | Kernel version (uname -r) | `6.2.0-generic` |
| `facts.hostname` | System hostname | `myserver` |
| `facts.user` | Current username | `alice` |
| `facts.home` | Home directory | `/home/alice` |
| `facts.pkg_manager` | Package manager (derived from OS) | `apt`, `brew`, `dnf`, `pacman`, `apk`, `zypper` |
| `facts.go_os` | Go runtime OS | `linux`, `darwin` |
| `facts.go_arch` | Go runtime architecture | `amd64`, `arm64` |

#### Network

| Fact | Description | Example Value |
|------|-------------|---------------|
| `facts.default_ipv4` | Primary IPv4 address | `10.0.0.5` |
| `facts.default_interface` | Default network interface | `eth0`, `en0` |
| `facts.default_ipv4_subnet` | Subnet (CIDR) of the default interface (Linux) | `10.0.0.0/24` |
| `facts.default_ipv4_prefix` | Prefix length of the default subnet (Linux) | `24` |
| `facts.all_ipv4` | All non-loopback IPv4 addresses (list) | `[10.0.0.5, 172.17.0.1]` |
| `facts.all_ipv6` | All global IPv6 addresses (list) | `[2001:db8::1]` |

On Linux, network facts are gathered via `ip route` and `ip addr`. On macOS, `route` and `ifconfig` are used. If the commands are unavailable (e.g., minimal containers), the facts are simply absent.

#### EC2 (auto-detected via IMDSv2)

These facts are only available when running on an EC2 instance. Tack uses IMDSv2 with a 1-second timeout, so there is no delay on non-EC2 hosts.

| Fact | Description | Example Value |
|------|-------------|---------------|
| `facts.ec2_instance_id` | Instance ID | `i-01bc2d8980ba86bc7` |
| `facts.ec2_region` | AWS region | `eu-west-1` |
| `facts.ec2_az` | Availability zone | `eu-west-1a` |
| `facts.ec2_instance_type` | Instance type | `t3.medium` |
| `facts.ec2_ami_id` | AMI ID | `ami-0abcdef1234567890` |
| `facts.ec2_tags.<key>` | Instance tags (map) | `facts.ec2_tags.Name` → `prod-web-0` |
| `facts.ec2_private_ip` | Private IPv4 address | `10.80.41.80` |
| `facts.ec2_public_ip` | Public IPv4 address (if assigned) | `54.12.34.56` |

EC2 tags require "Allow tags in instance metadata" to be enabled on the instance. If IMDS tags are unavailable, Tack falls back to the EC2 API (requires `ec2:DescribeTags` permission).

#### Environment

A subset of environment variables is captured under `facts.env`:

| Fact | Description |
|------|-------------|
| `facts.env.PATH` | System PATH |
| `facts.env.SHELL` | Login shell |
| `facts.env.LANG` | Locale |
| `facts.env.LC_ALL` | Locale override |
| `facts.env.TERM` | Terminal type |
| `facts.env.EDITOR` | Default editor |

These are also accessible via the top-level `env` variable (e.g., `{{ env.HOME }}`).

### Using Facts in Conditionals

```yaml
tasks:
  # Platform-specific tasks
  - name: Install on macOS
    brew:
      name: git
    when: facts.os_family == 'Darwin'

  - name: Install on Debian/Ubuntu
    apt:
      name: git
    when: facts.os_family == 'Debian'

  - name: Install on RHEL/Fedora
    command:
      cmd: dnf install -y git
    when: facts.os_family == 'RedHat'

  # Architecture-specific
  - name: Download amd64 binary
    command:
      cmd: curl -o /tmp/app https://example.com/app-amd64
    when: facts.arch == 'amd64'

  - name: Download arm64 binary
    command:
      cmd: curl -o /tmp/app https://example.com/app-arm64
    when: facts.arch == 'arm64'
```

## Environment Variables

Access environment variables via `env`:

```yaml
tasks:
  - name: Use HOME
    file:
      path: "{{ env.HOME }}/.config/myapp"
      state: directory

  - name: Show user
    command:
      cmd: echo "Running as {{ env.USER }}"

  - name: Use PATH
    command:
      cmd: echo "Path is {{ env.PATH }}"
```

## Registered Variables

Store task results for later use:

```yaml
tasks:
  - name: Get current version
    command:
      cmd: cat /opt/myapp/VERSION
    register: version_result

  - name: Show version
    command:
      cmd: echo "Current version is {{ version_result.data.stdout }}"

  - name: Conditional on result
    command:
      cmd: ./upgrade.sh
    when: version_result.data.stdout != '2.0.0'
```

### Registered Result Structure

```yaml
registered_var:
  changed: true/false
  message: "Task result message"
  data:
    # Module-specific data
    # For command module:
    cmd: "the command"
    stdout: "output"
    stderr: "errors"
    exit_code: 0
```

### Using .changed

Check if a previous task made changes:

```yaml
tasks:
  - name: Update config
    copy:
      src: config.yaml
      dest: /etc/myapp/config.yaml
    register: config_result

  - name: Restart if config changed
    command:
      cmd: systemctl restart myapp
    when: config_result.changed
```

## Loop Variables

During loops, special variables are available:

```yaml
tasks:
  - name: Create directories
    file:
      path: "{{ item }}"
      state: directory
    loop:
      - /opt/app/logs
      - /opt/app/data
      - /opt/app/config

  # With custom loop variable
  - name: Install packages
    apt:
      name: "{{ pkg }}"
    loop:
      - nginx
      - redis
    loop_var: pkg

  # Using loop_index
  - name: Create numbered files
    copy:
      dest: "/tmp/file-{{ loop_index }}.txt"
      content: "File number {{ loop_index }}"
    loop:
      - first
      - second
      - third
```

| Variable | Description |
|----------|-------------|
| `item` | Current loop item (default) |
| `loop_index` | Current index (0-based) |
| Custom via `loop_var` | Your chosen variable name |

## Complex Example

```yaml
name: Dynamic Configuration
hosts: localhost
gather_facts: true

vars:
  app:
    name: myservice
    version: "1.2.3"
    ports:
      http: 8080
      https: 8443
  environments:
    - development
    - staging
    - production

tasks:
  - name: Create app directory
    file:
      path: "/opt/{{ app.name }}"
      state: directory

  - name: Create environment configs
    copy:
      dest: "/opt/{{ app.name }}/{{ item }}.yaml"
      content: |
        environment: {{ item }}
        app_name: {{ app.name }}
        version: {{ app.version }}
        http_port: {{ app.ports.http }}
        os: {{ facts.os_family }}
    loop: "{{ environments }}"

  - name: Show summary
    command:
      cmd: |
        echo "Deployed {{ app.name }} v{{ app.version }} on {{ facts.hostname }}"
```
