package tui

import (
	"image"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// handleMouse routes mouse events to the active screen's viewport.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.state {
	case StatePipeline:
		cmd = m.handlePipelineMouse(msg)
	case StateRunsList:
		m.runsListScreen, cmd = m.runsListScreen.HandleMouse(msg)
	case StateRunDetail:
		m.runDetailScreen, cmd = m.runDetailScreen.HandleMouse(msg)
	}
	return m, cmd
}

// handlePipelineMouse routes mouse events for the pipeline alt-screen layout.
// Wheel events always reach the timeline; click/motion/release are bounded
// to the timeline region to avoid background panes stealing foreground events.
func (m *Model) handlePipelineMouse(msg tea.MouseMsg) tea.Cmd {
	var cmd tea.Cmd
	switch msg.(type) {
	case tea.MouseWheelMsg:
		m.pipelineScreen.timeline, cmd = m.pipelineScreen.timeline.Update(msg)
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg:
		pt := image.Point{X: msg.Mouse().X, Y: msg.Mouse().Y}
		if pt.In(m.regions.timeline) {
			m.pipelineScreen.timeline, cmd = m.pipelineScreen.timeline.Update(msg)
		}
	}
	return cmd
}

// handleKey processes key events.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Cancel) {
		// Second Ctrl+C within the time gate → cancel and quit immediately.
		if m.ctrlCPending && time.Now().Before(m.ctrlCDeadline) {
			if m.cancelCause != nil {
				m.cancelCause(orchestrator.ErrUserCancelled)
			}
			return m, tea.Quit
		}
		// Pipeline is idle or completed → quit immediately (nothing to cancel).
		// WP17 (pre-existing bug, model_keys.go:53-58): deliberately NOT
		// gated on m.state == StatePipeline — a run started on the pipeline
		// screen stays active in the background after the user navigates to
		// the runs list (or anywhere else); quitting from THERE with ^C must
		// still see it as active and go through the cancel-then-quit gate
		// below, not skip straight to tea.Quit and leave the run's process
		// group orphaned.
		pipelineActive := m.pipelineScreen.active &&
			m.pipelineScreen.content != ContentCompletion
		if !pipelineActive {
			return m, tea.Quit
		}
		// First Ctrl+C with active pipeline → cancel and start time gate
		m.ctrlCPending = true
		m.ctrlCDeadline = time.Now().Add(3 * time.Second)
		timeoutCmd := tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return ctrlCTimeoutMsg{}
		})
		// Dispatch cancel to the active pipeline screen
		prevInputH := m.pipelineScreen.inputZoneHeight()
		m.pipelineScreen = m.pipelineScreen.HandleCtrlCCancel()
		if m.pipelineScreen.inputZoneHeight() != prevInputH {
			m.recalculateLayout()
		}
		// Process any intent emitted by the cancel handler
		if intent := m.pipelineScreen.PendingIntent; intent != nil {
			m.pipelineScreen.PendingIntent = nil
			return m.processIntent(intent, timeoutCmd)
		}
		return m, timeoutCmd
	}

	switch m.state {
	case StatePrompt:
		return m.handlePromptKey(msg)
	case StatePipeline:
		return m.handlePipelineKey(msg)
	case StateRunsList:
		return m.handleRunsListKey(msg)
	case StateRunDetail:
		return m.handleRunDetailKey(msg)
	}
	return m, nil
}

