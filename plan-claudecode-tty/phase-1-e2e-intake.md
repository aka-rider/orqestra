# Work Package: e2e-intake

| Field | Value |
|-------|-------|
| **ID** | `e2e-intake` |
| **Wave** | 7 |
| **depends_on** | `pty-runner-lifecycle`, `tui-term-integration`, `input-detection` |
| **Files** | `internal/harness/pty_runner_e2e_test.go` |

## Goal

Single end-to-end integration test proving the full Intake agent lifecycle with real Claude Code in a real Docker sandbox with a real PTY. This is the graduation test for Phase 1.

## Steps

1. Create `internal/harness/pty_runner_e2e_test.go` (build tag `//go:build e2e`):

```go
func TestE2E_IntakePTY(t *testing.T) {
    // 1. Generate session name
    // 2. Write user prompt as input artifact
    // 3. Prepare sandbox (Docker, network=bridge)
    // 4. Launch Claude Code PTY inside sandbox
    // 5. Wait for PTYDoneMsg (timeout: 3 min)
    // 6. Collect output artifact
    //
    // Assert:
    //   - output.md exists in session dir with valid frontmatter
    //   - output body is non-empty markdown
    //   - input_hash in output matches hash of input.md
    //   - system-prompt.md saved in session dir
    //   - container destroyed (docker ps shows nothing with orqestra label)
    //   - volumes removed (docker volume ls shows nothing)
    //   - no zombie processes
    //   - session dir contains: input.md, output.md, system-prompt.md
}
```

1. Test setup requirements:
   - Docker running with `orqestra-sandbox:latest` image built.
   - `ANTHROPIC_API_KEY` environment variable set.
   - Network access for Claude Code API calls.
   - Timeout: 3 minutes (Claude Code startup + prompt processing).

2. Test cleanup:
   - `t.Cleanup` calls `Destroy()` unconditionally.
   - Verify no orphaned containers: `docker ps -a --filter label=orqestra`.
   - Verify no orphaned volumes.

3. This test runs real Claude Code. It costs tokens. It's slow (~1-2 min). Run only with `go test -tags e2e`.

## Acceptance

- `go test ./internal/harness/ -tags e2e -run TestE2E_IntakePTY -timeout 5m` passes.
- Output artifact has valid YAML frontmatter with all required fields.
- Artifact chain validation passes (InputHash matches).
- No orphaned Docker resources after test.
- Files touched: ONLY `internal/harness/pty_runner_e2e_test.go`.
