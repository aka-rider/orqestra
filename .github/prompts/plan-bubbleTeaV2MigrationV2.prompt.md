# Plan

## Goal

Upgrade all four charmbracelet dependencies (bubbletea, bubbles, lipgloss, glamour) from v1 to v2 and add Shift+Enter / Alt+Enter multiline input to the TUI prompt.

## Context

Verified by spot-checks against the codebase on 2026-05-11:

- The TUI refactor (plan-refactor-tui.md) is complete. model.go is 534 lines. The `Model` struct has 4 screen sub-models (`promptScreen`, `pipelineScreen`, `runsListScreen`, `runDetailScreen`) plus global UI state (22 fields total).
- **Viewport mutations are already hoisted.** All `SetContent()` calls live in `SyncViewports()` methods on each screen, called from `Update()`. Every `View()` method is a pure reader. Risk 1 from the original draft plan is eliminated.
- Intent pattern is in place: screens return `tea.Cmd` yielding typed intent messages (15 types in messages.go). Parent catches intents and performs side-effects.
- All tests pass: `go test ./internal/tui/... -count=1 -short` exits 0, `go vet ./internal/tui/...` clean, `go build ./cmd/orqestra` succeeds.

**Current dependencies (go.mod):**

- `github.com/charmbracelet/bubbles v1.0.0`
- `github.com/charmbracelet/bubbletea v1.3.10`
- `github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834`
- `github.com/charmbracelet/glamour v1.0.0` (indirect)

**File inventory (touch points for migration):**

| File                 | Lines | Key v2 touch points                                                                             |
| -------------------- | ----- | ----------------------------------------------------------------------------------------------- |
| model.go             | 534   | `View() string` → `tea.View`, `case tea.KeyMsg:` → `KeyPressMsg`, mouse routing, `tea.KeyCtrlC` |
| screen_pipeline.go   | ~1080 | ~35 key handler sites, mouse `msg.X`/`msg.Y`, 3× `viewport.New(0,0)`, `tea.KeyShiftTab`         |
| screen_prompt.go     | ~189  | `tea.KeyRunes` (L218), `msg.Runes` (L219), `@` detection, Enter/Shift+Enter                     |
| screen_runs_list.go  | ~124  | key handling, 1× `viewport.New(0,0)`                                                            |
| screen_run_detail.go | ~230  | key handling, 3× `viewport.New(0,0)`                                                            |
| layout.go            | 52    | `lipgloss.Place` via `joinSplitView()` — unchanged API                                          |
| styles.go            | 84    | `lipgloss.NewStyle()` / `.Foreground()` / `.Color()` — import path only                         |
| markdown.go          | 18    | `glamour.WithAutoStyle()` removal                                                               |
| tui.go               | 39    | `tea.WithAltScreen()` + `tea.WithMouseCellMotion()` removal                                     |
| editor.go            | 22    | `tea.ExecProcess` — import path only                                                            |
| messages.go          | 86    | import path only                                                                                |
| app_test.go          | ~1264 | `sendKey()`/`sendRune()` rewrite, viewport field access                                         |
| app_smoke_test.go    | ~211  | `tea.KeyMsg{}` → `tea.KeyPressMsg{}`                                                            |
| layout_test.go       | ~183  | viewport `.Width` → `.Width()` method calls                                                     |
| runs_test.go         | ~342  | `sendKey()` callers                                                                             |

**Architectural decisions:**

