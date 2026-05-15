# Plan: Architect Dialog Debug Tools & Git Micro-Repo Fix

## Gap Analysis (9 gaps)

| #   | Gap                                                      | Where                                                                                                   | Evidence                                                                                                                                                   |
| --- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | **Critic revision silently dropped**                     | `orchestrator.go` L680 — `if revisedPlan != nil { planRepo.Commit(...) }`                               | Session `162804`: `architect_critic_revision_meta.json` exists (5306 tokens, "done") but git log has no critic commit                                      |
| 2   | **User comment revision silently dropped**               | `orchestrator.go` L800 — same `if revisedPlan != nil` guard                                             | If `detectPlanRevision` returns nil, no commit. User's feedback and architect's response vanish.                                                           |
| 3   | **User comment text never persisted**                    | `orchestrator.go` L728 — `decision.Comment` goes to `ContinueSession()` as param, never written to file | After run exits, user's words are gone. Only in-memory `chatHistory` (TUI RAM).                                                                            |
| 4   | **Architect chat-only response never persisted**         | `orchestrator.go` L808 — `EventChatResponse` updates TUI RAM only                                       | Architect's answer lost on exit.                                                                                                                           |
| 5   | **No run-level log file**                                | `main.go` L42 — `slog.SetDefault(slog.NewTextHandler(stderr, ...))`                                     | TUI owns stderr → all slog output swallowed. Every `slog.Warn("plan commit failed")` invisible. `StepMeta` written post-completion → crash loses all.      |
| 6   | **`detectPlanRevision` false negatives**                 | `architect.go` L133 — `strings.TrimSpace` comparison + baseline fallback                                | Critic template says "re-output the COMPLETE plan" → same content → revision suppressed. Baseline failure only logged to slog.Debug (swallowed per GAP 5). |
| 7   | **Diff shows last revision only**                        | `gitrepo.go` L61 — hardcoded `HEAD~1 HEAD`                                                              | No history browsing in TUI. (Out of scope — git repo browsable from terminal post-run.)                                                                    |
| 8   | **Zero test coverage for DecisionComment**               | `orchestrator_test.go`                                                                                  | Tests for Approve, Cancel, Edit exist. Nothing for Comment flow.                                                                                           |
| 9   | **Architect doesn't see what user changed after Ctrl+E** | `orchestrator.go` DecisionEdit handler L718                                                             | Commits and re-shows gate but no diff tracked. Architect gets wall of text, must spot-the-difference.                                                      |

---

## Git Micro-Repo Structure

```
plan-history/
├── .git/
├── plan.md      # Changes when architect revises OR user Ctrl+E edits.
└── dialog.md    # Append-only conversation log. Changes EVERY iteration.
```

Two files, no metadata.json. Everything metadata.json would store is already in dialog.md + git commit metadata.

**`dialog.md`** format (append-only):

```markdown
---
## [2026-05-15 17:11:57] architect: initial plan
Plan committed. 59226 output tokens.
---

## [2026-05-15 17:21:00] critic

3 blockers found (1 high, 1 medium, 1 low).

---

## [2026-05-15 17:23:00] architect: Re: critic feedback (no changes)

No plan revision. 5306 output tokens.

---

## [2026-05-15 17:45:00] user

The root cause analysis in WP1 seems wrong — the binary wasn't rebuilt.

---

## [2026-05-15 17:50:00] user: manual edit

(see plan.md diff)

---

## [2026-05-15 18:05:00] architect: Re: user feedback

Revised plan. 27319 output tokens.
```

**Commit messages:**

| Event                             | Message                                       |
| --------------------------------- | --------------------------------------------- |
| First plan                        | `initial plan`                                |
| Critic report                     | `critic: <first line, 50ch>`                  |
| Architect post-critic (changed)   | `architect: Re: critic feedback`              |
| Architect post-critic (unchanged) | `architect: Re: critic feedback (no changes)` |
| User comment                      | `user: <truncated 50ch>`                      |
| Architect revised                 | `architect: Re: <truncated 50ch>`             |
| Architect chat only               | `architect: Re: <truncated 50ch> (chat only)` |
| User manual edit (^E)             | `user: manual edit`                           |

**New `GitRepo` API:**

