# Work Package: validator-pm-pty

| Field | Value |
|-------|-------|
| **ID** | `validator-pm-pty` |
| **Wave** | 9 |
| **depends_on** | `planner-pty` |
| **Files** | `internal/validator/plan_validator_pty.go`, `internal/pm/pm_pty.go`, related test files |

## Goal

Migrate Plan Validator and Project Manager agents to sandboxed PTY execution with air-gapped (network=none) sandbox configs and artifact-based I/O.

## Steps

### Plan Validator

1. Create `internal/validator/plan_validator_pty.go`:
   - Validator uses `SandboxedPTYRunner` with sandbox config: `network=none` (air-gapped).
   - Input: specification artifact from planner step.
   - Output: validation report artifact.
   - Sandbox config:

     ```yaml
     network: none
     memory: 2g
     cpus: 1
     max_lifetime: 5m
     ```

2. Test (`//go:build integration`): validator sandbox has no network access, produces valid validation artifact.

### Project Manager

1. Create `internal/pm/pm_pty.go`:
   - PM uses `SandboxedPTYRunner` with sandbox config: `network=none` (air-gapped).
   - Input: specification artifact.
   - Output: project plan artifact with work packages.
   - Sandbox config:

     ```yaml
     network: none
     memory: 2g
     cpus: 1
     max_lifetime: 10m
     ```

2. Wire human gate on decomposed work packages:
   - PM output artifact parsed into work package list.
   - Confirm view displays work packages for user approval.

3. Test (`//go:build integration`): PM sandbox has no network access, produces valid project plan artifact.

## Acceptance

- `go test ./internal/validator/ -run TestPlanValidatorPTY -tags integration` passes.
- `go test ./internal/pm/ -run TestPMPTY -tags integration` passes.
- Both sandboxes enforce `network=none` (air-gapped — cannot reach external hosts).
- Artifact chain validation passes: validator's InputHash matches planner output hash; PM's InputHash matches planner output hash.
- Files touched: ONLY `internal/validator/plan_validator_pty.go`, `internal/pm/pm_pty.go`, and their test files.
