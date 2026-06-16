package tui

import (
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
	switch keyMsg.String() {
	case "ctrl+e":
		return s.openStepLog()
	case "ctrl+shift+r":
		if !s.completeness.Complete && s.detail.Path != "" {
			s.PendingIntent = RestartRunIntent{
				RunPath:           s.detail.Path,
				Phase: orchestrator.RestartPhase(s.completeness.RestartPhase),
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
	switch msg.Code {
	case tea.KeyEscape:
		s.PendingIntent = NavigateBackIntent{}
		return s, nil
	case tea.KeyUp:
		if s.stepCursor > 0 {
			s.stepCursor--
			s.LoadStepLog()
			s.SyncViewports()
		}
		return s, nil
	case tea.KeyDown:
		if s.stepCursor < len(s.detail.Steps)-1 {
			s.stepCursor++
			s.LoadStepLog()
			s.SyncViewports()
		}
		return s, nil
	case tea.KeyPgUp:
		s.stepCursor = max(0, s.stepCursor-5)
		s.LoadStepLog()
		s.SyncViewports()
		return s, nil
	case tea.KeyPgDown:
		s.stepCursor = min(max(0, len(s.detail.Steps)-1), s.stepCursor+5)
		s.LoadStepLog()
		s.SyncViewports()
		return s, nil
	case tea.KeyEnter:
		s.focus = RunDetailFocusContent
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusLog
		} else {
			s.focus = RunDetailFocusContent
		}
		return s, nil
	}
	return s, nil
}

func (s RunDetailScreen) updateContent(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.focus = RunDetailFocusMenu
		return s, nil
	case tea.KeyUp:
		s.detailVP.ScrollUp(1)
		return s, nil
	case tea.KeyDown:
		s.detailVP.ScrollDown(1)
		return s, nil
	case tea.KeyPgUp:
		s.detailVP.HalfPageUp()
		return s, nil
	case tea.KeyPgDown:
		s.detailVP.HalfPageDown()
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusMenu
		} else {
			s.focus = RunDetailFocusLog
		}
		return s, nil
	}
	return s, nil
}

func (s RunDetailScreen) updateLog(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.focus = RunDetailFocusMenu
		return s, nil
	case tea.KeyUp:
		s.logVP.ScrollUp(1)
		return s, nil
	case tea.KeyDown:
		s.logVP.ScrollDown(1)
		return s, nil
	case tea.KeyPgUp:
		s.logVP.HalfPageUp()
		return s, nil
	case tea.KeyPgDown:
		s.logVP.HalfPageDown()
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusContent
		} else {
			s.focus = RunDetailFocusMenu
		}
		return s, nil
	}
	return s, nil
}