| Method                                             | Writes              | When                                                               |
| -------------------------------------------------- | ------------------- | ------------------------------------------------------------------ |
| `CommitPlan(markdown, message)`                    | plan.md + dialog.md | User ^E edit (auto-appends `(see plan.md diff)` to dialog.md)      |
| `CommitDialog(entry DialogEntry)`                  | dialog.md only      | User comment, critic report, architect chat-only                   |
| `CommitPlanAndDialog(markdown, entry DialogEntry)` | plan.md + dialog.md | Initial plan, architect revision                                   |
| `DiffPlain(sinceHash)`                             | — (read-only)       | Plain unified diff (no ANSI, 3 context lines) for architect prompt |

`DialogEntry`: `{ Timestamp time.Time, Role string, Message string, OutputTokens int }`

**Orchestrator call sites (every code path):**

| Orchestrator site             | Current code                                           | New code                                                                               |
| ----------------------------- | ------------------------------------------------------ | -------------------------------------------------------------------------------------- |
| L422 (--plan load)            | `planRepo.Commit(plan, "user: plan loaded from file")` | `CommitPlanAndDialog(plan, {Role:"user", Msg:"plan loaded from file"})`                |
| L559 (initial plan)           | `planRepo.Commit(plan, "architect: initial plan")`     | `CommitPlanAndDialog(plan, {Role:"architect", Msg:"initial plan", Tokens:N})`          |
| **NEW** (critic report)       | _nothing_                                              | `CommitDialog({Role:"critic", Msg:firstLine(report)})`                                 |
| L680 (post-critic, changed)   | `if revisedPlan != nil { planRepo.Commit(...) }`       | `CommitPlanAndDialog(plan, {Role:"architect", Msg:"Re: critic feedback", Tokens:N})`   |
| L680 (post-critic, unchanged) | _nothing_                                              | `CommitDialog({Role:"architect", Msg:"Re: critic feedback (no changes)", Tokens:N})`   |
| **NEW** (user comment)        | _nothing_                                              | `CommitDialog({Role:"user", Msg:decision.Comment})`                                    |
| L800 (post-comment, changed)  | `if revisedPlan != nil { planRepo.Commit(...) }`       | `CommitPlanAndDialog(plan, {Role:"architect", Msg:"Re: "+trunc(comment), Tokens:N})`   |
| L808 (chat-only)              | _nothing_                                              | `CommitDialog({Role:"architect", Msg:"Re: "+trunc(comment)+" (chat only)", Tokens:N})` |
| L723 (^E edit)                | `planRepo.Commit(edited, "user: manual edit")`         | `CommitPlan(edited, "user: manual edit")` — also store pendingDiff                     |

---

## Ctrl+E Edit Confirmation Flow

**Current (broken)**: Editor exits → `editorReturnMsg` → if changed, immediately sends `DecisionEdit` → no chance to explain changes or abort.

**New flow**:

```
Ctrl+E → TUI suspends → editor → editor exits → TUI compares
                                                      │
                                              unchanged? → silent return to plan review
                                                      │
                                               changed → show confirmation prompt
                                                      │
                              ┌────────────────────────┴─────────────────────┐
                              │                                              │
                    "Yes" [Tab: add context...]                  "No, continue editing"
                              │                                              │
                     sends DecisionEdit                          returns to plan review
                  + optional comment from Tab                    (Ctrl+E to re-enter)
                              │
              ┌───────────────┴───────────────┐
              │                               │
        comment provided               no comment
              │                               │
    CommitPlanAndDialog              CommitPlan("user: manual edit")
    + architect gets diff            + pendingDiff stored for next comment
    + architect gets comment
    in one ContinueSession call
```

**Implementation:**

- New `ContentEditConfirm` mode in `PipelineScreen` — reuses option selector rendering from `viewUserQuestion`. Ensure the option selector and optional text input remain encapsulated rather than bleeding UI state variables up into `PipelineScreen`.
- `pendingEditContent` field on `PipelineScreen` — holds edited content until confirmed
- `editorReturnMsg` handler: if changed → store `pendingEditContent`, set `content = ContentEditConfirm`. Do NOT send DecisionEdit yet.
- "Yes" + Enter → `ConfirmEditIntent{EditedContent, Comment}` → `DecisionEdit` payload packed with both fields
- "No" + Enter → clear `pendingEditContent`, return to `ContentPlanReview`, `finalPlan` unchanged (still pre-edit)
- Tab on "Yes" → inline textarea "Describe your changes..."

**Orchestrator `DecisionEdit` handler expansion:**

