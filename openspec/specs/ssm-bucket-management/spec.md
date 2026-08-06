## ADDED Requirements

### Requirement: `ssm-bucket create` provisions a secure S3 transfer bucket
The system SHALL provide a `tack ssm-bucket create` command that creates (or converges the configuration of) the S3 bucket used for SSM large-file transfer, with public access blocked, server-side encryption enabled (SSE-S3 by default, or SSE-KMS when a KMS key ARN is supplied), a lifecycle rule expiring objects under the `tack-transfer/` prefix, and a `ManagedBy=tack` bucket tag identifying it as tack-owned. The command SHALL be idempotent: re-running it against a bucket it already owns SHALL succeed and re-apply the same configuration rather than erroring.

#### Scenario: Create a new bucket
- **WHEN** the user runs `tack ssm-bucket create --name my-bucket --region us-east-1` and no bucket named `my-bucket` exists
- **THEN** the bucket SHALL be created with public access blocked, encryption enabled, the `tack-transfer/` lifecycle rule applied, and the `ManagedBy=tack` tag set

#### Scenario: Re-run against an existing tack-managed bucket
- **WHEN** the user runs `create` again against a bucket it previously created
- **THEN** the command SHALL succeed without error and SHALL re-apply encryption, public-access-block, lifecycle, and tagging configuration

#### Scenario: Bucket name already owned by another account
- **WHEN** the requested bucket name is already taken by a different AWS account
- **THEN** the command SHALL fail with an error indicating the name is unavailable, rather than silently succeeding

### Requirement: `ssm-bucket status` reports bucket configuration
The system SHALL provide a `tack ssm-bucket status` command that reports whether the named bucket exists, whether it carries the `ManagedBy=tack` tag, and its encryption, public-access-block, versioning, and lifecycle configuration.

#### Scenario: Status of an existing tack-managed bucket
- **WHEN** the user runs `status` against a bucket created by `ssm-bucket create`
- **THEN** the output SHALL show the bucket as tack-managed along with its current encryption, public-access-block, and lifecycle settings

#### Scenario: Status of a non-existent bucket
- **WHEN** the user runs `status` against a bucket name that does not exist
- **THEN** the command SHALL report that the bucket does not exist rather than erroring with a raw AWS error

#### Scenario: Status of a bucket tack did not create
- **WHEN** the user runs `status` against a bucket that exists but lacks the `ManagedBy=tack` tag
- **THEN** the output SHALL clearly indicate the bucket is not tack-managed

### Requirement: `ssm-bucket delete` requires ownership confirmation before destroying data
The system SHALL refuse to delete a bucket that does not carry the `ManagedBy=tack` tag unless the user passes `--unmanaged`, independent of whether `--force` is also passed. When the ownership check passes, the command SHALL require either interactive confirmation or `--force` before proceeding, since deletion is irreversible.

#### Scenario: Refuses an unmanaged bucket
- **WHEN** the user runs `tack ssm-bucket delete --name some-bucket` and `some-bucket` lacks the `ManagedBy=tack` tag, without passing `--unmanaged`
- **THEN** the command SHALL fail before deleting anything and SHALL explain that `--unmanaged` is required to delete a bucket tack didn't create

#### Scenario: Interactive confirmation required by default
- **WHEN** the user runs `delete` against a tack-managed bucket without `--force` in an interactive terminal
- **THEN** the command SHALL prompt for confirmation, showing the object/version count about to be deleted, before proceeding

#### Scenario: --force skips the confirmation prompt
- **WHEN** the user runs `delete --force` against a tack-managed bucket
- **THEN** the command SHALL proceed without an interactive prompt

### Requirement: `ssm-bucket delete` fully empties a non-empty bucket before deletion
The system SHALL assume the bucket is very likely non-empty and SHALL remove every object, object version, delete marker, and incomplete multipart upload before calling `DeleteBucket`, regardless of object count or whether versioning was ever enabled on the bucket.

#### Scenario: Deletes all objects across pagination
- **WHEN** the bucket contains more objects than fit in a single list response (paginated listing)
- **THEN** the command SHALL page through every listing response and delete every object, batching delete requests up to the API's per-request key limit

#### Scenario: Deletes all versions and delete markers when versioning was ever enabled
- **WHEN** the bucket's versioning status is `Enabled` or `Suspended` (versioning was turned on at some point, even if later suspended)
- **THEN** the command SHALL enumerate and delete every object version and every delete marker, not just the current/latest version of each key

#### Scenario: Aborts incomplete multipart uploads
- **WHEN** the bucket has one or more incomplete multipart uploads
- **THEN** the command SHALL abort each of them as part of the delete flow

#### Scenario: Bucket becomes empty and is deleted
- **WHEN** all objects, versions, delete markers, and multipart uploads have been removed
- **THEN** the command SHALL delete the bucket itself and SHALL report the total number of objects/versions/delete-markers/multipart-uploads that were removed

#### Scenario: Concurrent writes during deletion
- **WHEN** `DeleteBucket` fails because a new object was written to the bucket after the delete pass completed
- **THEN** the command SHALL retry the list-and-delete pass once more before failing with an error instructing the user to re-run `ssm-bucket delete`

### Requirement: `ssm-bucket verify` validates instance access to the transfer bucket
The system SHALL provide a `tack ssm-bucket verify --instance <id>` command that performs a real upload/download round trip through the named bucket via the specified SSM-managed instance, cleaning up the temporary test object afterward. When `--attach-policy` is passed and the instance's IAM role lacks S3 access to the bucket, the command SHALL temporarily attach the same scoped inline IAM policy used by `ssm.attach_s3_policy` for the duration of the check and remove it afterward.

#### Scenario: Successful round trip without --attach-policy
- **WHEN** the target instance's role already has S3 access to the bucket and the user runs `verify --instance i-0abc123` without `--attach-policy`
- **THEN** the command SHALL upload a test object, download it back, confirm the content matches, remove the test object, and report success

#### Scenario: Missing permissions with --attach-policy
- **WHEN** the target instance's role lacks S3 access to the bucket and the user passes `--attach-policy`
- **THEN** the command SHALL temporarily attach a scoped inline policy granting access to the instance's role, complete the round trip successfully, and remove the policy before exiting

#### Scenario: Missing permissions without --attach-policy
- **WHEN** the target instance's role lacks S3 access to the bucket and `--attach-policy` is not passed
- **THEN** the command SHALL fail with an error that identifies the permission problem and suggests retrying with `--attach-policy` or configuring `ssm.attach_s3_policy`
