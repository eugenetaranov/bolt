package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/ssmbucket"
)

// ssmBucketCmd is the parent command for managing the S3 bucket used by the
// SSM connector for large file transfer.
var ssmBucketCmd = &cobra.Command{
	Use:   "ssm-bucket",
	Short: "Manage the S3 bucket used for SSM file transfer",
	Long: `Create, inspect, destroy, and verify access to the S3 bucket the SSM
connector uses for large file transfers (ssm.bucket / --ssm-bucket).
SSM's own command channel can't carry more than ~20-24 KB per file.`,
}

var ssmBucketCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create (or converge the configuration of) the SSM transfer bucket",
	Long: `Creates the S3 bucket with public access blocked, server-side encryption
enabled, a lifecycle rule expiring tack-transfer/ objects, and a
ManagedBy=tack tag. Safe to re-run: an already-owned bucket is treated as
success and its configuration is re-applied, so this also fixes drift.`,
	Args: cobra.NoArgs,
	RunE: runSSMBucketCreate,
}

var ssmBucketStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the SSM transfer bucket's configuration",
	Args:  cobra.NoArgs,
	RunE:  runSSMBucketStatus,
}

var ssmBucketDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete the SSM transfer bucket and everything in it",
	Long: `Deletes the bucket, assuming it's very likely non-empty: pages through
every object (and, if the bucket was ever versioned, every version and
delete marker), batch-deletes them, aborts incomplete multipart uploads,
then deletes the bucket itself. Refuses to touch a bucket that doesn't
carry the ManagedBy=tack tag unless --unmanaged is passed.`,
	Args: cobra.NoArgs,
	RunE: runSSMBucketDelete,
}

var ssmBucketVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify an instance can transfer files through the SSM bucket",
	Long: `Uploads a small test file to the bucket via the named SSM-managed
instance, downloads it back, confirms the content matches, and removes it.
By default this temporarily grants the instance's IAM role S3 access to the
bucket if it doesn't already have it (best-effort, removed again afterward),
exercising the same path a real transfer uses. The --attach-policy flag is
retained for backward compatibility and has no effect.`,
	Args: cobra.NoArgs,
	RunE: runSSMBucketVerify,
}

func init() {
	ssmBucketCmd.AddCommand(ssmBucketCreateCmd)
	ssmBucketCmd.AddCommand(ssmBucketStatusCmd)
	ssmBucketCmd.AddCommand(ssmBucketDeleteCmd)
	ssmBucketCmd.AddCommand(ssmBucketVerifyCmd)

	// Shared by every subcommand.
	ssmBucketCmd.PersistentFlags().String("name", "", "S3 bucket name (or TACK_SSM_BUCKET)")
	ssmBucketCmd.PersistentFlags().String("region", "", "AWS region (or TACK_SSM_REGION)")

	ssmBucketCreateCmd.Flags().String("kms-key-id", "", "KMS key ID/alias/ARN for SSE-KMS encryption (default: SSE-S3)")
	ssmBucketCreateCmd.Flags().Int32("lifecycle-days", 0, "Days before transfer objects expire (default: 1)")

	ssmBucketDeleteCmd.Flags().Bool("force", false, "Skip the interactive confirmation prompt")
	ssmBucketDeleteCmd.Flags().Bool("unmanaged", false, "Allow deleting a bucket tack didn't create (missing the ManagedBy=tack tag)")

	ssmBucketVerifyCmd.Flags().String("instance", "", "SSM-managed instance ID to round-trip a test transfer through (required)")
	ssmBucketVerifyCmd.Flags().Bool("attach-policy", false, "Deprecated/no-op: auto-attach is now on by default whenever a bucket is set")
}

// ssmBucketNameAndRegion resolves --name/--region, falling back to
// TACK_SSM_BUCKET/TACK_SSM_REGION — consistent with `run`'s
// --ssm-bucket/--ssm-region.
func ssmBucketNameAndRegion(cmd *cobra.Command) (name, region string, err error) {
	// Try cmd.Flags() first (works during cobra Execute()), then
	// InheritedFlags() as fallback (works when RunE is called directly in
	// tests) — same pattern as resolveVaultPassword.
	name, _ = cmd.Flags().GetString("name")
	if name == "" {
		name, _ = cmd.InheritedFlags().GetString("name")
	}
	if name == "" {
		name = os.Getenv("TACK_SSM_BUCKET")
	}
	if name == "" {
		return "", "", fmt.Errorf("bucket name required: pass --name or set TACK_SSM_BUCKET")
	}

	region, _ = cmd.Flags().GetString("region")
	if region == "" {
		region, _ = cmd.InheritedFlags().GetString("region")
	}
	if region == "" {
		region = os.Getenv("TACK_SSM_REGION")
	}
	return name, region, nil
}

