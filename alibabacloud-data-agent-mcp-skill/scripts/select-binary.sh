#!/bin/bash
# Locate and exec the data-agent-mcp-server binary for the current platform.
# Used by agent runtime mcpServers configurations (Claude Code, Qoder, ...).
#
# Deployment model: this skill ships WITHOUT server source code or binaries.
# The deployer builds the server (or downloads a release binary) and places
# it in one of the locations below. See references/INSTALLATION.md.
#
# Lookup order:
#   1. $DATA_AGENT_SERVER_BIN                — explicit path (standalone deployments)
#   2. <skill>/assets/bin/                   — binary placed inside the skill
#   3. <repo>/server/bin/                    — monorepo development build
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_BIN_DIR="${SCRIPT_DIR}/../assets/bin"
REPO_BIN_DIR="${SCRIPT_DIR}/../../server/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)        ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

# 1. Explicit override — points at a deployer-managed binary anywhere on disk.
if [ -n "${DATA_AGENT_SERVER_BIN:-}" ]; then
    if [ -x "$DATA_AGENT_SERVER_BIN" ]; then
        exec "$DATA_AGENT_SERVER_BIN" "$@"
    fi
    echo "DATA_AGENT_SERVER_BIN is set but not executable: $DATA_AGENT_SERVER_BIN" >&2
    exit 1
fi

# 2/3. Platform-suffixed binary first, then the plain name, in each location.
for dir in "$SKILL_BIN_DIR" "$REPO_BIN_DIR"; do
    for name in "data-agent-mcp-server-${OS}-${ARCH}" "data-agent-mcp-server"; do
        if [ -x "${dir}/${name}" ]; then
            exec "${dir}/${name}" "$@"
        fi
    done
done

echo "No data-agent-mcp-server binary found for ${OS}/${ARCH}." >&2
echo "Build it from the server project and place it in one of:" >&2
echo "  - \$DATA_AGENT_SERVER_BIN (explicit path)" >&2
echo "  - ${SKILL_BIN_DIR}/" >&2
echo "  - ${REPO_BIN_DIR}/ (monorepo development)" >&2
echo "Build: cd <repo>/server && make build   (Go 1.23+, see references/INSTALLATION.md)" >&2
exit 1
