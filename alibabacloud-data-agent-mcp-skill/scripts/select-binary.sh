#!/bin/bash
# Select the correct data-agent-mcp-server binary for the current platform.
# Used by Claude Code mcpServers configuration.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="${SCRIPT_DIR}/../assets/server/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)        ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

BINARY="${BIN_DIR}/data-agent-mcp-server-${OS}-${ARCH}"

if [ -f "$BINARY" ]; then
    exec "$BINARY" "$@"
fi

# Fallback: try plain binary name (local dev build)
FALLBACK="${BIN_DIR}/data-agent-mcp-server"
if [ -f "$FALLBACK" ]; then
    exec "$FALLBACK" "$@"
fi

# Auto-build from source if Go is available
SERVER_DIR="${SCRIPT_DIR}/../assets/server"
if command -v go >/dev/null 2>&1 && [ -f "${SERVER_DIR}/go.mod" ]; then
    echo "Pre-compiled binary not found for ${OS}/${ARCH}, building from source..." >&2
    (cd "$SERVER_DIR" && go build -trimpath -ldflags "-s -w" -o "${BIN_DIR}/data-agent-mcp-server" .) || {
        echo "Build failed. Install Go 1.23+ from https://go.dev/dl/" >&2
        exit 1
    }
    exec "${BIN_DIR}/data-agent-mcp-server" "$@"
fi

echo "No data-agent-mcp-server binary found for ${OS}/${ARCH}" >&2
echo "  Pre-compiled binaries are provided for Linux amd64/arm64 only." >&2
echo "  Install Go 1.23+ (https://go.dev/dl/) to build from source automatically." >&2
exit 1