1. **WP2+3 execute together.** Test helpers can't compile without v2 imports, and v2 types don't exist until the import path swap. The dependency swap and test helper rewrite are one atomic step.
2. **Shift+Enter as primary, Alt+Enter as fallback.** Shift+Enter requires Kitty keyboard protocol (supported by Kitty, WezTerm, Ghostty, foot, Windows Terminal). Alt+Enter works universally in all terminals. Both are bound.
3. **`textarea.Focus()` and Dynamic Textareas.** In v2, `Focus()` returns `tea.Cmd`. While our `Init()` returns `textarea.Blink` independently for the main prompt, we must NOT discard the `Focus()` command for textareas created dynamically during the session (like the coaching answer fields in `makeAnswerFields()`), because `Init()` is never called by the Bubble Tea runtime for those. Return them as a batch.
4. **lipgloss styles need import path only.** `lipgloss.Color("15")` call syntax is unchanged in v2. `lipgloss.NewStyle()`, `.Bold()`, `.Foreground()`, `.Background()`, `.Faint()`, `.Border()`, `.BorderForeground()`, `lipgloss.RoundedBorder()`, `lipgloss.Height()`, `lipgloss.Width()`, `lipgloss.Place()` — all unchanged.

## Constraints

- Every work package must leave the codebase compiling, tests passing, and TUI functional. No multi-commit broken state.
- Do NOT change the visual appearance of any TUI screen. Every pixel must render identically before and after (except footer hints for Shift+Enter).
- Do NOT rename public symbols (`Run`, `RunHeadless`, `NewModel`, `Model`).
- Do NOT touch orchestrator, agent, harness, config, scheduler, sandbox, plan, tokenlimit, or any package outside `internal/tui`.
- Upgrade all four deps together (bubbletea, bubbles, lipgloss, glamour). Do not leave mixed v1/v2 in go.mod.
- `lipgloss` v2 removes `AdaptiveColor` — our code doesn't use it (all styles use plain `Color("N")`). Do not introduce adaptive colors.

## Risks

### Risk 1 — Viewport mutations in View() (ELIMINATED)

The TUI refactor hoisted all `SetContent()`, `GotoBottom()`, `SetWidth()`, `SetHeight()` calls from `View()` into `SyncViewports()` methods called from `Update()`. Verified: `grep -n 'SetContent' internal/tui/screen_pipeline.go` shows SetContent calls only in `SyncViewports()`, `Reset()`, and `RecalculateLayout()` — never in any `View()` or `view*()` method.

### Risk 2 — `tea.KeyCtrlC` and ctrl+key constants removed in v2

v2 removes all `tea.KeyCtrl*` named constants. Every `tea.KeyCtrlC`, `tea.KeyCtrlS`, `tea.KeyCtrlR`, `tea.KeyCtrlE` must become either `msg.String() == "ctrl+c"` or `msg.Code == 'c' && msg.Mod == tea.ModCtrl`. The codebase uses these in model.go (`KeyCtrlC`), screen_prompt.go (`KeyCtrlS`, `KeyCtrlR`), screen_pipeline.go (`KeyCtrlS`, `KeyCtrlE`, `KeyCtrlR`, `KeyShiftTab`).

**Mitigation:** WP5 audits every `tea.KeyCtrl*` and `tea.KeyShift*` site. Test helpers rewritten in WP2 provide immediate feedback. The `msg.String()` approach is used for all ctrl+key matching because it's the most readable and officially recommended in the v2 upgrade guide.

### Risk 3 — `tea.KeyRunes` removed, `msg.Runes` → `msg.Text`

screen_prompt.go L218 uses `msg.Type == tea.KeyRunes && len(msg.Runes) > 0` and L219 uses `string(msg.Runes)`. Both patterns are removed in v2.

**Mitigation:** Replace with `len(msg.Text) > 0` and `msg.Text` respectively. This is a 2-line mechanical change.

### Risk 4 — Viewport field access → getter/setter methods

v2 changes `viewport.Width`, `.Height`, `.YOffset` from public fields to getter/setter methods (`.Width()`, `.SetWidth()`, `.Height()`, `.SetHeight()`, `.YOffset()`, `.SetYOffset()`). The codebase has ~16 write sites in `recalculateLayout()` and `RecalculateLayout()`, plus ~6 read sites in layout_test.go and app_test.go.

**Mitigation:** Mechanical find-replace in WP7. All sites are in the tui package. Viewport constructor `viewport.New(0, 0)` → `viewport.New()` (7 sites).

