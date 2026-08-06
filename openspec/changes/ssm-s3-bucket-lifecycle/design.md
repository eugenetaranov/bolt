## Context

`internal/connector/ssm` already talks to S3 for file transfer (`s3API`: `PutObject`/`GetObject`/`DeleteObject`, scoped deliberately narrow) and already has the machinery to temporarily grant an instance's IAM role S3 access (`ensureS3Access` / `WithAutoIAMPolicy`, added for `ssm.attach_s3_policy`). This change adds bucket *administration* (create/status/delete) plus a `verify` command that exercises the existing transfer + auto-IAM-policy path end to end against a real instance.

The critical constraint driving this design (raised directly by the user): **`delete` must assume the bucket is very likely non-empty** — it has to reliably remove every object (including old versions and delete markers if versioning was ever turned on, and abandoned multipart uploads) before `DeleteBucket` will succeed, not just clear the `tack-transfer/` prefix tack itself writes to.

## Goals / Non-Goals

**Goals:**
- `tack ssm-bucket create|status|delete` for the S3 transfer bucket, secure by default (encrypted, public access blocked, lifecycle-expired transfer objects).
- `delete` correctly empties and removes a bucket regardless of how many objects, versions, delete markers, or in-flight multipart uploads it holds.
- A hard guard against deleting a bucket tack didn't create (prevents `--name` typos from destroying unrelated data).
- `tack ssm-bucket verify --instance <id>` proves a real upload/download round trip through the bucket for a given SSM-managed instance, reusing the existing temporary-IAM-policy mechanism when the instance's role lacks access.
- Full unit-test coverage of the deletion algorithm's pagination/versioning/multipart-cleanup logic via a mocked S3 admin client (no live AWS calls in tests).

**Non-Goals:**
- Changing `ssm.bucket` / `ssm.attach_s3_policy` playbook or inventory behavior — this change only adds tooling around the bucket's lifecycle.
- SSE-KMS key creation/management — `create` accepts an existing KMS key ARN, it doesn't provision one.
- Cross-account bucket policies, replication, access logging, or multi-region buckets.
- Scoped/partial deletion (e.g. `--prefix`) — `delete` always removes the whole bucket, consistent with tack treating it as exclusively owned once created.
- Automatic bucket naming — the user always supplies `--name` (or `TACK_SSM_BUCKET`).

## Decisions

**1. New package `internal/ssmbucket`, not an extension of `internal/connector/ssm`.**
The connector's `s3API` interface is intentionally minimal (transfer only: put/get/delete a single object) so its mock surface and blast radius stay small. Bucket administration (`CreateBucket`, `PutBucketLifecycleConfiguration`, `ListObjectVersions`, `DeleteBucket`, ...) is a materially different, higher-privilege operation set. Keeping it in a separate package with its own `s3AdminAPI` interface (mirroring the `ssmAPI`/`s3API`/`ec2API`/`iamAPI` pattern already used in `ssm.go`) avoids widening the connector's test surface and keeps "transfer a file" and "manage the bucket" as clearly separate privilege boundaries. `verify` imports `internal/connector/ssm` directly to drive the real transfer path (no duplicated upload/download logic).

**2. Ownership marker gates `delete` (and `status`).**
`create` sets a bucket tag `ManagedBy=tack` via `PutBucketTagging` (idempotent, applied every run). `delete` calls `GetBucketTagging` first and refuses to proceed unless that tag is present, *unless* `--unmanaged` is explicitly passed. This is a hard gate independent of `--force` (which only skips the interactive confirmation prompt) — a mistyped `--name` on a random production bucket must not be one flag away from wiping it. `status` also surfaces whether the tag is present so users can tell tack-managed buckets apart from ones they'll need `--unmanaged` for.
- *Alternative considered*: a marker object (e.g. `.tack-bucket`) instead of a bucket tag. Rejected: a marker object lives under the same key space being deleted and could itself trigger "bucket has objects" confusion in `status`/dry-run counts; a bucket-level tag is cheaper to check (`GetBucketTagging`, one call) and cleanly separate from object data.

