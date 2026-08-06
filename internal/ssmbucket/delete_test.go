package ssmbucket

import (
	"context"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func managedTagging(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
	return &s3.GetBucketTaggingOutput{
		TagSet: []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("tack")}},
	}, nil
}

func TestDelete_EmptyBucket(t *testing.T) {
	var deleteBucketCalled bool
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			deleteBucketCalled = true
			return &s3.DeleteBucketOutput{}, nil
		},
	}
	m := New("empty-bucket", withS3AdminClient(mock))

	result, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.True(t, deleteBucketCalled)
	assert.Equal(t, 0, result.ObjectsRemoved)
}

func TestDelete_PaginatedObjects(t *testing.T) {
	// 2500 objects across 3 pages of ListObjectsV2 (1000/1000/500), then
	// deleted in 1000/1000/500 DeleteObjects batches.
	const total = 2500
	var pagesListed int
	var deletedKeys []string
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		listObjectsV2Fn: func(_ context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			pagesListed++
			start := 0
			if params.ContinuationToken != nil {
				start, _ = strconv.Atoi(aws.ToString(params.ContinuationToken))
			}
			end := start + 1000
			if end > total {
				end = total
			}
			var contents []types.Object
			for i := start; i < end; i++ {
				contents = append(contents, types.Object{Key: aws.String(keyFor(i))})
			}
			out := &s3.ListObjectsV2Output{Contents: contents}
			if end < total {
				out.IsTruncated = aws.Bool(true)
				out.NextContinuationToken = aws.String(strconv.Itoa(end))
			}
			return out, nil
		},
		deleteObjectsFn: func(_ context.Context, params *s3.DeleteObjectsInput) (*s3.DeleteObjectsOutput, error) {
			require.LessOrEqual(t, len(params.Delete.Objects), maxDeleteBatch)
			for _, obj := range params.Delete.Objects {
				deletedKeys = append(deletedKeys, aws.ToString(obj.Key))
			}
			return &s3.DeleteObjectsOutput{}, nil
		},
	}
	m := New("big-bucket", withS3AdminClient(mock))

	result, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.Equal(t, 3, pagesListed)
	assert.Equal(t, total, result.ObjectsRemoved)
	assert.Len(t, deletedKeys, total)
}

func keyFor(i int) string {
	return "tack-transfer/obj-" + string(rune('a'+i%26))
}

func TestDelete_VersionedBucketWithVersionsAndDeleteMarkers(t *testing.T) {
	var deletedIDs []types.ObjectIdentifier
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		getBucketVersioningFn: func(_ context.Context, _ *s3.GetBucketVersioningInput) (*s3.GetBucketVersioningOutput, error) {
			return &s3.GetBucketVersioningOutput{Status: types.BucketVersioningStatusEnabled}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []types.ObjectVersion{
					{Key: aws.String("a"), VersionId: aws.String("v1")},
					{Key: aws.String("a"), VersionId: aws.String("v2")},
				},
				DeleteMarkers: []types.DeleteMarkerEntry{
					{Key: aws.String("b"), VersionId: aws.String("dm1")},
				},
			}, nil
		},
		deleteObjectsFn: func(_ context.Context, params *s3.DeleteObjectsInput) (*s3.DeleteObjectsOutput, error) {
			deletedIDs = append(deletedIDs, params.Delete.Objects...)
			return &s3.DeleteObjectsOutput{}, nil
		},
	}
	m := New("versioned-bucket", withS3AdminClient(mock))

	result, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.VersionsRemoved)
	assert.Equal(t, 1, result.DeleteMarkersRemoved)
	assert.Len(t, deletedIDs, 3)
	for _, id := range deletedIDs {
		assert.NotEmpty(t, aws.ToString(id.VersionId), "versioned deletes must include VersionId")
	}
}

func TestDelete_SuspendedVersioningStillEnumeratesVersions(t *testing.T) {
	var usedListObjectVersions bool
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		getBucketVersioningFn: func(_ context.Context, _ *s3.GetBucketVersioningInput) (*s3.GetBucketVersioningOutput, error) {
			return &s3.GetBucketVersioningOutput{Status: types.BucketVersioningStatusSuspended}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput) (*s3.ListObjectVersionsOutput, error) {
			usedListObjectVersions = true
			return &s3.ListObjectVersionsOutput{}, nil
		},
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			t.Fatal("suspended-versioning bucket must use ListObjectVersions, not ListObjectsV2")
			return &s3.ListObjectsV2Output{}, nil
		},
	}
	m := New("suspended-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.True(t, usedListObjectVersions)
}