### Risk 5 — Kitty protocol unavailable in most terminals

Shift+Enter detection requires Kitty keyboard protocol. macOS Terminal.app, older iTerm2, tmux (without `set -g extended-keys on`), screen, and most SSH sessions don't support it.

**Mitigation:** Alt+Enter bound as secondary/fallback newline key. Footer hint shows both. Plain Enter always submits (backward compatible). Users in unsupported terminals get Alt+Enter, which works everywhere.

## Work Packages

### 1. Dependency swap and import path rewrite

**Steps:**

1. Run `go get charm.land/bubbletea/v2@latest charm.land/bubbles/v2@latest charm.land/lipgloss/v2@latest charm.land/glamour/v2@latest` to add v2 dependencies.
2. Run `go mod tidy` to clean up go.mod and go.sum.
3. In every `.go` file under `internal/tui/`, find-replace these import paths:
   - `"github.com/charmbracelet/bubbletea"` → `"charm.land/bubbletea/v2"`
   - `"github.com/charmbracelet/bubbles/textarea"` → `"charm.land/bubbles/v2/textarea"`
   - `"github.com/charmbracelet/bubbles/viewport"` → `"charm.land/bubbles/v2/viewport"`
   - `"github.com/charmbracelet/lipgloss"` → `"charm.land/lipgloss/v2"`
   - `"github.com/charmbracelet/glamour"` → `"charm.land/glamour/v2"`
4. Verify no old import paths remain: `grep -rn 'charmbracelet' internal/tui/` must return empty.

**Done when:**

- `grep -rn 'charmbracelet' internal/tui/` returns 0 matches.
- `go mod tidy` exits 0 with no `charmbracelet` direct deps remaining.

### 2. Rewrite test helpers and all test key construction for v2

Test helpers must be updated so we have test feedback throughout the remaining migration.

**Steps:**

1. In `app_test.go`, rewrite `sendKey()` (L62-64). The v1 signature is `func sendKey(m tea.Model, key tea.KeyType) (tea.Model, tea.Cmd)` constructing `tea.KeyMsg{Type: key}`. In v2, `tea.KeyMsg` is an interface and `tea.KeyType` is removed. Rewrite to:
   ```go
   func sendKey(m tea.Model, key rune) (tea.Model, tea.Cmd) {
       return m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
   }
   ```
2. In `app_test.go`, rewrite `sendRune()` (L66-68). The v1 version constructs `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)}`. Rewrite to:
   ```go
   func sendRune(m tea.Model, r string) (tea.Model, tea.Cmd) {
       return m.Update(tea.KeyPressMsg{Code: rune(r[0]), Text: r})
   }
   ```
3. Add a helper for ctrl+key combinations (not present in v1 — new for v2):
   ```go
   func sendCtrl(m tea.Model, key rune) (tea.Model, tea.Cmd) {
       return m.Update(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl, Text: string(key)})
   }
   ```
4. Update ALL callers of `sendKey` across `app_test.go`, `runs_test.go`. v2 key constant renames:
   - `tea.KeyEnter` → `tea.KeyEnter` (rune constant, same name ✅)
   - `tea.KeyEsc` → `tea.KeyEscape`
   - `tea.KeyPgUp` → `tea.KeyPgUp` (same ✅)
   - `tea.KeyPgDown` → `tea.KeyPgDown` (same ✅)
   - `tea.KeyUp` → `tea.KeyUp` (same ✅)
   - `tea.KeyDown` → `tea.KeyDown` (same ✅)
   - For all ctrl+key calls: replace `sendKey(m, tea.KeyCtrlC)` with `sendCtrl(m, 'c')`, `sendKey(m, tea.KeyCtrlS)` with `sendCtrl(m, 's')`, `sendKey(m, tea.KeyCtrlR)` with `sendCtrl(m, 'r')`, `sendKey(m, tea.KeyCtrlE)` with `sendCtrl(m, 'e')`.
