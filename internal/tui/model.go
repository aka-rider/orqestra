package tui

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// AppState represents the top-level TUI mode.
type AppState int

const (
	StatePrompt   AppState = iota // full-screen prompt entry
	StatePipeline                 // 3-zone split layout (pipeline running/done)
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
	state     AppState
	content   ContentMode
	width     int
	height    int
	startTime time.Time
	goal      string
	phase     orchestrator.Phase

	// Pipeline communication
	events    <-chan orchestrator.Event
	decisions chan<- orchestrator.Decision
	cancel    context.CancelFunc

	// Sidebar state
	agents []AgentRow

	// Content state (held by value to prevent races)
	gatewayResult    agent.GatewayResult
	finalPlan        string
	hasPlan          bool
	workerValidation string
	hasValidation    bool
	lastErr          error

	// Streaming output — shared buffer polled on tick, not via channel events
	streamBuf *orchestrator.StreamBuffer

	// Input state
	prompt       textarea.Model
	answerFields []textarea.Model
	answerCursor int

	// Plan edit state
	planEditor    textarea.Model
	hasPlanEditor bool

	// Agent history navigation
	focusedAgent int // 0 = live (no agent focused), 1-9 = agent index

	// UI state
	engine        *orchestrator.Engine
	ctrlC         int
	showDashboard bool
	showHelp      bool
	confirmNew    bool // awaiting confirmation to start new run while pipeline active

	// Viewports — scrollable zones managed by recalculateLayout()
	contentVP   viewport.Model
	sidebarVP   viewport.Model
	dashboardVP viewport.Model
	bounds      layoutBounds
}

// NewModel creates the initial TUI model.
func NewModel(engine *orchestrator.Engine) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter a task description. Be specific about the end state."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.CharLimit = 4096

	// Initialize viewports with zero size — recalculateLayout sets real dims on first WindowSizeMsg
	cvp := viewport.New(0, 0)
	cvp.MouseWheelEnabled = true
	svp := viewport.New(0, 0)
	svp.MouseWheelEnabled = true
	dvp := viewport.New(0, 0)
	dvp.MouseWheelEnabled = true

	return Model{
		state:       StatePrompt,
		startTime:   time.Now(),
		prompt:      ta,
		engine:      engine,
		contentVP:   cvp,
		sidebarVP:   svp,
		dashboardVP: dvp,
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
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		// Tick only matters during pipeline execution — triggers View refresh.
		if m.state == StatePipeline {
			return m, tickCmd()
		}
		return m, nil

	case OrchestratorEventMsg:
		return m.handleOrchestratorEvent(msg.Event)

	case pipelineClosedMsg:
		// Pipeline finished — stay in current content mode
		if m.content != ContentCompletion {
			m.content = ContentCompletion
		}
		return m, nil
	}

	// Pass to sub-models
	if m.state == StatePrompt {
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
	if m.state == StatePipeline && m.content == ContentCoaching {
		if m.answerCursor < len(m.answerFields) {
			var cmd tea.Cmd
			m.answerFields[m.answerCursor], cmd = m.answerFields[m.answerCursor].Update(msg)
			return m, cmd
		}
	}
	if m.state == StatePipeline && m.content == ContentPlanEdit && m.hasPlanEditor {
		var cmd tea.Cmd
		m.planEditor, cmd = m.planEditor.Update(msg)
		return m, cmd
	}

	return m, nil
}

// handleMouse routes mouse events to the viewport whose bounds enclose the cursor.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.state != StatePipeline {
		return m, nil
	}

	p := image.Pt(msg.X, msg.Y)

	if p.In(m.bounds.textarea) {
		// Don't scroll viewports if hovering the input zone
		return m, nil
	}

	var cmd tea.Cmd
	if m.showDashboard {
		m.dashboardVP, cmd = m.dashboardVP.Update(msg)
	} else if p.In(m.bounds.sidebar) {
		m.sidebarVP, cmd = m.sidebarVP.Update(msg)
	} else if p.In(m.bounds.content) {
		m.contentVP, cmd = m.contentVP.Update(msg)
	}
	return m, cmd
}

