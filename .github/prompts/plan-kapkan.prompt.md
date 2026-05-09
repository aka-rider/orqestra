# Plan

## Goal

Restore the human plan-approval gate (currently regressed — pipeline runs straight through to worker), upgrade the plan review UX to use glamour-rendered markdown in a scrollable viewport with comment-driven replanning and external editor support, fix the broken `stream-json` parser so tool-use and incremental text actually stream, and upgrade the activity bar to a multi-line log.

## Context

**Verified via spot-checks:**

- `internal/orchestrator/orchestrator.go:460-480` — `planGate:` label emits `EventGateRequest{GatePlanApproval}` when `!input.AutoApprove`. The `for` loop blocks on `<-decisions`. `TestEngine_PlanApprovalGate` passes with mocks.
- `internal/tui/model.go:473` — `startPipeline()` creates `orchestrator.Input` with `AutoApprove` unset (defaults to `false`). The interactive path should trigger the gate.
- `cmd/orqestra/main.go:210` — `slog.SetDefault(io.Discard)` during TUI mode silences all debug output, making the regression invisible.
- `internal/harness/claude_cli.go:193` — `RunStreaming` args: `[]string{"-p", prompt, "--output-format", "stream-json", "--verbose"}`. No `--include-partial-messages`.
- `internal/harness/claude_cli.go:241-255` — parser switch: `"assistant"` → `extractAssistantText()` (text only), `"content_block_delta"` → delta text, `"content_block_start"` → tool use. The latter two never fire without `--include-partial-messages`.
- `internal/harness/claude_cli.go:383-450` — `streamEvent` struct has fields: `Type`, `Delta`, `Message`, `ContentBlock`, `Usage`, `SessionID`. No `Event` field for `stream_event` wrapper.
- `internal/harness/claude_cli.go:407` — `extractAssistantText()` parses `message.content[].type=="text"` only, ignores `tool_use` blocks.
- `internal/tui/model.go:966-1000` — `renderActivityBar()` renders activities as a single horizontal line with `⚡` icons.
- `internal/tui/styles.go:53-67` — `activityIconStyle` (color 3, faint), `activityToolStyle` (color 14, faint, bold), `activityDetailStyle` (color 244, faint), `activitySepStyle` (color 240).
- `internal/agent/session.go:19-51` — `SessionDir` creates dirs under `.orqestra/sessions/<timestamp>-<slug>/`, provides `WriteArtifact(name, []byte)` and `ReadArtifact(name)`.
- `go.mod` — `bubbletea v1.3.10`, `bubbles v1.0.0`, `lipgloss v1.1.0`. No `glamour`. `tea.ExecProcess` confirmed present in v1.3.10.
- `internal/tui/model.go:1043-1058` — `viewPlanReview()` renders raw markdown line-by-line with truncation. No glamour, no formatting.
- `internal/orchestrator/orchestrator.go:460-480` — `DecisionComment` handler calls `planner.RefineWithCommentsStreaming()`, re-emits gate. But `writeArtifact(session, "final_plan.md", ...)` only happens AFTER the gate loop exits (line 485). Plan file doesn't exist when the gate renders.
- The TUI never sends `DecisionComment` — no `Enter`-to-comment keybinding exists in `handlePlanReviewKey()`.

## Constraints

- Must not break headless mode (`--prompt --auto-approve`). All gate changes guarded by `!input.AutoApprove`.
- Must not change the orchestrator's event types or `Decision` enum values — existing tests depend on them.
- Must not introduce import cycles between `tui` and `orchestrator` packages.
- `View()` must remain pure (no state mutation) per Bubble Tea architecture rules.
- No `time.Sleep` in tests.
- External editor keybinding is `Ctrl+E` (not `Cmd+Shift+E` — terminal apps cannot detect `Cmd`).

## Risks

