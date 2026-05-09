# Plan

## Goal

Persist per-step metadata and the original prompt into session directories, and add a Runs History TUI view that lets users browse past pipeline runs and inspect Claude JSONL logs in a two-column layout.

## Context

Key codebase facts verified by spot-checking:

- **Phase 1 (stream parsing) is already complete.** `RunStreaming()` and `RunContinue()` in `internal/harness/claude_cli.go` already pass `--include-partial-messages`, handle `stream_event` wrappers, dispatch `content_block_start`/`content_block_delta` from inner events, and call `extractAssistantToolUses()` on `assistant` messages. The `streamEvent` struct already has the `Event json.RawMessage` field. No stream parsing work remains.
- **Phase 1b (multi-line tool-use rendering) is already complete.** `renderActivityLog()` in `internal/tui/model.go:1092` renders a vertical multi-line log with `maxShow = 8`. Styles `activityToolStyle` (color 244, faint), `activityPathStyle` (color 12), and `activityDetailStyle` (color 244, faint) exist in `internal/tui/styles.go`. `fileHyperlink()` and `isFilePathTool()` are implemented.
- **`SessionDir`** in `internal/agent/session.go` provides `WriteArtifact(name, []byte)`, `ReadArtifact(name)`, `ArtifactPath(name)`. Dirs live under `.orqestra/sessions/<timestamp>-<slug>/`.
- **Orchestrator already writes** `researcher_draft.md`, `final_plan.md`, `worker_output.txt`, `worker_validation.txt` via `writeArtifact()`. `writeArtifactJSON()` exists at line 658 but is **never called** — no per-step JSON metadata is persisted.
- **The original user prompt is NOT saved** to the session dir.
- **`AppState`** has `StatePrompt`, `StatePipeline`. **`ContentMode`** has `ContentStreaming`, `ContentCoaching`, `ContentPlanReview`, `ContentPlanEdit`, `ContentAgentHistory`, `ContentCompletion`.
- **`View()`** dispatches to `viewPromptScreen()` or `viewPipelineScreen()` based on `m.state`.
- **`viewPipelineScreen()`** uses `joinSplitView()` with `contentVP` (left, 75% width) + `sidebarVP` (right, 25% width) and `dashboardVP` for full-width toggle.
- **`viewSidebar()`** renders agent list with icon + ID + elapsed + token count — this pattern is directly reusable for the step list in run details.
- **`viewDashboard()`** renders a full-width table with agent stats — this pattern is reusable for the runs list.
- **`recalculateLayout()`** derives all viewport dims from `m.width`, `m.height`, and named `const` chrome heights. New viewports follow the same pattern.
- **`viewAgentHistory()`** is a stub: `"(output history not captured in this mode)"` — this confirms agent output replay is not yet implemented.
- **Key handlers**: `handlePromptKey()` for `StatePrompt`, `handlePipelineKey()` for `StatePipeline`. Each `ContentMode` has a dedicated handler (e.g. `handleCompletionKey()`).

## Constraints

- Do NOT modify stream parsing in `internal/harness/claude_cli.go` — it is already correct.
- Do NOT modify `renderActivityLog()`, `activityToolStyle`, `activityPathStyle`, or `activityDetailStyle` — they are already correct.
- Do NOT add architecture, UX, or performance scope beyond what this plan describes.
- Do NOT import `internal/harness` from `internal/tui` — metadata types for the runs view belong in `internal/agent` or a new `internal/runs` package.
- Do NOT add generics or framework abstractions. Follow existing patterns (value receivers, table-driven tests, concrete types).

## Risks

- **JSONL path resolution**: Claude CLI stores session logs at `~/.claude/projects/-<cwd-with-dashes>/<session-id>.jsonl`. The CWD-to-dash conversion (`/Users/foo/bar` → `-Users-foo-bar`) may not always match if the workspace uses symlinks or non-canonical paths. Mitigation: use `filepath.EvalSymlinks` on CWD before converting, and return a clear error if the directory or file doesn't exist rather than failing silently.
- **Empty or corrupt session dirs**: `.orqestra/sessions/` may contain dirs with no `prompt.md` or no metadata files. Mitigation: skip malformed entries in the list view with a `(no data)` indicator rather than crashing.
- **Viewport state conflicts**: Adding a new `AppState` means `recalculateLayout()` needs a new branch, and `View()` needs a new dispatch. Mitigation: follow the exact `StatePrompt`/`StatePipeline` pattern — no new layout approach.

