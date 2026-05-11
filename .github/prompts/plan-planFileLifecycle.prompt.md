# Plan: Fix Architect Feedback Loop — `plansDirectory` + git micro-repo housekeeping

E2E first. Three unknowns block the final design:

1. Does `--resume` update the plan file, or only the initial call?
2. Does `--settings '{"plansDirectory":"/absolute/path"}'` work outside project root?
3. Does `--settings` persist across `--resume`, or must it be re-passed?

We answer all three with one test, then chart the implementation from observed behavior.

---

## Phase 0 — E2E validation (blocks everything else)

New test `TestPlanFileLifecycle` in `internal/agent/architect_e2e_test.go`, build tag `e2e`. Uses `e2eArchitectRunner()` pattern from `internal/agent/architect_resume_test.go`:

1. Create temp dir (absolute path, outside project root)
2. Build runner with existing options + new `WithSettings(fmt.Sprintf('{"plansDirectory":"%s"}', tmpDir))`
3. `RunStreaming(ctx, researchDraft, systemPrompt, &buf)` → capture `sessionID`
4. Glob `tmpDir/*.md` — log count and filenames. Assert exactly 1 file. Read content, log first 200 chars
5. `RunContinue(ctx, sessionID, questionPrompt, &buf)` — glob again, log delta. Record whether file count changed, content changed, or new file appeared
6. `RunContinue(ctx, sessionID, revisionPrompt, &buf)` — glob again, log delta
7. Log a structured summary: `{initial_file_count, question_file_delta, revision_file_delta, initial_content_changed_on_question, initial_content_changed_on_revision}`

This test is purely observational — it logs behavior without asserting revision detection. The assertions are only: step 4 produces exactly 1 `.md` file, and no step errors.

`WithSettings` is trivially `WithExtraArgs("--settings", json)` — added to `internal/harness/claude_cli.go` next to `WithPermissionMode`.

**Relevant files**: `internal/agent/architect_e2e_test.go`, `internal/harness/claude_cli.go`

---

## Phase 1 — Git micro-repo housekeeping (depends on Phase 0 results)

Extend `GitRepo` in `internal/plan/gitrepo.go` — `NewGitRepo` already creates `plan-history/` at `{session.Path}/plan-history/`. Set `plansDirectory` to point here. Claude Code writes its opaque `.md` file directly into this git repo's working tree.

New method `Housekeep(commitMsg string) (revised bool, planContent string, err error)`:

- `git status --porcelain` in `plan-history/`
- Parse: filter to non-dotfile entries
- 0 dirty → return `(false, "", nil)`
- 1 dirty → `git add <filename>`, `git commit -m "<commitMsg>"`, read file, return `(true, content, nil)`
- \>1 dirty → return error

The caller (Architect or Orchestrator) runs `Housekeep` after every architect interaction. The dirty file keeps its original Claude Code name — no rename.

Pre-turn assertion: before calling `RunStreaming`/`RunContinue`, verify `git status --porcelain` is empty. If not, something leaked from a previous failed run — error out.

**Relevant files**: `internal/plan/gitrepo.go`, `internal/plan/gitrepo_test.go`

---

## Phase 2 — `ContinueSession` uses plan file as primary (depends on Phase 1)

Change `ContinueSession` in `internal/agent/architect.go` return to `(chatText string, revised *RawPlan, usage TokenUsage, err error)`:

- Calls `Housekeep("revision: <comment prefix>")` after `RunContinue`
- If `revised == true` → parse plan content, return as `*RawPlan`
- If `revised == false` → return stdout as `chatText`, nil `*RawPlan`

`parsePlanResult` relaxation in `internal/agent/architect.go`: if content starts with `## Goal` or `## Context` but not `# Plan`, prepend `# Plan\n\n`. Keep `## Work Packages` validation.

For initial plan (`Refine`/`RefineStreaming`): `Housekeep("initial plan")` after the run. The plan file content becomes primary; stdout is ignored for plan extraction. `parsePlanResultWithRecovery` and `recoverPlanFromSession` become dead code.

