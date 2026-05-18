# Plan: TUI Unified Dashboard — Status Bar, Streaming, State Machine & 3-Pane Layout

## Context

Redesign the Orqestra TUI to: (1) shrink the 6-line sidebar to a 1-line status bar with live metrics and left-overflow truncation, (2) add a shimmering animation at the streaming tail, (3) move `AskUserQuestion` to the auto-growing bottom input area, (4) build a new 3-pane Dashboard overlay (`^D` and Run History) using isolated Bubble Tea sub-components with a strict FSM, and (5) unify live and historical views through the same `DashboardModel`. No backward compatibility for on-disk data.

---

## Architecture Overview

### Current Screen Zones (Pipeline Mode)

```
┌─ content viewport ────────────────────────────────── width ──┐
│ contentHeight = height - inputHeight - footerH - sidebarH    │
│ (streaming output, plan review, question, etc.)              │
├─ input zone ─────────────────────────────────────────────────┤ constPipelineInputHeight=2
│ "researching running..." or plan comment textarea            │
├─ sidebar ────────────────────────────────────────────────────┤ constSidebarHeight=6
│ config name + agents table + totals (6 lines)                │
├─ footer ─────────────────────────────────────────────────────┤ constFooterHeight=2
│ [^N] [^D] [^H] [^C]                                         │
└──────────────────────────────────────────────────────────────┘
```

### Target Screen Zones (Pipeline Mode — Main View)

```
┌─ content viewport ────────────────────────────────── width ──┐
│ contentHeight = height - inputHeight - 1 - footerH           │
│ (streaming output with shimmer, plan review, question, etc.) │
├─ input zone (auto-grows for AskUserQuestion) ────────────────┤ dynamic: 2..N
│ prompt status OR question options + input                    │
├─ status bar ─────────────────────────────────────────────────┤ 1 line
│ <..✓arch ✓crit ▶worker: Opus4 ↑12k ↓34k ⊞56% 42t/s ·∘○∘· │
├─ footer ─────────────────────────────────────────────────────┤ constFooterHeight=2
│ [^N] [^D] [^H] [^C]  ← footer changes: [Esc] in dashboard  │
└──────────────────────────────────────────────────────────────┘
```

### Target: Dashboard Overlay (`^D` or Run History Detail)

```
┌─────────────────────────────────────────────────────── width ─┐
│ ┌─ Left Pane 25% ──┐ ┌─ Right Pane 75% ──────────────────┐  │
│ │ Agent Menu        │ │ ┌─ Top VP (50%) ────────────────┐ │  │
│ │                   │ │ │ Input markdown / Prompt        │ │  │
│ │  ✓ researcher     │ │ │ (glamour-rendered)             │ │  │
│ │  ✓ architect #1   │ │ └────────────────────────────────┘ │  │
│ │  ✓ critic         │ │ ┌─ Bottom VP (50%) ─────────────┐ │  │
│ │ ▶ architect #2    │ │ │ Output plan / git status diff  │ │  │
│ │  ○ worker         │ │ │ (glamour or unified diff)      │ │  │
│ │                   │ │ └────────────────────────────────┘ │  │
│ └───────────────────┘ └────────────────────────────────────┘  │
│ ┌─ Bottom Log Pane ─── 100% width ── 30% height ──────────┐  │
│ │ read: internal/tui/model.go                              │  │
│ │ grep: "AgentRow"                                         │  │
│ │ ── * . . ── (shimmer when streaming)                     │  │
│ └──────────────────────────────────────────────────────────┘  │
│ [Esc] return │ [^E] editor │ [Tab/S-Tab] cycle │ [PgUp/Dn]   │
└───────────────────────────────────────────────────────────────┘
```

---

## Phase 1: Live Streaming Metrics Plumbing

### Step 1.1 — `UsageSink` Interface

**File:** `internal/harness/output.go` (alongside `ActivitySink` defined at L14)

- New interface: `type UsageSink interface { OnUsage(input, output int64) }`
- **File:** `internal/harness/claude_cli.go` — Modify `dispatchStreamEvent` (~L512): check `display.(UsageSink)` and fire `OnUsage` when `event.Usage != nil`
- Covers both `parseStream` and `parseStreamLines` (sandbox runner) since both call `dispatchStreamEvent`

### Step 1.2 — `StreamRing` Live Stats

**File:** `internal/orchestrator/stream_ring.go`

- Add fields to `StreamRing`: `liveInput int64`, `liveOutput int64`, `liveStart time.Time`
- New method `RecordUsage(input, output int64)` — mutex-guarded, accumulates tokens
- New method `SnapshotUsage() (in, out int64, start time.Time)` — safe reader
- Modify `SetAgent(id)`: before snapshotting entries, also capture `{liveInput, liveOutput, time.Now()}` into a new `agentUsage map[string]AgentUsageSnapshot`; reset live counters; set `liveStart = time.Now()`

### Step 1.3 — `streamWriter` Implements `UsageSink`

**File:** `internal/orchestrator/engine.go` (streamWriter, ~L1560)

- Add: `func (w *streamWriter) OnUsage(input, output int64) { w.ring.RecordUsage(input, output) }`

### Step 1.5 — Export Historical Log Parser

**File:** `internal/harness/logparser.go` (**CREATE**)

The JSONL parsing logic (`streamEvent`, activity extraction, tool-use formatting) is currently unexported and internal to `claude_cli.go` / `sandbox_cli_runner.go`. The new `LogViewerModel` needs to read historical session logs from disk. Rather than duplicating parsing, export a reader:

```go
// ParseSessionLog reads a Claude CLI JSONL session log and returns the
// extracted activities and text lines suitable for display.
func ParseSessionLog(r io.Reader) ([]Activity, []string, error)
```

- Reuses the existing unexported `streamEvent` struct and `dispatchStreamEvent` logic internally
- Returns `[]Activity` (tool invocations) and `[]string` (raw text lines) — the same format `StreamRing` stores
- Used by `LogViewerModel` for historical runs and by `RunDetailScreen` for log rendering
- Unit test: parse a known JSONL fixture → verify activities match expected

### Step 1.5 — Agent Metadata in Events

**File:** `internal/orchestrator/events.go`

- Add to `Event`: `Meta AgentMeta`
- New type:
  ```go
  type AgentMeta struct {
      ModelRef      string
      ModelDisplay  string
      Provider      string
      ContextWindow int64
  }
  ```

**File:** `internal/orchestrator/engine.go`

- Helper `resolveAgentMeta(cfg *config.Config, modelRef string) AgentMeta` uses `cfg.lookupModel(modelRef)` (unexported, case-insensitive lookup at `config.go:75`) — **NOT** raw `cfg.Models[modelRef]` map access, which would silently return zero values for case-mismatched aliases. Returns `AgentMeta{ModelRef: modelRef}` with zeroed fields if lookup fails (no silent fallback to a different model).
- Populate `Event.Meta` on every `emit(Event{Type: EventAgentStarted, ...})`
- **Note:** `config.ResolveModel()` (L404) provides full connection details but is heavier than needed here (includes API key interpolation). `lookupModel()` is sufficient for metadata extraction (`ModelConfig.Model`, `.Provider`, `.ContextWindow`). Since `lookupModel` is unexported, either: (a) expose a lightweight `Config.ModelMeta(name) (ModelConfig, bool)` method, or (b) inline the case-insensitive lookup in `resolveAgentMeta` using the same pattern.

