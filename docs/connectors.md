# Connectors

Connectors define how Tack connects to and executes commands on target systems.

## Available Connectors

| Connector | Syntax | Description |
|-----------|--------|-------------|
| **Local** | `connection: local` | Execute on the local machine |
| **Docker** | `connection: docker` | Execute inside a Docker container |
| **SSH** | `connection: ssh` or `-c ssh://user@host:port` | Connect via SSH |
| **SSM** | `connection: ssm` | Connect via AWS Systems Manager |

## Local Connector

Execute commands on the local machine. This is the default when no connection is specified.

```yaml
name: Local Setup
hosts: localhost
connection: local

tasks:
  - name: Install packages
    brew:
      name: [git, go]
      state: present
```

Supports sudo via `sudo: true` at play or task level. Password can be provided via `--sudo-password` flag, `TACK_SUDO_PASSWORD` env var, or interactive prompt.

## Docker Connector

Execute commands inside Docker containers using `docker exec`. File transfer uses `docker cp`.

```yaml
name: Configure Container
hosts: my-container
connection: docker

tasks:
  - name: Install curl
    command:
      cmd: apt-get update && apt-get install -y curl
```

The `hosts` value is the container name or ID. Sudo runs commands as the specified user inside the container.

**CLI shorthand:**

```bash
tack run playbook.yaml -c docker://my-container
```

## SSH Connector

Connect to remote hosts via SSH. Supports key-based and password authentication, and reads `~/.ssh/config` and `~/.ssh/known_hosts` automatically.

### Configuration Sources

SSH settings can come from multiple sources. Priority (highest first):

1. CLI flags (`--ssh-user`, `--ssh-port`, `--ssh-key`, `--ssh-password`, `--ssh-insecure`)
2. Playbook `ssh:` block
3. Per-host inventory `ssh:` settings
4. Group inventory `ssh:` settings
5. `~/.ssh/config`
6. Defaults

### Playbook Configuration

```yaml
name: Configure Web Server
hosts: [web1, web2]
connection: ssh

ssh:
  user: deploy
  key: ~/.ssh/deploy_key
  port: 22

tasks:
  - name: Install nginx
    apt:
      name: nginx
      state: present
```

### CLI Usage

```bash
# URI-style connection strings
tack run playbook.yaml -c ssh://deploy@web1:2222
tack run playbook.yaml -c ssh://deploy@web1 -c ssh://deploy@web2

# Separate flags
tack run playbook.yaml --hosts web1,web2 --ssh-user deploy --ssh-key ~/.ssh/deploy_key

# SSH config aliases work directly
tack run playbook.yaml --hosts myserver

# Connection type is auto-detected from SSH flags or remote hosts
tack run playbook.yaml --hosts web1 --ssh-user deploy
```

### Password Prompt Fallback

If key/agent authentication isn't available or the server rejects it, and no password was supplied via `--ssh-password`/`TACK_SSH_PASSWORD`/`ssh.password`, Tack automatically falls back to an interactive password prompt — no flag needed, mirroring how the `ssh` CLI itself behaves. The prompt only fires if it's actually needed (key-based auth succeeding never triggers it), and only once per run even across multiple hosts.

```bash
# No --ssh-password needed: prompts automatically if key/agent auth fails
tack run playbook.yaml --hosts web1 --ssh-user deploy
```

Disable the fallback with `--no-ssh-prompt` (or `TACK_SSH_NO_PROMPT=1`) for CI/non-interactive runs where a hang-on-prompt would be worse than a clear auth-failure error — automatically disabled whenever stdin isn't a terminal or `--auto-approve`/`-y` is set, so this is mainly for explicit opt-out in scripts that do have a TTY.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `TACK_CONNECTION` | Connection type |
| `TACK_HOSTS` | Comma-separated host list |
| `TACK_SSH_USER` | SSH username |
| `TACK_SSH_PORT` | SSH port |
| `TACK_SSH_KEY` | Path to SSH private key |
| `TACK_SSH_PASSWORD` | SSH password |
| `TACK_SSH_INSECURE` | Skip host key verification (`1`, `true`, or `yes`) |
| `TACK_SSH_NO_PROMPT` | Disable the automatic SSH password prompt fallback (`1`, `true`, or `yes`) |

## SSM Connector

Connect to AWS EC2 instances via Systems Manager. No SSH keys required - uses IAM-based authentication. Works with private instances that have no public IP.

### Tag-Based Discovery

SSM can discover instances by EC2 tags at runtime:

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

The `bucket` field is required for file upload/download operations (copy, template modules).

### File Transfer via S3

Without a bucket, file transfer falls back to inline base64 over the SSM command channel, which is capped at 24 KB. Configuring `bucket` routes larger files through S3 instead: tack uploads to `s3://<bucket>/tack-transfer/<instance-id>/...` and the instance pulls (or pushes) via `aws s3 cp`, which requires the instance's IAM role to have `s3:GetObject`/`s3:PutObject` on that bucket.

