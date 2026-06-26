# Orqestra — internal/tui/ (Bubble Tea MVU)

Extends the root `CLAUDE.md` (which loads alongside this file — §0 Prime Directive and §1 Go
fundamentals bind here). It adapts rune's TUI standard `pkg/ui/CLAUDE.md` from the sibling rune
checkout (see `replace rune` in go.mod); orqestra reuses rune's `keymap`/`styles`/`scroll`. Terms
are defined in `CONTEXT.md`; decisions in `docs/adr/`.

**Load-bearing rule restated** (this file is not re-injected after a context compaction): TUI errors
are user-visible truth — store them in model state until rendered; never rely on suppressed stderr
(root §0). `View()` is pure (§5).

## Vocabulary (see CONTEXT.md — use these terms, avoid the listed synonyms)

- **Timeline** — the single chronological scrollback: a vertical stack of full-width **Frames**.
- **Frame** — one full-width unit (Prose / Tool / Plan / Phase separator / Steer message). The unit
  of copy-to-clipboard.
- **Live Frame** — the most-recent Frame while its event is still unfolding (at most one at a time).
- **Static Frame** — a concluded Frame: frozen, rendered once, never changes again.
- "**event frame**" always means one Claude CLI stream-json (NDJSON) line — never an unqualified
  "frame".

Decisions: `docs/adr/0001-frames-are-frozen-renders-with-a-live-tail.md`,
`docs/adr/0002-plan-frame-markdownedit-one-shot-renderer.md`.

## 2. Component architecture (divide & conquer)

### 2.1 State residency — the component that RENDERS state OWNS it

Litmus test: if deleting a child's `View()` call would make a parent field dead code, that field
belongs on the child.

```go
// WRONG — screen holds what only the frame renders
type PipelineScreen struct { proseText string; toolRows []Row }
// RIGHT — the frame owns its render state; the screen holds the frame
type PipelineScreen struct { live frame.InteractiveFrame }
```

### 2.2 Frames are a polymorphic items list (the one sanctioned interface)

A heterogeneous, open-ended list of item kinds is the ONE place to use an interface for
polymorphism (open/closed over Frame kinds). Everywhere else prefer concrete component types
(rune §2.2).

- `frame.StaticFrame` — `Rows() []Row`, `SetWidth(w) StaticFrame`. Implementations (`Tool`, `Phase`,
  `Plan`, `TurnSnapshot`) use **value receivers**.
- `frame.InteractiveFrame` — `Update(tea.Msg) (InteractiveFrame, tea.Cmd)`, `Resolve() StaticFrame`,
  `Rows()`, `SetWidth`. Live frames (`LiveProse`, `TurnGroup`) accumulate state and use **pointer
  receivers** — that is their contract. `Resolve()` freezes a Live Frame into a Static one
  (ADR 0001).

### 2.3 Screens compose; frames render

Screen sub-models (`PipelineScreen`, `PromptScreen`, `RunsListScreen`, `RunDetailScreen`) own
layout, focus, and message routing. The root `Model` (`model.go`) owns global keys, screen routing,
and `recalculateLayout()`. A frame never reads sibling-frame state, decision channels, or contexts.

### 2.4 A message is defined in its PRODUCER's package

The frame that emits an event defines the message.

```go
// RIGHT (internal/tui/frame/prose_live.go:12,16)
type BlinkMsg struct{}
type DeltaMsg struct{ Text string }
```

Intents flow up to the root via the `intent` marker interface (messages.go:29); orchestration
events flow down through typed messages.

### 2.5 Dependency injection

Build keymap/styles ONCE from rune at startup (`runeUI`, runeui.go) and pass them down by value.
NEVER store `context.Context` or a logger on a Model (root §1.3).

## 3. Keybindings

Global keys — `ctrl+c`, app-level `esc`, merge decisions, run navigation, pipeline cancellation —
are rooted in `Model`. A screen forwards scoped keys to its focused child. rune's `keymap.Bindings`
is the single source of truth; one physical key string appears in exactly one binding.

## 4. Layout & dimensions

- Compute every dimension in `recalculateLayout()` (or a screen update method) on
  `tea.WindowSizeMsg` and on structural change — never in a render path.
- Query a child's intrinsic size (`Height()`); never hardcode it with a magic `const`.
- lipgloss v2 is **border-box**: `Width(n)`/`Height(n)` include borders. Subtract the frame ONCE,
  when sizing the child; the border style in `View()` gets the OUTER dimension. Do not subtract in
  both places.
- Clamp dimensions before assigning them to Bubble components; a tiny terminal renders the
  small-screen message without panicking.

## 5. The Elm cycle

- **Pure View.** `View()` renders only: no IO, no channel reads, no focus/viewport mutation, no
  `SetContent`/`SetWidth`/`SetHeight`/`GotoTop`/`GotoBottom`.
- **Non-blocking Update.** `Update()` may mutate model state and return commands, but never blocks
  on disk, network, timers, subprocesses, or long parsing — push that into a `tea.Cmd` that returns
  a typed completion message.
- **Capture before closure.** Copy model-derived values into locals BEFORE a `tea.Cmd` closure; it
  runs later.
- **Batch every child Cmd.** After each `child.Update(msg)`, accumulate the returned `tea.Cmd` and
  return `tea.Batch(cmds...)`; `tea.WindowSizeMsg` reaches every child that stores dimensions.
