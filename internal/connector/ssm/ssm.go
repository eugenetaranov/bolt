// Package ssm provides a connector for executing commands on EC2 instances via AWS Systems Manager.
package ssm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/tackhq/tack/internal/connector"
)

// Default settings.
const (
	defaultTimeout = 10 * time.Minute
	pollInterval   = 2 * time.Second
	maxBase64Bytes = 24 * 1024 // 24 KB limit for base64 inline transfer
	s3KeyPrefix    = "tack-transfer/"

	// iamPolicyName is the inline role-policy name used when
	// WithAutoIAMPolicy is enabled. Fixed and tack-owned so PutRolePolicy
	// safely overwrites any policy of the same name from a prior run, and
	// Close reliably removes exactly what it attached.
	iamPolicyName = "tack-ssm-s3-transfer"
)

// iamPropagationDelay is a brief wait after attaching a fresh IAM policy
// before relying on it — IAM policy changes are eventually consistent.
// A var (not const) so tests can zero it out.
var iamPropagationDelay = 3 * time.Second

// ssmAPI is the subset of the SSM client used by the connector.
type ssmAPI interface {
	DescribeInstanceInformation(ctx context.Context, params *ssm.DescribeInstanceInformationInput, optFns ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
	SendCommand(ctx context.Context, params *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(ctx context.Context, params *ssm.GetCommandInvocationInput, optFns ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
	CancelCommand(ctx context.Context, params *ssm.CancelCommandInput, optFns ...func(*ssm.Options)) (*ssm.CancelCommandOutput, error)
}

// s3API is the subset of the S3 client used by the connector.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// ec2API is the subset of the EC2 client used for tag-based instance
// resolution and instance-profile lookups.
type ec2API interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// iamAPI is the subset of the IAM client used to temporarily grant an
// instance's role S3 access to the transfer bucket.
type iamAPI interface {
	GetInstanceProfile(ctx context.Context, params *iam.GetInstanceProfileInput, optFns ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error)
	PutRolePolicy(ctx context.Context, params *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, params *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
}

// Connector executes commands on EC2 instances via AWS Systems Manager.
type Connector struct {
	instanceID     string
	region         string
	bucket         string // S3 bucket for file transfer; empty = base64 fallback
	timeout        time.Duration
	sudo           bool
	sudoPassword   string
	attachS3Policy bool // temporarily grant the instance's role S3 access for transfers
	ssmClient      ssmAPI
	s3Client       s3API
	ec2Client      ec2API
	iamClient      iamAPI

	// iamAttachedRole is the role name a temporary S3 transfer policy was
	// attached to, or "" if none is attached. Set once per connector
	// lifetime by ensureS3Access; cleared by Close after removal.
	iamAttachedRole string
}

// Option configures the SSM connector.
type Option func(*Connector)

// WithRegion sets the AWS region.
func WithRegion(region string) Option {
	return func(c *Connector) {
		c.region = region
	}
}

// WithBucket sets the S3 bucket for file transfers.
func WithBucket(bucket string) Option {
	return func(c *Connector) {
		c.bucket = bucket
	}
}

// WithTimeout sets the command execution timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Connector) {
		c.timeout = d
	}
}

// WithSudo enables sudo for command execution.
func WithSudo() Option {
	return func(c *Connector) {
		c.sudo = true
	}
}

// WithSudoPassword sets the sudo password.
func WithSudoPassword(password string) Option {
	return func(c *Connector) {
		c.sudoPassword = password
	}
}

// WithAutoIAMPolicy enables temporary attachment of an inline IAM policy
// granting the instance's role S3 access to the transfer bucket's
// tack-transfer/ prefix. The policy is attached on first S3 transfer and
// removed on Close. Use for instances that don't already have S3
// permissions provisioned; SSM's own file-transfer path caps out around
// 20 KB, so larger transfers require S3.
func WithAutoIAMPolicy() Option {
	return func(c *Connector) {
		c.attachS3Policy = true
	}
}

// withSSMClient injects a custom SSM client (for testing).
func withSSMClient(client ssmAPI) Option {
	return func(c *Connector) {
		c.ssmClient = client
	}
}

// withS3Client injects a custom S3 client (for testing).
func withS3Client(client s3API) Option {
	return func(c *Connector) {
		c.s3Client = client
	}
}

// withEC2Client injects a custom EC2 client (for testing).
func withEC2Client(client ec2API) Option {
	return func(c *Connector) {
		c.ec2Client = client
	}
}

// withIAMClient injects a custom IAM client (for testing).
func withIAMClient(client iamAPI) Option {
	return func(c *Connector) {
		c.iamClient = client
	}
}

// New creates a new SSM connector for the specified instance ID.
func New(instanceID string, opts ...Option) *Connector {
	c := &Connector{
		instanceID: instanceID,
		timeout:    defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Connect validates the instance is SSM-managed. If no SSM client was injected,
// it loads the AWS config and creates real SSM (and optionally S3) clients.
func (c *Connector) Connect(ctx context.Context) error {
	if c.ssmClient == nil {
		var optFns []func(*awsconfig.LoadOptions) error
		if c.region != "" {
			optFns = append(optFns, awsconfig.WithRegion(c.region))
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}
		c.ssmClient = ssm.NewFromConfig(cfg)
		if c.bucket != "" {
			c.s3Client = s3.NewFromConfig(cfg)
		}
		if c.attachS3Policy {
			c.ec2Client = ec2.NewFromConfig(cfg)
			c.iamClient = iam.NewFromConfig(cfg)
		}
	}

	// Validate instance is SSM-managed
	out, err := c.ssmClient.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{
			{
				Key:    aws.String("InstanceIds"),
				Values: []string{c.instanceID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe instance %s: %w", c.instanceID, err)
	}
	if len(out.InstanceInformationList) == 0 {
		return fmt.Errorf("instance %s is not managed by SSM (check SSM agent and IAM role)", c.instanceID)
	}

	return nil
}

// Execute runs a command on the instance via SSM SendCommand and polls for the result.
func (c *Connector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	fullCmd := c.buildCommand(cmd)

	timeoutSec := int(c.timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 600
	}

	sendOut, err := c.ssmClient.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{c.instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters: map[string][]string{
			"commands":         {fullCmd},
			"executionTimeout": {fmt.Sprintf("%d", timeoutSec)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send command to %s: %w", c.instanceID, err)
	}

	commandID := aws.ToString(sendOut.Command.CommandId)

	// Poll for completion
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Best-effort cancel with a fresh context
			cancelCtx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()
			_, _ = c.ssmClient.CancelCommand(cancelCtx, &ssm.CancelCommandInput{
				CommandId: aws.String(commandID),
			})
			return nil, ctx.Err()
		case <-ticker.C:
			invOut, err := c.ssmClient.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
				CommandId:  aws.String(commandID),
				InstanceId: aws.String(c.instanceID),
			})
			if err != nil {
				// InvocationDoesNotExist is transient — command may not have registered yet
				if strings.Contains(err.Error(), "InvocationDoesNotExist") {
					continue
				}
				return nil, fmt.Errorf("failed to get command invocation: %w", err)
			}

			switch invOut.Status {
			case ssmtypes.CommandInvocationStatusPending,
				ssmtypes.CommandInvocationStatusInProgress,
				ssmtypes.CommandInvocationStatusDelayed:
				continue

			case ssmtypes.CommandInvocationStatusSuccess:
				return &connector.Result{
					Stdout:   aws.ToString(invOut.StandardOutputContent),
					Stderr:   aws.ToString(invOut.StandardErrorContent),
					ExitCode: 0,
				}, nil

			case ssmtypes.CommandInvocationStatusFailed:
				exitCode := int(invOut.ResponseCode)
				return &connector.Result{
					Stdout:   aws.ToString(invOut.StandardOutputContent),
					Stderr:   aws.ToString(invOut.StandardErrorContent),
					ExitCode: exitCode,
				}, nil

			case ssmtypes.CommandInvocationStatusTimedOut:
				return nil, fmt.Errorf("command timed out on %s", c.instanceID)

			case ssmtypes.CommandInvocationStatusCancelled:
				return nil, fmt.Errorf("command was cancelled on %s", c.instanceID)

			default:
				return nil, fmt.Errorf("unexpected command status %s on %s", invOut.Status, c.instanceID)
			}
		}
	}
}

// Upload copies content to the remote instance.
// Uses S3 as a transfer medium if a bucket is configured, otherwise falls back to base64 inline.
func (c *Connector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("failed to read upload source: %w", err)
	}

	modeStr := fmt.Sprintf("%04o", mode)

	if c.bucket != "" {
		return c.uploadViaS3(ctx, data, dst, modeStr)
	}

	return c.uploadViaBase64(ctx, data, dst, modeStr)
}

// uploadViaS3 uploads data through an S3 bucket.
func (c *Connector) uploadViaS3(ctx context.Context, data []byte, dst, modeStr string) error {
	if err := c.ensureS3Access(ctx); err != nil {
		return err
	}

	key := s3KeyPrefix + c.instanceID + "/" + fmt.Sprintf("%d", time.Now().UnixNano())

	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	// Copy from S3 to destination on instance
	cmd := fmt.Sprintf("aws s3 cp s3://%s/%s %s && chmod %s %s",
		connector.ShellQuote(c.bucket), connector.ShellQuote(key), connector.ShellQuote(dst), modeStr, connector.ShellQuote(dst))
	if _, err := connector.Run(ctx, c, cmd); err != nil {
		c.cleanupS3(ctx, key)
		return fmt.Errorf("failed to copy from S3 to %s: %w", dst, err)
	}

	c.cleanupS3(ctx, key)
	return nil
}

// uploadViaBase64 uploads data inline using base64 encoding.
func (c *Connector) uploadViaBase64(ctx context.Context, data []byte, dst, modeStr string) error {
	if len(data) > maxBase64Bytes {
		return fmt.Errorf("file too large (%d bytes) for inline transfer; use --ssm-bucket for files over %d bytes",
			len(data), maxBase64Bytes)
	}

	b64 := base64.StdEncoding.EncodeToString(data)

	// Ensure parent directory exists
	cmd := fmt.Sprintf("mkdir -p %s && printf '%%s' '%s' | base64 -d > %s && chmod %s %s",
		connector.ShellQuote(dirOf(dst)), b64, connector.ShellQuote(dst), modeStr, connector.ShellQuote(dst))
	if _, err := connector.Run(ctx, c, cmd); err != nil {
		return fmt.Errorf("failed to upload to %s: %w", dst, err)
	}

	return nil
}

// Download copies content from the remote instance.
func (c *Connector) Download(ctx context.Context, src string, dst io.Writer) error {
	if c.bucket != "" {
		return c.downloadViaS3(ctx, src, dst)
	}

	return c.downloadViaBase64(ctx, src, dst)
}

// downloadViaS3 downloads data through an S3 bucket.
func (c *Connector) downloadViaS3(ctx context.Context, src string, dst io.Writer) error {
	if err := c.ensureS3Access(ctx); err != nil {
		return err
	}

	key := s3KeyPrefix + c.instanceID + "/" + fmt.Sprintf("%d", time.Now().UnixNano())

	// Copy from instance to S3
	cmd := fmt.Sprintf("aws s3 cp %s s3://%s/%s", connector.ShellQuote(src), connector.ShellQuote(c.bucket), connector.ShellQuote(key))
	if _, err := connector.Run(ctx, c, cmd); err != nil {
		return fmt.Errorf("failed to copy %s to S3: %w", src, err)
	}

	// Get from S3
	getOut, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		c.cleanupS3(ctx, key)
		return fmt.Errorf("failed to get object from S3: %w", err)
	}
	defer getOut.Body.Close()

	if _, err := io.Copy(dst, getOut.Body); err != nil {
		c.cleanupS3(ctx, key)
		return fmt.Errorf("failed to read S3 object: %w", err)
	}

	c.cleanupS3(ctx, key)
	return nil
}

// downloadViaBase64 downloads data inline using base64 encoding.
func (c *Connector) downloadViaBase64(ctx context.Context, src string, dst io.Writer) error {
	result, err := connector.Run(ctx, c, fmt.Sprintf("base64 %s", connector.ShellQuote(src)))
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(result.Stdout))
	if err != nil {
		return fmt.Errorf("failed to decode base64 output: %w", err)
	}

	if _, err := dst.Write(decoded); err != nil {
		return fmt.Errorf("failed to write downloaded content: %w", err)
	}

	return nil
}

// SetSudo enables or disables sudo for subsequent commands.
func (c *Connector) SetSudo(enabled bool, password string) {
	c.sudo = enabled
	c.sudoPassword = password
}

// Close removes any temporary IAM policy this connector attached via
// WithAutoIAMPolicy (best-effort), then returns. SSM itself has no
// persistent connection to tear down.
func (c *Connector) Close() error {
	if c.iamAttachedRole != "" && c.iamClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.iamClient.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   aws.String(c.iamAttachedRole),
			PolicyName: aws.String(iamPolicyName),
		})
		c.iamAttachedRole = ""
	}
	return nil
}

