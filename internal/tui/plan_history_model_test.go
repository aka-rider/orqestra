package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

func buildLiveTestRepo(t *testing.T) (string, string) {
	t.Helper()
	return buildTestPlanHistoryRepo(t, []string{"first", "second"})
}

func TestProcessIntent_OpenPlanHistory_FromGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	dir, head := buildLiveTestRepo(t)
	out, cmd := m.processIntent(OpenPlanHistoryIntent{HistoryDir: dir, HeadSHA: head, ReadOnly: false}, nil)
	mm := out.(Model)
	if mm.pipelineScreen.content != ContentPlanHistory {
		t.Fatalf("expected content=ContentPlanHistory, got %v", mm.pipelineScreen.content)
	}
	if cmd == nil {
		t.Fatal("expected non-nil load command")
	}
}

func TestProcessIntent_OpenPlanHistory_ReadOnly(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	dir, head := buildLiveTestRepo(t)
	out, _ := m.processIntent(OpenPlanHistoryIntent{HistoryDir: dir, HeadSHA: head, ReadOnly: true}, nil)
	mm := out.(Model)
	if mm.state != StatePlanHistoryDetail {
		t.Fatalf("expected state=StatePlanHistoryDetail, got %v", mm.state)
	}
}

func TestProcessIntent_OpenPlanHistory_EmptyDirSetsLastErr(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	out, _ := m.processIntent(OpenPlanHistoryIntent{HistoryDir: "", ReadOnly: true}, nil)
	mm := out.(Model)
	if mm.lastErr == nil {
		t.Fatal("expected lastErr to be set on empty HistoryDir")
	}
	if mm.state != StateRunDetail {
		t.Errorf("state should not change on empty HistoryDir: got %v", mm.state)
	}
}

func TestProcessIntent_RevertPlan_SendsDecisionEditEmptyComment(t *testing.T) {
	m := testModel()
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	events := make(chan orchestrator.Event, 1)
	m.events = events
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanHistory
	m.pipelineScreen.awaitingPlanDecision = true

	out, _ := m.processIntent(RevertPlanIntent{Content: "REVERTED", ShortSHA: "abc1234"}, nil)
	mm := out.(Model)
	if mm.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected content=ContentStreaming after revert, got %v", mm.pipelineScreen.content)
	}
	if mm.pipelineScreen.awaitingPlanDecision {
		t.Error("awaitingPlanDecision should be false after revert")
	}
	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionEdit {
			t.Errorf("expected DecisionEdit, got %v", d.Type)
		}
		if d.EditedContent != "REVERTED" {
			t.Errorf("expected REVERTED, got %q", d.EditedContent)
		}
		if d.Comment != "" {
			t.Errorf("expected empty Comment (non-destructive revert), got %q", d.Comment)
		}
	default:
		t.Fatal("expected decision to be sent on the channel")
	}
}

func TestProcessIntent_ClosePlanHistory_FromGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanHistory
	out, _ := m.processIntent(ClosePlanHistoryIntent{}, nil)
	mm := out.(Model)
	if mm.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected content=ContentPlanReview after close, got %v", mm.pipelineScreen.content)
	}
	if mm.state != StatePipeline {
		t.Errorf("state should remain StatePipeline, got %v", mm.state)
	}
}

func TestProcessIntent_ClosePlanHistory_FromRunDetail(t *testing.T) {
	m := testModel()
	m.state = StatePlanHistoryDetail
	out, _ := m.processIntent(ClosePlanHistoryIntent{}, nil)
	mm := out.(Model)
	if mm.state != StateRunDetail {
		t.Errorf("expected state=StateRunDetail after close, got %v", mm.state)
	}
}

func TestHandleRunDetailKey_OpenPlanHistoryIntent_Forwarded(t *testing.T) {
	m := testModel()
	m.state = StateRunDetail
	dir, head := buildLiveTestRepo(t)
	// Stash an intent on the run detail screen — simulate it having been set
	// by the screen's Update.
	m.runDetailScreen.PendingIntent = OpenPlanHistoryIntent{HistoryDir: dir, HeadSHA: head, ReadOnly: true}

	// Send any key that won't trigger NavigateBack — the intent should be
	// drained on entry. Use Tab (RunDetailScreen.Update handles Tab safely
	// for empty step list and re-sets no intent).
	out, _ := m.handleRunDetailKey(tea.KeyPressMsg{Code: tea.KeyTab})
	mm := out.(Model)
	// The screen's own Update may overwrite PendingIntent for unknown keys,
	// but we explicitly route OpenPlanHistoryIntent through processIntent in
	// handleRunDetailKey, so state should have changed.
	if mm.state != StatePlanHistoryDetail {
		t.Errorf("expected state=StatePlanHistoryDetail after OpenPlanHistoryIntent forward, got %v", mm.state)
	}
}
