package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
)

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
		if m.engine != nil {
			ans := i.Answer
			return m, batch(func() tea.Msg {
				m.engine.SendAnswer(ans)
				return nil
			})
		}
		return m, batch(nil)
	case ApprovePlanIntent:
		m.ctrl.Submit(orchestrator.Decision{Type: orchestrator.DecisionApprove})
		return m, batch(nil)
	case ConfirmEditIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:          orchestrator.DecisionEdit,
			EditedContent: i.EditedContent,
			Comment:       i.Comment,
			AutoApprove:   i.AutoApprove,
		})
		m.pipelineScreen.awaitingPlanDecision = false
		m.pipelineScreen.enterStreaming()
		return m, batch(nil)
	case EditPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:          orchestrator.DecisionEdit,
			EditedContent: i.ModifiedMarkdown,
		})
		return m, batch(nil)
	case CommentPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:    orchestrator.DecisionComment,
			Comment: i.Comment,
		})
		return m, batch(nil)
	case CancelPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{Type: orchestrator.DecisionCancel})
		return m, batch(nil)
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
	case RestartRunIntent:
		m.lastRestartRunPath = i.RunPath
		m.lastRestartPhase = i.Phase
		m.pipelineScreen.Reset()
		m.state = StatePrompt
		m.promptScreen.Reset()
		// Pre-fill with a restart prompt that includes the missing agent context.
		prompt := "Restart run from phase: " + string(i.Phase)
		m.promptScreen.SetValue(prompt)
		return m, batch(nil)
	case PostMessageIntent:
		if m.ctrl != nil && i.Text != "" {
			text := i.Text
			agentID := orchestrator.AgentID(i.AgentID)
			return m, batch(func() tea.Msg {
				if ch := m.ctrl.Input(agentID); ch != nil {
					ch <- harness.Message{Text: text}
				}
				return nil
			})
		}
		return m, batch(nil)
	}
	return m, batch(nil)
}

// startPipeline launches the orchestrator and returns a command to start listening.
func (m *Model) startPipeline(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancelCause(context.Background())
	m.cancelCause = cancel

	handle := m.engine.Start(ctx, orchestrator.Input{
		Prompt: prompt,
		Setup:  m.confirmedSetup,
	})
	m.obs = handle.Obs
	m.ctrl = handle.Ctrl
	m.lastRev = 0
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))

	return tea.Batch(notifyCmd(handle.Obs.NotifyCh()), tickCmd())
}

// startPipelineRestart launches the orchestrator for a restart run and returns
// a command to start listening. The restart context is passed through the Input.
func (m *Model) startPipelineRestart(prompt, runPath string, phase orchestrator.RestartPhase) tea.Cmd {
	ctx, cancel := context.WithCancelCause(context.Background())
	m.cancelCause = cancel

	handle := m.engine.Start(ctx, orchestrator.Input{
		Prompt: prompt,
		RestartFrom: orchestrator.RestartInput{
			RunPath: runPath,
			Phase:   phase,
		},
	})
	m.obs = handle.Obs
	m.ctrl = handle.Ctrl
	m.lastRev = 0
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))

	return tea.Batch(notifyCmd(handle.Obs.NotifyCh()), tickCmd())
}
