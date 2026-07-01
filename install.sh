#!/bin/sh
set -e

VERSION="0.1.0"
REPO="foldwork-dev/mcp-benchmark"
BINARY="mcp-benchmark"

# Detect OS and arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case $ARCH in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Build download URL
URL="https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}-${OS}-${ARCH}"

echo "Downloading mcp-benchmark v${VERSION} for ${OS}/${ARCH}..."
curl -fsSL "$URL" -o /tmp/mcp-benchmark
chmod +x /tmp/mcp-benchmark

# Install
INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  echo "Installing to $INSTALL_DIR requires sudo..."
  sudo mv /tmp/mcp-benchmark "$INSTALL_DIR/$BINARY"
else
  mv /tmp/mcp-benchmark "$INSTALL_DIR/$BINARY"
fi

echo "✓ mcp-benchmark installed to $INSTALL_DIR/$BINARY"
echo ""
echo "Run it on your project:"
echo "  mcp-benchmark ./your-project"
echo ""
echo "Want the full MCP daemon? → https://github.com/foldwork-dev/mcp-injector"
