package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if s.frameList.FrameCount() > 0 {
		switch msg.Code {
		case tea.KeyUp:
			s.frameList.FocusPrev()
			s.contentVP.SetYOffset(s.frameList.FrameTopLine(s.frameList.FocusedIndex()))
			return s, nil
		case tea.KeyDown:
			s.frameList.FocusNext()
			s.contentVP.SetYOffset(s.frameList.FrameTopLine(s.frameList.FocusedIndex()))
			return s, nil
		case tea.KeySpace:
			s.frameList.ToggleFocused()
			s.SyncViewports()
			return s, nil
		case tea.KeyEscape:
			if s.frameList.FocusedIndex() >= 0 {
				s.frameList.ClearFocus()
				s.SyncViewports()
				return s, nil
			}
		}
	}
	switch msg.String() {
	case "e":
		if s.frameList.FrameCount() > 0 && s.frameList.FocusedIndex() >= 0 {
			s.frameList.ToggleFocusedTools()
			s.SyncViewports()
			return s, nil
		}
	case "ctrl+n":
		if s.active {
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) handleCompletionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if s.frameList.FrameCount() > 0 {
		switch msg.Code {
		case tea.KeyUp:
			s.frameList.FocusPrev()
			s.contentVP.SetYOffset(s.frameList.FrameTopLine(s.frameList.FocusedIndex()))
			return s, nil
		case tea.KeyDown:
			s.frameList.FocusNext()
			s.contentVP.SetYOffset(s.frameList.FrameTopLine(s.frameList.FocusedIndex()))
			return s, nil
		case tea.KeySpace:
			s.frameList.ToggleFocused()
			s.SyncViewports()
			return s, nil
		case tea.KeyEscape:
			if s.frameList.FocusedIndex() >= 0 {
				s.frameList.ClearFocus()
				s.SyncViewports()
				return s, nil
			}
		}
	}
	switch msg.String() {
	case "e":
		if s.frameList.FrameCount() > 0 && s.frameList.FocusedIndex() >= 0 {
			s.frameList.ToggleFocusedTools()
			s.SyncViewports()
			return s, nil
		}
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

func (s PipelineScreen) viewHelp() string {
	return ` Orqestra Keybindings
─────────────────────────────────
 [Enter]       Submit prompt / confirm
 [PgUp/PgDn]   Scroll content
 [↑/↓]         Focus prev/next frame
 [Space]        Collapse/expand frame
 [e]            Expand/collapse tool history (focused frame)
 [Ctrl+A]      Accept plan / abort merge
 [Ctrl+E]      Edit plan in external editor
 [Ctrl+D]      Toggle dashboard / diff
 [Ctrl+N]      New run
 [Ctrl+R]      Historical runs
 [Ctrl+Q]      Quit (at completion)
 [Ctrl+H]      Toggle this help
 [Ctrl+Y]      Plan history viewer
 [Alt+1-9]     View agent output
 [Ctrl+C]      Cancel → exit (time-gated)
 [Esc]         Back / dismiss
`
}

func (s PipelineScreen) viewFooter() string {
	ctrlCHint := "[^C] cancel"
	if s.ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}

	// Dashboard overlay uses its own footer
	if s.showDashboard {
		return keyStyle.Render(" [Esc] return | [Tab] cycle | [PgUp/Dn] scroll              [^H] help  ") + ctrlCHint
	}

	switch s.content {
	case ContentUserQuestion:
		return keyStyle.Render(s.question.Footer()+"  [^H] help  ") + ctrlCHint
	case ContentPlanReview:
		footer := " [^A] accept | [^E] edit in editor | [Enter] comment | [Shift+Enter] newline"
		if s.planDiff != "" {
			footer += " | [^D] diff"
		}
		if s.planHistoryDir != "" {
			footer += " | [^Y] history"
		}
		footer += "  " + ctrlCHint
		if len(s.chatHistory) > 0 && (s.reviewTokensIn+s.reviewTokensOut > 0) {
			footer += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
		}
		return keyStyle.Render(footer)
	case ContentEditConfirm:
		if s.hasEditComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard                    [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] discard  [^H] help  ") + ctrlCHint
	case ContentMergeConflict:
		return keyStyle.Render(" [^A] abort merge | [Esc] continue                       [^H] help  ") + ctrlCHint
	case ContentAgentHistory:
		return keyStyle.Render(" [Esc] back to live                  [^D] expand  [^N] new  [^H] help  ") + ctrlCHint
	case ContentCompletion:
		hint := " [^N] new run | [^R] runs | [^Q] quit                    [^H] help"
		if s.frameList.FocusedIndex() >= 0 {
			hint = " [↑↓] focus  [Space] collapse  [e] tools  [^R] runs  [^Q] quit  [^H] help"
		}
		return keyStyle.Render(hint)
	default:
		hint := " [^N] new run                    [^D] expand  [Alt+N] agent  [^H] help  "
		if s.frameList.FocusedIndex() >= 0 {
			hint = " [↑↓] focus  [Space] collapse  [e] tools  [^D] expand  [^H] help  "
		}
		return keyStyle.Render(hint) + ctrlCHint
	}
}