## Work Packages

### 1. Persist original prompt and per-step metadata in session directory

**Steps:**

1. In `internal/orchestrator/orchestrator.go`, in the `run()` function, immediately after the session directory is created (after `emit(Event{Type: EventRunDirReady, ...})`), call `writeArtifact(session, "prompt.md", input.Prompt)` to save the original user prompt.
2. Define a `stepMeta` struct (unexported, in `orchestrator.go`) with fields: `AgentID string`, `ModelRef string`, `StartTime time.Time`, `EndTime time.Time`, `ClaudeSessionID string`, `Status string`, `Error string`, `InputTokens int64`, `OutputTokens int64`.
3. After each `EventAgentDone` or `EventAgentFailed` emission in `run()`, call `writeArtifactJSON(session, "<agentID>_meta.json", meta)` with the populated `stepMeta`. There are five agent completion points in `run()`: gateway (line ~340/350/358), researcher (line ~431), planner (line ~465), worker (line ~553), validator (line ~590). Each needs a `stepMeta` write.
4. To capture `ClaudeSessionID`, the `RunResult.SessionID` field is already returned by `RunStreaming`/`RunContinue`. Thread it into the `stepMeta` for the worker and validator steps (gateway/researcher/planner use `RunPrint` or `RunStreaming` which also return `SessionID`).

**Done when:**

- `go test ./internal/orchestrator/...` passes.
- A manual pipeline run produces `.orqestra/sessions/<run>/prompt.md` containing the user's prompt text.
- The same run dir contains `gateway_meta.json`, `researcher_meta.json`, `planner_meta.json`, `worker_meta.json`, `validator_meta.json` — each with non-zero `StartTime`/`EndTime` and `Status` fields.

### 2. Add run listing capability in `internal/agent/session.go`

**Steps:**

1. Add a `RunSummary` struct in `internal/agent/session.go`: `Timestamp time.Time`, `Slug string`, `Path string`, `Prompt string` (loaded from `prompt.md`), `Status string` (loaded from last `*_meta.json` or empty), `Duration time.Duration`.
2. Add a `ListRuns(repoPath string) ([]RunSummary, error)` function that scans `.orqestra/sessions/`, parses directory names into timestamp + slug, reads `prompt.md` from each dir (skip if missing), reads the last `*_meta.json` to determine status. Return sorted newest-first.
3. Add a `LoadRunDetail(runPath string) (RunDetail, error)` function. `RunDetail` contains `RunSummary` plus `Steps []StepMeta` (read from all `*_meta.json` files in the dir), `PlanMarkdown string` (from `final_plan.md`), `WorkerOutput string` (from `worker_output.txt`), `Validation string` (from `worker_validation.txt`).
4. Add unit tests in `internal/agent/session_test.go` (new file): create a temp `.orqestra/sessions/` dir with known artifacts, test `ListRuns` returns correct order and content, test `LoadRunDetail` loads all fields.

**Done when:**

- `go test ./internal/agent/...` passes.
- `ListRuns` returns entries sorted newest-first and handles missing `prompt.md` gracefully (empty string, not error).
- `LoadRunDetail` loads all metadata files and plan/validation artifacts.

### 3. Add `StateRunsList` and `StateRunDetail` to the TUI model

**Steps:**

