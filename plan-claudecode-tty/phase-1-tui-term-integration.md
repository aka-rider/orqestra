# Work Package: tui-term-integration

| Field | Value |
|-------|-------|
| **ID** | `tui-term-integration` |
| **Wave** | 6 |
| **depends_on** | `term-view`, `pty-runner-lifecycle`, `input-detection` |
| **Files** | `internal/tui/view_tabs.go`, `internal/tui/view_term.go` (minor), `internal/tui/messages.go`, `internal/tui/model.go`, `internal/tui/focus_test.go` |

## Goal

Wire `termView` into the TUI tab system, implement tab header states, quick-switch (`ctrl+space`), focus routing for PTY input, and the `ctrl+c` double-press kill behavior.

## Steps

### 1.6 — TUI Tab Rendering

1. Modify `internal/tui/view_tabs.go` & `model.go`:
   - Replace text-in-tab rendering with `termView` for PTY-backed tabs. The parent model/tab manager explicitly manages a map or slice (e.g. `map[int]termView`) to store independent states and forward messages correctly.
   - Tab header states based on agent lifecycle:

     | State | Header Display |
     |-------|---------------|
     | Provisioning | `◌ <name>` (dimmed) |
     | Running (no input needed) | `✦ <name>` (existing pulse) |
     | Needs input | `⚡ <name>` (rapid blink, yellow/orange) |
     | Done | `✓ <name>` (green) |
     | Failed | `✗ <name>` (red) |

   - ANSI escape sequences from PTY render correctly (Claude Code uses colors, spinners).
   - Terminal bell (`\a`) triggers tab notification, not `^G` in output. The VT parser callback emits a `PTYBellMsg` via async `p.Send()` to trigger visual status updates.
   - Window resize propagates: `WindowSizeMsg` updates VT buffer dimensions instantly and triggers an asynchronous `tea.Cmd` to call `PTYSession.Resize()` avoiding blocking the UI thread.

### 1.7 — Input Detection + Quick-Switch

1. Add to `internal/tui/model.go` (or appropriate sub-model):
   - On receiving `PTYNeedsInputMsg`: set tab header to `⚡`, update status bar.
   - `ctrl+space` keybinding: jump to the most recently signaled "needs input" tab. If no tab needs input, cycle to next running tab.
   - Status bar message: `⚡ Tab N needs input — Ctrl+Space to switch`.

### 1.8 — Focus Routing

1. Modify `internal/tui/model.go` Update routing:
   - When `FocusTabs` is active: all `tea.KeyMsg` except global hotkeys forward to the focused `termView`.
   - Global hotkeys (never forwarded to PTY): `ctrl+c`, `alt+1..9`, `ctrl+space`, `esc`.
   - `esc` releases focus back to `FocusPrompt` (command bar).
   - `Enter` or clicking tab area grants `FocusTabs`.
   - `ctrl+c` within focused tab:
     - First press: transition state to warn and display warning "Press Ctrl+C again to force kill the session".
     - Second press (within 2s): emit a `tea.Cmd` (e.g. `StopPTYCmd()`) to asynchronously send the kill signal to the Docker API and terminate the PTY container without blocking `Update()`. Do NOT proxy `ctrl+c` to Claude Code.

2. Add new messages to `internal/tui/messages.go`:
   - `PTYOutputMsg{TabIndex int, Data []byte}` (if not already present).
   - `PTYNeedsInputMsg{TabIndex int}`.
   - `PTYDoneMsg{TabIndex int, Err error, ExitCode int}`.
   - `PTYBellMsg{TabIndex int}`.

3. Update `internal/tui/focus_test.go`:
   - Assert key routing per focus state: FocusTabs forwards to termView, FocusPrompt does not.
   - Assert global hotkeys are never forwarded regardless of focus.
   - Assert `esc` transitions focus from tabs to prompt.
   - Assert `ctrl+c` double-press behavior (first warns, second kills).

## Acceptance

- `go test ./internal/tui/ -run TestFocus` passes.
- `go test ./internal/tui/ -run TestTermView` still passes.
- `go vet ./internal/tui/` clean.
- Tab headers render the correct state symbol for each lifecycle phase.
- `ctrl+space` switches to the needs-input tab (verified in test).
- `ctrl+c` double-press kills the session (verified in test).
- Files touched: ONLY `internal/tui/view_tabs.go`, `internal/tui/view_term.go`, `internal/tui/messages.go`, `internal/tui/model.go`, `internal/tui/focus_test.go`.