// handleRunsListKey delegates to RunsListScreen and handles intents.
func (m Model) handleRunsListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.runsListScreen, cmd = m.runsListScreen.Update(msg)
	if intent := m.runsListScreen.PendingIntent; intent != nil {
		m.runsListScreen.PendingIntent = nil
		switch i := intent.(type) {
		case NavigateBackIntent:
			// Return to where we came from. If a pipeline is still live, go back
			// to its view (the tick/anim loops stayed alive while we were away).
			if m.prevState == StatePipeline && (m.pipelineScreen.active || m.events != nil) {
				m.state = StatePipeline
				m.recalculateLayout()
				return m, nil
			}
			m.state = StatePrompt
			m.recalculateLayout()
			return m, nil
		case NavigateToRunDetailIntent:
			if i.RunIndex < 0 || i.RunIndex >= len(m.runsListScreen.runs) {
				return m, nil
			}
			detail, err := orchestrator.LoadRunDetail(m.runsListScreen.runs[i.RunIndex].Path)
			if err != nil {
				m.lastErr = err
				return m, nil
			}
			m.runDetailScreen.SetDetail(detail)
			m.runDetailScreen.LoadStepLog()
			m.state = StateRunDetail
			m.recalculateLayout()
			m.runDetailScreen.SyncViewports()
			return m, nil
		}
	}
	return m, cmd
}

// handleRunDetailKey delegates to RunDetailScreen and handles intents.
func (m Model) handleRunDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.runDetailScreen, cmd = m.runDetailScreen.Update(msg)
	if intent := m.runDetailScreen.PendingIntent; intent != nil {
		m.runDetailScreen.PendingIntent = nil
		switch intent.(type) {
		case NavigateBackIntent:
			m.state = StateRunsList
			m.recalculateLayout()
			m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width())
			return m, nil
		}
	}
	return m, cmd
}

// handlePromptKey delegates to PromptScreen and handles intents.
func (m Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When the setup panel is open, route all keys to it.
	if m.setupScreen.IsOpen() {
		var cmd tea.Cmd
		m.setupScreen, cmd = m.setupScreen.Update(msg)
		if intent := m.setupScreen.PendingIntent; intent != nil {
			m.setupScreen.PendingIntent = nil
			if ci, ok := intent.(ConfirmSetupIntent); ok {
				m.confirmedSetup = ci.Setup
			}
		}
		return m, cmd
	}

	prevHeight := m.promptScreen.DesiredInputHeight(m.height)
	var cmd tea.Cmd
	m.promptScreen, cmd = m.promptScreen.Update(msg)
	if m.promptScreen.DesiredInputHeight(m.height) != prevHeight {
		m.recalculateLayout()
	}
	if intent := m.promptScreen.PendingIntent; intent != nil {
		m.promptScreen.PendingIntent = nil
		switch i := intent.(type) {
		case StartPipelineIntent:
			blinkCmd := m.pipelineScreen.Start(i.Prompt)
			m.state = StatePipeline
			m.recalculateLayout()
			pipelineCmd := m.startPipeline(i.Prompt)
			return m, tea.Batch(blinkCmd, pipelineCmd, animTickCmd())
		case NavigateToRunsListIntent:
			m.navigateToRunsList()
			return m, nil
		case ToggleSetupIntent:
			if m.setupScreen.IsOpen() {
				m.setupScreen.Close()
			} else {
				m.setupScreen.Open(m.confirmedSetup)
			}
			return m, nil
		}
	}
	return m, cmd
}

// handlePipelineKey delegates to PipelineScreen and handles intents.
func (m Model) handlePipelineKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Explicit copy key bindings: Cmd+Shift+C copies selection; Cmd+C copies hovered frame.
	switch {
	case key.Matches(msg, m.keys.CopySelection):
		cmd := m.pipelineScreen.timeline.CopySelected()
		return m, cmd
	case key.Matches(msg, m.keys.Copy):
		if m.pipelineScreen.timeline.HasSelection() {
			cmd := m.pipelineScreen.timeline.CopySelected()
			return m, cmd
		}
	}

	prevInputH := m.pipelineScreen.inputZoneHeight()
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.Update(msg)
	if m.pipelineScreen.inputZoneHeight() != prevInputH {
		m.recalculateLayout()
	}
	if intent := m.pipelineScreen.PendingIntent; intent != nil {
		m.pipelineScreen.PendingIntent = nil
		return m.processIntent(intent, cmd)
	}
	return m, cmd
}
