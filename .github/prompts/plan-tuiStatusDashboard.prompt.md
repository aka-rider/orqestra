# Plan: TUI Status Line, Live Metrics, and Dashboard Rewrite

## Dependency

This plan runs **after** the Token Ownership Refactor (plan-tokenOwnershipRefactor.prompt.md).
It consumes `RunUsage`, `UsageSnapshot`, and `BudgetGuard` from the orchestrator package.
If that plan is not yet applied, Phase 1 Step 1 (UsageSink) and Phase 3 (status line budget display) need temporary stubs.

---

## Phase 1: Live Streaming Metrics Plumbing (harness → orchestrator)

### Step 1 — New `UsageSink` interface in harness

**File**: `internal/harness/claude_cli.go` (~L40, next to `ActivitySink`)

```go
type UsageSink interface {
    OnUsage(input, output int64)
}
```

Extend `dispatchStreamEvent` (~L493) to check `display.(UsageSink)` and fire `OnUsage` when `event.Usage != nil`:

```go
if event.Usage != nil {
    if sink, ok := display.(UsageSink); ok {
        sink.OnUsage(event.Usage.InputTokens, event.Usage.OutputTokens)
    }
}
```

This covers both `parseStream` (claude_cli.go) and `parseStreamLines` (sandbox_cli_runner.go) since both call `dispatchStreamEvent`. Every stream event type with non-nil Usage triggers the callback — not just `"result"`.

### Step 2 — Extend `StreamBuffer` with live stats

**File**: `internal/orchestrator/orchestrator.go` (StreamBuffer, ~L36)

Add fields:

```go
liveInput    int64
liveOutput   int64
liveStart    time.Time
agentMeta    map[string]AgentStreamMeta
```

New type:

```go
type AgentStreamMeta struct {
    Input, Output int64
    StartTime     time.Time
    EndTime       time.Time
}
```

Behavior changes:

- `SetAgent(id)` — snapshot current agent's live stats into `agentMeta[prevAgent]` with `EndTime: time.Now()`, reset `liveInput`/`liveOutput`, set `liveStart = time.Now()`.
- New `RecordUsage(input, output int64)` — mutex-guarded, accumulates into `liveInput`/`liveOutput`.
- Extend `Snapshot()` return to include `LiveInput, LiveOutput int64, LiveStart time.Time`.

### Step 3 — Implement `UsageSink` on `streamWriter`

**File**: `internal/orchestrator/orchestrator.go` (~L1560, `streamWriter`)

```go
func (w *streamWriter) OnUsage(input, output int64) {
    w.buf.RecordUsage(input, output)
}
```

`streamWriter` already implements `io.Writer` and `ActivitySink`. Adding `UsageSink` is one method.

### Step 4 — Tests

**File**: `internal/harness/claude_cli_test.go` (or new `stream_test.go`)

- `dispatchStreamEvent` with mock `UsageSink` fires `OnUsage` for events with non-nil Usage.
- Non-result event types (e.g., `"assistant"`) with Usage also trigger `OnUsage`.

**File**: `internal/orchestrator/orchestrator_test.go`

- `StreamBuffer.RecordUsage` accumulates correctly.
- `SetAgent` snapshots previous agent's stats into `agentMeta`.
- Concurrent `RecordUsage` + `Snapshot` under race detector.

---

## Phase 2: Agent Model Metadata Flow (config → event → TUI)

### Step 5 — New `AgentMeta` type on `Event`

**File**: `internal/orchestrator/orchestrator.go`

```go
type AgentMeta struct {
    ModelRef      string // config key, e.g., "opus-4"
    ModelDisplay  string // underlying model, e.g., "claude-opus-4-20250514"
    Provider      string // provider name, e.g., "anthropic"
    ContextWindow int64  // context window size in tokens
}
```

Add `Meta AgentMeta` field to `Event` struct. Populated only on `EventAgentStarted`.

### Step 6 — Populate `AgentMeta` from config in `run()`

**File**: `internal/orchestrator/orchestrator.go`

Helper:

```go
func resolveAgentMeta(cfg *config.Config, modelRef string) AgentMeta {
    mc, ok := cfg.Models[modelRef]
    if !ok {
        return AgentMeta{ModelRef: modelRef}
    }
    return AgentMeta{
        ModelRef:      modelRef,
        ModelDisplay:  mc.Model,
        Provider:      mc.Provider,
        ContextWindow: mc.ContextWindow,
    }
}
```

On each `emit(Event{Type: EventAgentStarted, ...})`, populate `Meta`:

