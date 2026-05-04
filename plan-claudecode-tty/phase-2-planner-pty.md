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
   - Must implement the existing planner interface/factory signature. Add a config toggle to determine instantiation.
   - Planner uses `SandboxedPTYRunner` with sandbox config: `network=bridge`, read-only repo mount at `/workspace`.
   - Explicitly define a writable output directory in the sandbox (e.g., `/tmp/orqestra-artifacts/`) and instruct the LLM to write the artifact there.
   - Claude Code tools allowed: `Read`, `Bash` (for curl, grep, find, git log).
   - No write to repo: planner writes ONLY to the designated writable artifacts directory.
   - Call `sandbox.ExtractArtifact` internally to pull the generated `output.md` from the sandbox to the host.
   - Parse specification from markdown artifact body securely avoiding JSON completely mapping values directly to `*plan.Spec`.

2. Wire human gate:
   - Pass the parsed `*plan.Spec` into the existing TUI flow. Do not modify `internal/tui/model.go` directly.

3. Sandbox config for planner (hardcoded in `planner_pty.go` as `sandbox.Config` defaults unless `orqestra.yaml` supports it):

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

- `go test ./internal/planner/ -run Tparsed directly to`*plan.Spec`.
- PTY logic successfully abstracts parsing so TUI changes are not required
- Planner output is a valid artifact with specification body.
- Human gate displays specification from artifact (not JSON).
- Files touched: ONLY `internal/planner/planner_pty.go`, `internal/planner/planner_pty_test.go`, related sandbox config.
