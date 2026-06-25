package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/xiii/orqestra/internal/orchestrator"
)

// Update handles key events for the run detail screen.
func (s RunDetailScreen) Update(msg tea.Msg) (RunDetailScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}

	// Global keys — not focus-dependent.
	switch {
	case key.Matches(keyMsg, s.keys.OpenStepLog):
		return s.openStepLog()
	case key.Matches(keyMsg, s.keys.RestartRun):
		if !s.completeness.Complete && s.detail.Path != "" {
			s.PendingIntent = RestartRunIntent{
				RunPath: s.detail.Path,
				Phase:   orchestrator.RestartPhase(s.completeness.RestartPhase),
			}
		}
		return s, nil
	}

	// Focus-dependent key dispatch.
	switch s.focus {
	case RunDetailFocusMenu:
		return s.updateMenu(keyMsg)
	case RunDetailFocusContent:
		return s.updateContent(keyMsg)
	case RunDetailFocusLog:
		return s.updateLog(keyMsg)
	}
	return s, nil
}

func (s RunDetailScreen) updateMenu(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch {
	case key.Matches(msg, s.keys.Back):
		s.PendingIntent = NavigateBackIntent{}
	case key.Matches(msg, s.keys.Up):
		if s.stepCursor > 0 {
			s.stepCursor--
			s.LoadStepLog()
			s.SyncViewports()
		}
	case key.Matches(msg, s.keys.Down):
		if s.stepCursor < len(s.detail.Steps)-1 {
			s.stepCursor++
			s.LoadStepLog()
			s.SyncViewports()
		}
	case key.Matches(msg, s.keys.PageUp):
		s.stepCursor = max(0, s.stepCursor-5)
		s.LoadStepLog()
		s.SyncViewports()
	case key.Matches(msg, s.keys.PageDown):
		s.stepCursor = min(max(0, len(s.detail.Steps)-1), s.stepCursor+5)
		s.LoadStepLog()
		s.SyncViewports()
	case key.Matches(msg, s.keys.Submit), key.Matches(msg, s.keys.FocusNext):
		s.focus = RunDetailFocusContent
	case key.Matches(msg, s.keys.FocusPrev):
		s.focus = RunDetailFocusLog
	}
	return s, nil
}

func (s RunDetailScreen) updateContent(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch {
	case key.Matches(msg, s.keys.Back):
		s.focus = RunDetailFocusMenu
	case key.Matches(msg, s.keys.Up):
		s.detailVP.ScrollUp(1)
	case key.Matches(msg, s.keys.Down):
		s.detailVP.ScrollDown(1)
	case key.Matches(msg, s.keys.PageUp):
		s.detailVP.HalfPageUp()
	case key.Matches(msg, s.keys.PageDown):
		s.detailVP.HalfPageDown()
	case key.Matches(msg, s.keys.FocusPrev):
		s.focus = RunDetailFocusMenu
	case key.Matches(msg, s.keys.FocusNext):
		s.focus = RunDetailFocusLog
	}
	return s, nil
}

func (s RunDetailScreen) updateLog(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch {
	case key.Matches(msg, s.keys.Back):
		s.focus = RunDetailFocusMenu
	case key.Matches(msg, s.keys.Up):
		s.logVP.ScrollUp(1)
	case key.Matches(msg, s.keys.Down):
		s.logVP.ScrollDown(1)
	case key.Matches(msg, s.keys.PageUp):
		s.logVP.HalfPageUp()
	case key.Matches(msg, s.keys.PageDown):
		s.logVP.HalfPageDown()
	case key.Matches(msg, s.keys.FocusPrev):
		s.focus = RunDetailFocusContent
	case key.Matches(msg, s.keys.FocusNext):
		s.focus = RunDetailFocusMenu
	}
	return s, nil
}