5. In `app_smoke_test.go` L163/L167, replace direct `tea.KeyMsg{Type: tea.KeyCtrlC}` with `tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, Text: "c"}`.
6. In `makeAnswerFields()` (`app_test.go` L595-610): `textarea.New()` API unchanged. `ta.Focus()` now returns `tea.Cmd`. **Do NOT discard it**. Since `Init()` is never called dynamically, you must collect it and batch it with the return `tea.Cmd` from `UpdateSubModel`. `ta.SetWidth()`, `ta.SetHeight()`, `ta.CharLimit`, `ta.SetValue()` — all unchanged.

**Done when:**

- All test files compile with v2 imports (verified together with subsequent WPs).
- `grep -n 'tea\.KeyType\|tea\.KeyMsg{' internal/tui/*_test.go` returns 0 matches.

### 3. `View()` return type and program options

**Steps:**

1. In `model.go`, change the `View()` method signature from `func (m Model) View() string` to `func (m Model) View() tea.View`. Replace the body:
   ```go
   func (m Model) View() tea.View {
       var content string
       switch m.state {
       case StatePrompt:
           content = m.promptScreen.View(m.effectiveWidth(), m.height)
       case StatePipeline:
           content = m.pipelineScreen.View(m.effectiveWidth(), m.height)
       case StateRunsList:
           content = m.runsListScreen.View(m.effectiveWidth(), m.height)
       case StateRunDetail:
           content = m.runDetailScreen.View(m.effectiveWidth(), m.height)
       }
       v := tea.NewView(content)
       v.AltScreen = true
       v.MouseMode = tea.MouseModeCellMotion
       return v
   }
   ```
2. In `tui.go`, remove `tea.WithAltScreen()` and `tea.WithMouseCellMotion()` from the `tea.NewProgram(model, ...)` call. The line becomes: `p := tea.NewProgram(model)`.

**Done when:**

- `go build ./internal/tui/...` exits 0.
- `grep -n 'WithAltScreen\|WithMouseCellMotion' internal/tui/tui.go` returns empty.

### 4. Key handling migration — all production files

Convert all key handling from v1 `tea.KeyMsg` struct / `.Type` / named constants to v2 `tea.KeyPressMsg` / `.Code` / `.String()`.

**Steps:**

**model.go:**

1. `case tea.KeyMsg:` (L178) → `case tea.KeyPressMsg:`.
2. `func (m Model) handleKey(msg tea.KeyMsg)` → `func (m Model) handleKey(msg tea.KeyPressMsg)`.
3. `if msg.Type == tea.KeyCtrlC` → `if msg.String() == "ctrl+c"`.
4. All four `handleXxxKey(msg tea.KeyMsg)` parameter types → `handleXxxKey(msg tea.KeyPressMsg)`.

**screen_prompt.go:**

5. `keyMsg, ok := msg.(tea.KeyMsg)` → `keyMsg, ok := msg.(tea.KeyPressMsg)`.
6. `switch keyMsg.Type {` → `switch keyMsg.Code {` (for named keys: `tea.KeyEnter`, `tea.KeyEscape`).
7. `case tea.KeyCtrlS:` → add a separate check: `if keyMsg.String() == "ctrl+s" { ... }` before the switch, or convert to a `msg.String()` switch. Recommended: convert the outer key dispatch to `switch keyMsg.String()` for ctrl combos, keep `switch keyMsg.Code` for named keys.
8. `case tea.KeyCtrlR:` → same treatment.
9. `keyMsg.String() == "@"` (L81) — unchanged ✅.
10. In `handleFilePickerKey()`: `switch msg.Type` → `switch msg.Code`. Constants: `tea.KeyEsc` → `tea.KeyEscape`, `tea.KeyEnter` → `tea.KeyEnter` ✅, `tea.KeyUp`/`tea.KeyDown` ✅, `tea.KeyBackspace` → `tea.KeyBackspace` ✅.
11. `msg.Type == tea.KeyRunes && len(msg.Runes) > 0` (L218) → `len(msg.Text) > 0`.
12. `string(msg.Runes)` (L219) → `msg.Text`.

