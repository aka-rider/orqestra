package tui

import (
	"context"
	"fmt"
	"image"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// AppState represents the top-level TUI mode.
type AppState int

const (
	StatePrompt    AppState = iota // full-screen prompt entry
	StatePipeline                  // 3-zone split layout (pipeline running/done)
	StateRunsList                  // historical runs list
	StateRunDetail                 // detail view for a single historical run
)

// ContentMode represents what the content zone shows during pipeline execution.
type ContentMode int

const (
	ContentStreaming    ContentMode = iota // auto-follows active agent stream
	ContentCoaching                        // gateway brief + questions
	ContentPlanReview                      // rendered spec
	ContentPlanEdit                        // editable textarea for plan modification
	ContentAgentHistory                    // frozen output of a previously-run agent
	ContentCompletion                      // QA report, summary
)

// AgentRow tracks a single agent's status in the sidebar.
type AgentRow struct {
	ID           string
	State        string // "running", "done", "waiting", "failed", "cancelled", "gate"
	Elapsed      time.Duration
	StartedAt    time.Time
	InputTokens  int64
	OutputTokens int64
}

// Model is the top-level Bubble Tea model for the Orqestra TUI.
type Model struct {
	state  AppState
	width  int
	height int

	// Pipeline communication (domain side-effects stay on root)
	events    <-chan orchestrator.Event
	decisions chan<- orchestrator.Decision
	cancel    context.CancelFunc

	// Engine
	engine *orchestrator.Engine

	// Per-screen sub-models
	promptScreen    PromptScreen
	pipelineScreen  PipelineScreen
	runsListScreen  RunsListScreen
	runDetailScreen RunDetailScreen

	// Global UI state
	ctrlC   int
	lastErr error // navigation-level errors (e.g. loading runs)
}

// NewModel creates the initial TUI model.
func NewModel(engine *orchestrator.Engine, configName string) Model {
	return Model{
		state:           StatePrompt,
		promptScreen:    NewPromptScreen(),
		pipelineScreen:  NewPipelineScreen(configName),
		engine:          engine,
		runsListScreen:  NewRunsListScreen(),
		runDetailScreen: NewRunDetailScreen(),
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
			data, err := os.ReadFile(m.pipelineScreen.planFilePath)
			if err != nil {
				m.pipelineScreen.lastErr = fmt.Errorf("read plan after editor: %w", err)
				return m, nil
			}
			edited := string(data)
			if edited != m.pipelineScreen.finalPlan {
				m.pipelineScreen.finalPlan = edited
				if m.decisions != nil {
					m.decisions <- orchestrator.Decision{
						Type:          orchestrator.DecisionEdit,
						EditedContent: edited,
					}
				}
				m.pipelineScreen.awaitingPlanDecision = false
				m.pipelineScreen.content = ContentStreaming
				m.pipelineScreen.SyncViewports()
				return m, waitForEvent(m.events)
			}
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
			m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width)
		case StateRunDetail:
			m.runDetailScreen.SyncViewports()
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		if m.state == StatePipeline {
			m.pipelineScreen.SyncViewports()
			return m, tickCmd()
		}
		return m, nil

	case OrchestratorEventMsg:
		m.pipelineScreen.ApplyEvent(msg.Event, m.width)
		for {
			select {
			case ev, ok := <-m.events:
				if !ok {
					if !m.pipelineScreen.awaitingPlanDecision && m.pipelineScreen.content != ContentCompletion {
						m.pipelineScreen.content = ContentCompletion
					}
					if m.state == StatePipeline {
						m.pipelineScreen.SyncViewports()
					}
					return m, nil
				}
				m.pipelineScreen.ApplyEvent(ev, m.width)
			default:
				if m.state == StatePipeline {
					m.pipelineScreen.SyncViewports()
				}
				return m, waitForEvent(m.events)
			}
		}

	case pipelineClosedMsg:
		if !m.pipelineScreen.awaitingPlanDecision && m.pipelineScreen.content != ContentCompletion {
			m.pipelineScreen.content = ContentCompletion
		}
		if m.state == StatePipeline {
			m.pipelineScreen.SyncViewports()
		}
		return m, nil
	}

	// Pass non-key messages to focused sub-models
	if m.state == StatePrompt {
		var cmd tea.Cmd
		m.promptScreen, cmd = m.promptScreen.Update(msg)
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
	if m.state != StatePipeline {
		return m, nil
	}
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.HandleMouse(msg)
	return m, cmd
}

// handleKey processes key events.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.ctrlC++
		if m.ctrlC >= 2 {
			return m, tea.Quit
		}
		return m, nil
	}
	m.ctrlC = 0

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
func (m Model) handleRunsListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.runsListScreen, cmd = m.runsListScreen.Update(msg)
	if cmd != nil {
		intentMsg := cmd()
		switch intentMsg.(type) {
		case NavigateBackIntent:
			m.state = StatePrompt
			m.recalculateLayout()
			return m, nil
		case NavigateToRunDetailIntent:
			intent := intentMsg.(NavigateToRunDetailIntent)
			if intent.RunIndex < 0 || intent.RunIndex >= len(m.runsListScreen.runs) {
				return m, nil
			}
			detail, err := agent.LoadRunDetail(m.runsListScreen.runs[intent.RunIndex].Path)
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
		default:
			return m, cmd
		}
	}
	return m, nil
}

