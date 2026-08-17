package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestGitRepo creates a local git repo (no network needed — git
// clone works fine against a plain filesystem path) with the given
// files, committed on a "main" branch. Returns the repo directory,
// usable directly as a GitSource.RepoURL.
func initTestGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	writeAndCommit(t, dir, files, "initial")
	return dir
}

func writeAndCommit(t *testing.T, dir string, files map[string]string, message string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", message)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

func TestGitSource_Fetch_CachesSameRepoRef(t *testing.T) {
	repoDir := initTestGitRepo(t, map[string]string{
		"roleA/tasks/main.yaml": "- name: a\n",
		"roleB/tasks/main.yaml": "- name: b\n",
	})

	srcA := &GitSource{RepoURL: repoDir, Path: "roleA"}
	srcB := &GitSource{RepoURL: repoDir, Path: "roleB"}

	pathA, cleanupA, err := srcA.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanupA()

	pathB, cleanupB, err := srcB.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanupB()

	assert.Equal(t, filepath.Dir(pathA), filepath.Dir(pathB),
		"fetching two paths from the same repo/ref should reuse a single clone")
}

func TestGitSource_Fetch_DifferentRefsCloneSeparately(t *testing.T) {
	repoDir := initTestGitRepo(t, map[string]string{"roleA/tasks/main.yaml": "- name: a\n"})
	runGit(t, repoDir, "checkout", "-q", "-b", "other")
	runGit(t, repoDir, "checkout", "-q", "main")

	srcMain := &GitSource{RepoURL: repoDir, Ref: "main", Path: "roleA"}
	srcOther := &GitSource{RepoURL: repoDir, Ref: "other", Path: "roleA"}

	pathMain, cleanupMain, err := srcMain.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanupMain()

	pathOther, cleanupOther, err := srcOther.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanupOther()

	assert.NotEqual(t, filepath.Dir(pathMain), filepath.Dir(pathOther),
		"different refs of the same repo must not share a clone")
}

func TestGitSource_Fetch_FailedCloneNotCached(t *testing.T) {
	bogusRepo := filepath.Join(t.TempDir(), "not-a-repo-yet")
	src := &GitSource{RepoURL: bogusRepo, Path: "file.txt"}

	_, _, err := src.Fetch(context.Background())
	require.Error(t, err)

	// Make it a real repo and retry with the same GitSource value — a
	// failed clone must not be cached, so this should succeed rather than
	// replaying the earlier error.
	require.NoError(t, os.MkdirAll(bogusRepo, 0755))
	runGit(t, bogusRepo, "init", "-q", "-b", "main")
	runGit(t, bogusRepo, "config", "user.email", "test@example.com")
	runGit(t, bogusRepo, "config", "user.name", "test")
	writeAndCommit(t, bogusRepo, map[string]string{"file.txt": "hello"}, "now real")

	path, cleanup, err := src.Fetch(context.Background())
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestGitSource_Fetch_ConcurrentCallsShareOneClone(t *testing.T) {
	repoDir := initTestGitRepo(t, map[string]string{"roleA/tasks/main.yaml": "- name: a\n"})

	const n = 5
	results := make([]string, n)
	cleanups := make([]func(), n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := &GitSource{RepoURL: repoDir, Path: "roleA"}
			results[i], cleanups[i], errs[i] = src.Fetch(context.Background())
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
	}
	for i := 1; i < n; i++ {
		assert.Equal(t, results[0], results[i], "concurrent fetches of the same repo/ref must resolve to the same clone")
	}
	for _, c := range cleanups {
		if c != nil {
			c()
		}
	}
}
