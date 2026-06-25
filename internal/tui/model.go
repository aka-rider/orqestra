package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// AppState represents the top-level TUI mode.
type AppState int

const (
	StatePrompt            AppState = iota // full-screen prompt entry
	StatePipeline                          // 3-zone split layout (pipeline running/done)
	StateRunsList                          // historical runs list
	StateRunDetail                         // detail view for a single historical run
)

// ContentMode represents what the content zone shows during pipeline execution.
type ContentMode int

const (
	ContentStreaming    ContentMode = iota // auto-follows active agent stream
	ContentCompletion                     // QA report, summary
	ContentUserQuestion                   // MCP AskUserQuestion picker
	ContentEditConfirm                    // Ctrl+E edit confirmation prompt
	ContentHumanGate                      // human-in-the-loop plan gate
)

// AgentState classifies an agent's execution state.
type AgentState string

const (
	AgentStateRunning AgentState = "running"
	AgentStateDone    AgentState = "done"
	AgentStateFailed  AgentState = "failed"
)

// AgentRow tracks a single agent's status and completion totals.
type AgentRow struct {
	ID           string
	State        AgentState
	Elapsed      time.Duration
	StartedAt    time.Time
	InputTokens  int64
	OutputTokens int64
}

// regionBounds holds the absolute terminal rectangles for the pipeline
// alt-screen layout. Used for mouse hit-testing to prevent panes stealing events.
type regionBounds struct {
	timeline image.Rectangle
	input    image.Rectangle
}

// Model is the top-level Bubble Tea model for the Orqestra TUI.
type Model struct {
	state     AppState
	prevState AppState // state to return to when leaving the runs list (set on entry)
	width     int
	height    int
	regions   regionBounds // pipeline alt-screen layout regions

	// Pipeline observation + control (ObsStore polling path)
	obs     *orchestrator.ObsStore
	ctrl    orchestrator.Control
	lastRev uint64
	cancel  context.CancelFunc

	// Engine
	engine *orchestrator.Engine

	// keys is the single source of truth for key bindings (validated at startup).
	keys keymap.Bindings

	// Shared rune setup bundle (built once, threaded into prompt/gate sub-models)
	runeUI runeUI

	// Per-screen sub-models
	promptScreen      PromptScreen
	pipelineScreen    PipelineScreen
	runsListScreen    RunsListScreen
	runDetailScreen   RunDetailScreen

	// Global UI state
	ctrlCPending  bool
	ctrlCDeadline time.Time
	lastErr       error // navigation-level errors (e.g. loading runs)

	// Restart state: carries restart context from run detail to prompt screen.
	lastRestartRunPath string
	lastRestartPhase   orchestrator.RestartPhase

	// Setup panel state.
	setupScreen    setupModel
	confirmedSetup orchestrator.PipelineSetup
}

// NewModel creates the initial TUI model. Returns an error if the rune UI
// bundle (keymap, textedit commands, keybind resolver) fails to initialise.
func NewModel(engine *orchestrator.Engine, configName string) (Model, error) {
	ui, err := newRuneUI()
	if err != nil {
		return Model{}, fmt.Errorf("init rune UI: %w", err)
	}
	keys := keymap.Default()
	if err := keys.ValidateNoPhysicalKeyCollisions(); err != nil {
		return Model{}, fmt.Errorf("validate keymap: %w", err)
	}
	return Model{
		state:             StatePrompt,
		keys:              keys,
		promptScreen:      NewPromptScreen(ui),
		pipelineScreen:    NewPipelineScreen(configName, ui, keys),
		engine:            engine,
		runeUI:            ui,
		runsListScreen:    NewRunsListScreen(),
		runDetailScreen:   NewRunDetailScreen(),
		setupScreen:       newSetupModel(),
		confirmedSetup:    orchestrator.DefaultPipelineSetup(),
	}, nil
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

const (
	tickInterval = time.Second
)

// tickCmd returns a tea.Cmd that fires a tickMsg after tickInterval.
func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func animTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return animTickMsg(t)
	})
}

// notifyCmd returns a tea.Cmd that waits for the next ObsStore notify signal.
func notifyCmd(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return obsNotifyMsg{}
	}
}

// obsNotifyMsg fires when ObsStore has an updated snapshot for the TUI to consume.
type obsNotifyMsg struct{}

