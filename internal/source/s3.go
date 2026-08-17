package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3DownloadAPI is the subset of the S3 client used to fetch a playbook
// (and its sibling files, so roles/includes resolve) from a bucket.
type s3DownloadAPI interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3Source downloads a playbook from an S3 bucket using the AWS SDK
// directly — no dependency on the `aws` CLI being installed.
type S3Source struct {
	Bucket string
	Key    string

	// client is injected in tests; when nil, Fetch creates a real S3
	// client from the ambient AWS config.
	client s3DownloadAPI
}

func parseS3Source(ref string) (*S3Source, error) {
	// ref is "s3://bucket/key/to/playbook.yaml"
	without := strings.TrimPrefix(ref, "s3://")
	idx := strings.Index(without, "/")
	if idx == -1 {
		return nil, fmt.Errorf("invalid S3 path (missing key): %s", ref)
	}
	return &S3Source{
		Bucket: without[:idx],
		Key:    without[idx+1:],
	}, nil
}

func (s *S3Source) Fetch(ctx context.Context) (string, func(), error) {
	client := s.client
	if client == nil {
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return "", nil, fmt.Errorf("failed to load AWS config: %w", err)
		}
		client = s3.NewFromConfig(cfg)
	}

	tmpDir, err := os.MkdirTemp("", "tack-s3-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	// Download the containing "directory" recursively (not just the one
	// key) so roles/includes referenced relative to the playbook resolve.
	// An empty prefix means the key lives at the bucket root.
	dir := filepath.Dir(s.Key)
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}

	if err := downloadPrefix(ctx, client, s.Bucket, prefix, tmpDir); err != nil {
		cleanup()
		return "", nil, err
	}

	playbookPath := filepath.Join(tmpDir, filepath.Base(s.Key))
	if _, err := os.Stat(playbookPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("playbook not found after download: %s: %w", filepath.Base(s.Key), err)
	}

	return playbookPath, cleanup, nil
}

// downloadPrefix recursively downloads every object under prefix in
// bucket into destDir, preserving each key's path relative to prefix
// (mirroring `aws s3 cp --recursive`'s layout).
func downloadPrefix(ctx context.Context, client s3DownloadAPI, bucket, prefix, destDir string) error {
	var continuationToken *string
	found := false
	for {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return fmt.Errorf("failed to list s3://%s/%s: %w", bucket, prefix, err)
		}

		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			rel := strings.TrimPrefix(key, prefix)
			if rel == "" || strings.HasSuffix(key, "/") {
				continue // the prefix "directory" marker itself, not a file
			}
			found = true
			destPath := filepath.Join(destDir, filepath.FromSlash(rel))
			if err := downloadObject(ctx, client, bucket, key, destPath); err != nil {
				return err
			}
		}

		if !aws.ToBool(out.IsTruncated) {
			break
		}
		continuationToken = out.NextContinuationToken
	}

	if !found {
		return fmt.Errorf("no objects found at s3://%s/%s", bucket, prefix)
	}
	return nil
}

// downloadObject downloads a single S3 object to destPath, creating any
// missing parent directories.
func downloadObject(ctx context.Context, client s3DownloadAPI, bucket, key, destPath string) error {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to download s3://%s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", destPath, err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", destPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, out.Body); err != nil {
		return fmt.Errorf("failed to write %s: %w", destPath, err)
	}
	return nil
}

// Ensure the real S3 client satisfies s3DownloadAPI.
var _ s3DownloadAPI = (*s3.Client)(nil)
