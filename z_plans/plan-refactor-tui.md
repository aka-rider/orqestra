# Plan

## Goal

Decompose the 1477-line model.go monolith into isolated screen sub-models with strict "messages down, intents up" data flow, making each screen independently testable and preparing the codebase for a mechanical Bubbletea v2 migration.

## Context

Verified by spot-checks against the codebase on 2026-05-10:

- model.go is 1477 lines. The `Model` struct has 40+ fields mixing domain side-effects (channels, cancel func, engine), UI state (viewports, textareas, cursors), and render config (bounds, dimensions).
- runs.go is 364 lines containing both View rendering and key handling for `StateRunsList` and `StateRunDetail`.
- messages.go is 21 lines — only defines `OrchestratorEventMsg`, `tickMsg`, `filePickerBatchMsg`, `filePickerDoneMsg`.
- layout.go is 120 lines — owns `recalculateLayout()`, `layoutBounds`, `joinSplitView()`, and all layout constants (`constHeaderHeight`, `constFooterHeight`, `splitRatio`, etc.).
- app_test.go is 1251 lines. Uses test hydration (directly setting model fields) with `testModel()`, `sendKey()`, `sendRune()` helpers.
- layout_test.go is 197 lines. Already has fuzz-size boundary tests (`TestLayout_NoPanicAtBoundarySizes`) and height invariant tests (`TestLayout_HeightInvariant`).
- `AppState` enum: `StatePrompt`, `StatePipeline`, `StateRunsList`, `StateRunDetail`.
- `ContentMode` enum: `ContentStreaming`, `ContentCoaching`, `ContentPlanReview`, `ContentPlanEdit`, `ContentAgentHistory`, `ContentCompletion`.
- Orchestrator types are in orchestrator.go: `Phase` (string type), `Event` struct (with `Type`, `Phase`, `AgentID`, `Gate`, `Err`, token fields), `Decision` struct (with `Type`, `GatewayAnswers`, `EditedContent`, `Comment`), `GateType` enum (`GateGatewayCoach`, `GatePlanApproval`).
- `viewPipelineScreen()` in model.go calls `m.contentVP.SetContent()`, `m.sidebarVP.SetContent()`, and `m.dashboardVP.SetContent()` inside `View()` — a mutation-in-View purity violation.
- `viewRunsListScreen()` and `viewRunDetailScreen()` in runs.go call `m.runsVP.SetContent()`, `m.runDetailVP.SetContent()`, `m.runStepsVP.SetContent()`, `m.runLogVP.SetContent()` inside `View()` — same violation.
- `makeAnswerFields()` is a test helper defined at `app_test.go:583`.
- `joinSplitView()` is in `layout.go:108`. `renderMarkdown()` is in `markdown.go:7`.
- All existing tests pass: `go test ./internal/tui/... -count=1 -short` exits 0.

**Architectural decisions:**

1. **Keep `OrchestratorEventMsg` as-is.** The current single-wrapper approach works. Decomposing into typed `Engine*Msg` per event is v2 scope. The parent's `applyEvent()` stays on the root model and fans data into screens.
2. **Screens are value types, not `tea.Model` implementors.** Each screen has `Update(msg tea.Msg) (ScreenType, tea.Cmd)` and `View() string`. The parent calls them explicitly — no interface dispatch. This avoids type assertions and gives the parent full routing control.
3. **Intents flow up via `tea.Cmd`.** When a screen needs the parent to perform a side-effect (write to channel, cancel context, navigate), its `Update` returns a `tea.Cmd` yielding a typed intent message. The parent catches the intent in its own `Update`.
4. **Domain side-effects remain on the root `Model`.** Channels, cancel func, engine, streamBuf stay on the root. Screens never import `orchestrator` for channels — they emit intents.
5. **Hoist viewport mutations before extraction.** Moving `SetContent()` out of `View()` into `Update()` is the prerequisite — it decouples content generation from viewport rendering, making extraction mechanical.

## Constraints

- Every work package must leave the codebase compiling, tests passing, and TUI functional. No multi-commit broken state.
- Do NOT decompose `OrchestratorEventMsg` into multiple typed event messages. That is v2 migration scope.
- Do NOT touch orchestrator, agent, harness, or any package outside tui.
- Do NOT add new dependencies. Use only existing `charmbracelet/bubbles`, `charmbracelet/bubbletea`, `charmbracelet/lipgloss`.
- Do NOT change the visual appearance of any TUI screen. Every pixel must render identically before and after.
- Do NOT rename public symbols (`Run`, `RunHeadless`, `NewModel`, `Model`).
- `filepicker.go` (457 lines), editor.go (27 lines), markdown.go (21 lines), `mascot.go` (31 lines), `styles.go` (90 lines) — leave these files unchanged. The file picker moves WITH `PromptScreen` in Work Package 5 only by updating field ownership, not by restructuring the file.
- Layout constants and `joinSplitView()` stay in layout.go. `recalculateLayout()` stays on the root model (it manages global viewport dimensions).

## Risks

1. **Viewport content staleness after hoist.** Moving `SetContent()` to `Update()` means every model mutation path that changes rendered content must call the corresponding sync method. A missed path produces stale viewport content. **Mitigation:** Work Package 1 adds render-every-state tests that catch stale content. Work Package 2 adds sync calls to every handler path systematically.
2. **Value semantics on screens containing viewports.** Viewports are value types in Bubbletea v1. If the parent forgets to reassign after `screen.Update()`, viewport scroll state is lost. **Mitigation:** Every extraction step includes a test that scrolls the viewport and asserts position changes persist.
3. **Textarea focus management across screen transitions.** Currently `NewModel()` calls `ta.Focus()` once. After extraction, the parent must manage focus when transitioning between screens. **Mitigation:** Each screen has a `Focus()` method called by the parent on activation.
4. **Test hydration breakage.** Existing tests directly set `m.runs`, `m.runsCursor`, `m.state`, etc. After extraction, these fields move into screen structs. **Mitigation:** Tests are updated in the same work package as each extraction. No deferred test fixups.
5. **Intent message ordering with channel writes.** Currently, key handlers write to `m.decisions` synchronously. With intents, the write happens one Update cycle later (parent catches intent, writes to channel). If the orchestrator checks the channel synchronously within the same event loop tick, this introduces a race. **Mitigation:** The orchestrator reads decisions asynchronously via `<-decisions`, and the TUI event loop is single-threaded. The one-cycle delay is safe because the orchestrator blocks on the channel read.

