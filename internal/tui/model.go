package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// AppState represents the top-level TUI mode.
type AppState int

const (
	StatePrompt            AppState = iota // full-screen prompt entry
	StatePipeline                          // 3-zone split layout (pipeline running/done)
	StateRunsList                          // historical runs list
	StateRunDetail                         // detail view for a single historical run
	StatePlanHistoryDetail                 // plan history viewer launched from Run Detail (read-only)
)

// ContentMode represents what the content zone shows during pipeline execution.
type ContentMode int

const (
	ContentStreaming     ContentMode = iota // auto-follows active agent stream
	ContentPlanReview                       // rendered spec
	ContentAgentHistory                     // frozen output of a previously-run agent
	ContentCompletion                       // QA report, summary
	ContentUserQuestion                     // MCP AskUserQuestion picker
	ContentPlanDiff                         // line diff of last plan revision
	ContentMergeConflict                    // post-run merge conflict resolution
	ContentEditConfirm                      // Ctrl+E edit confirmation prompt
	ContentPlanHistory                      // Ctrl+Y plan history viewer (live gate)
)

// AgentState classifies an agent's execution state.
type AgentState string

const (
	AgentStateRunning   AgentState = "running"
	AgentStateDone      AgentState = "done"
	AgentStateWaiting   AgentState = "waiting"
	AgentStateFailed    AgentState = "failed"
	AgentStateCancelled AgentState = "cancelled"
	AgentStateGate      AgentState = "gate"
)

// AgentRow tracks a single agent's status in the sidebar.
type AgentRow struct {
	ID            string
	State         AgentState
	Elapsed       time.Duration
	StartedAt     time.Time
	InputTokens   int64
	OutputTokens  int64
	ModelRef      string // config key used for this agent
	ModelDisplay  string // short display name (e.g. "claude-opus-4")
	Provider      string // provider name from config
	ContextWindow int64  // context window in tokens (0 = unknown)
}

// Model is the top-level Bubble Tea model for the Orqestra TUI.
type Model struct {
	state  AppState
	width  int
	height int

	// Pipeline communication (domain side-effects stay on root)
	events        <-chan orchestrator.Event
	streamUpdates <-chan orchestrator.StreamEntry
	decisions     chan<- orchestrator.Decision
	cancel        context.CancelFunc

	// Engine
	engine *orchestrator.Engine

	// Per-screen sub-models
	promptScreen      PromptScreen
	pipelineScreen    PipelineScreen
	runsListScreen    RunsListScreen
	runDetailScreen   RunDetailScreen
	planHistoryScreen PlanHistoryScreen

	// Global UI state
	ctrlCPending  bool
	ctrlCDeadline time.Time
	lastErr       error // navigation-level errors (e.g. loading runs)

	// Restart state: carries restart context from run detail to prompt screen.
	lastRestartRunPath           string
	lastRestartFirstMissingAgent string
}

// NewModel creates the initial TUI model.
func NewModel(engine *orchestrator.Engine, configName string) Model {
	return Model{
		state:             StatePrompt,
		promptScreen:      NewPromptScreen(),
		pipelineScreen:    NewPipelineScreen(configName),
		engine:            engine,
		runsListScreen:    NewRunsListScreen(),
		runDetailScreen:   NewRunDetailScreen(),
		planHistoryScreen: NewPlanHistoryScreen(),
	}
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

// waitForEvent returns a tea.Cmd that waits for the next pipeline event.
func waitForEvent(events <-chan orchestrator.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return pipelineClosedMsg{}
		}
		return OrchestratorEventMsg{Event: event}
	}
}

// pipelineClosedMsg signals the events channel was closed.
type pipelineClosedMsg struct{}