- Researcher (~L589): `Meta: resolveAgentMeta(e.Config, e.Config.Researcher.Model)`
- Architect: `Meta: resolveAgentMeta(e.Config, e.Config.Architect.Model)`
- Critic: `Meta: resolveAgentMeta(e.Config, e.Config.Critic.Model)`
- Worker: `Meta: resolveAgentMeta(e.Config, e.Config.Worker.Model)`

### Step 7 — Extend `AgentRow` with model fields

**File**: `internal/tui/model.go` (AgentRow, ~L44)

```go
type AgentRow struct {
    ID            string
    State         string
    Elapsed       time.Duration
    StartedAt     time.Time
    InputTokens   int64
    OutputTokens  int64
    // New fields
    ModelRef      string
    ModelDisplay  string
    Provider      string
    ContextWindow int64
}
```

### Step 8 — `ApplyEvent` stores metadata

**File**: `internal/tui/screen_pipeline.go` (ApplyEvent, ~L218)

In case `EventAgentStarted`:

```go
s.agents = append(s.agents, AgentRow{
    ID: event.AgentID, State: "running", StartedAt: time.Now(),
    ModelRef:      event.Meta.ModelRef,
    ModelDisplay:  event.Meta.ModelDisplay,
    Provider:      event.Meta.Provider,
    ContextWindow: event.Meta.ContextWindow,
})
```

### Step 9 — Persist model metadata in session artifacts

**File**: `internal/agent/session.go` (StepMeta, ~L60)

Add fields (with `omitempty` for backward compat with existing JSON artifacts):

```go
ModelDisplay  string `json:"model_display,omitempty"`
Provider      string `json:"provider,omitempty"`
ContextWindow int64  `json:"context_window,omitempty"`
```

**File**: `internal/orchestrator/orchestrator.go`

All `writeArtifactJSON(session, "*_meta.json", agent.StepMeta{...})` calls populate `ModelDisplay`, `Provider`, `ContextWindow` from `resolveAgentMeta`.

---

## Phase 3: Single-Line Status Bar (replaces 6-line sidebar)

### Layout Change

**Current** (11 lines of chrome):

```
┌─ content viewport ─────────────────┐
│ (contentHeight = height - 10)      │
├─ divider + status ─────────────────┤  constPipelineInputHeight = 2
│ researching running...             │
├─ config name + divider ────────────┤  constSidebarHeight = 6
│ Agents                             │
│ ────────                           │
│ ✓ researcher    1m 203k            │
│ ▶ architect     34s  -             │
│ ────────                           │
│ total: 203k | 1m46s               │
├─ divider + key hints ──────────────┤  constFooterHeight = 2
│ [^N] [^D] [^H] [^C]               │
└────────────────────────────────────┘
```

**New** (5 lines of chrome → +5 content lines):

```
┌─ content viewport ─────────────────┐
│ (contentHeight = height - 5)       │
├─ divider + status ─────────────────┤  constPipelineInputHeight = 2
│ researching running...             │
├─ status bar ───────────────────────┤  constSidebarHeight = 1
│ ✓res ✓arch ▶work: Opus4 ↑12k ↓34k ⊞56% 42t/s ·∘●∘·  │
├─ divider + key hints ──────────────┤  constFooterHeight = 2
│ [^N] [^D] [^H] [^C]               │
└────────────────────────────────────┘
```

### Step 10 — Layout constant

**File**: `internal/tui/layout.go`

```go
constSidebarHeight = 1  // was 6
```

Content viewport gains 5 lines automatically through existing `recalculateLayout()` math.

### Step 11 — Remove sidebar viewport

**File**: `internal/tui/screen_pipeline.go`

- Remove `sidebarVP viewport.Model` from `PipelineScreen` struct.
- `RecalculateLayout()`: remove `s.sidebarVP.SetWidth/SetHeight` calls.
- `SyncViewports()`: remove `s.sidebarVP.SetContent(s.viewSidebar(w))`.
- `View()`: replace `s.sidebarVP.View()` with direct `s.viewStatusLine(w)`.

The status line is 1 line — no scrolling needed, no viewport.

### Step 12 — New `viewStatusLine()` function

**File**: `internal/tui/screen_pipeline.go`

```go
func (s PipelineScreen) viewStatusLine(width int) string
```

Format: `{agent chain} {active agent detail} {animation}`

**Agent chain** — one entry per agent:

```
✓res  ✓arch  ✓crit  ▶work
```

State icons: `✓` done, `✗` failed, `⊘` cancelled, `●` gate, `▶` running, `○` waiting.
Names truncated to first 4 chars.

**Active agent detail** (only for the running agent):

```
Opus4 ↑12k ↓34k ⊞56% 42t/s
```