// handleKey processes key events.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global: Ctrl+C double-press to exit
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
	}
	return m, nil
}

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		prompt := strings.TrimSpace(m.prompt.Value())
		if prompt == "" {
			return m, nil
		}
		m.goal = prompt
		m.state = StatePipeline
		m.content = ContentStreaming
		m.startTime = time.Now()
		return m, m.startPipeline(prompt, false)
	case tea.KeyCtrlS:
		// Skip gateway
		prompt := strings.TrimSpace(m.prompt.Value())
		if prompt == "" {
			return m, nil
		}
		m.goal = prompt
		m.state = StatePipeline
		m.content = ContentStreaming
		m.startTime = time.Now()
		return m, m.startPipeline(prompt, true)
	default:
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
}

func (m Model) handlePipelineKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global pipeline keys first
	switch msg.String() {
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "d", "D":
		if m.content != ContentCoaching && m.content != ContentPlanEdit {
			m.showDashboard = !m.showDashboard
			return m, nil
		}
	}

	// PgUp / PgDown — delegate to active viewport
	switch msg.Type {
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		if m.showDashboard {
			m.dashboardVP, cmd = m.dashboardVP.Update(msg)
		} else {
			m.contentVP, cmd = m.contentVP.Update(msg)
		}
		return m, cmd
	}

	// If help is showing, any other key dismisses it
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	// If dashboard is showing, Esc returns to split view
	if m.showDashboard {
		if msg.Type == tea.KeyEsc {
			m.showDashboard = false
		}
		return m, nil
	}

	// Number keys for agent navigation (1-9)
	if msg.String() >= "1" && msg.String() <= "9" && m.content != ContentCoaching && m.content != ContentPlanEdit {
		idx := int(msg.String()[0] - '0')
		if idx <= len(m.agents) {
			m.focusedAgent = idx
			m.content = ContentAgentHistory
			m.contentVP.GotoTop()
			return m, nil
		}
	}

	switch m.content {
	case ContentCoaching:
		return m.handleCoachingKey(msg)
	case ContentPlanReview:
		return m.handlePlanReviewKey(msg)
	case ContentPlanEdit:
		return m.handlePlanEditKey(msg)
	case ContentAgentHistory:
		return m.handleAgentHistoryKey(msg)
	case ContentCompletion:
		return m.handleCompletionKey(msg)
	case ContentStreaming:
		return m.handleStreamingKey(msg)
	}
	return m, nil
}

func (m Model) handleCoachingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Submit answers
		answers := make([]orchestrator.GatewayAnswer, len(m.answerFields))
		for i, f := range m.answerFields {
			answers[i] = orchestrator.GatewayAnswer{
				QuestionIndex: i,
				Answer:        strings.TrimSpace(f.Value()),
			}
		}
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:           orchestrator.DecisionApprove,
				GatewayAnswers: answers,
			}
		}
		m.content = ContentStreaming
		return m, nil
	case tea.KeyCtrlS:
		// Skip coaching
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionSkip}
		}
		m.content = ContentStreaming
		return m, nil
	case tea.KeyTab:
		if m.answerCursor < len(m.answerFields)-1 {
			m.answerFields[m.answerCursor].Blur()
			m.answerCursor++
			m.answerFields[m.answerCursor].Focus()
		}
		return m, nil
	case tea.KeyShiftTab:
		if m.answerCursor > 0 {
			m.answerFields[m.answerCursor].Blur()
			m.answerCursor--
			m.answerFields[m.answerCursor].Focus()
		}
		return m, nil
	}
	// Pass to active answer field
	if m.answerCursor < len(m.answerFields) {
		var cmd tea.Cmd
		m.answerFields[m.answerCursor], cmd = m.answerFields[m.answerCursor].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handlePlanReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a", "A":
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionApprove}
		}
		m.content = ContentStreaming
		return m, nil
	case "e", "E":
		// Switch to plan edit mode
		contentWidth := max(1, int(float64(m.width)*splitRatio))
		contentHeight := max(4, m.height-constHeaderHeight-constPipelineInputHeight-constFooterHeight)
		ta := textarea.New()
		ta.SetWidth(max(1, contentWidth-2))
		ta.SetHeight(max(1, contentHeight-2))
		ta.CharLimit = 65536
		if m.hasPlan {
			ta.SetValue(m.finalPlan)
		}
		ta.Focus()
		m.planEditor = ta
		m.hasPlanEditor = true
		m.content = ContentPlanEdit
		return m, nil
	case "s", "S":
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{Type: orchestrator.DecisionCancel}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handlePlanEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlS:
		// Save edits and send to pipeline
		if m.decisions != nil {
			m.decisions <- orchestrator.Decision{
				Type:          orchestrator.DecisionEdit,
				EditedContent: m.planEditor.Value(),
			}
		}
		m.content = ContentStreaming
		return m, nil
	case tea.KeyEsc:
		// Discard edits, return to plan review
		m.content = ContentPlanReview
		return m, nil
	}
	// Pass to textarea
	if m.hasPlanEditor {
		var cmd tea.Cmd
		m.planEditor, cmd = m.planEditor.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleAgentHistoryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.content = ContentStreaming
		m.focusedAgent = 0
		m.contentVP.GotoTop()
		return m, nil
	}
	return m, nil
}