- **Glamour rendering width** — glamour adds padding/margins. If content width exceeds viewport width, horizontal overflow wraps poorly. Mitigation: pass `width-4` to `WithWordWrap()`, verified by rendering a test plan and measuring output width. Ensure glamour behaves nicely together with the native bubble tea editor: if the content overflows vertically, the editor/viewport must render scrollbars (or percentage indicators), and enforce strict boundaries to prevent horizontal layout breaking.
- **`tea.ExecProcess` and alt screen** — suspending TUI for external editor requires the program to be created with `tea.WithAltScreen()` (confirmed in `tui.go:15`). Bubble Tea handles save/restore. Risk is low.
- **Stream buffer contention during replanning** — when `DecisionComment` triggers a re-plan, the orchestrator resets the stream buffer via `stream.SetAgent("planner")`. The TUI's tick-based polling will pick up the new agent. No race: `StreamBuffer` is mutex-protected.

## Work Packages

### 1. Fix `stream-json` parser: add `--include-partial-messages` and handle `stream_event` wrapper

The parser expects `content_block_start` and `content_block_delta` as top-level event types. Without `--include-partial-messages`, Claude CLI never emits these — it only emits `assistant` (complete messages) and `result`. This means: zero incremental text streaming, zero tool-use activity detection.

**Steps:**

1. In `internal/harness/claude_cli.go:193`, change `RunStreaming` args from:

   ```go
   args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
   ```

   to:

   ```go
   args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
   ```

   Apply the same change to `RunContinue` at line 296:

   ```go
   args := []string{"--resume", sessionID, "-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
   ```

2. Add `Event json.RawMessage` field to `streamEvent` struct (line 383):

   ```go
   Event json.RawMessage `json:"event,omitempty"` // inner event for stream_event wrapper
   ```

3. Add a `case "stream_event"` to the scanner loop in `RunStreaming` (after line 255) and the identical loop in `RunContinue` (after line 350):

   ```go
   case "stream_event":
       if event.Event == nil {
           continue
       }
       var inner streamEvent
       if err := json.Unmarshal(event.Event, &inner); err != nil {
           continue
       }
       switch inner.Type {
       case "content_block_start":
           if sink, ok := stdout.(ActivitySink); ok {
               if name, args := inner.extractToolUse(); name != "" {
                   sink.OnToolUse(name, ToolDetail(name, args))
               }
           }
       case "content_block_delta":
           if inner.Delta.Text != "" {
               stdout.Write([]byte(inner.Delta.Text))
           }
       }
   ```

4. Update `extractAssistantText()` (line 407) to also extract tool-use blocks from `assistant` messages as a fallback, so complete assistant messages still surface tool activity:

   ```go
   // After the text extraction loop, add:
   if sink, ok := /* not available here — skip, handled at call site */; ok { ... }
   ```

   Actually, the call site in the `"assistant"` case (line 241) should also check for tool_use blocks. Add after the text write:

   ```go
   if sink, ok := stdout.(ActivitySink); ok {
       if tools := event.extractAssistantToolUses(); len(tools) > 0 {
           for _, tu := range tools {
               sink.OnToolUse(tu.Name, ToolDetail(tu.Name, tu.Input))
           }
       }
   }
   ```

   Add `extractAssistantToolUses()` method:

   ```go
   type toolUseBlock struct {
       Name  string
       Input json.RawMessage
   }

   func (e *streamEvent) extractAssistantToolUses() []toolUseBlock {
       if e.Message == nil { return nil }
       var msg struct {
           Content []struct {
               Type  string          `json:"type"`
               Name  string          `json:"name"`
               Input json.RawMessage `json:"input"`
           } `json:"content"`
       }
       if err := json.Unmarshal(e.Message, &msg); err != nil { return nil }
       var tools []toolUseBlock
       for _, block := range msg.Content {
           if block.Type == "tool_use" {
               tools = append(tools, toolUseBlock{Name: block.Name, Input: block.Input})
           }
       }
       return tools
   }
   ```

