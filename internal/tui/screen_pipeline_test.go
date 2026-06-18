package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// seedStreamingModel returns a Model in a live streaming state with n stream
// lines already buffered and the content viewport synced to the bottom.
func seedStreamingModel(t *testing.T, n int) Model {
	t.Helper()
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.pipelineScreen.goal = "scroll test"

	stream := orchestrator.NewStreamRing(n + 50)
	m.pipelineScreen.streamBuf = stream
	stream.SetAgent("researcher")
	for i := 1; i <= n; i++ {
		stream.AppendText(fmt.Sprintf("stream line %03d\n", i))
	}
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()
	if !m.pipelineScreen.contentVP.AtBottom() {
		t.Fatal("expected fresh streaming viewport to follow the bottom")
	}
	return m
}

// TestPipelineScroll_KeyScrollAndTickStability covers the scroll-stability
// invariant: PgUp scrolls off the bottom, and a later stream tick must not yank
// a user-scrolled viewport back down.
func TestPipelineScroll_KeyScrollAndTickStability(t *testing.T) {
	m := seedStreamingModel(t, 100)

	res, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = res.(Model)
	if m.pipelineScreen.contentVP.AtBottom() {
		t.Fatal("expected viewport to leave the bottom after PgUp")
	}
	scrolled := m.pipelineScreen.contentVP.YOffset()

	// New content arrives and a tick fires SyncViewports.
	m.pipelineScreen.streamBuf.AppendText("stream line 101\n")
	res, _ = m.Update(tickMsg(time.Now()))
	m = res.(Model)

	if m.pipelineScreen.contentVP.AtBottom() {
		t.Error("tick yanked a user-scrolled viewport back to the bottom")
	}
	if got := m.pipelineScreen.contentVP.YOffset(); got != scrolled {
		t.Errorf("tick moved user scroll position: got %d want %d", got, scrolled)
	}
}

// TestPipelineScroll_MouseWheel covers mouse-wheel routing — a regression guard
// against Model.handleMouse reverting to a no-op.
func TestPipelineScroll_MouseWheel(t *testing.T) {
	m := seedStreamingModel(t, 100)

	before := m.pipelineScreen.contentVP.YOffset()
	res, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = res.(Model)
	after := m.pipelineScreen.contentVP.YOffset()

	if after >= before {
		t.Errorf("expected wheel-up to scroll the content viewport up: before=%d after=%d", before, after)
	}
}

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
	s := NewPipelineScreen("test")
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

// The streaming body now carries the FULL history; the viewport (not a slice)
// decides what is visible. Following the bottom shows the newest lines and clips
// the oldest, but the full history stays retained for scrollback.
func TestViewStreaming_ViewportRetainsFullHistoryAndClips(t *testing.T) {
	s := setupTestPipelineScreen()
	s.content = ContentStreaming
	s.active = true
	s.RecalculateLayout(120, 6) // small content zone forces clipping
	s.SyncViewports()

	if !strings.Contains(s.lastContentBody, "Read") || !strings.Contains(s.lastContentBody, "Bash") {
		t.Errorf("expected activity names in content, got %s", s.lastContentBody)
	}
	// Full history retained (oldest line is in the content, just scrolled off).
	if !strings.Contains(s.lastContentBody, "stream line 01") {
		t.Errorf("expected full stream history retained in viewport content")
	}
	// Following the bottom: newest line visible, oldest line clipped out of window.
	view := s.contentVP.View()
	if !strings.Contains(view, "stream line 16") {
		t.Errorf("expected newest line visible at bottom, got:\n%s", view)
	}
	if strings.Contains(view, "stream line 01") {
		t.Errorf("expected oldest line clipped by the viewport window, got:\n%s", view)
	}
}

