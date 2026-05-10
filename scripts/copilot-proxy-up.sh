#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROXY_DIR="$SCRIPT_DIR/copilot-proxy"

# Stop any existing instance
docker rm -f copilot-proxy 2>/dev/null || true

docker run --rm --name copilot-proxy -p 4141:4141 \
  -v "$(pwd)/copilot-data:/root/.local/share/copilot-api" \
  -v "$PROXY_DIR/preload.js:/app/preload.js:ro" \
  -v "$PROXY_DIR/bunfig.toml:/app/bunfig.toml:ro" \
  -w /app \
  oven/bun:latest bunx copilot-api@latest start
echo "copilot-proxy started on :4141"
