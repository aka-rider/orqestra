# Plan: Upgrade Bubbletea to v2 for Shift+Enter Support

Bubbletea v1.3.10 cannot detect Shift+Enter — terminals send the same byte for both Enter and Shift+Enter, and v1 only tracks `Alt` as a modifier. The fix is upgrading to bubbletea v2.0.6 (stable since Feb 2026), which adds a `Mod` bitmask via the Kitty keyboard protocol. Then we intercept `shift+enter` in the prompt handler to insert a newline instead of submitting.

## Breaking Change Summary

| Category                                                          | Sites     | Effort                       |
| ----------------------------------------------------------------- | --------- | ---------------------------- |
| Import paths (`github.com/charmbracelet/` → `charm.land/`)        | 13 files  | Mechanical                   |
| Key handling (`KeyMsg`→`KeyPressMsg`, `.Type`→`.Code`, constants) | ~35       | Moderate — per-switch review |
| `View() string` → `View() tea.View`                               | 1 + tests | Low                          |
| Viewport constructor & field access → methods                     | ~30       | Mechanical                   |
| Mouse handling (split types)                                      | ~4        | Low                          |
| Program options → View fields                                     | 3         | Low                          |
| Textarea API tweaks                                               | ~5        | Low                          |
| Glamour v1 → v2 (lipgloss v2 compat)                              | 1 file    | Low                          |

**Total: ~95 touch points, all mechanical or low-judgment.**

## Top 5 Risks

### Risk 1 — `View()` mutates viewports (design fix, not mechanical rename)

`viewPipelineScreen()` (model.go L928–955) calls `.SetContent()`, `.GotoBottom()`, `.SetWidth()`, `.SetHeight()` on viewports inside the View chain. In v1 this was wrong but tolerated. In v2, `View()` returns a `tea.View` value — the runtime may call it more than once per frame or cache it. These mutations will cause non-deterministic scroll positions and double-render glitches.

**Fix**: Hoist all viewport `.SetContent()`, `.GotoBottom()`, `.SetWidth()`, `.SetHeight()` calls from `viewPipelineScreen()` into `Update()` — triggered on tick, event arrival, and window resize.

### Risk 2 — `msg.String()` semantics change breaks rune/char routing

The code uses `msg.String()` for character matching (`"@"`, `"d"`, `"1"`–`"9"`, `"y"`, `"a"`, etc.). In v2, `String()` includes modifier prefixes and renames `" "` → `"space"`. `tea.KeyRunes` no longer exists — the file picker (filepicker.go L428) checks `msg.Type == tea.KeyRunes`, which won't compile. Every `msg.String()` comparison and `KeyRunes` check must be audited:

- `"@"` detection after prompt update (model.go L409)
- Number-key agent navigation `"1"`–`"9"` (model.go L456)
- Single-char commands `"d"`, `"s"`, `"n"`, `"y"`, `"a"`, `"e"`, `"q"` in 6 handlers

**Fix**: Replace `msg.Type == tea.KeyRunes` with `len(msg.Text) > 0`. Audit every `msg.String()` comparison — v2 rune keys still return the character for `String()`, but verify no modifier prefix leaks into comparisons.

### Risk 3 — `glamour` v1 is incompatible with `lipgloss` v2

markdown.go imports `glamour v1.0.0`. Glamour v1 depends on lipgloss v1. After upgrading lipgloss to `charm.land/lipgloss/v2`, two lipgloss versions coexist in the module graph. If glamour's internal rendering conflicts with v2's terminal state, markdown output will be garbled.

**Fix**: Add `charm.land/glamour/v2` to the dependency swap phase. Verify `NewTermRenderer`, `WithAutoStyle`, `WithWordWrap`, `WithEmoji` API is preserved.

### Risk 4 — Test helpers construct v1 `tea.KeyMsg` structs (entire test suite blocked)

`sendKey` and `sendRune` in app_test.go construct `tea.KeyMsg{Type: key}` and `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)}`. In v2, `tea.KeyMsg` is an interface (not a struct), `tea.KeyType` doesn't exist, and `tea.KeyRunes` doesn't exist. Both helpers must be rewritten using `tea.KeyPressMsg` before any production code changes, otherwise zero test feedback during migration.