**Relevant files**: `internal/agent/architect.go`, `internal/agent/helpers.go`

---

## Phase 3 — Orchestrator wiring (depends on Phase 2)

In `internal/orchestrator/orchestrator.go` `DecisionComment` handler:

- Remove `exec.Command("git", ... "status", "--porcelain")` block (L641-654)
- Remove `os.ReadFile(planRepo.PlanPath())` recovery (L656-665)
- Use new `ContinueSession` return: `if revised != nil` → update `finalPlanMarkdown`, commit msg = `"revision: <first 60 chars>"`. `if revised == nil` → emit `EventChatResponse`
- Pass the `plansDirectory` setting when constructing the architect runner in `buildEngine()` — `WithSettings(fmt.Sprintf('{"plansDirectory":"%s"}', planRepo.Dir()))`

**stdout streaming for TUI**: `RunStreaming`/`RunContinue` still streams to `&streamWriter{buf: stream}` for live TUI rendering. The plan file is read after the stream completes. stdout = observability, plan file = truth.

**Relevant files**: `internal/orchestrator/orchestrator.go`, `cmd/orqestra/main.go`

---

## Phase 4 — AskUserQuestion guidance (parallel with everything)

Fill dangling `ASKING USER QUESTIONS:` in `internal/config/pipeline.yaml`:

- Researcher (L115): "Only when the codebase is genuinely ambiguous about what the user wants. Max 2 questions. Never ask about implementation approach."
- Architect (L179): "Only to clarify acceptance criteria or scope boundaries. Max 2 questions. Never ask HOW — decide."

---

## Verification

1. `go test ./internal/agent/... -tags e2e -run TestPlanFileLifecycle -v -timeout 300s` — Phase 0 passes, logs confirm plan file behavior
2. `go test ./internal/plan/... -run TestHousekeep` — 0/1/>1 dirty file cases
3. `go test ./internal/agent/... -run TestArchitect` — plan extraction from file, `## Goal` normalization
4. `go test ./internal/orchestrator/... -run TestEngine` — DecisionComment uses new return
5. `go build ./cmd/orqestra && go vet ./...`
6. Manual: `orqestra plan "validate readme"` completes architect step

## Decisions

- Keep original Claude Code filename — no rename to `plan.md`
- Commit messages carry user comment prefix
- `recoverPlanFromSession` (JSONL chain) becomes dead code — remove or keep for diagnostics
- `ResolveSessionLogPath` stays (used by TUI run-detail screen)
- Phase 0 results may change Phases 1-3 significantly — that's the point

## Constraints

- Do NOT change `CLIRunner` or `ContinuableRunner` interfaces.
- Do NOT change worker, researcher, or gateway logic.
- Do NOT add third-party dependencies.
- Do NOT remove `permission_mode: plan` from the architect.
- Follow TUI Elm architecture: no state mutation in `View()`, no blocking in `Update()`.
- No magic numbers in layout.

## Risks

1. **`--resume` may not update the plan file.** If Claude Code only writes the plan file on initial invocation, `Housekeep` will see 0 dirty files on revision turns. Mitigation: Phase 0 E2E test reveals this. Fallback: parse stdout for `# Plan` on continuation turns.
2. **`--settings` may not work with absolute paths outside project root.** Docs say "Path is relative to project root. Default: `~/.claude/plans`" but the default itself uses `~/`. Mitigation: Phase 0 tests this directly.
3. **`--settings` may not persist across `--resume`.** If `buildFinalArgs()` re-passes it on every invocation (it does — verified in source), this should work. Mitigation: Phase 0 logs file state across turns.
4. **Claude Code creates subdirectories inside plansDirectory.** If the plan file lands in a subdirectory rather than at the root, glob `*.md` misses it. Mitigation: use `**/*.md` glob in `readPlanFromDir`.
