#!/bin/bash
#
# Bolt Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/eugenetaranov/bolt/main/install.sh | bash
#

set -e

GITHUB_REPO="eugenetaranov/bolt"
INSTALL_DIR="/usr/local/bin"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info() {
    echo -e "${BLUE}==>${NC} $1"
}

success() {
    echo -e "${GREEN}==>${NC} $1"
}

warn() {
    echo -e "${YELLOW}Warning:${NC} $1"
}

error() {
    echo -e "${RED}Error:${NC} $1"
    exit 1
}

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Darwin*)    echo "darwin" ;;
        Linux*)     echo "linux" ;;
        *)          error "Unsupported operating system: $(uname -s)" ;;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)              error "Unsupported architecture: $(uname -m)" ;;
    esac
}

# Get latest release tag from GitHub
get_latest_release() {
    curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | \
        grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/'
}

# Main installation
main() {
    echo ""
    echo "  ╔══════════════════════════════════════╗"
    echo "  ║         Bolt Installer               ║"
    echo "  ║   System Bootstrapping Tool          ║"
    echo "  ╚══════════════════════════════════════╝"
    echo ""

    local os=$(detect_os)
    local arch=$(detect_arch)

    info "Detected: $os/$arch"

    # Get latest release
    local version=$(get_latest_release)
    if [ -z "$version" ]; then
        error "No releases found. Please check https://github.com/${GITHUB_REPO}/releases"
    fi

    # Download binary
    local binary_name="bolt-${os}-${arch}"
    local download_url="https://github.com/${GITHUB_REPO}/releases/download/${version}/${binary_name}"

    info "Downloading ${binary_name} ${version}..."

    local tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT

    if ! curl -fsSL -o "${tmp_dir}/bolt" "$download_url"; then
        error "Failed to download from ${download_url}"
    fi

    chmod +x "${tmp_dir}/bolt"

    # Install binary
    info "Installing to $INSTALL_DIR..."

    if [ -w "$INSTALL_DIR" ]; then
        mv "$tmp_dir/bolt" "$INSTALL_DIR/bolt"
    else
        sudo mv "$tmp_dir/bolt" "$INSTALL_DIR/bolt"
    fi

    # Verify installation
    if command -v bolt &> /dev/null; then
        success "Bolt ${version} installed successfully!"
        echo ""
        bolt --version
        echo ""
        echo "Get started:"
        echo "  bolt --help              Show help"
        echo "  bolt modules             List available modules"
        echo "  bolt run playbook.yaml   Run a playbook"
        echo ""
        echo "Documentation: https://github.com/${GITHUB_REPO}/tree/main/docs"
    else
        warn "Bolt was installed to $INSTALL_DIR but is not in your PATH"
        echo "Add this to your shell profile:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    fi
}

main "$@"