**screen_pipeline.go:**

13. `func (s PipelineScreen) Update(msg tea.KeyMsg)` → `Update(msg tea.KeyPressMsg)`.
14. `switch msg.String()` at L283 — unchanged ✅ (rune comparisons "?", "d", "D", etc. return same strings in v2).
15. `switch msg.Type` at L297 → `switch msg.Code`. `case tea.KeyPgUp, tea.KeyPgDown:` → same names ✅.
16. `msg.Type == tea.KeyEsc` at L317 → `msg.Code == tea.KeyEscape`.
17. `msg.String() >= "1" && msg.String() <= "9"` at L325 — unchanged ✅.
18. In `handleCoachingKey()`: `switch msg.Type` → `switch msg.Code`. `tea.KeyEnter` → `tea.KeyEnter` ✅. `tea.KeyCtrlS` → check via `msg.String() == "ctrl+s"`. `tea.KeyTab` → `tea.KeyTab` ✅. `tea.KeyShiftTab` → `msg.Code == tea.KeyTab && msg.Mod.Contains(tea.ModShift)`.
19. In `handlePlanReviewKey()`: `switch msg.Type` → `switch msg.Code`. `tea.KeyCtrlE` → `msg.String() == "ctrl+e"`. `tea.KeyEnter` ✅. `switch msg.String()` at L453 — unchanged ✅.
20. In `handlePlanEditKey()`: `switch msg.Type` → `switch msg.Code`. `tea.KeyCtrlS` → `msg.String() == "ctrl+s"`. `tea.KeyEsc` → `tea.KeyEscape`.
21. In `handleAgentHistoryKey()`: `switch msg.Type` → `switch msg.Code`. `tea.KeyEsc` → `tea.KeyEscape`.
22. In `handleStreamingKey()`: `switch msg.String()` — unchanged ✅.
23. In `handleCompletionKey()`: `switch msg.Type` → `switch msg.Code`. `tea.KeyCtrlR` → `msg.String() == "ctrl+r"`. `switch msg.String()` — unchanged ✅.
24. `HandleMouse(msg tea.MouseMsg)` signature — stays as `tea.MouseMsg` (interface in v2 ✅).

**screen_runs_list.go:**

25. `keyMsg, ok := msg.(tea.KeyMsg)` → `keyMsg, ok := msg.(tea.KeyPressMsg)`.
26. `switch keyMsg.Type` → `switch keyMsg.Code`. Constants: `tea.KeyEsc` → `tea.KeyEscape`, rest unchanged.
27. `switch keyMsg.String()` — unchanged ✅.

**screen_run_detail.go:**

28. Same pattern as screen_runs_list.go. `tea.KeyMsg` → `tea.KeyPressMsg`, `.Type` → `.Code`, `tea.KeyEsc` → `tea.KeyEscape`.

**Done when:**

- `go build ./internal/tui/...` exits 0.
- `grep -n 'tea\.KeyMsg' internal/tui/*.go | grep -v '_test.go' | grep -v 'interface\|tea\.KeyMsg)'` — should show zero v1-style `tea.KeyMsg` struct usage. Note: `tea.KeyMsg` as an interface type annotation in function signatures that accept both press and release is valid in v2.
- `grep -n '\.Type ==' internal/tui/*.go | grep -v '_test.go'` returns 0 matches (no v1 `.Type` access on key messages).

### 5. Mouse handling migration

**Steps:**

