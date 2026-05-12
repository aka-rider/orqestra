# Plan: AskUserQuestion Tool — End-to-End

## TL;DR

`AskUserQuestion` is already a **built-in deferred tool** in Claude Code (schema: `{"question":"string"}`), but it cannot resolve in `-p` mode (non-interactive) which is how Orqestra drives harnesses. We implement it as a **custom MCP tool** with a richer schema (options, multi-select, inline hints) served by `orqestra mcp-bridge`. The built-in `AskUserQuestion` is disallowed to prevent unresolvable calls; the MCP version replaces it.

## Architecture

```
Claude CLI ←stdio/MCP JSON-RPC→ orqestra mcp-bridge ←Unix socket→ QuestionBridge goroutine ←channels→ Orchestrator ←events→ TUI
```

Claude CLI starts the MCP bridge as a managed subprocess (via `--mcp-config`). When the model calls `mcp__orqestra__AskUserQuestion`, Claude CLI routes it to the bridge via JSON-RPC. The bridge connects to the main Orqestra process's Unix socket, sends the question, blocks for the answer. The orchestrator's bridge goroutine reads from the socket, emits an `EventUserQuestion`, the TUI renders the question picker, user answers, answer flows back through socket → bridge → CLI → model continues.

Key insight: `RunStreaming()` is blocking in the orchestrator goroutine, so question exchange happens out-of-band. The MCP tool call naturally blocks the model's turn until the tool result returns.

---

## Phase 1: Types & Protocol (no deps between files, parallelizable)

### Step 1.1 — Question/Answer domain types

Add to `internal/orchestrator/orchestrator.go`:

- `UserQuestion` struct: `AgentID string`, `Question string`, `Options []QuestionOption`, `AllowCustom bool`, `MultiSelect bool`
- `QuestionOption` struct: `Label string`, `Hint string`
- `QuestionAnswer` struct: `SelectedIndices []int`, `CustomTexts map[int]string`, `Skipped bool`
- New `EventUserQuestion EventType` constant
- Add `UserQuestion UserQuestion` field to `Event` struct

### Step 1.2 — TUI message types

Add to `internal/tui/messages.go`:

- `SubmitQuestionAnswerIntent` struct with `Answer orchestrator.QuestionAnswer` — implements `intent`

---

## Phase 2: MCP Bridge Server

### Step 2.1 — MCP bridge server implementation

Create `internal/harness/mcp_server.go`:

- Minimal MCP JSON-RPC 2.0 server: handles `initialize`, `notifications/initialized`, `tools/list`, `tools/call`
- Single tool: `AskUserQuestion` with input schema:
  ```json
  {
    "question": "string (required)",
    "options": [{ "label": "string", "hint": "string (optional)" }],
    "allow_custom": "boolean (default true)",
    "multi_select": "boolean (default false)"
  }
  ```
- On `tools/call`: serialize question to Unix socket, block reading answer, return formatted answer as text content
- Socket protocol: length-prefixed JSON frames (4-byte big-endian length + JSON payload)
- `RunMCPServer(socketPath string) error` entry point — reads stdin, writes stdout
- Tests: unit test JSON-RPC message handling with mock socket

### Step 2.2 — `mcp-bridge` subcommand

Add to `cmd/orqestra/main.go`:

- `case "mcp-bridge":` in the subcommand switch
- Requires `--socket <path>` flag
- Calls `harness.RunMCPServer(socketPath)`
- Clean exit on stdin close (MCP lifecycle)

---

## Phase 3: Question Bridge (socket listener)

### Step 3.1 — Question bridge implementation

Create `internal/harness/question_bridge.go`:

- `QuestionBridge` struct: `socketPath string`, `listener net.Listener`, `questions chan UserQuestion`, `pendingAnswer chan QuestionAnswer`
- `NewQuestionBridge(socketPath string) *QuestionBridge`
- `Start(ctx context.Context) error` — creates Unix socket, starts accept loop goroutine
- Accept loop: for each connection, read question JSON → send to `questions` chan → block on `pendingAnswer` chan → write answer JSON → close connection
- `Stop()` — close listener, unlink socket file
- `SocketPath() string` — for MCP config injection
- `Questions() <-chan UserQuestion` — TUI reads from this (via orchestrator event forwarding)
- `SendAnswer(answer QuestionAnswer)` — writes to `pendingAnswer` chan
- Socket protocol matches Step 2.1 (length-prefixed JSON)
- Tests: unit test socket round-trip with a mock client