- Commits to git, storing the last architect commit hash to properly track diffs across sequential user edits: `lastArchitectHash = planRepo.HeadCommitHash()`
- If `decision.Comment` provided: immediately call `architect.ContinueSessionWithDiff()` — sends edit + comment + diff (using `git diff <lastArchitectHash> HEAD -- plan.md`) in one shot. Resets baseline.
- If no comment: `continue` as today, accumulating changes against `lastArchitectHash` for the next `DecisionComment`

**Decision struct change** — `Comment` field already exists on `Decision` for `DecisionComment`. Phase 2 reuses it for `DecisionEdit` (when user adds context via Tab):

```go
type Decision struct {
    Type          DecisionType
    EditedContent string // for DecisionEdit
    Comment       string // for DecisionComment AND DecisionEdit (when user adds context)
}
```

---

## Architect Receives Diff After User Edits

New template when diff is available:

```
<plan_changes>
{plain unified diff — no ANSI colors, 3 context lines}
</plan_changes>

<current_plan>
{full edited plan}
</current_plan>

<reviewer_message>
{user comment}
</reviewer_message>

Focus on the reviewer's edits first. If the reviewer asks a question, answer it.
If the reviewer requests further changes, revise the plan.
```

- `ContinueSessionWithDiff(ctx, sessionID, currentPlan, diff, comment, stdout)` — uses diff template when diff non-empty, falls back to existing `continuePromptTemplate` when empty
- `DiffPlain(sinceHash)` on GitRepo: `git diff --no-color <sinceHash> HEAD -- plan.md`

---

## Run Log

- In `Engine.run()` goroutine, after `session.Path` available (~L290):
  - `os.OpenFile(filepath.Join(session.Path, "run.log"), O_CREATE|O_WRONLY|O_APPEND, 0644)`
  - IMPORTANT: Protect global slog closure leakage. Before `defer f.Close()`, ensure you execute `defer slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))` so trailing routines don't attempt writing onto the closed file handle!
  - `logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))`
  - `slog.SetDefault(logger)` inside the goroutine so that all agent and harness logs are captured (since TUI sets the global logger to `io.Discard` in main, this safely redirects internal output to the file without breaking stderr).
  - `defer f.Close()`
- Replace every `slog.Warn`/`.Debug`/`.Info` directly inside orchestrator `run()` body with `logger.Warn`/etc (optional but good practice).
- Add structured entries at decision points:
  - `logger.Info("gate: user decision", "type", decision.Type, "comment_len", len(decision.Comment))`
  - `logger.Info("architect: continuation result", "revised", revisedPlan != nil, "chat_len", len(chatResponse), "output_tokens", revisedUsage.OutputTokens)`
  - In `detectPlanRevision`: `logger.Debug("revision detection", "baseline_err", baselineErr, "baseline_len", len(baseline), "current_len", len(currentPlan), "post_run_len", len(planContent), "matches_baseline", eq1, "matches_current", eq2)`

---

## Implementation Phases

### Phase 1: Run Log (standalone, parallel with Phase 2)

**1.1** — Open `run.log` in `Engine.run()`, create scoped `*slog.Logger`, `defer f.Close()`
**1.2** — Replace all `slog.*` calls in `run()` with `logger.*`, add structured entries at decision points

Files: `internal/orchestrator/orchestrator.go`

### Phase 2: Git Micro-Repo + Ctrl+E Flow + Diff-to-Architect (standalone, parallel with Phase 1)

**2.1** — Add `DialogEntry`, `CommitPlan`, `CommitDialog`, `CommitPlanAndDialog`, `DiffPlain(sinceHash)`, `HeadCommitHash()` to `GitRepo`. Keep old `Commit()` as deprecated redirect. All staging commands (`git add`) must explicitly include the targeted files (`plan.md`, `dialog.md`). Note that `CommitDialog` should open `dialog.md` configured for appending: `os.OpenFile(..., os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)` so it creates the dialogue cache explicitly if logging first!
Files: `internal/plan/gitrepo.go`

**2.2** — Replace all 5 `planRepo.Commit()` sites + add 3 new `CommitDialog` calls + sequential `lastArchitectHash` diff tracking.
Files: `internal/orchestrator/orchestrator.go`

**2.3** — Add `continueWithDiffTemplate` and `ContinueSessionWithDiff()` to architect.
Files: `internal/agent/architect.go`

