# Plan: Human Plan Gate with Rich Markdown Review

Restore the broken plan approval gate between architect and worker, and upgrade it from raw text dump to a rich, interactive plan review experience with glamour-rendered markdown, architect chat, inline editing, and external editor support.

The `GatePlanApproval` gate IS wired in both orchestrator and TUI code, but something causes it to never appear at runtime. Phase 0 diagnoses the regression. Then we build the real UX: rendered markdown in a scrollable viewport, a comment textarea for arguing with the architect, inline editing, and external editor via `Ctrl+E`.

---

## Phase 0: Diagnose the Gate Regression _(independent)_

The gate event emission and handling code looks correct on paper. Likely causes:

1. **Event buffer saturation** — events channel is buffered(16); rapid research+planning events could fill it, causing `emit()` to block before the gate event is sent. Verify buffer is sufficient.
2. **`AutoApprove` accidentally set** — grep all `AutoApprove` assignments.
3. **State overwrite** — a subsequent event (e.g. `EventPhaseChange`) could arrive and overwrite `ContentPlanReview` back to `ContentStreaming`.

**Steps:**

1. Add debug `slog.Info` at the `planGate:` label in `orchestrator.run()`
2. Add a failing integration test: `AutoApprove: false` → assert `EventGateRequest(GatePlanApproval)` arrives
3. Check event channel pressure (increase buffer or use non-blocking sends with slog)

**Files:**

- `internal/orchestrator/orchestrator.go` (~L420-475) — `run()`, `planGate:` label
- `internal/tui/model.go` — `startPipeline()`, `handleOrchestratorEvent()`

---

## Phase 1: Add Glamour Dependency _(independent)_

1. `go get github.com/charmbracelet/glamour@latest`
2. New file `internal/tui/markdown.go` — helper `renderMarkdown(content string, width int) string` using `glamour.NewTermRenderer` with `WithAutoStyle()`, `WithWordWrap(width-4)`, `WithEmoji()`. Falls back to raw text on error.
3. Cache the `*glamour.TermRenderer` on `Model` (justified pointer: resource handle). Recreate when width changes in `recalculateLayout()`.

**Files:**

- `go.mod` / `go.sum` — new dependency
- `internal/tui/markdown.go` — new file, renderer helper

---

## Phase 2: Plan File Persistence Before Gate _(independent)_

1. Move `writeArtifact(session, "final_plan.md", ...)` to **before** the `EventGateRequest` emit in `orchestrator.run()`
2. Add `PlanFilePath string` to `GateRequest` so TUI knows the on-disk path
3. Add `EventRunDirReady` event emitted right after session creation (so TUI has `runDir` early)
4. On `DecisionEdit` and `DecisionComment`, re-write the file on disk too

**Files:**

- `internal/orchestrator/orchestrator.go` — move writeArtifact, add PlanFilePath to GateRequest, add EventRunDirReady
- `internal/tui/model.go` — new `runDir`, `planFilePath` fields, handle EventRunDirReady

---

## Phase 3: Rich Plan Review Mode _(depends on 1, 2)_

1. **Rendered view** — `viewPlanReview()` calls `renderMarkdown(m.finalPlan, width)` → displays in `contentVP` viewport. Scrollable via mouse wheel + PgUp/PgDown (already wired).
2. **Comment textarea** — New `planComment textarea.Model` on `Model`. Rendered in the input zone during `ContentPlanReview` (2-line textarea with placeholder "Comment to refine the plan…").
3. **Key bindings in `handlePlanReviewKey()`:**
   - `A` → `DecisionApprove` (existing)
   - `E` → switch to `ContentPlanEdit` (existing)
   - `Ctrl+E` → open `planFilePath` in external editor (Phase 4)
   - `Enter` (with non-empty comment) → `DecisionComment` (Phase 5)
   - `S` → cancel (existing)
4. **Footer** — `[A] accept | [E] edit | [Ctrl+E] ext editor | [Enter] comment | [S] cancel`
5. **Layout** — Input zone height for plan review increases from 2→4 lines (divider + instruction + 2-line textarea). Adjust `recalculateLayout()` with a `constPlanReviewInputHeight`.

