#!/bin/bash
set -euo pipefail

# Generic Agent Sandbox Entrypoint
#
# Uses BTRFS copy-on-write snapshots for efficient workspace provisioning.
# This image is repo-agnostic — any project is mounted at /workspace-src at runtime.
# Every sandbox is fully ephemeral — no state persists between runs.
#
# Volume layout:
#   /workspace-src  — ANY repo bind-mount (read-only, provided at docker create)
#   /btrfs-pool     — ephemeral volume for btrfs image (destroyed with sandbox)
#   /mnt/btrfs      — btrfs mount point
#   /workspace      — writable CoW snapshot
#   /run/mcp.sock   — Docker MCP gateway socket (if mounted)
#
# Flow:
#   1. Create fresh btrfs filesystem in ephemeral volume
#   2. Sync source into a subvolume
#   3. Snapshot → writable workspace (instant, O(1) CoW)
#   4. Bind-mount snapshot as /workspace
#   5. Drop to sandbox user, sleep (ready for docker exec)

BTRFS_IMG="/btrfs-pool/sandbox.img"
BTRFS_MNT="/mnt/btrfs"
SRC_SUBVOL="$BTRFS_MNT/source"
WS_SNAPSHOT="$BTRFS_MNT/workspace"
BTRFS_SIZE="${BTRFS_SIZE:-20G}"

# --- Step 1: Create fresh btrfs filesystem ---
echo "[sandbox] Creating btrfs filesystem (${BTRFS_SIZE} sparse)..."
truncate -s "$BTRFS_SIZE" "$BTRFS_IMG"
mkfs.btrfs -f -q "$BTRFS_IMG"

mkdir -p "$BTRFS_MNT"
mount -o loop,compress=zstd "$BTRFS_IMG" "$BTRFS_MNT"

# --- Step 2: Create source subvolume from mounted repo ---
echo "[sandbox] Syncing source into btrfs subvolume..."
btrfs subvolume create "$SRC_SUBVOL" > /dev/null
rsync -a /workspace-src/ "$SRC_SUBVOL/"

# Mark source as read-only (required for btrfs send parent).
btrfs property set "$SRC_SUBVOL" ro true

# --- Step 3: Snapshot source → workspace (INSTANT, CoW) ---
echo "[sandbox] Creating CoW workspace snapshot..."
btrfs subvolume snapshot "$SRC_SUBVOL" "$WS_SNAPSHOT" > /dev/null

# --- Step 4: Bind-mount snapshot as /workspace ---
mount --bind "$WS_SNAPSHOT" /workspace
chown sandbox:sandbox /workspace

# Symlink read-only dependency mounts into workspace if present.
for dep_dir in /deps/*/; do
    if [ -d "$dep_dir" ]; then
        dep_name=$(basename "$dep_dir")
        if [ ! -e "/workspace/$dep_name" ]; then
            ln -sf "$dep_dir" "/workspace/$dep_name"
        fi
    fi
done

# Configure MCP if socket is mounted.
if [ -S "/run/mcp.sock" ]; then
    export DOCKER_MCP_SOCKET="/run/mcp.sock"
    echo "[sandbox] MCP gateway available at /run/mcp.sock"
fi

echo "[sandbox] Ready."

# --- Step 5: Drop privileges and wait for exec ---
exec su -s /bin/bash sandbox -c "exec sleep infinity"
