# Orqestra — internal/tui/ (Bubble Tea MVU)

Extends the root `CLAUDE.md` (Prime Directive and Go fundamentals bind here) and adapts rune's
TUI standard (`pkg/ui/CLAUDE.md` in the local rune checkout wired via `replace` in go.mod);
orqestra reuses rune's `keymap`/`styles`/`scroll`. Terms are defined in `CONTEXT.md`; decisions
in `docs/adr/`.

**Load-bearing rule restated** (this file is not re-injected after a context compaction): TUI
errors are user-visible truth — store them in model state until rendered; alt-screen suppresses
stderr, so nothing else surfaces them. `View()` is pure.

## 1. Vocabulary (use these terms; see CONTEXT.md)

- **Timeline** — the single chronological scrollback: a vertical stack of full-width **Frames**.
- **Frame** — one full-width unit (Prose / Tool / Plan / Phase separator / Steer message). The
  unit of copy-to-clipboard.
- **Live Frame** — the most-recent Frame while its event is still unfolding (at most one at a
  time). **Static Frame** — a concluded Frame: frozen, rendered once, never changes again.
- "**event frame**" always means one Claude CLI stream-json (NDJSON) line — never an unqualified
  "frame".

## 2. Component architecture

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
polymorphism (open/closed over Frame kinds). Everywhere else prefer concrete component types.

- `frame.StaticFrame` — `Rows() []Row`, `SetWidth(w) StaticFrame`: a frozen render.
- `frame.InteractiveFrame` — adds `Update(tea.Msg)` and `Resolve() StaticFrame`; `Resolve()`
  freezes a Live Frame into a Static one.
- Use value receivers on frames by default; reach for pointer receivers only when `Update` must
  mutate accumulated state shared across copies (as `TurnGroup` does).

### 2.3 Screens compose; frames render

Screen sub-models (Prompt, Pipeline, RunsList, RunDetail, Setup) own layout, focus, and message
routing. The root `Model` owns global keys, screen routing, and `recalculateLayout()`. A frame
never reads sibling-frame state, decision channels, or contexts.

### 2.4 A message is defined in its PRODUCER's package

The frame that emits an event defines the message. Intents flow up to the root via the `intent`
marker interface; orchestration events flow down through typed messages.

```go
// RIGHT — defined beside the live-prose frame that emits them
type BlinkMsg struct{}
type DeltaMsg struct{ Text string }
```

### 2.5 Dependency injection

Build keymap/styles ONCE from rune at startup and pass them down by value. Keep rune types
confined to the rune-boundary files (`runeui.go`, `promptinput.go`, `screen_prompt.go`). A Model
carries no `context.Context` and no logger.

## 3. Keybindings

Global keys — `ctrl+c`, app-level `esc`, merge decisions, run navigation, pipeline cancellation —
are rooted in `Model`. A screen forwards scoped keys to its focused child. rune's
`keymap.Bindings` is the single source of truth; one physical key string appears in exactly one
binding.

## 4. Layout & dimensions

- Compute every dimension in `recalculateLayout()` (or a screen update method) on
  `tea.WindowSizeMsg` and on structural change; render paths only read stored dimensions.
- Query a child's intrinsic size (`Height()`) instead of hardcoding a magic `const`.
- lipgloss v2 is **border-box**: `Width(n)`/`Height(n)` include borders. Subtract the frame ONCE,
  when sizing the child; the border style in `View()` gets the OUTER dimension.
- Clamp dimensions before assigning them to Bubble components; a tiny terminal renders the
  small-screen message without panicking.

## 5. The Elm cycle

- **Pure View.** `View()` renders only: no IO, no channel reads, no focus/viewport mutation, no
  `SetContent`/`SetWidth`/`SetHeight`/`GotoTop`/`GotoBottom`.
- **Non-blocking Update.** `Update()` may mutate model state and return commands, but never
  blocks on disk, network, timers, subprocesses, or long parsing — push that into a `tea.Cmd`
  that returns a typed completion message.
- **Capture before closure.** Copy model-derived values into locals BEFORE a `tea.Cmd` closure;
  it runs later.