## Work Packages

### 1. Extend smoke tests to cover all hydrated states

layout_test.go already covers `StatePrompt` and `StatePipeline` with the streaming content mode. This step adds coverage for every `ContentMode` and the runs screens, catching nil-pointer panics and layout violations before refactoring begins.

**Steps:**

1. In layout_test.go, add a `TestLayout_AllStatesRenderWithoutPanic` test function. Build a table of hydrated models covering these states:
   - `StatePipeline` + `ContentCoaching` (set `m.gatewayResult` with one question, `m.answerFields` via the same inline construction as `applyEvent` does)
   - `StatePipeline` + `ContentPlanReview` (set `m.hasPlan = true`, `m.finalPlan = "# Test Plan\n..."`)
   - `StatePipeline` + `ContentPlanEdit` (set `m.hasPlanEditor = true`, create and assign `m.planEditor` textarea)
   - `StatePipeline` + `ContentAgentHistory` (set `m.focusedAgent = 1`, `m.agents = []AgentRow{{ID: "worker", State: "done"}}`)
   - `StatePipeline` + `ContentCompletion` (set `m.hasValidation = true`, `m.workerValidation = "pass"`)
   - `StateRunsList` (set `m.runs` via `testRunSummaries()` from runs_test.go)
   - `StateRunDetail` (set `m.runDetail` via `testRunDetail()`, `m.runLogLines = []string{"line1"}`)
2. For each hydrated model, loop through sizes `{60,10}`, `{120,40}`, `{200,60}`, `{1,1}`. Call `m.recalculateLayout()` then `m.View()` inside a `defer recover()` block. Fail the test on any panic.
3. Add a `TestLayout_CtrlCAlwaysQuits` test. For each hydrated model from step 1, send `tea.KeyMsg{Type: tea.KeyCtrlC}` twice. Assert the second call's `tea.Cmd` executes to `tea.Quit` (check by comparing to `tea.Quit` — since `tea.Quit` returns a `tea.QuitMsg`, execute the cmd and type-assert the result).
4. Add a `TestLayout_EditorReturnError` test. Hydrate a `StatePipeline` model. Send `editorReturnMsg{err: fmt.Errorf("editor failed")}` via `m.Update()`. Assert `m.lastErr` is non-nil and `m.editorRunning` is false. Call `m.View()` — no panic.

**Done when:**

- `go test ./internal/tui/... -run TestLayout_AllStatesRenderWithoutPanic -count=1 -v` passes.
- `go test ./internal/tui/... -run TestLayout_CtrlCAlwaysQuits -count=1 -v` passes.
- `go test ./internal/tui/... -run TestLayout_EditorReturnError -count=1 -v` passes.
- `go test ./internal/tui/... -count=1` passes (all existing tests still green).

### 2. Hoist viewport mutations from View() to Update()

Move all `viewport.SetContent()` and `viewport.GotoBottom()` calls out of `View()` methods and into `Update()` paths. After this step, every `View()` method is a pure function that only reads viewport state.

**Steps:**

1. In model.go, add three private methods with pointer receivers:

   ```go
   // syncPipelineViewports updates viewport content from current model state.
   // Must be called from Update() after any state change that affects rendered content.
   func (m *Model) syncPipelineViewports() { ... }

   // syncRunsListViewport updates the runs list viewport from current model state.
   func (m *Model) syncRunsListViewport() { ... }

   // syncRunDetailViewports updates all three run detail viewports from current model state.
   func (m *Model) syncRunDetailViewports() { ... }
   ```

2. Implement `syncPipelineViewports()`: Move the body of `viewPipelineScreen()`'s viewport mutation block (lines that call `m.dashboardVP.SetContent`, `m.contentVP.SetContent`, `m.sidebarVP.SetContent`, and the `GotoBottom` logic) into this method. The method calls `m.viewContent(contentWidth)`, `m.viewSidebar(sidebarWidth)`, `m.viewDashboard()` to generate content strings, then feeds them into the viewports. Compute `contentWidth` and `sidebarWidth` from `m.width` and `splitRatio`, identical to the current `viewPipelineScreen()`.

3. Implement `syncRunsListViewport()`: Move the content-generation logic from `viewRunsListScreen()` (the block that builds the runs list string and calls `m.runsVP.SetContent()`) into this method. Use the same rendering code — iterate `m.runs`, format each row with cursor highlighting, call `m.runsVP.SetContent(body)`.

4. Implement `syncRunDetailViewports()`: Move the content-generation logic from `viewRunDetailScreen()` (the block that builds left content, step menu, and calls `m.runDetailVP.SetContent()`, `m.runStepsVP.SetContent()`) into this method. Note: `loadStepLog()` already calls `m.runLogVP.SetContent()` — it is already correct. Leave it as-is.

