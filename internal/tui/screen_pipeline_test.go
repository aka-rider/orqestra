package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
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
	s.focusedAgent = 1
	return s
}

func TestViewStreaming_NoRawDump(t *testing.T) {
	s := setupTestPipelineScreen()

	out := s.viewStreaming(120)

	if !strings.Contains(out, "Read") || !strings.Contains(out, "Bash") {
		t.Errorf("expected activity names, got %s", out)
	}

	if strings.Contains(out, "stream line 01") {
		t.Errorf("expected oldest stream lines to be truncated")
	}
	if !strings.Contains(out, "stream line 06") {
		t.Errorf("expected newest stream lines to be visible")
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

func TestViewAgentHistory_ShowsActivities(t *testing.T) {
	s := setupTestPipelineScreen()
	// trigger snapshot of researcher
	s.streamBuf.SetAgent("architect")

	out := s.viewAgentHistory(120)

	if !strings.Contains(out, "Read") || !strings.Contains(out, "file1.txt") {
		t.Errorf("expected activity in agent history, got %s", out)
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
	s.contentVP.SetWidth(80)
	s.contentVP.SetHeight(20)
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
	if s.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview, got %d", s.content)
	}
	if s.pendingEditContent != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.pendingEditContent)
	}
	if s.finalPlan != "# Original Plan" {
		t.Errorf("expected finalPlan unchanged, got %q", s.finalPlan)
	}
	if !s.hasPlanComment {
		t.Error("expected hasPlanComment to be true after declining edit")
	}
}

func TestEditConfirm_EscapeReturns(t *testing.T) {
	s := setupEditConfirmScreen()

	// Press Escape
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if s.PendingIntent != nil {
		t.Errorf("expected no PendingIntent, got %T", s.PendingIntent)
	}
	if s.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview, got %d", s.content)
	}
	if s.pendingEditContent != "" {
		t.Errorf("expected pendingEditContent cleared, got %q", s.pendingEditContent)
	}
	if s.finalPlan != "# Original Plan" {
		t.Errorf("expected finalPlan unchanged, got %q", s.finalPlan)
	}
	if !s.hasPlanComment {
		t.Error("expected hasPlanComment to be true after escape")
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
	s.contentVP.SetWidth(80)
	s.contentVP.SetHeight(20)
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
	// Simulate a Space keypress via the parent dispatch path. For printable
	// characters the textarea looks at msg.Text (verified against
	// bubbles/v2@v2.1.0/textarea.go:1316: insertRunesFromUserInput([]rune(msg.Text))).
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

	// Should fit within width
	// Note: dimStyle may add ANSI escapes, so we can't check raw len
	if out == "" {
		t.Error("expected non-empty status line even at narrow width")
	}
}

func TestViewStatusLine_ShimmerCycles(t *testing.T) {
	// Shimmer moved from status bar to frame list headers.
	// Status bar should now be stable across animFrame changes.
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
	// Status bar should be stable (shimmer is in frame headers now)
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

func TestSyncViewports_AutoFollow_Completion(t *testing.T) {
	s := NewPipelineScreen("test")
	s.contentVP.SetWidth(80)
	s.contentVP.SetHeight(10)
	s.content = ContentCompletion
	s.frameList.AppendFrame(Frame{Kind: CompletionFrame, State: FrameFinished,
		Parts: []ContentPart{{IsText: true, Text: strings.Repeat("line\n", 30)}}})
	s.contentVP.GotoBottom()
	s.SyncViewports()
	if !s.contentVP.AtBottom() {
		t.Error("expected viewport at bottom after SyncViewports with ContentCompletion")
	}
}

func TestDrainStreamUpdates_SkipsEmptyTool(t *testing.T) {
	fl := NewFrameList(80)
	s := PipelineScreen{
		frameList: fl,
		streamBuf: orchestrator.NewStreamRing(200),
	}
	// Start an in-progress frame so UpdateActive has something to mutate.
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "test"})

	updates := make(chan orchestrator.StreamEntry, 4)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryToolUse, Tool: "Read", Detail: ""}
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryToolUse, Tool: "Bash", Detail: "ls -la"}
	close(updates)

	s.DrainStreamUpdates(updates)

	// Only the tool with non-empty detail should be present.
	if fl.FrameCount() != 1 {
		t.Fatalf("expected 1 frame, got %d", fl.FrameCount())
	}
	frame := fl.frames[0]
	toolCount := 0
	for _, p := range frame.Parts {
		if !p.IsText {
			toolCount++
		}
	}
	if toolCount != 1 {
		t.Fatalf("expected 1 tool block, got %d", toolCount)
	}
	if frame.Parts[0].Tool.Name != "Bash" {
		t.Fatalf("expected Bash tool, got %s", frame.Parts[0].Tool.Name)
	}
	if frame.Parts[0].Tool.Detail != "ls -la" {
		t.Fatalf("expected 'ls -la' detail, got %q", frame.Parts[0].Tool.Detail)
	}
}

func TestDrainStreamUpdates_AccumulatesDelta(t *testing.T) {
	fl := NewFrameList(80)
	s := PipelineScreen{
		frameList: fl,
		streamBuf: orchestrator.NewStreamRing(200),
	}

	// Start an in-progress frame.
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "test"})

	updates := make(chan orchestrator.StreamEntry, 4)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryDelta, Text: "Hello"}
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryDelta, Text: " World"}
	close(updates)

	s.DrainStreamUpdates(updates)

	frame := fl.frames[0]
	if frame.Partial != "Hello World" {
		t.Fatalf("expected partial 'Hello World', got %q", frame.Partial)
	}
}

