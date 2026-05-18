# Plan: Unify plan-mode agent data flow

## TL;DR

Every planning stage (researcher, architect, critic) follows the same pattern:
`prompt + context.md → [plan-mode Claude] → output.md`. Replace the three
per-role structs with a single entity (working name: `Planner`) that wraps a
`ContinuableRunner` + system prompt and always reads output from the plan file.
Drop unreliable severity parsing. Fix critic to read from plan file (currently
reads stdout — data flow bug). Worker stays separate, uses runner directly.

## Design decisions

- **Always plan mode** — the entity has no mode flag. It IS the plan-mode wrapper.
  Worker doesn't use it.
- **Always `ContinuableRunner`** — no type assertions. All runner fields become
  `ContinuableRunner`. `LimitedRunner`, `wrapRunner`, and `NewClaudeCLIFromConfig`
  adapt their signatures.
- **Drop `parseSeverityCounts` / `BlockerSummary`** — regex is fragile
  (`**CRITICAL:**` vs `**Severity**: High`), can mislead with false-zero. Raw
  critic markdown passes through to the architect and TUI as-is.
- **Critic reads plan file** — critic is configured `permission_mode: plan` in
  pipeline.yaml but code uses `result.Output` (stdout). This is a data-flow bug.
  Unified entity fixes it: critic becomes another Planner, output comes from plan
  file like researcher and architect.
- **`RawPlan` stays** but simplified. `Warnings` field populated by caller via
  `CheckPlanHealth`, not by the entity. `*RawPlan` nil semantics for revision
  detection preserved.
- **Naming TBD** — working name `Planner`. User noted "maybe the thing I'm trying
  to model is not called Agent" since Worker is also an agent in product terms.
  Final name chosen at implementation time.

## Phase 1: Adapt runner interfaces (parallel with Phase 2)

1. **`internal/harness/claude_cli.go`** — Add `NewContinuableCLIFromConfig` that
   returns `(ContinuableRunner, error)` (same as `NewClaudeCLIFromConfig` but typed
   for the superset interface; `*ClaudeCLI` already satisfies it). Alternatively,
   change `NewClaudeCLIFromConfig` return type to `ContinuableRunner` since every
   concrete implementation (`ClaudeCLI`, `SandboxCLIRunner`) implements both.

2. **`internal/tokenlimit/runner.go`** — Change `LimitedRunner.inner` from
   `CLIRunner` to `ContinuableRunner`. Remove the type assertion in `RunContinue`.
   Update `NewLimitedRunner` signature.

3. **`cmd/orqestra/main.go`** — Change `wrapRunner` signature to
   `ContinuableRunner → ContinuableRunner`. Update `Runners` field assignments.

4. **`internal/orchestrator/orchestrator.go`** — Change `Runners` struct: all
   fields become `harness.ContinuableRunner`.

## Phase 2: Introduce Planner entity + prompt helpers

5. **Create `internal/agent/planner.go`** — the unified entity:

   ```
   Planner struct { runner ContinuableRunner; system string }
   NewPlanner(runner ContinuableRunner, system string) *Planner
   Run(ctx, prompt, stdout) → (PlanResult, error)
     - calls runner.RunStreaming(ctx, prompt, system, stdout)
     - calls ReadPlanFromRun(result) for authoritative output
     - returns PlanResult{Plan, Chat, Usage, SessionID}
   Continue(ctx, sessionID, prompt, stdout) → (PlanResult, error)
     - calls runner.RunContinue(ctx, sessionID, prompt, stdout)
     - calls ReadPlanFromRun(result) for authoritative output
     - returns PlanResult with Chat = result.Output (for gate responses)
   ```

   `PlanResult` type: `{ Plan string, Chat string, Usage TokenUsage, SessionID string }`
   - `Plan`: plan file content (authoritative output, always populated)
   - `Chat`: stream result text (for continuation chat/Q&A responses)

6. **Create `internal/agent/prompts.go`** — extract all prompt templates:
   - `ArchitectPrompt(userPrompt, researchFacts string) string`
   - `ArchitectRevisionPrompt(previousPlan, comments string) string`
   - `ContinuePrompt(currentPlan, comment string) string`
   - `ContinueDiffPrompt(currentPlan, diff, comment string) string`
   - `CriticContinuePrompt(currentPlan, criticReport string) string`
   - `CriticReviewPrompt(userPrompt, planMarkdown string) string`
   - Keep existing in spec.go: `BuildExecutionPromptFromPlan`, `WorkerValidationPrompt`, `CommitMessagePrompt`
   - **Canary check**: `CheckPromptIntegrity(assembled, originalPrompt string) (string, bool)`.
     Substring check: if `originalPrompt` does not appear verbatim in `assembled`,
     prepend it as `<original_prompt>...</original_prompt>` and return `true` (canary
     tripped). Orchestrator emits a warning event and logs it. TUI shows yellow
     indicator. This catches prompt template bugs and transfer chain corruption —
     NOT markdown parsing, just a substring invariant.

7. **Export `DetectPlanRevision` as package-level** — signature adapts to
   `PlanResult`:
   `DetectPlanRevision(planContent, baseline string, baselineErr error, currentPlan string) *RawPlan`