5. Add `m.syncPipelineViewports()` calls to every `Update()` path that changes pipeline state:
   - At the end of the `OrchestratorEventMsg` handler (after the `applyEvent` drain loop, before returning), add `if m.state == StatePipeline { m.syncPipelineViewports() }`.
   - In the `tickMsg` handler, add `m.syncPipelineViewports()` before returning `tickCmd()`.
   - At the end of `handlePipelineKey`, `handleCoachingKey`, `handlePlanReviewKey`, `handlePlanEditKey`, `handleStreamingKey`, `handleCompletionKey`, `handleAgentHistoryKey` — add `m.syncPipelineViewports()` before every `return m, cmd` that changes `m.content` or viewport-affecting state.
   - In the `tea.WindowSizeMsg` handler, after `m.recalculateLayout()`, add state-dependent sync calls: `if m.state == StatePipeline { m.syncPipelineViewports() } else if m.state == StateRunsList { m.syncRunsListViewport() } else if m.state == StateRunDetail { m.syncRunDetailViewports() }`.

6. Add `m.syncRunsListViewport()` calls:
   - At the end of `handleRunsListKey` after any cursor change (`tea.KeyUp`, `tea.KeyDown`, `"j"`, `"k"`).
   - In `navigateToRunsList()` after setting `m.runs` and `m.runsCursor`.

7. Add `m.syncRunDetailViewports()` calls:
   - At the end of `handleRunDetailKey` after step cursor change (`"j"`, `"k"`).
   - After `m.runDetail = detail` in the `tea.KeyEnter` handler of `handleRunsListKey`.

8. Remove all `SetContent()` and `GotoBottom()` calls from `viewPipelineScreen()`, `viewRunsListScreen()`, and `viewRunDetailScreen()`. These View methods now only call `m.contentVP.View()`, `m.sidebarVP.View()`, etc. to read the already-set content. Specifically:
   - In `viewPipelineScreen()`: remove the `m.dashboardVP.SetContent(...)`, `m.contentVP.SetContent(...)`, `m.sidebarVP.SetContent(...)`, and `m.contentVP.GotoBottom()` calls. Replace with direct `m.dashboardVP.View()` / `m.contentVP.View()` / `m.sidebarVP.View()` reads. Also remove the `m.dashboardVP.Width = w` / `m.dashboardVP.Height = contentHeight` re-assignments (these are set by `recalculateLayout` and `syncPipelineViewports`).
   - In `viewRunsListScreen()`: remove the `m.runsVP.SetContent(...)` call. Just call `m.runsVP.View()`.
   - In `viewRunDetailScreen()`: remove the `m.runDetailVP.SetContent(...)`, `m.runStepsVP.SetContent(...)`, `m.runLogVP.SetContent(...)` calls. Just call their `.View()` methods.

**Done when:**

- `go test ./internal/tui/... -count=1` passes (all tests, including the new smoke tests from WP1).
- `grep -n 'SetContent' model.go internal/tui/runs.go` shows SetContent calls ONLY inside `sync*` methods, `resetPipelineState()`, `loadStepLog()`, and `recalculateLayout()` — never inside any `view*` or `View()` function.
- Manually verify: `go build ./cmd/orqestra && ./orqestra --help` compiles and prints help (no import cycles, no panics at startup).

### 3. Define Intent message types in messages.go

Add typed intent messages that screens will return via `tea.Cmd` to request parent-level side-effects. For now, these types are defined but not yet used — they become the screen API in subsequent packages.

**Steps:**

1. In messages.go, add the following types after the existing message types:

   ```go
   // --- Intent messages (screens → parent via tea.Cmd) ---

   // StartPipelineIntent requests the parent start a new pipeline run.
   type StartPipelineIntent struct {
       Prompt      string
       SkipGateway bool
   }

   // CancelPipelineIntent requests the parent cancel the active pipeline.
   type CancelPipelineIntent struct{}

   // SubmitGatewayIntent submits answers to gateway coaching questions.
   type SubmitGatewayIntent struct {
       Answers []orchestrator.GatewayAnswer
   }

   // SkipGatewayIntent skips gateway coaching.
   type SkipGatewayIntent struct{}

   // ApprovePlanIntent approves the current plan.
   type ApprovePlanIntent struct{}

   // EditPlanIntent submits an edited plan.
   type EditPlanIntent struct {
       ModifiedMarkdown string
   }

   // CommentPlanIntent submits a comment for plan refinement.
   type CommentPlanIntent struct {
       Comment string
   }

   // CancelPlanIntent cancels the plan (rejects it).
   type CancelPlanIntent struct{}

   // NavigateToPromptIntent requests navigation back to prompt screen.
   type NavigateToPromptIntent struct {
       PreFillGoal string // optional: pre-fill prompt with previous goal
   }

   // NavigateToRunsListIntent requests navigation to the runs history list.
   type NavigateToRunsListIntent struct{}

   // NavigateToRunDetailIntent requests navigation to a specific run.
   type NavigateToRunDetailIntent struct {
       RunIndex int
   }

   // NavigateBackIntent requests navigation to the previous screen.
   type NavigateBackIntent struct{}

   // ToggleDashboardIntent toggles the full dashboard view.
   type ToggleDashboardIntent struct{}

   // OpenExternalEditorIntent requests opening a file in the external editor.
   type OpenExternalEditorIntent struct {
       FilePath string
   }

   // ConfirmNewRunIntent confirms starting a new run while pipeline is active.
   type ConfirmNewRunIntent struct{}
   ```

2. Add the `orchestrator` import to messages.go's import block: `"github.com/xiii/orqestra/internal/orchestrator"`.

3. Add a helper function to messages.go for creating intent commands:

   ```go
   // intentCmd wraps a message in a tea.Cmd for returning intents from screen Update methods.
   func intentCmd(msg tea.Msg) tea.Cmd {
       return func() tea.Msg { return msg }
   }
   ```

**Done when:**

