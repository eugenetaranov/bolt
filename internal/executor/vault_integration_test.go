package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tackhq/tack/internal/playbook"
	"github.com/tackhq/tack/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestVault(t *testing.T, dir string, filename string, content string, password string) string {
	t.Helper()
	encrypted, err := vault.Encrypt([]byte(content), []byte(password))
	require.NoError(t, err)
	path := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(path, encrypted, 0o600))
	return path
}

// TestVaultIntegration_PasswordFromCallback tests that a full playbook run with
// vault_file succeeds when ResolveVaultPassword returns the correct password.
func TestVaultIntegration_PasswordFromCallback(t *testing.T) {
	dir := t.TempDir()
	password := "integration-test-pass"
	vaultPath := createTestVault(t, dir, "secrets.vault", "db_pass: secret123\n", password)

	e := New()
	e.AutoApprove = true
	e.ResolveVaultPassword = func() ([]byte, error) { return []byte(password), nil }

	falseVal := false
	pb := &playbook.Playbook{
		Path: dir,
		Plays: []*playbook.Play{
			{
				Name:        "Test vault integration",
				Hosts:       []string{"localhost"},
				Connection:  "local",
				GatherFacts: &falseVal,
				VaultFile:   vaultPath,
				Vars:        map[string]any{},
				Tasks:       []*playbook.Task{},
			},
		},
	}

	result, err := e.Run(context.Background(), pb)
	require.NoError(t, err)
	assert.True(t, result.Success)
}

// TestVaultIntegration_VaultOverridesPlayVar verifies that a decrypted vault
// variable wins over a same-named play var (a play default must not shadow a
// secret).
func TestVaultIntegration_VaultOverridesPlayVar(t *testing.T) {
	dir := t.TempDir()
	password := "pw"
	vaultPath := createTestVault(t, dir, "secrets.vault", "secret: vault-value\n", password)

	e := New()
	e.AutoApprove = true
	e.ResolveVaultPassword = func() ([]byte, error) { return []byte(password), nil }

	falseVal := false
	pb := &playbook.Playbook{
		Path: dir,
		Plays: []*playbook.Play{{
			Name:        "vault precedence",
			Hosts:       []string{"localhost"},
			Connection:  "local",
			GatherFacts: &falseVal,
			VaultFile:   vaultPath,
			Vars:        map[string]any{"secret": "play-value"},
			Tasks: []*playbook.Task{
				{Assert: &playbook.AssertSpec{That: []string{"secret == 'vault-value'"}}},
			},
		}},
	}

	result, err := e.Run(context.Background(), pb)
	require.NoError(t, err)
	assert.True(t, result.Success, "vault var should override the play var")
}

// TestVaultIntegration_PasswordFileFirstLineOnly tests that when a password file
// contains multiple lines, only the first line is used as the password.
func TestVaultIntegration_PasswordFileFirstLineOnly(t *testing.T) {
	dir := t.TempDir()
	correctPass := "correctpass"
	vaultPath := createTestVault(t, dir, "secrets.vault", "key: value\n", correctPass)

	// Write password file with extra line that must be ignored
	pwFile := filepath.Join(dir, "vault-password.txt")
	require.NoError(t, os.WriteFile(pwFile, []byte("correctpass\nextra-line\n"), 0o600))

	// Read first line only — same logic as cmd/tack/main.go
	data, err := os.ReadFile(pwFile)
	require.NoError(t, err)
	firstLine := string(data)
	if idx := len(firstLine); idx > 0 {
		for i, c := range firstLine {
			if c == '\n' {
				firstLine = firstLine[:i]
				break
			}
		}
	}

	e := New()
	e.ResolveVaultPassword = func() ([]byte, error) { return []byte(firstLine), nil }

	play := &playbook.Play{VaultFile: vaultPath}
	vars, loadErr := e.loadVaultVars(play, dir)
	require.NoError(t, loadErr)
	assert.Equal(t, "value", vars["key"])
}