5. Add tests in `internal/harness/output_test.go`:
   - `TestStreamEventToolUse` — parse a `stream_event` wrapping `content_block_start` with `tool_use`, verify name and args.
   - `TestStreamEventTextDelta` — parse a `stream_event` wrapping `content_block_delta` with `text_delta`.
   - `TestAssistantToolUseFallback` — parse an `assistant` message containing `tool_use` blocks, verify `extractAssistantToolUses()` returns them.

**Done when:**

- `go test ./internal/harness/... -run TestStreamEvent -v` passes
- `go test ./internal/harness/... -run TestAssistantToolUseFallback -v` passes
- `go build ./cmd/orqestra` succeeds

---

### 2. Upgrade activity bar to multi-line activity log

Replace the cramped single-line `⚡ Read file  ⚡ Bash cmd` bar with a multi-line vertical log where each tool invocation gets its own line.

**Steps:**

1. In `internal/tui/styles.go`, update `activityToolStyle` and add `activityPathStyle`:

   ```go
   activityToolStyle = lipgloss.NewStyle().
       Foreground(lipgloss.Color("244")).
       Faint(true)
       // Remove .Bold(true) — tool name should be dim

   activityPathStyle = lipgloss.NewStyle().
       Foreground(lipgloss.Color("12"))
       // Blue, not faint — the filepath is the high-priority info
   ```

   Remove `activityIconStyle` and `activitySepStyle` (no longer used).

2. In `internal/tui/model.go`, rename `renderActivityBar` → `renderActivityLog` (line 966). Replace the implementation:

   ```go
   func renderActivityLog(activities []orchestrator.Activity, width int) string {
       const maxShow = 8
       start := 0
       if len(activities) > maxShow {
           start = len(activities) - maxShow
       }
       recent := activities[start:]

       var b strings.Builder
       maxLineWidth := width - 2
       for _, act := range recent {
           toolLabel := activityToolStyle.Render(fmt.Sprintf(" %-6s", act.Tool))
           detail := act.Detail
           if isFilePathTool(act.Tool) && detail != "" {
               detail = fileHyperlink(detail)
           }
           if act.Tool == "Bash" {
               b.WriteString(toolLabel + " " + activityDetailStyle.Render(detail))
           } else {
               b.WriteString(toolLabel + " " + activityPathStyle.Render(detail))
           }
           b.WriteString("\n")
       }
       return b.String()
   }
   ```

3. Update the call site in `viewStreaming` (line 938): change `renderActivityBar` → `renderActivityLog`.

4. Update `internal/tui/app_test.go` if any test references the old activity bar format.

**Done when:**

- `go test ./internal/tui/... -v` passes
- `go build ./cmd/orqestra` succeeds

---

### 3. Add glamour dependency and markdown rendering helper

**Steps:**

1. Run `go get github.com/charmbracelet/glamour@latest` from the repo root.

2. Create `internal/tui/markdown.go`:

   ```go
   package tui

   import "github.com/charmbracelet/glamour"

   // renderMarkdown renders a markdown string using glamour for styled terminal
   // output. Falls back to raw content on any rendering error.
   func renderMarkdown(content string, width int) string {
       r, err := glamour.NewTermRenderer(
           glamour.WithAutoStyle(),
           glamour.WithWordWrap(max(20, width-4)),
           glamour.WithEmoji(),
       )
       if err != nil {
           return content
       }
       out, err := r.Render(content)
       if err != nil {
           return content
       }
       return out
   }
   ```

   Note: we create a new renderer each call rather than caching, because width changes require a new renderer, and `View()` is called frequently but glamour rendering is only needed in `ContentPlanReview` mode (not hot path). If profiling shows this is a problem, cache by width in `Update()`.

3. Create `internal/tui/markdown_test.go`:

   ```go
   package tui

   import (
       "strings"
       "testing"
   )

   func TestRenderMarkdown(t *testing.T) {
       md := "# Hello\n\nThis is **bold** text.\n\n- item 1\n- item 2\n"
       out := renderMarkdown(md, 80)
       if out == md {
           t.Error("expected glamour to transform the markdown, got raw input back")
       }
       if !strings.Contains(out, "Hello") {
           t.Error("expected rendered output to contain 'Hello'")
       }
   }

   func TestRenderMarkdownFallback(t *testing.T) {
       // Empty content should not panic
       out := renderMarkdown("", 80)
       if out != "" && !strings.TrimSpace(out) == "" {
           // glamour may add whitespace to empty input — that's fine
       }
       _ = out
   }
   ```

