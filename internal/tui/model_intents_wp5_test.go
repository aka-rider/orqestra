package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// TestProcessIntent_SubmitQuestionAnswer_RoutesQuestionAnswerIntent is the
// WP5/J25 gate proof, ported to the event bus: answering a question through
// the real key-driven resolveQuestion path must deliver a
// QuestionAnswerIntent (correlated by QuestionID/Answer.ID) on
// handle.Intents, and a LATER, unrelated event must never re-open the
// identical, already-answered question. Under the pre-WP10 ObsStore
// snapshot-diffing design this needed an explicit ClearQuestion call to
// avoid the next stream event's snapshot re-deriving "still pending"
// (screen_pipeline_snapshot.go's `snap.HasQuestion && !QuestionOpen()`); the
// event bus makes this a non-issue by construction — onQuestionAsked only
// ever fires from a genuinely NEW EventQuestionAsked, never re-derived from
// an unrelated event.
func TestProcessIntent_SubmitQuestionAnswer_RoutesQuestionAnswerIntent(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true
	m.pipelineScreen.content = ContentStreaming

	intents := make(chan orchestrator.Intent, 4)
	m.intents = intents

	m = applyEvent(m, orchestrator.EventQuestionAsked{ToolCall: mcp.ToolCall{ID: "q-1", Question: "Pick one?"}})
	if !m.pipelineScreen.chat.QuestionOpen() {
		t.Fatal("setup: expected the question open before answering")
	}

	// Answer it through the real key path (Escape → Skipped, mirroring
	// TestUserQuestion_HandleCtrlCCancel_EmitsSkipIntent's shape but via the
	// full Model, since processIntent is what's under test here).
	ps, cmd := m.pipelineScreen.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.pipelineScreen = ps
	if ps.chat.QuestionOpen() {
		t.Fatal("setup: expected the question closed by the real key path before processIntent")
	}
	intent := ps.PendingIntent
	m.pipelineScreen.PendingIntent = nil
	_ = cmd

	qaIntent, ok := intent.(SubmitQuestionAnswerIntent)
	if !ok {
		t.Fatalf("expected SubmitQuestionAnswerIntent from the key path, got %T", intent)
	}

	_, sendCmd := m.processIntent(qaIntent, nil)
	if sendCmd == nil {
		t.Fatal("expected a non-nil cmd delivering the QuestionAnswerIntent")
	}
	sendCmd() // sendIntent's Cmd — invoke synchronously, as bubbletea would.

	select {
	case in := <-intents:
		qa, ok := in.(orchestrator.QuestionAnswerIntent)
		if !ok {
			t.Fatalf("expected QuestionAnswerIntent, got %T", in)
		}
		if qa.QuestionID != qaIntent.Answer.ID || qa.Answer.ID != qaIntent.Answer.ID {
			t.Errorf("expected QuestionID/Answer.ID = %q, got %+v", qaIntent.Answer.ID, qa)
		}
	default:
		t.Fatal("expected a QuestionAnswerIntent on m.intents")
	}

	// A later, unrelated event (e.g. a stream delta) must never resurrect the
	// answered question.
	m2 := applyEvent(m, orchestrator.EventDelta{AgentID: "architect", Text: "still working"})
	if m2.pipelineScreen.chat.QuestionOpen() {
		t.Error("question re-opened after answering and a later event — J25 regression")
	}
}
