## Why

Tack's SSM connector already supports S3-backed file transfer (`ssm.bucket`) to work around the ~24 KB inline command-channel limit, and can temporarily grant an instance's IAM role S3 access via `ssm.attach_s3_policy`. But users must still provision the transfer bucket themselves (with the right encryption, public-access-block, and lifecycle settings) before either feature is usable, and have no built-in way to confirm a given instance can actually reach it. A first-class subcommand to create, inspect, and destroy that bucket — and to verify an instance's access to it, attaching a temporary IAM policy on demand — removes that manual setup step and closes the loop on the existing S3 transfer workaround.

## What Changes

- New `tack ssm-bucket` command group (mirrors the existing `tack vault` parent-command pattern):
  - `tack ssm-bucket create` — provisions the S3 bucket used for SSM large-file transfer, with secure-by-default settings: block public access, SSE-S3 (or SSE-KMS via flag) encryption, and a lifecycle rule expiring objects under the `tack-transfer/` prefix after N days. Idempotent — safe to re-run against an existing tack-managed bucket.
  - `tack ssm-bucket status` — reports whether the bucket exists and shows its encryption, public-access-block, and lifecycle configuration.
  - `tack ssm-bucket delete` — deletes the bucket, handling the realistic case that it's non-empty: paginates through every object (not just under `tack-transfer/` — the whole bucket, since tack owns it once created), removes all versions and delete markers if versioning was ever enabled, aborts incomplete multipart uploads, batch-deletes in chunks of up to 1000 keys, then deletes the bucket itself. Requires `--force` (or interactive confirmation) since it's destructive; reports the object count removed.
  - `tack ssm-bucket verify --instance <id>` — round-trips a small test object through the bucket via the named SSM-managed instance (upload + download + cleanup) to confirm the transfer path works end to end. If the instance's role lacks S3 access and `--attach-policy` is passed, temporarily attaches the same scoped inline policy `ssm.attach_s3_policy` uses (via the existing `ssmconn.WithAutoIAMPolicy`/`ensureS3Access` mechanism) for the duration of the check, then removes it.
- No changes to existing `ssm.bucket` / `ssm.attach_s3_policy` playbook behavior — this change adds bucket lifecycle tooling around it and reuses the existing temporary-IAM-policy mechanism as-is.

## Capabilities

### New Capabilities
- `ssm-bucket-management`: CLI subcommands to create, inspect, and destroy the S3 bucket used for SSM file transfer, and to verify a given instance's access to it (optionally attaching a temporary IAM policy to prove the round trip).

### Modified Capabilities
(none — existing SSM connector S3-transfer and auto-IAM-policy behavior is unchanged and has no prior spec in `openspec/specs/`)

## Impact

- New AWS SDK usage: `s3.CreateBucket`, `PutBucketEncryption`, `PutPublicAccessBlock`, `PutBucketLifecycleConfiguration`, `HeadBucket`/`GetBucketEncryption`/`GetPublicAccessBlock`/`GetBucketLifecycleConfiguration`/`GetBucketVersioning` (status), and for deletion: `GetBucketVersioning` (detect if versioning was ever enabled), `ListObjectsV2` or `ListObjectVersions` (paginated), `ListMultipartUploads`/`AbortMultipartUpload`, batched `DeleteObjects`, `DeleteBucket`. No new SDK module — `github.com/aws/aws-sdk-go-v2/service/s3` is already a dependency.
- New CLI files: likely `cmd/tack/ssm_bucket.go` (subcommand wiring, mirroring `cmd/tack/vault.go`).
- New/extended package: likely `internal/connector/ssm` (or a new `internal/ssmbucket` package) providing the bucket create/status/delete/verify operations, reusing `ssmconn.Connector`'s existing `ensureS3Access`/`WithAutoIAMPolicy` for the `verify --attach-policy` path.
- Docs: `docs/connectors.md` SSM section, `llms.txt` command/flag reference, `examples/` (optional usage example).
- No breaking changes to existing commands, flags, or playbook/inventory schema.