// planHistoryVisible reports whether the plan-history viewer should receive
// key/mouse events instead of the underlying pipeline / run-detail screen.
func (m Model) planHistoryVisible() bool {
	if m.state == StatePlanHistoryDetail {
		return true
	}
	return m.state == StatePipeline && m.pipelineScreen.content == ContentPlanHistory
}

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
		m.pipelineScreen.editorRunning = false
		if msg.err != nil {
			m.pipelineScreen.lastErr = msg.err
			return m, nil
		}
		if m.pipelineScreen.planFilePath != "" {
			return m, func() tea.Msg {
				data, err := os.ReadFile(m.pipelineScreen.planFilePath)
				if err != nil {
					return editorPlanReadMsg{err: fmt.Errorf("read plan after editor: %w", err)}
				}
				return editorPlanReadMsg{content: string(data)}
			}
		}
		return m, nil

	case editorPlanReadMsg:
		if msg.err != nil {
			m.pipelineScreen.lastErr = msg.err
			return m, nil
		}
		edited := msg.content
		if edited != m.pipelineScreen.finalPlan {
			// Show confirmation prompt instead of immediate DecisionEdit
			m.pipelineScreen.pendingEditContent = edited
			m.pipelineScreen.editConfirmCursor = 0
			m.pipelineScreen.hasEditComment = false
			m.pipelineScreen.content = ContentEditConfirm
			m.pipelineScreen.hasPlanComment = false
			m.recalculateLayout()
			m.pipelineScreen.SyncViewports()
			return m, nil
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
		switch m.state {
		case StatePipeline:
			m.pipelineScreen.SyncViewports()
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
		if m.state == StatePipeline {
			m.pipelineScreen.DrainStreamUpdates(m.streamUpdates)
			// Skip pipeline sync while the plan-history viewer is overlaid: its
			// rendering is owned by planHistoryScreen, and timer ticks must not
			// rebuild unrelated viewport content (tui-instructions.md §async).
			if !m.planHistoryVisible() {
				m.pipelineScreen.SyncViewports()
			}
			return m, tickCmd()
		}
		return m, nil

	case animTickMsg:
		if m.state == StatePipeline && m.pipelineScreen.active {
			m.pipelineScreen.animFrame++
			m.pipelineScreen.frameList.SetAnimFrame(m.pipelineScreen.animFrame)
			return m, animTickCmd()
		}
		return m, nil

	case ctrlCTimeoutMsg:
		m.ctrlCPending = false
		return m, nil

	case OrchestratorEventMsg:
		prevContent := m.pipelineScreen.content
		prevComment := m.pipelineScreen.hasPlanComment
		m.pipelineScreen.ApplyEvent(msg.Event, m.width)
		for {
			select {
			case ev, ok := <-m.events:
				if !ok {
					if !m.pipelineScreen.awaitingPlanDecision && m.pipelineScreen.content != ContentCompletion {
						m.pipelineScreen.content = ContentCompletion
					}
					if m.state == StatePipeline {
						if inputHeightChanged(prevContent, m.pipelineScreen.content, prevComment, m.pipelineScreen.hasPlanComment) {
							m.recalculateLayout()
						}
						m.pipelineScreen.SyncViewports()
					}
					return m, nil
				}
				m.pipelineScreen.ApplyEvent(ev, m.width)
			default:
				if m.state == StatePipeline {
					if inputHeightChanged(prevContent, m.pipelineScreen.content, prevComment, m.pipelineScreen.hasPlanComment) {
						m.recalculateLayout()
					}
					m.pipelineScreen.SyncViewports()
				}
				return m, waitForEvent(m.events)
			}
		}

	case planRevisionsLoadedMsg, planRevisionDetailLoadedMsg:
		var cmd tea.Cmd
		m.planHistoryScreen, cmd = m.planHistoryScreen.Update(msg)
		if intent := m.planHistoryScreen.PendingIntent; intent != nil {
			m.planHistoryScreen.PendingIntent = nil
			return m.processIntent(intent, cmd)
		}
		return m, cmd

	case pipelineClosedMsg:
		prevContent := m.pipelineScreen.content
		prevComment := m.pipelineScreen.hasPlanComment
		if !m.pipelineScreen.awaitingPlanDecision && m.pipelineScreen.content != ContentCompletion {
			m.pipelineScreen.content = ContentCompletion
		}
		if m.state == StatePipeline {
			if inputHeightChanged(prevContent, m.pipelineScreen.content, prevComment, m.pipelineScreen.hasPlanComment) {
				m.recalculateLayout()
			}
			m.pipelineScreen.SyncViewports()
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

// handleMouse routes mouse events to the active screen.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.planHistoryVisible() {
		var cmd tea.Cmd
		m.planHistoryScreen, cmd = m.planHistoryScreen.HandleMouse(msg)
		if intent := m.planHistoryScreen.PendingIntent; intent != nil {
			m.planHistoryScreen.PendingIntent = nil
			return m.processIntent(intent, cmd)
		}
		return m, cmd
	}
	if m.state != StatePipeline {
		return m, nil
	}
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.HandleMouse(msg)
	return m, cmd
}

// handleKey processes key events.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		// Second Ctrl+C within the time gate → quit immediately
		if m.ctrlCPending && time.Now().Before(m.ctrlCDeadline) {
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
		prevComment := m.pipelineScreen.hasPlanComment
		m.pipelineScreen = m.pipelineScreen.HandleCtrlCCancel()
		if inputHeightChanged(prevContent, m.pipelineScreen.content, prevComment, m.pipelineScreen.hasPlanComment) {
			m.recalculateLayout()
		}
		// Process any intent emitted by the cancel handler
		if intent := m.pipelineScreen.PendingIntent; intent != nil {
			m.pipelineScreen.PendingIntent = nil
			return m.processIntent(intent, timeoutCmd)
		}
		return m, timeoutCmd
	}

	if m.planHistoryVisible() {
		return m.handlePlanHistoryKey(msg)
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

// handlePlanHistoryKey delegates to PlanHistoryScreen and drains its intent.
func (m Model) handlePlanHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.planHistoryScreen, cmd = m.planHistoryScreen.Update(msg)
	if intent := m.planHistoryScreen.PendingIntent; intent != nil {
		m.planHistoryScreen.PendingIntent = nil
		return m.processIntent(intent, cmd)
	}
	return m, cmd
}

// handleRunsListKey delegates to RunsListScreen and handles intents.
func (m Model) handleRunsListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.runsListScreen, cmd = m.runsListScreen.Update(msg)
	if intent := m.runsListScreen.PendingIntent; intent != nil {
		m.runsListScreen.PendingIntent = nil
		switch i := intent.(type) {
		case NavigateBackIntent:
			m.state = StatePrompt
			m.recalculateLayout()
			return m, nil
		case NavigateToRunDetailIntent:
			if i.RunIndex < 0 || i.RunIndex >= len(m.runsListScreen.runs) {
				return m, nil
			}
			detail, err := agent.LoadRunDetail(m.runsListScreen.runs[i.RunIndex].Path)
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
		case OpenPlanHistoryIntent, RestartRunIntent:
			return m.processIntent(intent, cmd)
		}
	}
	return m, cmd
}