func TestViewStreaming_FilePathsAreFullPaths(t *testing.T) {
	s := setupTestPipelineScreen()

	out := s.viewStreaming(120)

	if !strings.Contains(out, "file1.txt") {
		t.Errorf("expected relative path to remain visible, got %s", out)
	}
	if !strings.Contains(out, "file:///test/dir/file1.txt") {
		t.Errorf("expected absolute OSC 8 URI, got %s", out)
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
	s := NewPipelineScreen("test")
	s.content = ContentEditConfirm
	s.awaitingPlanDecision = true
	s.hasPlan = true
	s.finalPlan = "# Original Plan"
	s.pendingEditContent = "# Modified Plan"
	s.editConfirmCursor = 0
	s.hasEditComment = false
	return s
}

func TestEditConfirm_YesWithComment(t *testing.T) {
	s := setupEditConfirmScreen()

	// Press Tab to open comment textarea
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !s.hasEditComment {
		t.Fatal("expected hasEditComment to be true after Tab")
	}

	// Type comment
	s.editConfirmComment.SetValue("Fixed imports")

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
	if s.pendingEditContent != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.pendingEditContent)
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
	if s.pendingEditContent != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.pendingEditContent)
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
	if s.pendingEditContent != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.pendingEditContent)
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
	s := NewPipelineScreen("test")
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
		f := s.viewFooter()
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

// --- Status Bar Tests ---

func TestViewStatusLine_Empty(t *testing.T) {
	s := PipelineScreen{}
	out := s.viewStatusLine(80)
	if out != "" {
		t.Errorf("expected empty status line with no agents, got: %q", out)
	}
}

func TestViewStatusLine_ConfigName(t *testing.T) {
	s := PipelineScreen{configName: "orqestra.yaml"}
	out := s.viewStatusLine(80)
	if !strings.Contains(out, "orqestra.yaml") {
		t.Errorf("expected config name in status line, got: %q", out)
	}
}

func TestViewStatusLine_AgentChain(t *testing.T) {
	s := PipelineScreen{
		active: true,
		agents: []AgentRow{
			{ID: "researcher", State: AgentStateDone},
			{ID: "architect", State: AgentStateDone},
			{ID: "worker", State: AgentStateRunning, ModelDisplay: "claude-opus-4", ContextWindow: 200000},
		},
		liveInput:  12000,
		liveOutput: 8000,
		liveStart:  time.Now().Add(-30 * time.Second),
	}
	out := s.viewStatusLine(120)

	if !strings.Contains(out, "✓rese") {
		t.Errorf("expected ✓rese for done researcher, got: %q", out)
	}
	if !strings.Contains(out, "✓arch") {
		t.Errorf("expected ✓arch for done architect, got: %q", out)
	}
	if !strings.Contains(out, "▶work") {
		t.Errorf("expected ▶work for running worker, got: %q", out)
	}
	if !strings.Contains(out, "claude-opus-4") {
		t.Errorf("expected model name in status line, got: %q", out)
	}
	if !strings.Contains(out, "↑12k") {
		t.Errorf("expected input tokens ↑12k, got: %q", out)
	}
	if !strings.Contains(out, "↓8.0k") {
		t.Errorf("expected output tokens ↓8.0k, got: %q", out)
	}
}

func TestViewStatusLine_Truncation(t *testing.T) {
	s := PipelineScreen{
		active: true,
		agents: []AgentRow{
			{ID: "researcher", State: AgentStateDone},
			{ID: "architect", State: AgentStateDone},
			{ID: "critic", State: AgentStateDone},
			{ID: "worker", State: AgentStateRunning, ModelDisplay: "claude-opus-4", ContextWindow: 200000},
		},
		liveInput:  50000,
		liveOutput: 30000,
		liveStart:  time.Now().Add(-60 * time.Second),
	}
	out := s.viewStatusLine(40)

	if out == "" {
		t.Error("expected non-empty status line even at narrow width")
	}
}

func TestViewStatusLine_ShimmerCycles(t *testing.T) {
	s := PipelineScreen{
		active:    true,
		agents:    []AgentRow{{ID: "worker", State: AgentStateRunning}},
		liveStart: time.Now(),
	}

	results := map[string]bool{}
	for i := 0; i < 5; i++ {
		s.animFrame = i
		out := s.viewStatusLine(80)
		results[out] = true
	}
	if len(results) != 1 {
		t.Errorf("expected stable status bar, got %d unique outputs", len(results))
	}
}

func TestFormatTokenCompact(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{9999, "10.0k"},
		{10000, "10k"},
		{123456, "123k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		got := formatTokenCompact(tt.input)
		if got != tt.want {
			t.Errorf("formatTokenCompact(%d) = %q, want %q", tt.input, got, tt.want)
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
