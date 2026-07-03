package tui

import (
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)


func TestFileHyperlink_AbsolutePath(t *testing.T) {
	path := "/Users/dev/internal/model.go"
	out := fileHyperlink(path, "/Users/default")

	if !strings.Contains(out, path) {
		t.Errorf("expected output to contain '%s' as visible text, got %q", path, out)
	}
	if !strings.Contains(out, "file:///Users/dev/internal/model.go") {
		t.Errorf("expected URI to be absolute, got %q", out)
	}
}

func TestFileHyperlink_RelativePath(t *testing.T) {
	path := "internal/model.go"
	cwd := "/Users/dev"
	out := fileHyperlink(path, cwd)

	if !strings.HasSuffix(out, "\033\\internal/model.go\033]8;;\033\\") {
		t.Errorf("expected output to end with visible text '%s', got %q", path, out)
	}
	if !strings.Contains(out, "file:///Users/dev/internal/model.go") {
		t.Errorf("expected URI to be absolute based on cwd, got %q", out)
	}
}

func setupTestPipelineScreen() PipelineScreen {
	s := NewPipelineScreen("test", runeUI{}, keymap.Default())
	s.cwd = "/test/dir"
	s.agents = []AgentRow{
		{
			ID: "researcher", State: AgentStateDone, Elapsed: time.Second, InputTokens: 100, OutputTokens: 50,
			Activities: []toolActivity{{Tool: "Read", Detail: "file1.txt"}, {Tool: "Bash", Detail: "ls -l"}},
		},
		{ID: "architect", State: AgentStateRunning, StartedAt: time.Now()},
	}
	return s
}

func TestViewStreaming_FilePathsAreFullPaths(t *testing.T) {
	line := formatActivityLine("Read", "file1.txt", "/test/dir")

	if !strings.Contains(line, "file1.txt") {
		t.Errorf("expected relative path to remain visible, got %s", line)
	}
	if !strings.Contains(line, "file:///test/dir/file1.txt") {
		t.Errorf("expected absolute OSC 8 URI, got %s", line)
	}
}

// TestApplyEvent_DeltaGoesToTimeline verifies that an EventDelta reaches the
// active TurnGroup's live tail and appears in the view.
func TestApplyEvent_DeltaGoesToTimeline(t *testing.T) {
	s := PipelineScreen{
		timeline:      NewTimeline(keymap.Default(), timelineStyles{selectionBg: selectionBg}),
		agentRowIndex: make(map[string]int),
	}
	s.timeline.SetRect(image.Rect(0, 0, 80, 20))
	tg := frame.NewTurnGroup()
	s.currentTurn = tg
	s.timeline.SetTail(tg)

	s.ApplyEvent(orchestrator.EventDelta{AgentID: "researcher", Text: "hello world"}, 80)

	if !s.timeline.HasContent() {
		t.Fatal("expected timeline to have content (active tail)")
	}
	if !strings.Contains(s.timeline.View(), "hello world") {
		t.Error("expected 'hello world' in the brief header after the delta")
	}
}

// The producer resolves tools in the TurnGroup: a tool-use adds a pending entry
// and the matching result resolves it in place via ResolveTool.
func TestPipeline_ToolResolvedByProducer(t *testing.T) {
	s := PipelineScreen{
		timeline:      NewTimeline(keymap.Default(), timelineStyles{selectionBg: selectionBg}),
		agentRowIndex: make(map[string]int),
	}
	s.timeline.SetRect(image.Rect(0, 0, 80, 20))
	tg := frame.NewTurnGroup()
	s.currentTurn = tg
	s.timeline.SetTail(tg)

	s.ApplyEvent(orchestrator.EventToolCall{AgentID: "researcher", Tool: "Read", Detail: "foo.go"}, 80)
	s.ApplyEvent(orchestrator.EventToolResult{AgentID: "researcher", IsError: false}, 80)

	if len(s.pendingTools) != 0 {
		t.Errorf("expected the pending tool resolved, %d left", len(s.pendingTools))
	}
	// The TurnGroup is the tail; its rows include the resolved tool row.
	if v := s.timeline.View(); !strings.Contains(v, "✓") {
		t.Errorf("expected the resolved tool ✓ in the view:\n%s", v)
	}
}

