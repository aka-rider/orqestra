# Plan: Rearchitect Main Loop — Raw Passthrough + Chrome-on-Demand

**TL;DR**: The active Claude Code session owns the terminal completely — raw stdin→PTY, raw PTY→stdout, zero emulation in the hot path. `Ctrl+B` suspends the passthrough and enters a rich BubbleTea chrome overlay (tabs, logs, pipeline status). Picking a tab exits chrome and returns to raw passthrough on the new active child. ~400 lines of core mux code. Perfect terminal fidelity. Ships fast.

---

## Why Not Reimplement tmux

The original plan used `vt.SafeEmulator.Render()` in the hot path — effectively reimplementing terminal emulation. This is a rabbit hole:

- `charmbracelet/x/vt` is test-grade, not production-grade
- Claude Code (React/Ink) does full-screen repaints; any emulator bug = visual corruption
- Synchronized output protocol, scrolling regions, DA1 responses, bracketed paste — all fragile
- No production-quality Go VT emulator library exists

**The insight**: we don't need to emulate anything. The child process IS a terminal application. Let it talk directly to the real terminal. We only need to *interrupt* that conversation momentarily for chrome.

---

## Current Problems Identified

| Problem                                    | Location                                                                    | Impact                                                                                             |
| ------------------------------------------ | --------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `keyToBytes()` lossy re-encoding           | `internal/tui/view_term.go:143-176`                                         | Breaks unicode IME, vim bindings, paste, bracketed paste, mouse, F13+, Kitty protocol              |
| `go m.startAgent(...)` on value-type Model | `internal/tui/model.go:333`                                                 | Captures model fields by value-copy into a goroutine — accidentally safe but violates Elm contract |
| Double exit signaling                      | `internal/agent/seatbelt_runner.go:72-82` + `internal/tui/model.go:350-361` | `Wait()` fires `OnDone`, then goroutine sends `AgentCompleteMsg` — two messages for one event      |
| Shared `*termView` via pointer return      | `internal/tui/view_tabs.go:54-58`                                           | Mutable pointer returned from `TermTab()` mutated in `attachPTYMsg` handler                        |
| Pipeline logic in TUI model                | `internal/tui/model.go:388-430`                                             | State machine coupled to rendering — untestable in isolation                                       |
| BubbleTea owns stdin                       | `internal/tui/tui.go:17-22`                                                 | Framework fundamentally intercepts all input, no passthrough mode exists                           |

---

