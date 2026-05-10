## Plan: Handle Claude Code plan-mode side-channel

**TL;DR**: Both researcher and planner run in `permission_mode: plan`. The difference is non-deterministic model behavior — the planner chose to write the plan to `~/.claude/plans/` (Claude Code's native plan file) instead of returning it as text. Fix: when `parsePlanResult` rejects the `result` text, recover the plan from disk via the session JSONL.

---

**Steps**

### Phase 1: Harness — Extract plan file path from session JSONL

1. Add `ExtractPlanFilePath(sessionLogPath string) (string, error)` to `internal/harness/logpath.go`. This scans the session JSONL for `{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"..."}}` and returns the path. Reuse the existing `bufio.Scanner` + 1MB buffer pattern from `ParseSessionLog`.

### Phase 2: Planner — Plan file recovery

2. Extract a `recoverPlanFromSession(sessionID string) (string, error)` helper on `Planner` in `internal/agent/planner.go`. It:
   - Gets cwd via `os.Getwd()`
   - Calls `harness.ResolveSessionLogPath(cwd, sessionID)` (already exists in `internal/harness/logpath.go`)
   - Calls `harness.ExtractPlanFilePath(jsonlPath)` (new from step 1)
   - Reads the plan file with `os.ReadFile`
   - Returns the plan markdown

3. Modify `RefineStreaming`, `Refine`, `RefineWithComments`, and `RefineWithCommentsStreaming` in `internal/agent/planner.go`: after `parsePlanResult` fails, if `result.SessionID != ""`, attempt recovery. If recovery succeeds, re-run `parsePlanResult` with the file contents. If not, return the original error. Log recovery at `slog.Info`.

### Phase 3: Tests

4. Test `ExtractPlanFilePath` in `internal/harness/logpath_test.go` — create temp JSONL with a `plan_mode` attachment, verify extraction. Test missing attachment → error.

5. Test planner recovery in `internal/agent/planner_test.go` — mock runner returns a result without `# Plan` but with a session ID. Set up temp JSONL + plan file. Verify planner recovers.

**Relevant files**

- `internal/harness/logpath.go` — add `ExtractPlanFilePath`, reuse `ResolveSessionLogPath`
- `internal/agent/planner.go` — add `recoverPlanFromSession`, modify all four `Refine*` methods
- `internal/harness/logpath_test.go` — new test
- `internal/agent/planner_test.go` — new test

**Verification**

1. `go test ./internal/harness/...`
2. `go test ./internal/agent/...`
3. `go build ./cmd/orqestra`
4. `go vet ./...`
5. Manual re-run of the same prompt to confirm end-to-end recovery

**Decisions**

- `permission_mode: plan` stays — changing it would let agents modify files unchecked
- Recovery lives in the planner (not harness) — it's plan-mode-specific behavior
- Session JSONL is the source of truth for the plan file path — no regex parsing of conversational text
- `os.Getwd()` for repo path — matches how Claude CLI was invoked
- Original error returned if recovery fails — no silent fallback
