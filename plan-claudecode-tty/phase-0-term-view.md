# Work Package: term-view

| Field | Value |
|-------|-------|
| **ID** | `term-view` |
| **Wave** | 4 |
| **depends_on** | `pty-session` |
| **Files** | `internal/tui/view_term.go`, `internal/tui/view_term_test.go`, `internal/tui/messages.go` |

## Goal

Implement the `termView` bubbletea sub-model that maintains a virtual terminal buffer and renders it, replacing `streamView` for PTY-backed agent sessions.

## Steps

1. Add `github.com/charmbracelet/x/vt` to `go.mod` (run `go get github.com/charmbracelet/x/vt`).

2. Add custom messages to `internal/tui/messages.go`:
   - `PTYOutputMsg{TabIndex int, Data []byte}`
   - `PTYNeedsInputMsg{TabIndex int}`
   - `PTYDoneMsg{TabIndex int, Err error, ExitCode int}`

3. Create `internal/tui/view_term.go` with:
   - `termView` struct: `tabIndex int`, `ptySession` (interface with `Write([]byte) (int, error)` and `Resize(int, int) error` — do NOT import concrete PTYSession to avoid circular deps), `vt` (virtual terminal), `needsInput bool`, `done bool`, `err error`, `startedAt time.Time`, `width/height int`, `focused bool`.
   - `newTermView(tabIndex int, cols, rows int) termView` constructor.
   - `Update(msg tea.Msg) (termView, tea.Cmd)` handling:
     - `PTYOutputMsg`: feed `msg.Data` to VT buffer, clear `needsInput`.
     - `PTYNeedsInputMsg`: set `needsInpu Ensure width and height restrict the virtual terminal strictly to `width` horizontally and `height - 1` vertically to make space for the footer status bar without breaking the boundary.
     - Status line below: running (with elapsed time), needs input (⚡), done (✓), failed (✗).
   - `keyToBytes(msg tea.KeyMsg) []byte` helper — converts bubbletea key messages to raw terminal bytes. Must explicitly handle ANSI sequences for Enter, Backspace, arrows (`\x1b[A`), and control keys (Ctrl+C = `\x03`)tySession.Write()`.
     - `tea.WindowSizeMsg`: update dimensions, resize VT, resize PTY session.
   - `View() string` rendering:
     - Render VT screen buffer as string.
     - Status line below: running (with elapsed time), needs input (⚡), done (✓), failed (✗).
   - `keyToBytes(msg tea.KeyMsg) []byte` helper — converts bubbletea key messages to raw terminal bytes.

3. Create `internal/tui/view_term_test.go` with:
   - Feed canned ANSI byte sequences into `termView` via `PTYOutputMsg` → assert `View()` output contains expected text.
   - Terminal bell (`\a`) consumed — not rendered as `^G`.
   - Cursor movement sequences render correctly (e.g., `\x1b[2;5H` positions text).
   - Resize updates VT buffer dimensions (assert View width changes).
   - Keystroke forwarding: mock PTYSession, send `tea.KeyMsg`, assert `Write()` called with correct bytes.
   - Status line rendering per state: running shows elapsed, needsInput shows ⚡, done shows ✓, error shows ✗.

## Acceptance

- `go test ./internal/tui/ -run TestTermView` passes.
- `go vet ./internal/tui/` clean.
- `termView` does NOT import `internal/harness` directly — uses an interface for the PTY write/resize contract.
- Dependency added: `github.com/charmbracelet/x/vt` in `go.mod` (no fallback logic).
- Files touched: ONLY `internal/tui/view_term.go`, `internal/tui/view_term_test.go`, `internal/tui/messages.go`, `go.mod`, `go.sum`.