## Proposed Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  TERMINAL MODE (default, 99% of time)                        │
│                                                              │
│  /dev/tty stdin ──[raw bytes]──► active child PTY master     │
│  active child PTY master ──[raw bytes]──► /dev/tty stdout    │
│                              └──[tee]──► BEL scanner         │
│                                                              │
│  Child OWNS the full terminal. Perfect fidelity.             │
│  No parsing. No emulation. No rendering. Just pipe.          │
│                                                              │
│  Ctrl+B detected in input stream → suspend passthrough       │
└─────────────────────────────┬───────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│  CHROME MODE (Ctrl+B, momentary overlay)                     │
│                                                              │
│  BubbleTea takes over with alt-screen:                       │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │  ╭─ Orqestra ──────────────────────────────────────────────────╮ │  │
│  │  │                                                             │ │  │
│  │  │  Pipeline: [✓] Intake ─▶ [✓] Plan ─▶ [●] Workers            │ │  │
│  │  │  Goal:     "Implement tmux-like passthrough mux"            │ │  │
│  │  │                                                             │ │  │
│  │  ├─ Sessions (Tabs) ───────────────────────────────────────────┤ │  │
│  │  │  [1] Intake      ✓ completed  (2m 31s)                      │ │  │
│  │  │  [2] Planner     ✓ completed  (0m 45s)                      │ │  │
│  │  │  [3] Worker #1   ● active     (1m 12s)  ◄── (active tab)    │ │  │
│  │  │  [4] Worker #2     waiting                                  │ │  │
│  │  │                                                             │ │  │
│  │  ├─ Workspace & Progress───────────────────────────────────────┤ │  │
│  │  │  [File Changes]                 [Work Packages]             │ │  │
│  │  │  M internal/mux/mux.go          ✓ 1. Core Passthrough       │ │  │
│  │  │  A internal/mux/tab.go          ● 2. Chrome UI              │ │  │
│  │  │  D internal/tui/model.go        · 3. Orchestrator           │ │  │
│  │  │                                                             │ │  │
│  │  ├─ Recent Logs ───────────────────────────────────────────────┤ │  │
│  │  │  14:31:02 [Worker #1] Started package "Core Passthrough"    │ │  │
│  │  │  14:32:15 [Worker #1] ⚠ BEL detected (needs input)          │ │  │
│  │  ╰─────────────────────────────────────────────────────────────╯ │  │
│  │                                                                  │  │
│  │    1-9: Switch Tab │ Enter: Resume │ l: Toggle Logs │ q: Quit    │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                                              │
│  On exit chrome:                                             │
│    1. BubbleTea exits (releases alt-screen)                  │
│    2. Send SIGWINCH to new active child (triggers repaint)   │
│    3. Resume raw passthrough on the new active tab           │
└─────────────────────────────────────────────────────────────┘

┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│ Tab 0 (PTY)  │  │ Tab 1 (PTY)  │  │ Tab 2 (PTY)  │
│ BEL scanner  │  │ BEL scanner  │  │ BEL scanner  │
│ read goroutine│ │ read goroutine│ │ read goroutine│
│ claude code  │  │ claude code  │  │ claude code  │
└──────────────┘  └──────────────┘  └──────────────┘

Orchestrator (separate goroutine, channels only)
  → pipeline state: intake → planner → validator → worker
  → sends: AddTab, receives: TabExited
```

---

## Key Design Principles

1. **Active tab = dumb pipe.** `io.Copy` in both directions. The only byte we intercept is the configured prefix key (default `Ctrl+B` / `0x02`) in the stdin→PTY path. Configurable via `orqestra.yaml`:

   ```yaml
   tui:
     prefix_key: ctrl+b  # ctrl+b | ctrl+] | ctrl+\ | ctrl+space
   ```

2. **Chrome is a full BubbleTea app** that launches on demand (like `git commit` launching `$EDITOR`). It runs with alt-screen, shows the rich UI, then exits. We KEEP BubbleTea — but only for chrome, never for the terminal passthrough.
3. **Background tabs are continuously drained.** The read goroutine continuously reads the PTY, scans for BEL, and discards the bytes if inactive. This prevents background agents from hanging on full PTY buffers. When we switch to a tab, the `SIGWINCH` signal forces the React/Ink app to repaint its entire UI natively.
4. **BEL detection** is done by the read goroutine tee-ing output through `scanForBEL()` — just byte matching, not emulation.
5. **No `vt.SafeEmulator` in the critical path.** It's only used optionally in chrome mode if we want to show a preview of inactive tabs (future, not MVP).

---

## Steps

### Phase 1: Core Passthrough Mux (~400 lines)

**Goal:** Build the raw pipe.

1. **Create `internal/mux/mux.go`** — `Mux` struct: owns tty fd (via `os.File` for `/dev/tty`), slice of `*Tab`, active index, `inputMode` (TERMINAL/CHROME). `Run(ctx) error` is the blocking main loop.

2. **Create `internal/mux/tab.go`** — `Tab` struct: name, `*NativePTY`, done channel, attention bool, startedAt, exitCode. `readLoop()` goroutine: reads PTY output, writes to stdout when active (gated by `mux.active == tab.index`), tees through BEL scanner, discards when inactive (or buffers in kernel PTY buffer).

3. **Create `internal/mux/passthrough.go`** — The raw I/O loop:

   ```go
   func (m *Mux) passthrough(ctx context.Context) error {
       // Put tty in raw mode
       oldState, _ := term.MakeRaw(m.tty.Fd())
       defer term.Restore(m.tty.Fd(), oldState)

       buf := make([]byte, 4096)
       for {
           n, err := m.tty.Read(buf)
           if err != nil { return err }

           // Intercept Ctrl+B
           if n == 1 && buf[0] == m.prefixKey {
               m.enterChrome()
               continue
           }

           // Forward raw bytes to active PTY
           m.tabs[m.active].pty.Write(buf[:n])
       }
   }
   ```

4. **Create `internal/mux/chrome.go`** — Launches BubbleTea with alt-screen for the overlay UI. Simply passes the existing un-rawed `tty` directly to `tea.NewProgram(..., tea.WithInput(tty), tea.WithOutput(tty))` without complex fd gymnastics. Returns the user's choice (switch tab, quit, or resume). On return, sends `SIGWINCH` to new active child.

- **Acceptance Criteria**: Mux can spawn a PTY, pass `Ctrl+C` natively, and intercept `Ctrl+B` (or configured prefix) to pause/resume local I/O. Background PTYs reading loops correctly discard bytes when inactive to prevent blocking.
- **Quality Gate**: Unit tests pass using simulated FDs. Manual run proves `vim` and multi-line bracketed paste work natively inside the active PTY.

### Phase 2: Chrome UI (BubbleTea, reuse existing styling patterns)

**Goal:** Build the momentary overlay snapshot.
**UX Concept**: The user starts in the Intake terminal. `Ctrl+B` acts as stepping "out of the tunnel" to see the over-arching orchestration state.

1. **Create `internal/chrome/model.go`** — BubbleTea model for the chrome overlay. Shows:
   - **Progress Header:** Visual pipeline progression (`Intake -> Plan -> Workers`) and the top-level user goal.
   - **Sessions/Tabs:** Active and completed agents, with execution times and attention (`⚠`) markers.
   - **Workspace Context:** Side-by-side view tracking actively changed files (via `git status` / `stat`) and progression through defined Work Packages.
   - **Logs:** Scrollable recent system logs (toggleable). *(Note: These logs are Orchestrator-level telemetry/events, not the raw PTY stdout bytes which are discarded when tabs are inactive).*
   - **Keybindings:** `1-9` switch + exit, `Enter` resume, `q` quit, `j/k` navigate, `l` toggle logs section. (Displayed in a clear bottom bar legend).

2. **Create `internal/chrome/messages.go`** — Minimal message types: `Snapshot`, `TabInfo`, `LogEntry`, `WorkspaceState`. These are passed in as initial state (snapshot), not streamed — chrome is a momentary snapshot view.

- **Acceptance Criteria**: BubbleTea UI renders the macro pipeline state correctly. Navigation keys switch internal UI focus. Quitting/Exiting cleanly restores terminal logic to Mux.
- **Quality Gate**: `teatest` covers the visual rendering of the `Snapshot`. No terminal artifacting upon exit.

### Phase 3: Orchestrator (pipeline state machine)

**Goal:** Decouple pipeline logic from rendering.

1. **Create `internal/orchestrator/orchestrator.go`** — Pipeline state machine extracted from current `model.go`. Interface:

   ```go
   type Orchestrator struct { ... }

   // Commands the orchestrator sends to the mux
   type Cmd interface{ cmd() }
   type AddTabCmd struct { Name string; Spec agent.AgentSpec }
   type ShutdownCmd struct {}

   // Events the mux sends to the orchestrator
   type Event interface{ event() }
   type TabExitedEvent struct { Index int; ExitCode int }
   ```

2. **Simplify `agent.RunCallbacks`** — Remove `OnOutput` and `OnBEL` (mux owns PTY reading). Keep only `OnState` for orchestrator telemetry. The `SeatbeltRunner.RunInteractive()` return type becomes just the `*NativePTY` + done channel — no callback wiring needed.

- **Acceptance Criteria**: Orchestrator tracks Phase status, captures telemetry events for the Chrome logs, and triggers advancing to the next agent on Exit Events.
- **Quality Gate**: Headless unit tests successfully transition state from `Intake -> Plan -> Worker` using mock agent exit events alone.

### Phase 4: Integration

**Goal:** Wire it all together and handle real system signals.

1. **Rewrite `cmd/orqestra/main.go` `runTUI()`** — Wire:

   ```go
   func runTUI(ctx context.Context, cfg *config.Config, ...) {
       mux := mux.New()
       orch := orchestrator.New(cfg, seatbeltRunner, mux)
       go orch.Run(ctx)  // manages pipeline, adds tabs to mux

       if err := mux.Run(ctx); err != nil {
           // terminal already restored by mux
           slog.Error("mux error", "err", err)
       }
   }
   ```

2. **Signal handling in `Mux.Run()`**:
    - `SIGWINCH` → resize active child PTY (`pty.Resize(cols, rows)`)
    - `SIGINT` while in TERMINAL mode → forward `0x03` to child (already happens naturally since tty is raw)
    - `SIGINT` while in CHROME mode → quit

3. **Graceful shutdown** — On quit: `SIGTERM` all child PTYs → 3s timeout → `SIGKILL` → restore terminal state → exit.

- **Acceptance Criteria**: E2E pipeline manages raw input correctly. `SIGWINCH` resizes the PTY. `Ctrl+C` terminates gracefully tracking cleanup.
- **Quality Gate**: Successfully run `orqestra` full workflow via manual E2E test. No hanging processes on SIGKILL.

### Phase 5: Cleanup

**Goal:** Leave the codebase cleaner than we found it.

1. **Delete `internal/tui/`** entirely.
2. **Update `go.mod`** — keep `bubbletea` (used by chrome overlay), keep `lipgloss`, keep `bubbles` (spinner in chrome). Drop `x/vt` from critical path (optional for future tab preview).
3. **Port tests** — Mux: test with mock FDs. Orchestrator: test state machine with mock tab events. Chrome: standard BubbleTea `teatest` for the overlay UI.

- **Acceptance Criteria**: `go.mod` updated, dead code `internal/tui/*` deleted.
- **Quality Gate**: Code compiles warning-free. `go test -race ./...` passes. `goleak` asserts 0 lingering goroutines.

---

## Relevant Files

| File | Action |
|------|--------|
| `internal/tui/*` | **DELETE** — replaced by `mux/` + `chrome/` + `orchestrator/` |
| `internal/mux/mux.go` | **NEW** — core passthrough multiplexer |
| `internal/mux/tab.go` | **NEW** — tab lifecycle, PTY ownership |
| `internal/mux/passthrough.go` | **NEW** — raw I/O loop |
| `internal/mux/chrome.go` | **NEW** — chrome entry/exit bridge |
| `internal/chrome/model.go` | **NEW** — BubbleTea overlay UI |
| `internal/chrome/messages.go` | **NEW** — chrome message types |
| `internal/orchestrator/orchestrator.go` | **NEW** — pipeline state machine |
| `cmd/orqestra/main.go` | **REWRITE** `runTUI()` and `runTUIFromSpec()` |
| `internal/agent/seatbelt_runner.go` | **SIMPLIFY** — remove callback wiring, return PTY directly |
| `internal/agent/runner.go` | **SIMPLIFY** — strip OnOutput/OnBEL from RunCallbacks |
| `internal/agent/pty_native.go` | **KEEP** — used by mux for PTY lifecycle |
| `go.mod` | **KEEP** bubbletea (for chrome), drop `x/vt` from required |

---

## Data Flow Detail

### TERMINAL mode (hot path)

```
stdin read loop:              PTY read loop (per tab):
┌─────────┐                  ┌──────────────┐
│ tty.Read │                  │ pty.Read     │
└────┬─────┘                  └──────┬───────┘
     │ buf[:n]                       │ buf[:n]
     ▼                               ▼
┌─────────────┐              ┌──────────────────┐
│ buf[0]==0x02?│              │ tab == active?    │
│ (Ctrl+B)    │              │                   │
└──┬───────┬──┘              └──┬────────────┬──┘
   │yes    │no                  │yes         │no
   ▼       ▼                    ▼            ▼
enterChrome  pty.Write      tty.Write     scanForBEL
             (to child)     (to screen)   (set attention)
```

### Chrome mode (overlay)

```
1. Pause stdin read loop (it blocks waiting for chrome to finish)
2. Pause PTY→stdout writes (background tabs drain and discard)
3. Snapshot: collect tab states, pipeline phase, recent logs
4. Launch BubbleTea chrome overlay (alt-screen)
5. User interacts: switch tab, view logs, quit
6. BubbleTea exits (restores main screen)
7. If tab switched: update mux.active, send SIGWINCH to new child
8. Resume stdin read loop + PTY→stdout writes
9. New active child repaints (React/Ink always repaints on SIGWINCH)
```

---

## Edge Cases & Solutions

| Edge Case | Solution |
|-----------|----------|
| Child exits while in TERMINAL mode | Read loop gets EOF, `TabExitedEvent` fires. Orchestrator handles event, automatically advances pipeline or auto-enters chrome if pipeline is paused. |
| Child exits while in CHROME mode | `TabExitedEvent` fires. BubbleTea UI updates the snapshot dynamically or upon the next action rendering the completed marker. |
| Prefix key conflict with child app | Configurable via `tui.prefix_key`. Default `ctrl+b` rarely needed in Claude Code (not a shell). Double-press sends the literal byte to child. Users with readline muscle memory can switch to `ctrl+]` or `ctrl+space`. |
| Paste containing `0x02` byte | Bracketed paste: simple substring check (`bytes.Contains(buf[:n], []byte("\x1b[200~"))`) in the input loop is sufficient for local chunked reads. No complex state machine needed. |
| SIGWINCH during chrome | Forwarded to BubbleTea (it handles resize). On chrome exit, re-send to child. |
| Multiple rapid Ctrl+B | Debounce: ignore if chrome was exited <100ms ago. |
| Background tab output | Background tabs are drained and discarded continuously so the process never blocks. The UI state is restored via `SIGWINCH` upon switching. |

---

## Verification

1. `go build ./cmd/orqestra` — compiles clean
2. Manual: type unicode (日本語), vim `dd`/`yy`, paste multi-line — all work natively (raw passthrough, zero interpretation)
3. Manual: `Ctrl+B` → chrome overlay appears with tabs + logs + pipeline status
4. Manual: press `2` in chrome → switches to tab 2 → Claude Code repaints cleanly
5. Manual: BEL in background tab → chrome shows `⚠` marker on that tab
6. Manual: resize terminal → active child adapts immediately (SIGWINCH)
7. Manual: `Ctrl+B` then `q` → all children killed, terminal restored cleanly
8. Manual: paste text containing `0x02` byte → arrives correctly in child (bracketed paste detection)
9. `go test -race ./internal/mux/... ./internal/orchestrator/... ./internal/chrome/...`
10. No goroutine leaks verified via `goleak`

---

## Decisions

- **Keep BubbleTea** — but ONLY for the chrome overlay. It's perfect for that: rich interactive UI, alt-screen, exits cleanly. We just don't let it own the terminal full-time.
- **Configurable prefix key** — default `Ctrl+B` (tmux convention). Alternatives: `Ctrl+]` (no readline conflict), `Ctrl+\` (SIGQUIT, we intercept), `Ctrl+Space` (rare in TUIs). Double-press sends literal to child. Set via `tui.prefix_key` in `orqestra.yaml`.
- **No VT emulation in the hot path** — active tab is raw passthrough. No `vt.SafeEmulator`. No `keyToBytes()`. No rendering. Just pipe.
- **BEL detection via byte scan** — `scanForBEL()` already exists and works. Tee output through it in the read goroutine, no emulator needed.
- **Pipeline logic decoupled from display** — orchestrator is independently testable, communicates via channels.
- **Chrome is a snapshot** — when you enter chrome, it shows current state. It doesn't live-update (no streaming). This keeps it simple and avoids concurrency issues with BubbleTea.

---

## Complexity Estimate

| Component | Lines (est.) | Risk |
|-----------|-------------|------|
| `internal/mux/` (passthrough + tabs) | ~300 | Low — just fd I/O, well-understood |
| `internal/chrome/` (BubbleTea overlay) | ~250 | Low — standard BubbleTea, no terminal emulation |
| `internal/orchestrator/` | ~200 | Low — extract existing state machine |
| `cmd/orqestra/main.go` changes | ~50 | Low — simplification |
| Bracketed paste detection in input loop | ~5 | Low — simple substring check |
| Signal handling + graceful shutdown | ~50 | Low — standard Go patterns |
| **Total new code** | **~855** | |
| **Deleted code** (`internal/tui/`) | **~900** | |

Net change: roughly zero lines. Massive reduction in complexity.
