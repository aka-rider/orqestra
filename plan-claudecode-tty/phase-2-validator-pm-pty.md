# Work Package: validator-pm-pty

| Field | Value |
|-------|-------|
| **ID** | `validator-pm-pty` |
| **Wave** | 9 |
| **depends_on** | `planner-pty` |
| **Files** | `internal/validator/plan_validator_pty.go`, `internal/pm/pm_pty.go`, `internal/scheduler/graph.go` (or `pipeline.yaml`), `internal/tui/model.go` (minor for gate wiring), and related tests |

## Goal

Migrate Plan Validator and Project Manager agents to sandboxed PTY execution with air-gapped (`network=none`) sandbox configs and artifact-based I/O. Note: The LLM process inside the air-gapped sandboxes requires specific routing (via Docker host networking mapping like `host.docker.internal` or explicit socket mounts) to reach the embedded `CLIProxyAPI` while remaining isolated from the external internet.

## Steps

### Plan Validator

1. Create `internal/validator/plan_validator_pty.go`:
   - Validator uses `SandboxedPTYRunner`.
   - Sandbox config fields explicitly translated to Go structure: `sandbox.Config{Network: "none", Memory: "2g", CPUs: 1, MaxLifetime: 5 * time.Minute}`. Validate host routing for `CLIProxyAPI` access is intact under this strict network isolation.
   - Input: specification artifact from planner step.
   - Output: validation report artifact.

2. Test (`//go:build integration`): validator sandbox has no network access, produces valid validation artifact.

### Project Manager

1. Create `internal/pm/pm_pty.go`:
   - PM uses `SandboxedPTYRunner` with sandbox config: `network=none` (air-gapped).
   - Input: specification artifact.
   - Output: project plan artifact with work packages.
   - Sandbox config:

     ```yaml
     network: none
     memory: 2g.
   - Sandbox config explicitly mapped to Go struct: `sandbox.Config{Network: "none", Memory: "2g", CPUs: 1, MaxLifetime: 10 * time.Minute}`.

   - Input: specification artifact.
   - Output: project plan artifact with work packages.

2. Wire human gate and Scheduler Graph:
   - PM output artifact parsed into work package list.
   - Connect the implementation into the core orchestrator flow by updating `internal/scheduler/graph.go` (and config if applicable).
   - Confirm view displays work packages for user approval (update `internal/tui/model.go` as necessary for parsing transition)` passes.

- Both sandboxes enforce `network=none` (air-gapped — cannot reach external hosts).
- Artifact chain validation passes: validator's InputHash matches planner output hash; PM's InputHash matches planner output hash.
- Files touched: ONLY `internal/validator/plan_validator_pty.go`, `internal/pm/pm_pty.go`, and their test files.
 but cleanly route proxy HTTP calls to the orchestrator layer.
- Artifact chain validation passes: validator's InputHash matches planner output hash; PM's InputHash matches planner output hash.
- Files touched: `internal/validator/plan_validator_pty.go`, `internal/pm/pm_pty.go`, `internal/scheduler/graph.go`, `internal/tui/model.go`, and related test
