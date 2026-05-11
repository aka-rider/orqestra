# Plan: Conversational Plan Review (Revised)

## Goal

Replace the stateless cold re-prompt in the plan review gate with a persistent planner session, so the architect retains full research context across user interactions — questions, change requests, and plan revisions — during plan review.

## Context

### Planner type and runner wiring

- `Planner` struct in `internal/agent/planner.go` (line 19-22) holds `runner harness.CLIRunner`. `NewPlanner` (line 25) takes `harness.CLIRunner`.
- `Runners.Planner` in `internal/orchestrator/orchestrator.go` (line ~247) is typed `harness.CLIRunner`.
- `buildEngine` in `cmd/orqestra/main.go` assigns `plannerRunner` from `wrapRunner(...)` which returns `harness.CLIRunner`. Concrete type is `*tokenlimit.LimitedRunner`.
- `LimitedRunner.RunContinue` in `internal/tokenlimit/runner.go` (line 80) type-asserts `r.inner.(harness.ContinuableRunner)` — works because inner is `*ClaudeCLI`.
- `runPlanOnly` in `main.go` (line ~353) creates planner with raw `CLIRunner` from `NewClaudeCLIFromConfig`.
- `wrapRunner` (line 537) returns `harness.CLIRunner`, not `ContinuableRunner`. The type assertion to `ContinuableRunner` must happen AFTER `wrapRunner`.

### Gate loop structure (orchestrator.go)

- The plan gate loop starts at label `planGate:` (line ~545). On `DecisionComment`:
  ```go
  planner := agent.NewPlanner(e.Runners.Planner, e.Config.Planner)   // NEW planner every time
  revised, revisedUsage, revSessionID, err := planner.RefineWithCommentsStreaming(...)
  ```
  Cold re-prompt. The model has zero memory of its research or reasoning.
- `planSessionID` is declared inside the `{}` block containing research+planning (~line 497-539). It goes out of scope before `planGate:`.
- The `goto planGate` path (when `input.PlanFile != ""`) skips planning entirely — no session exists.

### RunContinue mechanics (claude_cli.go)

- `ClaudeCLI.RunContinue` (line ~316) uses: `--resume <sessionID> -p <prompt> --output-format stream-json --verbose --include-partial-messages`.
- It re-appends `c.extraArgs` which includes `--allowed-tools`, `--disallowed-tools`, `--permission-mode`.
- Returns `RunResult{Output, Usage, SessionID}` — same structure as `RunStreaming`.

### Plan extraction (planner.go)

- `parsePlanResultWithRecovery` (line ~96): tries stdout first (look for `# Plan` + `## Work Packages`), falls back to session JSONL side-channel (`~/.claude/plans/`).
- For chat-only responses (model answers a question without revising), stdout parsing fails AND the side-channel finds the OLD plan file (unchanged). `ContinueSession` must treat this as "no plan change" — not "error."

### TUI architecture (internal/tui/)

#### State machine

```
AppState (top-level):
  StatePrompt ──[Enter]──→ StatePipeline
  StatePipeline ──[N]──→ StatePrompt
  StatePipeline ──[Ctrl+R]──→ StateRunsList
  StateRunsList ──[Enter]──→ StateRunDetail
  StateRunDetail ──[Esc]──→ StateRunsList

ContentMode (within StatePipeline):
  ContentStreaming ──[EventGateRequest(GateGatewayCoach)]──→ ContentCoaching
  ContentCoaching ──[Enter]──→ ContentStreaming
  ContentStreaming ──[EventGateRequest(GatePlanApproval)]──→ ContentPlanReview
  ContentPlanReview ──[A]──→ ContentStreaming (approve)
  ContentPlanReview ──[E]──→ ContentPlanEdit
  ContentPlanReview ──[Enter+comment]──→ ContentStreaming (comment sent, planner runs)
  ContentPlanEdit ──[Ctrl+S]──→ ContentStreaming (edit sent)
  ContentPlanEdit ──[Esc]──→ ContentPlanReview (discard)
  ContentStreaming ──[EventComplete]──→ ContentCompletion
```

#### New states added by this plan

```
  ContentPlanReview ──[Enter+comment]──→ ContentStreaming
      ↓ (if planSessionID!="")
    ContinueSession returns:
      ├── *RawPlan non-nil ──→ EventPlanReady + EventGateRequest ──→ ContentPlanReview (plan updated, chat entry added)
      └── *RawPlan nil     ──→ EventChatResponse ──→ ContentPlanReview (chat answer shown, plan unchanged)

  ContentPlanReview ──[D]──→ ContentPlanDiff (if planDiff!="")
  ContentPlanDiff ──[Esc]──→ ContentPlanReview
  ContentPlanDiff ──[D]──→ ContentPlanReview
```

#### Key data flow

```
User types comment → Enter
  ↓
PipelineScreen.handlePlanReviewKey → CommentPlanIntent{Comment}
  ↓
Model.handlePipelineKey → decisions <- Decision{Type: DecisionComment, Comment}
  ↓ + returns waitForEvent(m.events)
orchestrator.run() gate loop receives DecisionComment
  ├── planSessionID != "" → planner.ContinueSession(sessionID, plan, comment, streamWriter)
  │     ├── *RawPlan != nil → update finalPlanMarkdown, continue (re-emits EventGateRequest)
  │     └── *RawPlan == nil → emit EventChatResponse{ChatText: response}, continue (re-emits EventGateRequest)
  └── planSessionID == "" → planner.RefineWithCommentsStreaming (cold fallback)
  ↓
TUI receives OrchestratorEventMsg via waitForEvent
  ↓
Model.Update → pipelineScreen.ApplyEvent
  ├── EventChatResponse → append ChatEntry{Role:"architect"}, restore ContentPlanReview + textarea
  └── EventGateRequest(GatePlanApproval) → update finalPlan, append ChatEntry if chatHistory non-empty
```

#### Existing message types (messages.go)

The intent pattern is established: screens set `PendingIntent` (a `tea.Msg`), parent `Model.handlePipelineKey` reads it, dispatches to orchestrator via `m.decisions`, and returns `waitForEvent(m.events)`. New intents are NOT needed — `CommentPlanIntent` already exists.

#### Existing test patterns (app_test.go, app_smoke_test.go)

- `testModel()` creates a `Model` with `noopRunner{}` (implements `CLIRunner` + `ContinuableRunner` via `RunContinue`).
- `sendRune(m, "a")` / `sendKey(m, tea.KeyEnter)` / `sendCtrl(m, 's')` helpers.
- `viewString(m)` extracts rendered content from `Model.View()`.
- `hydratedModels()` returns named models for all states — used for fuzz-render tests.
- `ApplyEvent(event, width)` is called directly in tests — no channels needed for unit tests.
- Smoke test: `TestLayout_AllStatesRenderWithoutPanic` renders all states at multiple terminal sizes.

#### Existing orchestrator test patterns (orchestrator_test.go)

- `mockRunner` implements `CLIRunner` + `ContinuableRunner`. Can return `output`, `sessionID`, `err`.
- `switchingMockRunner` returns different outputs on sequential calls. Uses `*int` counter.
- `testEngine(gateway, researcher, planner, worker, validation)` constructs an `Engine` with mock runners.
- Tests use `engine.Start(ctx, Input{...})` and loop over `channels.Events`, sending `Decisions` when gate events arrive.

### System prompt design

The planner system prompt (pipeline.yaml, `planner` section) instructs the model to produce output starting with `# Plan`. The current system prompt:

- Describes the role as "Principal Engineer"
- Defines a one-shot workflow: read draft → spot-check → restructure → output
- Has no awareness of conversational review — no concept of "the reviewer may ask questions or request changes"
- Has no concept of partial output (chat-only vs. plan revision)

**Required changes for session continuation:**

1. Add a `CONVERSATION REVIEW` section to the system prompt that tells the model:
   - After the initial plan, the human reviewer may ask questions or request changes via `--resume`
   - When answering questions: respond conversationally, do NOT output `# Plan`
   - When revising: output the COMPLETE revised plan starting with `# Plan`
   - The continuation sub-prompt will include `<current_plan>` and `<reviewer_message>` tags
2. The `continuePromptTemplate` (WP2) works in tandem with this — the system prompt sets expectations, the sub-prompt provides structure

**For the continuation sub-prompt**, the follow-up must:

