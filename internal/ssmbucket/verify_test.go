package ssmbucket

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tackhq/tack/internal/connector"
)

// fakeConnector is a minimal connector.Connector test double: Upload
// stores what was written, Download echoes it back (unless overridden),
// letting Verify's round-trip logic be exercised without reaching into
// the real SSM connector's own (unexported) client mocks.
type fakeConnector struct {
	connectErr  error
	uploadErr   error
	downloadErr error
	// downloadOverride, if set, is returned by Download instead of
	// echoing back whatever was uploaded (to simulate corruption).
	downloadOverride []byte

	stored      []byte
	executedCmd []string
	closeCalled bool
}

func (f *fakeConnector) Connect(context.Context) error { return f.connectErr }

func (f *fakeConnector) Execute(_ context.Context, cmd string) (*connector.Result, error) {
	f.executedCmd = append(f.executedCmd, cmd)
	return &connector.Result{ExitCode: 0}, nil
}

func (f *fakeConnector) Upload(_ context.Context, src io.Reader, _ string, _ uint32) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	f.stored = data
	return nil
}

func (f *fakeConnector) Download(_ context.Context, _ string, dst io.Writer) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	data := f.stored
	if f.downloadOverride != nil {
		data = f.downloadOverride
	}
	_, err := dst.Write(data)
	return err
}

func (f *fakeConnector) SetSudo(bool, string) {}

func (f *fakeConnector) Close() error {
	f.closeCalled = true
	return nil
}

func (f *fakeConnector) String() string { return "fake://test-instance" }

var _ connector.Connector = (*fakeConnector)(nil)

func TestVerify_SuccessWithoutAttachPolicy(t *testing.T) {
	fake := &fakeConnector{}
	var capturedOpts VerifyOptions
	m := New("my-bucket", withVerifyConnectorFactory(func(opts VerifyOptions) connector.Connector {
		capturedOpts = opts
		return fake
	}))

	result, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.NoError(t, err)
	assert.Equal(t, 32, result.BytesTransferred)
	assert.False(t, capturedOpts.AttachPolicy)
	assert.True(t, fake.closeCalled, "Verify must Close the connector so any attached IAM policy is detached")
	require.Len(t, fake.executedCmd, 1)
	assert.Contains(t, fake.executedCmd[0], "rm -f")
}

func TestVerify_SuccessWithAttachPolicy(t *testing.T) {
	fake := &fakeConnector{}
	var capturedOpts VerifyOptions
	m := New("my-bucket", withVerifyConnectorFactory(func(opts VerifyOptions) connector.Connector {
		capturedOpts = opts
		return fake
	}))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123", AttachPolicy: true})
	require.NoError(t, err)
	assert.True(t, capturedOpts.AttachPolicy)
}

func TestVerify_RequiresInstanceID(t *testing.T) {
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector {
		t.Fatal("connector should never be built when InstanceID is missing")
		return nil
	}))

	_, err := m.Verify(context.Background(), VerifyOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance")
}

func TestVerify_AccessDeniedGivesActionableRemedies(t *testing.T) {
	fake := &fakeConnector{
		uploadErr: errors.New("failed to copy from S3 to /tmp/x: An error occurred (AccessDenied) when calling the PutObject operation: Access Denied"),
	}
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector { return fake }))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.Error(t, err)
	// Auto-attach is the default now; the remedy points at the real fixes,
	// not the retired --attach-policy flag.
	assert.NotContains(t, err.Error(), "--attach-policy")
	assert.Contains(t, err.Error(), "iam:PutRolePolicy")
	assert.Contains(t, err.Error(), "pre-provision the instance role")
	assert.True(t, fake.closeCalled)
}

func TestVerify_NonAccessDeniedErrorNotMisclassified(t *testing.T) {
	fake := &fakeConnector{uploadErr: errors.New("connection timed out")}
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector { return fake }))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--attach-policy")
	assert.Contains(t, err.Error(), "connection timed out")
}

func TestVerify_DownloadErrorStillCleansUpRemoteFile(t *testing.T) {
	fake := &fakeConnector{downloadErr: errors.New("network blip")}
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector { return fake }))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.Error(t, err)
	require.Len(t, fake.executedCmd, 1, "must still attempt remote cleanup after a download failure")
	assert.Contains(t, fake.executedCmd[0], "rm -f")
}

func TestVerify_ContentMismatchFails(t *testing.T) {
	fake := &fakeConnector{downloadOverride: []byte("not what was uploaded, wrong length!!")}
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector { return fake }))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not match")
}

func TestVerify_ConnectFailurePropagates(t *testing.T) {
	fake := &fakeConnector{connectErr: errors.New("instance not managed by SSM")}
	m := New("my-bucket", withVerifyConnectorFactory(func(VerifyOptions) connector.Connector { return fake }))

	_, err := m.Verify(context.Background(), VerifyOptions{InstanceID: "i-test123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance not managed by SSM")
	assert.False(t, fake.closeCalled, "must not Close a connector that never connected")
}
