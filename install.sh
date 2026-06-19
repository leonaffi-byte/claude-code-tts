#!/bin/bash
set -e

# Claude Code TTS Plugin Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/ybouhjira/claude-code-tts/main/install.sh | bash
#
# SUPPLY-CHAIN NOTE: piping this script straight into `bash` runs whatever the
# remote install.sh contains, and the build step compiles and can run arbitrary
# repository code. If you want to inspect before running, prefer the
# download-inspect-then-run path:
#
#   curl -fsSL https://raw.githubusercontent.com/ybouhjira/claude-code-tts/main/install.sh -o install.sh
#   less install.sh        # review it
#   bash install.sh        # then run it
#
# By default this installer tracks the main branch. For a reproducible,
# non-HEAD-of-main install, pin to a published release tag or commit by setting
# VERSION, e.g. VERSION=v1.0.0 curl ... | bash
#
# NOTE: do not default VERSION to a tag that does not exist in the repo — the
# clone/update logic below silently falls back to main when the ref is missing,
# which would turn an apparent "pin" into an unannounced HEAD-of-main checkout.

REPO="ybouhjira/claude-code-tts"
INSTALL_DIR="$HOME/.claude/plugins/claude-code-tts"
# Ref to check out: a branch, tag, or commit. Defaults to main (the only branch
# guaranteed to exist). Override for a reproducible pin:
#   VERSION=v1.0.0 curl ... | bash
VERSION="${VERSION:-main}"

echo "Installing Claude Code TTS Plugin..."
echo ""

# Check for Go
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed."
    echo "Please install Go 1.23+ from https://golang.org/dl/"
    exit 1
fi

# Check Go version
GO_VERSION=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | sed 's/go//')
REQUIRED_VERSION="1.23"
if [ "$(printf '%s\n' "$REQUIRED_VERSION" "$GO_VERSION" | sort -V | head -n1)" != "$REQUIRED_VERSION" ]; then
    echo "Error: Go $REQUIRED_VERSION+ is required (found $GO_VERSION)"
    exit 1
fi

# Check for jq (required by the auto-speak Stop hook)
if ! command -v jq &> /dev/null; then
    echo "Warning: jq is not installed."
    echo "The automatic Stop hook (auto-speak) requires jq to parse and build"
    echo "its JSON payload. Install it to enable automatic TTS:"
    echo "  macOS:        brew install jq"
    echo "  Debian/Ubuntu: sudo apt install jq"
    echo "  Fedora:        sudo dnf install jq"
    echo ""
fi

# Check for OpenAI API key
if [ -z "$OPENAI_API_KEY" ]; then
    echo "Warning: OPENAI_API_KEY is not set."
    echo "Set it before using the plugin:"
    echo "  export OPENAI_API_KEY=\"sk-...\""
    echo ""
fi

# Clone or update repository, pinned to $VERSION (a tag, or "main" to track HEAD).
REPO_URL="https://github.com/$REPO.git"
if [ -d "$INSTALL_DIR" ]; then
    echo "Updating existing installation (pinned to $VERSION)..."
    cd "$INSTALL_DIR"
    # Robust, deterministic sync: never let a dirty tree or divergent history
    # abort the installer under `set -e` (as a plain `git pull` would).
    git fetch --quiet --tags origin
    git checkout --quiet -f "$VERSION" 2>/dev/null \
        || git checkout --quiet -B main origin/main
    # Discard any local modifications so the checkout is exactly the pinned ref.
    git reset --hard --quiet "$VERSION" 2>/dev/null \
        || git reset --hard --quiet origin/main
else
    echo "Cloning repository (pinned to $VERSION)..."
    mkdir -p "$(dirname "$INSTALL_DIR")"
    # Try a shallow clone of the pinned tag first; fall back to main.
    if ! git clone --quiet --depth 1 --branch "$VERSION" "$REPO_URL" "$INSTALL_DIR" 2>/dev/null; then
        echo "Tag $VERSION not found; cloning main instead."
        git clone --quiet --depth 1 "$REPO_URL" "$INSTALL_DIR"
    fi
    cd "$INSTALL_DIR"
fi

# Build
echo "Building..."
make build --quiet

# Create plugin structure
mkdir -p "$INSTALL_DIR/bin"
mkdir -p "$INSTALL_DIR/.claude"
cp bin/tts-server "$INSTALL_DIR/bin/"

echo ""
echo "Installation complete!"
echo ""
echo "Next steps:"
echo "  1. Ensure OPENAI_API_KEY is set in your environment"
echo "  2. Add the MCP server to Claude Code:"
echo "     claude mcp add tts $INSTALL_DIR/bin/tts-server"
echo ""
echo "Or add to ~/.config/claude-code/claude_desktop_config.json:"
printf '  {
    "mcpServers": {
      "tts": {
        "command": "%s/bin/tts-server",
        "env": {
          "OPENAI_API_KEY": "your-key-here"
        }
      }
    }
  }\n' "$INSTALL_DIR"
