# Plan: Gateway Agent — Specification Coach + TUI Split Layout

**Status**: Implementation-ready
**Scope**: Rename intent→gateway, evolve gateway behavior, introduce bi-directional orchestrator-TUI communication, rewrite TUI to stable 3-zone split layout, enable headless mode for E2E testing.

---

## Core Architecture Decision: Two Independent State Machines

The TUI and Pipeline are **two independent actors** communicating over typed channels:

```
┌─────────────┐      chan Event       ┌──────────────┐
│  Pipeline   │ ──────────────────▶   │     TUI      │
│  (Engine)   │                       │  (Bubble Tea)│
│             │ ◀──────────────────   │              │
└─────────────┘      chan Decision    └──────────────┘
```

- **Pipeline → TUI**: `<-chan orchestrator.Event` — streams phase changes, agent stats, gate requests, errors, completion
- **TUI → Pipeline**: `chan<- orchestrator.Decision` — sends gate responses (approve, answers, cancel)
- **Pipeline blocks at gates**, waiting for a Decision on the channel
- **TUI is always responsive** — user can scroll, navigate agents, view history while pipeline runs
- **Cancel = context cancellation** of current agent; run is marked cancelled; UI survives; prompt pre-filled for new run

This design is ready for future bi-directional steering (interrupt worker mid-turn) without architectural changes — only the agent's decision-handling logic changes.

---

## Phase 1: Mechanical Rename (intent/intake → gateway)

Already in progress. Tracked separately. Rename all type names, config keys, phase constants, TUI messages, CLI variable names. Delete `IntentVerdictReject` entirely.

---

## Phase 2: Gateway Output Schema + Behavior

### 2.1 Types — `internal/agent/gateway.go`

```go
type GatewayVerdict string

const (
    GatewayVerdictAccept GatewayVerdict = "accept"
    GatewayVerdictCoach  GatewayVerdict = "coach"
)

type GatewayResult struct {
    Verdict         GatewayVerdict  `json:"verdict"`
    Brief           PromptBrief     `json:"brief"`
    Questions       []Question      `json:"questions"`
    Confidence      float64         `json:"confidence"`
    PlannerQuestion string          `json:"planner_question"`
}

type PromptBrief struct {
    Task            string   `json:"task"`
    EndState        string   `json:"end_state"`
    Deliverables    []string `json:"deliverables"`
    Scope           []string `json:"scope"`
    NonScope        []string `json:"non_scope"`
    AcceptanceHints []string `json:"acceptance_hints"`
}

type Question struct {
    Text    string   `json:"text"`
    Options []string `json:"options"`
    Default string   `json:"default"`
}
```

### 2.2 Behavior Rules

