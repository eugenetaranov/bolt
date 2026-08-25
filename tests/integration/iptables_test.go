package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// iptablesDistro describes one target distribution for the iptables module
// integration matrix: the image, a best-effort command to install iptables,
// and the file the module is expected to write when persist:true.
type iptablesDistro struct {
	name        string
	image       string
	install     string // shell command; best-effort (packaging varies)
	persistPath string
	optional    bool // skip (not fail) when the image cannot be pulled
}

var iptablesDistros = []iptablesDistro{
	{
		name:        "ubuntu",
		image:       "ubuntu:24.04",
		install:     "apt-get update -qq && apt-get install -y -qq --no-install-recommends iptables",
		persistPath: "/etc/iptables/rules.v4",
	},
	{
		name:        "debian",
		image:       "debian:12",
		install:     "apt-get update -qq && apt-get install -y -qq --no-install-recommends iptables",
		persistPath: "/etc/iptables/rules.v4",
	},
	{
		name:        "fedora",
		image:       "fedora:latest",
		install:     "dnf install -y iptables-services || dnf install -y iptables-nft || dnf install -y iptables",
		persistPath: "/etc/sysconfig/iptables",
	},
	{
		// CentOS Linux is EOL; use Stream. Marked optional so the suite stays
		// green if the image is unavailable.
		name:        "centos-stream",
		image:       "quay.io/centos/centos:stream9",
		install:     "dnf install -y iptables-services || dnf install -y iptables-nft || dnf install -y iptables",
		persistPath: "/etc/sysconfig/iptables",
		optional:    true,
	},
	{
		name:        "arch",
		image:       "archlinux:latest",
		install:     "pacman -Sy --noconfirm iptables-nft || pacman -Sy --noconfirm iptables",
		persistPath: "/etc/iptables/iptables.rules",
	},
}

// TestIptablesModule exercises the iptables module end-to-end across several
// Linux distributions: it adds a rule (with persist), re-runs for idempotency,
// verifies the distro-specific persistence file, then removes the rule.
//
// The rule is tagged with a comment so it can be matched reliably in
// `iptables -S` output regardless of iptables' internal normalization.
func TestIptablesModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	ctx := context.Background()
	const comment = "tack test 8080"

	for _, d := range iptablesDistros {
		d := d
		t.Run(d.name, func(t *testing.T) {
			// Each distro uses a distinct container name, so they can run
			// concurrently to keep total wall-clock within the CI timeout.
			t.Parallel()
			name := "tack-ipt-" + d.name
			cont, err := startPrivilegedContainer(ctx, d.image, name)
			if err != nil {
				if d.optional {
					t.Skipf("skipping %s: could not start container: %v", d.image, err)
				}
				require.NoError(t, err, "failed to start %s", d.image)
			}
			t.Cleanup(func() {
				_ = cont.Terminate(ctx)
				removeContainer(name)
			})

			// Best-effort install; packaging differs per distro, so we don't
			// hard-fail here — the capability pre-check below decides whether
			// this environment can run iptables at all.
			_, installOut, _ := execInContainer(ctx, cont, []string{"sh", "-c", d.install})

			// Pre-check: can iptables actually run in this container/kernel?
			// If not (e.g. missing package or nft backend unavailable), skip
			// rather than fail — that is an environment limitation, not a
			// module bug.
			code, _, err := execInContainer(ctx, cont, []string{"iptables", "-w", "-S"})
			require.NoError(t, err)
			if code != 0 {
				t.Skipf("iptables cannot initialize in %s (install output:\n%s)", d.name, installOut)
			}

			// Add the rule (with persistence).
			addTasks := fmt.Sprintf(`  - name: allow 8080
    iptables:
      chain: INPUT
      protocol: tcp
      destination_port: 8080
      jump: ACCEPT
      comment: %q
      persist: true
`, comment)
			runIptablesPlaybook(t, name, addTasks)

			// The rule should now be present, with its comment and port.
			rules := iptablesDump(ctx, t, cont)
			require.Contains(t, rules, "--dport 8080", "rule should be present after add:\n%s", rules)
			require.Contains(t, rules, comment, "rule comment should be present:\n%s", rules)

			// Re-running must be idempotent: exactly one matching rule.
			runIptablesPlaybook(t, name, addTasks)
			if got := strings.Count(iptablesDump(ctx, t, cont), comment); got != 1 {
				t.Errorf("expected exactly one rule after idempotent re-run, found %d", got)
			}

			// Persistence: the distro-specific file should exist and contain
			// the rule.
			assertFileExists(t, ctx, cont, d.persistPath)
			assertFileContains(t, ctx, cont, d.persistPath, []string{"8080"})

			// Remove the rule.
			removeTasks := fmt.Sprintf(`  - name: remove 8080
    iptables:
      chain: INPUT
      protocol: tcp
      destination_port: 8080
      jump: ACCEPT
      comment: %q
      state: absent
`, comment)
			runIptablesPlaybook(t, name, removeTasks)
			if got := strings.Count(iptablesDump(ctx, t, cont), comment); got != 0 {
				t.Errorf("expected rule to be removed, still found %d", got)
			}
		})
	}
}

// startPrivilegedContainer starts a named, privileged container from a raw
// image running `sleep`. Privileged mode gives the container the NET_ADMIN
// capability needed to modify its own iptables ruleset.
func startPrivilegedContainer(ctx context.Context, image, name string) (testcontainers.Container, error) {
	removeContainer(name)
	req := testcontainers.ContainerRequest{
		Image: image,
		Name:  name,
		Cmd:   []string{"sleep", "900"},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Privileged = true
		},
		WaitingFor: wait.ForExec([]string{"true"}).WithStartupTimeout(90 * time.Second),
	}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

// removeContainer force-removes a container by name, ignoring errors.
func removeContainer(name string) {
	_ = exec.Command("docker", "rm", "-f", name).Run()
}

// runIptablesPlaybook writes and runs a one-off playbook targeting the named
// container via the docker connector, failing the test if tack errors.
func runIptablesPlaybook(t *testing.T, host, tasks string) {
	t.Helper()
	pb := fmt.Sprintf("name: iptables-it\nhosts: %s\nconnection: docker\ntasks:\n%s", host, tasks)
	path := filepath.Join(t.TempDir(), "iptables.yaml")
	require.NoError(t, os.WriteFile(path, []byte(pb), 0o644))

	cmd := exec.Command(tackBinaryPath, "run", path, "--auto-approve")
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "iptables playbook failed:\n%s", string(out))
	t.Logf("playbook output:\n%s", string(out))
}

// iptablesDump returns `iptables -S INPUT` from the container.
func iptablesDump(ctx context.Context, t *testing.T, cont testcontainers.Container) string {
	t.Helper()
	code, out, err := execInContainer(ctx, cont, []string{"iptables", "-w", "-S", "INPUT"})
	require.NoError(t, err)
	require.Equal(t, 0, code, "iptables -S failed:\n%s", out)
	return out
}
