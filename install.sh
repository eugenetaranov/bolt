#!/bin/bash
#
# Bolt Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/eugenetaranov/bolt/main/install.sh | bash
#

set -e

REPO="https://github.com/eugenetaranov/bolt.git"
INSTALL_DIR="/usr/local/bin"
BOLT_DIR="${BOLT_DIR:-$HOME/.bolt}"

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

# Check for required commands
check_requirements() {
    local missing=()

    if ! command -v git &> /dev/null; then
        missing+=("git")
    fi

    if ! command -v go &> /dev/null; then
        # Check common Go installation paths
        if [ -x "/usr/local/go/bin/go" ]; then
            export PATH="/usr/local/go/bin:$PATH"
        elif [ -x "/opt/homebrew/bin/go" ]; then
            export PATH="/opt/homebrew/bin:$PATH"
        elif [ -x "$HOME/go/bin/go" ]; then
            export PATH="$HOME/go/bin:$PATH"
        else
            missing+=("go")
        fi
    fi

    if [ ${#missing[@]} -ne 0 ]; then
        error "Missing required tools: ${missing[*]}\n\nPlease install them and try again.\n\nOn macOS:  brew install ${missing[*]}\nOn Ubuntu: sudo apt install ${missing[*]}"
    fi
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

    check_requirements

    info "Go version: $(go version | awk '{print $3}')"

    # Create temp directory
    local tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT

    info "Cloning repository..."
    git clone --depth 1 --quiet "$REPO" "$tmp_dir/bolt"

    info "Building bolt..."
    cd "$tmp_dir/bolt"

    # Build with version info
    local version=$(git describe --tags --always 2>/dev/null || echo "dev")
    local commit=$(git rev-parse --short HEAD 2>/dev/null || echo "none")
    local date=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    go build -ldflags "-X main.version=$version -X main.commit=$commit -X main.date=$date" \
        -o bolt ./cmd/bolt

    # Install binary
    info "Installing to $INSTALL_DIR..."

    if [ -w "$INSTALL_DIR" ]; then
        mv bolt "$INSTALL_DIR/bolt"
    else
        sudo mv bolt "$INSTALL_DIR/bolt"
    fi

    # Verify installation
    if command -v bolt &> /dev/null; then
        success "Bolt installed successfully!"
        echo ""
        bolt --version
        echo ""
        echo "Get started:"
        echo "  bolt --help              Show help"
        echo "  bolt modules             List available modules"
        echo "  bolt run playbook.yaml   Run a playbook"
        echo ""
        echo "Documentation: https://github.com/eugenetaranov/bolt/tree/main/docs"
    else
        warn "Bolt was installed to $INSTALL_DIR but is not in your PATH"
        echo "Add this to your shell profile:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    fi
}

main "$@"
