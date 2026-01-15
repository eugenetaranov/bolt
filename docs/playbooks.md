# Playbooks

Playbooks are YAML files that define a set of tasks to execute on target hosts. This document covers playbook structure and syntax.

## Structure Overview

```yaml
# A playbook contains one or more plays
name: Play Name                    # Optional description
hosts: localhost                   # Required: target hosts
connection: local                  # Connection type (local, ssh, ssm)
gather_facts: true                 # Gather system facts (default: true)
become: false                      # Enable privilege escalation
become_user: root                  # User to become (default: root)

vars:                              # Play-level variables
  myvar: value

tasks:                             # List of tasks to execute
  - name: Task name
    module_name:
      param: value

handlers:                          # Tasks triggered by notify
  - name: Handler name
    module_name:
      param: value
```

## Play Attributes

| Attribute | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | no | - | Description of the play |
| `hosts` | string | **yes** | - | Target hosts (e.g., `localhost`, `webservers`) |
| `connection` | string | no | `local` | Connection type: `local`, `ssh`, `ssm` |
| `gather_facts` | bool | no | `true` | Gather system facts before tasks |
| `become` | bool | no | `false` | Enable privilege escalation (sudo) |
| `become_user` | string | no | `root` | User to become when using sudo |
| `vars` | map | no | - | Variables available to all tasks |
| `tasks` | list | no | - | Tasks to execute |
| `handlers` | list | no | - | Handlers triggered by notify |

## Task Attributes

```yaml
tasks:
  - name: Install nginx              # Task description
    apt:                             # Module name
      name: nginx                    # Module parameters
      state: present
    when: facts.os_family == 'Debian'  # Conditional
    register: install_result         # Store result in variable
    notify: restart nginx            # Trigger handler if changed
    ignore_errors: false             # Continue on failure
    retries: 3                       # Retry count on failure
    delay: 5                         # Seconds between retries
    become: true                     # Sudo for this task
    changed_when: "false"            # Override change detection
```

| Attribute | Type | Description |
|-----------|------|-------------|
| `name` | string | Task description (shown in output) |
| `when` | string | Conditional expression |
| `register` | string | Store task result in this variable |
| `notify` | string/list | Handler(s) to trigger if task changes something |
| `loop` | list | Iterate task over items |
| `loop_var` | string | Variable name for loop item (default: `item`) |
| `ignore_errors` | bool | Continue execution even if task fails |
| `retries` | int | Number of retry attempts |
| `delay` | int | Seconds to wait between retries |
| `become` | bool | Enable sudo for this task |
| `become_user` | string | User to become |
| `changed_when` | string | Override when task reports changed |
| `failed_when` | string | Override when task reports failed |

## Conditionals (when)

Tasks can be conditionally executed using `when`:

```yaml
tasks:
  # Simple boolean
  - name: Run if variable is true
    command:
      cmd: echo "enabled"
    when: feature_enabled

  # Comparison
  - name: Only on macOS
    brew:
      name: git
    when: facts.os_family == 'Darwin'

  # Not equal
  - name: Skip on production
    command:
      cmd: echo "not prod"
    when: environment != 'production'

  # Negation
  - name: Run if not skipped
    command:
      cmd: echo "running"
    when: not skip_task

  # Check registered result
  - name: Run if previous task changed
    command:
      cmd: systemctl restart app
    when: config_result.changed
```

## Loops

Execute a task multiple times with different values:

```yaml
tasks:
  # Basic loop
  - name: Create directories
    file:
      path: "{{ item }}"
      state: directory
    loop:
      - /opt/app
      - /opt/app/config
      - /opt/app/logs

  # Loop with custom variable name
  - name: Install packages
    apt:
      name: "{{ pkg }}"
      state: present
    loop:
      - nginx
      - postgresql
      - redis
    loop_var: pkg
```

Loop variables available:
- `item` (or custom `loop_var`) - Current item
- `loop_index` - Current index (0-based)

## Handlers

Handlers are tasks that only run when notified:

```yaml
tasks:
  - name: Update nginx config
    copy:
      src: nginx.conf
      dest: /etc/nginx/nginx.conf
    notify: restart nginx

  - name: Update app config
    copy:
      src: app.conf
      dest: /etc/app/config.yaml
    notify:
      - restart nginx
      - restart app

handlers:
  - name: restart nginx
    command:
      cmd: systemctl restart nginx

  - name: restart app
    command:
      cmd: systemctl restart app
```

Handlers:
- Only run if the notifying task reports `changed`
- Run once at the end of the play (deduplicated)
- Run in the order they are defined, not notified

## Multiple Plays

A playbook can contain multiple plays:

```yaml
# First play - setup webservers
- name: Configure Web Servers
  hosts: webservers
  tasks:
    - name: Install nginx
      apt:
        name: nginx
        state: present

# Second play - setup databases
- name: Configure Databases
  hosts: databases
  tasks:
    - name: Install PostgreSQL
      apt:
        name: postgresql
        state: present
```

## Privilege Escalation

Run tasks with elevated privileges:

```yaml
# Play-level become
name: System Setup
hosts: localhost
become: true
become_user: root

tasks:
  - name: Install system packages
    apt:
      name: nginx

  # Override for specific task
  - name: Run as app user
    command:
      cmd: whoami
    become: true
    become_user: appuser
```

## Complete Example

```yaml
name: Setup Web Application
hosts: localhost
connection: local
gather_facts: true
become: false

vars:
  app_name: myapp
  app_dir: /opt/{{ app_name }}
  app_user: www-data

tasks:
  - name: Create application directory
    file:
      path: "{{ app_dir }}"
      state: directory
      mode: "0755"
    become: true

  - name: Install dependencies (macOS)
    brew:
      name:
        - nginx
        - redis
      state: present
    when: facts.os_family == 'Darwin'

  - name: Install dependencies (Linux)
    apt:
      name:
        - nginx
        - redis-server
      state: present
      update_cache: true
    when: facts.os_family == 'Debian'
    become: true

  - name: Copy application config
    copy:
      dest: "{{ app_dir }}/config.yaml"
      content: |
        app:
          name: {{ app_name }}
          environment: production
      mode: "0644"
    notify: restart app

  - name: Ensure app is running
    command:
      cmd: pgrep {{ app_name }} || {{ app_dir }}/start.sh
      creates: /var/run/{{ app_name }}.pid

handlers:
  - name: restart app
    command:
      cmd: "{{ app_dir }}/restart.sh"
```

## Best Practices

1. **Use descriptive names** - Make task names clear and actionable
2. **Use variables** - Avoid hardcoding values
3. **Be idempotent** - Tasks should be safe to run multiple times
4. **Use conditionals** - Make playbooks work across platforms
5. **Group related tasks** - Keep playbooks organized
6. **Use handlers** - For service restarts and similar actions
7. **Validate first** - Run `bolt validate` before executing