func (m Model) handleStreamingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// If awaiting confirmation for new run
	if m.confirmNew {
		switch msg.String() {
		case "y", "Y":
			// Confirmed: cancel current and start new
			if m.cancel != nil {
				m.cancel()
			}
			m.confirmNew = false
			m.state = StatePrompt
			m.prompt.Reset()
			if m.goal != "" {
				m.prompt.SetValue(m.goal)
			}
			return m, nil
		default:
			// Any other key cancels the confirmation
			m.confirmNew = false
			return m, nil
		}
	}

	switch msg.String() {
	case "s", "S":
		// Cancel running agent
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case "n", "N":
		// New run — confirm if pipeline is active
		if m.cancel != nil && m.content == ContentStreaming {
			m.confirmNew = true
			return m, nil
		}
		m.state = StatePrompt
		m.prompt.Reset()
		if m.goal != "" {
			m.prompt.SetValue(m.goal)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "n", "N":
		m.state = StatePrompt
		m.prompt.Reset()
		if m.goal != "" {
			m.prompt.SetValue(m.goal)
		}
		return m, nil
	case "q", "Q":
		return m, tea.Quit
	}
	return m, nil
}

// handleOrchestratorEvent updates the model based on orchestrator events.
func (m Model) handleOrchestratorEvent(event orchestrator.Event) (tea.Model, tea.Cmd) {
	// Always re-subscribe to the next event
	nextCmd := waitForEvent(m.events)

	switch event.Type {
	case orchestrator.EventPhaseChange:
		m.phase = event.Phase

	case orchestrator.EventAgentStarted:
		m.agents = append(m.agents, AgentRow{ID: event.AgentID, State: "running", StartedAt: time.Now()})

	case orchestrator.EventAgentDone:
		for i := range m.agents {
			if m.agents[i].ID == event.AgentID {
				m.agents[i].State = "done"
				m.agents[i].Elapsed = time.Since(m.agents[i].StartedAt)
				m.agents[i].InputTokens = event.InputTokens
				m.agents[i].OutputTokens = event.OutputTokens
			}
		}

	case orchestrator.EventAgentFailed:
		for i := range m.agents {
			if m.agents[i].ID == event.AgentID {
				m.agents[i].State = "failed"
			}
		}
		m.lastErr = event.Err

	case orchestrator.EventAgentCancelled:
		for i := range m.agents {
			if m.agents[i].ID == event.AgentID {
				m.agents[i].State = "cancelled"
			}
		}

	case orchestrator.EventGateRequest:
		m.contentVP.GotoTop()
		switch event.Gate.Type {
		case orchestrator.GateGatewayCoach:
			m.content = ContentCoaching
			m.gatewayResult = event.Gate.GatewayResult
			// Create answer fields pre-filled with defaults
			contentWidth := max(1, int(float64(m.width)*splitRatio))
			m.answerFields = make([]textarea.Model, len(m.gatewayResult.Questions))
			for i, q := range m.gatewayResult.Questions {
				ta := textarea.New()
				ta.SetWidth(max(1, contentWidth-4))
				ta.SetHeight(1)
				ta.CharLimit = 512
				if q.Default != "" {
					ta.SetValue(q.Default)
				}
				if i == 0 {
					ta.Focus()
				}
				m.answerFields[i] = ta
			}
			m.answerCursor = 0

		case orchestrator.GatePlanApproval:
			m.content = ContentPlanReview
			m.finalPlan = event.Gate.FinalPlanMarkdown
			m.hasPlan = true
		}

	case orchestrator.EventPlanReady:
		m.finalPlan = event.FinalPlan
		m.hasPlan = true

	case orchestrator.EventComplete:
		m.content = ContentCompletion
		m.contentVP.GotoTop()
		if event.WorkerValidation != "" {
			m.workerValidation = event.WorkerValidation
			m.hasValidation = true
		}
	}

	return m, nextCmd
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
	m.streamBuf = channels.Stream

	// Start event listener + tick timer concurrently
	return tea.Batch(waitForEvent(channels.Events), tickCmd())
}