1. Include the current plan as ground truth (the model may have drifted from its own conversation history across `--resume` boundaries).
2. Not re-state the system prompt (it's preserved by `--resume`).
3. Distinguish between "answer a question" and "revise the plan" by output structure, not by classifying user intent. The model decides.

### Rename: planner → architect

The codebase uses "planner" for the agent that produces implementation plans, but the user-facing name is "architect" (visible in the TUI sidebar, chat history, and the plan review conversation). This rename aligns the internal nomenclature with the user-facing identity.

**Scope of rename:**

| Location                                | Current                                                      | New                                                                 |
| --------------------------------------- | ------------------------------------------------------------ | ------------------------------------------------------------------- |
| `internal/agent/planner.go`             | file, `Planner` type, `NewPlanner`                           | `architect.go`, `Architect`, `NewArchitect`                         |
| `internal/agent/planner_test.go`        | file, `plannerMockCLIRunner`                                 | `architect_test.go`, `architectMockCLIRunner`                       |
| `internal/config/config.go`             | `PlannerConfig`, `Planner PlannerConfig`, `PlannerAttempts`  | `ArchitectConfig`, `Architect ArchitectConfig`, `ArchitectAttempts` |
| `internal/config/pipeline.yaml`         | `planner:` section, `planner_attempts:`                      | `architect:` section, `architect_attempts:`                         |
| `internal/orchestrator/orchestrator.go` | `Runners.Planner`, agent ID `"planner"`, `buildPlannerInput` | `Runners.Architect`, `"architect"`, `buildArchitectInput`           |
| `internal/tui/screen_prompt.go`         | `" ○ planner"`                                               | `" ○ architect"`                                                    |
| `internal/tui/app_test.go`              | `Planner:`, `"planner"` string refs                          | `Architect:`, `"architect"`                                         |
| `internal/tokenlimit/` tests            | `"planner"` string refs                                      | `"architect"`                                                       |
| `internal/agent/session.go`             | `agentOrder` includes `"planner"`                            | `"architect"`                                                       |
| `cmd/orqestra/main.go`                  | `plannerRunner`, `cfg.Planner` refs                          | `architectRunner`, `cfg.Architect`                                  |

**YAML backward compat:** `internal/config/config.go` already has precedent for key migration (validator → planner). Add the same pattern: if `planner:` key is present and `architect:` is absent, promote it. Log a deprecation warning.

### Plan versioning: git micro-repo

Every plan mutation — initial plan, architect revision, external editor save, in-TUI edit — is committed to a bare git repository inside the session directory. This gives us:

- **Diff**: `git diff --no-color HEAD~1 HEAD -- plan.md` produces a real unified diff. No custom diff algorithm.
- **History**: `git log --oneline -- plan.md` shows the full revision trail.
- **100% coverage**: external editor edits get committed too, so `[D]` always works.
- **No `previousPlan` string field**: git IS the history.

**Repo location**: `{session.Path}/plan-history/` (dedicated subdirectory, doesn't pollute session artifacts).

**Lifecycle**:

```
Orchestrator creates session dir
  → plan.NewGitRepo(session.Path)  // git init + config user
  → planRepo.Commit(initialPlan, "initial plan from architect")

DecisionComment → ContinueSession returns revised plan
  → planRepo.Commit(revised, "revision: <comment prefix>")
  → planDiff, _ = planRepo.Diff()   // git diff HEAD~1 HEAD
  → emit(EventGateRequest{PlanDiff: planDiff})

DecisionEdit (in-TUI edit or external editor)
  → planRepo.Commit(edited, "manual edit")
  → planDiff, _ = planRepo.Diff()
  → emit(EventGateRequest{PlanDiff: planDiff})
```

**Type** (`plan/gitrepo.go`):

```go
type GitRepo struct {
    dir string // absolute path to plan-history/ subdirectory
}

func NewGitRepo(sessionPath string) (*GitRepo, error)
func (r *GitRepo) Commit(markdown, message string) error
func (r *GitRepo) Diff() (string, error)       // HEAD~1..HEAD, returns "" if < 2 commits
func (r *GitRepo) HasHistory() bool             // rev-list --count HEAD > 1
func (r *GitRepo) Log() (string, error)         // oneline log
```

**Edge cases**:

- Single commit (initial plan, no revisions yet): `HasHistory()` returns false, `Diff()` returns `""`. The `[D]` key is hidden.
- `git` not installed: `NewGitRepo` returns error. Orchestrator logs warning. `planRepo` stays `nil`. All commit/diff calls are no-ops. Diff view is disabled (graceful degradation).
- The micro-repo is NEVER pushed, branched, or merged. It's a local append-only commit log.

**TUI data flow**: the orchestrator computes the diff string and sends it in `EventGateRequest.PlanDiff`. The TUI stores `planDiff string` and renders it. Zero IO in the TUI — pure Elm.

## Constraints

- Do NOT change `CLIRunner` or `ContinuableRunner` interfaces.
- Do NOT change worker, researcher, or gateway logic.
- Do NOT add third-party dependencies.
- Do NOT classify user intent — one codepath, model decides by output structure.
- The plan review must work identically when `input.PlanFile != ""` (no architect session) — fall back to cold `RefineWithCommentsStreaming`.
- Follow TUI Elm architecture: no state mutation in `View()`, no blocking in `Update()`, all viewport mutations in `Update()`.
- No magic numbers in layout. New input zone heights use named constants.

## Risks

1. **`--resume` may not preserve tool restrictions**. If the resumed session gets Write access, the model could modify files. Mitigation: `ClaudeCLI.RunContinue` appends `c.extraArgs` on every invocation (verified in source). The E2E test (WP6) validates by inspecting JSONL.
2. **Side-channel plan file may not update on `--resume` turns**. Mitigation: `parsePlanResultWithRecovery` tries stdout first — if the model outputs the full plan, the side-channel is not needed. If both fail, `ContinueSession` treats it as chat-only.
3. **`planSessionID` scoping**. Moving to function scope means `goto planGate` path leaves it `""`. Gate loop checks for empty ID and falls back.
4. **System prompt drift on resumed sessions**. The `--resume` flag may not re-apply the system prompt from the original invocation. Mitigation: the continuation sub-prompt includes the current plan as ground truth. The E2E test validates the model produces coherent responses.

## Work Packages

### 1. Validate `--resume` behavior with a minimal prototype

**Rationale:** Before changing the planner type system, orchestrator, or TUI, confirm that `--resume` actually works for the planner's use case — that tool restrictions survive, that the model can produce both chat-only and plan-revision responses, and that `parsePlanResultWithRecovery` handles `RunContinue` output.

**Steps:**

1. Create `internal/agent/planner_resume_test.go` with build tag `//go:build e2e`.

2. Add test `TestResume_ToolRestrictionsPreserved`:
   - Construct a `*ClaudeCLI` with `WithAllowedTools([]string{"Read","Grep","Glob","Bash"})`, `WithDisallowedTools([]string{"ExitPlanMode"})`, `WithPermissionMode("plan")`.
   - Call `RunStreaming(ctx, "List the files in the current directory", "You are a test agent.", &buf)`. Capture `sessionID`.
   - Call `RunContinue(ctx, sessionID, "What tools do you have available? List them all.", &buf)`.
   - Assert `err == nil`.
   - Use `harness.ResolveSessionLogPath(cwd, sessionID)` to find the JSONL file.
   - Read the JSONL. Scan for `"type":"user"` lines — should be exactly 2.
   - Log whether the model's response mentions Write/Edit tools (informational — the hard constraint is in `extraArgs`).

3. Add test `TestResume_ChatOnlyResponse`:
   - Same runner setup.
   - First call: `RunStreaming(ctx, trivialPrompt, plannerSystemPrompt, &buf)` — produce a plan. Capture `sessionID`.
   - Parse the plan with `parsePlanResult` to confirm it's valid.
   - Second call: `RunContinue(ctx, sessionID, continuePrompt, &buf)` where `continuePrompt` asks "Why did you choose this approach?" with the `<current_plan>` / `<reviewer_message>` structure.
   - Attempt `parsePlanResult` on the result — expect it to fail (no `# Plan` in output).
   - Assert `result.Output` is non-empty and contains substantive text.

4. Add test `TestResume_PlanRevisionResponse`:
   - Same runner setup.
   - First call: produce a plan. Capture `sessionID`.
   - Second call: `RunContinue(ctx, sessionID, revisionPrompt, &buf)` where `revisionPrompt` asks "Remove the first work package entirely and output the complete revised plan starting with '# Plan'."
   - Parse the result with `parsePlanResult` — expect it to succeed.
   - Assert the revised plan differs from the original.

**Done when:**

- `go test ./internal/agent/... -tags e2e -run TestResume -v -timeout 300s` passes.
- Test output logs: JSONL user turn count, tool registration status, and whether chat-only vs. plan-revision distinction works.

### 2. Iterate on the continuation sub-prompt

**Rationale:** The follow-up prompt wording determines whether the model correctly distinguishes "answer question" from "revise plan." This must be tested against the real model before hardcoding.

**Steps:**

1. In `internal/agent/planner_resume_test.go`, add `TestContinuePrompt_Variants` (build tag `e2e`):
   - Define the candidate continuation prompt template:

     ```
     The current implementation plan is below. The reviewer sent a message.

     <current_plan>
     {currentPlan}
     </current_plan>

     <reviewer_message>
     {userMessage}
     </reviewer_message>

     If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
     If the reviewer requests changes, revise the plan and output the complete updated plan.
     Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".
     ```

   - Sub-test `QuestionGetsAnswer`: use reviewer_message `"Why did you choose to put step 3 before step 4?"`. Assert output does NOT contain `# Plan`.
   - Sub-test `ChangeRequestGetsPlan`: use reviewer_message `"Remove work package 1 and renumber the remaining packages"`. Assert output DOES contain `# Plan` and `## Work Packages`.
   - Sub-test `AmbiguousRequestStillWorks`: use reviewer_message `"The verification section is weak"`. Accept either outcome — the model might answer or revise. Assert `err == nil` and `result.Output != ""`.

2. If the model fails to distinguish correctly, adjust the prompt template wording. Candidates:
   - Add `"Do NOT output '# Plan' unless you actually changed the plan."` (stronger negative constraint).
   - Add `"If you only answered a question, end your response without a plan section."` (explicit termination instruction).

3. Once a prompt template passes all sub-tests consistently (2/2 required, 3rd is informational), freeze it as the `continuePromptTemplate` constant in `internal/agent/planner.go`.

**Done when:**

- `go test ./internal/agent/... -tags e2e -run TestContinuePrompt -v -timeout 300s` passes.
- The frozen prompt template is committed as a `const` in `planner.go`.
- At least 2 out of 3 sub-tests pass. The third is logged but not required.

### 3. Update system prompt for conversational review

**Rationale:** The current system prompt assumes a one-shot workflow. Session continuation requires the model to understand it may be asked follow-up questions or revision requests — and to distinguish between them by output structure.

**Steps:**

1. In `internal/config/pipeline.yaml`, in the `planner:` → `system_prompt:` block, append a `CONVERSATION REVIEW` section after the `RULES:` block (before the closing `|`):

   ```yaml
       CONVERSATION REVIEW:
       After producing the initial plan, the human reviewer may send follow-up
       messages via session continuation. When this happens:

       - If the reviewer asks a QUESTION (e.g. "Why did you choose X?",
         "What happens if Y?"), answer conversationally. Do NOT output
         "# Plan" or any plan structure. Just answer.
       - If the reviewer requests a CHANGE (e.g. "Remove WP1", "Add error
         handling to step 3", "Split this into two packages"), revise the
         plan and output the COMPLETE updated plan starting with "# Plan".
         Output the entire plan, not just the changed parts — the system
         replaces the previous version wholesale.
       - You do NOT need to classify the reviewer's intent. Let your output
         structure speak: if you changed the plan, output "# Plan". If you
         didn't, don't.
       - The continuation prompt will include <current_plan> and
         <reviewer_message> tags. The <current_plan> is the ground truth —
         it may differ from what you remember if the reviewer edited it
         externally.
   ```

2. In the same `system_prompt:`, update the `YOUR WORKFLOW:` section item 6 from:

   ```
   6. Produce the final plan as a markdown document.
   ```

   to:

   ```
   6. Produce the final plan as a markdown document.
   7. If the reviewer sends follow-up messages, handle them per the
      CONVERSATION REVIEW rules above.
   ```

3. In the `researcher:` → `system_prompt:`, update the reference from "The planner will restructure it" to "The architect will restructure it" on the last line.

**Done when:**

- `cat internal/config/pipeline.yaml | grep -c "CONVERSATION REVIEW"` returns 1.
- `cat internal/config/pipeline.yaml | grep -c "architect will restructure"` returns 1.
- `go build ./cmd/orqestra` compiles (embedded YAML is valid).
- `go test ./internal/config/...` passes.

### 4. Rename planner → architect across the codebase

**Rationale:** The user-facing name is "architect" (TUI sidebar, chat history). Aligning the internal name eliminates the cognitive split.

**Steps:**

1. Rename files:
   - `mv internal/agent/planner.go internal/agent/architect.go`
   - `mv internal/agent/planner_test.go internal/agent/architect_test.go`
   - `mv internal/agent/planner_resume_test.go internal/agent/architect_resume_test.go` (created in WP1)

2. In `internal/agent/architect.go`:
   - Rename type `Planner` → `Architect` (all methods: `Refine`, `RefineStreaming`, `RefineWithComments`, `RefineWithCommentsStreaming`, receiver `(p *Planner)` → `(a *Architect)`).
   - Rename `NewPlanner` → `NewArchitect`.
   - Rename `runner harness.CLIRunner` field — no change to field name, just ensure it compiles.

3. In `internal/agent/architect_test.go`:
   - Rename `plannerMockCLIRunner` → `architectMockCLIRunner`.
   - Update all `NewPlanner` calls to `NewArchitect`.
   - Update all `Planner` type references to `Architect`.

4. In `internal/config/config.go`:
   - Rename `PlannerConfig` → `ArchitectConfig`.
   - Rename `Planner PlannerConfig` → `Architect ArchitectConfig` in `Config` struct. Update yaml tag: `yaml:"architect"`.
   - Rename `PlannerAttempts` → `ArchitectAttempts` in `RetryConfig`. Update yaml tag: `yaml:"architect_attempts"`.
   - Add backward-compat migration: if raw YAML contains `planner:` key and no `architect:` key, promote `planner` → `architect`. Follow the existing pattern used for the `validator` → `planner` migration. Log `slog.Warn("deprecated config key 'planner:' — use 'architect:'")`.

5. In `internal/config/pipeline.yaml`:
   - Rename `planner:` section to `architect:`.
   - Rename `planner_attempts:` to `architect_attempts:`.

6. In `internal/orchestrator/orchestrator.go`:
   - Rename `Runners.Planner` → `Runners.Architect` (type stays `harness.CLIRunner` for now — WP5 changes it to `ContinuableRunner`).
   - Rename all `AgentID: "planner"` → `AgentID: "architect"`.
   - Rename `buildPlannerInput` → `buildArchitectInput`.
   - Rename local `planner :=` variables to `architect :=`.
   - Update `plannerAttempts` → `architectAttempts`, `e.Config.Planner` → `e.Config.Architect`, `e.Config.Retry.PlannerAttempts` → `e.Config.Retry.ArchitectAttempts`.

7. In `internal/tui/screen_prompt.go`:
   - Change `" ○ planner"` → `" ○ architect"` (line 164).

8. In `internal/tui/app_test.go`:
   - Update `Planner:` struct field refs → `Architect:`.
   - Update `config.PlannerConfig{}` → `config.ArchitectConfig{}`.
   - Update all `"planner"` string literals → `"architect"`.

9. In `internal/agent/session.go`:
   - Update `agentOrder` to include `"architect"` instead of `"planner"`.

10. In `internal/tokenlimit/runner_test.go` and `store_test.go`:
    - Update `"planner"` string literals → `"architect"`.

11. In `cmd/orqestra/main.go`:
    - Rename `plannerRunner` → `architectRunner`.
    - Update `cfg.Planner` → `cfg.Architect`.
    - Update `"planner"` agent ID strings.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- `go vet ./...` passes.
- `grep -r '"planner"' internal/ cmd/ | grep -v _test.go | grep -v '.md'` returns no matches (all production code renamed).
- `go test ./internal/agent/... -run TestArchitect` passes.
- `go test ./internal/config/...` passes (including backward-compat for old `planner:` YAML key).
- `go test ./internal/orchestrator/...` passes.
- `go test ./internal/tui/...` passes.
- `go test ./internal/tokenlimit/...` passes.

### 5. Change Architect runner type to ContinuableRunner and add ContinueSession method

**Steps:**

1. In `internal/agent/architect.go`, change `runner harness.CLIRunner` → `runner harness.ContinuableRunner`.

2. In `internal/agent/architect.go`, change `NewArchitect` parameter: `func NewArchitect(runner harness.CLIRunner, cfg config.ArchitectConfig)` → `func NewArchitect(runner harness.ContinuableRunner, cfg config.ArchitectConfig)`.

3. In `internal/orchestrator/orchestrator.go`, in the `Runners` struct, change `Architect harness.CLIRunner` → `Architect harness.ContinuableRunner`.

4. In `cmd/orqestra/main.go` `buildEngine` function, after `architectRunner = wrapRunner(architectRunner, limiter, cfg, cfg.Architect.Model, "architect")`:

   ```go
   continuableArchitect, ok := architectRunner.(harness.ContinuableRunner)
   if !ok {
       slog.Error("architect runner does not support session continuation")
       os.Exit(exitInvalidInput)
   }
   ```

   Use `continuableArchitect` when assigning `Runners.Architect` in the `Engine` construction below.

5. In `cmd/orqestra/main.go` `runPlanOnly` function, after `architectRunner` is created from `NewClaudeCLIFromConfig`:

   ```go
   continuableArchitect, ok := architectRunner.(harness.ContinuableRunner)
   if !ok {
       slog.Error("architect runner does not support session continuation")
       os.Exit(exitInvalidInput)
   }
   architect := agent.NewArchitect(continuableArchitect, cfg.Architect)
   ```

6. Add `continuePromptTemplate` constant to `internal/agent/architect.go` (the frozen template from WP2):

   ```go
   const continuePromptTemplate = `The current implementation plan is below. The reviewer sent a message.

   <current_plan>
   %s
   </current_plan>

   <reviewer_message>
   %s
   </reviewer_message>

   If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
   If the reviewer requests changes, revise the plan and output the complete updated plan.
   Begin with your response. Then, ONLY if you changed the plan, output the full revised plan starting with "# Plan".`
   ```

7. Add `ContinueSession` method to `Architect`:

   ```go
   // ContinueSession resumes the architect's Claude session with a user message.
   // Returns:
   //   - plan: non-nil if the model produced a revised plan, nil if chat-only
   //   - response: the model's full text output
   //   - usage: token consumption for this turn
   //   - err: harness errors (NOT parse errors — those mean "no plan change")
   func (a *Architect) ContinueSession(ctx context.Context, sessionID, currentPlan, userMessage string, stdout io.Writer) (*RawPlan, string, harness.TokenUsage, error) {
       prompt := fmt.Sprintf(continuePromptTemplate, currentPlan, userMessage)
       result, err := a.runner.RunContinue(ctx, sessionID, prompt, stdout)
       if err != nil {
           return nil, "", harness.TokenUsage{}, fmt.Errorf("architect continue session: %w", err)
       }
       plan, usage, _, parseErr := a.parsePlanResultWithRecovery(result)
       if parseErr != nil {
           // Parse failure is expected for chat-only responses
           return nil, result.Output, result.Usage, nil
       }
       return &plan, result.Output, usage, nil
   }
   ```

8. Update existing mock in `internal/agent/architect_test.go`: `architectMockCLIRunner` must implement `ContinuableRunner`. Add:

   ```go
   func (m *architectMockCLIRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
       if m.err != nil {
           return harness.RunResult{}, m.err
       }
       return harness.RunResult{Output: m.response, SessionID: m.sessionID}, nil
   }
   ```

   Verify all existing `TestArchitect_*` tests still pass.

9. Add new tests in `internal/agent/architect_test.go`:
   - `TestContinueSession_PlanRevised`: mock returns output containing `# Plan\n\n## Goal\n...\n\n## Work Packages\n...`. Assert `*RawPlan` non-nil, `plan.Markdown` starts with `# Plan`, `response` is the full output, `err` is nil.
   - `TestContinueSession_ChatOnly`: mock returns `"The first work package was designed that way because the config parser must be initialized before the resolver can run."`. Assert `*RawPlan` is nil, `response` contains the text, `err` is nil.
   - `TestContinueSession_HarnessError`: mock returns `fmt.Errorf("connection refused")`. Assert `err != nil`, `*RawPlan` is nil.
   - `TestContinueSession_PromptContainsPlanAndMessage`: add a `capturedPrompt string` field to the mock, capture the prompt in `RunContinue`. Assert the prompt contains both `<current_plan>` and `<reviewer_message>` tags with the provided values.

10. Update `internal/orchestrator/orchestrator_test.go`: the `mockRunner` already implements `RunContinue`. Verify `testEngine` still compiles with the new `Runners.Architect` type (it should — `*mockRunner` implements `ContinuableRunner`).

11. Update `internal/tui/app_test.go`: the `noopRunner` already implements `RunContinue`. Verify `testModel()` still compiles.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- `go test ./internal/agent/... -run TestArchitect` passes (all existing + new tests).
- `go test ./internal/agent/... -run TestContinueSession` passes with all four cases.
- `go test ./internal/orchestrator/...` passes (all existing tests).
- `go test ./internal/tui/...` passes (all existing tests).

### 6. Plan version history via git micro-repo (`plan/gitrepo.go`)

**Rationale:** Every plan mutation — architect revision, external editor save, in-TUI edit — must produce a diffable version. A git repo inside the session dir gives us real unified diffs with zero custom algorithms, and covers 100% of mutation paths.

**Steps:**

1. Create `internal/plan/gitrepo.go`:

   ```go
   package plan

   import (
       "fmt"
       "os"
       "os/exec"
       "path/filepath"
       "strconv"
       "strings"
   )

   // GitRepo is a single-file git repository for tracking plan versions.
   // It lives inside the session directory and is never pushed or branched.
   type GitRepo struct {
       dir string // absolute path to plan-history/ subdirectory
   }

   // NewGitRepo initializes a git repo at {sessionPath}/plan-history/.
   // Returns an error if git is not available or init fails.
   func NewGitRepo(sessionPath string) (*GitRepo, error) {
       dir := filepath.Join(sessionPath, "plan-history")
       if err := os.MkdirAll(dir, 0o755); err != nil {
           return nil, fmt.Errorf("create plan-history dir: %w", err)
       }
       if err := run(dir, "git", "init", "--quiet"); err != nil {
           return nil, fmt.Errorf("git init: %w", err)
       }
       if err := run(dir, "git", "config", "user.name", "orqestra"); err != nil {
           return nil, fmt.Errorf("git config user.name: %w", err)
       }
       if err := run(dir, "git", "config", "user.email", "plan@orqestra.local"); err != nil {
           return nil, fmt.Errorf("git config user.email: %w", err)
       }
       return &GitRepo{dir: dir}, nil
   }

   // Commit writes the plan markdown to plan.md, stages, and commits.
   func (r *GitRepo) Commit(markdown, message string) error {
       planPath := filepath.Join(r.dir, "plan.md")
       if err := os.WriteFile(planPath, []byte(markdown), 0o644); err != nil {
           return fmt.Errorf("write plan.md: %w", err)
       }
       if err := run(r.dir, "git", "add", "plan.md"); err != nil {
           return fmt.Errorf("git add: %w", err)
       }
       // --allow-empty handles the case where the content is identical (no-op edit)
       if err := run(r.dir, "git", "commit", "--quiet", "-m", message, "--allow-empty"); err != nil {
           return fmt.Errorf("git commit: %w", err)
       }
       return nil
   }

   // Diff returns the unified diff between the previous and current plan version.
   // Returns "" if there is only one commit (no previous version).
   func (r *GitRepo) Diff() (string, error) {
       if !r.HasHistory() {
           return "", nil
       }
       out, err := exec.Command("git", "-C", r.dir, "diff", "--no-color", "HEAD~1", "HEAD", "--", "plan.md").Output()
       if err != nil {
           return "", fmt.Errorf("git diff: %w", err)
       }
       return string(out), nil
   }

   // HasHistory returns true if there are at least 2 commits (a diff is available).
   func (r *GitRepo) HasHistory() bool {
       out, err := exec.Command("git", "-C", r.dir, "rev-list", "--count", "HEAD").Output()
       if err != nil {
           return false
       }
       count, _ := strconv.Atoi(strings.TrimSpace(string(out)))
       return count > 1
   }

   // Log returns the oneline commit log for plan.md.
   func (r *GitRepo) Log() (string, error) {
       out, err := exec.Command("git", "-C", r.dir, "log", "--oneline", "--", "plan.md").Output()
       if err != nil {
           return "", fmt.Errorf("git log: %w", err)
       }
       return string(out), nil
   }

   func run(dir string, name string, args ...string) error {
       cmd := exec.Command(name, args...)
       cmd.Dir = dir
       cmd.Stdout = nil
       cmd.Stderr = nil
       return cmd.Run()
   }
   ```

2. Create `internal/plan/gitrepo_test.go`:

   ```go
   func TestGitRepo_CommitAndDiff(t *testing.T) {
       repo, err := NewGitRepo(t.TempDir())
       if err != nil { t.Fatalf("NewGitRepo: %v", err) }

       err = repo.Commit("# Plan\n\n## Goal\nOriginal.\n", "initial plan")
       if err != nil { t.Fatalf("first commit: %v", err) }

       if repo.HasHistory() {
           t.Error("HasHistory should be false after 1 commit")
       }
       diff, _ := repo.Diff()
       if diff != "" {
           t.Error("Diff should be empty after 1 commit")
       }

       err = repo.Commit("# Plan\n\n## Goal\nRevised.\n", "revision")
       if err != nil { t.Fatalf("second commit: %v", err) }

       if !repo.HasHistory() {
           t.Error("HasHistory should be true after 2 commits")
       }
       diff, err = repo.Diff()
       if err != nil { t.Fatalf("Diff error: %v", err) }
       if !strings.Contains(diff, "-Original.") || !strings.Contains(diff, "+Revised.") {
           t.Errorf("unexpected diff:\n%s", diff)
       }
   }

   func TestGitRepo_ThreeCommits_DiffShowsLatest(t *testing.T) {
       repo, _ := NewGitRepo(t.TempDir())
       repo.Commit("v1", "first")
       repo.Commit("v2", "second")
       repo.Commit("v3", "third")
       diff, _ := repo.Diff()
       if !strings.Contains(diff, "-v2") || !strings.Contains(diff, "+v3") {
           t.Errorf("diff should show v2→v3, got:\n%s", diff)
       }
   }

   func TestGitRepo_IdenticalCommit(t *testing.T) {
       repo, _ := NewGitRepo(t.TempDir())
       repo.Commit("same", "first")
       err := repo.Commit("same", "second") // --allow-empty
       if err != nil { t.Fatalf("identical commit should not error: %v", err) }
   }

   func TestGitRepo_Log(t *testing.T) {
       repo, _ := NewGitRepo(t.TempDir())
       repo.Commit("v1", "initial plan")
       repo.Commit("v2", "revision: remove WP1")
       log, _ := repo.Log()
       if !strings.Contains(log, "initial plan") || !strings.Contains(log, "revision: remove WP1") {
           t.Errorf("log missing entries:\n%s", log)
       }
   }
   ```

**Done when:**

- `go test ./internal/plan/... -run TestGitRepo` passes all 4 tests.

### 7. Wire session continuity and plan versioning into the orchestrator gate loop

**Steps:**

1. In `internal/orchestrator/orchestrator.go`, add `EventChatResponse` to the `EventType` constants (after `EventRunDirReady`):

   ```go
   EventRunDirReady   // emitted once after session dir is created
   EventChatResponse  // emitted when architect answers without revising the plan
   ```

2. Add fields to the `Event` struct:

   ```go
   type Event struct {
       // ... existing fields ...
       ChatText string // set on EventChatResponse
   }
   ```

3. Add `PlanDiff string` field to `GateRequest`:

   ```go
   type GateRequest struct {
       // ... existing fields ...
       PlanDiff string // unified diff from git micro-repo (empty if no history)
   }
   ```

4. In the `run()` function, move `var planSessionID string` from inside the `{}` block to function scope, next to `var finalPlanMarkdown string`. Initialize to `""`.

5. Move the planner construction out of the `{}` block to function scope. The same instance is reused in the gate loop.

6. Remove the inner `var planSessionID string` declaration. The existing assignment writes to the outer variable.

7. After the session dir is created and before the planning phase, initialize the git micro-repo:

   ```go
   var planRepo *plan.GitRepo
   if session.Path != "" {
       var repoErr error
       planRepo, repoErr = plan.NewGitRepo(session.Path)
       if repoErr != nil {
           slog.Warn("plan history unavailable — diff disabled", "err", repoErr)
       }
   }
   ```

8. After `finalPlanMarkdown = plan.Markdown` (initial plan set), commit to the repo:

   ```go
   if planRepo != nil {
       if err := planRepo.Commit(finalPlanMarkdown, "initial plan from architect"); err != nil {
           slog.Warn("plan commit failed", "err", err)
       }
   }
   ```

9. In the gate loop, compute `planDiff` before emitting `EventGateRequest`:

   ```go
   var planDiff string
   if planRepo != nil {
       planDiff, _ = planRepo.Diff()
   }
   writeArtifact(session, "final_plan.md", finalPlanMarkdown)
   emit(Event{Type: EventGateRequest, Gate: GateRequest{
       Type:              GatePlanApproval,
       FinalPlanMarkdown: finalPlanMarkdown,
       PlanFilePath:      session.ArtifactPath("final_plan.md"),
       PlanDiff:          planDiff,
   }})
   ```

10. In the `DecisionEdit` handler, commit the edit before `continue`:

    ```go
    case DecisionEdit:
        edited := strings.TrimSpace(decision.EditedContent)
        if !strings.HasPrefix(edited, "# Plan") {
            emit(Event{Type: EventError, Err: fmt.Errorf("edited plan must start with '# Plan'")})
            return
        }
        finalPlanMarkdown = edited
        if planRepo != nil {
            if err := planRepo.Commit(edited, "manual edit"); err != nil {
                slog.Warn("plan commit failed", "err", err)
            }
        }
        continue
    ```

11. Replace the `DecisionComment` handler:

    ```go
    case DecisionComment:
        emit(Event{Type: EventAgentStarted, AgentID: "architect"})
        stream.SetAgent("architect")
        revStart := time.Now()

        var revisedPlan *agent.RawPlan
        var chatResponse string
        var revisedUsage harness.TokenUsage
        var revSessionID string
        var err error

        if planSessionID != "" {
            revisedPlan, chatResponse, revisedUsage, err = architect.ContinueSession(
                ctx, planSessionID, finalPlanMarkdown, decision.Comment, &streamWriter{buf: stream})
            revSessionID = planSessionID
        } else {
            var plan agent.RawPlan
            plan, revisedUsage, revSessionID, err = architect.RefineWithCommentsStreaming(
                ctx, finalPlanMarkdown, decision.Comment, &streamWriter{buf: stream})
            if err == nil {
                revisedPlan = &plan
            }
        }

        if err != nil {
            writeArtifactJSON(session, "architect_revision_meta.json", stepMeta{
                AgentID: "architect", ModelRef: e.Config.Architect.Model,
                StartTime: revStart, EndTime: time.Now(),
                ClaudeSessionID: revSessionID, Status: "failed", Error: err.Error(),
            })
            emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: err})
            emit(Event{Type: EventError, Err: fmt.Errorf("architect revision: %w", err)})
            return
        }

        writeArtifactJSON(session, "architect_revision_meta.json", stepMeta{
            AgentID: "architect", ModelRef: e.Config.Architect.Model,
            StartTime: revStart, EndTime: time.Now(),
            ClaudeSessionID: revSessionID, Status: "done",
            InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens,
        })
        emit(Event{Type: EventAgentDone, AgentID: "architect",
            InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens})

        if revisedPlan != nil {
            finalPlanMarkdown = revisedPlan.Markdown
            if planRepo != nil {
                msg := "revision"
                if len(decision.Comment) > 50 {
                    msg = "revision: " + decision.Comment[:50]
                } else if decision.Comment != "" {
                    msg = "revision: " + decision.Comment
                }
                if err := planRepo.Commit(revisedPlan.Markdown, msg); err != nil {
                    slog.Warn("plan commit failed", "err", err)
                }
            }
        } else {
            emit(Event{Type: EventChatResponse, ChatText: chatResponse})
        }
        continue
    ```

12. Add test `TestEngine_PlanComment_SessionContinuation` in `internal/orchestrator/orchestrator_test.go`:

- Create a `switchingMockRunner` for the architect. First call (RefineStreaming) returns `validPlanMarkdown()` with `sessionID: "plan-sess-1"`. Second call (RunContinue) returns chat-only text. Third call (RunContinue) returns a revised plan.
- Construct an engine: `Runners.Architect` = switching architect mock. Other runners = standard mocks with auto-accept.
- Start pipeline with `Input{Prompt: "test"}`.
- On `EventGateRequest(GatePlanApproval)`, send `DecisionComment{Comment: "why this approach?"}`.
- Assert: `EventChatResponse` emitted with non-empty `ChatText`. `EventAgentDone` emitted for architect.
- On next `EventGateRequest(GatePlanApproval)`, verify plan text is UNCHANGED (same as original).
- Send `DecisionComment{Comment: "remove WP1"}`.
- Assert: `EventAgentDone` emitted. Plan text IS changed (new plan from switching mock).
- Send `DecisionApprove` on next gate.

13. Add test `TestEngine_PlanComment_FallbackCold` in `orchestrator_test.go`:
   - Use `Input{PlanFile: validPlanMarkdown()}` — this skips research/planning, so `planSessionID == ""`.
   - Send `DecisionComment{Comment: "fix it"}`.
   - Assert: `RefineWithCommentsStreaming` is called (not `RunContinue`). The architect mock's `RunContinue` should NOT be called. Use a tracking mock that records which methods were invoked.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- `go test ./internal/orchestrator/... -run TestEngine` passes (all existing + new tests).
- `TestEngine_PlanComment_SessionContinuation` verifies the full chat → plan-revision flow.
- `TestEngine_PlanComment_FallbackCold` verifies `--plan` flag falls back correctly.

### 8. TUI: handle EventChatResponse and show conversation in plan review

This WP specifies every UI element, every keystroke, every render function, and every test.

#### 5a. Data structures

**Steps:**

1. Add `ChatEntry` struct to `internal/tui/screen_pipeline.go`:

   ```go
   // ChatEntry is one turn in the user-architect conversation during plan review.
   type ChatEntry struct {
       Role          string // "you" or "architect"
       Text          string
       HasPlanChange bool   // true if this entry accompanies a plan revision
   }
   ```

2. Add fields to `PipelineScreen`:

   ```go
   // Conversation state during plan review
   chatHistory     []ChatEntry
   planDiff        string          // unified diff from git micro-repo, set by EventGateRequest.PlanDiff
   diffViewport    viewport.Model  // paginated viewport for diff rendering
   reviewTokensIn  int64
   reviewTokensOut int64
   ```

3. In `Reset()`, add:
   ```go
   s.chatHistory = nil
   s.planDiff = ""
   s.diffViewport = viewport.New(0, 0)
   s.reviewTokensIn = 0
   s.reviewTokensOut = 0
   ```

#### 5b. ApplyEvent changes

4. In `ApplyEvent`, add case for `EventChatResponse`:

   ```go
   case orchestrator.EventChatResponse:
       s.chatHistory = append(s.chatHistory, ChatEntry{Role: "architect", Text: event.ChatText})
       s.content = ContentPlanReview
       s.awaitingPlanDecision = true
       contentWidth := max(1, int(float64(width)*splitRatio))
       s.planComment = textarea.New()
       s.planComment.Placeholder = "Ask a question or request changes..."
       s.planComment.SetWidth(max(1, contentWidth-4))
       s.planComment.SetHeight(2)
       s.planComment.CharLimit = 1024
       s.planComment.Focus()
       s.hasPlanComment = true
   ```

5. In `ApplyEvent`, modify the `EventGateRequest` → `GatePlanApproval` case:
   - Store `planDiff` from the event. If non-empty and `chatHistory` is non-empty, append a chat entry noting the revision.
   - Change placeholder from `"Comment to refine the plan..."` to `"Ask a question or request changes..."`.

   ```go
   case orchestrator.GatePlanApproval:
       s.planDiff = event.Gate.PlanDiff
       if len(s.chatHistory) > 0 && s.planDiff != "" {
           s.chatHistory = append(s.chatHistory, ChatEntry{
               Role: "architect", Text: "(plan revised — see diff with [D])", HasPlanChange: true,
           })
       }
       s.awaitingPlanDecision = true
       s.content = ContentPlanReview
       s.finalPlan = event.Gate.FinalPlanMarkdown
       s.hasPlan = true
       s.planFilePath = event.Gate.PlanFilePath
       contentWidth := max(1, int(float64(width)*splitRatio))
       s.planComment = textarea.New()
       s.planComment.Placeholder = "Ask a question or request changes..."
       // ... rest unchanged
   ```

6. In `ApplyEvent`, the `EventPlanReady` handler stays unchanged (no `previousPlan` logic needed — git has the history).

7. In `ApplyEvent`, in the `EventAgentDone` case, accumulate review tokens:
   ```go
   // After existing agent state update loop:
   if event.AgentID == "architect" && len(s.chatHistory) > 0 {
       s.reviewTokensIn += event.InputTokens
       s.reviewTokensOut += event.OutputTokens
   }
   ```

#### 5c. Keystroke changes

8. In `handlePlanReviewKey`, when `Enter` sends a comment, append user message BEFORE emitting intent:

   ```go
   // Inside the Enter handler, after comment := strings.TrimSpace(...)
   if comment != "" {
       s.chatHistory = append(s.chatHistory, ChatEntry{Role: "you", Text: comment})
       s.planComment.Reset()
       // ... rest unchanged
   }
   ```

9. In `handlePlanReviewKey`, add `d`/`D` case in the `switch msg.String()` block (alongside `a`/`e`/`s`):
   ```go
   case "d", "D":
       if s.planDiff != "" {
           s.content = ContentPlanDiff
           s.hasPlanComment = false
           s.contentVP.GotoTop()
           s.SyncViewports()
       }
       return s, nil
   ```

#### 5d. Render functions

10. Modify `viewPlanReview` to show conversation history above the plan:

    ```go
    func (s PipelineScreen) viewPlanReview(width int) string {
        if !s.hasPlan {
            return " Waiting for plan...\n"
        }
        var b strings.Builder
        if len(s.chatHistory) > 0 {
            for _, entry := range s.chatHistory {
                var prefix, style string
                if entry.Role == "architect" {
                    b.WriteString(goalStyle.Render(" Architect: "))
                } else {
                    b.WriteString(dimStyle.Render(" You: "))
                }
                lines := strings.SplitN(entry.Text, "\n", 4)
                for i, line := range lines {
                    if i == 3 {
                        b.WriteString(dimStyle.Render("    ...\n"))
                        break
                    }
                    if i > 0 {
                        b.WriteString("    ")
                    }
                    b.WriteString(line)
                    b.WriteString("\n")
                }
            }
            b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-2))))
            b.WriteString("\n")
        }
        b.WriteString(renderMarkdown(s.finalPlan, width))
        return b.String()
    }
    ```

    **Wireframe — First plan render (no conversation):**

    ```
    ┌─────────────────────────────────────────────────┬────────────┐
    │ # Plan                                          │ Agents     │
    │                                                 │────────────│
    │ ## Goal                                         │ ✓ gateway  │
    │ Add feature X.                                  │ ✓ research │
    │                                                 │ ✓ architect  │
    │ ## Work Packages                                │            │
    │ ### 1. Create the module                        │            │
    │ ...                                             │            │
    ├─────────────────────────────────────────────────┤            │
    │ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │            │
    │ Ask a question or request changes...            │            │
    ├─────────────────────────────────────────────────┴────────────┤
    │ [A] accept | [E] edit | [Ctrl+E] editor | [Enter] comment   │
    └─────────────────────────────────────────────────────────────-┘
    ```

    **Wireframe — After user asks a question (chat-only response):**

    ```
    ┌─────────────────────────────────────────────────┬────────────┐
    │  You: Why did you put step 3 before step 4?     │ Agents     │
    │  Architect: Step 3 initializes the config       │────────────│
    │      parser which step 4 depends on. The        │ ✓ gateway  │
    │      resolver cannot run without...             │ ✓ research │
    │ ─────────────────────────────────────────────── │ ✓ architect  │
    │ # Plan                                          │            │
    │                                                 │            │
    │ ## Goal                                         │            │
    │ Add feature X.                                  │            │
    │ ...                                             │            │
    ├─────────────────────────────────────────────────┤            │
    │ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │            │
    │ Ask a question or request changes...            │            │
    ├─────────────────────────────────────────────────┴────────────┤
    │ [A] accept | [E] edit | [Ctrl+E] editor | [D] diff | ...    │
    └─────────────────────────────────────────────────────────────-┘
    ```

    **Wireframe — After user requests a change (plan revised):**

    ```
    ┌─────────────────────────────────────────────────┬────────────┐
    │  You: Why did you put step 3 before step 4?     │ Agents     │
    │  Architect: Step 3 initializes the config...    │────────────│
    │  You: Remove work package 1                     │ ✓ gateway  │
    │  Architect: (plan revised — see diff with [D])  │ ✓ research │
    │ ─────────────────────────────────────────────── │ ✓ architect  │
    │ # Plan                                          │ ✓ architect  │
    │                                                 │            │
    │ ## Goal                                         │ Review:    │
    │ Add feature X. (REVISED)                        │   12.4k    │
    │                                                 │   tokens   │
    │ ## Work Packages                                │            │
    │ ### 1. (was WP2) Create the resolver            │            │
    │ ...                                             │            │
    ├─────────────────────────────────────────────────┤            │
    │ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │            │
    │ Ask a question or request changes...            │            │
    ├─────────────────────────────────────────────────┴────────────┤
    │ [A] accept | [E] edit | [Ctrl+E] editor | [D] diff | ...    │
    └─────────────────────────────────────────────────────────────-┘
    ```

    **Wireframe — Ctrl+E external editor:**

    ```
    (Bubble Tea suspends TUI, opens $EDITOR with final_plan.md)
    (On return: editorReturnMsg → reads file → Decision{Type: DecisionEdit})
    (Orchestrator commits edit to git micro-repo → diff is available)
    (Plan is replaced. Chat history remains. [D] shows diff against previous version.)
    ```

    **Wireframe — Diff view (D key):**

    ```
    ┌─────────────────────────────────────────────────┬────────────┐
    │  Plan Diff (last revision)                      │ Agents     │
    │ ─────────────────────────────────────────────── │────────────│
    │   # Plan                                        │ ✓ gateway  │
    │                                                 │ ✓ research │
    │   ## Goal                                       │ ✓ architect  │
    │ - Add feature X with 3 work packages.           │            │
    │ + Add feature X with 2 work packages.           │            │
    │                                                 │            │
    │   ## Work Packages                              │            │
    │ - ### 1. Initialize the config parser           │            │
    │ - **Steps:**                                    │            │
    │ - 1. Create config/parser.go                    │            │
    │                                                 │            │
    │ - ### 2. Create the resolver                    │            │
    │ + ### 1. Create the resolver                    │            │
    │   **Steps:**                                    │            │
    │   ...                                           │            │
    ├─────────────────────────────────────────────────┤            │
    │ [Esc] return to plan | [D] back to review       │            │
    ├─────────────────────────────────────────────────┴────────────┤
    │ [Esc] return to plan                                         │
    └─────────────────────────────────────────────────────────────-┘
    ```

11. In `viewFooter`, update the `ContentPlanReview` case to show diff hint and review tokens:
    ```go
    case ContentPlanReview:
        footer := " [A] accept | [E] edit | [Ctrl+E] editor | [Enter] comment | [Shift+Enter] newline"
        if s.planDiff != "" {
            footer += " | [D] diff"
        }
        footer += " | [S] cancel  [^C^C] quit"
        if len(s.chatHistory) > 0 && (s.reviewTokensIn+s.reviewTokensOut > 0) {
            footer += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
        }
        return keyStyle.Render(footer)
    ```

#### 5e. ContentPlanDiff view

12. Add `ContentPlanDiff` to the `ContentMode` enum in `internal/tui/model.go`:

    ```go
    ContentCompletion                      // QA report, summary
    ContentPlanDiff                        // line diff of last plan revision
    ```

13. In `viewContent`, add case:

    ```go
    case ContentPlanDiff:
        return s.viewPlanDiff(width)
    ```

14. In `viewInputZone`, add case:

    ```go
    case ContentPlanDiff:
        return keyStyle.Render(" [Esc] return to plan | [D] back to review")
    ```

15. In `viewFooter`, add case:

    ```go
    case ContentPlanDiff:
        return keyStyle.Render(" [Esc] return to plan                                        [?] help  [^C^C] quit")
    ```

16. Add `viewPlanDiff` method that renders the precomputed `git diff` output with color:

    ```go
    func (s PipelineScreen) viewPlanDiff(width int) string {
        var b strings.Builder
        b.WriteString(goalStyle.Render(" Plan Diff (last revision)"))
        b.WriteString("\n")
        b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-2))))
        b.WriteString("\n")
        // s.planDiff is a unified diff from git — colorize +/- lines
        for _, line := range strings.Split(s.planDiff, "\n") {
            switch {
            case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
                b.WriteString(passStyle.Render(line))
            case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
                b.WriteString(failStyle.Render(line))
            case strings.HasPrefix(line, "@@"):
                b.WriteString(phaseStyle.Render(line))
            case strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index "):
                continue // skip git header noise
            default:
                b.WriteString(line)
            }
            b.WriteString("\n")
        }
        return b.String()
    }
    ```

    No custom diff algorithm. The `planDiff` string arrives from the orchestrator via `EventGateRequest.PlanDiff`, which is the raw output of `git diff --no-color HEAD~1 HEAD -- plan.md`.

#### 5f. Key event routing for ContentPlanDiff

17. In the content-mode switch in `PipelineScreen.Update`, add:

    ```go
    case ContentPlanDiff:
        return s.handlePlanDiffKey(msg)
    ```

18. Add handler:

    ```go
    func (s PipelineScreen) handlePlanDiffKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
        if msg.Code == tea.KeyEscape || msg.String() == "d" || msg.String() == "D" {
            s.content = ContentPlanReview
            s.contentVP.GotoTop()
            contentWidth := max(1, int(float64(s.contentVP.Width()+s.sidebarVP.Width()+1)*splitRatio))
            s.planComment = textarea.New()
            s.planComment.Placeholder = "Ask a question or request changes..."
            s.planComment.SetWidth(max(1, contentWidth-4))
            s.planComment.SetHeight(2)
            s.planComment.CharLimit = 1024
            s.planComment.Focus()
            s.hasPlanComment = true
            s.awaitingPlanDecision = true
            s.SyncViewports()
        }
        return s, nil
    }
    ```

19. In the `Update` method's `"d", "D"` global key handler, add `ContentPlanDiff` to the exclusion list:
    ```go
    case "d", "D":
        if s.content != ContentCoaching && s.content != ContentPlanEdit && s.content != ContentPlanReview && s.content != ContentPlanDiff {
            s.showDashboard = !s.showDashboard
            // ...
        }
    ```

#### 5g. Update smoke tests and help text

20. In `viewHelp()`, update keybindings:

    ```
     [D]          Toggle full dashboard / Plan diff (in review)
    ```

21. In `hydratedModels()` in `app_smoke_test.go`, add two new hydrated models:

    ```go
    // StatePipeline + ContentPlanReview with chat history
    {
        m := base()
        m.state = StatePipeline
        m.pipelineScreen.content = ContentPlanReview
        m.pipelineScreen.hasPlan = true
        m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nDo the thing."
        m.pipelineScreen.chatHistory = []ChatEntry{
            {Role: "you", Text: "Why step 3 before step 4?"},
            {Role: "architect", Text: "Because config parser must init first."},
        }
        m.pipelineScreen.hasPlanComment = true
        m.pipelineScreen.planComment = textarea.New()
        m.pipelineScreen.planComment.SetWidth(80)
        m.pipelineScreen.planComment.SetHeight(2)
        m.pipelineScreen.planComment.CharLimit = 1024
        m.pipelineScreen.planComment.Focus()
        models["pipeline-plan-review-chat"] = m
    }

    // StatePipeline + ContentPlanDiff
    {
        m := base()
        m.state = StatePipeline
        m.pipelineScreen.content = ContentPlanDiff
        m.pipelineScreen.hasPlan = true
        m.pipelineScreen.planDiff = "--- a/plan.md\n+++ b/plan.md\n@@ -1,4 +1,4 @@\n # Plan\n \n ## Goal\n-Old.\n+New.\n"
        m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nNew."
        models["pipeline-plan-diff"] = m
    }
    ```

**Done when:**

- `go build ./cmd/orqestra` compiles.
- `go test ./internal/tui/...` passes — including `TestLayout_AllStatesRenderWithoutPanic` with the new hydrated models at all terminal sizes.

### 9. TUI unit tests for the conversation flow

**Steps:**

1. Add `TestTUI_ChatResponse` in `app_test.go`:

   ```go
   func TestTUI_ChatResponse(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.content = ContentPlanReview
       m.pipelineScreen.hasPlan = true
       m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nOriginal"
       m.pipelineScreen.hasPlanComment = true
       m.pipelineScreen.planComment = textarea.New()
       m.pipelineScreen.planComment.SetWidth(80)
       m.pipelineScreen.planComment.SetHeight(2)
       m.pipelineScreen.planComment.Focus()
       m.width = 120
       m.height = 40
       m.recalculateLayout()

       // Simulate EventChatResponse
       m.pipelineScreen.ApplyEvent(orchestrator.Event{
           Type:     orchestrator.EventChatResponse,
           ChatText: "Step 3 initializes the config parser.",
       }, m.width)

       // Verify state
       if m.pipelineScreen.content != ContentPlanReview {
           t.Errorf("expected ContentPlanReview, got %d", m.pipelineScreen.content)
       }
       if len(m.pipelineScreen.chatHistory) != 1 {
           t.Fatalf("expected 1 chat entry, got %d", len(m.pipelineScreen.chatHistory))
       }
       if m.pipelineScreen.chatHistory[0].Role != "architect" {
           t.Errorf("expected architect role, got %q", m.pipelineScreen.chatHistory[0].Role)
       }
       if !m.pipelineScreen.hasPlanComment {
           t.Error("expected comment textarea restored")
       }
       if m.pipelineScreen.finalPlan != "# Plan\n\n## Goal\nOriginal" {
           t.Error("expected plan unchanged after chat-only response")
       }

       // Verify render includes chat history
       view := m.pipelineScreen.viewPlanReview(100)
       if !strings.Contains(view, "Architect:") {
           t.Error("expected 'Architect:' prefix in rendered view")
       }
       if !strings.Contains(view, "config parser") {
           t.Error("expected chat text in rendered view")
       }
   }
   ```

2. Add `TestTUI_ChatHistory_UserAndArchitect` in `app_test.go`:

   ```go
   func TestTUI_ChatHistory_UserAndArchitect(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.content = ContentPlanReview
       m.pipelineScreen.hasPlan = true
       m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
       m.pipelineScreen.awaitingPlanDecision = true
       decisions := make(chan orchestrator.Decision, 1)
       m.decisions = decisions
       m.pipelineScreen.hasPlanComment = true
       m.pipelineScreen.planComment = textarea.New()
       m.pipelineScreen.planComment.SetWidth(80)
       m.pipelineScreen.planComment.SetHeight(2)
       m.pipelineScreen.planComment.Focus()
       m.pipelineScreen.planComment.SetValue("why this approach?")
       m.width = 120
       m.height = 40
       m.recalculateLayout()

       // Press Enter to submit comment
       result, _ := sendKey(m, tea.KeyEnter)
       model := result.(Model)

       // Verify user's message was added to chat history
       if len(model.pipelineScreen.chatHistory) != 1 {
           t.Fatalf("expected 1 chat entry, got %d", len(model.pipelineScreen.chatHistory))
       }
       if model.pipelineScreen.chatHistory[0].Role != "you" {
           t.Errorf("expected 'you' role, got %q", model.pipelineScreen.chatHistory[0].Role)
       }
       if model.pipelineScreen.chatHistory[0].Text != "why this approach?" {
           t.Errorf("expected 'why this approach?', got %q", model.pipelineScreen.chatHistory[0].Text)
       }
   }
   ```

3. Add `TestTUI_PlanDiffToggle` in `app_test.go`:

   ```go
   func TestTUI_PlanDiffToggle(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.content = ContentPlanReview
       m.pipelineScreen.hasPlan = true
       m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nNew"
       m.pipelineScreen.planDiff = "--- a/plan.md\n+++ b/plan.md\n@@ -1,4 +1,4 @@\n # Plan\n \n ## Goal\n-Old.\n+New.\n"
       m.pipelineScreen.awaitingPlanDecision = true
       m.width = 120
       m.height = 40
       m.recalculateLayout()

       // Press D to enter diff mode
       result, _ := sendRune(m, "d")
       model := result.(Model)
       if model.pipelineScreen.content != ContentPlanDiff {
           t.Errorf("expected ContentPlanDiff, got %d", model.pipelineScreen.content)
       }

       // Verify diff renders
       view := model.pipelineScreen.viewPlanDiff(100)
       if !strings.Contains(view, "Plan Diff") {
           t.Error("expected diff header in view")
       }
       if !strings.Contains(view, "-Old.") || !strings.Contains(view, "+New.") {
           t.Error("expected git unified diff content in view")
       }

       // Press Esc to return
       result2, _ := sendKey(model, tea.KeyEscape)
       model2 := result2.(Model)
       if model2.pipelineScreen.content != ContentPlanReview {
           t.Errorf("expected ContentPlanReview after Esc, got %d", model2.pipelineScreen.content)
       }
   }
   ```

4. Add `TestTUI_PlanDiffIgnoredWithoutHistory` in `app_test.go`:

   ```go
   func TestTUI_PlanDiffIgnoredWithoutHistory(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.content = ContentPlanReview
       m.pipelineScreen.hasPlan = true
       m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
       m.pipelineScreen.planDiff = "" // no history (initial plan, no revisions)
       m.width = 120
       m.height = 40
       m.recalculateLayout()

       result, _ := sendRune(m, "d")
       model := result.(Model)
       // Should stay in plan review — no diff available
       if model.pipelineScreen.content != ContentPlanReview {
           t.Errorf("expected ContentPlanReview (no diff available), got %d", model.pipelineScreen.content)
       }
   }
   ```

5. Add `TestTUI_ReviewTokenAccumulation` in `app_test.go`:

   ```go
   func TestTUI_ReviewTokenAccumulation(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.chatHistory = []ChatEntry{{Role: "you", Text: "q1"}}

       m.pipelineScreen.ApplyEvent(orchestrator.Event{
           Type:         orchestrator.EventAgentDone,
           AgentID:      "planner",
           InputTokens:  1000,
           OutputTokens: 500,
       }, m.width)

       if m.pipelineScreen.reviewTokensIn != 1000 {
           t.Errorf("reviewTokensIn = %d, want 1000", m.pipelineScreen.reviewTokensIn)
       }
       if m.pipelineScreen.reviewTokensOut != 500 {
           t.Errorf("reviewTokensOut = %d, want 500", m.pipelineScreen.reviewTokensOut)
       }
   }
   ```

6. Add `TestTUI_ViewPlanDiffColorize` in `app_test.go`:

   ```go
   func TestTUI_ViewPlanDiffColorize(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.pipelineScreen.content = ContentPlanDiff
       m.pipelineScreen.planDiff = "diff --git a/plan.md b/plan.md\nindex abc..def 100644\n--- a/plan.md\n+++ b/plan.md\n@@ -1,3 +1,3 @@\n # Plan\n-Old.\n+New.\n"
       m.width = 120
       m.height = 40

       view := m.pipelineScreen.viewPlanDiff(100)
       // git header noise should be stripped
       if strings.Contains(view, "diff --git") {
           t.Error("expected git header stripped from view")
       }
       if strings.Contains(view, "index abc") {
           t.Error("expected index line stripped from view")
       }
       // +/- lines should be present (colored by styles, but text is there)
       if !strings.Contains(view, "Plan Diff") {
           t.Error("expected diff header")
       }
   }
   ```

**Done when:**

- `go test ./internal/tui/... -run "TestTUI_Chat|TestTUI_PlanDiff|TestTUI_Review|TestTUI_ViewPlanDiff"` passes.
- All 6 new tests pass. All existing tests still pass.

### 10. E2E integration test for full session continuation

**Steps:**

1. Create `internal/agent/architect_e2e_test.go` with build tag `//go:build e2e`.

2. Test `TestArchitectSessionContinuation`:
   - Follow the existing E2E pattern in `internal/harness/harness_e2e_test.go`: use `ORQESTRA_LLM_URL` / `ORQESTRA_LLM_MODEL` env vars.
   - Construct a `*ClaudeCLI` runner with `WithAllowedTools`, `WithDisallowedTools`, `WithPermissionMode("plan")`.
   - Construct an `Architect` with this runner.
   - Call `architect.RefineStreaming(ctx, simpleResearchDraft, &buf)` — capture `sessionID`.
   - Assert `sessionID != ""`.
   - Call `architect.ContinueSession(ctx, sessionID, plan.Markdown, "Why this approach?", &buf)`.
   - Assert `err == nil`, `*RawPlan == nil` (chat-only), `response != ""`.
   - Call `architect.ContinueSession(ctx, sessionID, plan.Markdown, "Remove the first work package entirely", &buf)`.
   - Assert `*RawPlan != nil`, `plan.Markdown` differs from original.

3. Post-test JSONL inspection:
   - `harness.ResolveSessionLogPath(cwd, sessionID)` → read file → count `"type":"user"` lines → assert == 3.
   - Log tool registration status (informational).

**Done when:**

- `go test ./internal/agent/... -tags e2e -run TestArchitectSessionContinuation -v -timeout 300s` passes.

## Verification

Commands the worker runs after ALL packages complete:

- `go build ./cmd/orqestra`
- `go vet ./...`
- `grep -r '"planner"' internal/ cmd/ | grep -v _test.go | grep -v '.md'` returns no matches (WP4 rename complete)
- `go test ./internal/config/...` (WP3 system prompt + WP4 backward-compat)
- `go test ./internal/agent/... -run "TestArchitect|TestContinueSession"` (WP5)
- `go test ./internal/plan/... -run TestGitRepo` (WP6)
- `go test ./internal/orchestrator/... -run TestEngine` (WP7)
- `go test ./internal/tui/...` (WP8+9)
- `go test ./internal/tokenlimit/...` (WP4 rename)
- Manual: `orqestra --no-execute`, submit prompt, get plan, ask "why WP1?", see contextual answer above plan, ask "remove WP1", see plan update and "(plan revised)" note, press `d` to see colored diff, press `Esc`, approve.

## Assumptions

- `--resume` re-applies `--allowed-tools` and `--disallowed-tools` from `extraArgs` (verified by source inspection; validated by WP1 E2E).
- Claude CLI preserves session state on disk for the duration of interactive review.
- `parsePlanResultWithRecovery` works identically for `RunContinue` results (same `RunResult` structure).
- `git` is available on the host (macOS ships with it; CI images have it). If missing, diff is gracefully disabled.
- `LimitedRunner` returned by `wrapRunner` implements `ContinuableRunner` via its `RunContinue` method (verified in `tokenlimit/runner.go:80`).

## Gotchas

- **`wrapRunner` returns `harness.CLIRunner`**, not `ContinuableRunner`. The type assertion to `ContinuableRunner` must happen AFTER `wrapRunner` in `buildEngine`. The same assertion is needed in `runPlanOnly` — `NewArchitect` now requires `ContinuableRunner`.
- **`planSessionID` is block-scoped** in current code. Must move to function scope. The `goto planGate` path (--plan flag) has no session — `planSessionID` stays `""`, gate loop falls back to cold re-prompt.
- **The orchestrator creates a NEW `Architect` per `DecisionComment` turn** today. Must hoist the architect instance above the gate loop. The architect struct holds no mutable state between calls, so reuse is safe.
- **When the model responds without changing the plan**, `parsePlanResult` returns an error ("output does not start with # Plan"). In `ContinueSession`, this is EXPECTED — "no plan change," not "failure." The method returns `nil, response, usage, nil`.
- **Single-character keys `a`/`e`/`s`/`d` are intercepted before the comment textarea** in `handlePlanReviewKey`. Users cannot type these letters as the first character of a comment. This is a pre-existing modal design choice. The textarea is for multi-word input submitted via Enter.
- **The `editorReturnMsg` path** in `model.go` reads the file from disk and sends `DecisionEdit`. This bypasses the architect session — the architect never sees external edits. However, the orchestrator commits the edit to the git micro-repo, so `[D]` diff is available for all mutation paths (architect revision, external editor, in-TUI edit). Chat history persists across edits.
- **`architect_revision_meta.json` will overwrite** on each revision. The session JSONL has the full conversation history, so only the latest revision metadata is persisted as a separate file.
- **On plan revision, `EventPlanReady` is NOT re-emitted** by the orchestrator. The gate loop updates `finalPlanMarkdown` and `continue`s, which re-emits `EventGateRequest` carrying the updated plan in `GateRequest.FinalPlanMarkdown`. The TUI must update `finalPlan` from both `EventPlanReady` (first plan) AND `EventGateRequest.FinalPlanMarkdown` (revisions).
- **The `D` key has dual meaning**: dashboard toggle (in streaming/completion modes) vs. diff view (in plan review). The `Update` method's global `"d"` handler excludes `ContentPlanReview` and `ContentPlanDiff`. `handlePlanReviewKey` catches `"d"` locally. `handlePlanDiffKey` catches it to toggle back.
- **Multiple plan files from the harness**: each `RefineStreaming` and `ContinueSession` call may create or update a plan file under `~/.claude/plans/`. The side-channel recovery reads the LATEST `plan_mode` attachment from the JSONL. If the model creates a new plan file on revision (different filename), the JSONL will have a new attachment entry. `parsePlanResultWithRecovery` already scans the entire JSONL and returns the LAST match, so this works without changes.
- **Ctrl+E after conversation**: when the user opens the external editor, the file on disk is `final_plan.md` in the session directory. This file is written by the orchestrator at the top of the gate loop (`writeArtifact(session, "final_plan.md", finalPlanMarkdown)`). After a `ContinueSession` revision, the loop `continue`s and re-writes this file with the updated plan before re-emitting the gate request. So the editor always opens the latest plan version. After the user saves and returns, `editorReturnMsg` triggers a `DecisionEdit` that replaces `finalPlanMarkdown` — the architect session is NOT consulted. The orchestrator commits the edit to the git micro-repo, so the diff view shows external editor changes. Chat history persists.
- **YAML backward compatibility**: existing `.orqestra.yaml` files using `planner:` and `planner_attempts:` keys must still work after the rename. The config loader must detect the old keys and migrate them to `architect:` / `architect_attempts:` at load time, logging a deprecation warning. There is precedent for this in the existing validator→planner migration code.