// Update handles messages and returns the updated model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case filePickerBatchMsg:
		if m.promptScreen.fpActive {
			m.promptScreen.fp.entries = append(m.promptScreen.fp.entries, msg.entries...)
			m.promptScreen.fp.refilter(m.promptScreen.fpQuery)
		}
		if m.promptScreen.fp.scanCh != nil {
			return m, readNextBatch(m.promptScreen.fp.scanCh)
		}
		return m, nil

	case filePickerDoneMsg:
		m.promptScreen.fp.scanning = false
		return m, nil

	case editorReturnMsg:
		if msg.err != nil {
			m.pipelineScreen.lastErr = msg.err // fail closed: keep the original plan
			return m, nil
		}
		if path := m.pipelineScreen.editorFilePath; path != "" {
			return m, func() tea.Msg {
				data, err := os.ReadFile(path)
				if err != nil {
					return editorPlanReadMsg{err: fmt.Errorf("read plan after editor: %w", err)}
				}
				_ = os.Remove(path) // fire-and-forget: best-effort cleanup of the temp file after read-back
				return editorPlanReadMsg{content: string(data)}
			}
		}
		return m, nil

	case editorPlanReadMsg:
		if msg.err != nil {
			m.pipelineScreen.lastErr = msg.err // fail closed: keep the original plan
			return m, nil
		}
		edited := msg.content
		if strings.TrimSpace(edited) == "" {
			// Fail closed: an empty file is a corrupt/aborted edit — keep the plan.
			m.pipelineScreen.lastErr = errors.New("edited plan was empty — keeping the original")
			return m, nil
		}
		if edited != m.pipelineScreen.finalPlan {
			// Show confirmation prompt instead of immediate DecisionEdit
			m.pipelineScreen.pendingEditContent = edited
			m.pipelineScreen.editConfirmCursor = 0
			m.pipelineScreen.hasEditComment = false
			m.pipelineScreen.content = ContentEditConfirm
			m.recalculateLayout()
			return m, nil
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
		switch m.state {
		case StateRunsList:
			m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width())
		case StateRunDetail:
			m.runDetailScreen.SyncViewports()
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		// Keep the tick loop alive while the pipeline screen is visible OR a run
		// is still active in the background (user navigated to runs list). This
		// keeps the stream ring fresh so returning shows live state, and avoids
		// spawning duplicate loops on re-entry.
		switch {
		case m.state == StatePipeline:
			if m.obs != nil {
				m.pipelineScreen.DrainStreamUpdates(m.obs.StreamCh())
			}
			m.recalculateLayout() // refresh streaming console height after drain
			return m, tickCmd()
		case m.pipelineScreen.active:
			// Background ingest — user is in runs list; transcript updates silently.
			if m.obs != nil {
				m.pipelineScreen.DrainStreamUpdates(m.obs.StreamCh())
			}
			return m, tickCmd()
		}
		return m, nil

	case animTickMsg:
		if m.pipelineScreen.active {
			if m.state == StatePipeline {
				m.pipelineScreen.animFrame++
			}
			return m, animTickCmd()
		}
		return m, nil

	case ctrlCTimeoutMsg:
		m.ctrlCPending = false
		return m, nil

	case obsNotifyMsg:
		if m.obs == nil {
			return m, nil
		}
		snap := m.obs.Snapshot()
		prevContent := m.pipelineScreen.content
		m.pipelineScreen.DrainStreamUpdates(m.obs.StreamCh())
		m.pipelineScreen.ApplySnapshot(snap, m.width)
		m.lastRev = snap.Rev
		content := m.pipelineScreen.content

		if m.state == StatePipeline {
			if inputHeightChanged(prevContent, content) {
				m.recalculateLayout()
			}
		}
		if !snap.Terminal.Done {
			return m, notifyCmd(m.obs.NotifyCh())
		}
		return m, nil
	}

	// Pass non-key messages to focused sub-models
	if m.state == StatePrompt {
		prevHeight := m.promptScreen.DesiredInputHeight(m.height)
		var cmd tea.Cmd
		m.promptScreen, cmd = m.promptScreen.Update(msg)
		if m.promptScreen.DesiredInputHeight(m.height) != prevHeight {
			m.recalculateLayout()
		}
		return m, cmd
	}
	if m.state == StatePipeline {
		var cmd tea.Cmd
		m.pipelineScreen, cmd = m.pipelineScreen.UpdateSubModel(msg)
		return m, cmd
	}

	return m, nil
}

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
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		// Pipeline is idle or completed → quit immediately (nothing to cancel)
		pipelineActive := m.state == StatePipeline &&
			m.pipelineScreen.active &&
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
		prevContent := m.pipelineScreen.content
		m.pipelineScreen = m.pipelineScreen.HandleCtrlCCancel()
		if inputHeightChanged(prevContent, m.pipelineScreen.content) {
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
			if m.prevState == StatePipeline && (m.pipelineScreen.active || m.obs != nil) {
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
		case RestartRunIntent:
			return m.processIntent(intent, cmd)
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
			// If we have a restart context, start a restart pipeline instead.
			if m.lastRestartRunPath != "" {
				runPath := m.lastRestartRunPath
				phase := m.lastRestartPhase
				m.lastRestartRunPath = ""
				m.lastRestartPhase = ""
				blinkCmd := m.pipelineScreen.Start(i.Prompt)
				m.state = StatePipeline
				m.recalculateLayout()
				pipelineCmd := m.startPipelineRestart(i.Prompt, runPath, phase)
				return m, tea.Batch(blinkCmd, pipelineCmd, animTickCmd())
			}
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

// inputHeightChanged reports whether a content mode transition requires a
// layout recalculation. Question mode uses dynamic height.
func inputHeightChanged(prevContent, nextContent ContentMode) bool {
	return (prevContent == ContentUserQuestion) != (nextContent == ContentUserQuestion)
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

	prevContent := m.pipelineScreen.content
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.Update(msg)
	if inputHeightChanged(prevContent, m.pipelineScreen.content) {
		m.recalculateLayout()
	}
	if intent := m.pipelineScreen.PendingIntent; intent != nil {
		m.pipelineScreen.PendingIntent = nil
		return m.processIntent(intent, cmd)
	}
	return m, cmd
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
		if m.engine != nil {
			ans := i.Answer
			return m, batch(func() tea.Msg {
				m.engine.SendAnswer(ans)
				return nil
			})
		}
		return m, batch(nil)
	case ApprovePlanIntent:
		m.ctrl.Submit(orchestrator.Decision{Type: orchestrator.DecisionApprove})
		return m, batch(nil)
	case ConfirmEditIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:          orchestrator.DecisionEdit,
			EditedContent: i.EditedContent,
			Comment:       i.Comment,
			AutoApprove:   i.AutoApprove,
		})
		m.pipelineScreen.awaitingPlanDecision = false
		m.pipelineScreen.enterStreaming()
		return m, batch(nil)
	case EditPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:          orchestrator.DecisionEdit,
			EditedContent: i.ModifiedMarkdown,
		})
		return m, batch(nil)
	case CommentPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{
			Type:    orchestrator.DecisionComment,
			Comment: i.Comment,
		})
		return m, batch(nil)
	case CancelPlanIntent:
		m.ctrl.Submit(orchestrator.Decision{Type: orchestrator.DecisionCancel})
		return m, batch(nil)
	case CancelPipelineIntent:
		if m.cancel != nil {
			m.cancel()
		}
		return m, batch(nil)
	case NavigateToPromptIntent:
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
		if m.cancel != nil {
			m.cancel()
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
	case RestartRunIntent:
		m.lastRestartRunPath = i.RunPath
		m.lastRestartPhase = i.Phase
		m.pipelineScreen.Reset()
		m.state = StatePrompt
		m.promptScreen.Reset()
		// Pre-fill with a restart prompt that includes the missing agent context.
		prompt := "Restart run from phase: " + string(i.Phase)
		m.promptScreen.SetValue(prompt)
		return m, batch(nil)
	case PostMessageIntent:
		if m.ctrl != nil && i.Text != "" {
			text := i.Text
			agentID := orchestrator.AgentID(i.AgentID)
			return m, batch(func() tea.Msg {
				if ch := m.ctrl.Input(agentID); ch != nil {
					ch <- harness.Message{Text: text}
				}
				return nil
			})
		}
		return m, batch(nil)
	}
	return m, batch(nil)
}

