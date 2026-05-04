#!/bin/bash
set -euo pipefail
# Sandbox entrypoint: symlinks deps, configures MCP, drops to sandbox user.
# Workspace is pre-populated in the container image via seed-and-commit provisioning.

# Symlink read-only dependency mounts into workspace if present.
for dep_dir in /deps/*/; do
    if [ -d "$dep_dir" ]; then
        dep_name=$(basename "$dep_dir")
        [ ! -e "/workspace/$dep_name" ] && ln -sf "$dep_dir" "/workspace/$dep_name"
    fi
done

# Configure MCP if socket is mounted.
[ -S "/run/mcp.sock" ] && export DOCKER_MCP_SOCKET="/run/mcp.sock"

# Drop privileges and wait for exec.
exec su -s /bin/bash sandbox -c "exec sleep infinity"
