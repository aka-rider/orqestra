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
   - Remove all stream-specific `tea.Msg` types in `internal/tui/messages.go` (e.g. `streamChunkMsg`, `streamErrorMsg`) and model properties tracking old stream states.

2. Update `internal/planner/` and `internal/pm/`:
   - Remove `planner.PlanStreaming()` and `pm.DecomposeStreaming()`.
   - Ensure the JSON parsers are explicitly transitioned to the new Markdown artifact parser safely decoupled in `plan/spec.go` instead of blind deletion.

3. Update Interface definition (`internal/harness/client.go` / `sandbox.Runner`):
   - Preserve core interface signatures but explicitly strip `RunPrint()` / `RunStreaming()` bounds.
   - Refactor `SandboxedCLIRunner` and ensure `CLIRunner` executes cleanly under the base `Run()` command for `llama-server`.

4. Update config & command builder:
   - Scrub `--output-format stream-json` from the CLI configurations (`internal/config/config.go` & `harness/claude_cli.go`).
   - JSON specification as inter-agent protocol — replaced by markdown artifacts.

5. Update tests:
   - Remove tests that exercised deleted code paths.
   - Add E2E equivalency check: Verify that PTY-based execution checks fill coverage vacated by the removed `PlanStreaming` test variants. Ensure `go test ./...` coverage on orchestration doesn't drop.

6. Run full test suite: `go test ./...` must pass after all removals.

## Acceptance

- `go test ./...` passes.
- `go vet ./...` clean.
- No references to `streamView`, `PlanStreaming`, `DecomposeStreaming`, `RunPrint` (for agent execution), or `--output-format stream-json` remain.
- `CLIRunner` retained only for llama-server local calls.
- No dead code (unused imports, unreachable functions).
