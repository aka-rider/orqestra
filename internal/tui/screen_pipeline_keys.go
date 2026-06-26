package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/tui/frame"
)

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// A question open in the chat takes the keys until it resolves; the prompt's
	// own shortcuts (scroll, new run, submit) are suspended while answering.
	if s.chat.QuestionOpen() {
		var cmd tea.Cmd
		s.chat, cmd = s.chat.Update(msg)
		if s.chat.question.Done() {
			s = s.resolveQuestion(s.chat.question)
		}
		return s, cmd
	}
	// A plan gate is a Soft/Hard gate over the same always-focused chat: a
	// keystroke approves/edits (hard), a typed reply revises (soft).
	if s.awaitingPlanDecision {
		return s.handleGateKey(msg)
	}
	switch {
	case key.Matches(msg, s.keys.PageUp, s.keys.PageDown, s.keys.ScrollTop, s.keys.ScrollBottom):
		var cmd tea.Cmd
		s.timeline, cmd = s.timeline.Update(msg)
		return s, cmd
	case key.Matches(msg, s.keys.NewRun):
		if s.active {
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case key.Matches(msg, s.keys.RunsList):
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case key.Matches(msg, s.keys.ExpandTools):
		s.SetToolFrameExpanded(!s.toolFrameExpanded)
		return s, nil
	case key.Matches(msg, s.keys.Submit):
		if text, ok := s.chat.Submit(); ok {
			s.timeline.Append(frame.NewSteer(text))
			agentID := s.lastAgentID
			return s, func() tea.Msg {
				return PostMessageIntent{AgentID: agentID, Text: text}
			}
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.chat, cmd = s.chat.Update(msg)
	return s, cmd
}

// handleGateKey drives a plan gate over the always-focused chat. The plan is
// already in the timeline; here the user approves (^A, hard), opens the plan in
// $EDITOR (^E, hard), scrolls, or types a revision and sends it (Enter, soft).
// ^C (cancel) is handled by HandleCtrlCCancel.
func (s PipelineScreen) handleGateKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch {
	case key.Matches(msg, s.keys.ApprovePlan):
		s.timeline.Append(frame.NewSteer("approved plan"))
		s.closeGate()
		s.PendingIntent = ApprovePlanIntent{}
		return s, nil
	case key.Matches(msg, s.keys.OpenPlanInEditor):
		// Write the plan to a temp file and open $EDITOR; fail closed on error,
		// keeping the gate open and the original plan unchanged.
		if s.hasPlan && s.finalPlan != "" {
			path, err := planTempFile(s.finalPlan)
			if err != nil {
				s.lastErr = err
				return s, nil
			}
			s.editorFilePath = path
			s.PendingIntent = OpenExternalEditorIntent{FilePath: path}
		}
		return s, nil
	case key.Matches(msg, s.keys.PageUp, s.keys.PageDown, s.keys.ScrollTop, s.keys.ScrollBottom):
		var cmd tea.Cmd
		s.timeline, cmd = s.timeline.Update(msg)
		return s, cmd
	case key.Matches(msg, s.keys.Submit):
		if text, ok := s.chat.Submit(); ok {
			s.timeline.Append(frame.NewSteer(text))
			s.closeGate()
			s.PendingIntent = CommentPlanIntent{Comment: text}
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.chat, cmd = s.chat.Update(msg)
	return s, cmd
}

func (s PipelineScreen) handleCompletionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch {
	case key.Matches(msg, s.keys.RunsList):
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case key.Matches(msg, s.keys.NewRun):
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case key.Matches(msg, s.keys.Quit):
		return s, tea.Quit
	}
	return s, nil
}

func (s PipelineScreen) handleEditConfirmKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	var res editConfirmResult
	var cmd tea.Cmd
	s.editConfirm, res, cmd = s.editConfirm.Update(msg, s.keys)
	switch res {
	case editConfirmApply:
		comment := s.editConfirm.commentText()
		s.PendingIntent = ConfirmEditIntent{
			EditedContent: s.editConfirm.pending,
			Comment:       comment,
			AutoApprove:   comment == "",
		}
		s.editConfirm = editConfirmModel{}
		s.awaitingPlanDecision = false
		s.enterStreaming()
	case editConfirmDiscard:
		// Keep the original plan; return to the gate (streaming + awaiting decision).
		s.editConfirm = editConfirmModel{}
		s.enterStreaming()
		s.awaitingPlanDecision = true
	}
	return s, cmd
}

func (s PipelineScreen) viewFooter(ctrlCPending bool) string {
	ctrlCHint := "[^C] cancel"
	if ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}

	// A question or a plan gate open in the chat owns the footer hints.
	if s.chat.QuestionOpen() {
		return keyStyle.Render(s.chat.question.Footer()+"  ") + ctrlCHint
	}
	if s.awaitingPlanDecision {
		return keyStyle.Render(" [^A] approve  [^E] edit  [⏎] revise  ") + ctrlCHint
	}

	switch s.content {
	case ContentEditConfirm:
		if s.editConfirm.hasComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] discard  ") + ctrlCHint
	case ContentCompletion:
		return keyStyle.Render(" [^N] new run  [^R] runs  [^Q] quit") + ctrlCHint
	default:
		// Count tool frames for footer hint.
		nTools := s.timeline.CollapsibleCount()
		baseHint := " [⏎] post  [^N] new run  [^R] runs"
		if s.active && nTools > constToolFrameMax {
			if s.toolFrameExpanded {
				baseHint += "  [^O] collapse"
			} else {
				baseHint += "  [^O] expand"
			}
		}
		return keyStyle.Render(baseHint) + ctrlCHint
	}
}