func TestViewCompletion_ShowsAgentSummary(t *testing.T) {
	s := setupTestPipelineScreen()

	out := s.viewCompletion(120)

	if !strings.Contains(out, "researcher") || !strings.Contains(out, "architect") {
		t.Errorf("expected agents in completion summary, got %s", out)
	}

	if !strings.Contains(out, "file1.txt") {
		t.Errorf("expected file activity in completion summary, got %s", out)
	}
}

func setupEditConfirmScreen() PipelineScreen {
	s := NewPipelineScreen("test", runeUI{}, keymap.Default())
	s.content = ContentEditConfirm
	s.awaitingPlanDecision = true
	s.hasPlan = true
	s.finalPlan = "# Original Plan"
	s.editConfirm = newEditConfirm("# Modified Plan")
	return s
}

func TestEditConfirm_YesWithComment(t *testing.T) {
	s := setupEditConfirmScreen()

	// Press Tab to open comment textarea
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !s.editConfirm.hasComment {
		t.Fatal("expected hasEditComment to be true after Tab")
	}

	// Type comment
	s.editConfirm.comment.SetValue("Fixed imports")

	// Press Enter to confirm
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	intent, ok := s.PendingIntent.(ConfirmEditIntent)
	if !ok {
		t.Fatalf("expected ConfirmEditIntent, got %T", s.PendingIntent)
	}
	if intent.EditedContent != "# Modified Plan" {
		t.Errorf("expected EditedContent %q, got %q", "# Modified Plan", intent.EditedContent)
	}
	if intent.Comment != "Fixed imports" {
		t.Errorf("expected Comment %q, got %q", "Fixed imports", intent.Comment)
	}
	if intent.AutoApprove {
		t.Errorf("expected AutoApprove=false when comment is non-empty")
	}
	if s.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %d", s.content)
	}
}

func TestEditConfirm_YesNoComment(t *testing.T) {
	s := setupEditConfirmScreen()

	// Press Enter directly (cursor=0 means Yes)
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	intent, ok := s.PendingIntent.(ConfirmEditIntent)
	if !ok {
		t.Fatalf("expected ConfirmEditIntent, got %T", s.PendingIntent)
	}
	if intent.EditedContent != "# Modified Plan" {
		t.Errorf("expected EditedContent %q, got %q", "# Modified Plan", intent.EditedContent)
	}
	if intent.Comment != "" {
		t.Errorf("expected empty Comment, got %q", intent.Comment)
	}
	if !intent.AutoApprove {
		t.Errorf("expected AutoApprove=true when comment is empty")
	}
	if s.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %d", s.content)
	}
	if s.editConfirm.pending != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.editConfirm.pending)
	}
}

func TestEditConfirm_No(t *testing.T) {
	s := setupEditConfirmScreen()

	// Move cursor to No
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	// Press Enter
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if s.PendingIntent != nil {
		t.Errorf("expected no PendingIntent, got %T", s.PendingIntent)
	}
	if !s.awaitingPlanDecision {
		t.Error("expected the gate to reopen (awaitingPlanDecision) after discard")
	}
	if s.editConfirm.pending != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.editConfirm.pending)
	}
	if s.finalPlan != "# Original Plan" {
		t.Errorf("expected finalPlan unchanged, got %q", s.finalPlan)
	}
}

func TestEditConfirm_EscapeReturns(t *testing.T) {
	s := setupEditConfirmScreen()

	// Press Escape
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if s.PendingIntent != nil {
		t.Errorf("expected no PendingIntent, got %T", s.PendingIntent)
	}
	if !s.awaitingPlanDecision {
		t.Error("expected the gate to reopen (awaitingPlanDecision) after discard")
	}
	if s.editConfirm.pending != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.editConfirm.pending)
	}
	if s.finalPlan != "# Original Plan" {
		t.Errorf("expected finalPlan unchanged, got %q", s.finalPlan)
	}
}

func setupUserQuestionScreen(multi bool) PipelineScreen {
	q := mcp.ToolCall{
		Question:    "Pick one",
		MultiSelect: multi,
		Options: []mcp.ToolOption{
			{Label: "Yes", Hint: "and..."},
			{Label: "No", Hint: "because..."},
		},
	}
	s := NewPipelineScreen("test", runeUI{}, keymap.Default())
	s.chat.OpenQuestion(q, 80) // content stays ContentStreaming; the chat hosts the question
	return s
}

