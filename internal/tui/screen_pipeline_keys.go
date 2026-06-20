package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.String() {
	case "pgup", "ctrl+b", "pgdown", "ctrl+f", "home", "end":
		var cmd tea.Cmd
		s.timeline, cmd = s.timeline.Update(msg)
		return s, cmd
	case "ctrl+n":
		if s.active {
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case "enter":
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
	switch msg.String() {
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case "ctrl+n":
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case "ctrl+q":
		return s, tea.Quit
	}
	return s, nil
}

func (s PipelineScreen) handleEditConfirmKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if s.hasEditComment {
		switch msg.Code {
		case tea.KeyTab:
			s.hasEditComment = false
			return s, nil
		case tea.KeyEscape:
			s.editConfirmComment.Reset()
			s.hasEditComment = false
			return s, nil
		case tea.KeyEnter:
			if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
				s.editConfirmComment.InsertString("\n")
				return s, nil
			}
			comment := strings.TrimSpace(s.editConfirmComment.Value())
			s.PendingIntent = ConfirmEditIntent{
				EditedContent: s.pendingEditContent,
				Comment:       comment,
				AutoApprove:   comment == "",
			}
			s.pendingEditContent = ""
			s.hasEditComment = false
			s.awaitingPlanDecision = false
			s.content = ContentStreaming
			return s, nil
		}
		if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) && !msg.Mod.Contains(tea.ModMeta) {
			var cmd tea.Cmd
			s.editConfirmComment, cmd = s.editConfirmComment.Update(msg)
			return s, cmd
		}
		return s, nil
	}

	switch msg.Code {
	case tea.KeyUp:
		if s.editConfirmCursor > 0 {
			s.editConfirmCursor--
		}
		return s, nil
	case tea.KeyDown:
		if s.editConfirmCursor < 1 {
			s.editConfirmCursor++
		}
		return s, nil
	case tea.KeyTab:
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
	case tea.KeyEnter:
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
			s.content = ContentStreaming
			return s, nil
		}
		// "No" — discard edit, return to gate
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentHumanGate
		s.awaitingPlanDecision = true
		return s, nil
	case tea.KeyEscape:
		// Same as "No"
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentHumanGate
		s.awaitingPlanDecision = true
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) viewFooter() string {
	ctrlCHint := "[^C] cancel"
	if s.ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}

	switch s.content {
	case ContentUserQuestion:
		return keyStyle.Render(s.question.Footer()+"  [^H] help  ") + ctrlCHint
	case ContentHumanGate:
		if s.activeChat != nil {
			return keyStyle.Render(s.activeChat.Footer()+"  [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [^H] help  ") + ctrlCHint
	case ContentEditConfirm:
		if s.hasEditComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard                    [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] discard  [^H] help  ") + ctrlCHint
	case ContentCompletion:
		hint := " [^N] new run  [^R] runs  [^Q] quit"
		if s.reviewTokensIn+s.reviewTokensOut > 0 {
			hint += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
		}
		return keyStyle.Render(hint) + ctrlCHint
	default:
		return keyStyle.Render(" [⏎] post  [^N] new run  [^R] runs  ") + ctrlCHint
	}
}