1. In `model.go`, `case tea.MouseMsg:` — keep as-is. `tea.MouseMsg` is an interface in v2 and matches all mouse event types. Do not re-wrap or attempt to redefine this into the `tea.MouseMsg` interface contextually during tests; use the direct implementation.
2. `func (m Model) handleMouse(msg tea.MouseMsg)` — keep signature. `tea.MouseMsg` interface.
3. In `screen_pipeline.go` `HandleMouse(msg tea.MouseMsg)`:
   - Change `p := image.Pt(msg.X, msg.Y)` to `mouse := msg.Mouse(); p := image.Pt(mouse.X, mouse.Y)`.
   - Viewport `.Update(msg)` calls — keep as-is (viewport accepts `tea.Msg`), ensuring we pass the unwrapped original message.

**Done when:**

- `go build ./internal/tui/...` exits 0.
- `grep -n 'msg\.X\|msg\.Y' internal/tui/screen_pipeline.go` returns only the `mouse.X`, `mouse.Y` pattern (no direct `.X`/`.Y` on `tea.MouseMsg` interface).

### 6. Viewport and textarea API migration

**Steps:**

**Viewport constructor:**

1. Replace all `viewport.New(0, 0)` with `viewport.New()`. Sites:
   - `screen_pipeline.go` `NewPipelineScreen()`: 3 calls (contentVP, sidebarVP, dashboardVP).
   - `screen_runs_list.go` `NewRunsListScreen()`: 1 call.
   - `screen_run_detail.go` `NewRunDetailScreen()`: 3 calls.

**Viewport field access → getter/setter methods:**

2. In `model.go` `recalculateLayout()` (~L500-534): All viewport dimension writes must change:
   - `m.runsListScreen.viewport.Width = m.width` → `m.runsListScreen.viewport.SetWidth(m.width)`
   - `m.runsListScreen.viewport.Height = contentHeight` → `m.runsListScreen.viewport.SetHeight(contentHeight)`
   - `m.runDetailScreen.detailVP.Width = contentWidth` → `m.runDetailScreen.detailVP.SetWidth(contentWidth)`
   - `m.runDetailScreen.detailVP.Height = upperHeight` → `m.runDetailScreen.detailVP.SetHeight(upperHeight)`
   - `m.runDetailScreen.stepsVP.Width = sidebarWidth` → `m.runDetailScreen.stepsVP.SetWidth(sidebarWidth)`
   - `m.runDetailScreen.stepsVP.Height = upperHeight` → `m.runDetailScreen.stepsVP.SetHeight(upperHeight)`
   - `m.runDetailScreen.logVP.Width = m.width` → `m.runDetailScreen.logVP.SetWidth(m.width)`
   - `m.runDetailScreen.logVP.Height = constRunLogHeight` → `m.runDetailScreen.logVP.SetHeight(constRunLogHeight)`

3. In `screen_pipeline.go` `RecalculateLayout()` (~L172-182): All viewport dimension writes:
   - `s.contentVP.Width = contentWidth` → `s.contentVP.SetWidth(contentWidth)`
   - `s.contentVP.Height = contentHeight` → `s.contentVP.SetHeight(contentHeight)`
   - `s.sidebarVP.Width = sidebarWidth` → `s.sidebarVP.SetWidth(sidebarWidth)`
   - `s.sidebarVP.Height = contentHeight` → `s.sidebarVP.SetHeight(contentHeight)`
   - `s.dashboardVP.Width = width` → `s.dashboardVP.SetWidth(width)`
   - `s.dashboardVP.Height = contentHeight` → `s.dashboardVP.SetHeight(contentHeight)`

4. In `screen_pipeline.go` `effectiveWidth()`: `s.contentVP.Width` → `s.contentVP.Width()`, `s.sidebarVP.Width` → `s.sidebarVP.Width()`.

5. In `layout_test.go`: All viewport field reads in assertions:
   - `m.pipelineScreen.contentVP.Width` → `m.pipelineScreen.contentVP.Width()`
   - `m.pipelineScreen.contentVP.Height` → `m.pipelineScreen.contentVP.Height()`
   - `m.pipelineScreen.sidebarVP.Width` → `m.pipelineScreen.sidebarVP.Width()`
   - `m.pipelineScreen.dashboardVP.Width` → `m.pipelineScreen.dashboardVP.Width()`

