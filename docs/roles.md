# Roles

Roles provide a way to organize and reuse playbook content. They follow the Ansible-compatible directory structure.

## Directory Structure

```
project/
├── playbook.yaml
└── roles/
    └── webserver/
        ├── tasks/
        │   └── main.yaml      # Tasks to execute
        ├── handlers/
        │   └── main.yaml      # Handlers for notify
        ├── defaults/
        │   └── main.yaml      # Default variables (lowest priority)
        ├── vars/
        │   └── main.yaml      # Role variables (higher priority)
        ├── files/
        │   └── ...            # Static files for copy module
        └── templates/
            └── ...            # Template files for template module
```

## Using Roles

Include roles in your playbook with the `roles` field:

```yaml
name: Setup Server
hosts: localhost
connection: local

roles:
  - webserver
  - database

tasks:
  - name: Additional task after roles
    command:
      cmd: echo "Setup complete"
```

## Role Files

### tasks/main.yaml

Contains the list of tasks to execute:

```yaml
- name: Install nginx
  apt:
    name: nginx
    state: present

- name: Create web root
  file:
    path: "{{ web_root }}"
    state: directory
    mode: "0755"
```

### handlers/main.yaml

Contains handlers that can be triggered via `notify`:

```yaml
- name: Restart nginx
  command:
    cmd: systemctl restart nginx
```

### defaults/main.yaml

Default variable values (lowest priority, easily overridden):

```yaml
web_root: /var/www/html
server_port: 80
```

### vars/main.yaml

Role variables (higher priority than defaults):

```yaml
nginx_user: www-data
```

### files/

Static files that can be copied to targets using the `copy` module's `src` parameter.

When a role task uses the `copy` module with a relative `src` path, Tack automatically looks for the file in the role's `files/` directory:

```yaml
# In roles/webserver/tasks/main.yaml
- name: Copy nginx config
  copy:
    src: nginx.conf           # Looks in roles/webserver/files/nginx.conf
    dest: /etc/nginx/nginx.conf
    mode: "0644"
```

Place your static files in the role's `files/` directory:

```
roles/webserver/
├── tasks/
│   └── main.yaml
└── files/
    ├── nginx.conf
    └── robots.txt
```

### templates/

Template files that are rendered with variable substitution using the `template` module.

When a role task uses the `template` module with a relative `src` path, Tack automatically looks for the template in the role's `templates/` directory:

```yaml
# In roles/webserver/tasks/main.yaml
- name: Deploy nginx config
  template:
    src: nginx.conf.j2        # Looks in roles/webserver/templates/nginx.conf.j2
    dest: /etc/nginx/nginx.conf
    mode: "0644"
```

Templates use Go's `text/template` syntax (`{{ .variable }}`):

```
# roles/webserver/templates/nginx.conf.j2
server {
    listen {{ .server_port }};
    server_name {{ .server_host }};
    root {{ .web_root }};
}
```

Place your template files in the role's `templates/` directory:

```
roles/webserver/
├── tasks/
│   └── main.yaml
├── templates/
│   ├── nginx.conf.j2
│   └── app.conf.j2
└── files/
    └── robots.txt
```

## Variable Precedence

Variables are merged in this order (lowest to highest priority):

1. Role defaults (`defaults/main.yaml`)
2. Role vars (`vars/main.yaml`), overridden by any per-invocation `vars:` on the `roles:` list entry (see [Tags and variables per role](#tags-and-variables-per-role))
3. Play vars
4. Registered variables

## Example

See `examples/roles-demo/` for a complete working example:

```bash
# Run the roles demo
./bin/tack run examples/roles-demo/playbook.yaml --dry-run
```

## Role Search Path

Roles are searched in the `roles/` directory relative to the playbook location.

```
my-project/
├── playbook.yaml          # References roles: [webserver]
└── roles/
    └── webserver/         # Found at ./roles/webserver/
```

## Remote Roles (git / HTTPS URLs)

A role entry can be a git or HTTPS URL instead of a local name, in which case Tack fetches it before the play runs — no manual cloning or `roles/` checkout required:

```yaml
roles:
  - webserver                                              # local, from ./roles/webserver/
  - https://github.com/org/ansible-role-nginx.git          # whole repo is the role
  - git@github.com:org/infra-roles.git//roles/database     # a role living in a subdirectory of a repo
```

Supported reference forms (the same ones `tack run <ref>` accepts for playbooks):

| Form | Example |
|------|---------|
| Git over HTTPS | `https://github.com/org/repo.git` |
| Git over SSH | `git@github.com:org/repo.git` |
| Role in a subdirectory | `<git-url>//path/to/role` |
| Pinned to a branch/tag/commit | `<git-url>@<ref>` (before any `//path`) |
| S3 | `s3://bucket/path/to/role` |

If the repo itself *is* the role (no subdirectory), omit the `//path` — Tack treats the whole repo as the role directory.

### Pasted straight from the browser

You don't have to hand-build the `.git//path` form. Tack also accepts GitHub/GitLab URLs exactly as your browser shows them — copy the address bar and paste it in:

```yaml
roles:
  - https://github.com/org/repo                              # repo homepage URL — whole repo is the role
  - https://github.com/org/repo/tree/main/roles/database      # after navigating into a folder
```

Both resolve to the same git clone Tack would do with the `.git`/`.git//path` forms above; the browse URL is just parsed back into `repo` + `ref` + `path`.

### Tags and variables per role

To pass tags and/or variables to a role, use the object form:

```yaml
roles:
  - role: https://github.com/org/ansible-role-nginx.git
    tags: [web]
    vars:
      nginx_version: "1.25"
```

`vars` are per-invocation variables for that role only — a clean way to pin a version or tune behavior without forking the role or touching its `defaults/main.yaml`. They override the role's own `defaults/main.yaml` and `vars/main.yaml` (see [Variable Precedence](#variable-precedence)).

Each remote role is fetched into a temporary directory for the duration of the play (git: `git clone --depth 1`, honoring an explicit `@ref`) and removed afterward. Local roles are unaffected and require no network access.