- **Batch every child Cmd.** After each `child.Update(msg)`, accumulate the returned `tea.Cmd`
  and return `tea.Batch(cmds...)`; `tea.WindowSizeMsg` reaches every child that stores
  dimensions.
- **Preserve scroll.** `viewport.SetContent()` is scroll-destructive: capture
  `AtBottom()`/offset before, restore after — in `Update`, never `View`.
- **Goroutine messages carry copies or immutable values** — never shared pointers, maps, or
  buffers.

## 6. Async command boundaries

File reads (run lists, run details, session logs, edited plans) and editor launches are `tea.Cmd`
functions returning typed messages; failures update visible model error state unless explicitly
best-effort. Long-running orchestration stays outside the TUI — the model consumes orchestrator
events through channels/commands and never mutates orchestrator state directly. Timer ticks may
refresh elapsed time and live stream output, but never reset user scroll or rebuild expensive
content unless the underlying data changed.

## 7. ContentMode migration (standing rule)

The root `Model` still routes some content through a flat `ContentMode` enum; the direction is
one sub-model or Frame per mode. When you touch a `ContentMode` arm, migrate that mode toward a
sub-model/frame; give a new UI state a sub-model or frame, not a new `ContentMode` value.

```go
// WRONG — one container switching on a kind-enum, holding every kind's fields
type Timeline struct { kind FrameKind; prose string; tool ToolRows; plan string }
// RIGHT — each kind is its own frame type behind StaticFrame / InteractiveFrame
frames []frame.StaticFrame   // + at most one live frame.InteractiveFrame
```

## 8. Debugging the TUI with ttyd + Playwright

To visually observe or drive the TUI (verify a layout fix, test a key-binding, reproduce a render
bug), expose the terminal with `ttyd` and drive it with the Playwright MCP `browser_*` tools. The
Playwright browser runs inside a container: reach the host via `host.docker.internal`, and read
text out of the terminal instead of screenshotting (screenshots are written inside the container,
unreadable from the host).

1. **Build & serve:** `make build`, then `ttyd -W -p 7681 ./orqestra --config orqestra.yaml` in
   the background. `-W` is required — ttyd is read-only by default and silently drops keystrokes.
2. **Open & size first:** `browser_navigate {url:"http://host.docker.internal:7681"}`, then
   `browser_resize {width:1400, height:900}` (≈ 240×50 cols/rows). Size before observing —
   layout recalculates on every `tea.WindowSizeMsg`; a narrow viewport triggers the small-screen
   fallback.
3. **Observe by reading the xterm buffer** with `browser_evaluate`: find the xterm.js instance
   (the global with `.buffer.active` and a numeric `.cols`), then collect
   `buf.getLine(i).translateToString(true)` per line — verifiable text you can assert on.
4. **Type** by filling `textarea.xterm-helper-textarea`; send single keys with
   `browser_press_key` (`Enter`, `Escape`, `ArrowDown`, `Control+c`).
5. **Wait on state:** `browser_wait_for {text:"Deliberation"}` / `{textGone:"Submitting"}`.
6. **Teardown:** kill ttyd and `browser_close`.

Tips: alt-screen suppresses stderr — check `.orqestra/sessions/<run>/` artifacts and
`~/.claude/projects/` JSONL for model-side errors. The editor flow suspends into a nested
alt-screen and blanks the browser — bypass it with `--auto-approve`. If port 7681 is taken, pick
another and adjust the URL.

## 9. Pre-merge checklist (TUI)

- [ ] `View()` is pure — no IO, no `SetContent`/`Set*`, no focus/viewport mutation.
- [ ] `Update()` does not block; long work is a `tea.Cmd` returning a typed message.
- [ ] Every child `tea.Cmd` is accumulated and returned via `tea.Batch`.
- [ ] `tea.WindowSizeMsg` reaches every child that stores dimensions.
- [ ] Each rendered piece of state lives on the frame/component that renders it.
- [ ] Frame messages are defined in the frame package that emits them.
- [ ] No `context.Context`/logger on a Model; goroutine messages carry copies.
- [ ] Global keys rooted in `Model`; each physical key lives in exactly one binding.
- [ ] Touched `ContentMode` arms moved toward a sub-model/frame; no new enum values.
- [ ] Dimensions clamped before assignment; the lipgloss border subtracted once.
