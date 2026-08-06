package ssmbucket

import (
	"errors"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	smithy "github.com/aws/smithy-go"
)

// apiErrorCode extracts the S3 error code from err, if it carries one.
// Several S3 "not configured" responses (GetBucketTagging with no tags,
// GetBucketEncryption with no config, GetBucketLifecycleConfiguration with
// no rules, DeleteBucket on a non-empty bucket) aren't modeled as concrete
// Go error types in aws-sdk-go-v2/service/s3/types — they only surface
// through the generic smithy.APIError code.
func apiErrorCode(err error) (string, bool) {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode(), true
	}
	return "", false
}

// isNotFoundStatus reports whether err carries an HTTP 404, as a fallback
// for operations (like HeadBucket) that may not surface a typed/coded
// error body.
func isNotFoundStatus(err error) bool {
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode() == 404
	}
	return false
}

// isNoSuchTagSet reports whether err is S3's "no tags configured" response
// to GetBucketTagging.
func isNoSuchTagSet(err error) bool {
	code, ok := apiErrorCode(err)
	return ok && code == "NoSuchTagSet"
}

// isNoSuchLifecycleConfiguration reports whether err is S3's "no lifecycle
// configured" response to GetBucketLifecycleConfiguration.
func isNoSuchLifecycleConfiguration(err error) bool {
	code, ok := apiErrorCode(err)
	return ok && code == "NoSuchLifecycleConfiguration"
}

// isServerSideEncryptionConfigurationNotFound reports whether err is S3's
// "no default encryption configured" response to GetBucketEncryption.
func isServerSideEncryptionConfigurationNotFound(err error) bool {
	code, ok := apiErrorCode(err)
	return ok && code == "ServerSideEncryptionConfigurationNotFoundError"
}

// isBucketNotEmpty reports whether err is S3's response to DeleteBucket
// when objects still remain.
func isBucketNotEmpty(err error) bool {
	code, ok := apiErrorCode(err)
	return ok && code == "BucketNotEmpty"
}

// isNoSuchBucket reports whether err indicates the bucket doesn't exist,
// across the different shapes S3 operations return that in (typed
// NoSuchBucket/NotFound errors, a generic coded error, or a bare HTTP 404
// from HeadBucket).
func isNoSuchBucket(err error) bool {
	if code, ok := apiErrorCode(err); ok {
		switch code {
		case "NoSuchBucket", "NotFound":
			return true
		}
	}
	return isNotFoundStatus(err)
}