- `↑` = input tokens consumed (from `s.liveInput`)
- `↓` = output tokens produced (from `s.liveOutput`)
- `⊞` = context window % = `(liveInput + liveOutput) / AgentRow.ContextWindow * 100`
- tok/s = `liveOutput / time.Since(liveStart).Seconds()`
- Model name: `AgentRow.ModelDisplay`, truncated to fit

**Truncation strategy** (narrow terminals, checked left-to-right):

1. First drop: animation
2. Second drop: tok/s
3. Third drop: context %
4. Always show: agent chain + active agent model + token counts

### Step 13 — Animation system

**File**: `internal/tui/screen_pipeline.go`

Add to `PipelineScreen`:

```go
animFrame int
```

**Shimmer** — 5-frame cycle of Unicode dots, advancing 1 frame per animTick:

```go
var shimmerFrames = []string{
    "·∘○∘·",
    "∘○∘·∘",
    "○∘·∘○",
    "∘·∘○∘",
    "·∘○∘·",
}
```

Characters: `·` (dim), `∘` (medium), `○` (bright). The bright dot travels right-to-left, creating a shimmer wave.

**Pulse** — Lip Gloss `Foreground` color cycles on the `▶` running icon:

```go
var pulsePalette = []lipgloss.Color{
    "#555555", // dim
    "#888888", // medium
    "#BBBBBB", // light
    "#FFFFFF", // bright
    "#BBBBBB", // light
    "#888888", // medium
}
```

6-state cycle. Advances 1 frame every 3 animTicks (~600ms period).
Shimmer at ~200ms, pulse at ~600ms → always out of phase.

### Step 14 — Animation tick

**File**: `internal/tui/messages.go`

```go
type animTickMsg time.Time
```

**File**: `internal/tui/model.go`

```go
func animTickCmd() tea.Cmd {
    return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
        return animTickMsg(t)
    })
}
```

In `Model.Update()`:

```go
case animTickMsg:
    if m.state == StatePipeline && m.pipelineScreen.active {
        m.pipelineScreen.animFrame++
        return m, animTickCmd()
    }
    return m, nil
```

No `SyncViewports` — only the counter changes. `viewStatusLine()` reads `animFrame` directly during render.

