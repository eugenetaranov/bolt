package ssmbucket

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// mockS3Admin is a test double for s3AdminAPI. Each operation has an
// overridable function field; unset ones return an empty success response.
type mockS3Admin struct {
	createBucketFn                    func(ctx context.Context, params *s3.CreateBucketInput) (*s3.CreateBucketOutput, error)
	headBucketFn                      func(ctx context.Context, params *s3.HeadBucketInput) (*s3.HeadBucketOutput, error)
	deleteBucketFn                    func(ctx context.Context, params *s3.DeleteBucketInput) (*s3.DeleteBucketOutput, error)
	putBucketTaggingFn                func(ctx context.Context, params *s3.PutBucketTaggingInput) (*s3.PutBucketTaggingOutput, error)
	getBucketTaggingFn                func(ctx context.Context, params *s3.GetBucketTaggingInput) (*s3.GetBucketTaggingOutput, error)
	putBucketEncryptionFn             func(ctx context.Context, params *s3.PutBucketEncryptionInput) (*s3.PutBucketEncryptionOutput, error)
	getBucketEncryptionFn             func(ctx context.Context, params *s3.GetBucketEncryptionInput) (*s3.GetBucketEncryptionOutput, error)
	putPublicAccessBlockFn            func(ctx context.Context, params *s3.PutPublicAccessBlockInput) (*s3.PutPublicAccessBlockOutput, error)
	getPublicAccessBlockFn            func(ctx context.Context, params *s3.GetPublicAccessBlockInput) (*s3.GetPublicAccessBlockOutput, error)
	putBucketLifecycleConfigurationFn func(ctx context.Context, params *s3.PutBucketLifecycleConfigurationInput) (*s3.PutBucketLifecycleConfigurationOutput, error)
	getBucketLifecycleConfigurationFn func(ctx context.Context, params *s3.GetBucketLifecycleConfigurationInput) (*s3.GetBucketLifecycleConfigurationOutput, error)
	getBucketVersioningFn             func(ctx context.Context, params *s3.GetBucketVersioningInput) (*s3.GetBucketVersioningOutput, error)
	listObjectsV2Fn                   func(ctx context.Context, params *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
	listObjectVersionsFn              func(ctx context.Context, params *s3.ListObjectVersionsInput) (*s3.ListObjectVersionsOutput, error)
	deleteObjectsFn                   func(ctx context.Context, params *s3.DeleteObjectsInput) (*s3.DeleteObjectsOutput, error)
	listMultipartUploadsFn            func(ctx context.Context, params *s3.ListMultipartUploadsInput) (*s3.ListMultipartUploadsOutput, error)
	abortMultipartUploadFn            func(ctx context.Context, params *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error)
}

func (m *mockS3Admin) CreateBucket(ctx context.Context, params *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	if m.createBucketFn != nil {
		return m.createBucketFn(ctx, params)
	}
	return &s3.CreateBucketOutput{}, nil
}

func (m *mockS3Admin) HeadBucket(ctx context.Context, params *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if m.headBucketFn != nil {
		return m.headBucketFn(ctx, params)
	}
	return &s3.HeadBucketOutput{}, nil
}

func (m *mockS3Admin) DeleteBucket(ctx context.Context, params *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	if m.deleteBucketFn != nil {
		return m.deleteBucketFn(ctx, params)
	}
	return &s3.DeleteBucketOutput{}, nil
}

func (m *mockS3Admin) PutBucketTagging(ctx context.Context, params *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	if m.putBucketTaggingFn != nil {
		return m.putBucketTaggingFn(ctx, params)
	}
	return &s3.PutBucketTaggingOutput{}, nil
}

func (m *mockS3Admin) GetBucketTagging(ctx context.Context, params *s3.GetBucketTaggingInput, _ ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	if m.getBucketTaggingFn != nil {
		return m.getBucketTaggingFn(ctx, params)
	}
	return &s3.GetBucketTaggingOutput{}, nil
}

func (m *mockS3Admin) PutBucketEncryption(ctx context.Context, params *s3.PutBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error) {
	if m.putBucketEncryptionFn != nil {
		return m.putBucketEncryptionFn(ctx, params)
	}
	return &s3.PutBucketEncryptionOutput{}, nil
}

func (m *mockS3Admin) GetBucketEncryption(ctx context.Context, params *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	if m.getBucketEncryptionFn != nil {
		return m.getBucketEncryptionFn(ctx, params)
	}
	return &s3.GetBucketEncryptionOutput{}, nil
}

func (m *mockS3Admin) PutPublicAccessBlock(ctx context.Context, params *s3.PutPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	if m.putPublicAccessBlockFn != nil {
		return m.putPublicAccessBlockFn(ctx, params)
	}
	return &s3.PutPublicAccessBlockOutput{}, nil
}

func (m *mockS3Admin) GetPublicAccessBlock(ctx context.Context, params *s3.GetPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error) {
	if m.getPublicAccessBlockFn != nil {
		return m.getPublicAccessBlockFn(ctx, params)
	}
	return &s3.GetPublicAccessBlockOutput{}, nil
}

func (m *mockS3Admin) PutBucketLifecycleConfiguration(ctx context.Context, params *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	if m.putBucketLifecycleConfigurationFn != nil {
		return m.putBucketLifecycleConfigurationFn(ctx, params)
	}
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func (m *mockS3Admin) GetBucketLifecycleConfiguration(ctx context.Context, params *s3.GetBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	if m.getBucketLifecycleConfigurationFn != nil {
		return m.getBucketLifecycleConfigurationFn(ctx, params)
	}
	return &s3.GetBucketLifecycleConfigurationOutput{}, nil
}

func (m *mockS3Admin) GetBucketVersioning(ctx context.Context, params *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	if m.getBucketVersioningFn != nil {
		return m.getBucketVersioningFn(ctx, params)
	}
	return &s3.GetBucketVersioningOutput{}, nil
}

func (m *mockS3Admin) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if m.listObjectsV2Fn != nil {
		return m.listObjectsV2Fn(ctx, params)
	}
	return &s3.ListObjectsV2Output{}, nil
}

func (m *mockS3Admin) ListObjectVersions(ctx context.Context, params *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	if m.listObjectVersionsFn != nil {
		return m.listObjectVersionsFn(ctx, params)
	}
	return &s3.ListObjectVersionsOutput{}, nil
}

func (m *mockS3Admin) DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if m.deleteObjectsFn != nil {
		return m.deleteObjectsFn(ctx, params)
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (m *mockS3Admin) ListMultipartUploads(ctx context.Context, params *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	if m.listMultipartUploadsFn != nil {
		return m.listMultipartUploadsFn(ctx, params)
	}
	return &s3.ListMultipartUploadsOutput{}, nil
}

func (m *mockS3Admin) AbortMultipartUpload(ctx context.Context, params *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	if m.abortMultipartUploadFn != nil {
		return m.abortMultipartUploadFn(ctx, params)
	}
	return &s3.AbortMultipartUploadOutput{}, nil
}