**Done when:**

- `go test ./internal/tui/... -run TestRenderMarkdown -v` passes
- `go build ./cmd/orqestra` succeeds
- No import cycle: `internal/tui` → `glamour` (external, no cycle possible)

---

### 4. Plan file persistence before gate + run dir event

Save the plan markdown to the run directory BEFORE emitting the gate event so the TUI can reference the on-disk file for external editing.

**Steps:**

1. In `internal/orchestrator/orchestrator.go`, add a new event type after `EventError` (line 126):

   ```go
   EventRunDirReady // emitted once after session dir is created
   ```

   Add `RunDir string` field usage: the existing `Event.RunDir` field (already present, used in `EventComplete`) will also be set on `EventRunDirReady`.

2. Right after session creation (line 291, after `session, err = e.RunDirFactory("run")`), emit:

   ```go
   emit(Event{Type: EventRunDirReady, RunDir: session.Path})
   ```

3. Add `PlanFilePath string` field to `GateRequest` struct (line 149):

   ```go
   PlanFilePath string // absolute path to plan.md on disk (for external editor)
   ```

4. Move `writeArtifact(session, "final_plan.md", finalPlanMarkdown)` from line 485 (after the gate loop) to BEFORE the `EventGateRequest` emit inside the `planGate:` block. Place it at the top of the `for` loop body (line 461):

   ```go
   for {
       writeArtifact(session, "final_plan.md", finalPlanMarkdown)
       emit(Event{Type: EventGateRequest, Gate: GateRequest{
           Type:              GatePlanApproval,
           FinalPlanMarkdown: finalPlanMarkdown,
           PlanFilePath:      session.ArtifactPath("final_plan.md"),
       }})
       // ... rest of gate loop
   }
   ```

5. In the `DecisionEdit` handler (line 468), after updating `finalPlanMarkdown`, the `continue` will re-enter the loop which re-writes the file. Good.

6. In the `DecisionComment` handler (line 475), after `finalPlanMarkdown = revised.Markdown`, the `continue` will re-enter the loop which re-writes the file. Good.

7. Remove the duplicate `writeArtifact(session, "final_plan.md", finalPlanMarkdown)` that was at line 485 (now redundant since the loop writes on every iteration).

8. In `internal/tui/model.go`, add fields to `Model`:

   ```go
   runDir       string // set on EventRunDirReady
   planFilePath string // set on GatePlanApproval gate
   ```

9. In `handleOrchestratorEvent`, add a case for `EventRunDirReady`:

   ```go
   case orchestrator.EventRunDirReady:
       m.runDir = event.RunDir
   ```

   And in the `GatePlanApproval` handler, store the plan file path:

   ```go
   m.planFilePath = event.Gate.PlanFilePath
   ```

10. In `resetPipelineState()`, reset both fields:
    ```go
    m.runDir = ""
    m.planFilePath = ""
    ```

**Done when:**

- `go test ./internal/orchestrator/... -v` passes (existing tests still pass)
- `go build ./cmd/orqestra` succeeds

---

### 5. Rich plan review mode with glamour rendering, comment textarea, and external editor

This is the core UX upgrade. Replace raw text dump with rendered markdown in viewport, add comment-to-replan textarea, add external editor support.

**Steps:**

1. **Add comment textarea and editor state to Model** in `internal/tui/model.go`. Add fields near the existing `planEditor` fields:

   ```go
   planComment   textarea.Model // comment input for refining the plan
   hasPlanComment bool
   editorRunning  bool // true while external editor subprocess is active
   ```

