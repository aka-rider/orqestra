## Plan: Handle Claude Code plan-mode side-channel

**TL;DR**: Both researcher and planner run in `permission_mode: plan`. The difference is non-deterministic model behavior — the planner chose to write the plan to `~/.claude/plans/` (Claude Code's native plan file) instead of returning it as text. Fix: when `parsePlanResult` rejects the `result` text, recover the plan from disk via the session JSONL.

---

**Steps**

### Phase 1: Harness — Extract plan file path from session JSONL

1. Add `ExtractPlanFilePath(sessionLogPath string) (string, error)` to `internal/harness/logpath.go`. This unmarshals session JSONL lines into a new struct (e.g., `jsonlAttachmentMessage`) mirroring the attachment schema `{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"..."}}` to strictly parse the path (no regex or string slicing allowed).
2. Use the existing `bufio.Scanner` + 1MB buffer pattern from `ParseSessionLog`, but **return immediately** upon successfully parsing the first plan attachment to avoid unnecessary I/O overhead on large files.

### Phase 2: Planner — Plan file recovery

3. Extract a `recoverPlanFromSession(sessionID string) (string, error)` helper on `Planner` in `internal/agent/planner.go`. It:
   - Gets cwd via `os.Getwd()`
   - Calls `harness.ResolveSessionLogPath(cwd, sessionID)` (already exists in `internal/harness/logpath.go`)
   - Calls `harness.ExtractPlanFilePath(jsonlPath)` (new from step 1)
   - **Crucial Security Gate**: Validate that the extracted `planFilePath` resides within and strictly starts with `~/.claude/plans/` to prevent arbitrary file read traversal vulnerabilities.
   - Reads the plan file with `os.ReadFile`
   - Returns the plan markdown

4. Modify `RefineStreaming`, `Refine`, `RefineWithComments`, and `RefineWithCommentsStreaming` in `internal/agent/planner.go`:
   - After `parsePlanResult` fails, if `result.SessionID != ""`, attempt recovery.
   - If recovery succeeds, **mutate** `result.Output = recoveredPlanStr` and re-run `parsePlanResult(result)` with the updated data. Log recovery at `slog.Info`.
   - If recovery fails, **log the recovery error** (e.g., `slog.Debug("plan file recovery failed", "err", recoverErr)`) to document the fallback failure exactly, then return the original parser error (to prevent silent failures).

### Phase 3: Tests

5. Test `ExtractPlanFilePath` in `internal/harness/logpath_test.go` — create temp JSONL with a `plan_mode` attachment, verify structural extraction. Test missing attachment → error. Test early exit.

6. Test planner recovery in `internal/agent/planner_test.go` — mock runner returns a result without `# Plan` but with a session ID. Set up temp JSONL + plan file. Verify planner mutates result and recovers successfully. Test that out-of-bounds `planFilePath` yields an error.

**Relevant files**

- `internal/harness/logpath.go` — define structural attachment schema, add `ExtractPlanFilePath`, reuse `ResolveSessionLogPath`
- `internal/agent/planner.go` — add `recoverPlanFromSession`, validate paths securely, modify all four `Refine*` methods
- `internal/harness/logpath_test.go` — new tests
- `internal/agent/planner_test.go` — new tests

**Verification**

1. `go test ./internal/harness/...`
2. `go test ./internal/agent/...`
3. `go build ./cmd/orqestra`
4. `go vet ./...`
5. Manual re-run of the same prompt to confirm end-to-end recovery

**Decisions**

- `permission_mode: plan` stays — changing it would let agents modify files unchecked
- Recovery lives in the planner (not harness) — it's plan-mode-specific behavior
- Session JSONL is the source of truth for the plan file path — no regex parsing of conversational text; structural parsing mandated.
- `os.Getwd()` for repo path — matches how Claude CLI was invoked
- Original error returned if recovery fails — but `recoverErr` is explicitly logged preventing silent suppression.