### Step 1.6 — Extend `AgentRow` in TUI

**File:** `internal/tui/model.go` (AgentRow struct, ~L44)

- Add: `ModelRef string`, `ModelDisplay string`, `Provider string`, `ContextWindow int64`

**File:** `internal/tui/screen_pipeline.go` (`ApplyEvent`, ~L218)

- On `EventAgentStarted`: populate new fields from `event.Meta`

---

## Phase 2: Status Bar, Shimmer Animation, AskUserQuestion

### Step 2.1 — Layout Constant Change

**File:** `internal/tui/layout.go`

- Change `constSidebarHeight = 6` → `constSidebarHeight = 1`
- Delete `constDefaultContextWindow` (replaced by per-agent `ContextWindow`)
- `recalculateLayout()` in `model.go` uses `constSidebarHeight` arithmetically; content gains 5 lines automatically

### Step 2.2 — Remove Sidebar Viewport

**File:** `internal/tui/screen_pipeline.go`

- Remove `sidebarVP viewport.Model` field (if present)
- In `RecalculateLayout()`: remove `s.sidebarVP.SetWidth/SetHeight` calls
- In `SyncViewports()`: remove `s.sidebarVP.SetContent(s.viewSidebar(w))`
- In `View()`: replace `s.sidebarVP.View()` with direct `s.viewStatusLine(width)`
- No viewport needed for 1 line of content

### Step 2.3 — `viewStatusLine()` Implementation

**File:** `internal/tui/screen_pipeline.go` (new function)

```go
func (s PipelineScreen) viewStatusLine(width int) string
```

**Format:** `{agent-chain} {active-detail} {shimmer}`

**Agent chain** — one glyph per agent:

```
✓res  ✓arch  ✓crit  ▶work
```

State icons: `✓` done, `✗` failed, `⊘` cancelled, `●` gate, `▶` running (pulsing), `○` waiting.
Names: first 4 chars of `AgentRow.ID`.

**Active agent detail** (only for running agent, higher contrast style):

```
U+F2DB  nf-fa-microchip
U+F0E7  nf-fa-bolt
U+F062  nf-fa-arrow-up
U+F063  nf-fa-arrow-down
U+F200  nf-fa-pie-chart
```

```
<nf-fa-microchip>Opus4 <nf-fa-arrow-up>12k <nf-fa-arrow-down>34k <nf-fa-pie-chart>56%  <nf-fa-bolt>42t/s
```

- `<nf-fa-arrow-up>` = input tokens consumed (from `s.liveInput`)
- `<nf-fa-arrow-down>` = output tokens produced (from `s.liveOutput`)
- `<nf-fa-pie-chart>` = context % = `(liveInput+liveOutput) / AgentRow.ContextWindow * 100`
- `<nf-fa-bolt>` = tok/s = `liveOutput / time.Since(liveStart).Seconds()`
- Model name: `AgentRow.ModelDisplay`, truncated to fit

**Overflow / Truncation Rule (checked left-to-right):**

1. Full string exceeds `width`? Drop shimmer.
2. Still exceeds? Drop tok/s.
3. Still exceeds? Drop context %.
4. Still exceeds? Truncate agent chain from the LEFT, prepend `<..`.
5. **Active agent + stats MUST always be visible.**

### Step 2.4 — Shimmer Animation

**File:** `internal/tui/screen_pipeline.go`

Add `animFrame int` to `PipelineScreen`.

**Shimmer frames** (5-frame cycle, Unicode dots):

```go
var shimmerFrames = []string{"·∘○∘·", "∘○∘·∘", "○∘·∘○", "∘·∘○∘", "·∘○∘·"}
```

Chars: `·` dim, `∘` medium, `○` bright. Bright dot travels right-to-left.

**Pulse** on `▶` icon: 6-color palette cycling (`#555`→`#888`→`#BBB`→`#FFF`→`#BBB`→`#888`). Advances 1 frame every 3 animTicks (~600ms period). Always out of phase with shimmer.

**Shimmer in streaming block** (`viewStreaming`, ~L737):
Append at bottom of streaming output, after activities + stream lines:

```
── * . .  ──────────────────────
```

Frame pattern: rotate `*`, `.`, `.` positions → creates a dot-crawl effect on the last visible line. Only shown when `s.phase` is active (not done).

### Step 2.5 — Animation Tick System

**File:** `internal/tui/messages.go`

- New type: `type animTickMsg time.Time`

**File:** `internal/tui/model.go`