**2.4** — Ctrl+E edit confirmation: `ContentEditConfirm` mode, `ConfirmEditIntent`, `pendingEditContent`, `handleEditConfirmKey()`, `viewEditConfirm()`.
The `editorReturnMsg` is handled in `Model.Update()` (model.go L134), NOT in `PipelineScreen.Update`. Add `ContentEditConfirm` handling to `Model.Update()`'s `editorReturnMsg` case (set `pendingEditContent`, switch content mode). Add a new `PipelineScreen.handleEditConfirmKey()` called from `PipelineScreen.Update` when `content == ContentEditConfirm`. Do NOT change `PipelineScreen.Update`'s `tea.KeyPressMsg` signature — that would break all 7 content-mode key handlers.
Files: `internal/tui/screen_pipeline.go`, `internal/tui/model.go`, `internal/tui/messages.go`

**3.1** — `FakeRunner` enhancement: `OnCall func(callIndex int)` callback.
Files: `internal/testutil/doubles.go`

**3.2** — GitRepo unit tests:

| #   | Test name                                   | Proves                                                                                                                           |
| --- | ------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `TestGitRepo_CommitPlanAndDialog`           | 1 commit: plan.md + dialog.md written, git log = 1                                                                               |
| 2   | `TestGitRepo_DialogOnlyCommit_NoPlanChange` | `git diff HEAD~1 HEAD -- plan.md` empty. `git diff HEAD~1 HEAD -- dialog.md` non-empty. `git log -- plan.md` = 1. `git log` = 2. |
| 3   | `TestGitRepo_PlanRevision_UpdatesBothFiles` | Both plan.md and dialog.md in diff                                                                                               |
| 4   | `TestGitRepo_DialogAppendOnly`              | 3 CommitDialog: dialog.md has all 3 entries in order, each preceded by `---`                                                     |
| 5   | `TestGitRepo_FullConversation`              | 7-step sequence → 7 commits. `git log -- plan.md` = 2-3. dialog.md = 7 entries. plan.md = last revised.                          |
| 6   | `TestGitRepo_DiffPlain_NoColor`             | No ANSI escapes. Contains `---`/`+++` markers.                                                                                   |
| 7   | `TestGitRepo_CommitPlan_UserEdit`           | plan.md updated, dialog.md has "(see plan.md diff)" entry                                                                        |

Files: `internal/plan/gitrepo_test.go`

**3.3** — Orchestrator integration tests:

| #   | Test name                                   | Sequence                                                    | Assertions                                                                   |
| --- | ------------------------------------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------- |
| 1   | `TestEngine_DecisionComment_CommitsDialog`  | Gate → Comment("fix WP1") → gate → Approve                  | dialog.md has "user: fix WP1". Git has both commits. plan.md updated.        |
| 2   | `TestEngine_DecisionComment_ChatOnly`       | Gate → Comment("why?") → EventChatResponse → gate → Approve | Git has "user: why?" + "architect: Re: why? (chat only)". plan.md unchanged. |
| 3   | `TestEngine_CriticRevision_AlwaysCommitted` | AutoApprove (with critic)                                   | Git has "critic: ..." + "architect: Re: critic feedback (no changes)".       |
| 4   | `TestEngine_RunLog_Created`                 | AutoApprove                                                 | `run.log` exists, size > 0, contains "phase" or "agent".                     |
| 5   | `TestEngine_FullConversation_Integrity`     | Critic + 2 Comments + Approve                               | git log count correct. dialog.md count correct. plan.md = final.             |
| 6   | `TestEngine_DecisionEdit_CommitsDialog`     | Gate → Edit("new") → gate → Approve                         | Git has "user: manual edit". Both files changed.                             |

Files: `internal/orchestrator/orchestrator_test.go`

**3.4** — Edit confirmation TUI tests:

| #   | Test name                        | Proves                                                                           |
| --- | -------------------------------- | -------------------------------------------------------------------------------- |
| 1   | `TestEditConfirm_YesWithComment` | "Yes" + Tab comment → `ConfirmEditIntent` with content + comment                 |
| 2   | `TestEditConfirm_YesNoComment`   | "Yes" without Tab → `ConfirmEditIntent` with content only                        |
| 3   | `TestEditConfirm_No`             | "No" → returns to `ContentPlanReview`. No intent emitted. `finalPlan` unchanged. |
| 4   | `TestEditConfirm_UnchangedFile`  | No prompt shown, returns to plan review silently                                 |

Files: `internal/tui/screen_pipeline_test.go`

**FakeRunner call sequencing (concrete example for orchestrator test 1):**

