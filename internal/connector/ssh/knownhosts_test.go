package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestConnect_FailsClosedWithoutKnownHosts verifies that when host-key
// verification cannot be established (no known_hosts file) and --insecure was
// not requested, Connect refuses rather than silently disabling verification.
func TestConnect_FailsClosedWithoutKnownHosts(t *testing.T) {
	empty := t.TempDir() // no .ssh/known_hosts here
	t.Setenv("HOME", empty)

	// Password auth so buildAuthMethods is non-empty; Connect then reaches the
	// host-key stage and must fail closed there (before dialing).
	c := New("192.0.2.1", WithUser("root"), WithPassword("x"), WithTimeout(2*time.Second))
	err := c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail closed without known_hosts")
	}
	if !strings.Contains(err.Error(), "cannot verify SSH host key") {
		t.Fatalf("expected a fail-closed host-key error, got: %v", err)
	}
}
