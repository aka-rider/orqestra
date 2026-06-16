package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// Up/Down arrows scroll the viewport by 1 line in streaming mode.
	switch msg.Code {
	case tea.KeyUp:
		if s.contentVP.YOffset() > 0 {
			s.contentVP.SetYOffset(s.contentVP.YOffset() - 1)
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyDown:
		s.contentVP.SetYOffset(s.contentVP.YOffset() + 1)
		s.SyncViewports()
		return s, nil
	}

	switch msg.String() {
	case "ctrl+o":
		// Expand/collapse the active streaming block.
		if s.frameList.FrameCount() > 0 {
			s.frameList.UpdateActive(func(f *Frame) {
				f.StreamingCollapsed = !f.StreamingCollapsed
			})
		}
		s.SyncViewports()
		return s, nil
	case "ctrl+n":
		if s.active {
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case "ctrl+d":
		if s.content != ContentPlanReview && s.content != ContentUserQuestion {
			s.showDashboard = !s.showDashboard
			s.SyncViewports()
			return s, nil
		}
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) handleCompletionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// Up/Down arrows scroll the viewport by 1 line.
	switch msg.Code {
	case tea.KeyUp:
		if s.contentVP.YOffset() > 0 {
			s.contentVP.SetYOffset(s.contentVP.YOffset() - 1)
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyDown:
		s.contentVP.SetYOffset(s.contentVP.YOffset() + 1)
		s.SyncViewports()
		return s, nil
	}

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

func (s PipelineScreen) viewHelp() string {
	return ` Orqestra Keybindings
─────────────────────────────────
 [Enter]       Submit prompt / confirm
 [PgUp/PgDn]   Scroll content
 [↑/↓]         Scroll content
 [^O]          Expand/collapse streaming block
 [Ctrl+A]      Accept plan / abort merge
 [Ctrl+E]      Edit plan in external editor
 [Ctrl+D]      Run details
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

	var inProgressHint string
	if s.frameList.HasInProgressFrame() {
		var collapsed bool
		s.frameList.UpdateActive(func(f *Frame) {
			collapsed = f.StreamingCollapsed
		})
		if collapsed {
			inProgressHint = " [^O] expand"
		} else {
			inProgressHint = " [^O] collapse"
		}
	}

	switch s.content {
	case ContentUserQuestion:
		return keyStyle.Render(s.question.Footer()+"  [^H] help  ") + ctrlCHint
	case ContentHumanGate:
		if s.activeChat != nil {
			return keyStyle.Render(s.activeChat.Footer()+"  [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [^H] help  ") + ctrlCHint
	case ContentPlanReview:
		footer := " [^A] accept | [^E] edit in editor | [Enter] comment | [Shift+Enter] newline"
		if s.planDiff != "" {
			footer += " | [^D] diff"
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
		return keyStyle.Render(" [Esc] back to live  [^D] details  [^R] runs  [^N] new  [^H] help  ") + ctrlCHint
	case ContentCompletion:
		hint := " [^N] new run  [^R] runs  [^Q] quit  [^H] help"
		if s.frameList.FocusedIndex() >= 0 {
			hint = " [↑↓] scroll  [PgUp/PgDn] page  [^D] details  [^R] runs  [^Q] quit  [^H] help"
		}
		return keyStyle.Render(hint) + ctrlCHint
	default:
		hint := " [^N] new run  [^D] details  [^R] runs  [^H] help"
		if s.frameList.HasInProgressFrame() {
			hint += inProgressHint
		}
		hint += "  " + ctrlCHint
		return keyStyle.Render(hint)
	}
}