1. In `internal/tui/model.go`, add two new `AppState` constants: `StateRunsList AppState = 2`, `StateRunDetail AppState = 3` (after `StatePipeline`).
2. Add fields to `Model`: `runs []agent.RunSummary` (cached list), `runDetail agent.RunDetail` (loaded on selection), `runsCursor int` (selected row in list), `runStepCursor int` (selected step in detail right column), `runLogLines []string` (parsed JSONL lines for the lower raw-log pane), `runsVP viewport.Model` (list viewport), `runDetailVP viewport.Model` (upper-left: prompt + output content), `runStepsVP viewport.Model` (upper-right: agent step menu), `runLogVP viewport.Model` (lower: raw agent stream output).
3. In `NewModel()`, initialize the four new viewports (`runsVP`, `runDetailVP`, `runStepsVP`, `runLogVP`) with `viewport.New(0, 0)` and `MouseWheelEnabled = true`, following the existing `cvp`/`svp`/`dvp` pattern.
4. In `recalculateLayout()`, add cases for `StateRunsList` and `StateRunDetail`. `StateRunsList` is full-width (like dashboard). `StateRunDetail` splits vertically: upper zone gets `contentHeight - constRunLogHeight` and is split horizontally via `splitRatio` (left = `runDetailVP`, right = `runStepsVP`); lower zone is `constRunLogHeight` lines for `runLogVP`, spanning full width. Add `constRunLogHeight = 8` to the layout constants in `layout.go`.
5. In `View()`, add dispatch: `StateRunsList` → `m.viewRunsListScreen()`, `StateRunDetail` → `m.viewRunDetailScreen()`.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- `go vet ./...` passes.
- The model has the new states and fields; no runtime behavior yet (next WP adds rendering and navigation).

### 4. Implement runs list screen rendering and navigation

**Steps:**

