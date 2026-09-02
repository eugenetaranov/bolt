package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tackhq/tack/internal/vault"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// zero overwrites a byte slice, best-effort scrubbing of secret material.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// secureVaultTempDir creates a private 0700 directory for editing decrypted
// vault content. The plaintext file (and any editor swap/backup files created
// alongside it) stay inside this dir on the user's temp filesystem, and the
// returned cleanup removes the whole directory. This avoids leaving cleartext
// or editor swap files loose in a world-listable /tmp.
func secureVaultTempDir() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "tack-vault-")
	if err != nil {
		return "", nil, err
	}
	// os.MkdirTemp already uses 0700; enforce it explicitly for defense.
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// vaultCmd is the parent command for vault operations.
var vaultCmd = &cobra.Command{
	Use:   "vault",
	Short: "Manage encrypted vault files",
	Long: `Manage encrypted vault files (AES-256-GCM).

Password sources, most to least secure:
  --vault-password-file <path>   read the first line of a file
  interactive prompt             typed, never stored
  TACK_VAULT_PASSWORD env var    convenient but readable via /proc/<pid>/environ
                                 by same-uid and root processes; avoid on shared
                                 or multi-tenant hosts.

While editing, decrypted content and any editor swap/backup files are kept in a
private 0700 temp directory and removed on exit.`,
}

// vaultInitCmd creates a new encrypted vault file.
var vaultInitCmd = &cobra.Command{
	Use:   "init <file>",
	Short: "Create a new encrypted vault file",
	Args:  cobra.ExactArgs(1),
	RunE:  runVaultInit,
}

// vaultEditCmd edits an existing encrypted vault file.
var vaultEditCmd = &cobra.Command{
	Use:   "edit <file>",
	Short: "Edit an encrypted vault file",
	Args:  cobra.ExactArgs(1),
	RunE:  runVaultEdit,
}

func init() {
	vaultCmd.AddCommand(vaultInitCmd)
	vaultCmd.AddCommand(vaultEditCmd)
	// PersistentFlags so both subcommands inherit via cmd.Flags()
	vaultCmd.PersistentFlags().String("vault-password-file", "", "Path to file containing vault password")
}

// resolveVaultPassword returns a []byte password using the resolution chain:
// TACK_VAULT_PASSWORD env > --vault-password-file flag > interactive prompt.
// confirmPrompt=true prompts twice and verifies match (for vault init).
// When env or file source is used, confirmation is always skipped.
func resolveVaultPassword(cmd *cobra.Command, confirmPrompt bool) ([]byte, error) {
	// 1. Environment variable (highest priority; skip confirmation even if confirmPrompt)
	if envPw := os.Getenv("TACK_VAULT_PASSWORD"); envPw != "" {
		return []byte(envPw), nil
	}

	// 2. Password file flag (inherited via PersistentFlags on parent).
	// Try cmd.Flags() first (works during cobra Execute()), then InheritedFlags()
	// as fallback (works when RunE is called directly in tests).
	vaultPwFile, _ := cmd.Flags().GetString("vault-password-file")
	if vaultPwFile == "" {
		vaultPwFile, _ = cmd.InheritedFlags().GetString("vault-password-file")
	}
	if vaultPwFile != "" {
		data, err := os.ReadFile(vaultPwFile)
		if err != nil {
			return nil, fmt.Errorf("--vault-password-file: %w", err)
		}
		// First line only
		line := strings.SplitN(string(data), "\n", 2)[0]
		return []byte(line), nil
	}

	// 3. Interactive prompt
	fmt.Fprint(os.Stderr, "Enter vault password: ")
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}

	if confirmPrompt {
		fmt.Fprint(os.Stderr, "Confirm vault password: ")
		pw2, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("reading confirmation password: %w", err)
		}
		if !bytes.Equal(pw, pw2) {
			return nil, fmt.Errorf("passwords do not match")
		}
	}

	return pw, nil
}

