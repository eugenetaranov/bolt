package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSourceInstallPipeline exercises get_url -> unarchive -> make end-to-end
// inside a container: it fetches a tarball (via file:// so no external network
// is needed), extracts it, builds a trivial C project, and installs the binary.
// A second run must be fully idempotent.
func TestSourceInstallPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	ctx := context.Background()
	name := "tack-src-install"
	cont, err := startPrivilegedContainer(ctx, "ubuntu:24.04", name)
	require.NoError(t, err, "failed to start container")
	t.Cleanup(func() {
		_ = cont.Terminate(ctx)
		removeContainer(name)
	})

	// Toolchain: get_url needs curl, unarchive needs tar, make needs a compiler.
	code, out, err := execInContainer(ctx, cont, []string{"sh", "-c",
		"apt-get update -qq && apt-get install -y -qq build-essential curl tar"})
	require.NoError(t, err)
	require.Equal(t, 0, code, "toolchain install failed: %s", out)

	// Create a trivial source project and tar it up (the "release" artifact).
	setup := `set -e
mkdir -p /srv/hello-1.0
cat > /srv/hello-1.0/hello.c <<'EOF'
#include <stdio.h>
int main(void){ printf("hello from tack\n"); return 0; }
EOF
printf 'hello: hello.c\n\tcc -o hello hello.c\ninstall:\n\tinstall -m 0755 hello /usr/local/bin/hello\n' > /srv/hello-1.0/Makefile
tar czf /srv/hello-1.0.tar.gz -C /srv hello-1.0`
	code, out, err = execInContainer(ctx, cont, []string{"sh", "-c", setup})
	require.NoError(t, err)
	require.Equal(t, 0, code, "project setup failed: %s", out)

	tasks := `  - name: fetch source
    get_url:
      url: file:///srv/hello-1.0.tar.gz
      dest: /tmp/hello-1.0.tar.gz
  - name: extract source
    unarchive:
      src: /tmp/hello-1.0.tar.gz
      dest: /usr/local/src
      creates: /usr/local/src/hello-1.0
  - name: build and install
    make:
      chdir: /usr/local/src/hello-1.0
      install: true
      creates: /usr/local/bin/hello`

	// First run builds everything.
	runModulePlaybook(t, name, tasks)
	assertFileExists(t, ctx, cont, "/usr/local/bin/hello")
	assertCommandOutput(t, ctx, cont, []string{"/usr/local/bin/hello"}, []string{"hello from tack"})

	// Second run must be a no-op across all three modules.
	out2 := runModulePlaybook(t, name, tasks)
	require.Contains(t, out2, "changed=0", "second run should be idempotent:\n%s", out2)
}

// runModulePlaybook writes and runs a one-off playbook targeting the named
// container via the docker connector, returning the combined output.
func runModulePlaybook(t *testing.T, host, tasks string) string {
	t.Helper()
	pb := fmt.Sprintf("name: source-install-it\nhosts: %s\nconnection: docker\ngather_facts: false\ntasks:\n%s", host, tasks)
	path := filepath.Join(t.TempDir(), "pipeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(pb), 0o644))

	cmd := exec.Command(tackBinaryPath, "run", path, "--auto-approve")
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "playbook failed:\n%s", string(out))
	t.Logf("playbook output:\n%s", string(out))
	return string(out)
}
