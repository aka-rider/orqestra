#!/bin/bash
docker run --rm --name copilot-proxy -p 4141:4141 \
  -v $(pwd)/copilot-data:/root/.local/share/copilot-api \
  oven/bun:latest bunx copilot-api@latest start
echo "copilot-proxy started on :4141"