// View renders the current screen.
func (m Model) View() string {
	switch m.state {
	case StatePrompt:
		return m.viewPromptScreen()
	case StatePipeline:
		return m.viewPipelineScreen()
	}
	return ""
}

func (m Model) effectiveWidth() int {
	if m.width >= minWidth {
		return m.width
	}
	return minWidth
}

func (m Model) viewPromptScreen() string {
	w := m.effectiveWidth()
	h := m.height
	if h < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header (2 lines)
	header := headerStyle.Render(" Orqestra") + "\n" +
		dividerStyle.Render(strings.Repeat("─", w)) + "\n"

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [Enter] submit | [Ctrl+S] skip gateway | [^C^C] quit")

	// Input zone (divider + instruction + textarea + newline)
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		" Enter a task description. Be specific about the end state.\n" +
		m.prompt.View() + "\n"

	// Content zone dimensions — derived from constants
	contentHeight := max(0, h-constHeaderHeight-constPromptInputHeight-constFooterHeight)
	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)

	// If content zone too small, skip split view — just render chrome
	if contentHeight < 2 {
		return header + input + footer
	}

	// Content: mascot art centered vertically
	mascot := renderMascot(contentWidth-2, contentHeight)
	mascotLines := strings.Split(mascot, "\n")
	padTop := 0
	if len(mascotLines) < contentHeight {
		padTop = (contentHeight - len(mascotLines)) / 2
	}
	var contentBuf strings.Builder
	for i := 0; i < contentHeight; i++ {
		mi := i - padTop
		if mi >= 0 && mi < len(mascotLines) {
			contentBuf.WriteString(" " + mascotLines[mi])
		}
		if i < contentHeight-1 {
			contentBuf.WriteString("\n")
		}
	}

	// Sidebar: static agent list
	var sidebarBuf strings.Builder
	sidebarBuf.WriteString(" Agents\n")
	sidebarBuf.WriteString(strings.Repeat("─", max(1, sidebarWidth-1)) + "\n")
	sidebarBuf.WriteString(" ● gateway     gate\n")
	sidebarBuf.WriteString("   awaiting input\n")
	sidebarBuf.WriteString("\n")
	sidebarBuf.WriteString(" ○ planner        -\n")
	sidebarBuf.WriteString(" ○ workers        -\n")
	sidebarBuf.WriteString(" ○ qa             -")

	body := joinSplitView(contentBuf.String(), sidebarBuf.String(), contentWidth, sidebarWidth, contentHeight)

	return header + body + "\n" + input + footer
}

func (m Model) viewPipelineScreen() string {
	w := m.effectiveWidth()
	h := m.height
	if h < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header (2 lines)
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	title := headerStyle.Render(" Orqestra")
	phase := phaseStyle.Render(fmt.Sprintf("▶ %s", m.phase))
	timeStr := elapsedStyle.Render(elapsed.String())
	header := fmt.Sprintf("%s  %s  %s\n", title, phase, timeStr) +
		dividerStyle.Render(strings.Repeat("─", w)) + "\n"

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		m.viewFooter()

	// Input zone (3 lines)
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		m.viewInputZone() + "\n"

	// Content zone dimensions
	contentHeight := max(0, h-constHeaderHeight-constPipelineInputHeight-constFooterHeight)
	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)

	// Render body depending on mode
	var body string
	if m.showDashboard {
		// Full-width dashboard via viewport
		m.dashboardVP.Width = w
		m.dashboardVP.Height = contentHeight
		m.dashboardVP.SetContent(m.viewDashboard())
		body = m.dashboardVP.View()
	} else {
		// Resolve content and sidebar
		var contentView string
		if m.showHelp {
			contentView = m.viewHelp()
		} else {
			contentView = m.viewContent(contentWidth)
		}
		sidebarView := m.viewSidebar(sidebarWidth)

		// Feed content into viewports
		// Preserve scroll position: auto-follow bottom for streaming
		atBottom := m.contentVP.AtBottom()
		m.contentVP.Width = contentWidth
		m.contentVP.Height = contentHeight
		m.contentVP.SetContent(contentView)
		if atBottom && m.content == ContentStreaming {
			m.contentVP.GotoBottom()
		}

		m.sidebarVP.Width = sidebarWidth
		m.sidebarVP.Height = contentHeight
		m.sidebarVP.SetContent(sidebarView)

		body = joinSplitView(m.contentVP.View(), m.sidebarVP.View(), contentWidth, sidebarWidth, contentHeight)
	}

	return header + body + "\n" + input + footer
}

