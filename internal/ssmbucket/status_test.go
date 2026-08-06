package ssmbucket

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_ExistingManagedBucket(t *testing.T) {
	mock := &mockS3Admin{
		getBucketTaggingFn: func(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
			return &s3.GetBucketTaggingOutput{
				TagSet: []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("tack")}},
			}, nil
		},
		getBucketEncryptionFn: func(_ context.Context, _ *s3.GetBucketEncryptionInput) (*s3.GetBucketEncryptionOutput, error) {
			return &s3.GetBucketEncryptionOutput{
				ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
					Rules: []types.ServerSideEncryptionRule{
						{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{SSEAlgorithm: types.ServerSideEncryptionAes256}},
					},
				},
			}, nil
		},
		getPublicAccessBlockFn: func(_ context.Context, _ *s3.GetPublicAccessBlockInput) (*s3.GetPublicAccessBlockOutput, error) {
			return &s3.GetPublicAccessBlockOutput{
				PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
					BlockPublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(true),
					IgnorePublicAcls: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true),
				},
			}, nil
		},
		getBucketVersioningFn: func(_ context.Context, _ *s3.GetBucketVersioningInput) (*s3.GetBucketVersioningOutput, error) {
			return &s3.GetBucketVersioningOutput{}, nil
		},
		getBucketLifecycleConfigurationFn: func(_ context.Context, _ *s3.GetBucketLifecycleConfigurationInput) (*s3.GetBucketLifecycleConfigurationOutput, error) {
			return &s3.GetBucketLifecycleConfigurationOutput{
				Rules: []types.LifecycleRule{
					{ID: aws.String(lifecycleRuleID), Status: types.ExpirationStatusEnabled, Expiration: &types.LifecycleExpiration{Days: aws.Int32(1)}},
				},
			}, nil
		},
	}
	m := New("my-bucket", withS3AdminClient(mock))

	status, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Exists)
	assert.True(t, status.Managed)
	assert.Equal(t, "SSE-S3", status.Encryption)
	assert.True(t, status.PublicAccessBlocked)
	assert.True(t, status.LifecycleConfigured)
	assert.Equal(t, int32(1), status.LifecycleDays)
}

func TestStatus_NonExistentBucket(t *testing.T) {
	mock := &mockS3Admin{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
			return nil, &types.NotFound{}
		},
	}
	m := New("ghost-bucket", withS3AdminClient(mock))

	status, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, status.Exists)
	assert.False(t, status.Managed)
}

func TestStatus_ExistingButUnmanagedBucket(t *testing.T) {
	mock := &mockS3Admin{
		getBucketTaggingFn: func(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
			return &s3.GetBucketTaggingOutput{
				TagSet: []types.Tag{{Key: aws.String("Owner"), Value: aws.String("someone-else")}},
			}, nil
		},
	}
	m := New("someone-elses-bucket", withS3AdminClient(mock))

	status, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Exists)
	assert.False(t, status.Managed)
}

func TestStatus_NoLifecycleOrEncryptionConfigured(t *testing.T) {
	mock := &mockS3Admin{
		getBucketTaggingFn: func(_ context.Context, _ *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "NoSuchTagSet"}
		},
		getBucketEncryptionFn: func(_ context.Context, _ *s3.GetBucketEncryptionInput) (*s3.GetBucketEncryptionOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "ServerSideEncryptionConfigurationNotFoundError"}
		},
		getBucketLifecycleConfigurationFn: func(_ context.Context, _ *s3.GetBucketLifecycleConfigurationInput) (*s3.GetBucketLifecycleConfigurationOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "NoSuchLifecycleConfiguration"}
		},
	}
	m := New("bare-bucket", withS3AdminClient(mock))

	status, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Exists)
	assert.False(t, status.Managed)
	assert.Empty(t, status.Encryption)
	assert.False(t, status.LifecycleConfigured)
}

func TestStatus_KMSEncryption(t *testing.T) {
	mock := &mockS3Admin{
		getBucketEncryptionFn: func(_ context.Context, _ *s3.GetBucketEncryptionInput) (*s3.GetBucketEncryptionOutput, error) {
			return &s3.GetBucketEncryptionOutput{
				ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
					Rules: []types.ServerSideEncryptionRule{
						{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
							SSEAlgorithm:   types.ServerSideEncryptionAwsKms,
							KMSMasterKeyID: aws.String("arn:aws:kms:us-east-1:111122223333:key/abc"),
						}},
					},
				},
			}, nil
		},
	}
	m := New("kms-bucket", withS3AdminClient(mock))

	status, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "SSE-KMS", status.Encryption)
	assert.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/abc", status.KMSKeyID)
}
