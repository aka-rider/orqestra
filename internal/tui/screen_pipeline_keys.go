package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
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
		text := strings.TrimSpace(s.postInput.Value())
		if text != "" {
			s.postInput.Reset()
			s.timeline.AppendSteer(text)
			agentID := s.lastAgentID
			return s, func() tea.Msg {
				return PostMessageIntent{AgentID: agentID, Text: text}
			}
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.postInput, cmd = s.postInput.Update(msg)
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
	if s.hasEditComment {
		switch {
		case key.Matches(msg, s.keys.FocusNext): // Tab: stop adding a comment
			s.hasEditComment = false
			return s, nil
		case key.Matches(msg, s.keys.Back): // Esc: discard the comment
			s.editConfirmComment.Reset()
			s.hasEditComment = false
			return s, nil
		case key.Matches(msg, s.keys.Submit): // bare Enter: save the comment
			comment := strings.TrimSpace(s.editConfirmComment.Value())
			s.PendingIntent = ConfirmEditIntent{
				EditedContent: s.pendingEditContent,
				Comment:       comment,
				AutoApprove:   comment == "",
			}
			s.pendingEditContent = ""
			s.hasEditComment = false
			s.awaitingPlanDecision = false
			s.enterStreaming()
			return s, nil
		}
		// Shift/Alt+Enter inserts a newline; any other printable key edits the
		// comment. Newline insertion is the textarea's own concern, not a binding.
		if msg.Code == tea.KeyEnter {
			s.editConfirmComment.InsertString("\n")
			return s, nil
		}
		if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) && !msg.Mod.Contains(tea.ModMeta) {
			var cmd tea.Cmd
			s.editConfirmComment, cmd = s.editConfirmComment.Update(msg)
			return s, cmd
		}
		return s, nil
	}

	switch {
	case key.Matches(msg, s.keys.Up):
		if s.editConfirmCursor > 0 {
			s.editConfirmCursor--
		}
		return s, nil
	case key.Matches(msg, s.keys.Down):
		if s.editConfirmCursor < 1 {
			s.editConfirmCursor++
		}
		return s, nil
	case key.Matches(msg, s.keys.FocusNext): // Tab: add a context comment
		if s.editConfirmCursor == 0 {
			ta := textarea.New()
			ta.Placeholder = "Describe your changes..."
			ta.SetWidth(max(1, 80-6))
			ta.SetHeight(2)
			ta.CharLimit = 1024
			ta.Focus()
			s.editConfirmComment = ta
			s.hasEditComment = true
			return s, nil
		}
		return s, nil
	case key.Matches(msg, s.keys.Submit):
		if s.editConfirmCursor == 0 {
			comment := ""
			if s.hasEditComment {
				comment = strings.TrimSpace(s.editConfirmComment.Value())
			}
			s.PendingIntent = ConfirmEditIntent{
				EditedContent: s.pendingEditContent,
				Comment:       comment,
				AutoApprove:   comment == "",
			}
			s.pendingEditContent = ""
			s.hasEditComment = false
			s.awaitingPlanDecision = false
			s.enterStreaming()
			return s, nil
		}
		// "No" — discard edit, return to gate
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentHumanGate
		s.awaitingPlanDecision = true
		return s, nil
	case key.Matches(msg, s.keys.Back): // Esc — same as "No"
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentHumanGate
		s.awaitingPlanDecision = true
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) viewFooter(ctrlCPending bool) string {
	ctrlCHint := "[^C] cancel"
	if ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}

	switch s.content {
	case ContentUserQuestion:
		return keyStyle.Render(s.question.Footer()+"  ") + ctrlCHint
	case ContentHumanGate:
		if s.activeChat != nil {
			return keyStyle.Render(s.activeChat.Footer()+"  ") + ctrlCHint
		}
		return ctrlCHint
	case ContentEditConfirm:
		if s.hasEditComment {
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
