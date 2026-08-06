package ssmbucket

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tackhq/tack/internal/connector"
	ssmconn "github.com/tackhq/tack/internal/connector/ssm"
)

// VerifyOptions configures Manager.Verify.
type VerifyOptions struct {
	// InstanceID is the SSM-managed instance to round-trip a test transfer
	// through. Required.
	InstanceID string

	// AttachPolicy has the underlying SSM connector temporarily attach a
	// scoped inline IAM policy granting the instance's role S3 access to
	// this bucket, if it doesn't already have it — the same mechanism
	// ssm.attach_s3_policy uses. Removed again when the connection closes.
	AttachPolicy bool
}

// VerifyResult reports a successful round trip.
type VerifyResult struct {
	// BytesTransferred is the size of the test payload that was uploaded,
	// downloaded, and confirmed to match.
	BytesTransferred int
}

// Verify performs a real upload+download round trip through the bucket via
// the named SSM-managed instance, to confirm the transfer path actually
// works end to end — not just that the bucket exists. It uploads a small
// random test payload to a throwaway path on the instance, downloads it
// back, confirms the content matches, and removes the remote test file.
func (m *Manager) Verify(ctx context.Context, opts VerifyOptions) (*VerifyResult, error) {
	if opts.InstanceID == "" {
		return nil, fmt.Errorf("verify requires an instance ID")
	}

	conn := m.newVerifyConnector(opts)
	if err := conn.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to instance %s: %w", opts.InstanceID, err)
	}
	defer conn.Close()

	payload := make([]byte, 32)
	if _, err := rand.Read(payload); err != nil {
		return nil, fmt.Errorf("failed to generate test payload: %w", err)
	}
	remotePath := fmt.Sprintf("/tmp/.tack-ssm-bucket-verify-%s", hex.EncodeToString(payload[:8]))

	if err := conn.Upload(ctx, bytes.NewReader(payload), remotePath, 0o600); err != nil {
		return nil, classifyVerifyError(err, opts)
	}

	var buf bytes.Buffer
	downloadErr := conn.Download(ctx, remotePath, &buf)

	// Best-effort remote cleanup regardless of download outcome — don't
	// leave the test file behind even if the round trip failed partway.
	_, _ = conn.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(remotePath)))

	if downloadErr != nil {
		return nil, classifyVerifyError(downloadErr, opts)
	}

	if !bytes.Equal(payload, buf.Bytes()) {
		return nil, fmt.Errorf("downloaded content did not match uploaded content (got %d bytes, want %d)", buf.Len(), len(payload))
	}

	return &VerifyResult{BytesTransferred: len(payload)}, nil
}

// newVerifyConnector builds the connector.Connector Verify drives: the
// real SSM connector by default, or an injected fake in tests.
func (m *Manager) newVerifyConnector(opts VerifyOptions) connector.Connector {
	if m.verifyConnectorFactory != nil {
		return m.verifyConnectorFactory(opts)
	}
	var connOpts []ssmconn.Option
	if m.region != "" {
		connOpts = append(connOpts, ssmconn.WithRegion(m.region))
	}
	connOpts = append(connOpts, ssmconn.WithBucket(m.bucket))
	if opts.AttachPolicy {
		connOpts = append(connOpts, ssmconn.WithAutoIAMPolicy())
	}
	return ssmconn.New(opts.InstanceID, connOpts...)
}

// classifyVerifyError turns a raw transfer failure into an actionable
// message: when it looks like an S3 access-denied error, it points the
// user at --attach-policy (or tells them attaching didn't help, if they
// already tried it).
func classifyVerifyError(err error, opts VerifyOptions) error {
	if !isAccessDeniedError(err) {
		return fmt.Errorf("round trip through bucket failed: %w", err)
	}
	if opts.AttachPolicy {
		return fmt.Errorf("round trip failed with an access-denied error even with --attach-policy: %w", err)
	}
	return fmt.Errorf("the instance's IAM role appears to lack S3 access to this bucket; retry with --attach-policy, or configure ssm.attach_s3_policy: %w", err)
}

// isAccessDeniedError reports whether err's message indicates an S3
// access-denied response. The underlying failure comes from the remote
// `aws s3 cp` command's stderr (see internal/connector/ssm), which
// includes the AWS CLI's "An error occurred (AccessDenied) ..." text.
func isAccessDeniedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "AccessDenied")
}