func TestDelete_AbortsIncompleteMultipartUploads(t *testing.T) {
	var aborted []string
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		listMultipartUploadsFn: func(_ context.Context, _ *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error) {
			return &s3.ListMultipartUploadsOutput{
				Uploads: []types.MultipartUpload{
					{Key: aws.String("big-file.bin"), UploadId: aws.String("upload-1")},
					{Key: aws.String("other-file.bin"), UploadId: aws.String("upload-2")},
				},
			}, nil
		},
		abortMultipartUploadFn: func(_ context.Context, params *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
			aborted = append(aborted, aws.ToString(params.UploadId))
			return &s3.AbortMultipartUploadOutput{}, nil
		},
	}
	m := New("mpu-bucket", withS3AdminClient(mock))

	result, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"upload-1", "upload-2"}, aborted)
	assert.Equal(t, 2, result.MultipartUploadsAborted)
}

func TestDelete_RefusesUnmanagedBucket(t *testing.T) {
	var deleteBucketCalled bool
	mock := &mockS3Admin{
		getBucketTaggingFn: func(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
			return &s3.GetBucketTaggingOutput{}, nil // no ManagedBy tag
		},
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			deleteBucketCalled = true
			return &s3.DeleteBucketOutput{}, nil
		},
	}
	m := New("unmanaged-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotManaged)
	assert.False(t, deleteBucketCalled, "must not delete anything before the ownership check passes")
}

func TestDelete_UnmanagedFlagBypassesOwnershipCheck(t *testing.T) {
	var deleteBucketCalled bool
	mock := &mockS3Admin{
		getBucketTaggingFn: func(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
			t.Fatal("ownership tag should not even be checked when Unmanaged is set")
			return nil, nil
		},
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			deleteBucketCalled = true
			return &s3.DeleteBucketOutput{}, nil
		},
	}
	m := New("random-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{Unmanaged: true})
	require.NoError(t, err)
	assert.True(t, deleteBucketCalled)
}

func TestDelete_ConcurrentWriteRetriesThenSucceeds(t *testing.T) {
	var deleteBucketAttempts int
	var inventoryPasses int
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			inventoryPasses++
			return &s3.ListObjectsV2Output{}, nil
		},
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			deleteBucketAttempts++
			if deleteBucketAttempts == 1 {
				return nil, &smithy.GenericAPIError{Code: "BucketNotEmpty"}
			}
			return &s3.DeleteBucketOutput{}, nil
		},
	}
	m := New("racy-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, deleteBucketAttempts)
	assert.Equal(t, 2, inventoryPasses, "must re-list before the retried delete")
}

func TestDelete_ConcurrentWriteExhaustsRetriesThenFails(t *testing.T) {
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "BucketNotEmpty"}
		},
	}
	m := New("stubborn-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-run ssm-bucket delete")
}

func TestDelete_PartialDeleteObjectsErrorSurfaced(t *testing.T) {
	mock := &mockS3Admin{
		getBucketTaggingFn: managedTagging,
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("locked-object")}}}, nil
		},
		deleteObjectsFn: func(_ context.Context, _ *s3.DeleteObjectsInput) (*s3.DeleteObjectsOutput, error) {
			return &s3.DeleteObjectsOutput{
				Errors: []types.Error{{Key: aws.String("locked-object"), Code: aws.String("AccessDenied"), Message: aws.String("Access Denied")}},
			}, nil
		},
	}
	m := New("locked-bucket", withS3AdminClient(mock))

	_, err := m.Delete(context.Background(), DeleteOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "locked-object")
}

func TestPreview_ReportsCountsWithoutDeleting(t *testing.T) {
	var deleteObjectsCalled, deleteBucketCalled bool
	mock := &mockS3Admin{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("a")}, {Key: aws.String("b")}}}, nil
		},
		listMultipartUploadsFn: func(_ context.Context, _ *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error) {
			return &s3.ListMultipartUploadsOutput{Uploads: []types.MultipartUpload{{Key: aws.String("x"), UploadId: aws.String("u1")}}}, nil
		},
		deleteObjectsFn: func(_ context.Context, _ *s3.DeleteObjectsInput) (*s3.DeleteObjectsOutput, error) {
			deleteObjectsCalled = true
			return &s3.DeleteObjectsOutput{}, nil
		},
		deleteBucketFn: func(_ context.Context, _ *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error) {
			deleteBucketCalled = true
			return &s3.DeleteBucketOutput{}, nil
		},
	}
	m := New("preview-bucket", withS3AdminClient(mock))

	preview, err := m.Preview(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, preview.Objects)
	assert.Equal(t, 1, preview.MultipartUploads)
	assert.False(t, deleteObjectsCalled)
	assert.False(t, deleteBucketCalled)
}
