// Package source resolves playbook references to local file paths.
// It supports local files, git repos, S3 buckets, and plain HTTP URLs.
package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Source resolves a playbook reference to a local file path.
type Source interface {
	// Fetch downloads/clones the source to a local temp directory.
	// Returns the path to the playbook file and a cleanup function.
	Fetch(ctx context.Context) (playbookPath string, cleanup func(), err error)
}

// Resolve parses a playbook reference string and returns the appropriate Source.
func Resolve(ref string) (Source, error) {
	switch {
	case strings.HasPrefix(ref, "git@"):
		return parseGitSource(ref)
	case strings.HasPrefix(ref, "s3://"):
		return parseS3Source(ref)
	case (strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://")) && strings.Contains(ref, ".git"):
		return parseGitSource(ref)
	case isGitHubURL(ref):
		return parseGitHubURL(ref)
	case strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://"):
		return parseHTTPSource(ref)
	default:
		return &LocalSource{Path: ref}, nil
	}
}

func noop() {}

// splitRepoPath splits a reference at "//" into the repo part and the in-repo path.
func splitRepoPath(ref string) (repo, path string, err error) {
	// Find "//" that separates repo URL from path within repo.
	// For git SSH (git@...), search from the start.
	// For HTTPS, skip past "https://" before searching.
	searchFrom := 0
	if strings.HasPrefix(ref, "https://") {
		searchFrom = len("https://")
	} else if strings.HasPrefix(ref, "http://") {
		searchFrom = len("http://")
	}

	idx := strings.Index(ref[searchFrom:], "//")
	if idx == -1 {
		return "", "", fmt.Errorf("missing '//' separator between repo URL and path in: %s", ref)
	}
	idx += searchFrom

	repo = ref[:idx]
	path = ref[idx+2:]
	if path == "" {
		return "", "", fmt.Errorf("empty path after '//' in: %s", ref)
	}
	return repo, path, nil
}

// splitRepoRef splits "repo@ref" into repo and ref parts.
// If no @ref is present, returns the repo as-is and empty ref.
func splitRepoRef(repo string) (string, string) {
	// For git SSH URLs like git@github.com:user/repo.git@main,
	// we need to find the last @ that comes after .git
	if strings.HasPrefix(repo, "git@") {
		gitIdx := strings.Index(repo, ".git")
		if gitIdx == -1 {
			return repo, ""
		}
		rest := repo[gitIdx+4:]
		if strings.HasPrefix(rest, "@") {
			return repo[:gitIdx+4], rest[1:]
		}
		return repo, ""
	}

	// For HTTPS URLs like https://github.com/user/repo.git@main
	gitIdx := strings.Index(repo, ".git")
	if gitIdx == -1 {
		return repo, ""
	}
	rest := repo[gitIdx+4:]
	if strings.HasPrefix(rest, "@") {
		return repo[:gitIdx+4], rest[1:]
	}
	return repo, ""
}

// isGitHubURL checks if ref is a GitHub or GitLab browse URL: either the
// bare repo homepage (https://<host>/<owner>/<repo>, exactly what's in the
// address bar when you're looking at the repo root — no ".git", no
// "/tree/") or a folder/file page (.../(tree|blob)/<ref>/<path...>, what's
// in the address bar after navigating into a subdirectory or file). Either
// form can be pasted as-is as a role or playbook source.
func isGitHubURL(ref string) bool {
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := u.Hostname()
	if host != "github.com" && host != "gitlab.com" {
		return false
	}
	segments := browseURLSegments(u.Path)
	if len(segments) == 2 {
		return true // bare repo homepage
	}
	// Need at least: owner, repo, tree|blob, ref
	return len(segments) >= 4 && (segments[2] == "tree" || segments[2] == "blob")
}

// browseURLSegments splits a URL path into its non-empty segments,
// tolerating a trailing slash (e.g. ".../owner/repo/" from a browser's
// address bar).
func browseURLSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func parseGitHubURL(ref string) (*GitSource, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub URL: %s", ref)
	}
	segments := browseURLSegments(u.Path)
	owner := segments[0]
	repo := segments[1]
	repoURL := fmt.Sprintf("%s://%s/%s/%s.git", u.Scheme, u.Host, owner, repo)
	if len(segments) == 2 {
		// Bare repo homepage: whole repo, default branch.
		return &GitSource{RepoURL: repoURL, Path: "."}, nil
	}
	gitRef := segments[3]
	path := "."
	if len(segments) > 4 {
		path = strings.Join(segments[4:], "/")
	}
	return &GitSource{
		RepoURL: repoURL,
		Ref:     gitRef,
		Path:    path,
	}, nil
}
