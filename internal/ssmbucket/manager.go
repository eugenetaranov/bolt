// Package ssmbucket manages the lifecycle of the S3 bucket used by the SSM
// connector for large file transfers (see internal/connector/ssm). It is
// intentionally separate from that connector: transferring a file only
// needs PutObject/GetObject/DeleteObject on a single key, while managing
// the bucket itself (create/status/delete) needs a much wider,
// higher-privilege set of S3 admin operations. Keeping them in different
// packages keeps each client's mock surface and blast radius scoped to
// what it actually does.
package ssmbucket

import (
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/tackhq/tack/internal/connector"
)

// transferPrefix is the S3 key prefix the SSM connector writes transfer
// objects under (internal/connector/ssm.s3KeyPrefix). Duplicated here as a
// literal rather than imported since it's an unexported connector detail;
// keep the two in sync if it ever changes.
const transferPrefix = "tack-transfer/"

// Bucket ownership tag. Set by Create, checked by Delete (and reported by
// Status) to distinguish buckets tack created from arbitrary buckets a user
// might accidentally point --name at.
const (
	tagKey   = "ManagedBy"
	tagValue = "tack"
)

// lifecycleRuleID identifies the lifecycle rule Create manages, so re-runs
// update the same rule instead of accumulating duplicates.
const lifecycleRuleID = "tack-transfer-expiry"

// defaultLifecycleDays is used when CreateOptions.LifecycleDays is unset.
const defaultLifecycleDays = 1

// deleteRetries is how many additional list+delete passes Delete attempts
// if DeleteBucket reports the bucket is still non-empty (a task could have
// written a new object after the delete pass completed).
const deleteRetries = 1

// s3AdminAPI is the subset of the S3 client used for bucket administration
// (create/status/delete). Deliberately separate from the connector's
// transfer-only s3API.
type s3AdminAPI interface {
	CreateBucket(ctx context.Context, params *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	DeleteBucket(ctx context.Context, params *s3.DeleteBucketInput, optFns ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)

	PutBucketTagging(ctx context.Context, params *s3.PutBucketTaggingInput, optFns ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error)
	GetBucketTagging(ctx context.Context, params *s3.GetBucketTaggingInput, optFns ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)

	PutBucketEncryption(ctx context.Context, params *s3.PutBucketEncryptionInput, optFns ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error)
	GetBucketEncryption(ctx context.Context, params *s3.GetBucketEncryptionInput, optFns ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error)

	PutPublicAccessBlock(ctx context.Context, params *s3.PutPublicAccessBlockInput, optFns ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error)
	GetPublicAccessBlock(ctx context.Context, params *s3.GetPublicAccessBlockInput, optFns ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error)

	PutBucketLifecycleConfiguration(ctx context.Context, params *s3.PutBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	GetBucketLifecycleConfiguration(ctx context.Context, params *s3.GetBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)

	GetBucketVersioning(ctx context.Context, params *s3.GetBucketVersioningInput, optFns ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)

	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	ListObjectVersions(ctx context.Context, params *s3.ListObjectVersionsInput, optFns ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)

	ListMultipartUploads(ctx context.Context, params *s3.ListMultipartUploadsInput, optFns ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error)
	AbortMultipartUpload(ctx context.Context, params *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// Manager administers the lifecycle of a single S3 bucket used for SSM
// file transfer.
type Manager struct {
	bucket  string
	region  string
	timeout time.Duration
	client  s3AdminAPI

	// verifyConnectorFactory builds the connector.Connector Verify drives.
	// nil means use the real SSM connector (defaultVerifyConnector);
	// overridable in tests via withVerifyConnectorFactory.
	verifyConnectorFactory func(VerifyOptions) connector.Connector
}

// Option configures a Manager.
type Option func(*Manager)

// WithRegion sets the AWS region.
func WithRegion(region string) Option {
	return func(m *Manager) {
		m.region = region
	}
}

// WithTimeout sets the timeout used for AWS API calls made by Connect (not
// individually per-call — callers control per-call timeouts via ctx).
func WithTimeout(d time.Duration) Option {
	return func(m *Manager) {
		m.timeout = d
	}
}

// withS3AdminClient injects a custom S3 admin client (for testing).
func withS3AdminClient(client s3AdminAPI) Option {
	return func(m *Manager) {
		m.client = client
	}
}

// withVerifyConnectorFactory injects a fake connector.Connector factory
// for Verify (for testing), avoiding the need to reach into the SSM
// connector's own internal client mocks from this package.
func withVerifyConnectorFactory(f func(VerifyOptions) connector.Connector) Option {
	return func(m *Manager) {
		m.verifyConnectorFactory = f
	}
}

// New creates a new Manager for the named bucket.
func New(bucket string, opts ...Option) *Manager {
	m := &Manager{bucket: bucket}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Connect loads the AWS config and creates a real S3 client, if one wasn't
// already injected (e.g. by tests).
func (m *Manager) Connect(ctx context.Context) error {
	if m.client != nil {
		return nil
	}
	var optFns []func(*awsconfig.LoadOptions) error
	if m.region != "" {
		optFns = append(optFns, awsconfig.WithRegion(m.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}
	m.client = s3.NewFromConfig(cfg)
	return nil
}

// Ensure the real S3 client satisfies s3AdminAPI.
var _ s3AdminAPI = (*s3.Client)(nil)