func runSSMBucketCreate(cmd *cobra.Command, _ []string) error {
	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		return err
	}
	kmsKeyID, _ := cmd.Flags().GetString("kms-key-id")
	lifecycleDays, _ := cmd.Flags().GetInt32("lifecycle-days")

	ctx, cancel := signalContext(context.Background())
	defer cancel()

	m := ssmbucket.New(name, ssmbucket.WithRegion(region))
	result, err := m.Create(ctx, ssmbucket.CreateOptions{
		KMSKeyID:      kmsKeyID,
		LifecycleDays: lifecycleDays,
	})
	if err != nil {
		return err
	}

	if result.AlreadyExisted {
		fmt.Printf("Bucket %s already existed; configuration re-applied.\n", name)
	} else {
		fmt.Printf("Bucket %s created.\n", name)
	}
	return nil
}

func runSSMBucketStatus(cmd *cobra.Command, _ []string) error {
	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := signalContext(context.Background())
	defer cancel()

	m := ssmbucket.New(name, ssmbucket.WithRegion(region))
	status, err := m.Status(ctx)
	if err != nil {
		return err
	}

	if !status.Exists {
		fmt.Printf("Bucket %s does not exist.\n", name)
		return nil
	}

	encryption := status.Encryption
	if encryption == "" {
		encryption = "(none)"
	}
	versioning := status.Versioning
	if versioning == "" {
		versioning = "(never enabled)"
	}

	fmt.Printf("Bucket:                 %s\n", name)
	fmt.Printf("Managed by tack:        %v\n", status.Managed)
	fmt.Printf("Encryption:             %s\n", encryption)
	if status.KMSKeyID != "" {
		fmt.Printf("KMS key:                %s\n", status.KMSKeyID)
	}
	fmt.Printf("Public access blocked:  %v\n", status.PublicAccessBlocked)
	fmt.Printf("Versioning:             %s\n", versioning)
	fmt.Printf("Lifecycle rule:         %v", status.LifecycleConfigured)
	if status.LifecycleConfigured {
		fmt.Printf(" (expires after %d day(s))", status.LifecycleDays)
	}
	fmt.Println()

	if !status.Managed {
		fmt.Println("\nWarning: this bucket was not created by tack (missing the ManagedBy=tack tag).")
		fmt.Println("`ssm-bucket delete` will refuse to touch it unless you pass --unmanaged.")
	}
	return nil
}

func runSSMBucketDelete(cmd *cobra.Command, _ []string) error {
	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")
	unmanaged, _ := cmd.Flags().GetBool("unmanaged")

	ctx, cancel := signalContext(context.Background())
	defer cancel()

	m := ssmbucket.New(name, ssmbucket.WithRegion(region))

	if !force {
		preview, err := m.Preview(ctx)
		if err != nil {
			return fmt.Errorf("failed to preview bucket %s before deletion: %w", name, err)
		}
		if !confirmBucketDelete(name, preview) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	result, err := m.Delete(ctx, ssmbucket.DeleteOptions{Unmanaged: unmanaged})
	if err != nil {
		if errors.Is(err, ssmbucket.ErrNotManaged) {
			return fmt.Errorf("%w (pass --unmanaged if you're sure)", err)
		}
		return err
	}

	fmt.Printf("Deleted bucket %s (%d object(s), %d version(s), %d delete marker(s), %d multipart upload(s) removed).\n",
		name, result.ObjectsRemoved, result.VersionsRemoved, result.DeleteMarkersRemoved, result.MultipartUploadsAborted)
	return nil
}

// confirmBucketDelete shows what will be destroyed and asks for
// interactive yes/no confirmation. Returns false on EOF/non-approval.
func confirmBucketDelete(name string, preview *ssmbucket.DeletePreview) bool {
	fmt.Printf("This will permanently delete bucket %q and everything in it:\n", name)
	fmt.Printf("  %d object(s), %d version(s), %d delete marker(s), %d incomplete multipart upload(s)\n",
		preview.Objects, preview.Versions, preview.DeleteMarkers, preview.MultipartUploads)
	fmt.Print("Proceed? (yes/no): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	return output.IsApproval(strings.TrimSpace(scanner.Text()))
}

func runSSMBucketVerify(cmd *cobra.Command, _ []string) error {
	name, region, err := ssmBucketNameAndRegion(cmd)
	if err != nil {
		return err
	}
	instance, _ := cmd.Flags().GetString("instance")
	if instance == "" {
		return fmt.Errorf("--instance is required")
	}
	attachPolicy, _ := cmd.Flags().GetBool("attach-policy")

	ctx, cancel := signalContext(context.Background())
	defer cancel()

	m := ssmbucket.New(name, ssmbucket.WithRegion(region))
	result, err := m.Verify(ctx, ssmbucket.VerifyOptions{
		InstanceID:   instance,
		AttachPolicy: attachPolicy,
	})
	if err != nil {
		return err
	}

	fmt.Printf("OK: round-tripped %d bytes through bucket %s via instance %s.\n", result.BytesTransferred, name, instance)
	return nil
}