6. In `app_test.go`: viewport field reads in assertions:
   - `model.pipelineScreen.contentVP.YOffset` → `model.pipelineScreen.contentVP.YOffset()`
   - `model.runsListScreen.viewport.Width` → `model.runsListScreen.viewport.Width()` (if referenced)

7. In `runs_test.go`: `model.runsListScreen.viewport.Width` → `model.runsListScreen.viewport.Width()` (if referenced).

**Glamour:**

8. In `markdown.go`, remove `glamour.WithAutoStyle()` from the `NewTermRenderer()` call. v2 defaults to dark theme. The call becomes:
   ```go
   r, err := glamour.NewTermRenderer(
       glamour.WithWordWrap(max(20, width-4)),
       glamour.WithEmoji(),
   )
   ```

**Done when:**

- `go build ./internal/tui/...` exits 0.
- `go test ./internal/tui/... -count=1` exits 0.
- `grep -n 'viewport\.New(0' internal/tui/` returns empty.
- `grep -n 'WithAutoStyle' internal/tui/` returns empty.
- `grep -nE '\.(contentVP|sidebarVP|dashboardVP|detailVP|stepsVP|logVP|viewport)\.(Width|Height|YOffset) [^(]' internal/tui/*.go` returns empty (no direct field access without parens).

### 7. Shift+Enter / Alt+Enter multiline input

**Steps:**

1. In `screen_prompt.go` `Update()`, change the Enter key handling. Currently `case tea.KeyEnter:` submits the prompt unconditionally. Change to:

   ```go
   case tea.KeyEnter:
       if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
           // Let textarea handle Shift+Enter / Alt+Enter as newline
           var cmd tea.Cmd
           s.textarea.InsertString("\n")
           s.textarea, cmd = s.textarea.Update(msg)
           return s, cmd
       }
       prompt := strings.TrimSpace(s.textarea.Value())
       if prompt == "" {
           return s, nil
       }
       return s, intentCmd(StartPipelineIntent{Prompt: prompt, SkipGateway: false})
   ```

2. In `screen_pipeline.go` `handleCoachingKey()`, apply the same pattern to the Enter handler. Currently `case tea.KeyEnter:` submits coaching answers. Change to check for Shift/Alt modifiers and pass through to textarea if present.

3. In `screen_pipeline.go` `handlePlanReviewKey()`, apply the same pattern to the Enter handler at L438. Currently Enter submits a plan comment. With Shift/Alt, pass through to the plan comment textarea for newline.

4. Update the prompt screen footer in `screen_prompt.go` `View()`. The current footer line reads:

   ```
   [Enter] submit | [Ctrl+S] skip gateway | [Ctrl+R] runs  [^C^C] quit
   ```

   Change to:

   ```
   [Enter] submit | [Shift+Enter] newline | [Ctrl+S] skip gateway | [Ctrl+R] runs  [^C^C] quit
   ```

5. Update the coaching and plan review footer hints similarly where keybinding help is displayed.

6. Add a test in `app_test.go`: construct a `tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}`, send it to a model in `StatePrompt` with text in the textarea. Assert the model stays in `StatePrompt` (did not submit). Then send a plain `tea.KeyPressMsg{Code: tea.KeyEnter}` and assert it transitions to `StatePipeline` (submitted).

**Done when:**

- `go build ./cmd/orqestra` exits 0.
- `go test ./internal/tui/... -count=1` exits 0.
- New test `TestTUI_ShiftEnterNewline` passes.
- `grep -n 'ModShift\|ModAlt' internal/tui/screen_prompt.go` returns at least 2 matches.

## Verification

Commands the worker runs after ALL packages complete to confirm success:

- `go build ./...`
- `go test ./internal/tui/... -count=1 -v`
- `go vet ./...`
- Manual: launch TUI, verify prompt renders, try Shift+Enter (in Kitty/WezTerm/Ghostty), try Alt+Enter (universal).
- Manual: run a pipeline, verify coaching screen, plan review, streaming, completion render identically.
- Manual: verify mouse scrolling in all viewports.
- Manual: verify file picker (@) works.
- Manual: verify runs list navigation and detail view.