// handlePromptKey delegates to PromptScreen and handles intents.
func (m Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
				firstMissing := m.lastRestartFirstMissingAgent
				m.lastRestartRunPath = ""
				m.lastRestartFirstMissingAgent = ""
				m.pipelineScreen.Start(i.Prompt)
				m.state = StatePipeline
				m.recalculateLayout()
				pipelineCmd := m.startPipelineRestart(i.Prompt, runPath, firstMissing)
				return m, tea.Batch(pipelineCmd, animTickCmd())
			}
			m.pipelineScreen.Start(i.Prompt)
			m.state = StatePipeline
			m.recalculateLayout()
			pipelineCmd := m.startPipeline(i.Prompt)
			return m, tea.Batch(pipelineCmd, animTickCmd())
		case NavigateToRunsListIntent:
			m.navigateToRunsList()
			return m, nil
		}
	}
	return m, cmd
}

// planReviewHeightChanged reports whether a content mode transition requires a
// layout recalculation. The taller input zone (constPlanReviewInputHeight) is
// only active when content is ContentPlanReview AND the comment textarea is
// visible, so both dimensions must be compared.
func inputHeightChanged(prevContent, nextContent ContentMode, prevComment, nextComment bool) bool {
	prevTall := prevContent == ContentPlanReview && prevComment
	nextTall := nextContent == ContentPlanReview && nextComment
	if prevTall != nextTall {
		return true
	}
	// Question mode uses dynamic height
	prevQuestion := prevContent == ContentUserQuestion
	nextQuestion := nextContent == ContentUserQuestion
	return prevQuestion != nextQuestion
}

