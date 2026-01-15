# Connectors

Connectors define how Bolt connects to and executes commands on target systems.

## Available Connectors

| Connector | Status | Description |
|-----------|--------|-------------|
| `local` | ✅ Implemented | Execute on local machine |
| `docker` | ✅ Implemented | Execute in Docker containers |
| `ssh` | 🚧 Planned | Connect via SSH |
| `ssm` | 🚧 Planned | AWS Systems Manager |

## Local Connector

Execute commands on the local machine.

### Configuration

```yaml
name: Local Playbook
hosts: localhost
connection: local  # This is the default

tasks:
  - name: Run locally
    command:
      cmd: whoami
```

### Features

- Direct command execution via `/bin/sh`
- File operations using local filesystem
- Optional sudo support via `become`

### With Privilege Escalation

```yaml
name: System Setup
hosts: localhost
connection: local
become: true
become_user: root

tasks:
  - name: Install package (as root)
    apt:
      name: nginx
      state: present

  - name: Run as specific user
    command:
      cmd: whoami
    become: true
    become_user: postgres
```

## Docker Connector

Execute commands inside Docker containers using `docker exec`.

### Configuration

```yaml
name: Configure Container
hosts: my-container        # Container name or ID
connection: docker

tasks:
  - name: Install package
    command:
      cmd: apt-get update && apt-get install -y curl

  - name: Create directory
    file:
      path: /app/data
      state: directory
```

### Features

- Execute commands via `docker exec`
- File upload/download via `docker cp`
- Run as specific user with `become_user`
- Works with container names or IDs

### With User Override

```yaml
name: Configure as App User
hosts: my-container
connection: docker
become: true
become_user: appuser

tasks:
  - name: Run as appuser
    command:
      cmd: whoami
    # Output: appuser
```

### Example: Setup a Development Container

```yaml
name: Setup Dev Container
hosts: dev-container
connection: docker

tasks:
  - name: Update package cache
    command:
      cmd: apt-get update

  - name: Install development tools
    command:
      cmd: apt-get install -y git vim curl

  - name: Create workspace
    file:
      path: /workspace
      state: directory
      mode: "0755"

  - name: Copy configuration
    copy:
      content: |
        export PATH="/usr/local/bin:$PATH"
        alias ll="ls -la"
      dest: /root/.bashrc
```

### Running the Container First

```bash
# Start a container
docker run -d --name my-container ubuntu:22.04 sleep 600

# Run bolt playbook
bolt run container-setup.yaml

# Enter the container to verify
docker exec -it my-container bash
```

## SSH Connector (Planned)

Connect to remote hosts via SSH.

### Planned Configuration

```yaml
name: Remote Setup
hosts: webserver.example.com
connection: ssh

# SSH-specific settings
ssh_user: deploy
ssh_port: 22
ssh_private_key: ~/.ssh/id_rsa

tasks:
  - name: Install on remote
    apt:
      name: nginx
```

### Planned Features

- Password and key-based authentication
- SSH agent forwarding
- Jump host / bastion support
- Connection multiplexing
- Configurable timeouts

## SSM Connector (Planned)

Connect to AWS EC2 instances via Systems Manager.

### Planned Configuration

```yaml
name: AWS Instance Setup
hosts: i-1234567890abcdef0
connection: ssm

# AWS-specific settings
aws_region: us-east-1
aws_profile: production

tasks:
  - name: Install on EC2
    apt:
      name: nginx
```

### Planned Features

- No SSH keys required
- Works with private instances (no public IP)
- IAM-based authentication
- CloudWatch logging integration
- Automatic session management

## Connector Interface

All connectors implement this interface:

```go
type Connector interface {
    // Connect establishes a connection
    Connect(ctx context.Context) error

    // Execute runs a command and returns output
    Execute(ctx context.Context, cmd string) (*Result, error)

    // Upload copies content to target
    Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error

    // Download copies content from target
    Download(ctx context.Context, src string, dst io.Writer) error

    // Close terminates the connection
    Close() error

    // String returns connection description
    String() string
}

type Result struct {
    Stdout   string
    Stderr   string
    ExitCode int
}
```

## Implementing Custom Connectors

1. Create a new package under `internal/connector/`
2. Implement the `Connector` interface
3. Add connector selection in `executor.getConnector()`

Example structure:

```go
package myconnector

import (
    "context"
    "github.com/eugenetaranov/bolt/internal/connector"
)

type Connector struct {
    // connection settings
}

func New(opts ...Option) *Connector {
    return &Connector{}
}

func (c *Connector) Connect(ctx context.Context) error {
    // establish connection
    return nil
}

func (c *Connector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
    // run command
    return &connector.Result{
        Stdout:   "output",
        ExitCode: 0,
    }, nil
}

// ... implement remaining methods

// Verify interface compliance
var _ connector.Connector = (*Connector)(nil)
```

## Connection Selection

The connector is selected based on the `connection` field in the play:

```yaml
# Explicit local
connection: local

# SSH (when implemented)
connection: ssh

# AWS SSM (when implemented)
connection: ssm
```

If not specified, `local` is used by default.