```
Researcher: call 0 → {Output:"saved", SessionID:"test-researcher-sid"}
Architect initial: call 0 → {Output:"saved", SessionID:"test-architect-sid"}
  plan read from ~/.claude/plans/test-architect-sid-plan.md
Architect continue: call 1 → {Output:"chat", SessionID:"test-architect-sid",
  OnCall: func(i int) { os.WriteFile("test-architect-sid-plan.md", revisedContent) }}
  detectPlanRevision sees file changed → revisedPlan != nil
Worker: separate FakeRunner, calls 0+1+2
```

### Phase 4: Fix `detectPlanRevision` (depends on Phase 1 diagnostics)

**4.1** — Reproduce "same plan" with run.log, read comparison details.
**4.2** — Likely fix: skip echo suppression for critic flow (`ContinueWithCriticReport`) since template demands full re-output. Add `skipEchoSuppression bool` to `detectPlanRevision`.
Files: `internal/agent/architect.go`, `internal/agent/architect_test.go`

---

## Verification Checklist

1. `go test ./internal/plan/ -run TestGitRepo` — all 7 new tests pass
2. `go test ./internal/orchestrator/ -run TestEngine_DecisionComment` — comment flow
3. `go test ./internal/orchestrator/ -run TestEngine_CriticRevision` — critic never dropped
4. `go test ./internal/orchestrator/ -run TestEngine_RunLog` — log created
5. `go test ./internal/orchestrator/ -run TestEngine_FullConversation` — end-to-end
6. `go test ./internal/tui/ -run TestEditConfirm` — all 4 edit confirmation tests
7. **Manual**: run, comment ×2, `cd .orqestra/sessions/*/plan-history && git log --oneline` → all turns. `cat dialog.md` → full conversation. `git log --oneline -- plan.md` → only plan changes.
8. **Manual**: Ctrl+E edit → confirmation prompt appears → Tab to add comment → architect receives diff
9. **Crash test**: `kill -9` mid-architect → `run.log` has entries up to kill
10. **Grep guarantee**: `grep planRepo orchestrator.go` → every call is CommitPlan/CommitDialog/CommitPlanAndDialog

## Files Changed

| File                                         | What                                                                                                             |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `internal/plan/gitrepo.go`                   | `DialogEntry`, `CommitPlan`, `CommitDialog`, `CommitPlanAndDialog`, `DiffPlain()`, deprecate `Commit()`          |
| `internal/plan/gitrepo_test.go`              | 7 new tests                                                                                                      |
| `internal/orchestrator/orchestrator.go`      | Run log init, replace 7 commit sites, add 3 new CommitDialog, pendingDiff tracking, DecisionEdit+Comment handler |
| `internal/orchestrator/orchestrator_test.go` | 6 new tests                                                                                                      |
| `internal/agent/architect.go`                | `continueWithDiffTemplate`, `ContinueSessionWithDiff()`, diagnostic logging in `detectPlanRevision`              |
| `internal/tui/screen_pipeline.go`            | `ContentEditConfirm`, `pendingEditContent`, `handleEditConfirmKey()`, `viewEditConfirm()`                        |
| `internal/tui/model.go`                      | `editorReturnMsg` → show confirmation instead of immediate DecisionEdit. `ConfirmEditIntent` handling.           |
| `internal/tui/messages.go`                   | `ConfirmEditIntent` type                                                                                         |
| `internal/tui/screen_pipeline_test.go`       | 4 new tests                                                                                                      |
| `internal/testutil/doubles.go`               | `OnCall` callback on `FakeCall`                                                                                  |

## Decisions

- No metadata.json — dialog.md + git commit metadata is sufficient
- plan.md changes from both sides (architect + user Ctrl+E)
- After user Ctrl+E, architect receives plain unified diff (no ANSI, 3 context lines) in `<plan_changes>`
- Ctrl+E returns to confirmation prompt, not immediate DecisionEdit
- "No, continue editing" returns to plan review (Ctrl+E to re-enter), does not re-open editor
- Unchanged file after editor → skip prompt silently
- Logger as `*slog.Logger` parameter, not global
- Old `Commit()` kept as deprecated redirect during transition
- `detectPlanRevision` critic fix: skip echo suppression for critic flow specifically

## Scope

- **IN**: run.log, git 2-file structure (plan.md + dialog.md), all commit gaps, diff-to-architect, Ctrl+E confirmation flow, 17 test scenarios, FakeRunner OnCall
- **OUT**: TUI history browser, metadata.json, researcher/critic/worker agent changes, plan.Spec changes