Start `animTickCmd()` when the first `EventAgentStarted` arrives. Stop (don't reissue) when pipeline completes.

### Step 15 — Layout bounds update

**File**: `internal/tui/model.go` (`recalculateLayout`)

Sidebar bounds now cover 1 line instead of 6. The calculation already uses `constSidebarHeight`, so the constant change in Step 10 is sufficient.

Remove `sidebarVP` from mouse event forwarding in `PipelineScreen.Update()` if it exists.

---

## Phase 4: Dashboard Rewrite

### Step 16 — Rewrite `viewDashboard()`

**File**: `internal/tui/screen_pipeline.go` (replaces existing ~L954)

Per-agent card layout:

```
╭─ researcher ─────────────────────────────────────╮
│ Model: claude-opus-4-20250514 (Anthropic)        │
│ ↑ Consumed: 12,345    ↓ Produced: 8,901          │
│ Throughput: 42.3 tok/s                            │
│ Context: █████░░░░░ 48% of 200,000               │
╰──────────────────────────────────────────────────╯
```

Data sources:

- Running agent: live stats from `s.liveInput`/`s.liveOutput`/`s.liveStart` (polled on tick from StreamBuffer).
- Completed agents: `AgentRow.InputTokens`/`OutputTokens` (set on `EventAgentDone`) or `StreamBuffer.agentMeta`.
- Context window % = `(input + output) / AgentRow.ContextWindow * 100` per-agent (from config, not hardcoded).
- Throughput: `output / elapsed` for running agent; `output / AgentRow.Elapsed.Seconds()` for completed.

Delete `constDefaultContextWindow` from `internal/tui/layout.go` (~L41) — replaced by per-agent `ContextWindow` from config.

### Step 17 — Dashboard scrolling

Keep `dashboardVP viewport.Model` for scrollable content (multiple agent cards can exceed screen height).
`SyncViewports()` continues to call `s.dashboardVP.SetContent(s.viewDashboard())`.

---

## Phase 5: Tick Integration

### Step 18 — TUI tick polls StreamBuffer for live stats

**File**: `internal/tui/model.go` (`tickMsg` handler, ~L205)

```go
case tickMsg:
    if m.state == StatePipeline {
        agentID, _, _, liveIn, liveOut, liveStart := m.pipelineScreen.streamBuf.SnapshotFull()
        m.pipelineScreen.liveInput = liveIn
        m.pipelineScreen.liveOutput = liveOut
        m.pipelineScreen.liveStart = liveStart
        _ = agentID // already tracked in agents list
        // existing SyncViewports() and tickCmd() follow
    }
```

**File**: `internal/tui/screen_pipeline.go`

Add fields:

```go
liveInput  int64
liveOutput int64
liveStart  time.Time
```

Used by `viewStatusLine()` for the running agent's display and by `viewDashboard()` for the running agent's card.

---

## Data Ownership

| Data                    | Owner                                        | API                                                  | Consumers                               |
| ----------------------- | -------------------------------------------- | ---------------------------------------------------- | --------------------------------------- |
| Live deltas             | harness (emits) → orchestrator (accumulates) | `UsageSink.OnUsage()` → `StreamBuffer.RecordUsage()` | TUI tick poll                           |
| Agent model info        | config (source) → orchestrator (resolves)    | `resolveAgentMeta()` → `Event.Meta`                  | TUI `AgentRow`                          |
| Run-scoped accumulation | orchestrator                                 | `RunUsage.Record/Snapshot` (token refactor)          | TUI, BudgetGuard                        |
| Budget enforcement      | orchestrator                                 | `BudgetGuard.Check()` (token refactor)               | `Engine.run()` interrupts agent         |
| Per-agent display state | tui                                          | `AgentRow` fields + `liveInput/liveOutput`           | `viewStatusLine`, `viewDashboard`       |
| Animation state         | tui                                          | `PipelineScreen.animFrame`                           | `viewStatusLine` (shimmer + pulse)      |
| Session artifacts       | orchestrator (writes), agent (reads)         | `StepMeta` JSON → `LoadRunDetail()`                  | `screen_run_detail`, `screen_runs_list` |

---

## Relevant Files

| File                                    | Action | Key Changes                                                                                                       |
| --------------------------------------- | ------ | ----------------------------------------------------------------------------------------------------------------- |
| `internal/harness/claude_cli.go`        | MODIFY | `UsageSink` interface, extend `dispatchStreamEvent` for live deltas                                               |
| `internal/orchestrator/orchestrator.go` | MODIFY | `StreamBuffer` live stats, `AgentMeta` type, `resolveAgentMeta()`, `Event.Meta`, `streamWriter.OnUsage`           |
| `internal/tui/layout.go`                | MODIFY | `constSidebarHeight` 6→1, delete `constDefaultContextWindow`                                                      |
| `internal/tui/model.go`                 | MODIFY | `AgentRow` model fields, `animTickMsg`, `animTickCmd`, tick polls live stats                                      |
| `internal/tui/messages.go`              | MODIFY | `animTickMsg` type                                                                                                |
| `internal/tui/screen_pipeline.go`       | MODIFY | Remove `sidebarVP`, new `viewStatusLine()`, rewrite `viewDashboard()`, animation fields, `liveInput`/`liveOutput` |
| `internal/tui/icons.go`                 | MODIFY | ↑↓⊞ arrow/context icons                                                                                           |
| `internal/agent/session.go`             | MODIFY | `StepMeta` gains `ModelDisplay`, `Provider`, `ContextWindow`                                                      |
| `internal/tui/screen_run_detail.go`     | MODIFY | Model info in step detail rendering                                                                               |

## Verification

1. `go test -race ./internal/harness/` — UsageSink dispatch, non-result events with usage
2. `go test -race ./internal/orchestrator/` — StreamBuffer live stats, SetAgent snapshot, race safety
3. `go test -race ./internal/tui/` — render purity (`viewStatusLine` returns consistent output for same animFrame), layout (1-line sidebar), animFrame stable across repeated `View()` calls
4. `go build ./...`
5. Manual: live tok/s updates during researcher phase
6. Manual: ^D shows per-agent model name + provider + context %
7. Manual: narrow terminal — agent chain always visible, animation drops first
8. Manual: shimmer/pulse visible, out-of-phase rhythm (~200ms vs ~600ms)
9. Manual: complete run, reload runs list, verify model info in run detail from persisted StepMeta

## Scope Boundaries

**Included**:

- Live token deltas via UsageSink (parseStream + parseStreamLines)
- Agent model metadata flow (config → event → AgentRow)
- Single-line status bar with shimmer/pulse animation
- Dashboard rewrite with model name, provider, context window %
- Session artifact persistence of model info

**Excluded**:

- Thinking tokens (future item — field slot planned but not parsed)
- Token budget display in status line (depends on token ownership refactor; budget `int64` slots in once `RunChannels.Budget` exists)
- Nerd Font icon variants (standard Unicode only; configurable icon set is future)
- Historical run dashboard (^D only works during live pipeline; run detail screen shows saved data)
