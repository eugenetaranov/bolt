package copy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tackhq/tack/internal/connector/local"
	"github.com/tackhq/tack/internal/module"
)

// buildTree writes a small directory tree under root:
//
//	root/
//	  top.txt
//	  sub/
//	    nested.txt
//	  empty/
func buildTree(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "top.txt"), "top")
	mustWrite(t, filepath.Join(root, "sub", "nested.txt"), "nested")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, params map[string]any) *module.Result {
	t.Helper()
	m := &Module{}
	res, err := m.Run(context.Background(), local.New(), params)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestDirSyncBasic(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "out")
	buildTree(t, src)

	// Trailing slash: contents land directly in dest.
	res := run(t, map[string]any{
		"src":      src + "/",
		"dest":     dest,
		"mode":     "0640",
		"dir_mode": "0750",
	})
	if !res.Changed {
		t.Fatalf("expected Changed, got %q", res.Message)
	}

	// Files present with the file mode.
	assertMode(t, filepath.Join(dest, "top.txt"), 0o640)
	assertMode(t, filepath.Join(dest, "sub", "nested.txt"), 0o640)
	assertContent(t, filepath.Join(dest, "sub", "nested.txt"), "nested")
	// Directories present with the dir mode, including the empty one.
	assertMode(t, filepath.Join(dest, "sub"), 0o750)
	assertMode(t, filepath.Join(dest, "empty"), 0o750)
}

func TestDirSyncNoTrailingSlash(t *testing.T) {
	srcParent := t.TempDir()
	src := filepath.Join(srcParent, "payload")
	buildTree(t, src)
	dest := filepath.Join(t.TempDir(), "out")

	run(t, map[string]any{"src": src, "dest": dest})

	// Without a trailing slash, the directory itself lands under dest.
	assertContent(t, filepath.Join(dest, "payload", "top.txt"), "top")
}

func TestDirSyncIdempotent(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "out")
	buildTree(t, src)

	params := map[string]any{"src": src + "/", "dest": dest, "mode": "0644", "dir_mode": "0755"}
	run(t, params)
	res := run(t, params)
	if res.Changed {
		t.Fatalf("second run should be unchanged, got %q", res.Message)
	}
}

func TestDirSyncDelete(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "out")
	buildTree(t, src)
	run(t, map[string]any{"src": src + "/", "dest": dest})

	// A stray remote file that is not in the source.
	stray := filepath.Join(dest, "sub", "stray.txt")
	mustWrite(t, stray, "stray")

	// delete=false leaves it.
	run(t, map[string]any{"src": src + "/", "dest": dest})
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("stray should survive without delete: %v", err)
	}

	// delete=true prunes it.
	res := run(t, map[string]any{"src": src + "/", "dest": dest, "delete": true})
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray should be pruned with delete=true, err=%v", err)
	}
	if !res.Changed {
		t.Fatalf("prune should report Changed, got %q", res.Message)
	}
}

func TestDirSyncSymlink(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "real.txt"), "real")
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")

	run(t, map[string]any{"src": src + "/", "dest": dest})

	link := filepath.Join(dest, "link.txt")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected a symlink at %s", link)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "real.txt" {
		t.Fatalf("symlink target = %q, want real.txt", target)
	}
}

func TestDirSyncValidateRejected(t *testing.T) {
	src := t.TempDir()
	buildTree(t, src)
	m := &Module{}
	_, err := m.Run(context.Background(), local.New(), map[string]any{
		"src":      src + "/",
		"dest":     filepath.Join(t.TempDir(), "out"),
		"validate": "true %s",
	})
	if err == nil {
		t.Fatal("expected error when validate is combined with a directory src")
	}
}

func TestCheckDirSync(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "out")
	buildTree(t, src)
	m := &Module{}

	// Nothing deployed yet: check should report changes.
	cr, err := m.Check(context.Background(), local.New(), map[string]any{"src": src + "/", "dest": dest})
	if err != nil {
		t.Fatal(err)
	}
	if !cr.WouldChange {
		t.Fatalf("expected WouldChange before deploy, got %q", cr.Message)
	}

	// After deploy, check should report no change.
	run(t, map[string]any{"src": src + "/", "dest": dest, "mode": "0644", "dir_mode": "0755"})
	cr, err = m.Check(context.Background(), local.New(), map[string]any{"src": src + "/", "dest": dest, "mode": "0644", "dir_mode": "0755"})
	if err != nil {
		t.Fatal(err)
	}
	if cr.WouldChange {
		t.Fatalf("expected no change after deploy, got %q", cr.Message)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q, want %q", path, data, want)
	}
}