**Fix**: Rewrite `sendKey`/`sendRune` first (Phase 3 step 0), before touching production handlers.

### Risk 5 — Kitty protocol unavailable in most terminals (silent feature loss)

Shift+Enter detection requires Kitty keyboard protocol. Terminal support as of May 2026:

- **Works**: Kitty, WezTerm, Ghostty, foot, Windows Terminal (recent)
- **Doesn't work**: macOS Terminal.app, older iTerm2, tmux (unless `set -g extended-keys on`), screen, most SSH sessions

Users in tmux (extremely common) get no newline capability with no feedback.

**Fix**: Bind `alt+enter` as a secondary/fallback newline key. Display the correct keybinding in the prompt footer based on detected capabilities (`[Shift+Enter] newline` vs `[Alt+Enter] newline`).

## Steps

### Phase 0 — Rewrite test helpers (unblocks everything)

1. Rewrite `sendKey(m, tea.KeyType)` → construct `tea.KeyPressMsg` with appropriate `Code` field
2. Rewrite `sendRune(m, string)` → construct `tea.KeyPressMsg` with `Text` field and `Code` rune
3. Update all `makeAnswerFields` and test-local `textarea.New()` calls for v2 API
4. Confirm `go vet ./internal/tui/...` passes on test files alone (production files still v1 — won't compile yet, that's expected)

### Phase 1 — Dependency swap

1. `go get charm.land/bubbletea/v2@latest charm.land/bubbles/v2@latest charm.land/lipgloss/v2@latest charm.land/glamour/v2@latest`
2. Remove old `github.com/charmbracelet/{bubbletea,bubbles,lipgloss,glamour}` from go.mod
3. Find-replace all import paths across `internal/tui/*.go` and tests:
   - `github.com/charmbracelet/bubbletea` → `charm.land/bubbletea/v2`
   - `github.com/charmbracelet/bubbles/textarea` → `charm.land/bubbles/v2/textarea`
   - `github.com/charmbracelet/bubbles/viewport` → `charm.land/bubbles/v2/viewport`
   - `github.com/charmbracelet/lipgloss` → `charm.land/lipgloss/v2`
   - `github.com/charmbracelet/glamour` → `charm.land/glamour/v2`

### Phase 2 — Hoist viewport mutations out of View() (Risk 1 fix)

1. Move `viewPipelineScreen()`'s `.SetContent()`, `.SetWidth()`, `.SetHeight()`, `.GotoBottom()` calls into a new `updateViewportContent()` method
2. Call `updateViewportContent()` from `Update()` on: `tickMsg`, `OrchestratorEventMsg`, `tea.WindowSizeMsg`, and any content-mode transition
3. `viewPipelineScreen()` becomes pure: reads viewport `.View()` only, no mutations
4. Same treatment for `viewPromptScreen()` if it touches viewport state

### Phase 3 — `tea.Model` interface

1. `model.go` `View() string` → `View() tea.View`, wrap returns with `tea.NewView(...)`, set:
   - `view.AltScreen = true`
   - `view.MouseMode = tea.MouseModeCellMotion`
   - `view.KeyboardEnhancements` for Kitty protocol (Shift detection)
2. `tui.go` — remove `tea.WithAltScreen()`, `tea.WithMouseCellMotion()` from `tea.NewProgram`

### Phase 4 — Key handling (Risk 2 fix)

