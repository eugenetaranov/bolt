package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// newAttachPolicyCmd builds a minimal command with just the
// --ssm-attach-policy flag registered (default true, matching runCmd) so
// buildConnOverrides can resolve it in isolation. Other flags it reads are
// simply absent → Changed() is false and they're skipped.
func newAttachPolicyCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "run"}
	cmd.Flags().Bool("ssm-attach-policy", true, "")
	return cmd
}

func TestBuildConnOverrides_SSMAttachPolicy(t *testing.T) {
	t.Run("unset leaves nil (connector default-on)", func(t *testing.T) {
		t.Setenv("TACK_SSM_ATTACH_POLICY", "")
		o, err := buildConnOverrides(newAttachPolicyCmd(t))
		if err != nil {
			t.Fatalf("buildConnOverrides: %v", err)
		}
		if o.SSMAttachS3Policy != nil {
			t.Errorf("SSMAttachS3Policy = %v, want nil when unset", *o.SSMAttachS3Policy)
		}
	})

	t.Run("--ssm-attach-policy=false opts out", func(t *testing.T) {
		t.Setenv("TACK_SSM_ATTACH_POLICY", "")
		cmd := newAttachPolicyCmd(t)
		if err := cmd.Flags().Set("ssm-attach-policy", "false"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		o, err := buildConnOverrides(cmd)
		if err != nil {
			t.Fatalf("buildConnOverrides: %v", err)
		}
		if o.SSMAttachS3Policy == nil || *o.SSMAttachS3Policy {
			t.Errorf("SSMAttachS3Policy = %v, want pointer to false", o.SSMAttachS3Policy)
		}
	})

	t.Run("--ssm-attach-policy=true forces on", func(t *testing.T) {
		t.Setenv("TACK_SSM_ATTACH_POLICY", "")
		cmd := newAttachPolicyCmd(t)
		if err := cmd.Flags().Set("ssm-attach-policy", "true"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		o, err := buildConnOverrides(cmd)
		if err != nil {
			t.Fatalf("buildConnOverrides: %v", err)
		}
		if o.SSMAttachS3Policy == nil || !*o.SSMAttachS3Policy {
			t.Errorf("SSMAttachS3Policy = %v, want pointer to true", o.SSMAttachS3Policy)
		}
	})

	t.Run("env=0 opts out", func(t *testing.T) {
		t.Setenv("TACK_SSM_ATTACH_POLICY", "0")
		o, err := buildConnOverrides(newAttachPolicyCmd(t))
		if err != nil {
			t.Fatalf("buildConnOverrides: %v", err)
		}
		if o.SSMAttachS3Policy == nil || *o.SSMAttachS3Policy {
			t.Errorf("SSMAttachS3Policy = %v, want pointer to false", o.SSMAttachS3Policy)
		}
	})

	t.Run("env=true forces on", func(t *testing.T) {
		t.Setenv("TACK_SSM_ATTACH_POLICY", "true")
		o, err := buildConnOverrides(newAttachPolicyCmd(t))
		if err != nil {
			t.Fatalf("buildConnOverrides: %v", err)
		}
		if o.SSMAttachS3Policy == nil || !*o.SSMAttachS3Policy {
			t.Errorf("SSMAttachS3Policy = %v, want pointer to true", o.SSMAttachS3Policy)
		}
	})
}
