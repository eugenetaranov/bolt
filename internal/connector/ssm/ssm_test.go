package ssm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tackhq/tack/internal/connector"
)

// --- Mock SSM client ---

type mockSSM struct {
	describeInstanceInfoFn func(ctx context.Context, params *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error)
	sendCommandFn          func(ctx context.Context, params *ssm.SendCommandInput) (*ssm.SendCommandOutput, error)
	getCommandInvocationFn func(ctx context.Context, params *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error)
	cancelCommandFn        func(ctx context.Context, params *ssm.CancelCommandInput) (*ssm.CancelCommandOutput, error)
}

func (m *mockSSM) DescribeInstanceInformation(ctx context.Context, params *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	if m.describeInstanceInfoFn != nil {
		return m.describeInstanceInfoFn(ctx, params)
	}
	return &ssm.DescribeInstanceInformationOutput{
		InstanceInformationList: []ssmtypes.InstanceInformation{
			{InstanceId: aws.String("i-test123")},
		},
	}, nil
}

func (m *mockSSM) SendCommand(ctx context.Context, params *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	if m.sendCommandFn != nil {
		return m.sendCommandFn(ctx, params)
	}
	return &ssm.SendCommandOutput{
		Command: &ssmtypes.Command{CommandId: aws.String("cmd-123")},
	}, nil
}

func (m *mockSSM) GetCommandInvocation(ctx context.Context, params *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	if m.getCommandInvocationFn != nil {
		return m.getCommandInvocationFn(ctx, params)
	}
	return &ssm.GetCommandInvocationOutput{
		Status:                ssmtypes.CommandInvocationStatusSuccess,
		StandardOutputContent: aws.String("hello"),
		StandardErrorContent:  aws.String(""),
	}, nil
}

func (m *mockSSM) CancelCommand(ctx context.Context, params *ssm.CancelCommandInput, _ ...func(*ssm.Options)) (*ssm.CancelCommandOutput, error) {
	if m.cancelCommandFn != nil {
		return m.cancelCommandFn(ctx, params)
	}
	return &ssm.CancelCommandOutput{}, nil
}

// --- Mock S3 client ---

type mockS3 struct {
	putObjectFn    func(ctx context.Context, params *s3.PutObjectInput) (*s3.PutObjectOutput, error)
	getObjectFn    func(ctx context.Context, params *s3.GetObjectInput) (*s3.GetObjectOutput, error)
	deleteObjectFn func(ctx context.Context, params *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
}

func (m *mockS3) PutObject(ctx context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if m.putObjectFn != nil {
		return m.putObjectFn(ctx, params)
	}
	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3) GetObject(ctx context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getObjectFn != nil {
		return m.getObjectFn(ctx, params)
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader("file content")),
	}, nil
}

func (m *mockS3) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.deleteObjectFn != nil {
		return m.deleteObjectFn(ctx, params)
	}
	return &s3.DeleteObjectOutput{}, nil
}

// --- Mock EC2 client ---

