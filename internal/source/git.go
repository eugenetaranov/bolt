package source

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// GitSource clones a git repository and extracts a playbook (or role
// directory) from it.
type GitSource struct {
	RepoURL string
	Ref     string // branch, tag, or commit (empty = default branch)
	Path    string // path within the repo
}

func parseGitSource(ref string) (*GitSource, error) {
	repo, path, err := splitRepoPath(ref)
	if err != nil {
		return nil, err
	}
	repoURL, gitRef := splitRepoRef(repo)
	return &GitSource{
		RepoURL: repoURL,
		Ref:     gitRef,
		Path:    path,
	}, nil
}

// Fetch clones RepoURL@Ref (once per process, even across multiple
// GitSource values — see cloneRepoOnce) and returns the path to Path
// within that clone.
func (s *GitSource) Fetch(ctx context.Context) (string, func(), error) {
	tmpDir, err := cloneRepoOnce(ctx, s.RepoURL, s.Ref)
	if err != nil {
		return "", nil, err
	}

	playbookPath := filepath.Join(tmpDir, s.Path)
	if _, err := os.Stat(playbookPath); err != nil {
		return "", nil, fmt.Errorf("playbook not found in repo: %s: %w", s.Path, err)
	}

	// os.RemoveAll on an already-removed directory is a no-op, so it's
	// safe for every GitSource sharing this clone (same repo/ref, e.g.
	// several roles from one collection repo) to each get their own
	// cleanup closure over the same tmpDir — whichever fires last does
	// the real work.
	cleanup := func() { os.RemoveAll(tmpDir) }
	return playbookPath, cleanup, nil
}

// gitCloneFuture represents an in-flight or completed clone of one
// repo@ref, shared by every caller requesting that same key.
type gitCloneFuture struct {
	done chan struct{}
	dir  string
	err  error
}

var (
	gitCloneMu    sync.Mutex
	gitCloneCache = map[string]*gitCloneFuture{}
)

// cloneRepoOnce clones repoURL@ref into a fresh temp directory the first
// time it's called for that key, and returns the same directory to every
// subsequent caller (concurrent or not) with the same key — so a
// playbook referencing many roles/files from one repo only clones it
// once. A failed clone is not cached, so a later retry (e.g. after a
// transient network error) attempts a fresh clone.
func cloneRepoOnce(ctx context.Context, repoURL, ref string) (string, error) {
	key := repoURL + "@" + ref

	gitCloneMu.Lock()
	future, inFlight := gitCloneCache[key]
	if !inFlight {
		future = &gitCloneFuture{done: make(chan struct{})}
		gitCloneCache[key] = future
	}
	gitCloneMu.Unlock()

	if inFlight {
		select {
		case <-future.done:
			return future.dir, future.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	future.dir, future.err = doClone(ctx, repoURL, ref)
	close(future.done)

	if future.err != nil {
		gitCloneMu.Lock()
		delete(gitCloneCache, key)
		gitCloneMu.Unlock()
	}

	return future.dir, future.err
}

// doClone performs the actual `git clone --depth 1` into a fresh temp
// directory, cleaning it up on failure.
func doClone(ctx context.Context, repoURL, ref string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "tack-git-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, tmpDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return tmpDir, nil
}
