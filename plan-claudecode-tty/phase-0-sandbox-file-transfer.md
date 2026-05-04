# Work Package: sandbox-file-transfer

| Field | Value |
|-------|-------|
| **ID** | `sandbox-file-transfer` |
| **Wave** | 2 |
| **depends_on** | `docker-sdk-migration` |
| **Files** | `internal/sandbox/transfer.go`, `internal/sandbox/transfer_test.go`, `internal/sandbox/docker.go` (minor additions) |

## Goal

Implement robust file transfer in and out of sandboxed containers using the Docker Go SDK. Covers: staging the read-only repo copy into containers, staging input artifacts (input.md, system-prompt.md), and extracting vetted output files.

## Steps

### Copy-In: Host → Container

1. Create `internal/sandbox/transfer.go` with:
   - `CopyToContainer(ctx context.Context, cli *client.Client, containerID, containerPath string, content io.Reader) error`:
     - Wraps content in a tar archive (single file at the target path).
     - Calls `cli.CopyToContainer(ctx, containerID, "/", tarReader, container.CopyToContainerOptions{})`.
     - Used for staging `input.md`, `system-prompt.md`, and any per-agent config files.
   - `CopyDirToContainer(ctx context.Context, cli *client.Client, containerID, containerPath, hostDirPath string) error`:
     - Creates a tar archive from a host directory.
     - Streams to `cli.CopyToContainer`.
     - Used if we need to stage additional directories (e.g., `.orqestra/` scaffolding).
   - Both functions set proper ownership (`sandbox:sandbox` UID/GID 1000:1000 in tar headers).

2. Add `StageInputs(ctx context.Context, inputMD, systemPromptMD string) error` to `DockerSandbox`:
   - Creates `/workspace/.orqestra/` directory inside container.
   - Copies `input.md` content to `/workspace/.orqestra/input.md`.
   - Copies `system-prompt.md` content to `/workspace/.orqestra/system-prompt.md`.
   - Sets ownership to sandbox user.
   - This is what `Prepare()` in the PTY runner will call after provisioning.

### Copy-Out: Container → Host

1. Add to `transfer.go`:
   - `CopyFromContainer(ctx context.Context, cli *client.Client, containerID, containerPath string) ([]byte, error)`:
     - Calls `cli.CopyFromContainer(ctx, containerID, containerPath)`.
     - Extracts single file from tar response.
     - Returns raw bytes.
   - `CopyFileFromContainer(ctx context.Context, cli *client.Client, containerID, containerPath, hostPath string) error`:
     - Calls `CopyFromContainer`, writes bytes to `hostPath`.
     - Creates parent directories on host.
     - Sets file permissions based on tar header mode.

2. Rewrite `DockerSandbox.CopyOut()` to use `CopyFileFromContainer` internally (replacing the `docker exec cat` approach).

3. Add `ExtractArtifact(ctx context.Context) ([]byte, error)` to `DockerSandbox`:
   - Reads `/workspace/.orqestra/output.md` from container.
   - Returns raw bytes for the artifact parser.
   - Returns clear error if output.md doesn't exist (agent didn't write it).

### Verified File Extraction Pipeline

1. Update the extract+verify+stage pipeline in `SandboxedCLIRunner.applyChanges()` (or the new PTY runner equivalent):
   - After `ExtractChanges()` returns the diff list:
     1. For each added/modified file: `CopyFileFromContainer` to staging directory.
     2. Verify staged files (existing `Verifier.Verify()` — path traversal, size, executables).
     3. Atomic apply: move from staging to host repo (existing logic).
   - For deleted files: `os.Remove` on host (existing logic).
   - All file I/O through the SDK — no `docker exec cat`.

### Tests

1. Create `internal/sandbox/transfer_test.go` (build tag `//go:build integration`):
   - Test `CopyToContainer`: write known content, then `CopyFromContainer` and assert bytes match.
   - Test `StageInputs`: provision container, stage inputs, exec `cat /workspace/.orqestra/input.md` inside and verify content.
   - Test `ExtractArtifact`: write output.md inside container via exec, then ExtractArtifact, assert content.
   - Test `ExtractArtifact` missing file: returns clear error, not panic.
   - Test ownership: staged files are owned by sandbox user (UID 1000).
   - Test large file: stage a 10MB file, extract it, verify content hash.

## Acceptance

- `go test ./internal/sandbox/ -run TestTransfer -tags integration` passes.
- `go vet ./internal/sandbox/` clean.
- Zero `docker exec cat` patterns remain for file extraction.
- `StageInputs` successfully places `input.md` and `system-prompt.md` inside containers.
- `ExtractArtifact` retrieves output.md or returns descriptive error.
- All file transfer uses Docker SDK (`CopyToContainer`/`CopyFromContainer`), not CLI shell-outs.
- Tar archives set correct UID/GID ownership for the sandbox user.
- Files touched: `internal/sandbox/transfer.go`, `internal/sandbox/transfer_test.go`, `internal/sandbox/docker.go` (CopyOut refactor).