2. **Create `internal/tui/editor.go`** — external editor support:

   ```go
   package tui

   import (
       "os"
       "os/exec"

       tea "github.com/charmbracelet/bubbletea"
   )

   type editorReturnMsg struct {
       err error
   }

   // openExternalEditor opens the given file in the user's preferred editor.
   // Uses tea.ExecProcess to suspend the TUI, run the editor, and resume.
   func openExternalEditor(path string) tea.Cmd {
       editor := os.Getenv("EDITOR")
       if editor == "" {
           editor = os.Getenv("VISUAL")
       }
       if editor == "" {
           editor = "open" // macOS fallback
       }
       c := exec.Command(editor, path)
       return tea.ExecProcess(c, func(err error) tea.Msg {
           return editorReturnMsg{err: err}
       })
   }
   ```

3. **Add `editorReturnMsg` handling** in `model.go` `Update()` method. Add a new case in the main `switch msg := msg.(type)` block (before the `tea.WindowSizeMsg` case):

   ```go
   case editorReturnMsg:
       m.editorRunning = false
       if msg.err != nil {
           m.lastErr = msg.err
           return m, nil
       }
       // Re-read the plan file from disk
       if m.planFilePath != "" {
           data, err := os.ReadFile(m.planFilePath)
           if err != nil {
               m.lastErr = fmt.Errorf("read plan after editor: %w", err)
               return m, nil
           }
           edited := string(data)
           if edited != m.finalPlan {
               m.finalPlan = edited
               if m.decisions != nil {
                   m.decisions <- orchestrator.Decision{
                       Type:          orchestrator.DecisionEdit,
                       EditedContent: edited,
                   }
               }
               m.content = ContentStreaming
               return m, waitForEvent(m.events)
           }
       }
       // No change — stay in plan review
       return m, nil
   ```

   Add `"os"` to the imports if not already present.

4. **Update `handlePlanReviewKey`** in `model.go` to add `Ctrl+E` (external editor), `Enter` (comment), and pass-through to comment textarea. Replace the current implementation:

   ```go
   func (m Model) handlePlanReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
       switch msg.Type {
       case tea.KeyCtrlE:
           if m.planFilePath != "" {
               m.editorRunning = true
               return m, openExternalEditor(m.planFilePath)
           }
           return m, nil
       case tea.KeyEnter:
           // Submit comment for replanning
           if m.hasPlanComment {
               comment := strings.TrimSpace(m.planComment.Value())
               if comment != "" {
                   if m.decisions != nil {
                       m.decisions <- orchestrator.Decision{
                           Type:    orchestrator.DecisionComment,
                           Comment: comment,
                       }
                   }
                   m.planComment.Reset()
                   m.content = ContentStreaming
                   return m, nil
               }
           }
           return m, nil
       }

       switch msg.String() {
       case "a", "A":
           if m.decisions != nil {
               m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionApprove}
           }
           m.content = ContentStreaming
           return m, nil
       case "e", "E":
           // Switch to plan edit mode (existing inline editor)
           contentWidth := max(1, int(float64(m.width)*splitRatio))
           contentHeight := max(4, m.height-constHeaderHeight-constPlanReviewInputHeight-constFooterHeight)
           ta := textarea.New()
           ta.SetWidth(max(1, contentWidth-2))
           ta.SetHeight(max(1, contentHeight-2))
           ta.CharLimit = 65536
           if m.hasPlan {
               ta.SetValue(m.finalPlan)
           }
           ta.Focus()
           m.planEditor = ta
           m.hasPlanEditor = true
           m.content = ContentPlanEdit
           return m, nil
       case "s", "S":
           if m.decisions != nil {
               m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionCancel}
           }
           return m, nil
       }

       // Pass all other keys to comment textarea
       if m.hasPlanComment {
           var cmd tea.Cmd
           m.planComment, cmd = m.planComment.Update(msg)
           return m, cmd
       }
       return m, nil
   }
   ```