// handleRunDetailKey delegates to RunDetailScreen and handles intents.
func (m Model) handleRunDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.runDetailScreen, cmd = m.runDetailScreen.Update(msg)
	if cmd != nil {
		intentMsg := cmd()
		switch intentMsg.(type) {
		case NavigateBackIntent:
			m.state = StateRunsList
			m.recalculateLayout()
			m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width)
			return m, nil
		default:
			return m, cmd
		}
	}
	return m, nil
}

// handlePromptKey delegates to PromptScreen and handles intents.
func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.promptScreen, cmd = m.promptScreen.Update(msg)
	if cmd != nil {
		intentMsg := cmd()
		switch intent := intentMsg.(type) {
		case StartPipelineIntent:
			m.pipelineScreen.Start(intent.Prompt)
			m.state = StatePipeline
			m.recalculateLayout()
			pipelineCmd := m.startPipeline(intent.Prompt, intent.SkipGateway)
			return m, pipelineCmd
		case NavigateToRunsListIntent:
			m.navigateToRunsList()
			return m, nil
		default:
			return m, cmd
		}
	}
	return m, nil
}

// handlePipelineKey delegates to PipelineScreen and handles intents.
func (m Model) handlePipelineKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.pipelineScreen, cmd = m.pipelineScreen.Update(msg)
	if cmd != nil {
		intentMsg := cmd()
		switch intent := intentMsg.(type) {
		case SubmitGatewayIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{
					Type:           orchestrator.DecisionApprove,
					GatewayAnswers: intent.Answers,
				}
			}
			return m, nil
		case SkipGatewayIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionSkip}
			}
			return m, nil
		case ApprovePlanIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionApprove}
			}
			return m, nil
		case EditPlanIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{
					Type:          orchestrator.DecisionEdit,
					EditedContent: intent.ModifiedMarkdown,
				}
			}
			return m, nil
		case CommentPlanIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{
					Type:    orchestrator.DecisionComment,
					Comment: intent.Comment,
				}
			}
			return m, waitForEvent(m.events)
		case CancelPlanIntent:
			if m.decisions != nil {
				m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionCancel}
			}
			return m, nil
		case CancelPipelineIntent:
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case NavigateToPromptIntent:
			m.pipelineScreen.Reset()
			m.state = StatePrompt
			m.promptScreen.Reset()
			if intent.PreFillGoal != "" {
				m.promptScreen.SetValue(intent.PreFillGoal)
			}
			return m, nil
		case NavigateToRunsListIntent:
			m.navigateToRunsList()
			return m, nil
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
			return m, nil
		case OpenExternalEditorIntent:
			return m, openExternalEditor(intent.FilePath)
		default:
			return m, cmd
		}
	}
	return m, nil
}

// startPipeline launches the orchestrator and returns a command to start listening.
func (m *Model) startPipeline(prompt string, skipGateway bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	channels := m.engine.Start(ctx, orchestrator.Input{
		Prompt:      prompt,
		SkipGateway: skipGateway,
	})
	m.events = channels.Events
	m.decisions = channels.Decisions
	m.pipelineScreen.SetStreamBuf(channels.Stream)

	return tea.Batch(waitForEvent(channels.Events), tickCmd())
}

// View renders the current screen.
func (m Model) View() string {
	switch m.state {
	case StatePrompt:
		return m.promptScreen.View(m.effectiveWidth(), m.height)
	case StatePipeline:
		return m.pipelineScreen.View(m.effectiveWidth(), m.height)
	case StateRunsList:
		return m.runsListScreen.View(m.effectiveWidth(), m.height)
	case StateRunDetail:
		return m.runDetailScreen.View(m.effectiveWidth(), m.height)
	}
	return ""
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
		inputHeight = constPromptInputHeight
	case StatePipeline:
		if m.pipelineScreen.content == ContentPlanReview {
			inputHeight = constPlanReviewInputHeight
		} else {
			inputHeight = constPipelineInputHeight
		}
	case StateRunsList, StateRunDetail:
		inputHeight = 0
	}

	usedHeight := constHeaderHeight + inputHeight + constFooterHeight
	contentHeight := max(0, m.height-usedHeight)
	contentWidth := max(0, int(float64(m.width)*splitRatio))
	sidebarWidth := max(0, m.width-contentWidth-1)

	// Pipeline viewports and bounds
	m.pipelineScreen.RecalculateLayout(m.width, contentHeight)
	m.pipelineScreen.bounds = layoutBounds{
		content: image.Rect(0, constHeaderHeight, contentWidth, constHeaderHeight+contentHeight),
		sidebar: image.Rect(contentWidth+1, constHeaderHeight, m.width, constHeaderHeight+contentHeight),
		textarea: image.Rect(
			0,
			m.height-constFooterHeight-inputHeight,
			m.width,
			m.height-constFooterHeight,
		),
	}

	// Runs list: full-width viewport
	m.runsListScreen.viewport.Width = m.width
	m.runsListScreen.viewport.Height = contentHeight

	// Run detail: 3-zone layout
	if m.state == StateRunDetail {
		upperHeight := max(0, contentHeight-constRunLogHeight-1)
		m.runDetailScreen.detailVP.Width = contentWidth
		m.runDetailScreen.detailVP.Height = upperHeight
		m.runDetailScreen.stepsVP.Width = sidebarWidth
		m.runDetailScreen.stepsVP.Height = upperHeight
		m.runDetailScreen.logVP.Width = m.width
		m.runDetailScreen.logVP.Height = constRunLogHeight
	}

	// Update textarea width for prompt mode
	if m.state == StatePrompt {
		m.promptScreen.SetWidth(max(1, m.width-4))
		m.promptScreen.width = m.width
		m.promptScreen.height = m.height
	}
}
