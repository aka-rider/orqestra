# Work Package: term-view

| Field | Value |
|-------|-------|
| **ID** | `term-view` |
| **Wave** | 4 |
| **depends_on** | `pty-session` |
| **Files** | `internal/tui/view_term.go`, `internal/tui/view_term_test.go` |

## Goal

Implement the `termView` bubbletea sub-model that maintains a virtual terminal buffer and renders it, replacing `streamView` for PTY-backed agent sessions.

## Steps

1. Add `github.com/charmbracelet/x/vt` to `go.mod` (run `go get github.com/charmbracelet/x/vt`). If this package is too experimental or unavailable, use `github.com/hinshun/vt10x` instead.

2. Create `internal/tui/view_term.go` with:
   - `termView` struct: `tabIndex int`, `ptySession` (interface with `Write([]byte) (int, error)` and `Resize(uint16, uint16) error` — do NOT import concrete PTYSession to avoid circular deps), `vt` (virtual terminal), `needsInput bool`, `done bool`, `err error`, `startedAt time.Time`, `width/height int`, `focused bool`.
   - `newTermView(tabIndex int, cols, rows int) termView` constructor.
   - `Update(msg tea.Msg) (termView, tea.Cmd)` handling:
     - `PTYOutputMsg`: feed `msg.Data` to VT buffer, clear `needsInput`.
     - `PTYNeedsInputMsg`: set `needsInput = true`.
     - `PTYDoneMsg`: set `done = true`, store error.
     - `tea.KeyMsg`: if focused and session running, forward keystroke bytes via `keyToBytes(msg)` → `ptySession.Write()`.
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
- Dependency added: `github.com/charmbracelet/x/vt` (or `github.com/hinshun/vt10x`) in `go.mod`.
- Files touched: ONLY `internal/tui/view_term.go`, `internal/tui/view_term_test.go`, `go.mod`, `go.sum`.