Whenever a `bucket` is configured, tack **auto-attaches** a scoped, temporary inline IAM policy to the instance's role before the transfer and removes it when the connection closes — so instances that aren't pre-provisioned with S3 permissions work out of the box, with no extra flags:

```yaml
ssm:
  region: us-east-1
  bucket: my-ssm-transfer-bucket
```

The attach is **best-effort**: if tack's own credentials can't attach the policy (or the instance is already provisioned with S3 access), the transfer still proceeds. It only surfaces the attach failure if the instance-side `aws s3 cp` then fails — in which case the error tells you exactly what to grant.

The attached policy is scoped to `arn:aws:s3:::<bucket>/tack-transfer/<instance-id>/*` only — not the whole bucket. For tack to attach it, tack's own AWS credentials need `iam:GetInstanceProfile`, `iam:PutRolePolicy`, `iam:DeleteRolePolicy`, and `ec2:DescribeInstances`.

To opt out (e.g. instances already have S3 access and you don't want tack touching IAM), set `attach_s3_policy: false` (or `--ssm-attach-policy=false` / `TACK_SSM_ATTACH_POLICY=false`):

```yaml
ssm:
  region: us-east-1
  bucket: my-ssm-transfer-bucket
  attach_s3_policy: false
```

> **Note:** buckets created with `--kms-key-id` (SSE-KMS) additionally require the instance role to have `kms:Decrypt`/`kms:GenerateDataKey` on the key; the auto-attached policy currently grants only S3 actions, so KMS-encrypted transfer buckets need those KMS permissions granted separately.

Use `tack ssm-bucket create` to provision this bucket (with encryption, blocked public access, and a `tack-transfer/` lifecycle rule already set up) instead of creating it by hand — see [Managing the Transfer Bucket](#managing-the-transfer-bucket) below.

### Managing the Transfer Bucket

`tack ssm-bucket` creates, inspects, destroys, and verifies access to the S3 bucket used above. All four subcommands take `--name`/`--region` (or `TACK_SSM_BUCKET`/`TACK_SSM_REGION`):

```bash
# Create the bucket: public access blocked, encryption on, tack-transfer/
# lifecycle rule set, tagged ManagedBy=tack. Safe to re-run.
tack ssm-bucket create --name my-ssm-transfer-bucket --region us-east-1

# SSE-KMS instead of the SSE-S3 default, and a custom expiry window
tack ssm-bucket create --name my-bucket --kms-key-id arn:aws:kms:us-east-1:111122223333:key/abc --lifecycle-days 7

# Inspect current configuration
tack ssm-bucket status --name my-ssm-transfer-bucket

# Confirm a specific instance can actually upload/download through the
# bucket (auto-attaches a temporary IAM policy by default if needed)
tack ssm-bucket verify --name my-ssm-transfer-bucket --instance i-0abc123

# Delete the bucket (and everything in it — objects, all versions and
# delete markers if it was ever versioned, incomplete multipart uploads)
tack ssm-bucket delete --name my-ssm-transfer-bucket
```

`delete` refuses to touch a bucket that doesn't carry the `ManagedBy=tack` tag `create` sets, unless `--unmanaged` is passed — a mistyped `--name` can't wipe an unrelated bucket. Without `--force` it previews the object/version/delete-marker/multipart-upload counts and asks for interactive confirmation first.

Bucket administration needs broader AWS permissions than the transfer path itself: `s3:CreateBucket`, `s3:DeleteBucket`, `s3:PutBucketTagging`/`GetBucketTagging`, `s3:PutEncryptionConfiguration`/`GetEncryptionConfiguration`, `s3:PutBucketPublicAccessBlock`/`GetBucketPublicAccessBlock`, `s3:PutLifecycleConfiguration`/`GetLifecycleConfiguration`, `s3:GetBucketVersioning`, `s3:ListBucket`, `s3:ListBucketVersions`, `s3:ListBucketMultipartUploads`, `s3:DeleteObject`/`DeleteObjectVersion`, and `s3:AbortMultipartUpload`.

### Direct Instance IDs

```yaml
name: Configure Instances
connection: ssm
hosts: [i-0abc123, i-0def456]

ssm:
  region: us-east-1
  bucket: my-transfer-bucket
```

### CLI Usage

```bash
# Tags on CLI (SSM connection auto-detected)
tack run patch.yaml --ssm-tags env=production,role=app-server --ssm-region us-east-1

# Direct instance IDs
tack run patch.yaml --ssm-instances i-0abc123,i-0def456 --ssm-region us-east-1 --ssm-bucket my-bucket

# Large file transfer: auto-attach is on by default when a bucket is set
tack run deploy.yaml --ssm-instances i-0abc123 --ssm-bucket my-bucket

# Opt out of auto-attach (instances already have S3 access)
tack run deploy.yaml --ssm-instances i-0abc123 --ssm-bucket my-bucket --ssm-attach-policy=false
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `TACK_SSM_INSTANCES` | Comma-separated instance IDs |
| `TACK_SSM_TAGS` | Comma-separated key=value tags |
| `TACK_SSM_REGION` | AWS region |
| `TACK_SSM_BUCKET` | S3 bucket for file transfer |
| `TACK_SSM_ATTACH_POLICY` | Override auto-attach of the temporary S3-access IAM policy. On by default when a bucket is set; set `0`/`false`/`no` to opt out (`1`/`true`/`yes` to force on) |

AWS credentials use the standard SDK credential chain (env vars, shared config, IAM roles).

## Dynamic Inventory

In addition to static YAML inventory files, Tack supports dynamic inventory sources via a plugin architecture. Pass any source with `-i`:

**Executable scripts** — auto-detected by file permissions, run with `--list`:
```bash
tack run deploy.yaml -i ./my-inventory-script.sh
```

**Plugin configs** — YAML files with a `plugin:` key:
```yaml
# HTTP: fetch from REST API
plugin: http
url: https://cmdb.example.com/api/inventory
headers:
  Authorization: "Bearer {{ env.CMDB_TOKEN }}"

# EC2: discover AWS instances by tags
plugin: ec2
regions: [us-east-1]
filters:
  tag:env: production
group_by: [tag:role]
host_key: private_ip
```

**Multiple sources** merge in order (later wins on conflicts):
```bash
tack run deploy.yaml -i ec2.yml -i overrides.yml
```

**Inspect resolved inventory** for debugging:
```bash
tack inventory --list -i ec2.yml
tack inventory --host web1 -i hosts.yml
```

Use `--inventory-timeout` to control plugin execution timeout (default: 30s).

See [`examples/dynamic-inventory/`](../examples/dynamic-inventory/) for complete samples.

## Auto-Detection

When no `connection:` is specified, Tack infers the type from flags:

- SSH flags (`--ssh-user`, `--ssh-key`, etc.) or remote `--hosts` values imply `ssh`
- SSM flags (`--ssm-instances`, `--ssm-tags`) imply `ssm`
- Otherwise defaults to `local`

## Parallel Execution

Use `--forks N` (or `TACK_FORKS` env var) to execute against multiple hosts concurrently. Output is buffered per-host and flushed in host order after completion. Defaults to 1 (serial).

```bash
tack run deploy.yaml --hosts web1,web2,web3 --forks 3
```

### Parallel Fact Gathering

Fact gathering runs concurrently across all target hosts regardless of `--forks`. For multi-host plays the executor opens connectors and runs `Gathering Facts` for every host in parallel before any plan/apply work begins, then reuses the open connector for the apply phase.

This is most visible on slow connectors like SSM, where each round-trip costs several seconds. A four-host SSM play that previously waited `4 × t` for serial fact gather now waits roughly `t` (bounded by the slowest host).

The pre-pass is skipped for single-host plays, `connection: local`, and plays with `gather_facts: false`. Concurrency is internally capped at 20 to avoid overwhelming AWS API limits on very large fleets.

### Multi-host Plan & Approval

Plays targeting more than one host render a single consolidated plan with per-line host attribution and prompt for approval **once globally**, not per-host. Output looks like:

```
PLAY Configure web servers
HOSTS web1, web2
HOST web1 [ssh] - gathering facts ✓
HOST web2 [ssh] - gathering facts ✓

PLAN
web1: + apt: install nginx
web2: ~ command: rotate cert

Plan: 1 to change, 1 to run across 2 hosts.

Apply these changes to 2 hosts (web1, web2)? (yes/no):
```

The `PLAY` line is shown only when the play has a `name:` field; anonymous plays start at `HOSTS` (multi-host) or directly at the `HOST` banner (single-host). The `HOSTS` summary line lists up to five names; for larger fleets it shows `HOSTS h1, h2, h3, h4, h5 (and N more)`. Fact-gathering completion is reported inline on the `HOST` line — no separate `Gathering Facts` task line appears in text mode (JSON consumers receive a `host_facts` event instead).

The prompt names the targets directly so you can confirm the right hosts
without scrolling. Single-host plays use the form `Apply these changes to
<host> (<connection>)?`. Multi-host plays show the count plus the first
five names; if more than five hosts are targeted, the rest are abbreviated
to `, ...`.

Hostnames are column-aligned (capped at 30 characters; longer names truncate with `…`). Hosts whose plan contains only no-op tasks contribute zero body lines and are counted in the footer as `(N unchanged)`.

The approval prompt always runs on the main thread, even with `--forks > 1`. SIGINT during the prompt aborts the play with zero hosts applied.

Single-host plays continue to use the existing per-host plan format (no host prefix), so common-case output is unchanged.
