package tui

import (
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// TestProcessIntent_SubmitQuestionAnswer_ClearsQuestion is the WP5/J25 gate
// proof: answering a question must clear it from the ObsStore snapshot.
// Without this, the next stream event's snapshot still reports the
// (already-answered) question as pending, and the pipeline screen's
// ApplySnapshot re-opens the identical question forever
// (screen_pipeline_snapshot.go: snap.HasQuestion && !chat.QuestionOpen()).
func TestProcessIntent_SubmitQuestionAnswer_ClearsQuestion(t *testing.T) {
	obs := orchestrator.NewObsStore()
	obs.UserQuestion(mcp.ToolCall{ID: "q-1", Question: "Pick one?"})

	if !obs.Snapshot().HasQuestion {
		t.Fatal("setup: expected HasQuestion true before answering")
	}

	m := testModel()
	m.obs = obs

	// Answer the question — mirrors what PipelineScreen.resolveQuestion feeds
	// into processIntent after the user confirms.
	m.processIntent(SubmitQuestionAnswerIntent{Answer: mcp.Answer{ID: "q-1", FreeformText: "yes"}}, nil)

	// A subsequent, unrelated stream event must not resurrect the question.
	obs.Stream("architect", harness.Event{Kind: harness.EventChunk, Text: "still working", IsDelta: true})

	snap := obs.Snapshot()
	if snap.HasQuestion {
		t.Errorf("HasQuestion = true after answering and a later stream event — "+
			"the answered question re-opens forever (J25); UserQuestion = %+v", snap.UserQuestion)
	}
}
