package ssmbucket

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// CreateOptions configures Manager.Create.
type CreateOptions struct {
	// KMSKeyID, if set, enables SSE-KMS encryption using this key (ID,
	// alias, or ARN) instead of the default SSE-S3.
	KMSKeyID string

	// LifecycleDays is how many days transfer objects (under
	// tack-transfer/) live before expiring. Defaults to
	// defaultLifecycleDays when <= 0.
	LifecycleDays int32
}

// CreateResult reports the outcome of Manager.Create.
type CreateResult struct {
	// AlreadyExisted is true when the bucket was already owned by the
	// caller (Create still re-applied its configuration in that case).
	AlreadyExisted bool
}

// Create provisions (or converges the configuration of) the S3 bucket used
// for SSM file transfer: public access blocked, server-side encryption
// enabled, a lifecycle rule expiring tack-transfer/ objects, and a
// ManagedBy=tack tag identifying it as tack-owned. Safe to re-run — it
// treats an already-owned bucket as success and re-applies configuration
// every time, so it also serves as a "fix configuration drift" command.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (*CreateResult, error) {
	if err := m.Connect(ctx); err != nil {
		return nil, err
	}

	result := &CreateResult{}

	input := &s3.CreateBucketInput{Bucket: aws.String(m.bucket)}
	// us-east-1 is S3's default region: CreateBucket rejects an explicit
	// LocationConstraint of "us-east-1", so only set it for other regions.
	if m.region != "" && m.region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(m.region),
		}
	}
	if _, err := m.client.CreateBucket(ctx, input); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if errors.As(err, &owned) {
			// Already ours (returned in every region except us-east-1,
			// where a re-create of your own bucket returns 200 OK
			// instead — so we never even get here in that case).
			result.AlreadyExisted = true
		} else {
			return nil, fmt.Errorf("failed to create bucket %s: %w", m.bucket, err)
		}
	}

	if _, err := m.client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(m.bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to block public access on bucket %s: %w", m.bucket, err)
	}

	sseAlgorithm := types.ServerSideEncryptionAes256
	var kmsKeyID *string
	if opts.KMSKeyID != "" {
		sseAlgorithm = types.ServerSideEncryptionAwsKms
		kmsKeyID = aws.String(opts.KMSKeyID)
	}
	if _, err := m.client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(m.bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm:   sseAlgorithm,
						KMSMasterKeyID: kmsKeyID,
					},
				},
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to enable encryption on bucket %s: %w", m.bucket, err)
	}

	days := opts.LifecycleDays
	if days <= 0 {
		days = defaultLifecycleDays
	}
	if _, err := m.client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(m.bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{
				{
					ID:     aws.String(lifecycleRuleID),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{Prefix: aws.String(transferPrefix)},
					Expiration: &types.LifecycleExpiration{
						Days: aws.Int32(days),
					},
				},
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to set lifecycle rule on bucket %s: %w", m.bucket, err)
	}

	if _, err := m.client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(m.bucket),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String(tagKey), Value: aws.String(tagValue)},
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to tag bucket %s: %w", m.bucket, err)
	}

	return result, nil
}
