# Releasing Bolt

This document describes how to create a new release of Bolt.

## Prerequisites

### Local Development

Install GoReleaser for local testing:

```bash
# macOS
brew install goreleaser

# or via go install
go install github.com/goreleaser/goreleaser/v2@latest
```

### GitHub Setup

1. **Create a Homebrew Tap Repository**

   Create a new repository named `homebrew-tap` under your GitHub account:
   - Repository: `eugenetaranov/homebrew-tap`
   - This will host the Homebrew formula

2. **Create a Personal Access Token**

   Create a GitHub Personal Access Token (PAT) with `repo` scope:
   - Go to GitHub Settings → Developer settings → Personal access tokens
   - Create a token with `repo` scope (for pushing to the tap repository)

3. **Add Repository Secrets**

   In the `bolt` repository, add the following secret:
   - `HOMEBREW_TAP_TOKEN`: Your GitHub PAT with repo access

## Creating a Release

### 1. Test Locally (Optional)

```bash
# Check GoReleaser configuration
make release-check

# Test build without publishing
make release-dry-run

# Create a snapshot release
make release-snapshot
```

### 2. Create Release Tag

```bash
# Create and push a new tag
make release TAG=v1.0.0
```

This will:
1. Create an annotated git tag
2. Push the tag to GitHub
3. Trigger the GitHub Actions release workflow

### 3. GitHub Actions

The release workflow (`.github/workflows/release.yaml`) will automatically:
1. Run tests
2. Build binaries for all platforms (linux/darwin, amd64/arm64)
3. Create archives with checksums
4. Create a GitHub release with changelog
5. Update the Homebrew tap formula

## Release Artifacts

Each release includes:

| Artifact | Description |
|----------|-------------|
| `bolt_VERSION_darwin_amd64.tar.gz` | macOS Intel |
| `bolt_VERSION_darwin_arm64.tar.gz` | macOS Apple Silicon |
| `bolt_VERSION_linux_amd64.tar.gz` | Linux x86_64 |
| `bolt_VERSION_linux_arm64.tar.gz` | Linux ARM64 |
| `checksums.txt` | SHA256 checksums |

## Homebrew Installation

After release, users can install via Homebrew:

```bash
# Add the tap (first time only)
brew tap eugenetaranov/tap

# Install bolt
brew install bolt

# Or in one command
brew install eugenetaranov/tap/bolt
```

## Versioning

Follow [Semantic Versioning](https://semver.org/):

- `MAJOR.MINOR.PATCH` (e.g., `v1.2.3`)
- MAJOR: Breaking changes
- MINOR: New features (backward compatible)
- PATCH: Bug fixes (backward compatible)

## Troubleshooting

### GoReleaser Validation Errors

```bash
goreleaser check
```

### Homebrew Tap Not Updating

Ensure `HOMEBREW_TAP_TOKEN` secret is set and has `repo` scope.

### Build Failures

Check the GitHub Actions logs for detailed error messages.