// viewInputZone renders the input/status line between split and footer.
func (m Model) viewInputZone() string {
	if m.showHelp {
		return keyStyle.Render(" Press any key to dismiss help")
	}
	if m.showDashboard {
		return keyStyle.Render(" [Esc] return to split view")
	}
	switch m.content {
	case ContentStreaming:
		status := fmt.Sprintf(" %s running...", m.phase)
		if m.lastErr != nil {
			status = errorStyle.Render(fmt.Sprintf(" Error: %v", m.lastErr))
		}
		if m.confirmNew {
			status = warnStyle.Render(" Pipeline is active. Start new run? [Y] yes / [any] cancel")
		}
		return status
	case ContentCoaching:
		return keyStyle.Render(" Answer the questions above, then [Enter] to confirm")
	case ContentPlanReview:
		return keyStyle.Render(" [A] accept | [E] edit | [S] cancel")
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard")
	case ContentAgentHistory:
		agent := ""
		if m.focusedAgent > 0 && m.focusedAgent <= len(m.agents) {
			agent = m.agents[m.focusedAgent-1].ID
		}
		return keyStyle.Render(fmt.Sprintf(" viewing %s history (read-only)", agent))
	case ContentCompletion:
		if m.lastErr != nil {
			return errorStyle.Render(fmt.Sprintf(" Error: %v", m.lastErr))
		}
		return keyStyle.Render(" Pipeline complete")
	}
	return ""
}

func (m Model) viewContent(width int) string {
	switch m.content {
	case ContentStreaming:
		return m.viewStreaming(width)
	case ContentCoaching:
		return m.viewCoaching(width)
	case ContentPlanReview:
		return m.viewPlanReview(width)
	case ContentPlanEdit:
		return m.viewPlanEdit(width)
	case ContentAgentHistory:
		return m.viewAgentHistory(width)
	case ContentCompletion:
		return m.viewCompletion(width)
	}
	return ""
}

func (m Model) viewStreaming(width int) string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n", goalStyle.Render(m.goal)))
	}

	var streamAgent string
	var streamLines []string
	if m.streamBuf != nil {
		streamAgent, streamLines = m.streamBuf.Snapshot()
	}

	b.WriteString(fmt.Sprintf(" Phase: %s", m.phase))
	if streamAgent != "" {
		b.WriteString(fmt.Sprintf("  (%s)", streamAgent))
	}
	b.WriteString("\n")

	if len(streamLines) > 0 {
		b.WriteString(dividerStyle.Render(strings.Repeat("─", width-2)))
		b.WriteString("\n")
		// Render all lines — viewport handles clipping and scrolling
		maxLineWidth := width - 2
		if maxLineWidth < 1 {
			maxLineWidth = 1
		}
		for _, line := range streamLines {
			// Soft-wrap long lines instead of truncating
			for len(line) > maxLineWidth {
				b.WriteString(" ")
				b.WriteString(streamStyle.Render(line[:maxLineWidth]))
				b.WriteString("\n")
				line = line[maxLineWidth:]
			}
			b.WriteString(" ")
			b.WriteString(streamStyle.Render(line))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m Model) viewCoaching(_ int) string {
	var b strings.Builder
	brief := m.gatewayResult.Brief
	b.WriteString(fmt.Sprintf(" Task: %s\n", goalStyle.Render(brief.Task)))
	if brief.EndState != "" {
		b.WriteString(fmt.Sprintf(" End State: %s\n", brief.EndState))
	}
	b.WriteString("\n Questions:\n")
	for i, q := range m.gatewayResult.Questions {
		marker := "  "
		if i == m.answerCursor {
			marker = "▶ "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, q.Text))
		if i < len(m.answerFields) {
			b.WriteString(fmt.Sprintf("   %s\n", m.answerFields[i].View()))
		}
	}
	return b.String()
}