**3. Deletion algorithm handles the non-empty, possibly-versioned, possibly-multipart case explicitly.**
Order of operations in `delete`:
1. `GetBucketTagging` → enforce the ownership gate (Decision 2).
2. `GetBucketVersioning` → if `Status` is `Enabled` or `Suspended` (i.e. versioning was *ever* turned on — `Suspended` still leaves existing versions/delete-markers behind), use the versioned path; otherwise the plain path.
3. Preview pass: paginate (plain: `ListObjectsV2`; versioned: `ListObjectVersions`) to count objects/versions/delete-markers, and list open multipart uploads (`ListMultipartUploads`). Print the count in the confirmation prompt (or `--force` summary) so the user knows what they're about to destroy.
4. Delete pass: same pagination, batching up to 1000 identifiers per `DeleteObjects` call (S3's hard per-request limit) — `ObjectIdentifier{Key}` for the plain path, `ObjectIdentifier{Key, VersionId}` for both versions and delete markers on the versioned path. Continue until the list response is no longer truncated.
5. Abort every incomplete multipart upload found in step 3 via `AbortMultipartUpload` (these don't block `DeleteBucket` but are billed storage tack should clean up as part of "destroy everything").
6. `DeleteBucket`. If it fails with `BucketNotEmpty` (a task could have written a new object mid-run), re-run steps 3–5 once more (bounded: 1 retry) before surfacing a clear "objects were added concurrently; re-run `ssm-bucket delete`" error instead of looping indefinitely.
7. Report the total objects/versions/delete-markers/multipart-uploads removed.
- *Alternative considered*: always call `ListObjectVersions` regardless of the bucket's versioning status. Rejected: `ListObjectVersions` still works on a never-versioned bucket (every object simply has `VersionId=null`) but `DeleteObjects` with an explicit `VersionId: "null"` is more failure-prone across S3 semantics than the plain `ListObjectsV2`/`DeleteObjects{Key}` path for the common (never-versioned) case, so we branch on `GetBucketVersioning` and use the simpler path when possible.

**4. `create` is idempotent and converges config on re-run.**
`CreateBucket` treats `BucketAlreadyOwnedByYou` as success; `BucketAlreadyExists` (name owned by a different account — S3 names are globally unique) is a hard error surfaced as-is. `PutBucketEncryption`, `PutPublicAccessBlock`, `PutBucketLifecycleConfiguration`, and `PutBucketTagging` are called unconditionally every run so `create` also acts as a "fix drifted config" command.

**5. `verify` drives the real `ssmconn.Connector`, not a bucket-package reimplementation.**
`verify --instance i-xxx [--attach-policy]` builds an `ssmconn.Connector` with `WithBucket`, `WithRegion`, and `WithAutoIAMPolicy` (only if `--attach-policy`), `Connect()`s, `Upload()`s a small random payload to a throwaway remote path, `Download()`s it back, byte-compares, removes the remote temp file, then `Close()` — which is where an attached temporary IAM policy gets detached, identical to what a real playbook run does. This guarantees `verify` tests exactly the code path `ssm.attach_s3_policy` uses in production, with zero duplicated logic.

**6. CLI shape mirrors the existing `vault` parent-command pattern.**
`ssmBucketCmd` (parent, `Use: "ssm-bucket"`) with `create`/`status`/`delete`/`verify` subcommands in a new `cmd/tack/ssm_bucket.go`, following `cmd/tack/vault.go`'s structure (persistent flags shared where sensible: `--name`/`--region`/`TACK_SSM_BUCKET`/`TACK_SSM_REGION` fallbacks, consistent with `run`'s existing `--ssm-bucket`/`--ssm-region`).

## Risks / Trade-offs

- **[Risk]** `delete` permanently destroys data if pointed at the wrong bucket → **Mitigation:** hard ownership-tag gate (`--unmanaged` required to bypass) + object-count preview + confirmation prompt / explicit `--force`.
- **[Risk]** Large buckets (many thousands+ objects) make `delete` slow and could hit S3 rate limits → **Mitigation:** paginate + batch at the API's 1000-key ceiling; sequential requests; treat `SlowDown`/throttling as retryable with backoff (bounded retries, not infinite).
- **[Risk]** Objects written concurrently mid-delete (e.g. a playbook still running against the bucket) → **Mitigation:** one bounded retry of the list+delete pass on `BucketNotEmpty` from `DeleteBucket`, then a clear actionable error rather than a silent loop.
- **[Risk]** `verify --attach-policy` mutates the target instance's IAM role → **Mitigation:** reuses the already-shipped, already-tested scoped-policy attach/detach path (`ssm.attach_s3_policy`); no new IAM surface introduced by this change.
- **[Risk]** Global S3 bucket-name collisions on `create` → **Mitigation:** `BucketAlreadyExists` (not owned by caller) is surfaced as a distinct, clear error rather than silently treated as success.

## Migration Plan

Purely additive: a new command group and a new internal package. No schema, data, or existing-command changes, so there's nothing to migrate and no rollback beyond reverting the commit. `ssm.bucket`/`ssm.attach_s3_policy` playbook behavior is untouched.

## Open Questions

- Should `create` print a ready-to-paste `ssm:` YAML snippet (bucket + region) to reduce copy/paste setup error? Nice-to-have; not required for the core lifecycle/verify functionality — can be added as a follow-up task if desired during implementation.
- Should `delete` ever support scoping to just the `tack-transfer/` prefix (for a bucket shared with other tools) instead of always assuming exclusive ownership? Current design assumes exclusive ownership (enforced by the tag gate) and always deletes the whole bucket; revisit if a real shared-bucket use case shows up.
