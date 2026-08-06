package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tackhq/tack/internal/ssmbucket"
)

// makeSSMBucketParentCmd creates a parent ssmBucketCmd-like cobra.Command
// with --name/--region PersistentFlags registered so a child command
// inherits them, mirroring makeVaultParentCmd in vault_test.go.
func makeSSMBucketParentCmd(t *testing.T, flags map[string]string) *cobra.Command {
	t.Helper()
	parent := &cobra.Command{Use: "ssm-bucket"}
	parent.PersistentFlags().String("name", "", "S3 bucket name (or TACK_SSM_BUCKET)")
	parent.PersistentFlags().String("region", "", "AWS region (or TACK_SSM_REGION)")
	child := &cobra.Command{Use: "subcmd"}
	parent.AddCommand(child)
	for k, v := range flags {
		if err := parent.PersistentFlags().Set(k, v); err != nil {
			t.Fatalf("makeSSMBucketParentCmd: set flag %s: %v", k, err)
		}
	}
	return child
}

func TestSSMBucketNameAndRegion_FromFlags(t *testing.T) {
	cmd := makeSSMBucketParentCmd(t, map[string]string{"name": "my-bucket", "region": "eu-west-1"})

	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-bucket" {
		t.Errorf("name = %q, want my-bucket", name)
	}
	if region != "eu-west-1" {
		t.Errorf("region = %q, want eu-west-1", region)
	}
}

func TestSSMBucketNameAndRegion_FromEnv(t *testing.T) {
	t.Setenv("TACK_SSM_BUCKET", "env-bucket")
	t.Setenv("TACK_SSM_REGION", "ap-southeast-1")
	cmd := makeSSMBucketParentCmd(t, nil)

	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "env-bucket" {
		t.Errorf("name = %q, want env-bucket", name)
	}
	if region != "ap-southeast-1" {
		t.Errorf("region = %q, want ap-southeast-1", region)
	}
}

func TestSSMBucketNameAndRegion_FlagOverridesEnv(t *testing.T) {
	t.Setenv("TACK_SSM_BUCKET", "env-bucket")
	cmd := makeSSMBucketParentCmd(t, map[string]string{"name": "flag-bucket"})

	name, _, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "flag-bucket" {
		t.Errorf("name = %q, want flag-bucket (flag should win over env)", name)
	}
}

func TestSSMBucketNameAndRegion_MissingNameErrors(t *testing.T) {
	cmd := makeSSMBucketParentCmd(t, nil)

	_, _, err := ssmBucketNameAndRegion(cmd)
	if err == nil {
		t.Fatal("expected error when neither --name nor TACK_SSM_BUCKET is set")
	}
}

// withStdin temporarily replaces os.Stdin with a pipe whose read end the
// test owns, mirroring internal/output/output_test.go's helper.
func withStdin(t *testing.T, input string) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe write end: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = orig
		_ = r.Close()
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe write end: %v", err)
	}
	os.Stdout = orig
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(data)
}

func TestConfirmBucketDelete_Yes(t *testing.T) {
	cleanup := withStdin(t, "yes\n")
	defer cleanup()

	var confirmed bool
	out := captureStdout(t, func() {
		confirmed = confirmBucketDelete("my-bucket", &ssmbucket.DeletePreview{Objects: 5})
	})
	if !confirmed {
		t.Error("expected confirmBucketDelete to return true for \"yes\"")
	}
	if !strings.Contains(out, "my-bucket") {
		t.Errorf("expected prompt to mention bucket name, got: %q", out)
	}
}

func TestConfirmBucketDelete_No(t *testing.T) {
	cleanup := withStdin(t, "no\n")
	defer cleanup()

	confirmed := confirmBucketDelete("my-bucket", &ssmbucket.DeletePreview{})
	if confirmed {
		t.Error("expected confirmBucketDelete to return false for \"no\"")
	}
}

func TestConfirmBucketDelete_EOFReturnsFalse(t *testing.T) {
	cleanup := withStdin(t, "")
	defer cleanup()

	confirmed := confirmBucketDelete("my-bucket", &ssmbucket.DeletePreview{})
	if confirmed {
		t.Error("expected confirmBucketDelete to return false on EOF")
	}
}

func TestConfirmBucketDelete_ShowsPreviewCounts(t *testing.T) {
	cleanup := withStdin(t, "no\n")
	defer cleanup()

	preview := &ssmbucket.DeletePreview{Objects: 12, Versions: 3, DeleteMarkers: 2, MultipartUploads: 1}
	out := captureStdout(t, func() {
		confirmBucketDelete("my-bucket", preview)
	})
	for _, want := range []string{"12", "3", "2", "1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected preview output to contain %q, got: %q", want, out)
		}
	}
}