5. **Update `viewPlanReview`** to use glamour rendering:

   ```go
   func (m Model) viewPlanReview(width int) string {
       if !m.hasPlan {
           return " Waiting for plan...\n"
       }
       // Ensure glamour behaves nicely together with native bubble tea editor
       // If the content overflows, editor/viewport must render scrollbars or scroll percentage
       rendered := renderMarkdown(m.finalPlan, width)
       m.contentVP.SetContent(rendered)
       return m.contentVP.View()
   }
   ```

6. **Add `constPlanReviewInputHeight`** to `internal/tui/layout.go`:

   ```go
   // Plan review mode: divider + 2-line comment textarea + 1-line padding
   constPlanReviewInputHeight = 4
   ```

7. **Update `recalculateLayout()`** in `layout.go` to use `constPlanReviewInputHeight` when in plan review:

   ```go
   case StatePipeline:
       if m.content == ContentPlanReview {
           inputHeight = constPlanReviewInputHeight
       } else {
           inputHeight = constPipelineInputHeight
       }
   ```

   Note: `recalculateLayout()` is called from `Update()`, and `m.content` is available. This is fine because we're reading state to compute layout, not mutating in `View()`.

8. **Initialize the comment textarea** when entering `ContentPlanReview`. In `handleOrchestratorEvent`, in the `GatePlanApproval` case (after setting `m.hasPlan = true`), add:

   ```go
   contentWidth := max(1, int(float64(m.width)*splitRatio))
   m.planComment = textarea.New()
   m.planComment.Placeholder = "Comment to refine the plan..."
   m.planComment.SetWidth(max(1, contentWidth-4))
   m.planComment.SetHeight(2)
   m.planComment.CharLimit = 1024
   m.planComment.Focus()
   m.hasPlanComment = true
   m.recalculateLayout()
   ```

9. **Update `viewInputZone`** for `ContentPlanReview` — show the comment textarea instead of static text:

   ```go
   case ContentPlanReview:
       if m.hasPlanComment {
           return m.planComment.View()
       }
       return keyStyle.Render(" [A] accept | [E] edit | [Ctrl+E] ext editor | [Enter] comment | [S] cancel")
   ```

10. **Update `viewFooter`** for `ContentPlanReview`:

    ```go
    case ContentPlanReview:
        return keyStyle.Render(" [A] accept | [E] edit | [Ctrl+E] ext editor | [Enter] comment | [S] cancel   [?] help  [^C^C] quit")
    ```

11. **Update sub-model delegation** in `Update()`. After the existing `ContentPlanEdit` delegation block (around line 365), add:

    ```go
    if m.state == StatePipeline && m.content == ContentPlanReview && m.hasPlanComment {
        var cmd tea.Cmd
        m.planComment, cmd = m.planComment.Update(msg)
        return m, cmd
    }
    ```

12. **Update `resetPipelineState()`** — add:
    ```go
    m.hasPlanComment = false
    m.editorRunning = false
    m.planFilePath = ""
    m.runDir = ""
    ```

**Done when:**

- `go test ./internal/tui/... -v` passes
- `go build ./cmd/orqestra` succeeds
- Manual: plan gate appears, plan is rendered with glamour formatting, comment textarea is visible and functional

---

### 6. Diagnose and fix the plan gate regression

The orchestrator correctly emits `EventGateRequest(GatePlanApproval)` (test passes). The TUI correctly handles it in `handleOrchestratorEvent`. The regression is likely a timing/event-ordering issue with real LLM runners.

**Steps:**

1. Add diagnostic logging that survives TUI mode. In `internal/tui/model.go`, add a file-based logger at the top of `handleOrchestratorEvent`:

   ```go
   func (m Model) handleOrchestratorEvent(event orchestrator.Event) (tea.Model, tea.Cmd) {
       slog.Debug("tui event", "type", event.Type, "phase", event.Phase, "agentID", event.AgentID)
       // ... existing code
   ```

   In `internal/tui/tui.go`, before silencing slog, set up a file logger to `~/.orqestra/tui.log`:

   ```go
   if f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), ".orqestra", "tui.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
       slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
   } else {
       slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
   }
   ```

   Ensure `~/.orqestra/` exists (create with `os.MkdirAll`).

