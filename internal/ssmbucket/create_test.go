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

func TestCreate_FreshBucket(t *testing.T) {
	var taggingCalled, encryptionCalled, publicAccessCalled, lifecycleCalled bool
	mock := &mockS3Admin{
		putBucketTaggingFn: func(_ context.Context, params *s3.PutBucketTaggingInput) (*s3.PutBucketTaggingOutput, error) {
			taggingCalled = true
			require.Len(t, params.Tagging.TagSet, 1)
			assert.Equal(t, "ManagedBy", aws.ToString(params.Tagging.TagSet[0].Key))
			assert.Equal(t, "tack", aws.ToString(params.Tagging.TagSet[0].Value))
			return &s3.PutBucketTaggingOutput{}, nil
		},
		putBucketEncryptionFn: func(_ context.Context, params *s3.PutBucketEncryptionInput) (*s3.PutBucketEncryptionOutput, error) {
			encryptionCalled = true
			rule := params.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault
			assert.Equal(t, types.ServerSideEncryptionAes256, rule.SSEAlgorithm)
			return &s3.PutBucketEncryptionOutput{}, nil
		},
		putPublicAccessBlockFn: func(_ context.Context, params *s3.PutPublicAccessBlockInput) (*s3.PutPublicAccessBlockOutput, error) {
			publicAccessCalled = true
			cfg := params.PublicAccessBlockConfiguration
			assert.True(t, aws.ToBool(cfg.BlockPublicAcls))
			assert.True(t, aws.ToBool(cfg.BlockPublicPolicy))
			assert.True(t, aws.ToBool(cfg.IgnorePublicAcls))
			assert.True(t, aws.ToBool(cfg.RestrictPublicBuckets))
			return &s3.PutPublicAccessBlockOutput{}, nil
		},
		putBucketLifecycleConfigurationFn: func(_ context.Context, params *s3.PutBucketLifecycleConfigurationInput) (*s3.PutBucketLifecycleConfigurationOutput, error) {
			lifecycleCalled = true
			rule := params.LifecycleConfiguration.Rules[0]
			assert.Equal(t, transferPrefix, aws.ToString(rule.Filter.Prefix))
			assert.Equal(t, int32(defaultLifecycleDays), aws.ToInt32(rule.Expiration.Days))
			return &s3.PutBucketLifecycleConfigurationOutput{}, nil
		},
	}
	m := New("my-bucket", withS3AdminClient(mock))

	result, err := m.Create(context.Background(), CreateOptions{})
	require.NoError(t, err)
	assert.False(t, result.AlreadyExisted)
	assert.True(t, taggingCalled)
	assert.True(t, encryptionCalled)
	assert.True(t, publicAccessCalled)
	assert.True(t, lifecycleCalled)
}

func TestCreate_IdempotentReRun(t *testing.T) {
	mock := &mockS3Admin{
		createBucketFn: func(_ context.Context, _ *s3.CreateBucketInput) (*s3.CreateBucketOutput, error) {
			return nil, &types.BucketAlreadyOwnedByYou{}
		},
	}
	m := New("my-bucket", withS3AdminClient(mock))

	result, err := m.Create(context.Background(), CreateOptions{})
	require.NoError(t, err)
	assert.True(t, result.AlreadyExisted)
}

func TestCreate_BucketAlreadyExists(t *testing.T) {
	mock := &mockS3Admin{
		createBucketFn: func(_ context.Context, _ *s3.CreateBucketInput) (*s3.CreateBucketOutput, error) {
			return nil, &types.BucketAlreadyExists{}
		},
	}
	m := New("taken-bucket", withS3AdminClient(mock))

	_, err := m.Create(context.Background(), CreateOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taken-bucket")
}

func TestCreate_KMSEncryption(t *testing.T) {
	var capturedAlgorithm types.ServerSideEncryption
	var capturedKeyID string
	mock := &mockS3Admin{
		putBucketEncryptionFn: func(_ context.Context, params *s3.PutBucketEncryptionInput) (*s3.PutBucketEncryptionOutput, error) {
			rule := params.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault
			capturedAlgorithm = rule.SSEAlgorithm
			capturedKeyID = aws.ToString(rule.KMSMasterKeyID)
			return &s3.PutBucketEncryptionOutput{}, nil
		},
	}
	m := New("my-bucket", withS3AdminClient(mock))

	_, err := m.Create(context.Background(), CreateOptions{KMSKeyID: "arn:aws:kms:us-east-1:111122223333:key/abc"})
	require.NoError(t, err)
	assert.Equal(t, types.ServerSideEncryptionAwsKms, capturedAlgorithm)
	assert.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/abc", capturedKeyID)
}

func TestCreate_CustomLifecycleDays(t *testing.T) {
	var capturedDays int32
	mock := &mockS3Admin{
		putBucketLifecycleConfigurationFn: func(_ context.Context, params *s3.PutBucketLifecycleConfigurationInput) (*s3.PutBucketLifecycleConfigurationOutput, error) {
			capturedDays = aws.ToInt32(params.LifecycleConfiguration.Rules[0].Expiration.Days)
			return &s3.PutBucketLifecycleConfigurationOutput{}, nil
		},
	}
	m := New("my-bucket", withS3AdminClient(mock))

	_, err := m.Create(context.Background(), CreateOptions{LifecycleDays: 30})
	require.NoError(t, err)
	assert.Equal(t, int32(30), capturedDays)
}

func TestCreate_UsEast1OmitsLocationConstraint(t *testing.T) {
	var captured *types.CreateBucketConfiguration
	mock := &mockS3Admin{
		createBucketFn: func(_ context.Context, params *s3.CreateBucketInput) (*s3.CreateBucketOutput, error) {
			captured = params.CreateBucketConfiguration
			return &s3.CreateBucketOutput{}, nil
		},
	}
	m := New("my-bucket", WithRegion("us-east-1"), withS3AdminClient(mock))

	_, err := m.Create(context.Background(), CreateOptions{})
	require.NoError(t, err)
	assert.Nil(t, captured)
}

func TestCreate_OtherRegionSetsLocationConstraint(t *testing.T) {
	var captured *types.CreateBucketConfiguration
	mock := &mockS3Admin{
		createBucketFn: func(_ context.Context, params *s3.CreateBucketInput) (*s3.CreateBucketOutput, error) {
			captured = params.CreateBucketConfiguration
			return &s3.CreateBucketOutput{}, nil
		},
	}
	m := New("my-bucket", WithRegion("eu-west-1"), withS3AdminClient(mock))

	_, err := m.Create(context.Background(), CreateOptions{})
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, types.BucketLocationConstraint("eu-west-1"), captured.LocationConstraint)
}

// sanity check that our error-code helpers actually work against the
// generic error type S3 returns for these codes.
func TestGenericAPIErrorCodeHelpers(t *testing.T) {
	assert.True(t, isNoSuchTagSet(&smithy.GenericAPIError{Code: "NoSuchTagSet"}))
	assert.True(t, isServerSideEncryptionConfigurationNotFound(&smithy.GenericAPIError{Code: "ServerSideEncryptionConfigurationNotFoundError"}))
	assert.True(t, isNoSuchLifecycleConfiguration(&smithy.GenericAPIError{Code: "NoSuchLifecycleConfiguration"}))
	assert.True(t, isBucketNotEmpty(&smithy.GenericAPIError{Code: "BucketNotEmpty"}))
	assert.True(t, isNoSuchBucket(&smithy.GenericAPIError{Code: "NoSuchBucket"}))
	assert.False(t, isNoSuchTagSet(&smithy.GenericAPIError{Code: "AccessDenied"}))
}
