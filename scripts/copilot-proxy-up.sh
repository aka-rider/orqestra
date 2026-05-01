#!/bin/bash
docker run -d --name copilot-proxy -p 4141:4141 \
  -v $HOME/.config:/root/.config \
  node:lts npx copilot-api@latest start --claude-code
echo "copilot-proxy started on :4141"