func TestUserQuestion_HandleCtrlCCancel_EmitsSkipIntent(t *testing.T) {
	s := setupUserQuestionScreen(false)
	s = s.HandleCtrlCCancel()
	if s.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %v", s.content)
	}
	intent, ok := s.PendingIntent.(SubmitQuestionAnswerIntent)
	if !ok {
		t.Fatalf("expected SubmitQuestionAnswerIntent, got %T", s.PendingIntent)
	}
	if !intent.Answer.Skipped {
		t.Errorf("expected Skipped:true")
	}
}

// Answering a question posts the answered question to the timeline as a frame
// (the original-request "AskUserQuestion (Answered) is a new Frame -> timeline").
func TestUserQuestion_AnswerPostsFrameToTimeline(t *testing.T) {
	s := setupUserQuestionScreen(false)
	s.timeline.SetRect(image.Rect(0, 0, 80, 20))

	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // move to "No"
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm

	if s.content != ContentStreaming {
		t.Fatalf("expected ContentStreaming after answering, got %v", s.content)
	}
	view := s.timeline.View()
	if !strings.Contains(view, "Pick one") || !strings.Contains(view, "No") {
		t.Errorf("expected the answered question on the timeline, got:\n%s", view)
	}
}

func TestUserQuestion_HandleCtrlCCancel_ClosesInlineEditor(t *testing.T) {
	s := setupUserQuestionScreen(false)
	// Open the inline editor by sending Tab through the component.
	s.chat.question, _ = s.chat.question.Update(tea.KeyPressMsg{Code: tea.KeyTab})

	s = s.HandleCtrlCCancel()

	intent, ok := s.PendingIntent.(SubmitQuestionAnswerIntent)
	if !ok || !intent.Answer.Skipped {
		t.Errorf("expected skipped intent, got %#v", s.PendingIntent)
	}
	if s.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %v", s.content)
	}
}

func TestUserQuestion_TabHintRendered(t *testing.T) {
	s := setupUserQuestionScreen(false)
	out := s.chat.question.View(80)
	if !strings.Contains(out, "Tab") || !strings.Contains(out, "add context") {
		t.Errorf("expected Tab hint in render, got:\n%s", out)
	}
}

func TestUserQuestion_FooterIncludesTab(t *testing.T) {
	for _, multi := range []bool{false, true} {
		s := setupUserQuestionScreen(multi)
		f := s.viewFooter(false)
		if !strings.Contains(f, "[Tab] add context") {
			t.Errorf("multi=%v: expected [Tab] add context in footer, got: %s", multi, f)
		}
		if !strings.Contains(f, "[Enter] confirm") {
			t.Errorf("multi=%v: expected [Enter] confirm in footer, got: %s", multi, f)
		}
	}
}

func TestUserQuestion_MultiSelectToggleVisible(t *testing.T) {
	s := setupUserQuestionScreen(true)
	s, _ = s.Update(tea.KeyPressMsg{Text: " "})

	out := s.chat.question.View(80)
	if !strings.Contains(out, "[x]") {
		t.Errorf("expected toggled option to render [x], got:\n%s", out)
	}
	if !strings.Contains(out, "[ ]") {
		t.Errorf("expected un-toggled option to render [ ], got:\n%s", out)
	}
}

// --- Agent summary tests (replaced the removed live status bar) ---

func TestAgentSummaryLine(t *testing.T) {
	meta := orchestrator.AgentMeta{ModelDisplay: "qwen3.6"}
	got := agentSummaryLine("Done:", "✓", "architect", meta, 236000, 456000, 3*time.Minute+28*time.Second)
	for _, want := range []string{"Done:", "✓", "architect", "(qwen3.6)", "↑236k", "↓456k", "3m28s"} {
		if !strings.Contains(got, want) {
			t.Errorf("agentSummaryLine = %q, want substring %q", got, want)
		}
	}

	// Falls back to ModelRef when ModelDisplay is empty; omits elapsed when zero.
	meta2 := orchestrator.AgentMeta{ModelRef: "opus"}
	if got := agentSummaryLine("Failed:", "✗", "worker", meta2, 0, 0, 0); !strings.Contains(got, "(opus)") {
		t.Errorf("expected ModelRef fallback (opus), got %q", got)
	}
}

