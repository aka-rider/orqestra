package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
)

func testRunSummaries() []agent.RunSummary {
	return []agent.RunSummary{
		{
			Timestamp: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
			Slug:      "second-run",
			Path:      "/tmp/sessions/2026-05-10-120000-second-run",
			Prompt:    "Fix the bug in auth module",
			Status:    "done",
			Duration:  3 * time.Minute,
		},
		{
			Timestamp: time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC),
			Slug:      "first-run",
			Path:      "/tmp/sessions/2026-05-09-100000-first-run",
			Prompt:    "Add feature X",
			Status:    "failed",
			Duration:  5 * time.Minute,
		},
	}
}

func testRunDetail() agent.RunDetail {
	return agent.RunDetail{
		RunSummary: agent.RunSummary{
			Timestamp: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
			Slug:      "second-run",
			Path:      "/tmp/sessions/2026-05-10-120000-second-run",
			Prompt:    "Fix the bug in auth module",
			Status:    "done",
			Duration:  3 * time.Minute,
		},
		Steps: []agent.StepMeta{
			{AgentID: "gateway", StartTime: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 5, 10, 12, 0, 10, 0, time.UTC), Status: "done", InputTokens: 500, OutputTokens: 200},
			{AgentID: "researcher", StartTime: time.Date(2026, 5, 10, 12, 0, 10, 0, time.UTC), EndTime: time.Date(2026, 5, 10, 12, 1, 0, 0, time.UTC), Status: "done", InputTokens: 2000, OutputTokens: 1000, ClaudeSessionID: "sess-abc"},
			{AgentID: "worker", StartTime: time.Date(2026, 5, 10, 12, 1, 0, 0, time.UTC), EndTime: time.Date(2026, 5, 10, 12, 3, 0, 0, time.UTC), Status: "done", InputTokens: 5000, OutputTokens: 3000, ClaudeSessionID: "sess-def"},
		},
		PlanMarkdown: "# Plan\n\n## Goal\nFix auth bug.\n\n## Work Packages\n\n### 1. Fix\nDo the fix.",
		WorkerOutput: "done",
		Validation:   "✅ pass",
	}
}

func TestTUI_RunsListNavigation(t *testing.T) {
	m := testModel()
	m.runsListScreen.SetRuns(testRunSummaries())
	m.state = StateRunsList
	m.recalculateLayout()
	m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width)

	// Verify view contains prompt text
	view := m.View()
	if !strings.Contains(view, "Fix the bug") {
		t.Error("runs list view should contain first run's prompt")
	}
	if !strings.Contains(view, "Add feature X") {
		t.Error("runs list view should contain second run's prompt")
	}
	if !strings.Contains(view, "Runs History") {
		t.Error("runs list view should contain header")
	}

	// Press down to move cursor
	result, _ := sendKey(m, tea.KeyDown)
	model := result.(Model)
	if model.runsListScreen.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", model.runsListScreen.cursor)
	}

	// Press up to move back
	result, _ = sendKey(model, tea.KeyUp)
	model = result.(Model)
	if model.runsListScreen.cursor != 0 {
		t.Errorf("expected cursor at 0 after up, got %d", model.runsListScreen.cursor)
	}

	// j/k also work
	result, _ = sendRune(m, "j")
	model = result.(Model)
	if model.runsListScreen.cursor != 1 {
		t.Errorf("expected cursor at 1 after j, got %d", model.runsListScreen.cursor)
	}

	result, _ = sendRune(model, "k")
	model = result.(Model)
	if model.runsListScreen.cursor != 0 {
		t.Errorf("expected cursor at 0 after k, got %d", model.runsListScreen.cursor)
	}

	// Esc returns to prompt
	result, _ = sendKey(m, tea.KeyEsc)
	model = result.(Model)
	if model.state != StatePrompt {
		t.Errorf("expected StatePrompt after Esc, got %d", model.state)
	}
}

func TestTUI_RunsListEnterLoadsDetail(t *testing.T) {
	// Create a real temp session dir with metadata so LoadRunDetail works
	tmp := t.TempDir()
	sessDir := filepath.Join(tmp, ".orqestra", "sessions", "2026-05-10-120000-test-run")
	os.MkdirAll(sessDir, 0o755)
	os.WriteFile(filepath.Join(sessDir, "prompt.md"), []byte("Test prompt"), 0o644)
	os.WriteFile(filepath.Join(sessDir, "final_plan.md"), []byte("# Plan\n\n## Goal\nTest\n\n## Work Packages\n\n### 1. Do"), 0o644)

	meta := agent.StepMeta{
		AgentID:   "gateway",
		StartTime: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 12, 0, 10, 0, time.UTC),
		Status:    "done",
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(sessDir, "gateway_meta.json"), data, 0o644)

	m := testModel()
	m.runsListScreen.SetRuns([]agent.RunSummary{{
		Timestamp: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		Slug:      "test-run",
		Path:      sessDir,
		Prompt:    "Test prompt",
		Status:    "done",
	}})
	m.state = StateRunsList
	m.recalculateLayout()

	// Press Enter
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.state != StateRunDetail {
		t.Errorf("expected StateRunDetail, got %d", model.state)
	}
	if model.runDetailScreen.detail.Prompt != "Test prompt" {
		t.Errorf("expected prompt 'Test prompt', got %q", model.runDetailScreen.detail.Prompt)
	}
	if len(model.runDetailScreen.detail.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(model.runDetailScreen.detail.Steps))
	}
}