**Files:**

- `internal/tui/model.go` — `viewPlanReview`, `handlePlanReviewKey`, `viewInputZone`, `viewFooter`
- `internal/tui/layout.go` — new `constPlanReviewInputHeight`
- `internal/tui/markdown.go` — renderer (from Phase 1)

---

## Phase 4: External Editor Support _(depends on 2)_

1. New file `internal/tui/editor.go`:
   - `openExternalEditor(path string) tea.Cmd` using `tea.ExecProcess` (Bubble Tea's native subprocess handoff — suspends TUI, runs editor, resumes)
   - Editor resolution: `$EDITOR` → `$VISUAL` → `open` (macOS fallback)
2. New message type `editorReturnMsg{content string, err error}`
3. On return: re-read file → update `m.finalPlan` → re-show gate (send `DecisionEdit` if changed)

**Files:**

- `internal/tui/editor.go` — new file
- `internal/tui/model.go` — handle `editorReturnMsg`, `Ctrl+E` keybinding
- `internal/tui/messages.go` — add `editorReturnMsg`

---

## Phase 5: Comment-Driven Replanning Loop _(depends on 3)_

1. `Enter` with non-empty comment → send `Decision{Type: DecisionComment, Comment: text}` to orchestrator
2. Switch to `ContentStreaming` while planner re-runs
3. Orchestrator already handles this: calls `RefineWithCommentsStreaming()` → emits new `EventGateRequest` → TUI re-enters `ContentPlanReview` with revised plan
4. Add `writeArtifact` in orchestrator's `DecisionComment` handler after revision

**Files:**

- `internal/tui/model.go` — `handlePlanReviewKey` Enter handler
- `internal/orchestrator/orchestrator.go` — add writeArtifact after revision in DecisionComment handler

---

## Phase 6: Tests _(depends on all)_

1. `TestRenderMarkdown` — glamour renders ANSI, falls back on error
2. `TestTUI_PlanReviewComment` — Enter with comment sends `DecisionComment`
3. `TestTUI_PlanReviewExternalEditor` — `Ctrl+E` fires editor command
4. `TestTUI_PlanFileSavedBeforeGate` — artifact exists before gate fires
5. Regression test: full pipeline `AutoApprove: false` blocks at gate

**Files:**

- `internal/tui/markdown_test.go` — new
- `internal/tui/app_test.go` — extended
- `internal/orchestrator/orchestrator_test.go` — extended

---

## Verification

1. `go test ./internal/orchestrator/... ./internal/tui/...` — all tests pass
2. `go vet ./... && go build ./...` — clean
3. Manual: run TUI → plan gate appears with rendered markdown → scroll with mouse/PgUp/PgDown
4. Manual: type comment + Enter → planner re-runs → revised plan reappears
5. Manual: `E` → inline edit → `Ctrl+S` saves → gate re-shows
6. Manual: `Ctrl+E` → `$EDITOR` opens `.md` → save/close → TUI resumes with changes

---

## Decisions

- **Glamour** (`charmbracelet/glamour` v1.0.0) — canonical Charm markdown renderer, used by GitHub CLI and `glow`
- **`Ctrl+E`** for external editor — `Cmd+Shift+E` is undetectable by terminal apps
- **`tea.ExecProcess`** for editor subprocess — Bubble Tea's native mechanism, clean TUI suspend/resume
- **Comment in input zone** — keeps the plan viewport fully dedicated to reading; input zone expands to 4 lines during plan review
- **`DecisionComment` gap filled** — the orchestrator backend supported it all along, but the TUI never sent it
- **Plan saved before gate, not after** — user requirement; also enables external editor to open the file

## Dependency Order

```
Phase 0 ─────┐
Phase 1 ─────┤
Phase 2 ─────┼──→ Phase 3 ──→ Phase 5 ──→ Phase 6
             └──→ Phase 4 ──────────────↗
```

Phases 0, 1, 2 can run in parallel.