func (m Model) viewPlanReview(width int) string {
	if !m.hasPlan {
		return " Waiting for plan...\n"
	}
	var b strings.Builder
	// Render raw markdown plan — the plan IS the content
	for _, line := range strings.Split(m.finalPlan, "\n") {
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(" ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) viewCompletion(_ int) string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	if m.lastErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf(" Error: %v", m.lastErr)))
		b.WriteString("\n")
	}
	if m.hasValidation {
		b.WriteString(" Validation:\n")
		for _, line := range strings.Split(m.workerValidation, "\n") {
			b.WriteString(fmt.Sprintf("   %s\n", line))
		}
	}
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf("\n Elapsed: %s\n", elapsed))

	// Token summary
	var totalIn, totalOut int64
	for _, a := range m.agents {
		totalIn += a.InputTokens
		totalOut += a.OutputTokens
	}
	if totalIn+totalOut > 0 {
		b.WriteString(fmt.Sprintf(" Tokens: %s in, %s out (%s total)\n",
			formatTokens(totalIn), formatTokens(totalOut), formatTokens(totalIn+totalOut)))
	}
	return b.String()
}

func (m Model) viewPlanEdit(_ int) string {
	var b strings.Builder
	b.WriteString(" Editing plan (Ctrl+S to save, Esc to discard):\n\n")
	if m.hasPlanEditor {
		b.WriteString(m.planEditor.View())
	}
	return b.String()
}

func (m Model) viewAgentHistory(_ int) string {
	var b strings.Builder
	if m.focusedAgent > 0 && m.focusedAgent <= len(m.agents) {
		a := m.agents[m.focusedAgent-1]
		b.WriteString(fmt.Sprintf(" Agent: %s (%s)\n", goalStyle.Render(a.ID), a.State))
		b.WriteString(dividerStyle.Render(strings.Repeat("─", 40)))
		b.WriteString("\n")
		b.WriteString(" (output history not captured in this mode)\n")
	} else {
		b.WriteString(" No agent selected\n")
	}
	return b.String()
}

func (m Model) viewDashboard() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" %-14s   %-7s %5s %8s %8s %7s  %s\n",
		"Agent", "State", "Time", "In Tok", "Out Tok", "Tok/s", "Context"))
	b.WriteString(strings.Repeat("─", m.effectiveWidth()))
	b.WriteString("\n")
	for _, a := range m.agents {
		icon := "○"
		stateStr := "wait"
		switch a.State {
		case "running":
			icon = "▶"
			stateStr = "run"
		case "done":
			icon = "✓"
			stateStr = "done"
		case "failed":
			icon = "✗"
			stateStr = "fail"
		case "cancelled":
			icon = "⊘"
			stateStr = "cancel"
		case "gate":
			icon = "●"
			stateStr = "gate"
		}

		elapsed := "-"
		if a.State == "running" && !a.StartedAt.IsZero() {
			elapsed = time.Since(a.StartedAt).Truncate(time.Second).String()
		} else if a.Elapsed > 0 {
			elapsed = a.Elapsed.Truncate(time.Second).String()
		}

		inTok := "-"
		outTok := "-"
		tokPS := "-"
		ctxBar := "░░░░░░░░  -"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			inTok = fmt.Sprintf("%s", formatTokenCount(a.InputTokens))
			outTok = fmt.Sprintf("%s", formatTokenCount(a.OutputTokens))
			if a.Elapsed.Seconds() > 0 {
				tokPS = fmt.Sprintf("%.1f", float64(a.OutputTokens)/a.Elapsed.Seconds())
			}
			// Context bar: estimate % of 200k context window
			pct := float64(a.InputTokens+a.OutputTokens) / 200000.0 * 100
			if pct > 100 {
				pct = 100
			}
			filled := int(pct / 12.5) // 8 chars total
			empty := 8 - filled
			if empty < 0 {
				empty = 0
			}
			ctxBar = fmt.Sprintf("%s%s %2.0f%%",
				strings.Repeat("█", filled),
				strings.Repeat("░", empty),
				pct)
		}

		b.WriteString(fmt.Sprintf(" %-14s %s %-7s %5s %8s %8s %7s  %s\n",
			a.ID, icon, stateStr, elapsed, inTok, outTok, tokPS, ctxBar))
	}

	return b.String()
}

