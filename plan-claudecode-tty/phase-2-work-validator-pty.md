# Work Package: work-validator-pty

| Field | Value |
|-------|-------|
| **ID** | `work-validator-pty` |
| **Wave** | 10 |
| **depends_on** | `workers-pty` |
| **Files** | `internal/validator/work_validator_pty.go`, `internal/validator/work_validator_pty_test.go` |

## Goal

Migrate the Work Validator agent to sandboxed PTY execution with air-gapped sandbox, receiving specification + all worker results as input.

## Steps

1. Create `internal/validator/work_validator_pty.go`:
   - Work Validator uses `SandboxedPTYRunner` with sandbox config: `network=none` (air-gapped).
   - Input artifact: aggregation of specification + all worker output artifacts + file change manifests.
   - Output: validation report artifact (pass/fail with reasons).
   - Sandbox config:

     ```yaml
     network: none
     memory: 2g
     cpus: 1
     max_lifetime: 10m
     ```

2. Input composition:
   - Aggregate all worker `output.md` artifacts into a single `input.md` for the validator.
   - Include the original specification for reference.
   - Include file change manifest (list of files modified per worker).

3. Output handling:
   - Parse validation result from artifact body.
   - If validation fails: surface failure reason in TUI, hold pipeline, offer re-run option.
   - If validation passes: proceed to apply phase.

4. Test (`//go:build integration`):
   - Pre-write valid worker artifacts + spec → work validator produces pass result.
   - Pre-write inconsistent artifacts → work validator produces fail result.
   - Sandbox enforces no network access.

## Acceptance

- `go test ./internal/validator/ -run TestWorkValidatorPTY -tags integration` passes.
- Work validator sandbox is air-gapped (network=none).
- Validation failure surfaces clearly in TUI with actionable message.
- Artifact chain validation passes through the full pipeline.
- Files touched: ONLY `internal/validator/work_validator_pty.go`, `internal/validator/work_validator_pty_test.go`.