- `go build internal.` compiles with no errors.
- `go vet internal.` passes.
- `grep -c 'Intent' internal/tui/messages.go` returns at least 12 (one per intent type).

### 4. Extract RunsListScreen and RunDetailScreen

Move runs-list and run-detail UI into `screen_runs_list.go` and `screen_run_detail.go`. These are the simplest screens — they have no channel interaction and self-contained key handling.

**Steps:**

1. Create `internal/tui/screen_runs_list.go`. Define:

   ```go
   // RunsListScreen manages the historical runs list view.
   type RunsListScreen struct {
       runs     []agent.RunSummary
       cursor   int
       viewport viewport.Model
   }

   func NewRunsListScreen() RunsListScreen { ... }
   func (s RunsListScreen) Update(msg tea.Msg) (RunsListScreen, tea.Cmd) { ... }
   func (s RunsListScreen) View(width, height int) string { ... }
   func (s *RunsListScreen) SetRuns(runs []agent.RunSummary) { ... }
   func (s *RunsListScreen) SyncViewport(width int) { ... }
   func (s RunsListScreen) SelectedRun() (agent.RunSummary, bool) { ... }
   ```

   - `NewRunsListScreen()`: create viewport with `viewport.New(0, 0)` and `MouseWheelEnabled = true`.
   - `Update()`: move the body of `handleRunsListKey()` from runs.go. Replace `m.state = StatePrompt` with `return s, intentCmd(NavigateBackIntent{})`. Replace `tea.KeyEnter` logic with `return s, intentCmd(NavigateToRunDetailIntent{RunIndex: s.cursor})`. After any cursor change, call `s.SyncViewport(s.viewport.Width)`.
   - `View()`: move the body of `viewRunsListScreen()` from runs.go. It now reads `s.viewport.View()` (no SetContent — already synced). Receives `width` and `height` for header/footer chrome rendering.
   - `SetRuns()`: assigns `s.runs`, resets `s.cursor = 0`.
   - `SyncViewport()`: move the content-generation loop (formatting each run row with cursor highlight) from `syncRunsListViewport()` and call `s.viewport.SetContent(body)`.
   - `SelectedRun()`: returns `s.runs[s.cursor]` if in bounds, or `false`.

2. Create `internal/tui/screen_run_detail.go`. Define:

   ```go
   // RunDetailScreen manages the run detail inspection view.
   type RunDetailScreen struct {
       detail       agent.RunDetail
       stepCursor   int
       logLines     []string
       detailVP     viewport.Model
       stepsVP      viewport.Model
       logVP        viewport.Model
   }

   func NewRunDetailScreen() RunDetailScreen { ... }
   func (s RunDetailScreen) Update(msg tea.Msg) (RunDetailScreen, tea.Cmd) { ... }
   func (s RunDetailScreen) View(width, height int) string { ... }
   func (s *RunDetailScreen) SetDetail(detail agent.RunDetail) { ... }
   func (s *RunDetailScreen) SyncViewports(contentWidth, sidebarWidth, upperHeight, logHeight int) { ... }
   func (s *RunDetailScreen) LoadStepLog() { ... }
   ```

   - `NewRunDetailScreen()`: create three viewports with `MouseWheelEnabled = true`.
   - `Update()`: move the body of `handleRunDetailKey()` from runs.go. Replace `m.state = StateRunsList` with `return s, intentCmd(NavigateBackIntent{})`. After step cursor change, call `s.LoadStepLog()` and `s.SyncViewports(...)`. For `tea.KeyCtrlE`, return `intentCmd(OpenExternalEditorIntent{...})` instead of calling `m.openStepLog()` directly.
   - `View()`: move the body of `viewRunDetailScreen()` from runs.go. Read from viewports, no SetContent calls.
   - `LoadStepLog()`: move the body of `loadStepLog()` from runs.go into this method. It accesses `s.detail.Steps[s.stepCursor]` and calls `s.logVP.SetContent(...)`.
   - `viewRunSteps()`: move from runs.go into this file as a method on `RunDetailScreen`.

3. Update model.go:
   - Replace fields `runs []agent.RunSummary`, `runsCursor int`, `runsVP viewport.Model` with `runsListScreen RunsListScreen`.
   - Replace fields `runDetail agent.RunDetail`, `runStepCursor int`, `runLogLines []string`, `runDetailVP viewport.Model`, `runStepsVP viewport.Model`, `runLogVP viewport.Model` with `runDetailScreen RunDetailScreen`.
   - In `NewModel()`: replace the 4 runs-related viewport initializations (`rvp`, `rdvp`, `rsvp`, `rlvp`) with `runsListScreen: NewRunsListScreen()` and `runDetailScreen: NewRunDetailScreen()`.
   - In `handleKey()`: for `case StateRunsList`, delegate to `m.runsListScreen.Update(msg)` and handle returned intents. For `case StateRunDetail`, delegate to `m.runDetailScreen.Update(msg)`.
   - In `recalculateLayout()`: replace `m.runsVP.Width`, `m.runsVP.Height` with `m.runsListScreen.viewport.Width`, `m.runsListScreen.viewport.Height`. Same for run detail viewports. (Or pass layout dimensions via a method — choose whichever is cleaner.)
   - Update `navigateToRunsList()` to call `m.runsListScreen.SetRuns(runs)`.
   - In the `tea.KeyEnter` handler that transitions from runs list to detail: the parent catches `NavigateToRunDetailIntent`, loads the detail via `agent.LoadRunDetail(...)`, calls `m.runDetailScreen.SetDetail(detail)`, `m.runDetailScreen.LoadStepLog()`, and sets `m.state = StateRunDetail`.
   - Remove `syncRunsListViewport()` and `syncRunDetailViewports()` from model.go (they moved into the screen structs).