---

## Phase 4: Orchestrator Integration

### Step 4.1 — Wire bridge into Engine

Modify `internal/orchestrator/orchestrator.go`:

- Add `QuestionBridge *harness.QuestionBridge` field to `Engine`
- In `run()`, before research phase: start a forwarding goroutine that reads `bridge.Questions()` and calls the existing `emit()` function (which writes to the events channel consumed by the TUI's `waitForEvent()` tea.Cmd loop). **Do NOT use `program.Send()` or direct struct mutation** — `emit()` is the correct mechanism already in use by all other events.
- Add `QuestionAnswers chan<- orchestrator.QuestionAnswer` to `RunChannels`
- In `Start()`: create the question answer channel, wire it to bridge

### Step 4.2 — Wire bridge into runner creation

Modify `cmd/orqestra/main.go` `buildEngine()`:

- Determine socket path: `filepath.Join(os.TempDir(), fmt.Sprintf("orqestra-q-%d.sock", os.Getpid()))`
- Create `QuestionBridge` with this path
- Resolve `orqestra` binary path via `os.Executable()`
- Create a new `harness.ClaudeCLIOption`: `WithMCPBridgeServer(binaryPath, socketPath string)` — injects the bridge as an additional MCP server
- Apply this option to **gateway, researcher, and planner** runners (NOT worker)
- For gateway: the bridge is the ONLY MCP server. Set `allowed_tools: [mcp__orqestra__AskUserQuestion]` — no Read, Grep, Bash. Gateway remains a pure intake filter that can ask questions but cannot explore the codebase.
- For researcher and planner: the bridge is added alongside their existing MCP servers and allowed tools

### Step 4.3 — MCP config merging + disallow built-in AskUserQuestion

Modify `internal/harness/claude_cli.go`:

- Add `WithInlineMCPServer(name, command string, args []string) ClaudeCLIOption` — stores on `ClaudeCLI` struct
- Modify `filterMCPConfig` or add a new function that merges user MCP servers with inline servers
- When building CLI args, produce a single unified `--mcp-config` JSON that includes both user servers and inline bridge server
- The bridge MCP server entry: `{"command": "<binary>", "args": ["mcp-bridge", "--socket", "<socketPath>"]}`
- Auto-add `mcp__orqestra__AskUserQuestion` to allowed tools when bridge is configured
- Auto-add built-in `AskUserQuestion` to `--disallowed-tools` to prevent the model from calling the unresolvable built-in version (it would hang in `-p` mode)

---

## Phase 5: TUI — Question UI Component

### Step 5.1 — New ContentMode and screen state

Modify `internal/tui/screen_pipeline.go`:

- Add `ContentUserQuestion ContentMode` constant
- Add fields to `PipelineScreen`:
  - `userQuestion orchestrator.UserQuestion`
  - `questionCursor int` (highlighted option index)
  - `questionSelected map[int]bool` (multi-select state)
  - `questionCustom map[int]string` (custom text per option)
  - `questionCustomActive int` (which option has custom input expanded, -1 = none)
  - `questionCustomTA textarea.Model` (the inline custom text textarea)
  - `questionFreetext textarea.Model` (for freeform-only questions)
  - `questionFreetextActive bool`

### Step 5.2 — Event handling for questions

In `PipelineScreen.ApplyEvent()`:

- Handle `EventUserQuestion`: set `content = ContentUserQuestion`, populate question fields, reset selection state
- If question has no options: activate freetext mode (textarea)
- If question has options: set cursor to 0, initialize selection map

### Step 5.3 — Key handling for question mode

Add `handleUserQuestionKey(msg tea.KeyPressMsg)` to `PipelineScreen`:

- **↑/↓ or k/j**: navigate option cursor
- **Space**:
  - **Single-select** (`MultiSelect == false`): select highlighted, deselect all others (radio behavior)
  - **Multi-select** (`MultiSelect == true`): toggle highlighted on/off (checkbox behavior)
- **Tab**: expand/collapse inline custom text input for highlighted option
  - When expanding: create a 1-line textarea positioned inline, focus it; set `questionCustomActive = cursor`
  - When collapsing: save `questionTextarea.Value()` into `questionCustom[questionCustomActive]`, blur textarea; set `questionCustomActive = -1`
- **Enter**:
  - If custom textarea is active: confirm custom text (same as Tab-collapse), return to option navigation — do NOT confirm the whole answer yet
  - Otherwise: confirm selection → build `QuestionAnswer` → set `PendingIntent = SubmitQuestionAnswerIntent{...}` → switch to `ContentStreaming`
  - **Single-select shortcut**: if nothing selected yet, Enter on highlighted option selects it AND confirms in one keystroke
- **Esc**:
  - If custom textarea is active: cancel custom edit, discard text, restore `questionCustom[questionCustomActive]` to previous value (or delete key if empty), return to option navigation; set `questionCustomActive = -1`
  - Otherwise: skip question → set `PendingIntent = SubmitQuestionAnswerIntent{Answer: {Skipped: true}}` → switch to `ContentStreaming`
- When custom textarea is active: pass all other keys to textarea

`buildQuestionAnswer()` **must** populate `MCPAnswer.CustomTexts` from `questionCustom`:
```go
func (s PipelineScreen) buildQuestionAnswer() harness.MCPAnswer {
    // ... build SelectedIndices from questionSelected ...
    customTexts := make(map[int]string)
    for idx, text := range s.questionCustom {
        if strings.TrimSpace(text) != "" {
            customTexts[idx] = strings.TrimSpace(text)
        }
    }
    if len(customTexts) > 0 {
        answer.CustomTexts = customTexts
    }
    return answer
}
```
**This is not optional** — `FormatAnswer()` in `mcp_server.go` already renders `CustomTexts` in the tool result; if `buildQuestionAnswer()` omits them the model silently receives degraded output.

### Step 5.4 — Rendering

Add `viewUserQuestion(width int) string` to `PipelineScreen`:

- Render question header: agent name + question text in `goalStyle`
- For **single-select** questions, use radio indicators:

  ```
   researcher asks:
   How should missing config keys be handled?

   ❯ ● Return a structured error with the key path    , ⇥ add context
     ○ Log warning and use zero-value defaults         , ⇥ explain why
     ○ Panic with a descriptive message                , ⇥ explain why

   ↑↓ navigate  space select  ⇥ add context  ⏎ confirm  esc skip
  ```

- For **multi-select** questions, use checkbox indicators + count badge:

  ```
   planner asks:
   Which verification commands should the worker run? (select all that apply)

   ❯ ◼ go test ./internal/config/...                   , ⇥ add context
     ◼ go build ./cmd/orqestra                         , ⇥ add context
     ◻ go vet ./...                                    , ⇥ add context
     ◻ golangci-lint run                               , ⇥ add context

   2 selected  ↑↓ navigate  space toggle  ⇥ add context  ⏎ confirm  esc skip
  ```

- Inline hint: `, ⇥ <hint>` rendered in dim+faint style
- Custom text when expanded (indented with gutter):
  ```
   ❯ ◼ go test ./internal/config/...
     ┊ Also run with -race flag█
  ```
- For freeform questions (no options): render question text + full textarea
- Footer hint line differs by mode:
  - Single: `↑↓ navigate  space select  ⇥ add context  ⏎ confirm  esc skip`
  - Multi: `N selected  ↑↓ navigate  space toggle  ⇥ add context  ⏎ confirm  esc skip`
  - Freeform: `⏎ confirm  esc skip`

Add to `viewInputZone()`:

- `ContentUserQuestion` case: context-sensitive hint text

Add to `viewContent()`:

- Route `ContentUserQuestion` → `viewUserQuestion()`

### Step 5.5 — Intent dispatch

Modify `internal/tui/model.go` `handlePipelineKey()`:

- Add case `SubmitQuestionAnswerIntent` — **must return a `tea.Cmd`**, never block `Update()`:
  ```go
  case SubmitQuestionAnswerIntent:
      bridge := m.engine.QuestionBridge
      ans := i.Answer
      return m, func() tea.Msg {
          bridge.SendAnswer(ans)
          return nil
      }
  ```
  **Why**: `SendAnswer()` writes to a buffered channel (cap 1). If the bridge goroutine hasn't drained the previous answer yet (e.g. rapid sequential questions), a direct write in `Update()` blocks the Bubble Tea event loop indefinitely. Wrapping in `tea.Cmd` offloads the write to a goroutine, keeping `Update()` non-blocking per TUI architecture rules.

Modify `Model`:

- Access bridge via `m.engine.QuestionBridge` (engine is already on Model — no separate field needed)
- Remove the `questionBridge *harness.QuestionBridge` field proposal — redundant, creates two paths to the same object

### Step 5.6 — Styles

Add to `internal/tui/styles.go`:

- `questionHintStyle` — `dimStyle` + `Faint(true)`, low-contrast inline hint
- `questionSelectedStyle` — `passStyle` (green), selected option indicator
- `questionCursorStyle` — `phaseStyle` (cyan), cursor indicator
- `questionGutterStyle` — `dimStyle`, `┊` gutter for custom text

### Step 5.7 — Decisions channel buffering invariant

The `decisions` channel (type `chan<- orchestrator.Decision`) is written directly from `handlePipelineKey()` inside `Update()`. This is safe only if the channel is always buffered with capacity ≥ 1, ensuring the write never blocks when the orchestrator is momentarily not receiving.

**Requirement**: In `orchestrator.go` `Start()`, always create `decisions` with `make(chan Decision, 1)`. Add a test asserting that sending on `decisions` does not block when orchestrator is not yet reading. This invariant applies to all intent dispatches that write to `decisions`: `ApprovePlanIntent`, `EditPlanIntent`, `CommentPlanIntent`, `CancelPlanIntent` — not just the new `SubmitQuestionAnswerIntent`.

---

## Phase 6: Gateway Unification & System Prompt Updates

### Step 6.1 — Gateway prompt overhaul

Rewrite gateway `system_prompt` in `internal/config/pipeline.yaml`:

- Remove `mcp_servers: []` — gateway now gets the bridge MCP server
- Remove coaching output instructions ("COACHING TRIGGERS", "COACHING VOICE", questions array in JSON schema)
- Add: `"If user intent is ambiguous, call AskUserQuestion with 2-3 clear options and a recommended default. Once you have clarity, output verdict 'accept'. Never output verdict 'coach' — resolve ambiguity yourself via the tool."`
- Simplify JSON schema: remove `questions` array from output. Keep `verdict` (now always `"accept"` after tool-assisted clarification), `brief`, `confidence`
- Keep all existing ACCEPT BIAS rules, GROUNDING RULES, BRIEF FIELDS

### Step 6.2 — Gateway code cleanup

Modify `internal/agent/gateway.go`:

- Remove validation: `if gwResult.Verdict == GatewayVerdictCoach && len(gwResult.Questions) == 0` — coach verdict is no longer emitted
- Remove validation: `if len(gwResult.Questions) > 3` — questions are asked via tool, not JSON output
- Keep `GatewayVerdictCoach` constant for backward compat but it should never appear
- Keep `Question` type but mark as deprecated (may be removed in a follow-up)

### Step 6.3 — Orchestrator gateway loop simplification

Modify `internal/orchestrator/orchestrator.go`:

- Remove `maxCoachingRounds` constant
- Remove the `for round := 0; round < maxCoachingRounds; round++` loop — replace with a single `gw.Evaluate()` call
- Remove `EventGateRequest(GateGatewayCoach)` emission
- Remove `incorporateAnswers()` function
- Remove `GatewayAnswer` type (the MCP bridge handles answer delivery)
- Remove the `decisions` channel handling within the gateway section — model resolves coaching internally via MCP tool calls
- Keep `SkipGateway` and `AutoApprove` flags — they bypass the gateway entirely, which is orthogonal
- The gateway now just runs, optionally asks the user questions via MCP (which the TUI handles transparently), and returns `accept` with a brief

### Step 6.4 — TUI coaching removal

**Remove in this exact order** to maintain a compile-passing build after each step. Run `go build ./...` after each numbered step.

1. **orchestrator types** — remove `GatewayAnswer` struct, `GateGatewayCoach` constant, `incorporateAnswers()` function, `maxCoachingRounds` constant, gateway coaching loop from `run()`. `go build ./...` ✓
2. **agent gateway** — remove coaching validations in `internal/agent/gateway.go` (Steps 6.2 + 6.3 complete here). `go build ./...` ✓
3. **TUI messages** — remove `SubmitGatewayIntent` and `SkipGatewayIntent` from `internal/tui/messages.go`. **Do this before** touching screen files or `model.go` — the compiler will flag all remaining usages as errors, making them easy to find. `go build ./...` will fail at this point intentionally.
4. **model.go intent cases** — remove `SubmitGatewayIntent` and `SkipGatewayIntent` switch cases from `handlePipelineKey()`. `go build ./...` ✓
5. **screen_pipeline.go coaching** — remove `ContentCoaching` from `ContentMode` enum, `answerFields []textarea.Model`, `answerCursor int`, `handleCoachingKey()`, `viewCoaching()`, coaching branch from `UpdateSubModel()`, coaching cases from `viewContent()` and `viewInputZone()`, `gatewayResult agent.GatewayResult` field. `go build ./...` ✓

Modify `internal/tui/messages.go`:

- Remove `SubmitGatewayIntent` and `SkipGatewayIntent` types (step 3 above)

Modify `internal/tui/model.go`:

- Remove `SubmitGatewayIntent` and `SkipGatewayIntent` handling from `handlePipelineKey()` (step 4 above)

### Step 6.5 — Researcher + planner prompt updates

Add to researcher and planner `system_prompt` in `pipeline.yaml`:

```
You have access to an AskUserQuestion tool via MCP. Use it SPARINGLY —
only when you encounter a genuinely ambiguous design decision where two
or more approaches would produce materially different implementations.
Do NOT ask the user how to explore the codebase or what tools to use.
The user is reviewing your work in a TUI, so keep questions short and
provide meaningful pre-filled options with helpful context hints.
Max 5 options per question. Prefer providing a default/recommended option.
```

---

## Phase 7: Cleanup & Edge Cases

### Step 7.1 — Bridge lifecycle

- In `Engine.Start()`: call `bridge.Start(ctx)` before launching goroutine
- In `run()` defer: call `bridge.Stop()` to clean up socket
- Handle context cancellation: bridge respects ctx, closes socket on cancel
- If bridge fails to start (socket collision): log warning, continue without question support — agents work fine without it

### Step 7.2 — Skip answer formatting

When `QuestionAnswer.Skipped == true`, the MCP bridge returns:
`"The user explicitly skipped this question. Proceed with your best judgment based on the codebase evidence you've gathered."`
This gives the model clear signal without confusion.

### Step 7.3 — Answer formatting for model

The MCP bridge formats the `QuestionAnswer` into a plain-text tool result the model can parse naturally.

**Single-select** (one option selected):

```
Selected: Return a structured error with the key path
```

**Single-select with custom context**:

```
Selected: Return a structured error with the key path
  Context: Also wrap with fmt.Errorf so callers get chain
```

**Multi-select** (multiple options):

```
Selected (3 of 4):
- go test ./internal/config/...
- go build ./cmd/orqestra
- go vet ./...
```

**Multi-select with custom context on some**:

```
Selected (2 of 4):
- go test ./internal/config/...
  Context: Also run with -race flag
- go build ./cmd/orqestra
```

**Freeform** (no options, user typed free text):

```
User's answer: Also wrap with fmt.Errorf so callers get the full chain
```

**Nothing selected but confirmed** (Enter with no selection):

```
User confirmed without selecting any option. Proceed with your best judgment.
```

---

## Relevant Files

- `internal/orchestrator/orchestrator.go` — add `UserQuestion`, `QuestionAnswer` types, `EventUserQuestion`, bridge wiring in `run()` and `Start()`; remove `GatewayAnswer`, `GateGatewayCoach`, `incorporateAnswers()`, `maxCoachingRounds`, gateway coaching loop
- `internal/harness/mcp_server.go` _(new)_ — MCP JSON-RPC server, `RunMCPServer()`, tool schema
- `internal/harness/question_bridge.go` _(new)_ — Unix socket listener, question/answer channel management
- `internal/harness/claude_cli.go` — `WithInlineMCPServer()` option, MCP config merging
- `internal/agent/gateway.go` — remove `GatewayVerdictCoach` validations, deprecate `Question` type
- `internal/tui/messages.go` — add `SubmitQuestionAnswerIntent`; remove `SubmitGatewayIntent`, `SkipGatewayIntent`
- `internal/tui/screen_pipeline.go` — add `ContentUserQuestion`, question state fields, key handling, rendering; remove `ContentCoaching`, `answerFields`, `answerCursor`, `handleCoachingKey()`, `viewCoaching()`
- `internal/tui/model.go` — `questionBridge` field, intent dispatch for question answers; remove gateway coaching intent handling
- `internal/tui/styles.go` — question-specific styles
- `internal/tui/layout.go` — no changes expected (question uses existing content zone)
- `internal/config/pipeline.yaml` — gateway prompt overhaul, researcher/planner prompt updates
- `cmd/orqestra/main.go` — `mcp-bridge` subcommand, bridge creation in `buildEngine()`

## Verification

1. **Unit: MCP protocol** — test `initialize`, `tools/list`, `tools/call` JSON-RPC round-trips in `mcp_server_test.go`
2. **Unit: Question bridge** — test socket round-trip with mock MCP client in `question_bridge_test.go`
3. **Unit: TUI question rendering** — golden tests for `viewUserQuestion()`:
   - Single-select with 3 options, cursor on 2nd, 1st selected
   - Multi-select with 4 options, 2 selected, cursor on 3rd
   - Multi-select with custom text expanded on one option
   - Freeform question (no options, textarea visible)
4. **Unit: TUI key handling** — test navigation, space-select (radio vs checkbox), tab-expand, enter-confirm, esc-skip in question mode. Specifically:
   - Single-select: space on option A → space on option B → only B selected
   - Multi-select: space on A → space on B → both selected → space on A → only B selected
5. **Integration: bridge + MCP** — test full cycle: start bridge → start MCP server → send tool call → receive question → send answer → verify tool result
6. **Manual: full pipeline** — run `orqestra` with researcher model, verify question appears in TUI when model calls tool, answer flows back
7. **Manual: skip behavior** — press Esc on question, verify model receives skip message and continues
8. **Build**: `go build ./cmd/orqestra` succeeds, `go test ./...` passes

### Anti-Regression Tests (required, one per blocker)

**AR-1 — `SendAnswer` is deferred via `tea.Cmd`, never called in `Update()` (Blocker 1)**
In `internal/tui/model_test.go`: dispatch `SubmitQuestionAnswerIntent` through `handlePipelineKey()` with a bridge whose `pendingAnswer` channel is at capacity (pre-filled, unbuffered, or capacity 0). Assert two structural properties — no timing involved:
1. The returned `tea.Cmd` is **non-nil** — proves intent dispatch produced a command rather than calling `SendAnswer` inline.
2. Execute the returned `tea.Cmd` (call `cmd()`) and assert it returns `nil` (the cmd has no follow-up message) AND that the bridge's answer channel received the expected value — proves `SendAnswer` runs inside the cmd, not before it.

Use a mock bridge with a channel of capacity 0 (unbuffered): if `SendAnswer` were called inside `Update()` it would deadlock immediately; the test passing proves it is not.

**AR-2 — `buildQuestionAnswer` populates `CustomTexts` (Blocker 2)**
In `internal/tui/screen_pipeline_test.go`: construct a `PipelineScreen` with `questionCustom = map[int]string{0: "also use -race"}`, `questionSelected = map[int]bool{0: true}`, call `buildQuestionAnswer()`, assert `answer.CustomTexts[0] == "also use -race"`. Also assert that `FormatAnswer(toolCall, answer)` output contains `"Context: also use -race"`.

**AR-3 — Phase 6 removal compiles cleanly at each step (Blocker 3)**
In CI (`.github/workflows`), or as a note in the implementation checklist: after steps 1, 2, and 4 of the removal sequence, `go build ./...` must exit 0. Step 3 (message types removal) is the only intentional break point. Document this in the PR checklist, not as a test.

**AR-4 — Goroutine → TUI event path is channel-based, not `program.Send` (Blocker 4)**
In `internal/orchestrator/orchestrator_test.go`: write a unit test that directly calls the forwarding goroutine's body (extract it as a named helper, or test the `emit()` call in isolation). Seed the mock bridge's `Questions()` channel with one `MCPToolCall`, then call the forwarder synchronously, then read from the events channel with a non-blocking `select`. Assert the received event has `Type == EventUserQuestion` and the correct payload. No timers, no `time.Sleep` — the test is fully deterministic because the channel write and read happen in the same goroutine sequence.

**AR-5 — `decisions` channel never blocks with capacity ≥ 1 (Blocker 5)**
In `internal/orchestrator/orchestrator_test.go`: call `engine.Start()` and assert `cap(engine.RunChannels().Decisions) >= 1` using the built-in `cap()` function. This is a pure structural assertion — no goroutines, no timers. The capacity of a channel is immutable after creation, so `cap()` is the correct and deterministic way to verify this invariant. No write-and-drain dance needed.

## Decisions

- **MCP server over stream interception**: MCP is the native Claude CLI extension mechanism. Stream interception + session resume would be fragile and the model wouldn't see the answer as a proper tool result
- **Unix domain socket for IPC**: Simple, fast, no port conflicts. Per-PID path avoids collisions
- **Bridge as subcommand, not separate binary**: Single binary distribution, self-referencing via `os.Executable()`
- **Question skip → explicit "user skipped" message**: Prevents model confusion. The model knows the user saw the question and chose not to answer, so it can use its own judgment rather than re-asking
- **Both options-based and freeform questions**: Model chooses based on context. Options for bounded decisions, freeform for open-ended clarification
- **Socket protocol: length-prefixed JSON**: Simple, no framing ambiguity, no dependency on newlines in JSON
- **Unified AskUserQuestion for all agents (gateway + researcher + planner)**: Gateway uses the same `AskUserQuestion` MCP tool instead of its bespoke coaching loop (`GatewayResult.Questions` / `ContentCoaching` / `incorporateAnswers`). Gateway gets ONLY the `mcp__orqestra__AskUserQuestion` tool (no Read, Grep, Bash — still a pure intake filter). This eliminates the entire parallel coaching mechanism: `GateGatewayCoach`, `ContentCoaching`, `answerFields`, `answerCursor`, `SubmitGatewayIntent`, `SkipGatewayIntent`, `maxCoachingRounds`, `incorporateAnswers()`, and the `GatewayAnswer` type. The model manages its own Q&A turns naturally via tool calls — when it needs clarification it calls `AskUserQuestion`, gets the answer in the tool result, and continues reasoning. Once satisfied, it outputs `"verdict":"accept"` with the brief. The `"verdict":"coach"` path and `Questions` field on `GatewayResult` become vestigial (can be removed or kept for backward compat).

**Orchestrator simplification**: The gateway section of `run()` collapses from a multi-round loop with manual decision channel handling to a single `gw.Evaluate()` call. The model handles its own coaching internally. The `EventGateRequest(GateGatewayCoach)` event type, `GatewayAnswer` struct, `DecisionApprove` with answers, and `DecisionSkip` at the gateway gate are all eliminated. `SkipGateway` flag still works (bypasses the gateway entirely).

**TUI simplification**: `ContentCoaching` mode, `answerFields []textarea.Model`, `answerCursor int`, `handleCoachingKey()`, `viewCoaching()`, `UpdateSubModel` coaching branch — all removed. Replaced by the unified `ContentUserQuestion` picker that fires for any agent's `AskUserQuestion` call.

**Gateway pipeline.yaml changes**: Remove `mcp_servers: []` (was forcing no-tools). Add the bridge MCP server. Set `allowed_tools: [mcp__orqestra__AskUserQuestion]`. Update system prompt to say "If you need clarification, call AskUserQuestion with clear options. Never output verdict 'coach' — always resolve ambiguity yourself via the tool, then output verdict 'accept'."

## Out of Scope

- Persistent question history (questions are ephemeral, answers flow to model context)
- Multiple concurrent questions (Claude CLI processes one tool call at a time)
- Question timeout (model waits indefinitely; user can Esc to skip or Ctrl+C to cancel pipeline)
- Worker agent questions (workers execute, they don't ask)
- Full removal of `GatewayVerdictCoach` / `Question` types (backward compat — can be cleaned up in a follow-up)

## Dead Code Removal (part of this plan)

These are eliminated by Phase 6 (gateway unification):

- `GateGatewayCoach` constant
- `GatewayAnswer` struct
- `incorporateAnswers()` function
- `maxCoachingRounds` constant
- `ContentCoaching` mode
- `answerFields`, `answerCursor` on `PipelineScreen`
- `handleCoachingKey()`, `viewCoaching()` methods
- `SubmitGatewayIntent`, `SkipGatewayIntent` types
- Coaching branch in `UpdateSubModel()`
