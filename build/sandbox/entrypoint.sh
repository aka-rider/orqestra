#!/bin/bash
set -euo pipefail


# Configure MCP if socket is mounted.
[ -S "/run/mcp.sock" ] && export

# Wait for exec operations natively.
exec sleep infinity
