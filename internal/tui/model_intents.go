package tui

import (
	"context"
	"fmt"
	"log/slog"

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
//
// done (WP17 hardening note) bounds that send: once the run this intent
// belongs to has ended — naturally, or because the user abandoned it — done
// is closed (Model.closeIntentsDone) and nobody will ever drain ch again.
// Without this, a send that happened to arrive after that point (a Cmd
// already queued by bubbletea before the run ended) would block this Cmd's
// goroutine forever once the buffer filled. done may be nil (no run has
// ever started) — a nil channel never fires in a select, so the send just
// behaves as a plain (possibly still momentarily blocking, per the comment
// above) send in that case, same as before this hardening.
func sendIntent(ch chan<- orchestrator.Intent, done <-chan struct{}, in orchestrator.Intent) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		select {
		case ch <- in:
		case <-done:
			slog.Debug("tui: dropped intent — its run already ended", "intent_type", fmt.Sprintf("%T", in))
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
		return m, batch(sendIntent(m.intents, m.intentsDone, orchestrator.QuestionAnswerIntent{
			QuestionID: i.Answer.ID,
			Answer:     i.Answer,
		}))
	case ApprovePlanIntent:
		return m, batch(sendIntent(m.intents, m.intentsDone, orchestrator.GateDecisionIntent{
			GateID:   m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{Type: orchestrator.DecisionApprove},
		}))
	case ConfirmEditIntent:
		cmd := sendIntent(m.intents, m.intentsDone, orchestrator.GateDecisionIntent{
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
		return m, batch(sendIntent(m.intents, m.intentsDone, orchestrator.GateDecisionIntent{
			GateID: m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: i.Comment,
			},
		}))
	case CancelPlanIntent:
		return m, batch(sendIntent(m.intents, m.intentsDone, orchestrator.GateDecisionIntent{
			GateID:   m.pipelineScreen.gateID,
			Decision: orchestrator.Decision{Type: orchestrator.DecisionCancel},
		}))
	case CancelPipelineIntent:
		if m.cancelCause != nil {
			m.cancelCause(orchestrator.ErrUserCancelled)
		}
		return m, batch(nil)
	case NavigateToPromptIntent:
		// WP17/F1,A3: invalidate the active run BEFORE resetting screen
		// state — a late event from whatever run was active (finished or
		// not) must never be delivered to the fresh prompt/next run. Zero is
		// never a real RunID (Engine.runSeq starts at 1), so this always
		// mismatches every future runEventMsg until the next startPipeline.
		m.activeRunID = 0
		// WP17 hardening note: release any sendIntent Cmd still waiting on
		// this (now-abandoned) run's intents channel.
		m.closeIntentsDone()
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
		// WP17/F1,A3 — the exact ^N-while-active scenario: invalidate the
		// active run FIRST, before cancelling it or resetting any screen
		// state, so run 1's late events (including its own cancelled-run
		// EventRunFinished) are rejected by Update even before run 2 exists.
		m.activeRunID = 0
		// WP17 hardening note: release any sendIntent Cmd still waiting on
		// run 1's intents channel before run 2 gets its own fresh one.
		m.closeIntentsDone()
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
	// WP17/F1,A3: this run becomes the ONLY one whose events Update will
	// accept from now on — set before the first waitForEvent so there is no
	// window where an in-flight message from a just-abandoned prior run
	// could still slip through.
	m.activeRunID = handle.RunID
	// WP17 hardening note: a fresh done-signal for THIS run's intents —
	// closed by closeIntentsDone once it ends (naturally or abandoned), so
	// sendIntent can never block its Cmd goroutine forever past that point.
	m.intentsDone = make(chan struct{})

	return tea.Batch(waitForEvent(handle.RunID, handle.Events), tickCmd())
}
