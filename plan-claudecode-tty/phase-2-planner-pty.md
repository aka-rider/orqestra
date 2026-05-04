# Work Package: planner-pty

| Field | Value |
|-------|-------|
| **ID** | `planner-pty` |
| **Wave** | 8 |
| **depends_on** | `e2e-intake` |
| **Files** | `internal/planner/planner_pty.go`, `internal/planner/planner_pty_test.go`, sandbox config for planner |

## Goal

Migrate the Planner agent from `--output-format stream-json` to sandboxed PTY execution with artifact-based output. Same `SandboxedPTYRunner` path as Intake.

## Steps

1. Create planner PTY integration in `internal/planner/planner_pty.go`:
   - Planner uses `SandboxedPTYRunner` with sandbox config: `network=bridge`, read-only repo mount at `/workspace`.
   - Planner system prompt instructs: read input.md, research the codebase, write specification to output.md.
   - Claude Code tools allowed: `Read`, `Bash` (for curl, grep, find, git log).
   - No write to repo: planner writes ONLY to `/workspace/.orqestra/output.md`.

2. Wire human gate to read the extracted `output.md` artifact and display in confirm viewport:
   - Parse specification from markdown artifact body (replaces JSON parsing from `planner.PlanStreaming()`).
   - Existing confirm view receives parsed spec for display.

3. Sandbox config for planner:

   ```yaml
   network: bridge
   memory: 4g
   cpus: 2
   max_lifetime: 15m
   read_only_mounts:
     - host: /workspace
       container: /workspace
   ```

4. Create `internal/planner/planner_pty_test.go` (build tag `//go:build integration`):
   - Test Prepare: planner sandbox has read-only mount, network=bridge.
   - Test that output artifact has valid specification structure.
   - Test that planner cannot write to `/workspace` (read-only mount enforced).

## Acceptance

- `go test ./internal/planner/ -run TestPlannerPTY -tags integration` passes.
- Planner sandbox enforces read-only repo mount.
- Planner output is a valid artifact with specification body.
- Human gate displays specification from artifact (not JSON).
- Files touched: ONLY `internal/planner/planner_pty.go`, `internal/planner/planner_pty_test.go`, related sandbox config.