2. Run the TUI manually against a real prompt. Check `~/.orqestra/tui.log` for event sequence. Look for:
   - Is `EventGateRequest` with `GatePlanApproval` present?
   - Is there an `EventPhaseChange` to `executing` that arrives before or simultaneously with the gate?
   - Is the events channel closing prematurely?

3. **Most likely root cause**: The events channel buffer (16) fills during the gateway+research+planning phase, and the orchestrator's `emit()` function blocks:

   ```go
   emit := func(ev Event) {
       select {
       case events <- ev:
       case <-ctx.Done():
       }
   }
   ```

   If the TUI is slow to drain events (e.g., spending time in `waitForEvent` → processing → calling `waitForEvent` again), the buffer fills and `emit` blocks. When the buffer clears, events arrive in burst, and `EventGateRequest` gets processed followed immediately by another event that overwrites the content mode.

   Fix: increase buffer from 16 to 64:

   ```go
   events := make(chan Event, 64)
   ```

4. Add an explicit guard: when the TUI receives `EventGateRequest(GatePlanApproval)`, set a flag `m.awaitingPlanDecision = true`. In `handleOrchestratorEvent`, ignore `EventPhaseChange` and `EventAgentStarted` events while `awaitingPlanDecision` is true (the orchestrator is blocked on the decision channel anyway, so these events shouldn't arrive — but if they do due to buffering, don't let them overwrite the gate view).

5. Write a regression test in `internal/tui/app_test.go`:

   ```go
   func TestTUI_PlanGateBlocksOverwrite(t *testing.T) {
       m := testModel()
       m.state = StatePipeline
       m.content = ContentStreaming
       m.events = make(chan orchestrator.Event, 1)

       // Simulate gate event
       result, _ := m.handleOrchestratorEvent(orchestrator.Event{
           Type: orchestrator.EventGateRequest,
           Gate: orchestrator.GateRequest{
               Type:              orchestrator.GatePlanApproval,
               FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
           },
       })
       model := result.(Model)

       if model.content != ContentPlanReview {
           t.Fatalf("expected ContentPlanReview, got %d", model.content)
       }

       // Simulate a stale EventPhaseChange arriving after the gate
       result2, _ := model.handleOrchestratorEvent(orchestrator.Event{
           Type:  orchestrator.EventPhaseChange,
           Phase: orchestrator.PhaseExecuting,
       })
       model2 := result2.(Model)

       // Gate must NOT be overwritten
       if model2.content != ContentPlanReview {
           t.Errorf("gate was overwritten by stale EventPhaseChange: content=%d", model2.content)
       }
   }
   ```

**Done when:**