1. All `case tea.KeyMsg:` → `case tea.KeyPressMsg:` in `model.go`, `filepicker.go`, `runs.go`
2. All `msg.Type` switches → `msg.Code` or `msg.String()` matching:
   - `tea.KeyEnter` → `tea.KeyEnter` (constant is a rune in v2, used with `msg.Code`)
   - `tea.KeyCtrlC` → `msg.String() == "ctrl+c"` or `msg.Code == 'c' && msg.Mod.Contains(tea.ModCtrl)`
   - `tea.KeyCtrlS` → `msg.String() == "ctrl+s"`
   - `tea.KeyCtrlR` → `msg.String() == "ctrl+r"`
   - `tea.KeyCtrlE` → `msg.String() == "ctrl+e"`
   - `tea.KeyEsc` → `msg.Code == tea.KeyEscape`
   - `tea.KeyTab` → `msg.Code == tea.KeyTab`
   - `tea.KeyShiftTab` → `msg.Code == tea.KeyTab && msg.Mod.Contains(tea.ModShift)`
   - `tea.KeyPgUp/PgDown` → `msg.Code == tea.KeyPgUp` / `tea.KeyPgDown`
   - `tea.KeyUp/Down` → `msg.Code == tea.KeyUp` / `tea.KeyDown`
   - `tea.KeyBackspace` → `msg.Code == tea.KeyBackspace`
3. Replace `msg.Type == tea.KeyRunes && len(msg.Runes) > 0` (filepicker.go) with `len(msg.Text) > 0`
4. Audit all `msg.String()` single-char comparisons — confirm v2 still returns `"d"`, `"@"`, `"1"` etc. for unmodified rune presses

### Phase 5 — Mouse handling

1. `model.go` `handleMouse(msg tea.MouseMsg)` — v2 `tea.MouseMsg` is an interface. Options:
   - Keep signature as `tea.MouseMsg` (interface matches all mouse types), access `.X` / `.Y` via type switch or the concrete fields on `tea.MouseClickMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg`
   - Since the handler only needs X/Y for spatial routing, use a helper to extract coordinates from any mouse message type

### Phase 6 — Viewport & Textarea

1. `model.go` — 7× `viewport.New(0, 0)` → `viewport.New()` (v2 uses functional options; zero-size is the default)
2. `layout.go` — all `.Width = x` / `.Height = y` → `.SetWidth(x)` / `.SetHeight(y)` (~16 sites)
3. `layout_test.go` — viewport field reads `.Width` / `.Height` → `.Width()` / `.Height()` methods (~6 sites)
4. `textarea`: `DefaultKeyMap` var → `DefaultKeyMap()` func if referenced. Check `Cursor` field access patterns.

### Phase 7 — The feature (Shift+Enter with Alt+Enter fallback — Risk 5 fix)

1. `NewModel`: override textarea `InsertNewline` binding to accept both:
   ```go
   ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter"))
   ```
2. `handlePromptKey`: intercept Enter without Shift/Alt modifiers as submit (current behavior). With Shift or Alt modifier, fall through to textarea's default handler which matches `InsertNewline`.
3. Prompt footer: display `[Shift+Enter] newline` (or `[Alt+Enter] newline` as secondary hint)
4. Coaching answer fields and plan comment textarea: same treatment — Shift+Enter/Alt+Enter for newline, plain Enter for submit

### Phase 8 — Verification

1. `go build ./...` compiles
2. `go test ./internal/tui/...` passes
3. `go vet ./...` clean
4. Manual in Kitty/WezTerm/Ghostty: Shift+Enter inserts newline, Enter submits
5. Manual in tmux/Terminal.app: Alt+Enter inserts newline, Enter submits, Shift+Enter submits (graceful degradation)
6. Manual: mouse scrolling, file picker, runs list, plan review, coaching answers all still work
7. Manual: verify no scroll-position jumps during streaming (Risk 1 regression check)

## Decisions

- v2 is stable (v2.0.6, released ~3 months ago) — not bleeding-edge risk
- All four charmbracelet deps must upgrade together (bubbletea, bubbles, lipgloss, glamour)
- Alt+Enter is the universal fallback for terminals without Kitty protocol — always works
- Shift+Enter is the primary binding — works in Kitty-capable terminals, silently degrades to submit elsewhere
- Viewport mutations hoisted from View() to Update() — correctness fix independent of v2, but mandatory for v2
- Test helpers rewritten first (Phase 0) to maintain test feedback loop throughout migration
- Scope boundary: TUI only, no orchestrator/agent changes needed