// String returns a human-readable description of the connection.
func (c *Connector) String() string {
	desc := fmt.Sprintf("ssm://%s", c.instanceID)
	if c.region != "" {
		desc += fmt.Sprintf(" (region=%s)", c.region)
	}
	if c.sudo {
		desc += " (sudo)"
	}
	return desc
}

// buildCommand wraps the command with sudo if configured.
func (c *Connector) buildCommand(cmd string) string {
	return connector.BuildSudoCommand(cmd, c.sudo, c.sudoPassword, false)
}

// cleanupS3 removes a temporary S3 object (best-effort).
func (c *Connector) cleanupS3(ctx context.Context, key string) {
	if c.s3Client == nil {
		return
	}
	_, _ = c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
}

// ensureS3Access lazily attaches a scoped inline IAM policy to the
// instance's IAM role granting access to this instance's tack-transfer/
// prefix in the bucket, if WithAutoIAMPolicy was enabled and this
// connector hasn't already attached one. Idempotent — a second call is a
// no-op. The attached policy is removed on Close.
func (c *Connector) ensureS3Access(ctx context.Context) error {
	if !c.attachS3Policy || c.iamAttachedRole != "" {
		return nil
	}

	out, err := c.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{c.instanceID},
	})
	if err != nil {
		return fmt.Errorf("failed to describe instance %s for IAM role lookup: %w", c.instanceID, err)
	}
	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		return fmt.Errorf("instance %s not found while resolving its IAM role", c.instanceID)
	}
	profile := out.Reservations[0].Instances[0].IamInstanceProfile
	if profile == nil || aws.ToString(profile.Arn) == "" {
		return fmt.Errorf("instance %s has no IAM instance profile attached; cannot auto-attach an S3 transfer policy", c.instanceID)
	}

	profileName, err := instanceProfileNameFromARN(aws.ToString(profile.Arn))
	if err != nil {
		return err
	}

	ipOut, err := c.iamClient.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(profileName),
	})
	if err != nil {
		return fmt.Errorf("failed to look up instance profile %s: %w", profileName, err)
	}
	if ipOut.InstanceProfile == nil || len(ipOut.InstanceProfile.Roles) == 0 {
		return fmt.Errorf("instance profile %s has no attached IAM role", profileName)
	}
	roleName := aws.ToString(ipOut.InstanceProfile.Roles[0].RoleName)

	policyDoc, err := json.Marshal(s3TransferPolicy(c.bucket, c.instanceID))
	if err != nil {
		return fmt.Errorf("failed to build S3 transfer policy: %w", err)
	}

	if _, err := c.iamClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(iamPolicyName),
		PolicyDocument: aws.String(string(policyDoc)),
	}); err != nil {
		return fmt.Errorf("failed to attach S3 transfer policy to role %s: %w", roleName, err)
	}

	c.iamAttachedRole = roleName

	// IAM policy changes are eventually consistent; give it a moment
	// before the instance actually tries to use the new permissions.
	select {
	case <-time.After(iamPropagationDelay):
	case <-ctx.Done():
	}

	return nil
}