1. **No reject verdict.** Gateway ALWAYS produces a `Brief` — its best interpretation of the user's intent
2. `accept`: Brief is complete, PlannerQuestion is ready. Pipeline proceeds immediately.
3. `coach`: Brief is partially populated (gateway's assumptions), Questions ask user to confirm/refine. Pipeline gates until answers arrive.
4. `PlannerQuestion` is ALWAYS a question form: "How should X be designed such that Y, given Z?" — provokes planner depth.
5. `Brief.Task` is the user's intent translated to LLM specification language — precise verbs, explicit package/file references, technical scope terms.
6. `Question.Default` pre-fills the gateway's assumption. User confirms with Enter or edits.
7. Max 3 questions per evaluation. Max 3 coaching rounds before auto-accept.

### 2.3 Validation in `Gateway.Evaluate()`

| Verdict  | Required non-empty fields                           |
| -------- | --------------------------------------------------- |
| `accept` | `Brief.Task`, `Brief.EndState`, `PlannerQuestion`   |
| `coach`  | `Questions` (len > 0), `Brief.Task` (partial is OK) |

### 2.4 Gateway uses RunStreaming

Gateway calls `RunStreaming` so the TUI can display the brief as it generates in real-time. The stream output is parsed as JSON after completion.

### 2.5 Test Scenarios — `internal/agent/gateway_test.go`

| Test                                    | Input                                                       | Expected                                                            |
| --------------------------------------- | ----------------------------------------------------------- | ------------------------------------------------------------------- |
| `TestGateway_AcceptClearPrompt`         | "Add token-bucket rate limiting to internal/api/gateway.go" | Verdict=accept, Brief.Task non-empty, PlannerQuestion is a question |
| `TestGateway_CoachVaguePrompt`          | "make it better"                                            | Verdict=coach, Questions non-empty, Brief partially populated       |
| `TestGateway_NeverRejects`              | "build me a SaaS platform"                                  | Verdict=coach (NOT reject), Questions scope-reduce                  |
| `TestGateway_BriefAlwaysPopulated`      | any input                                                   | Brief.Task is never empty regardless of verdict                     |
| `TestGateway_PlannerQuestionIsQuestion` | any accepted input                                          | PlannerQuestion ends with "?" or contains "how"/"what"/"which"      |
| `TestGateway_MaxThreeQuestions`         | vague input                                                 | len(Questions) <= 3                                                 |
| `TestGateway_InvalidJSON`               | garbled LLM output                                          | returns error, does not panic                                       |
| `TestGateway_StreamingParse`            | valid streamed output                                       | parses final JSON from stream correctly                             |

---

## Phase 3: Orchestrator Communication Model

### 3.1 Types — `internal/orchestrator/orchestrator.go`

```go
// GateType identifies which interactive gate the pipeline is waiting at.
type GateType int

const (
    GateGatewayCoach GateType = iota
    GatePlanApproval
    GateQAReview
)

// GateRequest is emitted when the pipeline needs user input.
type GateRequest struct {
    Type           GateType
    GatewayResult  *agent.GatewayResult  // for GateGatewayCoach
    PlanOutput     *agent.PlanOutput     // for GatePlanApproval
    QAReport       *agent.ValidationReport // for GateQAReview
}

// Decision is sent from TUI to pipeline at gates.
type Decision struct {
    Type            DecisionType
    GatewayAnswers  []GatewayAnswer       // for coaching gate
    EditedContent   string                // for [E] edit at Plan/QA gates
}

type DecisionType int

const (
    DecisionApprove  DecisionType = iota  // proceed
    DecisionEdit                          // proceed with modified payload
    DecisionSkip                          // skip gateway, use current brief
    DecisionCancel                        // cancel current agent / run
)

type GatewayAnswer struct {
    QuestionIndex int
    Answer        string
}
```

### 3.2 Engine API Change

```go
type Engine struct {
    Config        *config.Config
    Runners       Runners
    RunDirFactory RunDirFactory
}

type RunChannels struct {
    Events    <-chan Event       // pipeline → TUI
    Decisions chan<- Decision    // TUI → pipeline
}

func (e *Engine) Start(ctx context.Context, input Input) RunChannels
```

`Start` launches the pipeline in a goroutine. Returns channels immediately. Pipeline sends events, blocks at gates waiting for decisions. The `Engine` is responsible for closing the `Events` channel upon exit to prevent leaks, especially vital for testing and headless mode.

### 3.3 Gate Flow

```
Pipeline goroutine:
    1. emit(Event{Type: EventGateRequest, Gate: GateRequest{Type: GateGatewayCoach, ...}})
    2. select:
         case decision := <-decisions:
             switch decision.Type { ... }
         case <-ctx.Done():
             return ctx.Err()
    3. continue pipeline
```

### 3.4 Cancel Flow

- TUI sends `Decision{Type: DecisionCancel}` on the decisions channel
- Pipeline receives it at the current gate (if blocked) OR via context cancellation (if running)
- Pipeline emits `Event{Type: EventAgentCancelled, AgentID: "planner"}`
- Pipeline emits `Event{Type: EventComplete}` with `Status: StatusCancelled`
- TUI sidebar shows agent as cancelled, run as cancelled
- TUI pre-fills prompt with the last goal for new run

Additionally, the TUI holds `context.CancelFunc` and calls it on [S] to cancel the running agent's context mid-execution (not waiting for a gate).

### 3.5 Headless Mode

CLI flags: `--prompt "..." --auto-approve`

When both flags present:

- Prompt injected non-interactively (no TUI prompt screen)
- Gateway auto-accepts (sends `DecisionApprove` immediately at coaching gate)
- Plan approval auto-approves (sends `DecisionApprove` immediately)
- QA gate auto-accepts with warnings
- Enables E2E testing without interactive input

Implementation: a headless "TUI" that reads from Events channel and auto-responds to gates.

### 3.6 Test Scenarios — `internal/orchestrator/orchestrator_test.go`

| Test                             | Setup                                | Assert                                                                |
| -------------------------------- | ------------------------------------ | --------------------------------------------------------------------- |
| `TestEngine_GatewayCoachGate`    | Mock gateway returns coach verdict   | Events channel emits GateRequest, pipeline blocks until Decision sent |
| `TestEngine_GatewayAcceptNoGate` | Mock gateway returns accept          | No GateRequest emitted, pipeline proceeds to planner                  |
| `TestEngine_PlanApprovalGate`    | Mock planner returns plan            | GateRequest emitted with PlanOutput, blocks until approve             |
| `TestEngine_CancelAtGate`        | Send DecisionCancel at plan gate     | Pipeline emits StatusCancelled, exits cleanly                         |
| `TestEngine_CancelMidExecution`  | Cancel context during worker         | Worker returns context error, pipeline emits cancelled                |
| `TestEngine_SkipGateway`         | Send DecisionSkip at coaching gate   | Pipeline passes raw prompt to planner                                 |
| `TestEngine_HeadlessAutoApprove` | Input.AutoApprove = true             | All gates auto-approve, pipeline completes                            |
| `TestEngine_GatewayCoachingLoop` | First eval=coach, second eval=accept | Two gateway calls, one gate, then proceeds                            |

---

## Phase 4: TUI — Stable 3-Zone Split Layout

### 4.1 Layout Architecture

```
┌───────────────────────────────────────────┬──────────────┐
│                                           │  SIDEBAR     │
│  CONTENT (context-dependent, scrollable)  │  agent list  │
│                                           │  + status    │
│  75% width                                │  25% width   │
│  ~80% height                              │  ~80% height │
│                                           │              │
├───────────────────────────────────────────┴──────────────┤
│ INPUT LINE (editable field or status)           ~2 lines  │
├──────────────────────────────────────────────────────────┤
│ FOOTER (persistent key legend)                   1 line   │
└──────────────────────────────────────────────────────────┘
```

Rendered via `lipgloss.JoinHorizontal(lipgloss.Top, contentView, sidebarView)` for the top portion, then `lipgloss.JoinVertical(lipgloss.Left, topRow, inputLine, footer)` for the full frame.

**Critical Event Loop Rule**: The TUI `Update()` func must NEVER block to read `Events`. It must return a recursive `tea.Cmd` (e.g., `waitForPipeline(events) tea.Msg`) that awaits the next pipeline event, yielding a BubbleTea message without freezing the render loop.

### 4.2 State Model — `internal/tui/model.go`

```go
type AppState int

const (
    StatePrompt   AppState = iota  // full-screen prompt entry (pre-pipeline)
    StatePipeline                  // 3-zone split layout (pipeline running/done)
    StateHelp                      // full-screen help overlay
)

type ContentMode int

const (
    ContentStreaming   ContentMode = iota  // auto-follows active agent stream
    ContentCoaching                       // gateway brief + questions
    ContentPlanReview                     // rendered spec
    ContentPlanEdit                       // editable spec in full-screen textarea
    ContentQAReview                       // QA report
    ContentQAEdit                         // editable QA context in full-screen textarea
    ContentAgentHistory                   // read-only past agent output (user navigated)
    ContentCompletion                     // summary + files changed
)

type Model struct {
    state         AppState
    content       ContentMode
    width, height int
    startTime     time.Time
    goal          string

    // Pipeline communication
    events    <-chan orchestrator.Event
    decisions chan<- orchestrator.Decision
    cancel    context.CancelFunc

    // Sidebar state
    agents       []AgentRow
    sidebarScroll int
    focusedAgent  int  // -1 = auto (follow active), 0+ = user-selected

    // Content state
    gatewayResult  *agent.GatewayResult
    planOutput     *agent.PlanOutput
    qaReport       *agent.ValidationReport
    completionResult *orchestrator.Result
    agentStreams   map[string]*ringbuf.Buffer  // agent output history

    // Input state
    prompt        textarea.Model
    answerFields  []textarea.Model  // one per gateway question
    answerCursor  int

    // UI state
    ctrlC         int
    scrollOffset  int
}
```

### 4.3 Content Modes

| Mode                  | Content Zone Shows                                   | Input Zone Shows                              | Triggered By                         |
| --------------------- | ---------------------------------------------------- | --------------------------------------------- | ------------------------------------ |
| `ContentStreaming`    | Active agent's stream buffer (auto-follow)           | Status: "planner running..."                  | Default during execution             |
| `ContentCoaching`     | Gateway's Brief + Questions with pre-filled defaults | Answer fields (editable)                      | `EventGateRequest{GateGatewayCoach}` |
| `ContentPlanReview`   | Rendered spec (goal, steps, acceptance)              | Status: "[A] accept \| [E] edit"              | `EventGateRequest{GatePlanApproval}` |
| `ContentPlanEdit`     | Bubble Tea `textarea` containing Markdown spec       | Status: "[Ctrl+S] save edits \| [Esc] cancel" | User presses [E] during Review       |
| `ContentQAReview`     | QA validation report                                 | Status: "[A] accept \| [F] fix \| [E] edit"   | `EventGateRequest{GateQAReview}`     |
| `ContentQAEdit`       | Bubble Tea `textarea` for additional fix context     | Status: "[Ctrl+S] fix with edits \| [Esc]"    | User presses [E] during QA Review    |
| `ContentAgentHistory` | Selected agent's frozen output (scrollable)          | Status: "viewing gateway history (read-only)" | User presses [1-9]                   |
| `ContentCompletion`   | Summary, files changed, token totals                 | "[N] new run \| [Q] quit"                     | `EventComplete`                      |

### 4.4 Sidebar Rendering

```
 gateway   ✓  3s    1.2k
 planner   ▶  24s  12.4k
 validator ○   -      -
 workers (0/3) ○     -
─────────────────────
 total: 13.6k | 27s
```

- Max 10 visible rows, scrollable with sidebar focus
- Workers collapsed into single row: "workers (2/3 done)"
- Expand workers in full dashboard view only ([D])
- State icons: `▶` running, `✓` done, `○` waiting, `✗` failed, `●` gate (needs input), `⊘` cancelled

### 4.5 Footer (always visible, single line)

```
 [Enter] confirm | [Ctrl+S] skip       [?] help  [D] expand  [1-9] agent  [S] stop  [N] new  [^C^C] quit
 ╰── context keys (left) ──╯           ╰────────────── persistent keys (right) ─────────────────────────╯
```

Context keys change per content mode:

- Coaching: `[Enter] confirm | [Ctrl+S] skip`
- Plan review: `[A] accept | [E] edit`
- Plan edit: `[Ctrl+S] save edits | [Esc] discard edits`
- QA review: `[A] accept | [F] fix | [E] edit instruction`
- QA edit: `[Ctrl+S] fix with edits | [Esc] discard edits`
- Agent history: `[Esc] back to live`
- Streaming: (none, just persistent keys)

### 4.6 Key Bindings

| Key               | Context            | Action                                                              |
| ----------------- | ------------------ | ------------------------------------------------------------------- |
| `Enter`           | Prompt screen      | Submit prompt, start pipeline                                       |
| `Enter`           | Coaching           | Submit answers, re-evaluate gateway                                 |
| `Tab / Shift+Tab` | Coaching           | Cycle focus forward/backward between question input fields          |
| `Ctrl+S`          | Prompt screen      | Skip gateway, raw prompt to planner                                 |
| `Ctrl+S`          | Coaching           | Skip remaining questions, proceed with current brief                |
| `PgUp / PgDn`     | Any state          | Scroll the content zone (safest across all terminals vs Cmd+Arrows) |
| `A`               | Plan review        | Accept plan, continue to workers                                    |
| `E`               | Plan review        | Switch to ContentPlanEdit mode for text modification                |
| `Ctrl+S`          | Plan edit          | Send DecisionEdit with modified plan content                        |
| `Esc`             | Plan edit          | Discard edits, return to ContentPlanReview                          |
| `A`               | QA review          | Accept with warnings                                                |
| `F`               | QA review          | Re-run workers with existing QA feedback                            |
| `E`               | QA review          | Switch to ContentQAEdit to append instructions                      |
| `Ctrl+S`          | QA edit            | Send DecisionEdit with modified QA instructions                     |
| `Esc`             | QA edit            | Discard edits, return to ContentQAReview                            |
| `S`               | Any pipeline state | Cancel current agent. Run marked cancelled.                         |
| `N`               | Any state          | New run. Pre-fills prompt. Confirms if pipeline active.             |
| `D`               | Pipeline state     | Toggle full dashboard (expand sidebar to full screen)               |
| `1-9`             | Pipeline state     | Focus content on agent N (read-only history)                        |
| `Esc`             | Agent history      | Return to live content (auto-follow active)                         |
| `Esc`             | Full dashboard     | Return to split view                                                |
| `?`               | Any state          | Toggle help overlay                                                 |
| `Q`               | Completion         | Quit app                                                            |
| `Ctrl+C Ctrl+C`   | Any state          | Quit app                                                            |

### 4.7 Forward-Correction vs. Plan Rejection

There is no formal "reject plan" action. Instead, the workflow defaults to forward-correction. Users can either:

- **Accept** (`A`) and pipeline continues
- **Edit** (`E`) to manually tweak, prune, or augment the plan inline before proceeding
- **Cancel** (`S`) the run entirely → pipeline stops, UI shows cancelled, prompt pre-filled for new run

Rationale: Hard rejection implies navigating backwards state machines. Orqestra runs are forward-only. If the gateway/planner misses the mark slightly, `[E] edit` fixes it locally. If it completely hallucinates, cancel and restart with a better prompt.

### 4.8 Full Dashboard Override ([D])

Replaces content+sidebar with the detailed agent table:

```
 Agent          State     Time   In Tok   Out Tok  Tok/s  Context
 gateway        ✓ done    3s     1,218    402      134.0  █░░░░░░░  4%
 planner        ▶ run     24s    8,741    2,319    68.1   ██████░░ 45%
 validator      ○ wait    -      -        -        -      ░░░░░░░░  -
 worker-1       ○ wait    -      -        -        -      ░░░░░░░░  -
 worker-2       ○ wait    -      -        -        -      ░░░░░░░░  -
 worker-3       ○ wait    -      -        -        -      ░░░░░░░░  -
```

Esc returns to split view. This is the only place workers are shown individually.

### 4.9 Test Scenarios — `internal/tui/app_test.go`

| Test                        | Action                         | Assert                                                             |
| --------------------------- | ------------------------------ | ------------------------------------------------------------------ |
| `TestTUI_PromptSubmit`      | Type "task", press Enter       | State transitions to StatePipeline, events channel receives prompt |
| `TestTUI_PromptSkipGateway` | Press Ctrl+S                   | Pipeline starts without gateway, DecisionSkip sent                 |
| `TestTUI_CoachingRender`    | Receive GateGatewayCoach event | ContentMode=ContentCoaching, brief rendered, answer fields visible |
| `TestTUI_CoachingSubmit`    | Fill answers, press Enter      | DecisionApprove sent with GatewayAnswers                           |
| `TestTUI_CoachingSkip`      | Press Ctrl+S during coaching   | DecisionSkip sent, pipeline proceeds                               |
| `TestTUI_PlanApproval`      | Receive GatePlanApproval event | ContentMode=ContentPlanReview, spec rendered                       |
| `TestTUI_PlanApprove`       | Press A                        | DecisionApprove sent                                               |
| `TestTUI_CancelAgent`       | Press S during execution       | context cancelled, sidebar shows cancelled                         |
| `TestTUI_AgentNavigation`   | Press 1 during planner run     | ContentMode=ContentAgentHistory, shows gateway output              |
| `TestTUI_AgentNavBack`      | Press Esc from agent history   | ContentMode returns to ContentStreaming                            |
| `TestTUI_NewRun`            | Press N after completion       | State=StatePrompt, prompt pre-filled                               |
| `TestTUI_NewRunConfirm`     | Press N during active run      | Confirmation shown before cancelling                               |
| `TestTUI_SidebarUpdates`    | Stream of agent events         | Sidebar rows update in real-time                                   |
| `TestTUI_FullDashboard`     | Press D                        | Full dashboard rendered, Esc returns to split                      |
| `TestTUI_DoubleCtrlC`       | Press Ctrl+C twice             | Program exits                                                      |
| `TestTUI_PlanEditSave`      | Modifies plan textarea, Accept | `DecisionEdit` sent with updated plan payload                      |
| `TestTUI_PlanEditDiscard`   | Press Esc in PlanEdit mode     | Mode reverts to `ContentPlanReview`, payload unchanged             |
| `TestTUI_QAEditSave`        | Modifies QA textarea, Fix      | `DecisionEdit` sent with attached instruction string               |

---

## Phase 5: System Prompt — Specification Coach

### 5.1 File: `internal/config/pipeline.yaml` — `gateway:` block

The system prompt instructs the gateway model to:

1. **Translate** user prose into LLM specification language (precise verbs, explicit file/package references, technical scope terms). NOT grammar fixing — specification precision.
2. **Assume** deliverables and acceptance criteria proactively. Fill `Brief` with best-guess assumptions.
3. **Ask confirmation questions** with pre-filled defaults: "Are these deliverables correct? [Yes / Add more...]"
4. **Frame the PlannerQuestion** to provoke deep thinking: "How should X be designed and implemented such that Y is achieved, given constraints Z?"
5. **Coach toward engineering-leader prompts** — outcomes not implementation, observable end states not process steps.
6. **Never reject.** Impossibly broad prompts → scope-reduce and ask "did you mean this slice?"
7. **Max 3 questions** per evaluation round. Each question has options + a freeform field.
8. **Bias toward accept** — if user names a file, package, command, or feature, accept with enriched brief.
9. **Don't ask implementation questions** the planner can answer by reading the repo.

### 5.2 JSON Output Schema (in system prompt)

```json
{
  "verdict": "accept|coach",
  "brief": {
    "task": "string: user intent in LLM specification language",
    "end_state": "string: observable result in repo terms",
    "deliverables": ["string: concrete files, tests, or changes"],
    "scope": ["string: packages/files/features in scope"],
    "non_scope": ["string: explicitly excluded"],
    "acceptance_hints": ["string: testable criteria"]
  },
  "questions": [
    { "text": "string", "options": ["string"], "default": "string" }
  ],
  "confidence": 0.0,
  "planner_question": "string: task framed as a deep-thinking question for the planner"
}
```

### 5.3 Coaching Loop Cap

After 3 rounds of `coach` verdict without reaching `accept`, the gateway's current best `Brief` is auto-accepted. TUI shows notice: "Proceeding with current understanding after 3 coaching rounds."

---

## Phase 6: Orchestrator + Planner Contract

### 6.1 Planner Input

- When gateway produces `accept`: planner receives `GatewayResult.PlannerQuestion`
- When gateway is skipped (Ctrl+S / DecisionSkip): planner receives the raw user prompt as-is
- When coaching loop cap reached: planner receives auto-constructed question from `Brief.Task` + `Brief.EndState`

### 6.2 Orchestrator Flow (simplified)

```
start:
    emit(PhaseGateway)
    gwResult := gateway.Evaluate(prompt)         // RunStreaming
    if gwResult.Verdict == "coach":
        emit(GateRequest{GateGatewayCoach, gwResult})
        decision := <-decisions                   // BLOCK
        if decision == Skip:
            plannerInput = prompt
            goto plan
        answers := decision.GatewayAnswers
        prompt = incorporateAnswers(prompt, answers)
        goto start                                // re-evaluate (max 3x)
    plannerInput = gwResult.PlannerQuestion

plan:
    emit(PhasePlanning)
    planOutput := planner.Plan(plannerInput)      // RunStreaming

plan_gate:
    emit(GateRequest{GatePlanApproval, planOutput})
    decision := <-decisions                       // BLOCK
    if decision == Cancel:
        return StatusCancelled
    if decision == Edit:
        planOutput.RawContent = decision.EditedContent
        // Proceed with modified plan

qa_loop:
    emit(PhaseValidating)
    ...workers...QA...
    qaReport := validator.Run(planOutput)
    emit(GateRequest{GateQAReview, qaReport})
    
    // Bubble Tea textarea intercepts keymaps to allow Tab focus cycling
    // Textarea bindings for edit saves are Ctrl+S.
    
    decision = <-decisions
    if decision == Cancel:
        return StatusCancelled
    if decision == Edit:
        qaReport.Instructions = append(qaReport.Instructions, decision.EditedContent)
        goto qa_loop // (Assuming Fix semantics implied)
    if decision == Fix:
        goto qa_loop
```

---

## Files to Create/Modify

| File                                         | Action                   | Key Changes                                                                                         |
| -------------------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------- |
| `internal/agent/gateway.go`                  | Modify (already renamed) | New types: GatewayResult, PromptBrief, Question. New method: Gateway.Evaluate(). Uses RunStreaming. |
| `internal/agent/gateway_test.go`             | Modify                   | 8 test scenarios from §2.5                                                                          |
| `internal/orchestrator/orchestrator.go`      | Rewrite                  | Channel-based Engine.Start(). Gate types. Decision types. Coaching loop.                            |
| `internal/orchestrator/orchestrator_test.go` | Rewrite                  | 8 test scenarios from §3.6                                                                          |
| `internal/tui/model.go`                      | Rewrite                  | AppState + ContentMode model. Split layout rendering via lipgloss. Channel subscription.            |
| `internal/tui/messages.go`                   | Rewrite                  | Remove Screen enum. Add ContentMode, AppState. Replace message types.                               |
| `internal/tui/tui.go`                        | Modify                   | Wire Engine.Start() channels. Event subscription loop as tea.Cmd.                                   |
| `internal/tui/styles.go`                     | Modify                   | Add sidebar styles, split layout styles.                                                            |
| `internal/tui/app_test.go`                   | Create                   | 16 test scenarios from §4.9                                                                         |
| `internal/config/config.go`                  | Modify                   | GatewayConfig (from IntentConfig rename). Add AutoApprove to PipelineConfig.                        |
| `internal/config/pipeline.yaml`              | Modify                   | gateway: system prompt rewrite                                                                      |
| `cmd/orqestra/main.go`                       | Modify                   | --prompt and --auto-approve flags. Headless mode. Channel wiring.                                   |

---

## Verification Commands

```sh
# Phase 2: Gateway types and behavior
go test ./internal/agent -run TestGateway -race -v
# Expect: 8 tests pass (Accept, Coach, NeverRejects, BriefAlwaysPopulated, PlannerQuestionIsQuestion, MaxThreeQuestions, InvalidJSON, StreamingParse)

# Phase 3: Orchestrator communication
go test ./internal/orchestrator -race -v
# Expect: 8 tests pass (CoachGate, AcceptNoGate, PlanApprovalGate, CancelAtGate, CancelMidExecution, SkipGateway, HeadlessAutoApprove, CoachingLoop)

# Phase 4: TUI rendering and interaction
go test ./internal/tui -race -v
# Expect: 16 tests pass (all §4.9 scenarios)

# Phase 5: Config loads correctly
go test ./internal/config -race -v

# Integration: everything compiles
go build ./cmd/orqestra

# Rename verification: no old names remain
grep -rn "IntentConfig\|IntentVerdict\|Recognizer\|NewRecognizer\|PhaseIntent\|IntentResultMsg" --include='*.go' .
# Expect: exit 1 (no matches)

grep -rn "VerdictReject\|IntentVerdictReject" --include='*.go' .
# Expect: exit 1 (no matches)

# Full suite
go test -race ./...

# Headless E2E (requires local model)
go run ./cmd/orqestra --prompt "Add a comment '// E2E' to cmd/orqestra/main.go" --auto-approve --config local
```

---

## Constraints

1. Do not add a "reject" verdict or plan rejection UI action.
2. Do not add separate packages for gateway types — they live in `internal/agent`.
3. Do not use discrete Screen transitions during pipeline execution — use ContentMode within stable frame.
4. Do not make the orchestrator depend on `*tea.Program` — communicate via channels only.
5. Do not add screen transitions that cause visible flashing — content zone updates in-place.
6. Do not parse gateway JSON with string slicing — use `encoding/json` with typed structs.
7. Do not use `time.Sleep` in tests — use channels and deterministic hooks.
8. Do not block the TUI event loop — all pipeline communication is async via channels and `tea.Cmd`.

---

## Out of Scope (separate plans)

- **Runs list UI** — view past run history, browse artifacts (→ `z_plans/plan-runs-list.md`)
- **Top-level navigation** — tab bar with dashboard / runs / help / config
- **Worker mid-turn interruption** — send guidance to running worker via `--continue`
- **Context auto-compaction** — `/compact` when context exceeds 60%
- **Git worktree isolation** — workers execute in isolated worktree