- `go test ./internal/tui/... -run TestTUI_PlanGateBlocksOverwrite -v` passes
- `go test ./internal/orchestrator/... -v` passes (buffer change doesn't break existing tests)
- Diagnostic log confirms event sequence during manual run

---

### 7. Tests for new functionality

**Steps:**

1. In `internal/tui/app_test.go`, add:

   `TestTUI_PlanReviewComment` — simulate `ContentPlanReview` with a filled comment textarea, press Enter, assert `DecisionComment` is sent with correct comment text, and content switches to `ContentStreaming`.

   `TestTUI_PlanReviewExternalEditor` — simulate `ContentPlanReview` with `planFilePath` set, press `Ctrl+E`, assert returned `tea.Cmd` is not nil (the ExecProcess command). Cannot test editor return in unit test, but verify the command fires.

   `TestTUI_PlanReviewGlamour` — create model in `ContentPlanReview` with a sample markdown plan, call `viewPlanReview(80)`, assert the output is not identical to the raw input (glamour transformed it).

   `TestTUI_EditorReturn` — simulate `editorReturnMsg` with a temp file containing modified plan text, assert `m.finalPlan` is updated and `DecisionEdit` is sent.

2. In `internal/orchestrator/orchestrator_test.go`, add:

   `TestEngine_PlanFileBeforeGate` — use a `RunDirFactory` that creates a temp dir, start pipeline with `AutoApprove: false`, when `EventGateRequest(GatePlanApproval)` arrives, assert `final_plan.md` exists in the run dir with the correct content, then send `DecisionApprove`.

**Done when:**

- `go test ./internal/tui/... -v` passes
- `go test ./internal/orchestrator/... -v` passes
- `go vet ./...` clean

## Verification

After all work packages complete, run:

- `go test ./internal/harness/... -v -count=1`
- `go test ./internal/tui/... -v -count=1`
- `go test ./internal/orchestrator/... -v -count=1`
- `go vet ./...`
- `go build ./cmd/orqestra`

Manual verification:

1. Run `./orqestra` with a real prompt. After planner finishes, the plan gate must appear with glamour-rendered markdown.
2. Scroll the plan with mouse wheel and PgUp/PgDown.
3. Type a comment in the textarea and press Enter — planner re-runs, revised plan re-appears.
4. Press `E` — inline editor opens with raw markdown, `Ctrl+S` saves, gate re-appears with edits.
5. Press `Ctrl+E` — `$EDITOR` opens `final_plan.md`, save and close — TUI resumes and applies changes.
6. Press `A` — pipeline proceeds to worker execution.
7. During streaming, tool activities render as multi-line log (dim tool name, blue filepath).
8. Text streams incrementally (no multi-second delay between turns).

## Assumptions

- `--include-partial-messages` is supported by the version of `claude` CLI the user has installed. If not, the `stream_event` case simply never fires and the existing `assistant` fallback handles complete messages.
- `$EDITOR` is set in the user's environment. If unset, `open` (macOS) is used as fallback, which opens the `.md` file in the default GUI editor.
- The plan markdown always starts with `# Plan` (enforced by the orchestrator's `DecisionEdit` handler which rejects plans not starting with `# Plan`).

## Gotchas

1. **`recalculateLayout()` timing** — when entering `ContentPlanReview` from `handleOrchestratorEvent`, we must call `m.recalculateLayout()` because the input zone height changes from 2 to 4 lines. Missing this causes the content viewport to be 2 lines too tall, overlapping the input zone.

2. **Textarea focus vs key routing** — in `ContentPlanReview`, the comment textarea must receive key events (for typing), but `A`, `E`, `S` must be intercepted as commands, not typed into the textarea. The solution is checking `msg.Type` for special keys first, then `msg.String()` for single-char commands, then passing remaining keys to the textarea. Single-char commands `a/e/s` will be typed into the textarea — this is acceptable because the user types comments and presses Enter. To invoke approve/edit/cancel, they use uppercase `A/E/S` or the textarea must not have focus. **Alternative**: only route to textarea when it's focused; use `Tab` to toggle focus. This needs a design decision.

3. **`tea.ExecProcess` requires `tea.WithAltScreen()`** — confirmed present in `tui.go:15`. The TUI already uses alt screen, so ExecProcess will work correctly.

4. **Glamour rendering adds trailing newlines** — glamour adds `\n\n` at the end of rendered output. The viewport handles this fine, but if testing exact output, trim trailing whitespace.

5. **`writeArtifact` inside the gate loop** — every iteration (including after comment-driven replanning) overwrites `final_plan.md`. This is intentional: the on-disk file always reflects the latest plan version. The external editor reads the latest version.

**Design decision needed for gotcha #2:** The plan proposes checking `msg.String()` for uppercase `A/E/S` as commands vs lowercase going to textarea. But `Shift+A` produces "A" which would approve instead of typing "A". The cleaner approach: treat `Ctrl+A` as approve, `Ctrl+E` as external editor (already defined), keep `E` for inline edit (only works when textarea is empty or not focused). **Recommendation:** Use `Ctrl+A` for approve (consistent with other Ctrl bindings), `E` for edit only when comment textarea is empty, `Esc` for cancel. This avoids the shift-key ambiguity entirely. The worker should implement whichever the user confirms.