// formatTokenCount formats a token count with comma separators.
func formatTokenCount(n int64) string {
	if n == 0 {
		return "-"
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func (m Model) viewHelp() string {
	return ` Orqestra Keybindings
─────────────────────────────────
 [Enter]      Submit prompt / confirm
 [Ctrl+S]     Skip gateway / save edits
 [Tab]        Next field (coaching)
 [Shift+Tab]  Previous field (coaching)
 [PgUp/PgDn]  Scroll content
 [A]          Accept plan
 [E]          Edit plan
 [S]          Stop/cancel running agent
 [N]          New run
 [D]          Toggle full dashboard
 [1-9]        View agent output
 [Esc]        Back / dismiss
 [?]          Toggle this help
 [Q]          Quit (at completion)
 [Ctrl+C x2]  Force quit
`
}

func (m Model) viewSidebar(width int) string {
	var b strings.Builder
	b.WriteString(" Agents\n")
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("\n")

	var totalTokens int64
	for _, a := range m.agents {
		icon := "○"
		switch a.State {
		case "running":
			icon = "▶"
		case "done":
			icon = "✓"
		case "failed":
			icon = "✗"
		case "cancelled":
			icon = "⊘"
		case "gate":
			icon = "●"
		}

		elapsed := "-"
		if a.State == "running" && !a.StartedAt.IsZero() {
			elapsed = time.Since(a.StartedAt).Truncate(time.Second).String()
		} else if a.Elapsed > 0 {
			elapsed = a.Elapsed.Truncate(time.Second).String()
		}

		tokens := "-"
		total := a.InputTokens + a.OutputTokens
		if total > 0 {
			tokens = formatTokens(total)
		}
		totalTokens += total

		// Truncate ID to fit sidebar
		id := a.ID
		maxID := width - 16
		if maxID < 4 {
			maxID = 4
		}
		if len(id) > maxID {
			id = id[:maxID]
		}

		b.WriteString(fmt.Sprintf(" %s %-*s %5s %6s\n", icon, maxID, id, elapsed, tokens))
	}
	if len(m.agents) == 0 {
		b.WriteString(" (waiting)\n")
	}

	// Totals row
	if totalTokens > 0 || len(m.agents) > 0 {
		b.WriteString(strings.Repeat("─", width))
		b.WriteString("\n")
		totalElapsed := time.Since(m.startTime).Truncate(time.Second)
		b.WriteString(fmt.Sprintf(" total: %s | %s\n", formatTokens(totalTokens), totalElapsed))
	}

	return b.String()
}

// formatTokens renders a token count in compact form (e.g., 1.2k, 12.4k, 128k).
func formatTokens(n int64) string {
	if n == 0 {
		return "-"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

func (m Model) viewFooter() string {
	switch m.content {
	case ContentCoaching:
		return keyStyle.Render(" [Enter] confirm | [Ctrl+S] skip | [Tab] next field       [?] help  [D] expand  [S] stop  [^C^C] quit")
	case ContentPlanReview:
		return keyStyle.Render(" [A] accept | [E] edit | [S] cancel                        [?] help  [D] expand  [1-9] agent  [^C^C] quit")
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard                       [?] help  [^C^C] quit")
	case ContentAgentHistory:
		return keyStyle.Render(" [Esc] back to live                                        [?] help  [D] expand  [S] stop  [N] new  [^C^C] quit")
	case ContentCompletion:
		return keyStyle.Render(" [N] new run | [Q] quit                                    [?] help")
	default:
		return keyStyle.Render(" [S] stop | [N] new run                                    [?] help  [D] expand  [1-9] agent  [^C^C] quit")
	}
}
