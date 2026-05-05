FROM oven/bun:1-slim

# Runtime dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    tini \
    curl \
    ca-certificates \
    unzip \
    && rm -rf /var/lib/apt/lists/*

# Assume MacOS defaults
ARG UID=501
ARG GID=20

# Create non-root sandbox user and root directories.
# Note: Since the host GID might naturally collide with existing groups in Debian (like GID 20: dialout),
# we allow fallback creation if groupadd fails.
RUN groupadd -g ${GID} sandbox || groupadd sandbox && \
    useradd -u ${UID} -g sandbox -d /workspace -s /bin/bash sandbox

# Entrypoint.
COPY build/sandbox/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Switch to sandbox user for all user-level setup
USER sandbox

# Install Claude Code globally using bun
RUN bun install -g @anthropic-ai/claude-code

# Bake project-level Claude Code config into workspace.
# Contains hooks (BEL on Stop/Notification) for Orqestra attention detection.
# Permissions are handled by --dangerously-skip-permissions at launch time.
# This is the ONLY config file we write. ~/.claude.json is never touched.
# For native Anthropic auth, host ~/.claude/ is bind-mounted at runtime.
RUN mkdir -p /workspace/.claude
COPY --chown=sandbox:sandbox build/sandbox/claude-settings.json /workspace/.claude/settings.json

WORKDIR /workspace

ENV DOCKER_MCP_SOCKET="/run/mcp.sock"
# Tini as PID 1. Entrypoint runs natively as sandbox user.
ENTRYPOINT ["tini", "--", "sleep", "infinity"]

LABEL sandbox=true
