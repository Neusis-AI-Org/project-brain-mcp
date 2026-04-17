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

echo
echo "GitHub token setup ----------------------------------------------"
echo

if [ -n "${GITHUB_PERSONAL_ACCESS_TOKEN:-}" ]; then
  echo "GITHUB_PERSONAL_ACCESS_TOKEN is already set in this shell. Skipping prompt."
else
  echo "Create a fine-grained GitHub personal access token with read access to"
  echo "the knowledge base repository you will use:"
  echo "  https://github.com/settings/personal-access-tokens/new"
  echo

  # Pick the user's shell rc file
  case "${SHELL:-}" in
    *zsh)  RC_FILE="$HOME/.zshrc" ;;
    *bash) RC_FILE="$HOME/.bashrc" ;;
    *)     RC_FILE="$HOME/.profile" ;;
  esac

  # Read from /dev/tty so it works even when piped via `curl | bash`
  if [ -r /dev/tty ]; then
    printf "Paste your token now (or press Enter to skip): "
    read -r TOKEN < /dev/tty || TOKEN=""
    if [ -n "$TOKEN" ]; then
      # Guard against duplicate entries
      if ! grep -q "GITHUB_PERSONAL_ACCESS_TOKEN" "$RC_FILE" 2>/dev/null; then
        printf '\nexport GITHUB_PERSONAL_ACCESS_TOKEN="%s"\n' "$TOKEN" >> "$RC_FILE"
        echo "Appended export to $RC_FILE."
        echo "Reload your shell:  source $RC_FILE"
      else
        echo "A GITHUB_PERSONAL_ACCESS_TOKEN entry already exists in $RC_FILE — leaving it alone."
        echo "Edit $RC_FILE manually if you want to replace the value."
      fi
    else
      echo "Skipped. Add this line to $RC_FILE when ready:"
      echo '  export GITHUB_PERSONAL_ACCESS_TOKEN="<token>"'
    fi
  else
    echo "Non-interactive shell detected. Add this line to $RC_FILE:"
    echo '  export GITHUB_PERSONAL_ACCESS_TOKEN="<token>"'
  fi
fi

echo
echo "Reference the env var in your MCP config, e.g. neusiscode.json:"
echo '  "environment": { "GITHUB_PERSONAL_ACCESS_TOKEN": "{env:GITHUB_PERSONAL_ACCESS_TOKEN}" }'
echo