- `animTickCmd()` returns `tea.Tick(200*time.Millisecond, func(t) tea.Msg { return animTickMsg(t) })`
- In `Update()`: on `animTickMsg`, if pipeline active: `m.pipelineScreen.animFrame++`, return `animTickCmd()`
- Start on first `EventAgentStarted`; stop (don't reissue) when pipeline completes

### Step 2.6 — Live Stats Tick Polling

**File:** `internal/tui/model.go` (existing `tickMsg` handler)

- On tick: call `streamBuf.SnapshotUsage()` → update `pipelineScreen.liveInput`, `.liveOutput`, `.liveStart`
- New fields on `PipelineScreen`: `liveInput int64`, `liveOutput int64`, `liveStart time.Time`

### Step 2.7 — AskUserQuestion Repositioning

**File:** `internal/tui/question.go` and `screen_pipeline.go`

- Remove `s.question.View(width)` from `viewContent()` (~L720)
- Move question rendering into `viewInputZone()` (~L686): when `ContentUserQuestion` is active, render the question options + input there
- **Auto-growth:** In `recalculateLayout()` (`model.go:660`): when `ContentUserQuestion` is active AND `hasQuestion` is true:
  - Compute required height = question option count + 2 (textarea + divider)
  - Set dynamic `inputHeight = max(constPipelineInputHeight, requiredHeight)`
  - Content viewport shrinks correspondingly (pushes content up)
- Upper area remains read-only output; bottom area is exclusively for user writing/input

---

## Phase 3: Dashboard 3-Pane Layout — Components & State Machine

### Component Architecture (Separate Files)

| File                                 | Component             | Responsibility                      |
| ------------------------------------ | --------------------- | ----------------------------------- |
| `internal/tui/dashboard.go`          | `DashboardModel`      | FSM controller, layout, msg routing |
| `internal/tui/dashboard_menu.go`     | `AgentMenuModel`      | Left pane: agent list navigation    |
| `internal/tui/dashboard_artifact.go` | `ArtifactViewerModel` | Right pane: top/bottom viewports    |
| `internal/tui/dashboard_log.go`      | `LogViewerModel`      | Bottom pane: streaming/static logs  |
| `internal/tui/dashboard_test.go`     | Tests                 | Full FSM traversal tests            |

### DashboardModel — FSM Definition

```go
type DashboardFocus int
const (
    FocusMenu         DashboardFocus = iota // Left pane: agent list
    FocusArtTop                             // Right-top: input/prompt viewport
    FocusArtBottom                          // Right-bottom: output/diff viewport
    FocusLog                                // Bottom: log viewer
    FocusPlanHistory                        // Nested: plan revision history (architect only)
)
```

### State Transition Diagram

```
                    Tab
    ┌──────────────────────────────────┐
    │                                  │
    v          Enter/Tab               │
┌────────┐ ──────────────> ┌──────────┐    Tab     ┌───────────┐    Tab    ┌─────────┐
│  Menu  │                 │ ArtTop   │ ────────> │ ArtBottom │ ────────> │   Log   │
└────────┘ <────────────── └──────────┘           └───────────┘           └─────────┘
    ^          Esc              ^   Esc                ^   Esc                 │ Esc
    │                          │                      │                       │
    │         S-Tab            │    S-Tab             │    S-Tab              │
    └──────────────────────────┴─────────────────────┴────────────────────────┘
                                      (all Esc → Menu)

    Esc from FocusMenu → CloseDashboardIntent (exit overlay entirely)

    --- Nested sub-state (architect card only) ---

    FocusArtTop/Bottom ── ^Y ──> FocusPlanHistory ── Esc ──> FocusArtTop
                                      │
                               (PlanHistoryScreen)
                               Up/Down, PgUp/PgDn, d/c, r
                               all delegated internally
```

### Complete State × Input Transition Matrix

| Current State                  | Input                 | Next State       | Action                                                          |
| ------------------------------ | --------------------- | ---------------- | --------------------------------------------------------------- |
| **FocusMenu**                  | `↑`/`↓`               | FocusMenu        | Delegate to `AgentMenuModel` (move cursor)                      |
| **FocusMenu**                  | `Enter`               | FocusArtTop      | Load selected agent's artifacts into right panes + logs         |
| **FocusMenu**                  | `Tab`                 | FocusArtTop      | Move focus right (no data reload)                               |
| **FocusMenu**                  | `Shift+Tab`           | FocusLog         | Wrap backward to log pane                                       |
| **FocusMenu**                  | `Esc`                 | —                | Emit `CloseDashboardIntent` (exit overlay, return to main view) |
| **FocusMenu**                  | `PgUp`/`PgDn`         | FocusMenu        | Delegate (scroll menu if > screen height)                       |
| **FocusArtTop**                | `↑`/`↓`/`PgUp`/`PgDn` | FocusArtTop      | Delegate to `ArtifactViewerModel.topVP` scroll                  |
| **FocusArtTop**                | `Tab`                 | FocusArtBottom   | Move focus to lower artifact                                    |
| **FocusArtTop**                | `Shift+Tab`           | FocusMenu        | Move focus left                                                 |
| **FocusArtTop**                | `Esc`                 | FocusMenu        | Return to menu                                                  |
| **FocusArtTop**                | `^E`                  | FocusArtTop      | Emit `OpenExternalEditorIntent{path: topArtifactPath}`          |
| **FocusArtBottom**             | `↑`/`↓`/`PgUp`/`PgDn` | FocusArtBottom   | Delegate to `ArtifactViewerModel.bottomVP` scroll               |
| **FocusArtBottom**             | `Tab`                 | FocusLog         | Move focus to log pane                                          |
| **FocusArtBottom**             | `Shift+Tab`           | FocusArtTop      | Move focus up                                                   |
| **FocusArtBottom**             | `Esc`                 | FocusMenu        | Return to menu                                                  |
| **FocusArtBottom**             | `^E`                  | FocusArtBottom   | Emit `OpenExternalEditorIntent{path: bottomArtifactPath}`       |
| **FocusLog**                   | `↑`/`↓`/`PgUp`/`PgDn` | FocusLog         | Delegate to `LogViewerModel` scroll                             |
| **FocusLog**                   | `Tab`                 | FocusMenu        | Wrap forward to menu                                            |
| **FocusLog**                   | `Shift+Tab`           | FocusArtBottom   | Move focus up                                                   |
| **FocusLog**                   | `Esc`                 | FocusMenu        | Return to menu                                                  |
| **FocusArtTop** (architect)    | `^Y`                  | FocusPlanHistory | Open `PlanHistoryScreen` for this architect's plan-history dir  |
| **FocusArtBottom** (architect) | `^Y`                  | FocusPlanHistory | Open `PlanHistoryScreen` for this architect's plan-history dir  |
| **FocusPlanHistory**           | `Esc`                 | FocusArtTop      | Close plan history, return to architect artifact view           |
| **FocusPlanHistory**           | `↑`/`↓`               | FocusPlanHistory | Delegate to `PlanHistoryScreen` (revision list navigation)      |
| **FocusPlanHistory**           | `PgUp`/`PgDn`         | FocusPlanHistory | Delegate to `PlanHistoryScreen` (scroll diff/content)           |
| **FocusPlanHistory**           | `r` (live, !readOnly) | FocusPlanHistory | Revert confirmation modal (existing `PlanHistoryScreen` flow)   |
| **FocusPlanHistory**           | `d`/`c`               | FocusPlanHistory | Toggle diff vs content pane (existing `paneMode`)               |

### Update Delegation Logic (Pseudocode)

```go
func (d DashboardModel) Update(msg tea.Msg) (DashboardModel, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        // 1. Global FSM transitions
        switch {
        case key.Matches(msg, keys.Tab):
            d.focus = (d.focus + 1) % 4
            return d, d.syncFocusCmd()
        case key.Matches(msg, keys.ShiftTab):
            d.focus = (d.focus - 1 + 4) % 4
            return d, d.syncFocusCmd()
        case key.Matches(msg, keys.Escape):
            if d.focus == FocusMenu {
                return d, emitIntent(CloseDashboardIntent{})
            }
            d.focus = FocusMenu
            return d, d.syncFocusCmd()
        }

        // 2. Handle ^Y → FocusPlanHistory (architect cards only)
        if key.Matches(msg, keys.CtrlY) && (d.focus == FocusArtTop || d.focus == FocusArtBottom) {
            card := d.menu.SelectedCard()
            if card.isArchitect() && card.PlanHistoryDir != "" {
                d.focus = FocusPlanHistory
                readOnly := !card.IsLive
                cmd = d.planHistory.Open(card.PlanHistoryDir, readOnly, card.PlanHistoryHeadSHA)
                return d, cmd
            }
            // No-op: not architect or no history dir
            return d, nil
        }

        // 3. Delegate to focused component
        switch d.focus {
        case FocusMenu:
            d.menu, cmd = d.menu.Update(msg)
            if key.Matches(msg, keys.Enter) {
                d.focus = FocusArtTop
                cmd = tea.Batch(cmd, d.loadAgentArtifactsCmd(d.menu.SelectedID()))
            }
        case FocusArtTop:
            d.artifact, cmd = d.artifact.UpdateTop(msg)
        case FocusArtBottom:
            d.artifact, cmd = d.artifact.UpdateBottom(msg)
        case FocusLog:
            d.log, cmd = d.log.Update(msg)
        case FocusPlanHistory:
            d.planHistory, cmd = d.planHistory.Update(msg)
            // Check if PlanHistoryScreen emitted an intent
            if d.planHistory.PendingIntent != nil {
                switch intent := d.planHistory.PendingIntent.(type) {
                case ClosePlanHistoryIntent:
                    d.focus = FocusArtTop
                    d.planHistory.PendingIntent = nil
                case RevertPlanIntent:
                    d.planHistory.PendingIntent = nil
                    d.focus = FocusArtTop
                    return d, emitIntent(intent) // propagate to orchestrator
                }
            }
        }

    case tea.WindowSizeMsg:
        // Broadcast to all sub-components
        d.width, d.height = msg.Width, msg.Height
        d.menu.SetSize(d.menuWidth(), d.upperHeight())
        d.artifact.SetSize(d.artWidth(), d.upperHeight())
        d.log.SetSize(d.width, d.logHeight())
    }
    return d, cmd
}
```

### Agent Card Rendering (Left Pane)

Each agent in the menu renders as a compact card:

```
╭─ researcher ✓ ──────────────────╮
│ claude-opus-4  ↑12k ↓8k  1m58s │
╰─────────────────────────────────╯
╭─ architect #1 ✓ ────────────────╮
│ claude-opus-4  ↑24k ↓15k 2m12s │
╰─────────────────────────────────╯
╭─ worker ▶ ──────────────────────╮  ← highlighted/selected
│ claude-opus-4  ↑5k ↓2k  34s    │
│ ⊞ 48% │ 42 tok/s              │
╰─────────────────────────────────╯
```

- Running agents show live-updating tok/s and context %
- Completed agents show static final stats
- **Each Architect/Critic revision is a separate card** in the menu

### Plan Revision History as Architect Sub-State

When an Architect card is selected and focused (`FocusArtTop` or `FocusArtBottom`), `^Y` opens the **plan revision history** as a nested sub-state. This reuses the existing `PlanHistoryScreen` component (`internal/tui/screen_plan_history.go`) which already implements:

- `Revisions()` — lists all plan.md commits via `git log` (`internal/plan/gitrepo_history.go:48`)
- `ContentAt(sha)` — reads plan.md at any commit (`internal/plan/gitrepo_history.go:83`)
- `DiffBetween(base, target)` — unified diff between revisions (`internal/plan/gitrepo_history.go:90`)
- `HeadCommitHash()` — current HEAD SHA (`internal/plan/gitrepo.go:215`)
- Revert flow — `RevertPlanIntent{Content, ShortSHA}` with confirmation modal

**Sub-state integration:**

```
FocusArtTop/FocusArtBottom (architect selected)
    │ ^Y
    v
┌─────────────────────────────────────────────────┐
│  FocusPlanHistory (nested sub-state)            │
│  Embeds PlanHistoryScreen within ArtifactViewer │
│  ┌─ revision list ─┐ ┌─ diff/content VP ──────┐│
│  │ HEAD  (current)  │ │ unified diff or full   ││
│  │ abc12 (v2 rev)   │ │ plan markdown          ││
│  │ def34 (v1 rev)   │ │                        ││
│  └──────────────────┘ └────────────────────────┘│
│  [r] revert (live only) │ [Esc] back to card   │
└─────────────────────────────────────────────────┘
    │ Esc
    v
 FocusArtTop (returns to architect artifact view)
```

**FSM extension:**

| Current State                       | Input                     | Next State       | Action                                                                                           |
| ----------------------------------- | ------------------------- | ---------------- | ------------------------------------------------------------------------------------------------ |
| FocusArtTop/Bottom (architect card) | `^Y`                      | FocusPlanHistory | Open `PlanHistoryScreen` with `historyDir` from session artifacts; readOnly=true in history mode |
| FocusPlanHistory                    | `Esc`                     | FocusArtTop      | Close plan history, return to artifact view                                                      |
| FocusPlanHistory                    | `r` (if live + !readOnly) | FocusPlanHistory | Show revert confirmation modal (existing flow)                                                   |
| FocusPlanHistory                    | `↑`/`↓`                   | FocusPlanHistory | Navigate revision list (delegate to `PlanHistoryScreen.Update`)                                  |
| FocusPlanHistory                    | `PgUp`/`PgDn`             | FocusPlanHistory | Scroll diff/content viewport                                                                     |
| FocusPlanHistory                    | `d`/`c`                   | FocusPlanHistory | Toggle diff vs content pane mode (existing `paneMode`)                                           |

**Data path:**

- Live run: `GateRequest.PlanHistoryDir` carries the path to the plan-history git micro-repo (set in `engine.go:665`); `GateRequest.PlanHistoryHeadSHA` identifies current HEAD
- Historical run: `<session-root>/plan-history/` directory (if present; may be absent for runs with only one plan iteration)
- `AgentCard` stores `PlanHistoryDir string` — populated from `GateRequest` (live) or from session directory scan (history)

**Guard:** `^Y` is a no-op if:

- Selected card is not an architect
- `PlanHistoryDir` is empty (single-iteration run, no history repo created)
- `plan.OpenGitRepo(dir)` returns an error (directory missing/corrupt)

### Artifact Viewer (Right Pane)

**Top VP content by agent type:**
| Agent | Top VP Content |
|-------|---------------|
| Researcher | Research prompt sent to Claude |
| Architect | System prompt / task description |
| Critic | Critic system prompt |
| Worker | Approved plan markdown (worker's input) |

**Bottom VP content by agent type:**
| Agent | Bottom VP Content |
|-------|------------------|
| Researcher | Research output summary |
| Architect | Produced plan markdown (glamour-rendered) |
| Critic | Critic review report |
| Worker | `git status` — list of changed files (or `worker_output.txt`) |

Both viewports use `glamour` for markdown rendering. File paths tracked for `^E`.

### Log Viewer (Bottom Pane)

- During live run: continuously polls `StreamRing.AgentEntries(selectedAgentID)` on tick
- For completed/historical: calls `harness.ParseSessionLog(reader)` (Step 1.4) to read `<session>/<agent>_session.jsonl` → returns `[]Activity` + `[]string` in the same format as `StreamRing` entries. **Does NOT reimplement JSONL parsing** — delegates to the exported harness function.
- Shimmer animation appended at bottom when the selected agent is currently streaming
- Format: activity log style (`read: file.go`, `grep: "pattern"`, raw stream lines)

---

## Phase 4: Integration Points

### Step 4.1 — PipelineScreen Hosts DashboardModel (replaces `dashboardVP`)

**File:** `internal/tui/screen_pipeline.go`

- **Remove** `dashboardVP viewport.Model` field (L95) and all 13 references:
  - Construction (L114)
  - `SetContent("")` / `GotoTop()` (L163, L166)
  - `SetContent(s.viewDashboard())` in `SyncViewports` (L183)
  - `SetWidth` / `SetHeight` in `RecalculateLayout` (L213-214)
  - `dashboardVP.Update(msg)` key/mouse routing (L354, L436)
  - `dashboardVP.View()` in `View()` body (L658)
  - `dashboardVP.Width()` in `viewDashboard` (L955)
- **Rationale:** `DashboardModel` manages its own sub-viewports (`AgentMenuModel`, `ArtifactViewerModel.topVP/bottomVP`, `LogViewerModel.vp`). An outer wrapping viewport would silently consume `tea.MouseMsg` (scroll wheel) and `tea.KeyMsg` events, preventing them from reaching the inner FSM.
- **Add** `dashboard DashboardModel` field
- Replace `viewDashboard()` body with `s.dashboard.View()`
- When `s.showDashboard == true`: route all `tea.KeyMsg` to `s.dashboard.Update(msg)` first
- On `CloseDashboardIntent` from dashboard: set `s.showDashboard = false`
- On tick: if dashboard visible, feed live stream data to `dashboard.log`

### Step 4.2 — RunDetailScreen Embeds DashboardModel

**File:** `internal/tui/screen_run_detail.go`

- Replace existing `detailVP`, `stepsVP`, `logVP` with embedded `DashboardModel`
- On initialization (when entering detail from runs list):
  - Parse `agent.RunDetail` → populate `AgentMenuModel` with steps as cards
  - Load `prompt.md` → Right Top VP (already in memory via `RunDetail.PlanMarkdown`)
  - Load `final_plan.md` / `worker_output.txt` → Right Bottom VP (already in memory)
  - Load `<agent>_session.jsonl` → **via async `tea.Cmd`** (see Step 4.2a below)
- Same keybindings, same FSM, same render — only data source differs (disk vs live)

### Step 4.2a — Async Log Loading (no blocking IO in Update)

**Rationale:** Session JSONL files can be multi-megabyte. Loading them synchronously in `Update()` or model initialization violates Bubble Tea's non-blocking contract and the TUI instruction calling this out as a known pressure point.

**Implementation:**

- Define message: `type logLoadedMsg struct { AgentID string; Activities []harness.Activity; Lines []string; Err error }`
- When agent selection changes in `DashboardModel` (via `Enter` in menu or cursor change), emit a `tea.Cmd` that calls `harness.ParseSessionLog()` in a goroutine:
  ```go
  func loadAgentLogCmd(sessionPath, agentID string) tea.Cmd {
      return func() tea.Msg {
          f, err := os.Open(filepath.Join(sessionPath, agentID+"_session.jsonl"))
          if err != nil {
              return logLoadedMsg{AgentID: agentID, Err: err}
          }
          defer f.Close()
          activities, lines, err := harness.ParseSessionLog(f)
          return logLoadedMsg{AgentID: agentID, Activities: activities, Lines: lines, Err: err}
      }
  }
  ```
- `LogViewerModel.Update()` handles `logLoadedMsg`: sets content, syncs viewport
- While loading, render a "Loading logs..." placeholder (no blank flash)
- This pattern matches the existing `loadPlanRevisions` / `loadPlanRevisionDetail` async pattern in `screen_plan_history.go`

### Step 4.2b — Prerequisite: Extend `LoadRunDetail` for Revisions and Critic

**File:** `internal/agent/session.go` (`LoadRunDetail`, L142)

**Problem:** The current `agentOrder` is hardcoded as `[]string{"researcher", "architect", "worker", "validator"}`. This:

- Omits `critic_meta.json` entirely
- Omits revision artifacts (`architect_critic_revision_meta.json`, `architect_revision_meta.json`)
- Cannot represent multiple architect iterations as separate cards

**Fix:** Replace the fixed-order scan with a glob-based discovery:

```go
// Replace hardcoded agentOrder with:
matches, _ := filepath.Glob(filepath.Join(runPath, "*_meta.json"))
for _, match := range matches {
    data, err := os.ReadFile(match)
    if err != nil { continue }
    var meta StepMeta
    if err := json.Unmarshal(data, &meta); err != nil { continue }
    detail.Steps = append(detail.Steps, meta)
}
// Sort by StartTime to preserve pipeline order
sort.Slice(detail.Steps, func(i, j int) bool {
    return detail.Steps[i].StartTime.Before(detail.Steps[j].StartTime)
})
```

This discovers all step metas including:

- `researcher_meta.json`
- `architect_meta.json` (first iteration)
- `critic_meta.json`
- `architect_critic_revision_meta.json` (critic-prompted revision)
- `architect_revision_meta.json` (gate-prompted revision)
- `worker_meta.json`
- `validator_meta.json`

### Step 4.2c — Prerequisite: Unique Revision Artifact Names

**File:** `internal/orchestrator/engine.go`

**Problem:** `writeArtifactJSON(session, "architect_revision_meta.json", ...)` is called on every revision iteration (L740, L763, L872, L895), overwriting the previous revision's metadata. Similarly `architect_critic_revision_meta.json` (L569, L592) overwrites.

**Fix:** Include the attempt number in the artifact filename:

```go
// Before (overwrites):
writeArtifactJSON(session, "architect_revision_meta.json", ...)

// After (unique per attempt):
writeArtifactJSON(session, fmt.Sprintf("architect_revision_%d_meta.json", architectAttempt), ...)
```

Same pattern for `architect_critic_revision_meta.json` → `architect_critic_revision_%d_meta.json`.

The glob in Step 4.2b (`*_meta.json`) will naturally discover all numbered revision files. `StepMeta.StartTime` provides sort order.

**No backward compatibility required** (per plan scope).

### Step 4.3 — Footer Adapts to State

**File:** `internal/tui/screen_pipeline.go` (or `model.go` `viewFooter`)

| Context               | Footer Content                                                |
| --------------------- | ------------------------------------------------------------- |
| Main view (streaming) | `[^N] new │ [^D] dashboard │ [^H] history │ [^C] quit`        |
| Dashboard overlay     | `[Esc] return │ [^E] editor │ [Tab] cycle │ [PgUp/Dn] scroll` |
| Plan review           | `[^A] accept │ [^E] edit │ [Enter] comment │ [^D] diff`       |

Footer is rendered by the parent (PipelineScreen/Model); content changes based on `showDashboard`.

---

## TUI Components — Msg, Update, Render Contract

### Component: Status Bar (`viewStatusLine`)

**Location:** `internal/tui/screen_pipeline.go` (pure render function on `PipelineScreen`)

| Aspect                 | Detail                                                                                               |
| ---------------------- | ---------------------------------------------------------------------------------------------------- |
| **Inputs (read-only)** | `s.agents []AgentRow`, `s.liveInput`, `s.liveOutput`, `s.liveStart`, `s.animFrame`, `width int`      |
| **Msg**                | None (rendered on tick, reads model state)                                                           |
| **Update**             | None (state updated by `tickMsg` handler and `ApplyEvent`)                                           |
| **Render**             | Pure function `viewStatusLine(width) string` — computes chain + detail + shimmer, applies truncation |

### Component: Shimmer Animation

**Location:** `internal/tui/screen_pipeline.go` (part of `viewStreaming` and `viewStatusLine`)

| Aspect        | Detail                                                                              |
| ------------- | ----------------------------------------------------------------------------------- |
| **State**     | `PipelineScreen.animFrame int`                                                      |
| **Msg**       | `animTickMsg` — increments `animFrame`                                              |
| **Render**    | `shimmerFrames[animFrame % 5]` for status bar; dot-crawl pattern for streaming tail |
| **Lifecycle** | Started on first `EventAgentStarted`; stopped when pipeline completes               |

### Component: Input Zone / AskUserQuestion

**Location:** `internal/tui/question.go` + `screen_pipeline.go:viewInputZone()`

| Aspect            | Detail                                                                                            |
| ----------------- | ------------------------------------------------------------------------------------------------- |
| **State**         | `PipelineScreen.question userQuestionModel`, `hasQuestion bool`                                   |
| **Msg**           | `tea.KeyMsg` routed when `content == ContentUserQuestion`                                         |
| **Update**        | `question.Update(msg)` handles option selection, textarea input                                   |
| **Render**        | `viewInputZone()` renders question when active; auto-grows by returning computed height to layout |
| **Layout effect** | `recalculateLayout()` checks `ContentUserQuestion` and expands input zone height dynamically      |

### Component: `DashboardModel` (FSM Controller)

**Location:** `internal/tui/dashboard.go`

| Aspect     | Detail                                                                                                              |
| ---------- | ------------------------------------------------------------------------------------------------------------------- |
| **State**  | `focus DashboardFocus`, `width int`, `height int`, sub-models below                                                 |
| **Msg**    | `tea.KeyMsg` (routing), `tea.WindowSizeMsg` (broadcast to all), `dashboardTickMsg` (live data refresh)              |
| **Update** | FSM router: handles Tab/S-Tab/Esc/Enter for focus transitions; delegates all other keys to focused sub-model        |
| **Render** | `View() string` — pure. `lipgloss.JoinVertical(topRow, bottomRow)` where topRow = `JoinHorizontal(menu, artifacts)` |

### Component: `AgentMenuModel` (Left Pane)

**Location:** `internal/tui/dashboard_menu.go`

| Aspect     | Detail                                                                                                                        |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------- |
| **State**  | `items []AgentCard`, `cursor int`, `width int`, `height int`, `focused bool`                                                  |
| **Msg**    | `tea.KeyMsg` (Up/Down/PgUp/PgDn when focused)                                                                                 |
| **Update** | Cursor movement with bounds clamping; emits `agentSelectedMsg{id}` on cursor change                                           |
| **Render** | Pure: iterates items, renders cards with highlight on cursor, scrolls if items exceed height                                  |
| **Data**   | `AgentCard { ID, State, ModelDisplay, InputTokens, OutputTokens, Elapsed, ContextWindow, TokPerSec, IsLive, PlanHistoryDir }` |

### Component: `ArtifactViewerModel` (Right Pane)

**Location:** `internal/tui/dashboard_artifact.go`

| Aspect     | Detail                                                                                                       |
| ---------- | ------------------------------------------------------------------------------------------------------------ |
| **State**  | `topVP viewport.Model`, `bottomVP viewport.Model`, `topPath string`, `bottomPath string`, `focusTop bool`    |
| **Msg**    | `tea.KeyMsg` (scroll when focused), `setArtifactContentMsg{top, bottom string}`                              |
| **Update** | Delegates scroll to active VP; handles `setArtifactContentMsg` by calling `glamour.Render` + `VP.SetContent` |
| **Render** | Pure: `JoinVertical(topVP.View(), divider, bottomVP.View())`                                                 |
| **Layout** | Top 50% height, Bottom 50% height (of right pane allocation)                                                 |

### Component: `LogViewerModel` (Bottom Pane)

**Location:** `internal/tui/dashboard_log.go`

| Aspect     | Detail                                                                     |
| ---------- | -------------------------------------------------------------------------- |
| **State**  | `vp viewport.Model`, `lines []string`, `isStreaming bool`, `animFrame int` |
| **Msg**    | `tea.KeyMsg` (scroll), `logUpdateMsg{lines []string, streaming bool}`      |
| **Update** | Appends new lines on `logUpdateMsg`; scroll delegation; shimmer frame sync |
| **Render** | Pure: `vp.View()` + shimmer suffix if streaming                            |

### Component: Footer

**Location:** Part of `Model.View()` in `internal/tui/model.go`

| Aspect       | Detail                                                                                             |
| ------------ | -------------------------------------------------------------------------------------------------- |
| **State**    | Derived from `m.state` and `m.pipelineScreen.showDashboard`                                        |
| **Msg**      | None (stateless render)                                                                            |
| **Render**   | Pure: returns key-hint line based on current context                                               |
| **Variants** | Main: `[^N][^D][^H][^C]`; Dashboard: `[Esc][^E][Tab][PgUp/Dn]`; Plan Review: `[^A][^E][Enter][^D]` |

---

## Phase 5: Data Flow — Ownership Table

| Data                            | Owner                            | Write API                                           | Read API                                    | Consumers                                    |
| ------------------------------- | -------------------------------- | --------------------------------------------------- | ------------------------------------------- | -------------------------------------------- |
| Live token deltas               | harness → stream_ring            | `UsageSink.OnUsage()` → `StreamRing.RecordUsage()`  | `SnapshotUsage()`                           | TUI tick → `liveInput/liveOutput`            |
| Per-agent accumulated tokens    | orchestrator                     | `Event{Type:AgentDone, InputTokens, OutputTokens}`  | `AgentRow.InputTokens/OutputTokens`         | Status bar, Agent cards                      |
| Agent model metadata            | config → orchestrator            | `resolveAgentMeta()` → `Event.Meta`                 | `AgentRow.ModelDisplay/ContextWindow`       | Status bar, Agent cards                      |
| Stream entries (tool use, text) | orchestrator (StreamRing)        | `StreamRing.Append()`                               | `Snapshot()`, `AgentEntries(id)`            | Streaming view, LogViewer                    |
| Animation frame                 | TUI                              | `animTickMsg` increments                            | `animFrame` field                           | shimmer in status bar + streaming + log pane |
| Session artifacts               | orchestrator writes, agent reads | `writeArtifactJSON()`, `SessionDir.WriteArtifact()` | `agent.LoadRunDetail()`, `agent.ListRuns()` | DashboardModel (history mode)                |
| Dashboard focus                 | TUI (DashboardModel)             | `Update()` FSM transitions                          | `focus` field                               | Render highlighting, key delegation          |

---

## Testing Scenarios

### A. Positive Paths — Full State Machine Traversal

**Test A1: Status Bar Render Accuracy**

```
Given: 4 agents [researcher:done, architect:done, critic:done, worker:running]
       worker liveInput=12000, liveOutput=8000, liveStart=30s ago, ContextWindow=200000
When:  viewStatusLine(120) is called
Then:  output = "✓res ✓arch ✓crit ▶work: <model> ↑12k ↓8k ⊞10% 267t/s ·∘○∘·"
```

**Test A2: Status Bar Left-Overflow Truncation**

```
Given: same as A1, but width=40
When:  viewStatusLine(40) is called
Then:  output starts with "<.." and active agent stats are fully visible
       shimmer may be dropped, tok/s may be dropped
```

**Test A3: Dashboard Entry and Full Tab Cycle**

```
Sequence: ^D → FocusMenu
          Tab → FocusArtTop
          Tab → FocusArtBottom
          Tab → FocusLog
          Tab → FocusMenu (wraps)
Assert:   focus state after each step matches expected
```

**Test A4: Dashboard Deep Dive and Return**

```
Sequence: ^D → FocusMenu
          Down, Down, Down → cursor at index 3 (critic)
          Enter → FocusArtTop, artifacts loaded for critic
          PgDown → topVP scrolled (verify offset > 0 if content allows)
          Esc → FocusMenu, cursor still at index 3
          Enter → FocusArtTop again
          Tab → FocusArtBottom
          PgDown → bottomVP scrolled
          Esc → FocusMenu
```

**Test A5: Reverse Tab Cycle (Shift+Tab)**

```
Sequence: ^D → FocusMenu
          Shift+Tab → FocusLog
          Shift+Tab → FocusArtBottom
          Shift+Tab → FocusArtTop
          Shift+Tab → FocusMenu (wraps)
```

**Test A6: AskUserQuestion Auto-Growth**

```
Given: pipeline screen in ContentUserQuestion, 5 options
When:  recalculateLayout() computes
Then:  inputHeight = max(2, 5+2) = 7
       contentHeight reduced by 5 compared to normal
       question renders in input zone, NOT in content viewport
```

**Test A7: Shimmer Animation Advances**

```
Given: pipeline running, animFrame=0
When:  3 animTickMsg dispatched
Then:  animFrame=3
       viewStatusLine renders shimmerFrames[3]
       viewStreaming appends dot-crawl frame 3
When:  pipeline completes (EventComplete)
Then:  animTickCmd not reissued
       shimmer not rendered
```

**Test A8: History Dashboard Initialization**

```
Given: completed run with researcher, architect, worker steps on disk
When:  RunDetailScreen initializes DashboardModel from disk artifacts
Then:  AgentMenu has 3 items
       Selecting researcher → topVP shows prompt.md, bottomVP shows research output
       LogViewer shows parsed researcher_session.jsonl
       Focus starts at FocusMenu
```

**Test A9: Multiple Architect Iterations as Separate Cards**

```
Given: pipeline ran: researcher, architect#1, critic, architect#2, worker
When:  DashboardModel populated
Then:  AgentMenu has 5 items (each is a separate card)
       architect#1 card shows original plan artifacts
       architect#2 card shows revised plan artifacts
       Each card has independent token counts
```

**Test A10: Plan Revision History Sub-State (^Y)**

```
Sequence: ^D → FocusMenu
          Down → select architect#1
          Enter → FocusArtTop (architect artifacts loaded)
          ^Y → FocusPlanHistory (PlanHistoryScreen opens with plan-history dir)
          Down, Down → navigate revision list (2 revisions back)
          PgDown → scroll diff viewport
          d → toggle to content view
          Esc → FocusArtTop (plan history closed, architect artifacts visible again)
Assert:   PlanHistoryScreen.Open() called with correct historyDir
          Revision list populated from git log
          Diff rendered between selected revision and HEAD
          After Esc, artifact viewports unchanged
```

**Test A11: Plan Revision Revert (Live Only)**

```
Given: live pipeline at plan gate, dashboard open, architect selected
Sequence: ^Y → FocusPlanHistory (readOnly=false because live)
          Down → select previous revision
          r → revert confirmation modal shown
          Enter (Yes) → RevertPlanIntent emitted with revision content
Assert:   PendingIntent is RevertPlanIntent{Content: <revision content>, ShortSHA: <sha>}
          Dashboard returns to FocusArtTop after revert
```

### B. Negative Paths — Boundary & Error Conditions

**Test B1: Esc at Root Exits Dashboard**

```
Given: dashboard open, FocusMenu
When:  Esc pressed
Then:  CloseDashboardIntent emitted
       Dashboard overlay closes, main view resumes
       No panic, no state corruption
```

**Test B2: Out-of-Bounds Menu Scrolling**

```
Given: AgentMenu with 3 items, cursor at 0
When:  100 × KeyUp dispatched
Then:  cursor remains at 0, no panic
When:  100 × KeyDown dispatched
Then:  cursor remains at 2 (last index), no panic
```

**Test B3: Out-of-Bounds Viewport Scrolling**

```
Given: ArtifactViewer topVP with content shorter than viewport height
When:  PgDown dispatched in FocusArtTop
Then:  viewport offset remains 0, no panic
```

**Test B4: Empty Agent List (Early ^D)**

```
Given: DashboardModel with 0 agents (^D pressed before any agent started)
When:  Enter pressed in FocusMenu
Then:  No transition occurs, no nil dereference
       ArtifactViewer shows placeholder "No agent selected"
       LogViewer shows placeholder "No logs available"
```

**Test B5: Missing Artifacts on Disk (History)**

```
Given: RunDetail where critic step exists but critic_session.jsonl is missing
When:  User selects critic in menu, presses Enter
Then:  ArtifactViewer shows critic report (from meta)
       LogViewer shows "No session log available" placeholder
       No panic from file-not-found
```

**Test B5a: ^Y on Non-Architect Card (No-Op)**

```
Given: Dashboard open, FocusArtTop, selected card is "researcher"
When:  ^Y pressed
Then:  No state change (^Y is a no-op for non-architect cards)
       Focus remains FocusArtTop
```

**Test B5b: ^Y on Architect Without Plan History (No-Op)**

```
Given: Dashboard open, FocusArtTop, selected card is "architect"
       but AgentCard.PlanHistoryDir is empty (single-iteration run)
When:  ^Y pressed
Then:  No state change, no panic
       Focus remains FocusArtTop
```

**Test B6: Rapid Focus Switching Under Load**

```
Given: Dashboard open, live streaming active
When:  Rapid alternating Tab + Shift+Tab (50 iterations)
Then:  Focus state always valid (0-3), no race, no render corruption
       go test -race passes
```

**Test B7: Terminal Resize During Dashboard**

```
Given: Dashboard open at 120x40
When:  WindowSizeMsg{Width:60, Height:20} dispatched
Then:  All sub-components receive resize
       Layout recalculates: menu=15 cols, right=45 cols, logs=6 rows
       No panic at minimum viable size
When:  WindowSizeMsg{Width:30, Height:8} (below minimum)
Then:  Graceful degradation (show "terminal too small" or collapse panes)
```

**Test B8: Zero Tokens / Zero Elapsed (Div-by-Zero)**

```
Given: agent running, liveOutput=0, liveStart=now (0 elapsed seconds)
When:  viewStatusLine renders tok/s
Then:  displays "0t/s" or omits tok/s entirely, no division by zero panic
```

### C. Edge Cases — Data Anomalies

**Test C1: Missing Critic Step (Partial Pipeline)**

```
Given: Pipeline config has no critic (skipped)
       Menu shows: researcher, architect, worker (no critic card)
When:  User navigates all items
Then:  No gaps, no nil cards, cursor clamped to actual item count
```

**Test C2: Multiple Architect Iterations with Empty Revision**

```
Given: Architect #2 produced no plan change (continuation without edit)
When:  User selects architect#2 in dashboard
Then:  Bottom VP shows "No plan changes in this iteration" or identical plan
       No panic, no empty viewport
```

**Test C3: Very Long Agent Output (Viewport Stress)**

```
Given: Worker produced 10,000 lines of output
When:  LogViewer loads this output
Then:  Viewport renders without blocking
       PgDown advances correctly
       Memory doesn't explode (ring buffer caps entries at maxEntries)
```

**Test C4: Unicode / Wide Characters in Status Bar**

```
Given: Model name contains CJK characters or emoji
When:  viewStatusLine computes lipgloss.Width
Then:  Truncation accounts for double-width chars correctly
       Status bar never exceeds terminal width
```

**Test C5: Live-to-History Transition**

```
Given: Dashboard open during live run showing worker streaming
When:  Pipeline completes (EventComplete)
Then:  Worker card state transitions from "▶" to "✓"
       LogViewer stops showing shimmer
       Stats become static (final values)
       Same dashboard remains visible (no forced close)
```

### D. Smoke Tests (All States × Sizes)

**Test D1: Render Without Panic** (extends existing `TestLayout_AllStatesRenderWithoutPanic`)

```
For each state in [pipeline+streaming, pipeline+dashboard, pipeline+question,
                   runDetail+dashboard, runsList]:
  For each size in [(60,10), (80,24), (120,40), (200,60)]:
    model.View() does not panic
    lipgloss.Height(output) <= terminal height
    lipgloss.Width(each line) <= terminal width
```

**Test D2: ^C Always Quits Through Dashboard**

```
Given: Dashboard open in any focus state
When:  Double ^C pressed
Then:  Application exits cleanly (existing escape hatch preserved)
```

---

## Relevant Files Summary

| File                                   | Action     | Key Changes                                                                                                                                  |
| -------------------------------------- | ---------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/harness/output.go`           | MODIFY     | `UsageSink` interface (alongside existing `ActivitySink`)                                                                                    |
| `internal/harness/claude_cli.go`       | MODIFY     | Extend `dispatchStreamEvent` to check `UsageSink`                                                                                            |
| `internal/harness/logparser.go`        | **CREATE** | `ParseSessionLog()` — exported reader for historical JSONL session logs                                                                      |
| `internal/orchestrator/stream_ring.go` | MODIFY     | `liveInput/liveOutput/liveStart`, `RecordUsage()`, `SnapshotUsage()`, `agentUsage` map                                                       |
| `internal/orchestrator/events.go`      | MODIFY     | `AgentMeta` type, `Meta` field on `Event`                                                                                                    |
| `internal/orchestrator/engine.go`      | MODIFY     | `resolveAgentMeta()` (case-insensitive), populate `Event.Meta`, `streamWriter.OnUsage`, numbered revision artifact filenames                 |
| `internal/tui/layout.go`               | MODIFY     | `constSidebarHeight` 6→1, delete `constDefaultContextWindow`                                                                                 |
| `internal/tui/model.go`                | MODIFY     | `AgentRow` new fields, `animTickMsg`/`animTickCmd`, tick polls live stats, dynamic inputHeight                                               |
| `internal/tui/messages.go`             | MODIFY     | `animTickMsg` type                                                                                                                           |
| `internal/tui/screen_pipeline.go`      | MODIFY     | Remove `sidebarVP` + `dashboardVP`, new `viewStatusLine()`, shimmer in `viewStreaming`, `liveInput/liveOutput` fields, host `DashboardModel` |
| `internal/tui/question.go`             | MODIFY     | Render in input zone instead of content area                                                                                                 |
| `internal/tui/dashboard.go`            | **CREATE** | `DashboardModel`, `DashboardFocus`, FSM `Update()`, layout `View()`                                                                          |
| `internal/tui/dashboard_menu.go`       | **CREATE** | `AgentMenuModel`, `AgentCard`, cursor navigation, card rendering                                                                             |
| `internal/tui/dashboard_artifact.go`   | **CREATE** | `ArtifactViewerModel`, dual viewport, glamour rendering, `^E` path tracking                                                                  |
| `internal/tui/dashboard_log.go`        | **CREATE** | `LogViewerModel`, streaming shimmer, log parsing                                                                                             |
| `internal/tui/dashboard_test.go`       | **CREATE** | Full FSM traversal tests (all scenarios A-D)                                                                                                 |
| `internal/tui/screen_run_detail.go`    | MODIFY     | Replace custom views with embedded `DashboardModel`, async log loading via `tea.Cmd`                                                         |
| `internal/tui/icons.go`                | MODIFY     | Status icons (✓✗⊘●▶○), arrow icons (↑↓⊞)                                                                                                     |
| `internal/agent/session.go`            | MODIFY     | `StepMeta` gains `ModelDisplay`, `Provider`, `ContextWindow`; `LoadRunDetail` glob-based step discovery (replaces hardcoded agent order)     |

---

## Scope & Decisions

**In scope:**

- Live token metrics via `UsageSink` (parseStream + parseStreamLines)
- Agent model metadata flow (config → event → AgentRow)
- 1-line status bar with shimmer/pulse animation and left-overflow truncation
- Auto-growing AskUserQuestion in bottom input zone
- 3-pane dashboard (live `^D` + history) with isolated Bubble Tea sub-components
- Strict FSM with exhaustive test coverage
- Each architect re-plan iteration = separate card in dashboard menu
- Unified dashboard for both live and historical runs (same component, different data source)

**Out of scope:**

- Thinking tokens display (future — field slot planned but not parsed)
- Token budget display in status line (depends on separate token ownership refactor)
- Nerd Font icon variants (standard Unicode only; configurable icon set is future)
- Backward compatibility for on-disk session format (no migration needed)

**Key decisions:**

- Arrow semantics: `↑` = consumed (input), `↓` = produced (output)
- `Esc` from `FocusMenu` = close dashboard overlay (exit), not no-op
- Animation: shimmer ~200ms tick, pulse ~600ms tick (always out of phase)
- Layout proportions: Left 25%, Right 75% (horizontal); Logs 30% height (vertical)
- `ArtifactViewer` splits right pane 50/50 vertically (top input, bottom output)
