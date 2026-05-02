#!/bin/bash
docker run -d --name copilot-proxy -p 4141:4141 \
  -v $(pwd)/copilot-data:/root/.local/share/copilot-api \
  copilot-api
echo "copilot-proxy started on :4141"
