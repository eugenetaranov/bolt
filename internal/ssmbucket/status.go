package ssmbucket

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Status summarizes a bucket's existence and configuration.
type Status struct {
	// Exists is false if the bucket doesn't exist (every other field is
	// zero-valued in that case).
	Exists bool

	// Managed is true if the bucket carries the ManagedBy=tack tag.
	Managed bool

	// Encryption is "SSE-S3", "SSE-KMS", or "" if no default encryption is
	// configured.
	Encryption string
	// KMSKeyID is set when Encryption is "SSE-KMS".
	KMSKeyID string

	// PublicAccessBlocked is true only if all four public-access-block
	// settings are enabled.
	PublicAccessBlocked bool

	// Versioning is "", "Enabled", or "Suspended".
	Versioning string

	// LifecycleConfigured is true if a lifecycle rule for the
	// tack-transfer/ prefix exists.
	LifecycleConfigured bool
	// LifecycleDays is the expiration day count of that rule, if configured.
	LifecycleDays int32
}

// Status reports whether the bucket exists and, if so, its current
// configuration. A bucket that exists but wasn't created by tack (missing
// the ManagedBy=tack tag) is reported with Managed: false rather than an
// error.
func (m *Manager) Status(ctx context.Context) (*Status, error) {
	if err := m.Connect(ctx); err != nil {
		return nil, err
	}

	if _, err := m.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(m.bucket)}); err != nil {
		if isNoSuchBucket(err) {
			return &Status{Exists: false}, nil
		}
		return nil, fmt.Errorf("failed to check bucket %s: %w", m.bucket, err)
	}

	status := &Status{Exists: true}

	managed, err := m.isManaged(ctx)
	if err != nil {
		return nil, err
	}
	status.Managed = managed

	if err := m.fillEncryption(ctx, status); err != nil {
		return nil, err
	}
	if err := m.fillPublicAccessBlock(ctx, status); err != nil {
		return nil, err
	}
	if err := m.fillVersioning(ctx, status); err != nil {
		return nil, err
	}
	if err := m.fillLifecycle(ctx, status); err != nil {
		return nil, err
	}

	return status, nil
}

// isManaged reports whether the bucket carries the ManagedBy=tack tag.
func (m *Manager) isManaged(ctx context.Context) (bool, error) {
	out, err := m.client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		if isNoSuchTagSet(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check tags on bucket %s: %w", m.bucket, err)
	}
	for _, tag := range out.TagSet {
		if aws.ToString(tag.Key) == tagKey && aws.ToString(tag.Value) == tagValue {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) fillEncryption(ctx context.Context, status *Status) error {
	out, err := m.client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		if isServerSideEncryptionConfigurationNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to check encryption on bucket %s: %w", m.bucket, err)
	}
	if out.ServerSideEncryptionConfiguration == nil || len(out.ServerSideEncryptionConfiguration.Rules) == 0 {
		return nil
	}
	def := out.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault
	if def == nil {
		return nil
	}
	switch def.SSEAlgorithm {
	case types.ServerSideEncryptionAwsKms, types.ServerSideEncryptionAwsKmsDsse:
		status.Encryption = "SSE-KMS"
		status.KMSKeyID = aws.ToString(def.KMSMasterKeyID)
	default:
		status.Encryption = "SSE-S3"
	}
	return nil
}

func (m *Manager) fillPublicAccessBlock(ctx context.Context, status *Status) error {
	out, err := m.client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		// No public-access-block configuration at all reads as "not
		// blocked", same as if every field were false.
		if code, ok := apiErrorCode(err); ok && code == "NoSuchPublicAccessBlockConfiguration" {
			return nil
		}
		return fmt.Errorf("failed to check public access block on bucket %s: %w", m.bucket, err)
	}
	cfg := out.PublicAccessBlockConfiguration
	if cfg == nil {
		return nil
	}
	status.PublicAccessBlocked = aws.ToBool(cfg.BlockPublicAcls) &&
		aws.ToBool(cfg.BlockPublicPolicy) &&
		aws.ToBool(cfg.IgnorePublicAcls) &&
		aws.ToBool(cfg.RestrictPublicBuckets)
	return nil
}

func (m *Manager) fillVersioning(ctx context.Context, status *Status) error {
	out, err := m.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		return fmt.Errorf("failed to check versioning on bucket %s: %w", m.bucket, err)
	}
	status.Versioning = string(out.Status)
	return nil
}

func (m *Manager) fillLifecycle(ctx context.Context, status *Status) error {
	out, err := m.client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(m.bucket)})
	if err != nil {
		if isNoSuchLifecycleConfiguration(err) {
			return nil
		}
		return fmt.Errorf("failed to check lifecycle configuration on bucket %s: %w", m.bucket, err)
	}
	for _, rule := range out.Rules {
		if aws.ToString(rule.ID) != lifecycleRuleID {
			continue
		}
		status.LifecycleConfigured = true
		if rule.Expiration != nil {
			status.LifecycleDays = aws.ToInt32(rule.Expiration.Days)
		}
		break
	}
	return nil
}
