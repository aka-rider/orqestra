package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// sendIntent returns a tea.Cmd that delivers in on ch. Wrapping the send in
// a Cmd (rather than sending synchronously inside Update) means even a send
// that DID block momentarily (e.g. an unusually full buffer) would only
// block this async goroutine, never the Bubble Tea event loop — plan
// WP10 step 7's "sends from Update wrapped in a tea.Cmd if they could ever
// block". ch is nil when no pipeline is running (no gate/question possible
// then either), so the nil-guard is defensive, not load-bearing.
func sendIntent(ch chan<- orchestrator.Intent, in orchestrator.Intent) tea.Cmd {
	return func() tea.Msg {
		if ch != nil {
			ch <- in
		}
		return nil
	}
}

// processIntent executes a pipeline screen intent and optionally batches with
// an additional command (e.g. the Ctrl+C timeout tick).
func (m Model) processIntent(intent tea.Msg, extraCmd tea.Cmd) (tea.Model, tea.Cmd) {
	batch := func(cmd tea.Cmd) tea.Cmd {
		if extraCmd != nil && cmd != nil {
			return tea.Batch(cmd, extraCmd)
		}
		if extraCmd != nil {
			return extraCmd
		}
		return cmd
	}
	switch i := intent.(type) {
	case SubmitQuestionAnswerIntent:
		return m, batch(sendIntent(m.intents, orchestrator.QuestionAnswerIntent{
			QuestionID: i.Answer.ID,
			Answer:     i.Answer,
		}))
	case ApprovePlanIntent:
		return m, batch(sendIntent(m.intents, orchestrator.GateDecisionIntent{
			GateID:   m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{Type: orchestrator.DecisionApprove},
		}))
	case ConfirmEditIntent:
		cmd := sendIntent(m.intents, orchestrator.GateDecisionIntent{
			GateID: m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{
				Type:          orchestrator.DecisionEdit,
				EditedContent: i.EditedContent,
				Comment:       i.Comment,
				AutoApprove:   i.AutoApprove,
			},
		})
		m.pipelineScreen.awaitingPlanDecision = false
		m.pipelineScreen.enterStreaming()
		return m, batch(cmd)
	case CommentPlanIntent:
		return m, batch(sendIntent(m.intents, orchestrator.GateDecisionIntent{
			GateID: m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: i.Comment,
			},
		}))
	case CancelPlanIntent:
		return m, batch(sendIntent(m.intents, orchestrator.GateDecisionIntent{
			GateID:   m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{Type: orchestrator.DecisionCancel},
		}))
	case CancelPipelineIntent:
		if m.cancelCause != nil {
			m.cancelCause(orchestrator.ErrUserCancelled)
		}
		return m, batch(nil)
	case NavigateToPromptIntent:
		m.pipelineScreen.Reset()
		m.state = StatePrompt
		m.promptScreen.Reset()
		if i.PreFillGoal != "" {
			m.promptScreen.SetValue(i.PreFillGoal)
		}
		return m, batch(nil)
	case NavigateToRunsListIntent:
		m.navigateToRunsList()
		return m, batch(nil)
	case ConfirmNewRunIntent:
		if m.cancelCause != nil {
			m.cancelCause(orchestrator.ErrUserCancelled)
		}
		goal := m.pipelineScreen.goal
		m.pipelineScreen.Reset()
		m.state = StatePrompt
		m.promptScreen.Reset()
		if goal != "" {
			m.promptScreen.SetValue(goal)
		}
		return m, batch(nil)
	case OpenExternalEditorIntent:
		return m, batch(openExternalEditor(i.FilePath))
	}
	return m, batch(nil)
}

// startPipeline launches the orchestrator and returns a command to start listening.
func (m *Model) startPipeline(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancelCause(context.Background())
	m.cancelCause = cancel

	handle := m.engine.Start(ctx, orchestrator.Input{
		Prompt:     prompt,
		Setup:      m.confirmedSetup,
		SetupValid: true,
	})
	m.events = handle.Events
	m.intents = handle.Intents

	return tea.Batch(waitForEvent(handle.Events), tickCmd())
}