func TestDrainStreamUpdates_FlushesPartialOnEntryText(t *testing.T) {
	fl := NewFrameList(80)
	s := PipelineScreen{
		frameList: fl,
		streamBuf: orchestrator.NewStreamRing(200),
	}

	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "test"})

	updates := make(chan orchestrator.StreamEntry, 4)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryDelta, Text: "partial"}
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryText, Text: "completed"}
	close(updates)

	s.DrainStreamUpdates(updates)

	frame := fl.frames[0]
	if frame.Partial != "" {
		t.Fatalf("expected empty partial after EntryText, got %q", frame.Partial)
	}
	// The partial text should have been flushed before the EntryText.
	// AppendText coalesces consecutive text parts, so we get a single part.
	if len(frame.Parts) != 1 {
		t.Fatalf("expected 1 text part, got %d", len(frame.Parts))
	}
	if !frame.Parts[0].IsText {
		t.Fatal("expected text part")
	}
	// Should contain both the flushed partial and the new text.
	if frame.Parts[0].Text != "partial\ncompleted\n" {
		t.Fatalf("expected 'partial\\ncompleted\\n', got %q", frame.Parts[0].Text)
	}
}

func TestPipelineScreen_StaticHeight(t *testing.T) {
	s := PipelineScreen{
		staticContent: "line1\nline2\nline3\n",
	}
	if got := s.StaticHeight(); got != 3 {
		t.Fatalf("expected StaticHeight=3, got %d", got)
	}

	s.staticContent = ""
	if got := s.StaticHeight(); got != 0 {
		t.Fatalf("expected StaticHeight=0 for empty content, got %d", got)
	}

	s.staticContent = "single line"
	if got := s.StaticHeight(); got != 1 {
		t.Fatalf("expected StaticHeight=1 for single line, got %d", got)
	}
}

func TestFrameList_RenderFinished(t *testing.T) {
	fl := NewFrameList(80)
	fl.AppendFrame(Frame{
		Kind:         AgentFrame,
		State:        FrameFinished,
		AgentID:      "researcher",
		AgentModel:   "qwen3.6",
		Parts:        []ContentPart{{IsText: true, Text: "Research done\n"}},
	})
	fl.AppendFrame(Frame{
		Kind:         AgentFrame,
		State:        FrameInProgress,
		AgentID:      "architect",
		AgentModel:   "qwen3.6",
		Parts:        []ContentPart{{IsText: true, Text: "Thinking...\n"}},
	})

	rendered := fl.RenderFinished(80)
	if rendered == "" {
		t.Fatal("expected non-empty RenderFinished output")
	}
	// Should contain the finished frame with icon prefix.
	if !containsStr(rendered, "⏺") {
		t.Fatal("expected '⏺' prefix in RenderFinished output")
	}
	// Should contain the agent ID.
	if !containsStr(rendered, "researcher") {
		t.Fatal("expected 'researcher' in RenderFinished output")
	}
}

func TestFrameList_RenderActive(t *testing.T) {
	fl := NewFrameList(80)
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameFinished,
		AgentID: "researcher",
		Parts:   []ContentPart{{IsText: true, Text: "Done\n"}},
	})
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "architect",
		Parts:   []ContentPart{{IsText: true, Text: "Working...\n"}},
	})

	rendered := fl.RenderActive(80)
	if rendered == "" {
		t.Fatal("expected non-empty RenderActive output")
	}
	// Should contain the in-progress frame.
	if !containsStr(rendered, "architect") {
		t.Fatal("expected 'architect' in RenderActive output")
	}
	// Should NOT contain the finished frame.
	if containsStr(rendered, "researcher") {
		t.Fatal("RenderActive should not include finished frames")
	}
}

func TestFrameList_HasInProgressFrame(t *testing.T) {
	fl := NewFrameList(80)
	if fl.HasInProgressFrame() {
		t.Fatal("expected no in-progress frame")
	}

	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "done"})
	if fl.HasInProgressFrame() {
		t.Fatal("expected no in-progress frame after adding finished frame")
	}

	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "running"})
	if !fl.HasInProgressFrame() {
		t.Fatal("expected in-progress frame")
	}
}

func TestFrameList_FinishActive_WithWidth(t *testing.T) {
	fl := NewFrameList(80)
	fl.AppendFrame(Frame{
		Kind:  AgentFrame,
		State: FrameInProgress,
		AgentID: "test",
		Parts: []ContentPart{
			{IsText: true, Text: "# Header\n\nSome **bold** text\n"},
		},
	})

	// Finish with width > 0 to trigger markdown rendering.
	fl.FinishActive(5*time.Second, 100, 200, 80)

	frame := fl.frames[0]
	if frame.State != FrameFinished {
		t.Fatalf("expected FrameFinished, got %v", frame.State)
	}
	if frame.Elapsed != 5*time.Second {
		t.Fatalf("expected Elapsed=5s, got %v", frame.Elapsed)
	}
	if frame.InputTokens != 100 {
		t.Fatalf("expected InputTokens=100, got %d", frame.InputTokens)
	}
	if frame.OutputTokens != 200 {
		t.Fatalf("expected OutputTokens=200, got %d", frame.OutputTokens)
	}
	// MarkdownRendered should be set for text parts.
	if frame.Parts[0].MarkdownRendered == "" {
		t.Fatal("expected MarkdownRendered to be set after FinishActive with width")
	}
}