// instanceProfileNameFromARN extracts the instance profile name from its
// ARN (arn:aws:iam::123456789012:instance-profile/NAME).
func instanceProfileNameFromARN(arnStr string) (string, error) {
	i := strings.LastIndex(arnStr, "/")
	if i < 0 || i == len(arnStr)-1 {
		return "", fmt.Errorf("unexpected instance profile ARN format: %s", arnStr)
	}
	return arnStr[i+1:], nil
}

// s3TransferPolicyDocument is the IAM policy document shape for scoped S3
// transfer access.
type s3TransferPolicyDocument struct {
	Version   string                      `json:"Version"`
	Statement []s3TransferPolicyStatement `json:"Statement"`
}

// s3TransferPolicyStatement is a single statement within an IAM policy document.
type s3TransferPolicyStatement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

// s3TransferPolicy builds a least-privilege policy scoped to this
// instance's own transfer prefix in the bucket (not the whole bucket).
func s3TransferPolicy(bucket, instanceID string) s3TransferPolicyDocument {
	return s3TransferPolicyDocument{
		Version: "2012-10-17",
		Statement: []s3TransferPolicyStatement{
			{
				Effect:   "Allow",
				Action:   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
				Resource: fmt.Sprintf("arn:aws:s3:::%s/%s%s/*", bucket, s3KeyPrefix, instanceID),
			},
		},
	}
}