4. Delete the old `handleRunsListKey()`, `handleRunDetailKey()`, `viewRunsListScreen()`, `viewRunDetailScreen()`, `viewRunSteps()`, `loadStepLog()`, `openStepLog()`, `navigateToRunsList()` from their original locations (runs.go and model.go). After this, runs.go should contain only shared helpers: `statusIcon()`, `formatDuration()`, and `navigateToRunsList()` (if it stays on Model). If runs.go is nearly empty, move its remaining functions into model.go or `screen_runs_list.go` and delete runs.go.

5. Update runs_test.go:
   - Change test hydration: replace `m.runs = testRunSummaries()` with `m.runsListScreen.SetRuns(testRunSummaries())`.
   - Replace `m.runsCursor` reads with `m.runsListScreen.cursor`.
   - Replace `m.runDetail = testRunDetail()` with `m.runDetailScreen.SetDetail(testRunDetail())`.
   - Replace `m.runStepCursor` reads with `m.runDetailScreen.stepCursor`.
   - Adjust any `model.runsCursor` assertions to `model.runsListScreen.cursor`.

6. Update app_test.go similarly — any test that sets `m.state = StateRunsList` and manipulates `m.runs` or `m.runsCursor` needs the same field path updates.

**Done when:**

- `go test ./internal/tui/... -count=1` passes.
- `go vet internal.` passes.
- Files `internal/tui/screen_runs_list.go` and `internal/tui/screen_run_detail.go` exist.
- `grep -n 'handleRunsListKey\|handleRunDetailKey\|viewRunsListScreen\|viewRunDetailScreen' model.go internal/tui/runs.go` returns zero matches (these functions no longer exist in the old locations).
- `grep -c 'NavigateBackIntent\|NavigateToRunDetailIntent' internal/tui/screen_runs_list.go internal/tui/screen_run_detail.go` returns at least 2 (intents are used).

### 5. Extract PromptScreen

Move prompt input, file picker integration, and prompt-screen rendering into `screen_prompt.go`.

**Steps:**

1. Create `internal/tui/screen_prompt.go`. Define:

   ```go
   // PromptScreen manages the task prompt input and file picker.
   type PromptScreen struct {
       textarea  textarea.Model
       fp        filePicker
       fpActive  bool
       fpAtStart int
       fpQuery   string
   }

   func NewPromptScreen() PromptScreen { ... }
   func (s PromptScreen) Update(msg tea.Msg) (PromptScreen, tea.Cmd) { ... }
   func (s PromptScreen) View(width, height int) string { ... }
   func (s *PromptScreen) Focus() { ... }
   func (s *PromptScreen) Reset() { ... }
   func (s *PromptScreen) SetValue(v string) { ... }
   func (s PromptScreen) Value() string { ... }
   func (s *PromptScreen) SetWidth(w int) { ... }
   ```

   - `NewPromptScreen()`: initialize the textarea with the same settings as current `NewModel()` (placeholder, width 80, height 3, char limit 4096, focused).
   - `Update()`: move the body of `handlePromptKey()`. For `tea.KeyEnter`: return `intentCmd(StartPipelineIntent{Prompt: prompt, SkipGateway: false})`. For `tea.KeyCtrlS`: return `intentCmd(StartPipelineIntent{Prompt: prompt, SkipGateway: true})`. For `tea.KeyCtrlR`: return `intentCmd(NavigateToRunsListIntent{})`. For `"@"` handling: keep the file picker activation logic inline (call `s.activateFilePicker(cmd)`).
   - `View()`: move the body of `viewPromptScreen()`. Receives `width` and `height` for layout.
   - `Focus()`: calls `s.textarea.Focus()`.
   - `Reset()`: calls `s.textarea.Reset()`.
   - Move `handleFilePickerKey()` and `activateFilePicker()` from model.go to methods on `PromptScreen` in `screen_prompt.go`. These functions currently reference `m.prompt`, `m.fp`, `m.fpActive`, `m.fpAtStart`, `m.fpQuery` — change all to `s.textarea`, `s.fp`, etc.

2. Update model.go:
   - Replace fields `prompt textarea.Model`, `fp filePicker`, `fpActive bool`, `fpAtStart int`, `fpQuery string` with `promptScreen PromptScreen`.
   - In `NewModel()`: replace textarea initialization with `promptScreen: NewPromptScreen()`.
   - In `handleKey()`: for `case StatePrompt`, delegate to `m.promptScreen.Update(msg)`. Catch `StartPipelineIntent` in the result: call `m.resetPipelineState()`, set `m.goal`, transition to `StatePipeline`, call `m.startPipeline()`. Catch `NavigateToRunsListIntent`: call `m.navigateToRunsList()`.
   - In `recalculateLayout()`: replace `m.prompt.SetWidth(...)` with `m.promptScreen.SetWidth(...)`.
   - In the bottom of `Update()` where `m.state == StatePrompt` delegates to `m.prompt.Update(msg)` — this entire block is replaced by the delegation in `handleKey`.
   - In `resetPipelineState()` and any other place that calls `m.prompt.Reset()` or `m.prompt.SetValue()` — route through `m.promptScreen.Reset()` / `m.promptScreen.SetValue()`.
   - In `filePickerBatchMsg` and `filePickerDoneMsg` handlers: route through `m.promptScreen.fp` instead of `m.fp`.

3. Move `readNextBatch()` from wherever it's defined (likely `filepicker.go`) — if it references Model fields, update it to work with PromptScreen fields. If it's already a standalone function taking a channel, it stays as-is.

4. Update app_test.go:
   - Replace `m.prompt.SetValue("add a feature")` with `m.promptScreen.SetValue("add a feature")`.
   - Replace `model.goal` assertions — `goal` stays on the root Model (set by the parent when catching `StartPipelineIntent`), so these assertions remain valid.
   - Replace any `m.fpActive` references with `m.promptScreen.fpActive`.

