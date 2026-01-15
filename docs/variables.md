# Variables and Facts

Bolt supports variable interpolation using `{{ variable }}` syntax, similar to Jinja2/Ansible templates.

## Variable Sources

Variables come from several sources (in order of precedence):

1. **Registered results** - Task outputs stored via `register`
2. **Loop variables** - `item` and `loop_index` during loops
3. **Play variables** - Defined in `vars` section
4. **Facts** - Gathered system information
5. **Environment** - Available via `env.VARNAME`

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

When `gather_facts: true` (the default), Bolt collects system information.

### Available Facts

| Fact | Description | Example Value |
|------|-------------|---------------|
| `facts.os_type` | OS kernel name | `Darwin`, `Linux` |
| `facts.os_family` | OS family | `Darwin`, `Debian`, `RedHat` |
| `facts.distribution` | Linux distribution | `ubuntu`, `fedora` |
| `facts.distribution_version` | Distribution version | `22.04` |
| `facts.os_name` | Full OS name | `macOS`, `Ubuntu 22.04 LTS` |
| `facts.os_version` | OS version | `14.0`, `22.04` |
| `facts.architecture` | CPU architecture | `x86_64`, `aarch64` |
| `facts.arch` | Normalized architecture | `amd64`, `arm64` |
| `facts.kernel` | Kernel version | `6.2.0-generic` |
| `facts.hostname` | System hostname | `myserver` |
| `facts.user` | Current username | `alice` |
| `facts.home` | Home directory | `/home/alice` |
| `facts.pkg_manager` | Package manager | `apt`, `brew`, `dnf` |

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
