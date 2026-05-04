# Work Package: overlayfs-sandbox

| Field | Value |
|-------|-------|
| **ID** | `overlayfs-sandbox` |
| **Wave** | 2 |
| **depends_on** | `docker-sdk-migration` |
| **Files** | `internal/sandbox/docker.go`, `internal/sandbox/extract.go`, `build/sandbox/Dockerfile`, `build/sandbox/entrypoint.sh` |

## Goal

Replace the BTRFS-based copy-on-write sandbox with native Docker OverlayFS. Eliminates the `--privileged` requirement, the loopback btrfs filesystem, and the fragile btrfs send/receive diff pipeline. Uses Docker's native overlay storage driver — every container already runs on OverlayFS by default.

## Steps

### Container Rebuild

1. Rewrite `build/sandbox/Dockerfile`:
   - Remove `btrfs-progs` from installed packages.
   - Keep: `rsync`, `git`, `tini`, `curl`, `ca-certificates`, `unzip`, bun, claude-code.
   - The workspace is populated by the entrypoint copying from `/workspace-src` (bind mount) to `/workspace` (writable container layer). No btrfs subvolumes.
   - Keep non-root `sandbox` user.
   - No `--privileged` needed.

2. Rewrite `build/sandbox/entrypoint.sh`:
   - Remove ALL btrfs logic (truncate, mkfs.btrfs, mount, subvolume create/snapshot, property set).
   - Remove `rsync` and `chown` from here (moved to seeding phase in provisioning).
   - New flow:
     1. Symlink read-only dependency mounts (keep existing logic).
     2. Configure MCP socket if present (keep existing logic).
     3. `exec su -s /bin/bash sandbox -c "exec sleep infinity"` (keep existing logic).

### Provisioning Changes

1. Update `DockerSandbox.Provision()` to use a seed-and-commit model:
   - Spin up a temporary seed container using the base image, bind mounting `/workspace-src`.
   - Execute `rsync -a /workspace-src/ /workspace/` and `chown -R sandbox:sandbox /workspace` inside the seed container.
   - Run `cli.ContainerCommit(ctx, seedContainerID, types.ContainerCommitOptions{Reference: ephemeralImageTag})` where the tag incorporates the session ID (e.g., `orqestra-ws-snapshot-<id>`).
   - Remove the seed container.
   - Start the actual runtime LLM sandbox using the newly created `ephemeralImageTag`.

2. Update `buildCreateArgs()` / SDK equivalent:
   - Remove `--privileged`.
   - Remove `--mount type=volume,source=<btrfs-volume>,target=/btrfs-pool`.
   - Remove `--mount type=volume,source=<ws-volume>,target=/workspace`.
   - Remove bind mount of `/workspace-src` for the runtime container (it's baked into the ephemeral image).
   - Use the `ephemeralImageTag` as the container image.

3. Update Sandbox Destruction (`internal/sandbox/reaper.go` & `docker.go`):
   - When destroying the container, explicitly issue `cli.ImageRemove(ctx, ephemeralImageTag, image.RemoveOptions{})` to prevent dangling snapshots from filling the disk.
   - Update background reaper to identify and clean up orphaned images matching `orqestra-ws-snapshot-*`.

### Change Extraction via OverlayFS Diff

1. Rewrite `ExtractChanges()` in `internal/sandbox/extract.go`:
   - Replace `parseBtrfsDump()` and all btrfs send/receive logic.
   - New approach — use Docker SDK's `ContainerDiff(ctx, containerID)`:
     - Returns `[]container.FilesystemChange` with `Path` and `Kind` (Added=0, Modified=1, Deleted=2).
     - Map `Kind` to `FileOp` (FileAdded, FileModified, FileDeleted).
     - Filter to only `/workspace/` paths (ignore system files, `/tmp`, etc.).
     - Strip `/workspace/` prefix from paths.
   - Enrich with size/executable info via `ContainerStatPath()` (already migrated in docker-sdk-migration).
   - This is ~30 lines replacing ~100 lines of btrfs parsing.

2. Remove `parseBtrfsDump()` and all BTRFS-related parsing functions from `extract.go`.

3. Update `CopyOut()`:
   - Already uses SDK `CopyFromContainer` after docker-sdk-migration.
   - Ensure path is `/workspace/<relative>` when extracting.

### Tests

1. Update `internal/sandbox/docker_test.go`:
   - Remove any tests that assert btrfs behavior.
   - Add test: `ExtractChanges` detects a file added inside container.
   - Add test: `ExtractChanges` detects a file modified inside container.
   - Add test: `ExtractChanges` detects a file deleted inside container.
   - Add test: changes outside `/workspace/` are filtered out.
   - Add test: provisioning works without `--privileged` (container starts, workspace is populated).

2. Remove `internal/sandbox/extract.go` btrfs parsing tests or move to a `_legacy` file if needed for reference.

## Acceptance

- `go test ./internal/sandbox/ -tags integration` passes.
- `docker build -t orqestra-sandbox:latest -f build/sandbox/Dockerfile .` succeeds.
- Container starts without `--privileged` flag.
- No `btrfs` references in `internal/sandbox/` (except possibly a legacy comment explaining the migration).
- `ExtractChanges()` correctly detects added/modified/deleted files via Docker SDK `ContainerDiff`.
- `Dockerfile` no longer installs `btrfs-progs`.
- `entrypoint.sh` is <20 lines with no btrfs logic.
- Files touched: `internal/sandbox/docker.go`, `internal/sandbox/extract.go`, `build/sandbox/Dockerfile`, `build/sandbox/entrypoint.sh`, `internal/sandbox/docker_test.go`.