**Done when:**

- `go test ./internal/tui/... -count=1` passes.
- File `internal/tui/screen_prompt.go` exists.
- `grep -n 'handlePromptKey\|handleFilePickerKey\|activateFilePicker' internal/tui/model.go` returns zero matches (moved to screen_prompt.go).
- `grep -c 'StartPipelineIntent\|NavigateToRunsListIntent' internal/tui/screen_prompt.go` returns at least 2.

### 6. Extract PipelineScreen

Move all pipeline-mode UI state, content-mode routing, and rendering into `screen_pipeline.go`. This is the largest extraction. The root Model retains only the domain side-effects (channels, cancel, engine) and the screen routing.

**Steps:**

1. Create `internal/tui/screen_pipeline.go`. Define:

   ```go
   // PipelineScreen manages the pipeline execution view with all content modes.
   type PipelineScreen struct {
       content    ContentMode
       goal       string
       phase      orchestrator.Phase
       configName string
       startTime  time.Time

       agents           []AgentRow
       gatewayResult    agent.GatewayResult
       finalPlan        string
       hasPlan          bool
       workerValidation string
       hasValidation    bool
       lastErr          error

       answerFields []textarea.Model
       answerCursor int

       planEditor    textarea.Model
       hasPlanEditor bool

       planComment    textarea.Model
       hasPlanComment bool
       editorRunning  bool

       focusedAgent         int
       runDir               string
       planFilePath         string
       awaitingPlanDecision bool

       showDashboard bool
       showHelp      bool
       confirmNew    bool

       contentVP   viewport.Model
       sidebarVP   viewport.Model
       dashboardVP viewport.Model
       bounds      layoutBounds
   }

   func NewPipelineScreen(configName string) PipelineScreen { ... }
   func (s PipelineScreen) Update(msg tea.Msg) (PipelineScreen, tea.Cmd) { ... }
   func (s PipelineScreen) View(width, height int) string { ... }
   func (s *PipelineScreen) ApplyEvent(event orchestrator.Event, width int) { ... }
   func (s *PipelineScreen) Reset() { ... }
   func (s *PipelineScreen) SyncViewports() { ... }
   func (s *PipelineScreen) RecalculateLayout(width, height, inputHeight int) { ... }
   ```

   - `NewPipelineScreen()`: initialize 3 viewports (`contentVP`, `sidebarVP`, `dashboardVP`) with `MouseWheelEnabled = true`.
   - `Update()`: move the body of `handlePipelineKey()` and all its sub-handlers (`handleCoachingKey`, `handlePlanReviewKey`, `handlePlanEditKey`, `handleStreamingKey`, `handleCompletionKey`, `handleAgentHistoryKey`). Convert every `m.decisions <- ...` to return an intent. Specifically:
     - `m.decisions <- Decision{Type: DecisionApprove}` → `return s, intentCmd(ApprovePlanIntent{})`
     - `m.decisions <- Decision{Type: DecisionSkip}` → `return s, intentCmd(SkipGatewayIntent{})`
     - `m.decisions <- Decision{Type: DecisionCancel}` → `return s, intentCmd(CancelPlanIntent{})`
     - `m.decisions <- Decision{Type: DecisionEdit, EditedContent: ...}` → `return s, intentCmd(EditPlanIntent{ModifiedMarkdown: ...})`
     - `m.decisions <- Decision{Type: DecisionComment, Comment: ...}` → `return s, intentCmd(CommentPlanIntent{Comment: ...})`
     - Cancel running agent (`m.cancel()`) → `return s, intentCmd(CancelPipelineIntent{})`
     - Navigation to prompt → `return s, intentCmd(NavigateToPromptIntent{PreFillGoal: s.goal})`
     - `m.confirmNew = true` followed by cancel+navigate → `return s, intentCmd(ConfirmNewRunIntent{})`
     - Ctrl+E open editor → `return s, intentCmd(OpenExternalEditorIntent{FilePath: s.planFilePath})`
     - `tea.KeyCtrlR` → `return s, intentCmd(NavigateToRunsListIntent{})`
   - Call `s.SyncViewports()` after any state mutation that changes rendered content, before returning.
   - `View()`: move the body of `viewPipelineScreen()` and all content view methods (`viewContent`, `viewStreaming`, `viewCoaching`, `viewPlanReview`, `viewPlanEdit`, `viewAgentHistory`, `viewCompletion`, `viewDashboard`, `viewSidebar`, `viewHelp`, `viewFooter`, `viewInputZone`). These become methods on `PipelineScreen` instead of `Model`. The View reads from viewports — no SetContent calls.
   - `ApplyEvent()`: move the body of `applyEvent()` from model.go. This stays as a pointer-receiver method because it mutates PipelineScreen fields. The parent calls it: `m.pipelineScreen.ApplyEvent(event, m.width)`.
   - `Reset()`: move the body of `resetPipelineState()` — clear all pipeline-specific fields.
   - `SyncViewports()`: move `syncPipelineViewports()` into this method.
   - `RecalculateLayout()`: move the pipeline-specific viewport sizing from `recalculateLayout()` — the root model computes dimensions and passes them to this method.

2. Move the following render helpers into `screen_pipeline.go` (they are only used by PipelineScreen):
   - `renderActivityLog()`, `isFilePathTool()`, `fileHyperlink()` from model.go
   - `formatTokens()`, `formatTokenCount()` from model.go — OR keep them in a shared location if RunDetailScreen also uses them. Check: `formatDuration()` is in runs.go and used by both. `formatTokens()` is used by `viewSidebar` (pipeline) and `viewCompletion` (pipeline). `formatTokenCount()` is used by `viewDashboard` (pipeline). These can move to `screen_pipeline.go`.

