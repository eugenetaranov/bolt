## 1. Package foundation (`internal/ssmbucket`)

- [x] 1.1 Create `internal/ssmbucket` package with an `s3AdminAPI` interface scoped to bucket-admin operations (`CreateBucket`, `HeadBucket`, `PutBucketTagging`, `GetBucketTagging`, `PutBucketEncryption`, `GetBucketEncryption`, `PutPublicAccessBlock`, `GetPublicAccessBlock`, `PutBucketLifecycleConfiguration`, `GetBucketLifecycleConfiguration`, `GetBucketVersioning`, `ListObjectsV2`, `ListObjectVersions`, `DeleteObjects`, `ListMultipartUploads`, `AbortMultipartUpload`, `DeleteBucket`), mirroring the `ssmAPI`/`s3API`/`ec2API`/`iamAPI` pattern in `internal/connector/ssm/ssm.go`.
- [x] 1.2 Add a `Manager` type wrapping the admin client + bucket name + region, with functional `Option`s (`WithRegion`) and a `withS3AdminClient` test injector, following `ssmconn.New`/`Option` conventions.
- [x] 1.3 Define the `ManagedBy=tack` tag key/value as package constants shared by `create`, `status`, and `delete`.

## 2. `create`

- [x] 2.1 Implement `Manager.Create(ctx)`: `CreateBucket` (treat `BucketAlreadyOwnedByYou` as success, `BucketAlreadyExists` as a hard error), then unconditionally apply `PutPublicAccessBlock` (block all public access), `PutBucketEncryption` (SSE-S3 default, SSE-KMS when a key ARN option is set), `PutBucketLifecycleConfiguration` (expire `tack-transfer/` objects after a configurable day count, default from design), and `PutBucketTagging` (`ManagedBy=tack`).
- [x] 2.2 Unit tests with a mocked `s3AdminAPI`: fresh create, idempotent re-run against an already-owned bucket, `BucketAlreadyExists` error surfaced correctly, encryption/lifecycle/tag/public-access-block calls all verified.

## 3. `status`

- [x] 3.1 Implement `Manager.Status(ctx)`: `HeadBucket` (existence), `GetBucketTagging` (tack-managed check), `GetBucketEncryption`, `GetPublicAccessBlock`, `GetBucketVersioning`, `GetBucketLifecycleConfiguration`; return a struct summarizing all of it (bucket-not-found and no-such-tagging/no-such-lifecycle "not configured" responses treated as normal, not errors).
- [x] 3.2 Unit tests: existing tack-managed bucket, non-existent bucket, existing-but-unmanaged bucket (missing tag), bucket with no lifecycle/encryption configured yet.

## 4. `delete`

- [x] 4.1 Implement the ownership gate: `GetBucketTagging` → require `ManagedBy=tack` unless an `unmanaged` option is set; return a distinct sentinel/error type the CLI layer can turn into the `--unmanaged` guidance message.
- [x] 4.2 Implement `GetBucketVersioning`-based branch selection (never-versioned vs. `Enabled`/`Suspended`).
- [x] 4.3 Implement the plain-path listing+delete: paginated `ListObjectsV2`, batched `DeleteObjects` (≤1000 keys/request), loop until not truncated.
- [x] 4.4 Implement the versioned-path listing+delete: paginated `ListObjectVersions`, batched `DeleteObjects` with `{Key, VersionId}` for both `Versions` and `DeleteMarkers`, loop until not truncated.
- [x] 4.5 Implement multipart-upload cleanup: paginated `ListMultipartUploads`, `AbortMultipartUpload` for each.
- [x] 4.6 Implement a dry-run "preview" pass (count objects/versions/delete-markers/multipart-uploads) used for the confirmation-prompt summary, reusing the same listing code as 4.3/4.4/4.5.
- [x] 4.7 Implement `Manager.Delete(ctx)`: preview → (caller handles confirmation) → delete pass (4.3–4.5) → `DeleteBucket` → on `BucketNotEmpty`, retry the delete pass once more → on second failure, return a clear "re-run delete" error → return total removed count.
- [x] 4.8 Unit tests: empty bucket, many-objects-paginated bucket, versioned bucket with multiple versions and delete markers, bucket with incomplete multipart uploads, unmanaged-bucket rejection, concurrent-write retry-then-fail path, successful retry-then-succeed path.

## 5. `verify`

- [x] 5.1 Implement a `verify` operation (in `internal/ssmbucket` or a small helper alongside it) that builds an `ssmconn.Connector` via `ssmconn.New` with `WithBucket`, `WithRegion`, and `WithAutoIAMPolicy` (only when `--attach-policy` is requested), `Connect()`s, `Upload()`s a small random test payload to a throwaway remote path, `Download()`s it back, byte-compares, removes the remote temp file via `Execute`, and `Close()`s (detaching any temporarily attached policy).
- [x] 5.2 Surface a distinguishable error/message when the round trip fails due to an access-denied-style error so the CLI can suggest `--attach-policy` / `ssm.attach_s3_policy`.
- [x] 5.3 Unit tests using the existing `ssmconn` test-injector options (`withSSMClient`, `withS3Client`, `withEC2Client`, `withIAMClient` — export or add test-only constructors as needed) covering: success without `--attach-policy`, success with `--attach-policy` when access is initially missing, and failure without `--attach-policy` producing the actionable error.

## 6. CLI wiring

- [x] 6.1 Create `cmd/tack/ssm_bucket.go` with a parent `ssmBucketCmd` (`Use: "ssm-bucket"`) and `create`/`status`/`delete`/`verify` subcommands, following `cmd/tack/vault.go`'s structure.
- [x] 6.2 Add shared `--name`/`--region` flags (with `TACK_SSM_BUCKET`/`TACK_SSM_REGION` env fallback, consistent with `run`'s existing `--ssm-bucket`/`--ssm-region`), plus `create`-specific flags (`--kms-key-id`, `--lifecycle-days`), `delete`-specific flags (`--force`, `--unmanaged`), and `verify`-specific flags (`--instance`, `--attach-policy`).
- [x] 6.3 Wire `delete`'s interactive confirmation prompt (skipped by `--force`), showing the preview count from task 4.6.
- [x] 6.4 Register `ssmBucketCmd` on `rootCmd` in `cmd/tack/main.go`'s `init()`.
- [x] 6.5 CLI-level tests (flag parsing/wiring) following existing `cmd/tack` test conventions where applicable.

## 7. Docs

- [x] 7.1 Add an `ssm-bucket` command section to `docs/connectors.md` (or a new `docs/ssm-bucket.md` if it doesn't fit cleanly) covering create/status/delete/verify usage and the ownership-tag safety model.
- [x] 7.2 Add the new commands/flags to `llms.txt`.
- [x] 7.3 Cross-link from the existing "File Transfer via S3" section (added for `ssm.attach_s3_policy`) to the new bucket-lifecycle commands.

## 8. Verification

- [x] 8.1 `go build ./...`, `go test -short ./...`, `golangci-lint run ./...` all clean.
- [x] 8.2 `openspec validate ssm-s3-bucket-lifecycle --strict` passes.
- [ ] 8.3 Manual smoke test against a real (or LocalStack-style) S3 bucket if credentials are available: create → status → verify → delete on a bucket seeded with plain objects, versioned objects, and an incomplete multipart upload, confirming delete succeeds and removes everything. **Not run**: no AWS credentials available in this environment. Unit tests (4.8) cover the equivalent scenarios against mocks; the CLI was confirmed to reach real AWS SDK credential resolution end-to-end (`tack ssm-bucket status` fails at IMDS credential lookup, not earlier).