## Assumptions

1. The 4 pre-existing test compilation errors from the TUI refactor are already fixed. If they're not, WP2 test compilation will surface them immediately. The worker should fix any remaining `m.applyEvent()` → `m.pipelineScreen.ApplyEvent()` and `m.contentVP` → `m.pipelineScreen.contentVP` references before proceeding.
2. `charm.land/glamour/v2` preserves `WithWordWrap()` and `WithEmoji()` options. Verified from the glamour v2 release notes — the API is preserved minus `WithAutoStyle()` and `WithColorProfile()`.
3. `charm.land/bubbletea/v2` `tea.KeyEnter`, `tea.KeyEscape`, `tea.KeyTab`, `tea.KeyPgUp`, `tea.KeyPgDown`, `tea.KeyUp`, `tea.KeyDown`, `tea.KeyBackspace` are rune constants (not removed like `tea.KeyCtrlC`). Verified from the v2 upgrade guide.
4. `tea.ExecProcess` still exists in v2 for external editor integration. Verified from the v2 source code.
5. `viewport.MouseWheelEnabled` is still a public bool field in v2 (not removed or made a method). Verified from the v2 viewport source.

## Gotchas

1. **`tea.KeyCtrlC` and all `tea.KeyCtrl*` constants are REMOVED in v2.** There are no named constants for ctrl+key combos. Every `tea.KeyCtrlC`, `tea.KeyCtrlS`, `tea.KeyCtrlR`, `tea.KeyCtrlE` reference must be replaced with `msg.String() == "ctrl+c"` style matching or `msg.Code == 'c' && msg.Mod == tea.ModCtrl`. This affects ~12 sites across model.go, screen_prompt.go, and screen_pipeline.go.
2. **`tea.KeyShiftTab` is REMOVED.** Must use `msg.Code == tea.KeyTab && msg.Mod.Contains(tea.ModShift)`. One site in screen_pipeline.go `handleCoachingKey()`.
3. **`tea.KeyEsc` renamed to `tea.KeyEscape`.** ~8 sites across screen files.
4. **`msg.Mouse()`** — In v2, `tea.MouseMsg` is an interface. Coordinates are accessed via `msg.Mouse().X`, `msg.Mouse().Y`, not `msg.X`/`msg.Y` directly. One site in screen_pipeline.go `HandleMouse()`.
5. **`viewport.New(0, 0)` → `viewport.New()`** — v2 uses functional options. Zero-size is the default when no options are passed. 7 sites total.
6. **Viewport `.Width`/`.Height`/`.YOffset` are now methods, not fields.** Read access must add `()`, write access must use `Set*()`. ~30 sites across production code and tests.
7. **`textarea.Focus()` returns `tea.Cmd` in v2.** Current code discards the return in `NewPromptScreen()` and `makeAnswerFields()`. This is safe because `Init()` returns `textarea.Blink` which handles cursor blink independently. If blink misbehaves in testing, batch the Focus Cmd into Init.
8. **`glamour.WithAutoStyle()` removed in v2.** Must be deleted from `renderMarkdown()` in markdown.go. v2 defaults to dark theme.
9. **Space bar returns `"space"` not `" "` in v2** when using `msg.String()`. The codebase doesn't compare against `" "` anywhere, so no impact.
10. **Bubbles v2 `DefaultStyles()` requires `isDark bool`** parameter. If you ever need to call `textarea.DefaultStyles()` or `viewport.DefaultStyles()`, pass `true`. Currently, `textarea.New()` internally uses `DefaultDarkStyles()`, so no explicit call is needed.
11. **The `model.go.bak` file** in the tui directory is leftover from the refactor. It still contains v1 code and will cause confusing grep results. Ignore it — it's not compiled.