// handlePipelineKey delegates to PipelineScreen and handles intents.
func (m Model) handlePipelineKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prevContent := m.pipelineScreen.content
	prevComment := m.pipelineScreen.hasPlanComment
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.Update(msg)
	if inputHeightChanged(prevContent, m.pipelineScreen.content, prevComment, m.pipelineScreen.hasPlanComment) {
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
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionApprove}
		}
		return m, batch(nil)
	case ConfirmEditIntent:
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:          orchestrator.DecisionEdit,
				EditedContent: i.EditedContent,
				Comment:       i.Comment,
				AutoApprove:   i.AutoApprove,
			}
		}
		m.pipelineScreen.awaitingPlanDecision = false
		m.pipelineScreen.content = ContentStreaming
		m.pipelineScreen.SyncViewports()
		return m, batch(waitForEvent(m.events))
	case EditPlanIntent:
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:          orchestrator.DecisionEdit,
				EditedContent: i.ModifiedMarkdown,
			}
		}
		return m, batch(nil)
	case CommentPlanIntent:
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: i.Comment,
			}
		}
		return m, batch(waitForEvent(m.events))
	case CancelPlanIntent:
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionCancel}
		}
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
	case AbortMergeIntent:
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionMergeAbort}
		}
		return m, batch(nil)
	case OpenPlanHistoryIntent:
		if i.HistoryDir == "" {
			m.lastErr = errors.New("plan history unavailable for this run")
			return m, batch(nil)
		}
		cmd := m.planHistoryScreen.Open(i.HistoryDir, i.ReadOnly, i.HeadSHA)
		if i.ReadOnly {
			m.state = StatePlanHistoryDetail
		} else {
			m.pipelineScreen.content = ContentPlanHistory
		}
		m.recalculateLayout()
		return m, batch(cmd)
	case RestartRunIntent:
		m.lastRestartRunPath = i.RunPath
		m.lastRestartFirstMissingAgent = i.FirstMissingAgent
		m.pipelineScreen.Reset()
		m.state = StatePrompt
		m.promptScreen.Reset()
		// Pre-fill with a restart prompt that includes the missing agent context.
		prompt := "Restart run from agent: " + i.FirstMissingAgent
		m.promptScreen.SetValue(prompt)
		return m, batch(nil)
	case ClosePlanHistoryIntent:
		if m.state == StatePlanHistoryDetail {
			m.state = StateRunDetail
			m.recalculateLayout()
			m.runDetailScreen.SyncViewports()
		} else {
			m.pipelineScreen.content = ContentPlanReview
			m.recalculateLayout()
			m.pipelineScreen.SyncViewports()
		}
		return m, batch(nil)
	case RevertPlanIntent:
		// Comment intentionally empty; AutoApprove intentionally false: revert
		// must re-show the gate so the user reviews the historical revision
		// before approving. See `DecisionEdit` branch in orchestrator.go.
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:          orchestrator.DecisionEdit,
				EditedContent: i.Content,
			}
		}
		m.pipelineScreen.content = ContentStreaming
		m.pipelineScreen.awaitingPlanDecision = false
		m.recalculateLayout()
		m.pipelineScreen.SyncViewports()
		return m, batch(waitForEvent(m.events))
	}
	return m, batch(nil)
}