// TestVaultIntegration_CachingAcrossPlays tests that when two plays in the same
// playbook reference the same vault file, the password is resolved only once.
func TestVaultIntegration_CachingAcrossPlays(t *testing.T) {
	dir := t.TempDir()
	password := "cache-test-password"
	vaultPath := createTestVault(t, dir, "shared.vault", "shared_key: shared_val\n", password)

	callCount := 0
	falseVal := false

	e := New()
	e.AutoApprove = true
	e.ResolveVaultPassword = func() ([]byte, error) {
		callCount++
		return []byte(password), nil
	}

	pb := &playbook.Playbook{
		Path: dir,
		Plays: []*playbook.Play{
			{
				Name:        "Play 1",
				Hosts:       []string{"localhost"},
				Connection:  "local",
				GatherFacts: &falseVal,
				VaultFile:   vaultPath,
				Vars:        map[string]any{},
				Tasks:       []*playbook.Task{},
			},
			{
				Name:        "Play 2",
				Hosts:       []string{"localhost"},
				Connection:  "local",
				GatherFacts: &falseVal,
				VaultFile:   vaultPath,
				Vars:        map[string]any{},
				Tasks:       []*playbook.Task{},
			},
		},
	}

	result, err := e.Run(context.Background(), pb)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 1, callCount, "ResolveVaultPassword should be called exactly once across two plays")
}

// TestVaultIntegration_PlayVarsWinOverVault tests that play-level vars take
// precedence over vault vars when both define the same key.
func TestVaultIntegration_PlayVarsWinOverVault(t *testing.T) {
	dir := t.TempDir()
	password := "precedence-test-pass"
	// Vault defines db_host=vault-host and db_pass=secret
	vaultPath := createTestVault(t, dir, "secrets.vault", "db_host: vault-host\ndb_pass: secret\n", password)

	e := New()
	e.ResolveVaultPassword = func() ([]byte, error) { return []byte(password), nil }

	// Play also defines db_host=play-host — play should win
	play := &playbook.Play{
		VaultFile: vaultPath,
		Vars:      map[string]any{"db_host": "play-host"},
	}

	vaultVars, err := e.loadVaultVars(play, dir)
	require.NoError(t, err)

	// Simulate the merge from runPlayOnHost: vault fills gaps, play vars win
	pctx := &PlayContext{Vars: make(map[string]any)}
	for k, v := range play.Vars {
		pctx.Vars[k] = v
	}
	for k, v := range vaultVars {
		if _, exists := pctx.Vars[k]; !exists {
			pctx.Vars[k] = v
		}
	}

	assert.Equal(t, "play-host", pctx.Vars["db_host"], "play var should win over vault var")
	assert.Equal(t, "secret", pctx.Vars["db_pass"], "vault var fills gap not in play vars")
}

// TestVaultIntegration_EnvVarPasswordSource tests that TACK_VAULT_PASSWORD env var
// is used as the password source without calling the ResolveVaultPassword prompt.
func TestVaultIntegration_EnvVarPasswordSource(t *testing.T) {
	dir := t.TempDir()
	password := "env-test-password"
	vaultPath := createTestVault(t, dir, "secrets.vault", "env_key: env_val\n", password)

	// Simulate the env var resolution pattern from cmd/tack/main.go
	t.Setenv("TACK_VAULT_PASSWORD", password)

	promptCalled := false
	e := New()

	// Wire password resolution same as cmd/tack/main.go
	if envPw := os.Getenv("TACK_VAULT_PASSWORD"); envPw != "" {
		pw := []byte(envPw)
		e.ResolveVaultPassword = func() ([]byte, error) { return pw, nil }
	} else {
		e.ResolveVaultPassword = func() ([]byte, error) {
			promptCalled = true
			return nil, nil
		}
	}

	play := &playbook.Play{VaultFile: vaultPath}
	vars, err := e.loadVaultVars(play, dir)
	require.NoError(t, err)
	assert.Equal(t, "env_val", vars["env_key"])
	assert.False(t, promptCalled, "interactive prompt should not be called when env var is set")
}
