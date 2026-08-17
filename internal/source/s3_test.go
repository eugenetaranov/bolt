package source

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockS3Download is a test double for s3DownloadAPI.
type mockS3Download struct {
	listObjectsV2Fn func(ctx context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
	getObjectFn     func(ctx context.Context, params *s3.GetObjectInput) (*s3.GetObjectOutput, error)
}

func (m *mockS3Download) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return m.listObjectsV2Fn(ctx, params)
}

func (m *mockS3Download) GetObject(ctx context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return m.getObjectFn(ctx, params)
}

func newObjectBody(content string) *s3.GetObjectOutput {
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(content))}
}

func TestS3Source_Fetch_Success(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			assert.Equal(t, "my-bucket", aws.ToString(params.Bucket))
			assert.Equal(t, "playbooks/", aws.ToString(params.Prefix))
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{Key: aws.String("playbooks/site.yaml")},
				},
			}, nil
		},
		getObjectFn: func(_ context.Context, params *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			assert.Equal(t, "playbooks/site.yaml", aws.ToString(params.Key))
			return newObjectBody("name: test\n"), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	path, cleanup, err := s.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, "site.yaml", filepath.Base(path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "name: test\n", string(data))
}

func TestS3Source_Fetch_PreservesRelativeStructure(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{Key: aws.String("playbooks/site.yaml")},
					{Key: aws.String("playbooks/roles/webserver/tasks/main.yaml")},
				},
			}, nil
		},
		getObjectFn: func(_ context.Context, params *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return newObjectBody("key=" + aws.ToString(params.Key)), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	path, cleanup, err := s.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	roleTasksPath := filepath.Join(filepath.Dir(path), "roles", "webserver", "tasks", "main.yaml")
	data, err := os.ReadFile(roleTasksPath)
	require.NoError(t, err, "nested object should be downloaded preserving its path relative to the prefix")
	assert.Equal(t, "key=playbooks/roles/webserver/tasks/main.yaml", string(data))
}

func TestS3Source_Fetch_Pagination(t *testing.T) {
	var seenTokens []string
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			token := aws.ToString(params.ContinuationToken)
			seenTokens = append(seenTokens, token)
			if token == "" {
				return &s3.ListObjectsV2Output{
					Contents:              []types.Object{{Key: aws.String("playbooks/a.yaml")}},
					IsTruncated:           aws.Bool(true),
					NextContinuationToken: aws.String("page2"),
				}, nil
			}
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{{Key: aws.String("playbooks/site.yaml")}},
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return newObjectBody("x"), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	path, cleanup, err := s.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, []string{"", "page2"}, seenTokens)
	_, err = os.Stat(filepath.Join(filepath.Dir(path), "a.yaml"))
	require.NoError(t, err, "objects from every page should be downloaded")
}

func TestS3Source_Fetch_SkipsDirectoryMarkers(t *testing.T) {
	var downloadedKeys []string
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{Key: aws.String("playbooks/")}, // the prefix "directory" marker itself
					{Key: aws.String("playbooks/site.yaml")},
				},
			}, nil
		},
		getObjectFn: func(_ context.Context, params *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			downloadedKeys = append(downloadedKeys, aws.ToString(params.Key))
			return newObjectBody("x"), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	_, cleanup, err := s.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, []string{"playbooks/site.yaml"}, downloadedKeys)
}

func TestS3Source_Fetch_RootKeyUsesEmptyPrefix(t *testing.T) {
	var capturedPrefix *string
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			capturedPrefix = params.Prefix
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{{Key: aws.String("site.yaml")}},
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return newObjectBody("x"), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "site.yaml", client: mock}

	_, cleanup, err := s.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	require.NotNil(t, capturedPrefix)
	assert.Equal(t, "", aws.ToString(capturedPrefix))
}

func TestS3Source_Fetch_PlaybookKeyNotAmongDownloadedObjects(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{{Key: aws.String("playbooks/other.yaml")}},
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return newObjectBody("x"), nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	_, _, err := s.Fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found after download")
}

func TestS3Source_Fetch_NoObjectsFound(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{}, nil
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	_, _, err := s.Fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no objects found")
}

func TestS3Source_Fetch_ListError(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return nil, assert.AnError
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	_, _, err := s.Fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list")
}

func TestS3Source_Fetch_GetObjectError(t *testing.T) {
	mock := &mockS3Download{
		listObjectsV2Fn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{{Key: aws.String("playbooks/site.yaml")}},
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return nil, assert.AnError
		},
	}
	s := &S3Source{Bucket: "my-bucket", Key: "playbooks/site.yaml", client: mock}

	_, _, err := s.Fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to download")
}
