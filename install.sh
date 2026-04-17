#!/usr/bin/env bash
# mcp-project-brain installer for Linux and macOS.
# Usage:
#   curl -fsSL https://neusis-ai-org.github.io/project-brain-mcp/install.sh | bash
# Environment variables:
#   VERSION      Pin a specific version (default: latest release).
#   INSTALL_DIR  Install location (default: $HOME/.local/bin).

set -euo pipefail

REPO="Neusis-AI-Org/project-brain-mcp"
BINARY="mcp-project-brain"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux*)  OS="Linux"  ;;
  Darwin*) OS="Darwin" ;;
  *) echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH="x86_64" ;;
  aarch64|arm64) ARCH="arm64"  ;;
  i386|i686)     ARCH="i386"   ;;
  *) echo "Unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "${VERSION:-}" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name":' | sed -E 's/.*"v?([^"]+)".*/\1/')
fi

ARCHIVE="${BINARY}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/v${VERSION}/${ARCHIVE}"

echo "Downloading $BINARY v$VERSION for $OS/$ARCH..."
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL "$URL" -o "$TMPDIR/$ARCHIVE"
tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"

mkdir -p "$INSTALL_DIR"
install -m 755 "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY"

echo "Installed: $INSTALL_DIR/$BINARY"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "Warning: $INSTALL_DIR is not on your PATH."
    echo "Add this line to your shell profile (.bashrc / .zshrc):"
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac
