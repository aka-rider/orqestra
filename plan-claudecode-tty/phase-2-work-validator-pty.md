# Work Package: work-validator-pty

| Field | Value |
|-------|-------|
| **ID** | `work-validator-pty` |
| **Wave** | 10 |
| **depends_on** | `workers-pty` |
| **Files** | `internal/validator/work_validator_pty.go`, `internal/validator/work_validator_pty_test.go` |

## Goal

IMPLEMENT ONLY the Work Validator agent PTY execution logic with air-gapped sandbox, receiving specification + all worker results as input. (Note: TUI surfacing and Pipeline integration are deferred or managed strictly by returning deterministic validation payloads instead of implementing TUI logic directly here).

## Steps

1. Create `internal/validator/work_validator_pty.go`:
   - Work Validator uses `SandboxedPTYRunner`.
   - Sandbox config fields explicitly translated to Go structure: `sandbox.Config{Network: "none", Memory: "2g", CPUs: 1, MaxLifetime: 10 * time.Minute}`.
   - Requires explicit routing mapping to allow access to local proxy while airgapped.
   - Interface matches runner signature, accepting context, master specification path, and a slice of worker artifact paths.
   - Output: validation report artifact (pass/fail with reasons).

2. Input composition (Handled locally inside WorkValidator's `Prepare` implementation):
   - Combine all injected worker `output.md` artifacts into a single master `input.md` for the sandboxed validation agent.
   - Include the original specification for reference.
   - Include file change manifest (list of files modified per worker extracted from the Artifact `Metadata`).

3. Output handling:
   - Parse validation result from artifact body.
   - Return structured validation failure/success to the orchestrator layer (does NOT implement the TUI blocking logic, only formats the error domain objects).

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
, securely maintaining proxy routing.
- Validation logic properly returns structured objects for the Pipeline to consume independently of the TUI logic
