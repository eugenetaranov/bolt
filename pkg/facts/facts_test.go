package facts

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/tackhq/tack/internal/connector"
)

// mockConnector returns canned stdout for Execute calls.
type mockConnector struct {
	stdout string
}

func (m *mockConnector) Connect(context.Context) error                              { return nil }
func (m *mockConnector) Upload(context.Context, io.Reader, string, uint32) error    { return nil }
func (m *mockConnector) Download(context.Context, string, io.Writer) error          { return nil }
func (m *mockConnector) SetSudo(bool, string)                                       {}
func (m *mockConnector) Close() error                                               { return nil }
func (m *mockConnector) String() string                                             { return "mock" }
func (m *mockConnector) Execute(_ context.Context, _ string) (*connector.Result, error) {
	return &connector.Result{Stdout: m.stdout}, nil
}

func TestGather_Linux(t *testing.T) {
	output := strings.Join([]string{
		"TACK_FACT os_type=Linux",
		"TACK_FACT architecture=x86_64",
		"TACK_FACT kernel=5.15.0-1020-aws",
		"TACK_FACT hostname=web1",
		"TACK_FACT user=ubuntu",
		"TACK_FACT home=/home/ubuntu",
		"TACK_FACT os_release_start",
		`ID=ubuntu`,
		`VERSION_ID="22.04"`,
		`PRETTY_NAME="Ubuntu 22.04 LTS"`,
		"TACK_FACT os_release_end",
		"TACK_FACT env_PATH=/usr/local/bin:/usr/bin",
		"TACK_FACT env_SHELL=/bin/bash",
		"TACK_FACT default_interface=eth0",
		"TACK_FACT default_ipv4=10.0.0.5",
		"TACK_FACT default_ipv4_subnet=10.0.0.0/24",
		"TACK_FACT default_ipv4_prefix=24",
		"TACK_FACT all_ipv4=10.0.0.5,172.17.0.1",
		"TACK_FACT all_ipv6=2001:db8::1",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := map[string]string{
		"os_type":              "Linux",
		"os_family":           "Debian",
		"pkg_manager":         "apt",
		"distribution":        "ubuntu",
		"distribution_version": "22.04",
		"os_name":             "Ubuntu 22.04 LTS",
		"architecture":        "x86_64",
		"arch":                "amd64",
		"kernel":              "5.15.0-1020-aws",
		"hostname":            "web1",
		"user":                "ubuntu",
		"home":                "/home/ubuntu",
	}

	for key, want := range checks {
		got, ok := facts[key].(string)
		if !ok {
			t.Errorf("facts[%q] missing or not a string (got %T)", key, facts[key])
			continue
		}
		if got != want {
			t.Errorf("facts[%q] = %q, want %q", key, got, want)
		}
	}

	env, ok := facts["env"].(map[string]string)
	if !ok {
		t.Fatalf("facts[env] missing or wrong type: %T", facts["env"])
	}
	if env["PATH"] != "/usr/local/bin:/usr/bin" {
		t.Errorf("env[PATH] = %q, want /usr/local/bin:/usr/bin", env["PATH"])
	}
	if env["SHELL"] != "/bin/bash" {
		t.Errorf("env[SHELL] = %q, want /bin/bash", env["SHELL"])
	}

	// Network facts
	if facts["default_interface"] != "eth0" {
		t.Errorf("default_interface = %q, want eth0", facts["default_interface"])
	}
	if facts["default_ipv4"] != "10.0.0.5" {
		t.Errorf("default_ipv4 = %q, want 10.0.0.5", facts["default_ipv4"])
	}
	if facts["default_ipv4_subnet"] != "10.0.0.0/24" {
		t.Errorf("default_ipv4_subnet = %q, want 10.0.0.0/24", facts["default_ipv4_subnet"])
	}
	if facts["default_ipv4_prefix"] != "24" {
		t.Errorf("default_ipv4_prefix = %q, want 24", facts["default_ipv4_prefix"])
	}
	allIPv4, ok := facts["all_ipv4"].([]string)
	if !ok || len(allIPv4) != 2 {
		t.Fatalf("all_ipv4 = %v, want [10.0.0.5 172.17.0.1]", facts["all_ipv4"])
	}
	if allIPv4[0] != "10.0.0.5" || allIPv4[1] != "172.17.0.1" {
		t.Errorf("all_ipv4 = %v, want [10.0.0.5 172.17.0.1]", allIPv4)
	}
	allIPv6, ok := facts["all_ipv6"].([]string)
	if !ok || len(allIPv6) != 1 || allIPv6[0] != "2001:db8::1" {
		t.Errorf("all_ipv6 = %v, want [2001:db8::1]", facts["all_ipv6"])
	}
}

func TestGather_Darwin(t *testing.T) {
	output := strings.Join([]string{
		"TACK_FACT os_type=Darwin",
		"TACK_FACT architecture=arm64",
		"TACK_FACT kernel=24.1.0",
		"TACK_FACT hostname=macbook",
		"TACK_FACT user=dev",
		"TACK_FACT home=/Users/dev",
		"TACK_FACT os_version=15.1",
		"TACK_FACT os_name=macOS",
		"TACK_FACT env_SHELL=/bin/zsh",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if facts["os_family"] != "Darwin" {
		t.Errorf("os_family = %q, want Darwin", facts["os_family"])
	}
	if facts["pkg_manager"] != "brew" {
		t.Errorf("pkg_manager = %q, want brew", facts["pkg_manager"])
	}
	if facts["arch"] != "arm64" {
		t.Errorf("arch = %q, want arm64", facts["arch"])
	}
	if facts["os_version"] != "15.1" {
		t.Errorf("os_version = %q, want 15.1", facts["os_version"])
	}
	if facts["os_name"] != "macOS" {
		t.Errorf("os_name = %q, want macOS", facts["os_name"])
	}
}

func TestGather_RedHat(t *testing.T) {
	output := strings.Join([]string{
		"TACK_FACT os_type=Linux",
		"TACK_FACT architecture=x86_64",
		"TACK_FACT kernel=5.14.0",
		"TACK_FACT hostname=rhel-host",
		"TACK_FACT user=ec2-user",
		"TACK_FACT home=/home/ec2-user",
		"TACK_FACT os_release_start",
		`ID="rhel"`,
		`VERSION_ID="9.2"`,
		`PRETTY_NAME="Red Hat Enterprise Linux 9.2"`,
		"TACK_FACT os_release_end",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if facts["os_family"] != "RedHat" {
		t.Errorf("os_family = %q, want RedHat", facts["os_family"])
	}
	if facts["pkg_manager"] != "dnf" {
		t.Errorf("pkg_manager = %q, want dnf", facts["pkg_manager"])
	}
	if facts["distribution"] != "rhel" {
		t.Errorf("distribution = %q, want rhel", facts["distribution"])
	}
}

func TestGather_Alpine(t *testing.T) {
	output := strings.Join([]string{
		"TACK_FACT os_type=Linux",
		"TACK_FACT architecture=aarch64",
		"TACK_FACT kernel=6.1.0",
		"TACK_FACT hostname=alpine-box",
		"TACK_FACT user=root",
		"TACK_FACT home=/root",
		"TACK_FACT os_release_start",
		`ID=alpine`,
		`VERSION_ID=3.19.0`,
		`PRETTY_NAME="Alpine Linux v3.19"`,
		"TACK_FACT os_release_end",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if facts["os_family"] != "Alpine" {
		t.Errorf("os_family = %q, want Alpine", facts["os_family"])
	}
	if facts["pkg_manager"] != "apk" {
		t.Errorf("pkg_manager = %q, want apk", facts["pkg_manager"])
	}
	if facts["arch"] != "arm64" {
		t.Errorf("arch = %q, want arm64", facts["arch"])
	}
}

func TestGather_MinimalOutput(t *testing.T) {
	// Simulate a minimal system that only returns os_type and architecture
	output := "TACK_FACT os_type=Linux\nTACK_FACT architecture=armv7l\n"

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if facts["os_type"] != "Linux" {
		t.Errorf("os_type = %q, want Linux", facts["os_type"])
	}
	if facts["arch"] != "arm" {
		t.Errorf("arch = %q, want arm", facts["arch"])
	}
	// Missing fields should not be set
	if _, ok := facts["hostname"]; ok {
		t.Error("hostname should not be set for minimal output")
	}
}

func TestNormalizeArch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"x86_64", "amd64"},
		{"amd64", "amd64"},
		{"aarch64", "arm64"},
		{"arm64", "arm64"},
		{"armv7l", "arm"},
		{"riscv64", "riscv64"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := normalizeArch(tt.input); got != tt.want {
				t.Errorf("normalizeArch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseOSRelease(t *testing.T) {
	input := `ID=ubuntu
VERSION_ID="22.04"
PRETTY_NAME="Ubuntu 22.04.3 LTS"
# comment
NAME="Ubuntu"
`
	got := parseOSRelease(input)

	if got["ID"] != "ubuntu" {
		t.Errorf("ID = %q, want ubuntu", got["ID"])
	}
	if got["VERSION_ID"] != "22.04" {
		t.Errorf("VERSION_ID = %q, want 22.04", got["VERSION_ID"])
	}
	if got["PRETTY_NAME"] != "Ubuntu 22.04.3 LTS" {
		t.Errorf("PRETTY_NAME = %q, want 'Ubuntu 22.04.3 LTS'", got["PRETTY_NAME"])
	}
	if got["NAME"] != "Ubuntu" {
		t.Errorf("NAME = %q, want Ubuntu", got["NAME"])
	}
}

func TestGather_EmptyEnvSkipped(t *testing.T) {
	output := "TACK_FACT os_type=Linux\nTACK_FACT env_PATH=/usr/bin\nTACK_FACT env_EMPTY=\n"

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env, ok := facts["env"].(map[string]string)
	if !ok {
		t.Fatalf("facts[env] missing or wrong type: %T", facts["env"])
	}
	if env["PATH"] != "/usr/bin" {
		t.Errorf("env[PATH] = %q, want /usr/bin", env["PATH"])
	}
	if _, ok := env["EMPTY"]; ok {
		t.Error("empty env var should not be included")
	}
}

func TestGather_EC2WithIMDSTags(t *testing.T) {
	// Prevent API fallback from being called
	orig := newEC2Client
	defer func() { newEC2Client = orig }()
	newEC2Client = func(_ context.Context, _ string) (ec2DescribeTagsAPI, error) {
		return nil, fmt.Errorf("should not be called")
	}

	output := strings.Join([]string{
		"TACK_FACT os_type=Linux",
		"TACK_FACT architecture=x86_64",
		"TACK_FACT hostname=web1",
		"TACK_FACT user=ec2-user",
		"TACK_FACT home=/home/ec2-user",
		"TACK_FACT ec2_instance_id=i-0abc123def456",
		"TACK_FACT ec2_region=us-east-1",
		"TACK_FACT ec2_az=us-east-1a",
		"TACK_FACT ec2_instance_type=t3.medium",
		"TACK_FACT ec2_ami_id=ami-0abcdef1234567890",
		"TACK_FACT ec2_tags_start",
		"Name=web-1",
		"Env=prod",
		"Role=frontend",
		"TACK_FACT ec2_tags_end",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ec2Checks := map[string]string{
		"ec2_instance_id":   "i-0abc123def456",
		"ec2_region":        "us-east-1",
		"ec2_az":            "us-east-1a",
		"ec2_instance_type": "t3.medium",
		"ec2_ami_id":        "ami-0abcdef1234567890",
	}
	for key, want := range ec2Checks {
		got, ok := facts[key].(string)
		if !ok {
			t.Errorf("facts[%q] missing or not a string", key)
			continue
		}
		if got != want {
			t.Errorf("facts[%q] = %q, want %q", key, got, want)
		}
	}

	tags, ok := facts["ec2_tags"].(map[string]string)
	if !ok {
		t.Fatalf("facts[ec2_tags] missing or wrong type: %T", facts["ec2_tags"])
	}
	if tags["Name"] != "web-1" {
		t.Errorf("ec2_tags[Name] = %q, want web-1", tags["Name"])
	}
	if tags["Env"] != "prod" {
		t.Errorf("ec2_tags[Env] = %q, want prod", tags["Env"])
	}
	if tags["Role"] != "frontend" {
		t.Errorf("ec2_tags[Role] = %q, want frontend", tags["Role"])
	}
}

func TestGather_EC2WithAPIFallback(t *testing.T) {
	orig := newEC2Client
	defer func() { newEC2Client = orig }()

	newEC2Client = func(_ context.Context, region string) (ec2DescribeTagsAPI, error) {
		if region != "us-west-2" {
			t.Errorf("expected region us-west-2, got %s", region)
		}
		return &mockEC2Client{
			tags: []types.TagDescription{
				{Key: aws.String("Name"), Value: aws.String("api-1")},
				{Key: aws.String("Team"), Value: aws.String("platform")},
			},
		}, nil
	}

	output := strings.Join([]string{
		"TACK_FACT os_type=Linux",
		"TACK_FACT ec2_instance_id=i-fallback123",
		"TACK_FACT ec2_region=us-west-2",
	}, "\n")

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tags, ok := facts["ec2_tags"].(map[string]string)
	if !ok {
		t.Fatalf("facts[ec2_tags] missing or wrong type: %T", facts["ec2_tags"])
	}
	if tags["Name"] != "api-1" {
		t.Errorf("ec2_tags[Name] = %q, want api-1", tags["Name"])
	}
	if tags["Team"] != "platform" {
		t.Errorf("ec2_tags[Team] = %q, want platform", tags["Team"])
	}
}

func TestGather_NonEC2(t *testing.T) {
	// No EC2 facts in output — should not set ec2_tags
	output := "TACK_FACT os_type=Linux\nTACK_FACT hostname=dev-laptop\n"

	conn := &mockConnector{stdout: output}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := facts["ec2_instance_id"]; ok {
		t.Error("ec2_instance_id should not be set on non-EC2")
	}
	if _, ok := facts["ec2_tags"]; ok {
		t.Error("ec2_tags should not be set on non-EC2")
	}
}

func TestGather_GoRuntimeFacts(t *testing.T) {
	conn := &mockConnector{stdout: ""}
	facts, err := Gather(context.Background(), conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := facts["go_os"].(string); !ok {
		t.Error("go_os should always be set")
	}
	if _, ok := facts["go_arch"].(string); !ok {
		t.Error("go_arch should always be set")
	}
}