3. Update model.go:
   - Replace the ~25 pipeline-specific fields with `pipelineScreen PipelineScreen`.
   - Keep on root Model: `state`, `width`, `height`, `engine`, `configName`, `startTime`, `goal`, `phase`, `events`, `decisions`, `cancel`, `streamBuf`, `ctrlC`.
   - Actually — `goal`, `phase`, `startTime`, `configName` are display state needed by PipelineScreen. Move them INTO PipelineScreen. The root model sets them when starting a pipeline: `m.pipelineScreen.goal = prompt`, `m.pipelineScreen.startTime = time.Now()`, `m.pipelineScreen.configName = m.configName`.
   - In `NewModel()`: replace viewport initializations with `pipelineScreen: NewPipelineScreen(configName)`.
   - In `Update()`:
     - `OrchestratorEventMsg`: call `m.pipelineScreen.ApplyEvent(event, m.width)` instead of `m.applyEvent(event)`. Then call `m.pipelineScreen.SyncViewports()`.
     - `tickMsg`: call `m.pipelineScreen.SyncViewports()` if in `StatePipeline`. Pass `streamBuf` snapshot data: add a `SetStreamSnapshot(agent string, lines []string, activities []Activity)` method on PipelineScreen, or have `ApplyEvent` / `SyncViewports` accept the streamBuf. **Simplest approach**: PipelineScreen holds a pointer to the `StreamBuffer` (set by parent after startPipeline). SyncViewports reads from it via `Snapshot()`. This is safe because StreamBuffer is already concurrent-safe.
     - `handleKey() case StatePipeline`: delegate to `m.pipelineScreen.Update(msg)`. Catch intent messages from the returned cmd. Process them:
       - `ApprovePlanIntent` → write `Decision{Type: DecisionApprove}` to `m.decisions`
       - `SkipGatewayIntent` → write `Decision{Type: DecisionSkip}` to `m.decisions`
       - `CancelPlanIntent` → write `Decision{Type: DecisionCancel}` to `m.decisions`
       - `EditPlanIntent` → write `Decision{Type: DecisionEdit, EditedContent: ...}` to `m.decisions`
       - `CommentPlanIntent` → write `Decision{Type: DecisionComment, Comment: ...}` to `m.decisions`
       - `CancelPipelineIntent` → call `m.cancel()`
       - `NavigateToPromptIntent` → `m.resetAndNavigateToPrompt(intent.PreFillGoal)`
       - `NavigateToRunsListIntent` → `m.navigateToRunsList()`
       - `OpenExternalEditorIntent` → return `openExternalEditor(intent.FilePath)`
       - `ConfirmNewRunIntent` → same as current confirm-new logic
     - **Intent catching pattern**: The parent calls `pipelineScreen.Update(msg)` which returns `(PipelineScreen, tea.Cmd)`. The parent then executes the cmd synchronously to check for intents: `if cmd != nil { intentMsg := cmd(); switch intent := intentMsg.(type) { ... } }`. For non-intent cmds (like textarea blink), re-wrap and return them.
   - `editorReturnMsg` handler: route to `m.pipelineScreen.editorRunning = false` and update `m.pipelineScreen.lastErr` / reload plan from file.
   - `recalculateLayout()`: call `m.pipelineScreen.RecalculateLayout(...)` with computed dimensions.
   - `View()`: for `StatePipeline`, return `m.pipelineScreen.View(m.width, m.height)`.
   - `startPipeline()`: after creating channels, set `m.pipelineScreen.streamBuf = channels.Stream` (or pass it via a Set method).
   - Remove `applyEvent()`, `resetPipelineState()`, `syncPipelineViewports()`, and all `view*` / `handle*Pipeline*` methods from model.go.

4. Update app_test.go:
   - All tests that hydrate pipeline state now set fields on `m.pipelineScreen` instead of `m`. For example:
     - `m.content = ContentCoaching` → `m.pipelineScreen.content = ContentCoaching`
     - `m.gatewayResult = ...` → `m.pipelineScreen.gatewayResult = ...`
     - `m.answerFields = ...` → `m.pipelineScreen.answerFields = ...`
     - `m.hasPlan = true` → `m.pipelineScreen.hasPlan = true`
     - `m.finalPlan = ...` → `m.pipelineScreen.finalPlan = ...`
     - `m.decisions = decisions` → stays on root `m.decisions` (parent owns channels)
     - `m.awaitingPlanDecision` → `m.pipelineScreen.awaitingPlanDecision`

5. Update layout_test.go: similarly adjust hydration paths for any pipeline-state fields.

**Done when:**

- `go test ./internal/tui/... -count=1` passes.
- File `internal/tui/screen_pipeline.go` exists.
- `wc -l internal/tui/model.go` reports < 350 lines.
- `grep -n 'handlePipelineKey\|handleCoachingKey\|handlePlanReviewKey\|viewPipelineScreen\|viewStreaming\|viewSidebar\|viewDashboard\|viewCompletion\|applyEvent\|resetPipelineState' internal/tui/model.go` returns zero matches.
- `grep -c 'Intent' internal/tui/screen_pipeline.go` returns at least 8 (intent usage).
- `go build ./cmd/orqestra` succeeds.

### 7. Verify and clean up residual coupling

Final pass to ensure no cross-screen field access remains and the router is clean.

**Steps:**