// startPipeline launches the orchestrator and returns a command to start listening.
func (m *Model) startPipeline(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	channels := m.engine.Start(ctx, orchestrator.Input{
		Prompt: prompt,
	})
	m.events = channels.Events
	m.streamUpdates = channels.StreamUpdates
	m.decisions = channels.Decisions
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))
	m.pipelineScreen.SetHistoryStore(channels.History)

	return tea.Batch(waitForEvent(channels.Events), tickCmd())
}

// startPipelineRestart launches the orchestrator for a restart run and returns
// a command to start listening. The restart context is passed through the Input.
func (m *Model) startPipelineRestart(prompt, runPath, firstMissingAgent string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	channels := m.engine.Start(ctx, orchestrator.Input{
		Prompt:      prompt,
		AutoApprove: true,
		RestartFrom: orchestrator.RestartInput{
			RunPath:           runPath,
			FirstMissingAgent: firstMissingAgent,
		},
	})
	m.events = channels.Events
	m.streamUpdates = channels.StreamUpdates
	m.decisions = channels.Decisions
	m.pipelineScreen.SetStreamBuf(orchestrator.NewStreamRing(200))
	m.pipelineScreen.SetHistoryStore(channels.History)

	return tea.Batch(waitForEvent(channels.Events), tickCmd())
}

// View renders the current screen.
func (m Model) View() tea.View {
	var content string
	if m.planHistoryVisible() {
		content = m.planHistoryScreen.View(m.effectiveWidth(), m.height)
	} else {
		switch m.state {
		case StatePrompt:
			content = m.promptScreen.View(m.effectiveWidth(), m.height)
		case StatePipeline:
			m.pipelineScreen.ctrlCPending = m.ctrlCPending
			content = m.pipelineScreen.View(m.effectiveWidth(), m.height)
		case StateRunsList:
			content = m.runsListScreen.View(m.effectiveWidth(), m.height)
		case StateRunDetail:
			content = m.runDetailScreen.View(m.effectiveWidth(), m.height)
		case StatePlanHistoryDetail:
			content = m.planHistoryScreen.View(m.effectiveWidth(), m.height)
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
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
		m.promptScreen.SetTextareaHeight(inputHeight - 2) // Subtract chrome
	case StatePipeline:
		if m.pipelineScreen.content == ContentPlanReview && m.pipelineScreen.hasPlanComment {
			inputHeight = constPlanReviewInputHeight
		} else if m.pipelineScreen.content == ContentUserQuestion && m.pipelineScreen.hasQuestion {
			// Auto-grow input zone for question options
			optCount := len(m.pipelineScreen.question.q.Options)
			inputHeight = max(constPipelineInputHeight, optCount+2)
		} else {
			inputHeight = constPipelineInputHeight
		}
	case StateRunsList, StateRunDetail:
		inputHeight = 0
	}

	// No header. Content gets full width. Sidebar is a bottom strip below the input zone.
	usedHeight := inputHeight + constFooterHeight + constSidebarHeight
	contentHeight := max(0, m.height-usedHeight)

	// Pipeline viewports and bounds
	m.pipelineScreen.RecalculateLayout(m.width, contentHeight)
	if m.pipelineScreen.content == ContentUserQuestion && m.pipelineScreen.hasQuestion {
		m.pipelineScreen.question = m.pipelineScreen.question.SetWidth(m.width)
	}
	inputTop := contentHeight
	m.pipelineScreen.bounds = layoutBounds{
		content: image.Rect(0, 0, m.width, contentHeight),
		sidebar: image.Rect(0, inputTop+inputHeight, m.width, inputTop+inputHeight+constSidebarHeight),
		textarea: image.Rect(
			0,
			contentHeight,
			m.width,
			contentHeight+inputHeight,
		),
	}

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

	if m.planHistoryVisible() {
		m.planHistoryScreen.RecalculateLayout(m.width, contentHeight)
	}
}
