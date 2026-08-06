package ssmbucket

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// maxDeleteBatch is S3's hard ceiling on keys per DeleteObjects call.
const maxDeleteBatch = 1000

// ErrNotManaged indicates the bucket doesn't carry the ManagedBy=tack tag
// Manager.Create sets. Delete refuses to proceed against such a bucket
// unless DeleteOptions.Unmanaged is set, so a mistyped --name can't
// destroy a bucket tack didn't create.
var ErrNotManaged = errors.New("bucket is not managed by tack (missing ManagedBy=tack tag); pass --unmanaged to delete it anyway")

// DeletePreview summarizes what a Delete call would remove, without
// deleting anything. Used to show the user what they're about to destroy
// before they confirm.
type DeletePreview struct {
	Objects          int
	Versions         int
	DeleteMarkers    int
	MultipartUploads int
}

// DeleteOptions configures Manager.Delete.
type DeleteOptions struct {
	// Unmanaged bypasses the ManagedBy=tack ownership check. Required to
	// delete a bucket tack didn't create.
	Unmanaged bool
}

// DeleteResult reports what Manager.Delete actually removed.
type DeleteResult struct {
	ObjectsRemoved          int
	VersionsRemoved         int
	DeleteMarkersRemoved    int
	MultipartUploadsAborted int
}

// deleteInventory is everything a delete pass needs: the full set of
// object identifiers to delete (keys for a never-versioned bucket, or
// key+versionId pairs covering every version and delete marker for a
// bucket that was ever versioned) plus any incomplete multipart uploads.
type deleteInventory struct {
	ids               []types.ObjectIdentifier
	objectCount       int
	versionCount      int
	deleteMarkerCount int
	multipart         []types.MultipartUpload
}

// Preview reports the object/version/delete-marker/multipart-upload counts
// Delete would remove, without deleting anything. Used to show the user
// what they're about to destroy before they confirm.
func (m *Manager) Preview(ctx context.Context) (*DeletePreview, error) {
	if err := m.Connect(ctx); err != nil {
		return nil, err
	}
	inv, err := m.buildDeleteInventory(ctx)
	if err != nil {
		return nil, err
	}
	return &DeletePreview{
		Objects:          inv.objectCount,
		Versions:         inv.versionCount,
		DeleteMarkers:    inv.deleteMarkerCount,
		MultipartUploads: len(inv.multipart),
	}, nil
}

// Delete empties and deletes the bucket. It assumes the bucket is very
// likely non-empty: it pages through every object (or, if the bucket was
// ever versioned, every version and delete marker), batch-deletes them,
// aborts incomplete multipart uploads, then deletes the bucket itself. If
// DeleteBucket reports the bucket is still non-empty (objects written
// concurrently after the delete pass completed), it retries the whole
// list-and-delete pass once more before giving up with an actionable
// error.
func (m *Manager) Delete(ctx context.Context, opts DeleteOptions) (*DeleteResult, error) {
	if err := m.Connect(ctx); err != nil {
		return nil, err
	}

	if !opts.Unmanaged {
		managed, err := m.isManaged(ctx)
		if err != nil {
			return nil, err
		}
		if !managed {
			return nil, fmt.Errorf("%w: %s", ErrNotManaged, m.bucket)
		}
	}

	result := &DeleteResult{}

	for attempt := 0; attempt <= deleteRetries; attempt++ {
		inv, err := m.buildDeleteInventory(ctx)
		if err != nil {
			return nil, err
		}

		if err := m.deleteAllObjects(ctx, inv.ids); err != nil {
			return nil, err
		}
		result.ObjectsRemoved += inv.objectCount
		result.VersionsRemoved += inv.versionCount
		result.DeleteMarkersRemoved += inv.deleteMarkerCount

		for _, upload := range inv.multipart {
			if _, err := m.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(m.bucket),
				Key:      upload.Key,
				UploadId: upload.UploadId,
			}); err != nil {
				return nil, fmt.Errorf("failed to abort multipart upload %q on bucket %s: %w", aws.ToString(upload.Key), m.bucket, err)
			}
		}
		result.MultipartUploadsAborted += len(inv.multipart)

		_, err = m.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(m.bucket)})
		if err == nil {
			return result, nil
		}
		if !isBucketNotEmpty(err) {
			return nil, fmt.Errorf("failed to delete bucket %s: %w", m.bucket, err)
		}
		// Bucket still non-empty: something was written concurrently.
		// Loop and try again (bounded by deleteRetries).
	}

	return nil, fmt.Errorf("bucket %s is still not empty after retrying deletion; objects may be getting written concurrently (e.g. a playbook still running against it) — re-run ssm-bucket delete", m.bucket)
}