// launchEditor opens the file at path in $EDITOR (or vi as fallback).
// The editor is attached to the process's stdin/stdout/stderr.
func launchEditor(ctx context.Context, path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.CommandContext(ctx, editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// atomicWrite writes data to a temp file in the same directory as dst,
// then renames it atomically to dst. On failure the temp file is removed.
// This ensures the vault file is never left in a partially-written state.
func atomicWrite(dst string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tack-vault-*.tmp")
	if err != nil {
		return fmt.Errorf("atomic write: create temp: %w", err)
	}
	tmpName := tmp.Name()
	wrote := false
	defer func() {
		if !wrote {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("atomic write: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic write: close: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("atomic write: chmod: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("atomic write: rename: %w", err)
	}
	wrote = true
	return nil
}

// runVaultInit implements `tack vault init <file>`.
// It checks that the file does not exist, prompts for a password with confirmation,
// opens $EDITOR with scaffold content, then encrypts and writes the result.
func runVaultInit(cmd *cobra.Command, args []string) error {
	vaultPath := args[0]

	// D-08: Refuse if file already exists
	if _, err := os.Stat(vaultPath); err == nil {
		return fmt.Errorf("file already exists: %s. Use 'tack vault edit' to modify it", vaultPath)
	}

	// D-09/D-06: Resolve password with confirmation for interactive prompts
	pw, err := resolveVaultPassword(cmd, true)
	if err != nil {
		return err
	}
	defer zero(pw)

	// Write scaffold content to a private 0700 dir (contains editor swap files).
	scaffoldContent := "# Add your secrets as YAML key-value pairs below\ndb_password: changeme\n"
	tmpDir, cleanupDir, err := secureVaultTempDir()
	if err != nil {
		return fmt.Errorf("creating secure temp dir: %w", err)
	}
	tmpPath := filepath.Join(tmpDir, "vault.yaml")

	// D-11/CLI-04: Wire cleanup to signal context + defer
	ctx, cancel := signalContext(context.Background())
	defer cancel()

	go func() {
		<-ctx.Done()
		cleanupDir()
	}()
	defer cleanupDir()

	if err := os.WriteFile(tmpPath, []byte(scaffoldContent), 0o600); err != nil {
		return fmt.Errorf("writing scaffold to temp file: %w", err)
	}

	// Launch editor
	if err := launchEditor(ctx, tmpPath); err != nil {
		return fmt.Errorf("editor exited with error: %w", err)
	}

	// Read edited content
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("reading edited file: %w", err)
	}
	defer zero(edited)

	// Encrypt the content
	encrypted, err := vault.Encrypt(edited, pw)
	if err != nil {
		return fmt.Errorf("encrypting vault: %w", err)
	}

	// D-13/CLI-03: Atomic write to target path
	if err := atomicWrite(vaultPath, encrypted, 0600); err != nil {
		return fmt.Errorf("writing vault file: %w", err)
	}

	// D-05: Print success message
	fmt.Fprintln(os.Stderr, "Vault file encrypted successfully.")
	return nil
}

// runVaultEdit implements `tack vault edit <file>`.
// It decrypts the vault, opens $EDITOR, detects no-op changes, and re-encrypts if modified.
func runVaultEdit(cmd *cobra.Command, args []string) error {
	vaultPath := args[0]

	// Read existing vault file
	data, err := os.ReadFile(vaultPath)
	if err != nil {
		return fmt.Errorf("reading vault: %w", err)
	}

	// Resolve password (no confirmation for edit)
	pw, err := resolveVaultPassword(cmd, false)
	if err != nil {
		return err
	}

	// Scrub the password no matter which path we return through.
	defer zero(pw)

	// Decrypt vault content
	plaintext, err := vault.Decrypt(data, pw)
	if err != nil {
		return fmt.Errorf("decrypting vault: %w", err)
	}
	defer zero(plaintext)

	// Write plaintext to a private 0700 dir (contains editor swap files too).
	tmpDir, cleanupDir, err := secureVaultTempDir()
	if err != nil {
		return fmt.Errorf("creating secure temp dir: %w", err)
	}
	tmpPath := filepath.Join(tmpDir, "vault.yaml")

	// D-11/CLI-04: Wire cleanup to signal context + defer
	ctx, cancel := signalContext(context.Background())
	defer cancel()

	go func() {
		<-ctx.Done()
		cleanupDir()
	}()
	defer cleanupDir()

	if err := os.WriteFile(tmpPath, plaintext, 0o600); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}

	// Launch editor
	if err := launchEditor(ctx, tmpPath); err != nil {
		// D-02: Non-zero editor exit → abort, keep original vault unchanged
		return fmt.Errorf("editor exited with error: %w", err)
	}

	// Read edited content
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("reading edited file: %w", err)
	}
	defer zero(edited)

	// D-04/CLI-05: No-op detection — skip re-encryption if content unchanged
	if subtle.ConstantTimeCompare(plaintext, edited) == 1 {
		fmt.Fprintln(os.Stderr, "No changes detected, vault unchanged.")
		return nil
	}

	// Re-encrypt with fresh salt/nonce
	encrypted, err := vault.Encrypt(edited, pw)
	if err != nil {
		return fmt.Errorf("encrypting vault: %w", err)
	}

	// D-13/CLI-03: Atomic write to vault path
	if err := atomicWrite(vaultPath, encrypted, 0600); err != nil {
		return fmt.Errorf("writing vault file: %w", err)
	}

	// D-05: Print success message
	fmt.Fprintln(os.Stderr, "Vault file encrypted successfully.")
	return nil
}