1. Add `viewRunsListScreen()` method to `Model`. Layout: header (`" Orqestra — Runs History"` + divider), full-width viewport with run entries, footer with keybindings. Each run entry is 2 lines: line 1 = timestamp + status icon (reuse `✓`/`✗`/`○` pattern from `viewSidebar()`) + duration + token count; line 2 = prompt text truncated to terminal width, rendered in `dimStyle`.
2. Add `handleRunsListKey(msg tea.KeyMsg)` method. Keys: `j`/`↓` move cursor down, `k`/`↑` move cursor up, `Enter` loads detail (call `agent.LoadRunDetail(m.runs[m.runsCursor].Path)`, set `m.runDetail`, switch to `StateRunDetail`), `Esc`/`q` return to `StatePrompt`, `PgUp`/`PgDn` scroll viewport.
3. Wire navigation into runs list: in `handlePromptKey()`, add a case for `Ctrl+H` — call `agent.ListRuns(cwd)` (get cwd from `os.Getwd()`), populate `m.runs`, set `m.state = StateRunsList`, reset `m.runsCursor = 0`. In `handlePipelineKey()`, add `Ctrl+H` only when `m.content == ContentCompletion` (don't allow mid-pipeline navigation to runs).
4. In `handleKey()`, add `StateRunsList` dispatch to `handleRunsListKey()`.
5. Update `viewFooter()` to return appropriate hint text for `StateRunsList`.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- From the prompt screen, pressing `Ctrl+H` shows a list of past runs (or an empty state if no `.orqestra/sessions/` exists).
- `↑`/`↓`/`j`/`k` navigate the list. `Esc` returns to prompt. `Enter` transitions to detail view.

### 5. Implement run detail screen with 3-zone layout (upper split + lower log pane)

**Layout:**

```
┌─────────────────────────────────┬────────────┐
│  Input Prompt                   │  gateway   │
│                                 │  researcher│
│          ⇩ ⇩ ⇩                 │  planner   │
│                                 │ ▶worker    │
│  Final Plan / Output            │  validator │
│  (rendered markdown)            │            │
├─────────────────────────────────┴────────────┤
│  Raw agent stream (from ~/.claude JSONL)     │
│   Read  internal/config/config.go            │
│   Bash  go test ./internal/config/...        │
│   Edit  internal/tui/model.go                │
└──────────────────────────────────────────────┘
```

**Steps:**

1. Add `viewRunDetailScreen()` method. Layout has 3 zones plus header/footer chrome:
   - **Header** (2 lines): run timestamp + status icon + elapsed + config name. Reuse `headerStyle`/`phaseStyle`/`elapsedStyle` from `viewPipelineScreen()`.
   - **Upper zone** (split horizontally via `joinSplitView()`): Left = `runDetailVP`, Right = `runStepsVP`. The left pane shows the original input prompt (from `m.runDetail.Prompt`), followed by a 3-line unicode separator (`"\n    ⇩  ⇩  ⇩\n\n"`), followed by the final plan/output rendered via `renderMarkdown(m.runDetail.PlanMarkdown, width)`. The right pane is a vertical agent menu.
   - **Divider** (1 line): `dividerStyle.Render(strings.Repeat("─", width))`.
   - **Lower zone** (`constRunLogHeight` = 8 lines, full width): `runLogVP` showing parsed JSONL tool-use/text lines from `m.runLogLines`.
   - **Footer** (2 lines): keybindings.
2. Add `viewRunSteps(width int)` method that renders the step list as a vertical menu. For each `step` in `m.runDetail.Steps`: icon based on `step.Status` (reuse the same `✓`/`✗`/`▶`/`○` mapping from `viewSidebar()`), step name (= `AgentID`), elapsed time (computed from `EndTime - StartTime`), token count. **Highlight** the currently selected step (`m.runStepCursor`) with inverted colors or `fpSelectedStyle`. Non-selected steps use `dimStyle`.
3. Add `handleRunDetailKey(msg tea.KeyMsg)` method. Keys:
   - `↑`/`↓` (arrow keys): scroll the lower raw-log viewport (`runLogVP`).
   - `j`/`k`: move `m.runStepCursor` up/down in the right-column step menu. When the step changes, reload `m.runLogLines` by calling `loadStepLog()` (see WP6) for the newly selected step's `ClaudeSessionID`, and update `runLogVP` content.
   - `PgUp`/`PgDn`: scroll the upper-left `runDetailVP` (prompt + plan).
   - `Ctrl+E`: open the selected step's Claude JSONL in system editor.
   - `Esc`: return to `StateRunsList`.
4. In `handleKey()`, add `StateRunDetail` dispatch to `handleRunDetailKey()`.
5. Update `viewFooter()` for `StateRunDetail`: `" [↑↓] scroll log | [j/k] step | [PgUp/PgDn] scroll plan | [Ctrl+E] open log | [Esc] back  [^C^C] quit"`.
6. In `recalculateLayout()` under the `StateRunDetail` branch, compute:
   - `upperHeight = contentHeight - constRunLogHeight - 1` (1 for the divider between upper and lower).
   - `lowerHeight = constRunLogHeight`.
   - Left/right widths from `splitRatio` (same as pipeline).
   - Set `runDetailVP.Width`, `runDetailVP.Height`, `runStepsVP.Width`, `runStepsVP.Height`, `runLogVP.Width = m.width`, `runLogVP.Height = lowerHeight`.

**Done when:**

- `go build ./cmd/orqestra` compiles.
- From the runs list, pressing `Enter` shows the 3-zone detail view: upper-left has prompt → `⇩⇩⇩` → plan, upper-right has the agent step menu, lower has raw JSONL log lines.
- `↑`/`↓` scroll the lower log pane. `j`/`k` switch the selected step and reload log lines. `PgUp`/`PgDn` scroll the upper-left plan pane.
- `Esc` returns to the runs list.

### 6. Add Claude JSONL log path resolver, parser, and `Ctrl+E` editor open

**Steps:**

1. Create `internal/harness/logpath.go` with:
   - `CwdToDash(absPath string) string` — exported, converts `/Users/xiii/Developer/orqestra` → `-Users-xiii-Developer-orqestra` (replace each `/` with `-`, result starts with `-`).
   - `ResolveSessionLogPath(sessionID string) (string, error)` — computes `home + "/.claude/projects/" + CwdToDash(cwd) + "/" + sessionID + ".jsonl"` where `cwd` is obtained via `os.Getwd()` + `filepath.EvalSymlinks`. Returns `os.ErrNotExist` if the file doesn't exist.
   - `ParseSessionLog(path string, maxLines int) ([]string, error)` — reads the JSONL file line by line, filters to `type:"assistant"` entries, and renders each content block as a single display line:
     - `tool_use` blocks → `" " + activityToolStyle(toolName) + " " + activityPathStyle(detail)` (reuse `ToolDetail()` from `output.go` for the detail text).
     - `text` blocks → `" ╶ " + dimStyle(first 120 chars of text)`, truncated to one line.
     - Skip `type:"user"`, `type:"system"`, `type:"rate_limit_event"`, and queue operations.
     - Return the last `maxLines` lines (newest at the bottom). If the file doesn't exist or is empty, return `nil, nil`.
2. Add unit tests in `internal/harness/logpath_test.go`:
   - Test `CwdToDash` conversion with `/Users/foo/bar` → `-Users-foo-bar`, `/` → `-`, and paths with trailing slashes.
   - Test `ParseSessionLog` with a temp JSONL file containing sampled real entries (assistant with tool_use, assistant with text, user with tool_result). Assert correct line count, assert tool_result lines are skipped, assert tool_use lines contain tool name and detail.
3. In `handleRunDetailKey()` for `Ctrl+E`: look up `m.runDetail.Steps[m.runStepCursor].ClaudeSessionID`. Call `harness.ResolveSessionLogPath(sessionID)`. If found, open with `exec.Command("open", path)` on macOS (reuse the `editorReturnMsg` pattern). If not found, set `m.lastErr`.
4. Add a `loadStepLog(sessionID string)` helper method on `Model` (or a free function called from `handleRunDetailKey`). It calls `harness.ResolveSessionLogPath(sessionID)`, then `harness.ParseSessionLog(path, 200)` to populate `m.runLogLines`. If the session ID is empty or the file is missing, set `m.runLogLines = []string{"(no agent log available)"}`. Update `runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))` and `runLogVP.GotoBottom()`.

**Done when:**

- `go test ./internal/harness/...` passes with logpath + parser tests.
- `go build ./cmd/orqestra` compiles.
- From the run detail view, the lower pane shows parsed JSONL tool-use and text lines for the selected step.
- Pressing `j`/`k` to switch steps reloads the lower pane with the new step's log.
- `Ctrl+E` opens the JSONL file in the system editor.

### 7. Add TUI tests for runs list and detail views

**Steps:**

1. In `internal/tui/app_test.go`, add `TestTUI_RunsListNavigation`: create a temp `.orqestra/sessions/` dir with 2 fake runs (each with `prompt.md` + a `gateway_meta.json`). Create a `testModel()`, set `m.runs` to the result of `agent.ListRuns(tmpDir)`, set `m.state = StateRunsList`. Assert `m.View()` contains the prompt text. Send `↓` key, assert cursor moved. Send `Esc`, assert `m.state == StatePrompt`.
2. Add `TestTUI_RunDetailLayout_ThreeZones`: create a `testModel()` with `m.state = StateRunDetail`, a pre-populated `m.runDetail` (with `.Prompt`, `.PlanMarkdown`, and `.Steps`), and `m.runLogLines` set to a few sample lines. Assert `m.View()` contains: (a) the prompt text, (b) the `⇩` separator, (c) plan content, (d) step names in the right column, (e) log lines in the lower pane.
3. Add `TestTUI_RunDetail_KeyNavigation`: pre-populate a model in `StateRunDetail` with 3 steps. Send `j` key, assert `m.runStepCursor` increments. Send `k`, assert it decrements. Send `↓`, assert `runLogVP` scrolls (verify via `runLogVP.YOffset`). Send `PgDn`, assert `runDetailVP` scrolls.
4. Add `TestTUI_CtrlH_FromPrompt`: verify `Ctrl+H` transitions to `StateRunsList`. Verify it's a no-op when `m.state == StatePipeline` and `m.content != ContentCompletion`.

**Done when:**

- `go test ./internal/tui/...` passes with the new test cases.
- Test coverage exists for: runs list render, 3-zone detail layout render, keyboard routing (arrow vs j/k vs PgUp/PgDn), state transitions, edge case of empty runs list, edge case of step with no session log.

## Verification

Commands the worker runs after ALL packages complete to confirm success:

- `go test ./...`
- `go build ./cmd/orqestra`
- `go vet ./...`

## Assumptions

1. Runs list is accessed via `Ctrl+H` from the prompt screen and from `ContentCompletion`. Not available mid-pipeline (would be confusing while agents are running).
2. `Ctrl+E` in the run detail view opens the JSONL with `open` (macOS). Cross-platform support is out of scope.
3. The upper-left pane in run detail always shows: the original prompt → unicode `⇩⇩⇩` separator → `final_plan.md` content (rendered markdown). This does NOT change when switching steps — it's the run's "input → output" story. The step selection only affects the lower raw-log pane.
4. Runs list is loaded synchronously from disk on `Ctrl+H`. If the directory doesn't exist, show `"No runs found. Run a pipeline first."` as empty state.
5. The lower log pane renders JSONL content inline via `ParseSessionLog()`. Only `assistant` events are shown. `tool_use` blocks render as dim tool name + blue detail (reusing existing activity log styles). `text` blocks render as dim truncated one-liners prefixed with `╶`. This keeps the pane scannable without full-text rendering.
6. `StepMeta` (the exported version of the per-step JSON) goes in `internal/agent/session.go` alongside `SessionDir`, `RunSummary`, and `RunDetail`, since it's domain data about sessions — not harness infrastructure.
7. Sandbox runs store JSONL in `~/.claude/projects/` keyed by the sandbox's `repoPath` (the git worktree or original repo path), not the orchestrator's CWD. `ResolveSessionLogPath` uses `os.Getwd()` which matches the orchestrator's CWD — this is correct because even sandbox runners set `cmd.Dir` to `repoPath` which equals the orchestrator's repo root. If a future change uses git worktrees as sandbox CWDs, the JSONL path will diverge and `ResolveSessionLogPath` will need an explicit `repoPath` parameter instead of `os.Getwd()`.

## Gotchas

1. **Phase 1 and 1b are DONE** — the draft plan was written against stale code. `--include-partial-messages`, `stream_event` handling, `extractAssistantToolUses()`, `renderActivityLog()`, and all activity styles are already implemented. The worker must NOT touch these files for those purposes.
2. **`writeArtifactJSON` exists but is never called** — it's at line 658 of `orchestrator.go`. The worker should use it for step metadata rather than writing a new JSON serialization helper.
3. **Gateway has multiple exit paths** — there are at least 5 places in the gateway coaching loop where `EventAgentDone` is emitted (accept on first try, accept in auto-approve mode, skip, max-rounds fallback, normal coaching accept). Each needs a `stepMeta` write. Missing one means some runs will have incomplete metadata.
4. **`viewAgentHistory()` is a stub** — it currently says `"(output history not captured in this mode)"`. The runs detail view is a different feature (historical runs, not live agent history), but the worker should NOT try to "fix" `viewAgentHistory()` as part of this plan.
5. **`recalculateLayout()` only handles `StatePrompt` and `StatePipeline`** — adding `StateRunsList`/`StateRunDetail` requires new branches in the `switch m.state` block AND in `inputHeight` calculation. If the worker forgets the `inputHeight` branch, the layout constants will be wrong and the viewport will be the wrong size.
6. **`handleKey()` dispatches on `m.state`** — the worker must add `StateRunsList` and `StateRunDetail` cases, or keypresses in those states will fall through to the default (which is nothing).
7. **The `Model` uses value semantics** (Bubble Tea convention) — all state mutations happen by reassigning `m.field = value` and returning the model. The worker must not introduce pointer receivers or shared mutation for the new runs state.
8. **Session dir names include timestamp** — `2006-01-02-150405` format. `ListRuns` must parse this format to sort by time. The `slug` portion after the timestamp is optional and may contain arbitrary text (from the gateway brief task summary or the raw prompt).
9. **`os.Getwd()` in TUI context** — when the TUI calls `ListRuns`, the CWD should be the repo root where `.orqestra/sessions/` lives. This is the same CWD the orchestrator uses to create `SessionDir`. The worker should not hardcode paths.
10. **JSONL parser is now in scope** — `ParseSessionLog()` in `internal/harness/logpath.go` reads `~/.claude/projects/.../<session>.jsonl`, filters to `assistant` events, and renders `tool_use` and `text` content blocks as one-line display strings. The parser reuses `ToolDetail()` from `output.go` for tool detail extraction. It must handle: empty files (return nil), files still being written (read what's available), and malformed JSON lines (skip silently with `slog.Debug`).
11. **The lower log pane uses arrow keys, upper pane uses PgUp/PgDn** — these keybindings must not conflict. `↑`/`↓` are dedicated to `runLogVP` scrolling. `j`/`k` switch the selected step. `PgUp`/`PgDn` scroll `runDetailVP`. The worker must NOT route `↑`/`↓` to step navigation — that's `j`/`k`.
12. **`constRunLogHeight` is a new layout constant** — add it to `layout.go` alongside the existing `const` block. Value = 8 (matching the `maxShow = 8` pattern in `renderActivityLog`). The upper zone height is derived as `contentHeight - constRunLogHeight - 1` (1 for the divider between zones).