8. **Write tests** in `internal/agent/planner_test.go`:
   - Verify `Run` reads plan file from `ReadPlanFromRun`
   - Verify `Continue` reads plan file + populates Chat from stream
   - Error propagation (runner error, plan-file read error)
   - Canary check: prompt present → no-op; prompt missing → prepend + flag
   - Use `testutil.FakeRunner` + `testutil.SetupPlanFile`

## Phase 3: Migrate orchestrator call sites

**Canary invariant**: Every initial planner call (researcher, architect, critic)
must include `input.Prompt` verbatim in the assembled prompt. `CheckPromptIntegrity`
runs before each `.Run()`. If tripped: prompt is prepended, orchestrator emits
a warning event (yellow in TUI), run continues degraded-but-truthful.
Continuations skip the check -- original prompt is in Claude session context.

**Baseline snapshot**: For every architect continuation, the orchestrator reads
the plan file BEFORE calling Continue (moved from inside architect methods to
orchestrator -- data preserved, just relocated).

9. Researcher (~L590): NewPlanner + canary + .Run(ctx, prompt, stdout)
10. Architect initial (~L557): NewPlanner + canary on ArchitectPrompt(prompt, facts) + .Run + CheckPlanHealth
11. Critic (~L747): NewPlanner + canary on CriticReviewPrompt(prompt, plan) + .Run -- reads plan file (fixes bug)
12. Architect cont. critic (~L830): baseline snapshot + .Continue + DetectPlanRevision
13. Architect cont. comment (gate): baseline + .Continue + DetectPlanRevision. Chat from result.Chat
14. Architect cont. edit+diff (gate): baseline + .Continue + DetectPlanRevision
15. Cold-start fallback: NewPlanner.Run(ArchitectRevisionPrompt(plan, comment)) + canary
16. Worker + validator: no change, direct runner usage

## Phase 4: Delete old agent structs

17. **Delete `Researcher` struct** from researcher.go. File becomes empty or deleted.

18. **Delete `Architect` struct** from architect.go. Prompt templates already in
    prompts.go. `detectPlanRevision` becomes exported `DetectPlanRevision` and
    stays in architect.go (or moves to planner.go).

19. **Delete `Critic` struct, `BlockerSummary`, `CriticReport`, `parseSeverityCounts`**
    from critic.go. Critic report is just markdown — no wrapper type needed.

20. **Rewrite tests**:
    - researcher_test.go → planner_test.go (plan-mode run)
    - architect_test.go → continuation + revision detection tests
    - critic_test.go → delete severity parsing tests, add plan-mode critic test

21. **Update orchestrator tests** — `testEngineWithPlanFiles` helper uses
    `ContinuableRunner` in Runners struct. FakeRunner already implements
    ContinuableRunner.

## Phase 5: Cleanup (optional, separate PR)

22. Delete `Specification`, `PlanOutput`, `ProjectPlan`, `WorkPackage` if no live
    code path uses them.

23. Update CLAUDE.md / copilot-instructions.md references to `RawPlan`, agent
    structs, `ReadPlanFromRun` callers.

## Relevant files

**New:**

- `internal/agent/planner.go` — unified Planner entity + PlanResult
- `internal/agent/prompts.go` — prompt template functions

**Modified (agent):**

- `internal/agent/researcher.go` — delete Researcher struct
- `internal/agent/architect.go` — delete Architect struct, export DetectPlanRevision
- `internal/agent/critic.go` — delete everything (struct + severity parsing + types)
- `internal/agent/spec.go` — keep RawPlan, keep worker prompt helpers
- `internal/agent/plan_extract.go` — keep ReadPlanFromRun (called by Planner)
- `internal/agent/plancheck.go` — keep CheckPlanHealth (called by orchestrator)

**Modified (runner chain):**

- `internal/harness/claude_cli.go` — return type of NewClaudeCLIFromConfig → ContinuableRunner
- `internal/tokenlimit/runner.go` — inner field → ContinuableRunner, drop type assertion
- `cmd/orqestra/main.go` — wrapRunner signature, Runners construction

**Modified (orchestrator):**

- `internal/orchestrator/orchestrator.go` — Runners struct, all agent call sites
- `internal/orchestrator/orchestrator_test.go` — test helpers

**Modified (tests):**

- `internal/agent/*_test.go` — rewrite
- `internal/tui/app_test.go` — Runners field types

## Verification

1. `go build ./...`
2. `go test -race ./internal/agent/ -v`
3. `go test -race ./internal/harness/ -v`
4. `go test -race ./internal/tokenlimit/ -v`
5. `go test -race ./internal/orchestrator/ -v`
6. `go test -race ./... -v`
7. `make test`
8. Smoke: `./bin/orqestra --prompt "test" --auto-approve --config orqestra.yaml`

## Open question: Naming

Working name is `Planner`. The user notes Worker is also an "agent" in product
terms, so calling the plan-mode entity `Agent` creates a false dichotomy.
Options: `Planner`, `Stage`, `PlanSession`, `Turn`. To be decided at
implementation time.