// buildDeleteInventory lists everything Delete needs to remove: it checks
// the bucket's versioning status to decide whether a plain object listing
// suffices or every version and delete marker must be enumerated, then
// lists incomplete multipart uploads.
func (m *Manager) buildDeleteInventory(ctx context.Context) (*deleteInventory, error) {
	vOut, err := m.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		return nil, fmt.Errorf("failed to check versioning on bucket %s: %w", m.bucket, err)
	}

	inv := &deleteInventory{}

	// Status is "" if versioning was never turned on. "Enabled" or
	// "Suspended" both mean old versions/delete markers may exist and
	// must be enumerated explicitly.
	if vOut.Status == "" {
		ids, err := m.listPlainObjects(ctx)
		if err != nil {
			return nil, err
		}
		inv.ids = ids
		inv.objectCount = len(ids)
	} else {
		ids, versions, markers, err := m.listVersionedObjects(ctx)
		if err != nil {
			return nil, err
		}
		inv.ids = ids
		inv.versionCount = versions
		inv.deleteMarkerCount = markers
	}

	multipart, err := m.listMultipartUploads(ctx)
	if err != nil {
		return nil, err
	}
	inv.multipart = multipart

	return inv, nil
}

// listPlainObjects pages through every object in a never-versioned bucket.
func (m *Manager) listPlainObjects(ctx context.Context) ([]types.ObjectIdentifier, error) {
	var ids []types.ObjectIdentifier
	var token *string
	for {
		out, err := m.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(m.bucket),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list objects in bucket %s: %w", m.bucket, err)
		}
		for _, obj := range out.Contents {
			ids = append(ids, types.ObjectIdentifier{Key: obj.Key})
		}
		if !aws.ToBool(out.IsTruncated) {
			return ids, nil
		}
		token = out.NextContinuationToken
	}
}

// listVersionedObjects pages through every object version and delete
// marker in a bucket that was ever versioned (Enabled or Suspended).
func (m *Manager) listVersionedObjects(ctx context.Context) (ids []types.ObjectIdentifier, versions, deleteMarkers int, err error) {
	var keyMarker, versionMarker *string
	for {
		out, err2 := m.client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          aws.String(m.bucket),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
		})
		if err2 != nil {
			return nil, 0, 0, fmt.Errorf("failed to list object versions in bucket %s: %w", m.bucket, err2)
		}
		for _, v := range out.Versions {
			ids = append(ids, types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
			versions++
		}
		for _, d := range out.DeleteMarkers {
			ids = append(ids, types.ObjectIdentifier{Key: d.Key, VersionId: d.VersionId})
			deleteMarkers++
		}
		if !aws.ToBool(out.IsTruncated) {
			return ids, versions, deleteMarkers, nil
		}
		keyMarker = out.NextKeyMarker
		versionMarker = out.NextVersionIdMarker
	}
}

// listMultipartUploads pages through every incomplete multipart upload.
func (m *Manager) listMultipartUploads(ctx context.Context) ([]types.MultipartUpload, error) {
	var uploads []types.MultipartUpload
	var keyMarker, uploadIDMarker *string
	for {
		out, err := m.client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket:         aws.String(m.bucket),
			KeyMarker:      keyMarker,
			UploadIdMarker: uploadIDMarker,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list multipart uploads in bucket %s: %w", m.bucket, err)
		}
		uploads = append(uploads, out.Uploads...)
		if !aws.ToBool(out.IsTruncated) {
			return uploads, nil
		}
		keyMarker = out.NextKeyMarker
		uploadIDMarker = out.NextUploadIdMarker
	}
}

// deleteAllObjects batch-deletes ids, chunking at S3's 1000-key-per-request
// ceiling.
func (m *Manager) deleteAllObjects(ctx context.Context, ids []types.ObjectIdentifier) error {
	for len(ids) > 0 {
		n := len(ids)
		if n > maxDeleteBatch {
			n = maxDeleteBatch
		}
		batch := ids[:n]
		ids = ids[n:]

		out, err := m.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(m.bucket),
			Delete: &types.Delete{
				Objects: batch,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("failed to delete objects from bucket %s: %w", m.bucket, err)
		}
		if len(out.Errors) > 0 {
			first := out.Errors[0]
			return fmt.Errorf("failed to delete %d object(s) from bucket %s (e.g. %q: %s)",
				len(out.Errors), m.bucket, aws.ToString(first.Key), aws.ToString(first.Message))
		}
	}
	return nil
}
