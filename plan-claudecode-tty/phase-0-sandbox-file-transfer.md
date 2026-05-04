# Work Package: sandbox-file-transfer

| Field | Value |
|-------|-------|
| **ID** | `sandbox-file-transfer` |
| **Wave** | 2 |
| **depends_on** | `docker-sdk-migration` |
| **Files** | `internal/sandbox/transfer.go`, `internal/sandbox/transfer_test.go`, `internal/sandbox/docker.go` (minor additions) |

## Goal

Implement robust file transfer in and out of sandboxed containers using the Docker Go SDK. Covers: staging the read-only repo copy into containers, staging input artifacts (input.md, system-prompt.md), and extracting vetted output files. Incorporates strict session isolation, streaming I/O, and path sanitization.

## Steps

### Copy-In: Host → Container

1. Create `internal/sandbox/transfer.go` with:
   - `CopyToContainer(ctx context.Context, cli *client.Client, containerID, containerPath string, content io.Reader) error`:
     - Wraps content in a tar archive (single file at the target path).
     - Explicitly sets standard file modes (`0644` for files, `0755` for directories).
     - Calls `cli.CopyToContainer(ctx, containerID, "/", tarReader, container.CopyToContainerOptions{})`.
     - Used for staging `input.md`, `system-prompt.md`, and any per-agent config files.
     - Preserves the host's UID/GID naturally, expecting the sandbox image to have been committed to mirror the host user (running as a non-root user).
   - `CopyDirToContainer(ctx context.Context, cli *client.Client, containerID, containerPath, hostDirPath string) error`:
     - Creates a tar archive from a host directory. Resolves or ignores symlinks properly to prevent host escapes.
     - Streams to `cli.CopyToContainer`.
     - Handles deferred cleanup of any temporary host scaffolding.

2. Add `StageInputs(ctx context.Context, sess sandbox.Session, inputMD, systemPromptMD string) error` to `DockerSandbox`:
   - `sandbox.Session` struct encapsulates session name, start timestamp, and session dir.
   - Creates `/workspace/.orqestra/<sess.Name>/` directory inside container.
   - Copies `input.md` content to `/workspace/.orqestra/<sess.Name>/input.md`.
   - Copies `system-prompt.md` content to `/workspace/.orqestra/<sess.Name>/system-prompt.md`.
   - This is what `Prepare()` in the PTY runner will call after provisioning.

### Copy-Out: Container → Host

1. Add to `transfer.go`:
   - `CopyFromContainer(ctx context.Context, cli *client.Client, containerID, containerPath string) (io.ReadCloser, error)`:
     - Calls `cli.CopyFromContainer(ctx, containerID, containerPath)`.
     - Returns the tar stream (must be closed by caller) to avoid pulling massive files into memory.
   - `CopyFileFromContainer(ctx context.Context, cli *client.Client, containerID, containerPath, hostPath string) error`:
     - Calls `CopyFromContainer`.
     - Extracts single file from the returned tar stream directly to disk.
     - **Zip Slip Protection:** Sanitizes paths within the tar header (`filepath.Clean`), preventing traversal escapes.
     - Creates parent directories on host securely using `os.MkdirAll`.
     - Sets file permissions based on tar header mode.

2. Rewrite `DockerSandbox.CopyOut()` to use `CopyFileFromContainer` internally (replacing the `docker exec cat` approach).

3. Add `ExtractArtifact(ctx context.Context, sess sandbox.Session) ([]byte, error)` to `DockerSandbox`:
   - Reads `/workspace/.orqestra/<sess.Name>/output.md` from container.
   - Caps token-limit/size defensively, returning raw bytes for the artifact parser.
   - Returns a detailed error if `output.md` doesn't exist, summarizing actual directory contents to aid debugging.

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
   - Test `CopyToContainer`: write known content, then `CopyFromContainer` and assert stream matches.
   - Test `StageInputs`: provision container, stage inputs (with session Name), exec `cat /workspace/.orqestra/<sessionName>/input.md` inside and verify content.
   - Test `CopyFileFromContainer` Zip Slip protection: mock malicious tar header and ensure failure.
   - Test `ExtractArtifact` missing file: returns clear error describing actual dir contents, not panic.
   - Test large file extraction: stream works securely without memory spikes.
   - **Test Isolation:** Run two concurrent containers/sessions with different IDs, extract, and prove non-interference.

## Acceptance

- `go test ./internal/sandbox/ -run TestTransfer -tags integration` passes.
- `go vet ./internal/sandbox/` clean.
- Zero `docker exec cat` patterns remain for file extraction.
- `StageInputs` handles isolated session dirs (`/workspace/.orqestra/<sessionName>/`).
- File transfers use streaming `io.ReadCloser` where possible instead of massive buffers.
- Zip-slip path traversal is mitigated during extraction.
- File ownership naturally aligns with the host since the base sandbox image is pre-configured to mirror host UID/GID (ensuring execution as a non-root user).
- Files touched: `internal/sandbox/transfer.go`, `internal/sandbox/transfer_test.go`, `internal/sandbox/docker.go`.
