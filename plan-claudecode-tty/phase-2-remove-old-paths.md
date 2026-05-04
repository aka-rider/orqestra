# Work Package: remove-old-paths

| Field | Value |
|-------|-------|
| **ID** | `remove-old-paths` |
| **Wave** | 11 |
| **depends_on** | `planner-pty`, `validator-pm-pty`, `workers-pty`, `work-validator-pty` |
| **Files** | Multiple files across `internal/` — deletions and modifications |

## Goal

Remove legacy execution paths that are fully replaced by the PTY-based pipeline. Only after all agents are migrated.

## Steps

1. Remove `streamView` from `internal/tui/view_stream.go`:
   - Fully replaced by `termView`.
   - Remove all references in `view_tabs.go` and `model.go`.

2. Remove from `internal/planner/`:
   - `planner.PlanStreaming()` — replaced by sandbox PTY + artifact extraction.
   - Any JSON specification output format logic.

3. Remove from `internal/pm/`:
   - `pm.DecomposeStreaming()` — replaced by sandbox PTY + artifact extraction.

4. Remove from `internal/harness/`:
   - `SandboxedCLIRunner.RunPrint()` / `RunStreaming()` — replaced by `SandboxedPTYRunner`.
   - Keep `CLIRunner` only for cheap llama-server API calls (local validation).

5. Remove from agent execution:
   - `--output-format stream-json` flag usage — agents run in native interactive mode.
   - JSON specification as inter-agent protocol — replaced by markdown artifacts.

6. Update tests:
   - Remove tests that exercised deleted code paths.
   - Ensure remaining tests still pass after removals.

7. Run full test suite: `go test ./...` must pass after all removals.

## Acceptance

- `go test ./...` passes.
- `go vet ./...` clean.
- No references to `streamView`, `PlanStreaming`, `DecomposeStreaming`, `RunPrint` (for agent execution), or `--output-format stream-json` remain.
- `CLIRunner` retained only for llama-server local calls.
- No dead code (unused imports, unreachable functions).