func TestTUI_RunDetailLayout_ThreeZones(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	m.runDetailScreen.SetDetail(testRunDetail())
	m.runDetailScreen.logLines = []string{
		"  Read go.mod",
		"  Bash go test ./...",
		"  ╶ I found the issue.",
	}
	m.recalculateLayout()
	m.runDetailScreen.SyncViewports()

	view := m.View()

	// Upper-left should contain prompt
	if !strings.Contains(view, "Fix the bug") {
		t.Error("detail view should contain the prompt text")
	}

	// Should contain the separator
	if !strings.Contains(view, "⇩") {
		t.Error("detail view should contain the ⇩ separator")
	}

	// Right column should contain step names
	if !strings.Contains(view, "gateway") {
		t.Error("detail view should contain 'gateway' step")
	}
	if !strings.Contains(view, "researcher") {
		t.Error("detail view should contain 'researcher' step")
	}
	if !strings.Contains(view, "worker") {
		t.Error("detail view should contain 'worker' step")
	}

	// Lower zone should contain log lines
	if !strings.Contains(view, "Read go.mod") {
		t.Error("detail view should contain log lines")
	}
}

func TestTUI_RunDetail_KeyNavigation(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	m.runDetailScreen.SetDetail(testRunDetail())
	m.runDetailScreen.logLines = []string{"line1", "line2"}
	m.recalculateLayout()

	// j moves step cursor down
	result, _ := sendRune(m, "j")
	model := result.(Model)
	if model.runDetailScreen.stepCursor != 1 {
		t.Errorf("expected step cursor 1 after j, got %d", model.runDetailScreen.stepCursor)
	}

	// k moves step cursor up
	result, _ = sendRune(model, "k")
	model = result.(Model)
	if model.runDetailScreen.stepCursor != 0 {
		t.Errorf("expected step cursor 0 after k, got %d", model.runDetailScreen.stepCursor)
	}

	// Esc returns to runs list
	result, _ = sendKey(m, tea.KeyEsc)
	model = result.(Model)
	if model.state != StateRunsList {
		t.Errorf("expected StateRunsList after Esc, got %d", model.state)
	}
}

func TestTUI_CtrlR_FromPrompt(t *testing.T) {
	m := testModel()
	m.state = StatePrompt
	m.recalculateLayout()

	result, _ := sendKey(m, tea.KeyCtrlR)
	model := result.(Model)

	// Should transition to runs list (may be empty, but state should change)
	if model.state != StateRunsList {
		t.Errorf("expected StateRunsList after Ctrl+R, got %d", model.state)
	}
}

func TestTUI_CtrlR_NotDuringPipeline(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming // mid-pipeline

	// Ctrl+R should be a no-op during active pipeline (no handler for it)
	result, _ := sendKey(m, tea.KeyCtrlR)
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected to stay in StatePipeline, got %d", model.state)
	}
}

func TestTUI_RunsListEmpty(t *testing.T) {
	m := testModel()
	m.state = StateRunsList
	m.recalculateLayout()
	m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width)

	view := m.View()
	if !strings.Contains(view, "No runs found") {
		t.Error("empty runs list should show 'No runs found' message")
	}

	// Enter on empty list should be a no-op
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)
	if model.state != StateRunsList {
		t.Errorf("expected to stay in StateRunsList, got %d", model.state)
	}
}

func TestTUI_RunStepNoSessionID(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	m.runDetailScreen.SetDetail(agent.RunDetail{
		RunSummary: agent.RunSummary{Status: "done"},
		Steps: []agent.StepMeta{
			{AgentID: "gateway", Status: "done"}, // no ClaudeSessionID
		},
	})
	m.recalculateLayout()
	m.runDetailScreen.LoadStepLog()

	if len(m.runDetailScreen.logLines) == 0 {
		t.Fatal("expected at least one log line")
	}
	if !strings.Contains(m.runDetailScreen.logLines[0], "no agent log") {
		t.Errorf("expected 'no agent log' placeholder, got %q", m.runDetailScreen.logLines[0])
	}
}

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"done", "✓"},
		{"failed", "✗"},
		{"", "○"},
		{"unknown", "○"},
	}
	for _, tt := range tests {
		got := statusIcon(tt.status)
		if got != tt.want {
			t.Errorf("statusIcon(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, ""},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{5 * time.Minute, "5m0s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