- **Preserve scroll.** `viewport.SetContent()` is scroll-destructive: capture `AtBottom()`/offset
  before, restore after — in `Update`, never `View`.
- **Goroutine messages carry copies or immutable values** — never shared pointers, maps, or buffers.

## 6. Async command boundaries

File reads (run lists, run details, session logs, edited plans) and editor launches are `tea.Cmd`
functions returning typed messages; failures update visible model error state unless explicitly
best-effort. Long-running orchestration stays outside the TUI — the model consumes orchestrator
events through channels/commands and never mutates orchestrator state directly. Timer ticks may
refresh elapsed time and live stream output, but never reset user scroll or rebuild expensive
content unless the underlying data changed.

## 7. Current state & migration direction (in flight — branch `tui-rehaul`)

- Live structure: `model.go` (root/router + `recalculateLayout` at :213/:221/:246), the screen
  sub-models, `messages.go` (intents), `timeline.go` + `timeline_*` (the scrollback), `chat.go`
  (the always-present input that hosts gates + questions), and `internal/tui/frame/` (the Frame
  components beachhead).
- rune boundary: rune types confined to `runeui.go`, `promptinput.go`, `screen_prompt.go`; shared
  layers (`keymap` / `styles` / `scroll`) consumed from rune.
- MIGRATION: decompose the `Timeline` god-object and the `ContentMode` flat enum (model.go:29) into
  Frame components plus one active sub-model.

```go
// WRONG — one container switching on a kind-enum, holding every kind's fields
type Timeline struct { kind FrameKind; prose string; tool ToolRows; plan string }
// RIGHT — each kind is its own frame type behind StaticFrame / InteractiveFrame
frames []frame.StaticFrame   // + at most one live frame.InteractiveFrame
```

When you touch a `ContentMode` arm, migrate that mode toward a sub-model/frame; do not add new
`ContentMode` values.

## 8. Debugging the TUI with ttyd + Playwright

To visually observe or drive the TUI (verify a layout fix, test a key-binding, reproduce a render
bug), expose the running terminal with `ttyd` and drive it with the Playwright MCP `browser_*` tools.

1. **Build & serve:** `make build` then `ttyd -p 7681 ./orqestra` (add `--config orqestra.yaml` as
   needed). `ttyd` serves xterm.js at `http://localhost:7681` and inherits your real Claude CLI
   credentials.
2. **Open & size first:** `browser_navigate {url:"http://localhost:7681"}` then
   `browser_resize {width:1400, height:900}` (≈ 240×50 cols/rows). Size before any screenshot —
   layout recalculates on every `tea.WindowSizeMsg`; a narrow viewport triggers the small-screen
   fallback.
3. **Focus & observe:** `browser_click {target:"terminal"}`, then `browser_take_screenshot`
   (xterm.js renders to a canvas, so screenshots beat `browser_snapshot`).
4. **Keys:** `browser_press_key` with `KeyboardEvent.key` names — `Enter`, `Escape`, `ArrowDown`,
   `Control+c`, `Control+p`. Screenshot after each interaction; add `browser_wait_for {time:0.3}`
   when the frame has not settled.
5. **Wait on state:** `browser_wait_for {text:"Deliberation"}` / `{textGone:"Submitting"}`.
6. **Teardown:** kill ttyd, or send `Control+c` and `browser_close`.

Tips: alt-screen suppresses stderr — check `.orqestra/sessions/<run>/` artifacts and
`~/.claude/projects/` JSONL for model-side errors. The editor flow blanks the browser (alt-screen
suspends) — bypass it with `--auto-approve`. If port 7681 is taken, pick another and adjust the URL.

## 9. LLM pitfalls (TUI) — each is a hard violation

| # | Anti-pattern | Rule |
|---|---|---|
| 1 | IO / `SetContent` / `Set*` / focus or viewport mutation inside `View()` | §5 |
| 2 | blocking on disk / network / subprocess inside `Update()` | §5 |
| 3 | dropping a child's `tea.Cmd` instead of batching it | §5 |
| 4 | a message defined in the consumer instead of the producer frame | §2.4 |
| 5 | a screen holding render state that only a frame renders | §2.1 |
| 6 | a magic `const` for a child dimension instead of `Height()` | §4 |
| 7 | `context.Context` or a logger stored on a Model | §2.5 |
| 8 | a goroutine message carrying a shared pointer / map / buffer | §5 |
| 9 | adding a new `ContentMode` value instead of a sub-model/frame | §7 |
| 10 | double-subtracting the lipgloss border (recalc AND View) | §4 |

## 11. Pre-merge checklist (TUI)

- [ ] `View()` is pure — no IO, no `SetContent`/`Set*`, no focus/viewport mutation.
- [ ] `Update()` does not block; long work is a `tea.Cmd` returning a typed message.
- [ ] Every child `tea.Cmd` is accumulated and returned via `tea.Batch`.
- [ ] `tea.WindowSizeMsg` reaches every child that stores dimensions.
- [ ] Each rendered piece of state lives on the frame/component that renders it.
- [ ] Frame messages are defined in the frame package that emits them.
- [ ] No `context.Context`/logger on a Model; goroutine messages carry copies.
- [ ] Global keys (`ctrl+c`, `esc`, merge, navigation, cancel) are rooted in `Model`.
- [ ] No new `ContentMode` value; touched modes move toward a sub-model/frame.
- [ ] Dimensions clamped before assignment; tiny terminals render without panic.