// TestApplyEvent_AppendsDoneSummary verifies the end-of-agent summary line is
// appended to the transcript (with real tokens) when an agent transitions to done.
func TestApplyEvent_AppendsDoneSummary(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.width = 120
	m.height = 40
	m.recalculateLayout() // sets the timeline rect so appended rows are built

	meta := orchestrator.AgentMeta{ModelDisplay: "qwen3.6"}
	m = applyEvent(m, orchestrator.EventAgentStarted{AgentID: "architect", Meta: meta})
	m = applyEvent(m, orchestrator.EventAgentDone{AgentID: "architect", Usage: harness.TokenUsage{Input: 236000, Output: 456000}})

	out := m.pipelineScreen.timeline.View()
	for _, want := range []string{"Done:", "architect", "qwen3.6", "236k", "456k"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q after agent done; got:\n%s", want, out)
		}
	}
}

// TestApplyEvent_TokenTotalsAccumulateAcrossPasses is the J40 gate proof:
// the architect agent identity is started and completed TWICE in one run
// (deliberation, then a revise round) — the second EventAgentStarted must
// NOT reset the row's accumulated token totals, and the second
// EventAgentDone must ADD its usage onto the first pass's, not overwrite it.
//
// RED-first proof (quoted verbatim in the WP10 report): with onAgentDone
// changed to assign (row.InputTokens = usage.Input) instead of accumulate
// (row.InputTokens += usage.Input) — mirroring the pre-WP10
// ObsStore.AgentStarted bug of constructing a brand-new agentEntry on every
// AgentStarted call, discarding prior usage — this test failed with
// "expected accumulated InputTokens=300, got 200" (the second pass's usage
// alone, first pass's 100 lost). Restoring the += fix makes it pass again.
func TestApplyEvent_TokenTotalsAccumulateAcrossPasses(t *testing.T) {
	s := PipelineScreen{agentRowIndex: make(map[string]int)}

	s.ApplyEvent(orchestrator.EventAgentStarted{AgentID: "architect"}, 80)
	s.ApplyEvent(orchestrator.EventAgentDone{AgentID: "architect", Usage: harness.TokenUsage{Input: 100, Output: 50}}, 80)

	// Second pass for the SAME agent identity (e.g. a revise round).
	s.ApplyEvent(orchestrator.EventAgentStarted{AgentID: "architect"}, 80)
	s.ApplyEvent(orchestrator.EventAgentDone{AgentID: "architect", Usage: harness.TokenUsage{Input: 200, Output: 100}}, 80)

	row := s.ensureAgentRow("architect")
	if row.InputTokens != 300 {
		t.Errorf("expected accumulated InputTokens=300, got %d", row.InputTokens)
	}
	if row.OutputTokens != 150 {
		t.Errorf("expected accumulated OutputTokens=150, got %d", row.OutputTokens)
	}
	// Only ONE row exists for this agent identity — passes accumulate onto
	// it rather than each creating a fresh entry.
	if len(s.agents) != 1 {
		t.Errorf("expected exactly 1 agent row across both passes, got %d", len(s.agents))
	}
}

// TestApplyEvent_SkipsEmptyTool verifies a tool call with an empty Detail is
// never recorded as an activity (matches the pre-WP10 ring's behavior of
// dropping empty-detail tool entries).
func TestApplyEvent_SkipsEmptyTool(t *testing.T) {
	s := PipelineScreen{agentRowIndex: make(map[string]int)}

	s.ApplyEvent(orchestrator.EventToolCall{AgentID: "researcher", Tool: "Read", Detail: ""}, 80)
	s.ApplyEvent(orchestrator.EventToolCall{AgentID: "researcher", Tool: "Bash", Detail: "ls -la"}, 80)

	row := s.ensureAgentRow("researcher")
	if len(row.Activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(row.Activities))
	}
	if row.Activities[0].Tool != "Bash" {
		t.Fatalf("expected Bash tool, got %s", row.Activities[0].Tool)
	}
}