// dirOf returns the directory component of a path.
func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "."
	}
	return path[:i]
}

// ResolveInstancesByTags uses EC2 DescribeInstances to find running instances
// matching the given tags. Returns a list of instance IDs.
func ResolveInstancesByTags(ctx context.Context, tags map[string]string, region string) ([]string, error) {
	var optFns []func(*awsconfig.LoadOptions) error
	if region != "" {
		optFns = append(optFns, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return resolveInstancesByTagsWithClient(ctx, ec2.NewFromConfig(cfg), tags)
}

// resolveInstancesByTagsWithClient is the testable core of ResolveInstancesByTags.
func resolveInstancesByTagsWithClient(ctx context.Context, client ec2API, tags map[string]string) ([]string, error) {
	filters := []ec2types.Filter{
		{
			Name:   aws.String("instance-state-name"),
			Values: []string{"running"},
		},
	}
	for k, v := range tags {
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + k),
			Values: []string{v},
		})
	}

	out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances: %w", err)
	}

	var ids []string
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			if inst.InstanceId != nil {
				ids = append(ids, *inst.InstanceId)
			}
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no running instances found matching tags: %v", tags)
	}

	return ids, nil
}

// Ensure Connector implements the connector.Connector interface.
var _ connector.Connector = (*Connector)(nil)
