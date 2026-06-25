package tui

import (
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
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
	sb := orchestrator.NewStreamRing(50)
	sb.SetAgent("researcher")
	sb.AppendText("stream line 01\n")
	sb.AppendText("stream line 02\n")
	sb.AppendText("stream line 03\n")
	sb.AppendText("stream line 04\n")
	sb.AppendText("stream line 05\n")
	sb.AppendText("stream line 06\n")
	sb.AppendText("stream line 07\n")
	sb.AppendText("stream line 08\n")
	sb.AppendText("stream line 09\n")
	sb.AppendText("stream line 10\n")
	sb.AppendText("stream line 11\n")
	sb.AppendText("stream line 12\n")
	sb.AppendText("stream line 13\n")
	sb.AppendText("stream line 14\n")
	sb.AppendText("stream line 15\n")
	sb.AppendText("stream line 16\n") // > 15 lines to test preview

	sb.AppendActivity("Read", "file1.txt")
	sb.AppendActivity("Bash", "ls -l")

	s.SetStreamBuf(sb)
	s.agents = []AgentRow{
		{ID: "researcher", State: AgentStateDone, Elapsed: time.Second, InputTokens: 100, OutputTokens: 50},
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

// TestDrainStreamUpdates_TextLineGoesToTimeline verifies that a newline-terminated
// EntryText is promoted to the timeline as a static prose frame.
func TestDrainStreamUpdates_TextLineGoesToTimeline(t *testing.T) {
	s := PipelineScreen{
		streamBuf: orchestrator.NewStreamRing(200),
		timeline:  NewTimeline(keymap.Default(), timelineStyles{selectionBg: selectionBg}),
		knownAgents: make(map[string]string),
	}

	updates := make(chan orchestrator.StreamEntry, 2)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryText, Text: "hello world\n"}
	close(updates)

	s.DrainStreamUpdates(updates)

	// The completed line must appear in the timeline.
	if !s.timeline.HasContent() {
		t.Fatal("expected timeline to have content after ingesting a completed line")
	}
}

func TestViewCompletion_ShowsAgentSummary(t *testing.T) {
	s := setupTestPipelineScreen()
	s.streamBuf.SetAgent("architect") // Snapshot researcher

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
	if s.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate, got %d", s.content)
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
	if s.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate, got %d", s.content)
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
	s.content = ContentUserQuestion
	s.question = newUserQuestion(q, 80)
	s.hasQuestion = true
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
	s.question, _ = s.question.Update(tea.KeyPressMsg{Code: tea.KeyTab})

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
	out := s.question.View(80)
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

	out := s.question.View(80)
	if !strings.Contains(out, "[x]") {
		t.Errorf("expected toggled option to render [x], got:\n%s", out)
	}
	if !strings.Contains(out, "[ ]") {
		t.Errorf("expected un-toggled option to render [ ], got:\n%s", out)
	}
}

// --- Agent summary tests (replaced the removed live status bar) ---

func TestAgentSummaryLine(t *testing.T) {
	a := orchestrator.AgentSnapshot{
		AgentID: "architect",
		Meta:    orchestrator.AgentMeta{ModelDisplay: "qwen3.6"},
		Input:   236000,
		Output:  456000,
	}
	got := agentSummaryLine("Done:", "✓", a, 3*time.Minute+28*time.Second)
	for _, want := range []string{"Done:", "✓", "architect", "(qwen3.6)", "↑236k", "↓456k", "3m28s"} {
		if !strings.Contains(got, want) {
			t.Errorf("agentSummaryLine = %q, want substring %q", got, want)
		}
	}

	// Falls back to ModelRef when ModelDisplay is empty; omits elapsed when zero.
	a2 := orchestrator.AgentSnapshot{AgentID: "worker", Meta: orchestrator.AgentMeta{ModelRef: "opus"}}
	if got := agentSummaryLine("Failed:", "✗", a2, 0); !strings.Contains(got, "(opus)") {
		t.Errorf("expected ModelRef fallback (opus), got %q", got)
	}
}

// TestApplySnapshot_AppendsDoneSummary verifies the end-of-agent summary line is
// appended to the transcript (with real tokens) when an agent transitions to done.
func TestApplySnapshot_AppendsDoneSummary(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.width = 120
	m.height = 40
	m.recalculateLayout() // sets the timeline rect so appended rows are built

	meta := orchestrator.AgentMeta{ModelDisplay: "qwen3.6"}
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{{AgentID: "architect", Status: "running", Meta: meta}},
	}, m.width)
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{
			{AgentID: "architect", Status: "done", Meta: meta, Input: 236000, Output: 456000},
		},
	}, m.width)

	out := m.pipelineScreen.timeline.View()
	for _, want := range []string{"Done:", "architect", "qwen3.6", "236k", "456k"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q after agent done; got:\n%s", want, out)
		}
	}
}

func TestDrainStreamUpdates_SkipsEmptyTool(t *testing.T) {
	s := PipelineScreen{
		streamBuf: orchestrator.NewStreamRing(200),
	}

	updates := make(chan orchestrator.StreamEntry, 4)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryToolUse, Tool: "Read", Detail: ""}
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryToolUse, Tool: "Bash", Detail: "ls -la"}
	close(updates)

	s.DrainStreamUpdates(updates)

	// Only the tool with non-empty detail should be in the ring.
	_, _, _, activities := s.streamBuf.SnapshotText()
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Tool != "Bash" {
		t.Fatalf("expected Bash tool, got %s", activities[0].Tool)
	}
}
