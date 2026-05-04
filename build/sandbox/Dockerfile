# Agent Sandbox — Generic Execution Container
#
# Repo-agnostic isolated execution environment for Claude agent sessions.
# ANY repo is bind-mounted at /workspace-src (read-only) at runtime.
# Workspace is populated via seed-and-commit during provisioning (OverlayFS).
# Docker MCP servers are accessible via the mounted MCP gateway socket.
#
# Build:  docker build -t orqestra-sandbox:latest -f build/sandbox/Dockerfile .
# Usage:  Managed by the sandbox package — not meant for direct use.

FROM debian:bookworm-slim

# Runtime dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    rsync \
    git \
    tini \
    curl \
    ca-certificates \
    unzip \
    && rm -rf /var/lib/apt/lists/*

# Install bun (JavaScript runtime — runs Claude Code).
RUN curl -fsSL https://bun.sh/install | bash
ENV PATH="/root/.bun/bin:${PATH}"

# Install Claude Code globally via bun.
RUN bun install -g @anthropic-ai/claude-code

# Create non-root sandbox user.
RUN groupadd -g 1000 sandbox && \
    useradd -u 1000 -g sandbox -m -s /bin/bash sandbox

# Copy bun + claude to sandbox user.
RUN cp -a /root/.bun /home/sandbox/.bun && \
    chown -R sandbox:sandbox /home/sandbox/.bun
ENV PATH="/home/sandbox/.bun/bin:${PATH}"

# Mount points (populated at runtime by docker create).
RUN mkdir -p /workspace /workspace-src /deps /run && \
    chown sandbox:sandbox /workspace

# Entrypoint.
COPY build/sandbox/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace

# Tini as PID 1. Entrypoint drops to sandbox user.
ENTRYPOINT ["tini", "--", "/entrypoint.sh"]

LABEL sandbox=true