type mockEC2 struct {
	describeInstancesFn func(ctx context.Context, params *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
}

func (m *mockEC2) DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if m.describeInstancesFn != nil {
		return m.describeInstancesFn(ctx, params)
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

// --- Mock IAM client ---

type mockIAM struct {
	getInstanceProfileFn func(ctx context.Context, params *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error)
	putRolePolicyFn      func(ctx context.Context, params *iam.PutRolePolicyInput) (*iam.PutRolePolicyOutput, error)
	deleteRolePolicyFn   func(ctx context.Context, params *iam.DeleteRolePolicyInput) (*iam.DeleteRolePolicyOutput, error)
}

func (m *mockIAM) GetInstanceProfile(ctx context.Context, params *iam.GetInstanceProfileInput, _ ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error) {
	if m.getInstanceProfileFn != nil {
		return m.getInstanceProfileFn(ctx, params)
	}
	return &iam.GetInstanceProfileOutput{
		InstanceProfile: &iamtypes.InstanceProfile{
			Roles: []iamtypes.Role{{RoleName: aws.String("test-role")}},
		},
	}, nil
}

func (m *mockIAM) PutRolePolicy(ctx context.Context, params *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	if m.putRolePolicyFn != nil {
		return m.putRolePolicyFn(ctx, params)
	}
	return &iam.PutRolePolicyOutput{}, nil
}

func (m *mockIAM) DeleteRolePolicy(ctx context.Context, params *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	if m.deleteRolePolicyFn != nil {
		return m.deleteRolePolicyFn(ctx, params)
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}

// instanceWithProfile returns a mockEC2 DescribeInstances response
// describing a single instance with the given instance-profile ARN.
func instanceWithProfile(instanceID, profileArn string) *ec2.DescribeInstancesOutput {
	var profile *ec2types.IamInstanceProfile
	if profileArn != "" {
		profile = &ec2types.IamInstanceProfile{Arn: aws.String(profileArn)}
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{
			{
				Instances: []ec2types.Instance{
					{InstanceId: aws.String(instanceID), IamInstanceProfile: profile},
				},
			},
		},
	}
}

// --- Tests ---

func TestNew(t *testing.T) {
	c := New("i-abc123",
		WithRegion("us-west-2"),
		WithBucket("my-bucket"),
		WithSudo(),
		WithSudoPassword("pass"),
	)

	assert.Equal(t, "i-abc123", c.instanceID)
	assert.Equal(t, "us-west-2", c.region)
	assert.Equal(t, "my-bucket", c.bucket)
	assert.True(t, c.sudo)
	assert.Equal(t, "pass", c.sudoPassword)
	assert.Equal(t, defaultTimeout, c.timeout)
}

func TestConnect_Success(t *testing.T) {
	c := New("i-test123", withSSMClient(&mockSSM{}))

	err := c.Connect(context.Background())
	require.NoError(t, err)
}

func TestConnect_NotManaged(t *testing.T) {
	mock := &mockSSM{
		describeInstanceInfoFn: func(_ context.Context, _ *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
			return &ssm.DescribeInstanceInformationOutput{
				InstanceInformationList: []ssmtypes.InstanceInformation{},
			}, nil
		},
	}
	c := New("i-notmanaged", withSSMClient(mock))

	err := c.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by SSM")
}

func TestExecute_Success(t *testing.T) {
	mock := &mockSSM{
		getCommandInvocationFn: func(_ context.Context, _ *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			return &ssm.GetCommandInvocationOutput{
				Status:                ssmtypes.CommandInvocationStatusSuccess,
				StandardOutputContent: aws.String("output"),
				StandardErrorContent:  aws.String(""),
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	result, err := c.Execute(context.Background(), "echo hello")
	require.NoError(t, err)
	assert.Equal(t, "output", result.Stdout)
	assert.Equal(t, 0, result.ExitCode)
}

func TestExecute_Failed(t *testing.T) {
	mock := &mockSSM{
		getCommandInvocationFn: func(_ context.Context, _ *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			return &ssm.GetCommandInvocationOutput{
				Status:                ssmtypes.CommandInvocationStatusFailed,
				ResponseCode:          1,
				StandardOutputContent: aws.String(""),
				StandardErrorContent:  aws.String("command not found"),
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	result, err := c.Execute(context.Background(), "badcmd")
	require.NoError(t, err) // not an error — just a non-zero exit code
	assert.Equal(t, 1, result.ExitCode)
	assert.Equal(t, "command not found", result.Stderr)
}

func TestExecute_TimedOut(t *testing.T) {
	mock := &mockSSM{
		getCommandInvocationFn: func(_ context.Context, _ *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			return &ssm.GetCommandInvocationOutput{
				Status: ssmtypes.CommandInvocationStatusTimedOut,
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	_, err := c.Execute(context.Background(), "sleep 999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestExecute_ContextCancelled(t *testing.T) {
	callCount := 0
	mock := &mockSSM{
		getCommandInvocationFn: func(_ context.Context, _ *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			callCount++
			return &ssm.GetCommandInvocationOutput{
				Status: ssmtypes.CommandInvocationStatusInProgress,
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Execute(ctx, "echo hello")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestExecute_WithSudo(t *testing.T) {
	var capturedCmd string
	mock := &mockSSM{
		sendCommandFn: func(_ context.Context, params *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
			capturedCmd = params.Parameters["commands"][0]
			return &ssm.SendCommandOutput{
				Command: &ssmtypes.Command{CommandId: aws.String("cmd-123")},
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock), WithSudo(), WithSudoPassword("secret"))

	_, _ = c.Execute(context.Background(), "apt update")
	assert.Contains(t, capturedCmd, "sudo -S sh -c")
	assert.Contains(t, capturedCmd, "secret")
}

func TestUploadBase64(t *testing.T) {
	var capturedCmd string
	mock := &mockSSM{
		sendCommandFn: func(_ context.Context, params *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
			capturedCmd = params.Parameters["commands"][0]
			return &ssm.SendCommandOutput{
				Command: &ssmtypes.Command{CommandId: aws.String("cmd-123")},
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	content := []byte("hello world")
	err := c.Upload(context.Background(), bytes.NewReader(content), "/tmp/test.txt", 0644)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(content)
	assert.Contains(t, capturedCmd, b64)
	assert.Contains(t, capturedCmd, "base64 -d")
	assert.Contains(t, capturedCmd, "chmod 0644")
}

func TestUploadBase64_TooLarge(t *testing.T) {
	c := New("i-test123", withSSMClient(&mockSSM{}))

	large := make([]byte, maxBase64Bytes+1)
	err := c.Upload(context.Background(), bytes.NewReader(large), "/tmp/big.bin", 0644)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ssm-bucket")
}

func TestUploadViaS3(t *testing.T) {
	var s3Key string
	s3mock := &mockS3{
		putObjectFn: func(_ context.Context, params *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			s3Key = aws.ToString(params.Key)
			return &s3.PutObjectOutput{}, nil
		},
	}
	ssmMock := &mockSSM{}
	c := New("i-test123",
		withSSMClient(ssmMock),
		withS3Client(s3mock),
		WithBucket("my-bucket"),
	)

	err := c.Upload(context.Background(), strings.NewReader("data"), "/opt/file", 0755)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(s3Key, s3KeyPrefix))
}

func TestDownloadBase64(t *testing.T) {
	content := "file content"
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	mock := &mockSSM{
		getCommandInvocationFn: func(_ context.Context, _ *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			return &ssm.GetCommandInvocationOutput{
				Status:                ssmtypes.CommandInvocationStatusSuccess,
				StandardOutputContent: aws.String(b64),
				StandardErrorContent:  aws.String(""),
			}, nil
		},
	}
	c := New("i-test123", withSSMClient(mock))

	var buf bytes.Buffer
	err := c.Download(context.Background(), "/tmp/test.txt", &buf)
	require.NoError(t, err)
	assert.Equal(t, content, buf.String())
}

func TestDownloadViaS3(t *testing.T) {
	s3mock := &mockS3{
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(strings.NewReader("s3 content")),
			}, nil
		},
	}
	ssmMock := &mockSSM{}
	c := New("i-test123",
		withSSMClient(ssmMock),
		withS3Client(s3mock),
		WithBucket("my-bucket"),
	)

	var buf bytes.Buffer
	err := c.Download(context.Background(), "/opt/file", &buf)
	require.NoError(t, err)
	assert.Equal(t, "s3 content", buf.String())
}

func TestEnsureS3Access_Disabled(t *testing.T) {
	// attachS3Policy not set (WithAutoIAMPolicy not passed): no clients
	// required, ensureS3Access is a no-op.
	c := New("i-test123")
	require.NoError(t, c.ensureS3Access(context.Background()))
	assert.Empty(t, c.iamAttachedRole)
}

func TestEnsureS3Access_AttachesScopedPolicyOnce(t *testing.T) {
	iamPropagationDelay = 0 // skip the real-world propagation wait in tests

	var putCount int
	var capturedPolicy string
	ec2mock := &mockEC2{
		describeInstancesFn: func(_ context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return instanceWithProfile("i-test123", "arn:aws:iam::123456789012:instance-profile/my-profile"), nil
		},
	}
	iamMock := &mockIAM{
		getInstanceProfileFn: func(_ context.Context, params *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
			assert.Equal(t, "my-profile", aws.ToString(params.InstanceProfileName))
			return &iam.GetInstanceProfileOutput{
				InstanceProfile: &iamtypes.InstanceProfile{
					Roles: []iamtypes.Role{{RoleName: aws.String("my-instance-role")}},
				},
			}, nil
		},
		putRolePolicyFn: func(_ context.Context, params *iam.PutRolePolicyInput) (*iam.PutRolePolicyOutput, error) {
			putCount++
			assert.Equal(t, "my-instance-role", aws.ToString(params.RoleName))
			assert.Equal(t, iamPolicyName, aws.ToString(params.PolicyName))
			capturedPolicy = aws.ToString(params.PolicyDocument)
			return &iam.PutRolePolicyOutput{}, nil
		},
	}
	c := New("i-test123",
		withEC2Client(ec2mock),
		withIAMClient(iamMock),
		WithBucket("my-bucket"),
		WithAutoIAMPolicy(),
	)

	require.NoError(t, c.ensureS3Access(context.Background()))
	assert.Equal(t, "my-instance-role", c.iamAttachedRole)
	assert.Equal(t, 1, putCount)

	var doc s3TransferPolicyDocument
	require.NoError(t, json.Unmarshal([]byte(capturedPolicy), &doc))
	require.Len(t, doc.Statement, 1)
	assert.Equal(t, "arn:aws:s3:::my-bucket/tack-transfer/i-test123/*", doc.Statement[0].Resource)
	assert.ElementsMatch(t, []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"}, doc.Statement[0].Action)

	// Second call is a no-op: PutRolePolicy is not invoked again.
	require.NoError(t, c.ensureS3Access(context.Background()))
	assert.Equal(t, 1, putCount)
}

func TestEnsureS3Access_NoInstanceProfile(t *testing.T) {
	ec2mock := &mockEC2{
		describeInstancesFn: func(_ context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return instanceWithProfile("i-test123", ""), nil
		},
	}
	c := New("i-test123",
		withEC2Client(ec2mock),
		withIAMClient(&mockIAM{}),
		WithBucket("my-bucket"),
		WithAutoIAMPolicy(),
	)

	err := c.ensureS3Access(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no IAM instance profile attached")
	assert.Empty(t, c.iamAttachedRole)
}

func TestUploadViaS3_AutoIAMPolicy(t *testing.T) {
	iamPropagationDelay = 0

	var putObjectCalled, putRolePolicyCalled bool
	s3mock := &mockS3{
		putObjectFn: func(_ context.Context, _ *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			putObjectCalled = true
			assert.True(t, putRolePolicyCalled, "S3 upload happened before the IAM policy was attached")
			return &s3.PutObjectOutput{}, nil
		},
	}
	ec2mock := &mockEC2{
		describeInstancesFn: func(_ context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return instanceWithProfile("i-test123", "arn:aws:iam::123456789012:instance-profile/my-profile"), nil
		},
	}
	iamMock := &mockIAM{
		putRolePolicyFn: func(_ context.Context, _ *iam.PutRolePolicyInput) (*iam.PutRolePolicyOutput, error) {
			putRolePolicyCalled = true
			return &iam.PutRolePolicyOutput{}, nil
		},
	}
	c := New("i-test123",
		withSSMClient(&mockSSM{}),
		withS3Client(s3mock),
		withEC2Client(ec2mock),
		withIAMClient(iamMock),
		WithBucket("my-bucket"),
		WithAutoIAMPolicy(),
	)

	err := c.Upload(context.Background(), strings.NewReader("data"), "/opt/file", 0755)
	require.NoError(t, err)
	assert.True(t, putObjectCalled)
	assert.True(t, putRolePolicyCalled)
}

func TestClose_DetachesAttachedIAMPolicy(t *testing.T) {
	var deletedRole, deletedPolicy string
	iamMock := &mockIAM{
		deleteRolePolicyFn: func(_ context.Context, params *iam.DeleteRolePolicyInput) (*iam.DeleteRolePolicyOutput, error) {
			deletedRole = aws.ToString(params.RoleName)
			deletedPolicy = aws.ToString(params.PolicyName)
			return &iam.DeleteRolePolicyOutput{}, nil
		},
	}
	c := New("i-test123", withIAMClient(iamMock))
	c.iamAttachedRole = "my-instance-role" // simulate a prior ensureS3Access attach

	require.NoError(t, c.Close())
	assert.Equal(t, "my-instance-role", deletedRole)
	assert.Equal(t, iamPolicyName, deletedPolicy)
	assert.Empty(t, c.iamAttachedRole)
}

func TestClose_NoIAMPolicyAttached(t *testing.T) {
	// No PutRolePolicy ever happened, so Close must not call DeleteRolePolicy.
	iamMock := &mockIAM{
		deleteRolePolicyFn: func(_ context.Context, _ *iam.DeleteRolePolicyInput) (*iam.DeleteRolePolicyOutput, error) {
			t.Fatal("DeleteRolePolicy should not be called when nothing was attached")
			return &iam.DeleteRolePolicyOutput{}, nil
		},
	}
	c := New("i-test123", withIAMClient(iamMock))
	require.NoError(t, c.Close())
}

func TestInstanceProfileNameFromARN(t *testing.T) {
	name, err := instanceProfileNameFromARN("arn:aws:iam::123456789012:instance-profile/my-profile")
	require.NoError(t, err)
	assert.Equal(t, "my-profile", name)

	_, err = instanceProfileNameFromARN("not-an-arn")
	require.Error(t, err)
}

func TestResolveInstancesByTags(t *testing.T) {
	mock := &mockEC2{
		describeInstancesFn: func(_ context.Context, params *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			// Verify filters include the tags and running state
			var hasRunning, hasTag bool
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "instance-state-name" {
					hasRunning = true
				}
				if aws.ToString(f.Name) == "tag:Env" && f.Values[0] == "prod" {
					hasTag = true
				}
			}
			assert.True(t, hasRunning)
			assert.True(t, hasTag)

			return &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-aaa111")},
							{InstanceId: aws.String("i-bbb222")},
						},
					},
				},
			}, nil
		},
	}

	ids, err := resolveInstancesByTagsWithClient(context.Background(), mock, map[string]string{"Env": "prod"})
	require.NoError(t, err)
	assert.Equal(t, []string{"i-aaa111", "i-bbb222"}, ids)
}

func TestResolveInstancesByTags_NoResults(t *testing.T) {
	mock := &mockEC2{
		describeInstancesFn: func(_ context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return &ec2.DescribeInstancesOutput{}, nil
		},
	}

	_, err := resolveInstancesByTagsWithClient(context.Background(), mock, map[string]string{"Env": "staging"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running instances found")
}

func TestString(t *testing.T) {
	c := New("i-abc123", WithRegion("us-east-1"), WithSudo())
	assert.Equal(t, "ssm://i-abc123 (region=us-east-1) (sudo)", c.String())
}

func TestClose(t *testing.T) {
	c := New("i-abc123")
	assert.NoError(t, c.Close())
}

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name     string
		sudo     bool
		sudoPass string
		cmd      string
		want     string
	}{
		{"no sudo", false, "", "ls", "ls"},
		{"sudo no pass", true, "", "ls", "sudo sh -c 'ls'"},
		{"sudo with pass", true, "secret", "ls", fmt.Sprintf("printf '%%s\\n' 'secret' | sudo -S sh -c 'ls'")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Connector{sudo: tt.sudo, sudoPassword: tt.sudoPass}
			assert.Equal(t, tt.want, c.buildCommand(tt.cmd))
		})
	}
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, "'/tmp/test'", connector.ShellQuote("/tmp/test"))
	assert.Equal(t, "'/tmp/it'\"'\"'s here'", connector.ShellQuote("/tmp/it's here"))
}

func TestDirOf(t *testing.T) {
	assert.Equal(t, "/tmp", dirOf("/tmp/file.txt"))
	assert.Equal(t, ".", dirOf("file.txt"))
	assert.Equal(t, "/a/b", dirOf("/a/b/c"))
}
