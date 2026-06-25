package tui

import (
	"fmt"

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
			s.timeline.Append(frame.NewSteer(text, dimStyle))
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
		// Keep the original plan; return to the gate.
		s.editConfirm = editConfirmModel{}
		s.content = ContentHumanGate
		s.awaitingPlanDecision = true
	}
	return s, cmd
}

func (s PipelineScreen) viewFooter(ctrlCPending bool) string {
	ctrlCHint := "[^C] cancel"
	if ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}

	// A question open in the chat owns the footer hints, regardless of run state.
	if s.chat.QuestionOpen() {
		return keyStyle.Render(s.chat.question.Footer()+"  ") + ctrlCHint
	}

	switch s.content {
	case ContentHumanGate:
		if s.activeChat != nil {
			return keyStyle.Render(s.activeChat.Footer()+"  ") + ctrlCHint
		}
		return ctrlCHint
	case ContentEditConfirm:
		if s.editConfirm.hasComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] discard  ") + ctrlCHint
	case ContentCompletion:
		hint := " [^N] new run  [^R] runs  [^Q] quit"
		if s.reviewTokensIn+s.reviewTokensOut > 0 {
			hint += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
		}
		return keyStyle.Render(hint) + ctrlCHint
	default:
		// Count tool frames for footer hint.
		hasOverflow := s.timeline.ToolCount() > constToolFrameMax

		baseHint := " [⏎] post  [^N] new run  [^R] runs"
		if s.active && hasOverflow {
			if s.toolFrameExpanded {
				baseHint += "  [^O] collapse"
			} else {
				baseHint += "  [^O] expand"
			}
		}
		return keyStyle.Render(baseHint) + ctrlCHint
	}
}