1. Run `go vet internal.` — fix any issues.
2. Verify model.go is a pure router: it should contain only `Model` struct (with screen sub-models + domain side-effects), `NewModel()`, `Init()`, `Update()` (router + intent dispatcher), `View()` (screen delegator), `handleKey()` (global keys + screen delegation), `handleMouse()`, `startPipeline()`, `recalculateLayout()`, and navigation helpers. No content rendering, no content-mode-specific key handling.
3. Verify no screen imports or references the `decisions` channel, `cancel` func, or `engine`. `grep 'decisions\|cancel\|engine' internal/tui/screen_*.go` should return zero matches for channel/func fields (only intent returns).
4. Run the full test suite: `go test ./internal/tui/... -count=1 -v`.
5. Run `go test ./... -count=1` to verify no cross-package breakage.
6. Run `go build ./cmd/orqestra` to verify clean compilation.

**Done when:**

- `go test ./... -count=1` passes.
- `go build ./cmd/orqestra` succeeds.
- `go vet Developer.` passes.
- `wc -l internal/tui/model.go` reports < 350 lines.
- `wc -l internal/tui/screen_pipeline.go` reports < 700 lines.
- `wc -l internal/tui/screen_prompt.go` reports < 250 lines.
- `wc -l internal/tui/screen_runs_list.go` reports < 150 lines.
- `wc -l internal/tui/screen_run_detail.go` reports < 250 lines.

## Verification

Commands the worker runs after ALL packages complete to confirm success:

- `go test ./internal/tui/... -count=1 -v`
- `go test ./... -count=1`
- `go build ./cmd/orqestra`
- `go vet Developer.`
- `wc -l model.go internal/tui/screen_*.go internal/tui/messages.go`
- `grep -rn 'SetContent' internal/tui/model.go` — should return zero matches (all SetContent lives in screen sync methods)
- `grep -rn '\.decisions\s*<-\|\.cancel()' internal/tui/screen_*.go` — should return zero matches (screens emit intents, never touch channels)

## Assumptions

1. The Bubbletea v1 `viewport.Model` is a value type where `SetContent()` mutates in place via value semantics (confirmed — it's a struct, not a pointer). This means viewport state is preserved across `Update()` → `View()` cycles only if the parent reassigns the screen struct after `Update()`.
2. `orchestrator.StreamBuffer` is safe to hold by pointer in `PipelineScreen` because it has its own mutex (confirmed at `orchestrator.go:51`).
3. The `intentCmd` pattern (execute the returned cmd synchronously to check for intents) works because Bubbletea v1 commands are `func() tea.Msg` — they can be called directly. Intent commands return immediately (no IO). Non-intent commands (like `textarea.Blink`) should be returned to the Bubbletea runtime as-is. The parent must distinguish: execute the cmd, if the result is an intent type → process it, otherwise → re-wrap and return the original cmd to the runtime.
4. `filepicker.go` defines the `filePicker` type and `readNextBatch` function as package-level symbols (not methods on Model). They can be consumed by `PromptScreen` without modification.
5. The `makeAnswerFields` test helper at `app_test.go:583` duplicates the answer field creation logic from `applyEvent()`. After extraction, both the test helper and `ApplyEvent()` on PipelineScreen should use the same logic.

## Gotchas

1. **`viewPipelineScreen()` reassigns viewport Width/Height inline** (lines `m.contentVP.Width = contentWidth`, `m.dashboardVP.Width = w` etc.) in addition to `recalculateLayout()`. After the hoist, these redundant reassignments must be removed or moved to `SyncViewports()`. If they remain in View(), they are mutations in a pure function.

2. **`handleStreamingKey` confirm-new flow** is stateful: pressing `"n"` sets `m.confirmNew = true`, then the NEXT keypress checks `m.confirmNew`. After extraction, this two-press flow must be preserved within PipelineScreen's state — the intent to confirm is different from the intent to execute.

3. **`editorReturnMsg` handler in model.go** reads `m.planFilePath` to reload the plan from disk after the editor closes. After extraction, `planFilePath` lives on PipelineScreen. The root model must route `editorReturnMsg` to the pipeline screen, which then reloads and emits an `EditPlanIntent` if the content changed. Alternatively, the root model reads `m.pipelineScreen.planFilePath` directly (simpler, acceptable since it's a read).

4. **The `applyEvent` drain loop in `OrchestratorEventMsg` handler** calls `m.applyEvent(ev)` in a tight `select` loop. After extraction, this becomes `m.pipelineScreen.ApplyEvent(ev, m.width)` in the loop. The `pipelineScreen` must be updated in-place via pointer receiver, and the final state assigned back to `m.pipelineScreen`. Since `ApplyEvent` is a pointer method, the worker must ensure the loop operates on `&m.pipelineScreen`, not a copy.

5. **`recalculateLayout()` is called from `navigateToRunsList()` and `handleRunsListKey` (on Esc/Enter/q).** After extraction, screens don't call `recalculateLayout()`. The parent must call it when processing navigation intents. If forgotten, the new screen renders with stale layout dimensions.

6. **`viewRunsListScreen` and `viewRunDetailScreen` use method receiver `(m Model)` but call `m.runsVP.SetContent()` which is a mutation.** Go allows this because `viewport.Model` is a struct — the mutation happens on a copy within the View call. This means the CURRENT code silently regenerates content every frame (the mutation is discarded). After the hoist, content is set once in Update and persisted. This is correct but means the output may differ slightly if the View was relying on frame-by-frame regeneration (e.g., timer display in `viewRunsListScreen`). Verify: `viewRunsListScreen` does NOT display live timers — it shows historical data. Safe.

7. **`formatDuration()` in runs.go is used by both `RunsListScreen.View()` and `RunDetailScreen.View()`.** It should remain in a shared file (keep in runs.go or move to `styles.go`). Do not duplicate it.

8. **`statusIcon()` in runs.go** is also used by `viewDashboard()` on PipelineScreen. After extraction, it must be accessible from both `screen_run_detail.go` and `screen_pipeline.go`. Keep it in a shared location (e.g., `styles.go` or a new `helpers.go`).