// startPipeline launches the orchestrator and returns a command to start listening.
func (m *Model) startPipeline(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	handle := m.engine.Start(ctx, orchestrator.Input{
		Prompt: prompt,
		Setup:  m.confirmedSetup,
	})
	m.obs = handle.Obs
	m.ctrl = handle.Ctrl
	m.lastRev = 0
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))

	return tea.Batch(notifyCmd(handle.Obs.NotifyCh()), tickCmd())
}

// startPipelineRestart launches the orchestrator for a restart run and returns
// a command to start listening. The restart context is passed through the Input.
func (m *Model) startPipelineRestart(prompt, runPath string, phase orchestrator.RestartPhase) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	handle := m.engine.Start(ctx, orchestrator.Input{
		Prompt: prompt,
		RestartFrom: orchestrator.RestartInput{
			RunPath: runPath,
			Phase:   phase,
		},
	})
	m.obs = handle.Obs
	m.ctrl = handle.Ctrl
	m.lastRev = 0
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))

	return tea.Batch(notifyCmd(handle.Obs.NotifyCh()), tickCmd())
}

// View renders the current screen.
func (m Model) View() tea.View {
	// Setup panel overlay takes over the entire screen when open.
	if m.state == StatePrompt && m.setupScreen.IsOpen() {
		v := tea.NewView(viewSetupOverlay(m.setupScreen, m.effectiveWidth(), m.height))
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		v.KeyboardEnhancements.ReportAlternateKeys = true
		v.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
		return v
	}

	var content string
	switch m.state {
	case StatePrompt:
		content = m.promptScreen.View(m.effectiveWidth(), m.height)
	case StatePipeline:
		content = m.pipelineScreen.View(m.effectiveWidth(), m.height, m.ctrlCPending)
	case StateRunsList:
		content = m.runsListScreen.View(m.effectiveWidth(), m.height)
	case StateRunDetail:
		content = m.runDetailScreen.View(m.effectiveWidth(), m.height)
	}
	v := tea.NewView(content)
	// Keyboard enhancements: enable for all states so Shift+Enter is
	// distinguishable from plain Enter in the prompt and gate inputs.
	v.KeyboardEnhancements.ReportAlternateKeys = true
	v.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	// Alt-screen states: pipeline, runs list, run detail. StatePrompt is inline.
	if m.state == StatePipeline || m.state == StateRunsList || m.state == StateRunDetail {
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m Model) effectiveWidth() int {
	if m.width >= minWidth {
		return m.width
	}
	return minWidth
}

// recalculateLayout computes viewport and textarea dimensions based on current
// terminal size and state. Must be called from Update() after size changes.
func (m *Model) recalculateLayout() {
	if m.width < minWidth || m.height < minHeight {
		return
	}

	var inputHeight int
	switch m.state {
	case StatePrompt:
		inputHeight = m.promptScreen.DesiredInputHeight(m.height)
		m.promptScreen.SetTextareaHeight(inputHeight - 1) // Subtract divider chrome
	case StatePipeline:
		if m.pipelineScreen.content == ContentUserQuestion && m.pipelineScreen.hasQuestion {
			// Auto-grow input zone for question options
			optCount := len(m.pipelineScreen.question.q.Options)
			inputHeight = max(constPipelineInputHeight, optCount+2)
		} else {
			inputHeight = constPipelineInputHeight
		}
	case StateRunsList, StateRunDetail:
		inputHeight = 0
	}

	if m.pipelineScreen.content == ContentUserQuestion && m.pipelineScreen.hasQuestion {
		m.pipelineScreen.question = m.pipelineScreen.question.SetWidth(m.width)
	}

	// Pipeline alt-screen layout: timeline + input + footer (no status bar).
	if m.state == StatePipeline {
		chromeH := inputHeight + constFooterHeight
		timelineH := max(0, m.height-chromeH)

		m.pipelineScreen.postInput.SetWidth(m.width)

		y := 0
		m.regions.timeline = image.Rect(1, y, m.width-1, y+timelineH) // 1-col margins
		y += timelineH
		m.regions.input = image.Rect(0, y, m.width, y+inputHeight)

		m.pipelineScreen.timeline.SetRect(m.regions.timeline)
	}

	// No header. Content gets full width. Sidebar is a bottom strip below the input zone.
	usedHeight := inputHeight + constFooterHeight + constSidebarHeight
	contentHeight := max(0, m.height-usedHeight)

	// Runs list: full-width viewport
	m.runsListScreen.viewport.SetWidth(m.width)
	m.runsListScreen.viewport.SetHeight(contentHeight)

	// Run detail: agent menu LEFT, plan content RIGHT, log BOTTOM.
	// RunDetail manages its own chrome; don't rely on pipeline's usedHeight.
	if m.state == StateRunDetail {
		stepsFullH := max(0, m.height-constRunDetailHeaderHeight-constFooterHeight)
		inputH := max(0, m.height-constRunDetailChromeHeight-constRunLogHeight)
		menuW := max(constRunDetailMinMenuW, m.width*constRunDetailMenuPct/100)
		contentW := max(0, m.width-menuW-1)
		m.runDetailScreen.stepsVP.SetWidth(menuW)
		m.runDetailScreen.stepsVP.SetHeight(stepsFullH)
		m.runDetailScreen.detailVP.SetWidth(contentW)
		m.runDetailScreen.detailVP.SetHeight(inputH)
		m.runDetailScreen.logVP.SetWidth(contentW)
		m.runDetailScreen.logVP.SetHeight(constRunLogHeight)
	}

	// Update textarea width for prompt mode
	if m.state == StatePrompt {
		m.promptScreen.SetWidth(max(1, m.width-4))
		m.promptScreen.width = m.width
		m.promptScreen.height = m.height
	}

	// Propagate gate body dimensions to the active chat view (markdownedit).
	if m.state == StatePipeline &&
		m.pipelineScreen.content == ContentHumanGate &&
		m.pipelineScreen.activeChat != nil {
		bodyH := max(0, m.height-constPipelineInputHeight-constFooterHeight)
		m.pipelineScreen.activeChat.SetSize(m.width, bodyH)
	}

}
