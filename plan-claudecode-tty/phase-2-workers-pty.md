# Work Package: workers-pty

| Field | Value |
|-------|-------|
| **ID** | `workers-pty` |
| **Wave** | 9 |
| **depends_on** | `planner-pty` |
| **Files** | `internal/harness/pty_runner.go` (worker path in Collect), `internal/scheduler/` adjustments |

## Goal

Enable parallel worker agents running in their own sandboxed PTY tabs, with file change extraction via overlayfs/git diff and staging to session directory.

## Steps

1. Extend `SandboxedPTYRunner.Collect()` worker path:
   - For worker agents (step prefix "05"): call `Sandbox.ExtractChanges(ctx)`.
   - Run security verification on extracted changes (path traversal, size, executables).
   - Stage verified files to `<session>/<step>/changes/` via `Sandbox.CopyOut()`.
   - Return `CollectResult{Artifact, Changes}`.

2. Parallel PTY tabs:
   - Workers get individual tabs: `05-worker-0`, `05-worker-1`, etc.
   - Each in its own sandbox with `network=bridge`.
   - Worker sandbox config:

     ```yaml
     network: bridge
     memory: 4g
     cpus: 2
     max_lifetime: 50m
     allowed_executables:
       - "*.sh"
       - "node_modules/.bin/*"
     ```

   - Claude Code tools: `Read`, `Bash`, `Write` (full implementation access).

3. Scheduler integration:
   - Workers from the same dependency wave launch concurrently.
   - Each worker's `RunningAgent` gets a unique `tabIndex`.
   - `PTYOutputMsg`, `PTYDoneMsg` route by `TabIndex` to the correct tab.

4. File change application:
   - After all workers complete and pass validation: apply verified changes from `<session>/<step>/changes/` to host repo.
   - Application is gated behind human confirmation in TUI.

5. Tests (`//go:build integration`):
   - Two parallel workers writing to disjoint files → both Collect successfully.
   - Worker that modifies existing file → ExtractChanges detects the diff.
   - Path traversal attempt rejected by verifier.
   - Worker tab switching works (ctrl+space to needs-input worker).

## Acceptance

- `go test ./internal/harness/ -run TestWorkerPTY -tags integration` passes.
- Parallel workers produce independent change sets in their respective `changes/` dirs.
- Security verifier rejects path traversal and oversized files.
- Tab switching to needs-input worker functions correctly.
- Files touched: `internal/harness/pty_runner.go` (worker Collect path), scheduler adjustments, test files. Does NOT modify `internal/sandbox/` extract logic (uses existing `ExtractChanges`).
