package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKnownHostsConfig_ScopesAlgorithms verifies that host-key negotiation is
// restricted to the key types pinned in known_hosts for the host (as OpenSSH
// does), so a server offering another key type (e.g. ecdsa/rsa) does not cause
// a spurious "key mismatch" when only the ed25519 key is pinned.
func TestKnownHostsConfig_ScopesAlgorithms(t *testing.T) {
	tmp := t.TempDir()
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pin only an ed25519 key for testhost.
	line := "testhost ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIP9HD0vJnFTodndvXqQo5M8PWm2hnt6tq9twcpb1ViuI\n"
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)

	cb, algos, err := knownHostsConfig("testhost:22")
	if err != nil {
		t.Fatalf("knownHostsConfig: %v", err)
	}
	if cb == nil {
		t.Fatal("expected a non-nil host key callback")
	}
	if len(algos) == 0 {
		t.Fatal("expected pinned algorithms for a known host")
	}
	joined := strings.Join(algos, ",")
	if !strings.Contains(joined, "ssh-ed25519") {
		t.Errorf("expected ed25519 in scoped algorithms, got %v", algos)
	}
	for _, unpinned := range []string{"rsa", "ecdsa"} {
		if strings.Contains(joined, unpinned) {
			t.Errorf("scoped algorithms should not include unpinned %q type: %v", unpinned, algos)
		}
	}

	// An unknown host yields no scoped algorithms (first-connect behavior).
	if _, algos2, err := knownHostsConfig("unknownhost:22"); err != nil {
		t.Fatalf("knownHostsConfig(unknown): %v", err)
	} else if len(algos2) != 0 {
		t.Errorf("expected no algorithms for an unknown host, got %v", algos2)
	}
}
