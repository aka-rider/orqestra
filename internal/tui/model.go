package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
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
	ID      string
	State   string // "running", "done", "waiting", "failed", "cancelled", "gate"
	Elapsed time.Duration
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
	gatewayResult agent.GatewayResult
	planOutput    agent.PlanOutput
	hasPlan       bool
	qaReport      agent.ValidationReport
	hasQA         bool
	lastErr       error

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
}

// NewModel creates the initial TUI model.
func NewModel(engine *orchestrator.Engine) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter a task description. Be specific about the end state."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.CharLimit = 4096

	return Model{
		state:     StatePrompt,
		startTime: time.Now(),
		prompt:    ta,
		engine:    engine,
	}
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	return textarea.Blink
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
		m.prompt.SetWidth(msg.Width - 4)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

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
		ta := textarea.New()
		ta.SetWidth(m.effectiveWidth() - 10)
		ta.SetHeight(m.height - 8)
		ta.CharLimit = 65536
		if m.hasPlan {
			content, _ := json.MarshalIndent(m.planOutput, "", "  ")
			ta.SetValue(string(content))
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
		m.agents = append(m.agents, AgentRow{ID: event.AgentID, State: "running"})

	case orchestrator.EventAgentDone:
		for i := range m.agents {
			if m.agents[i].ID == event.AgentID {
				m.agents[i].State = "done"
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
		switch event.Gate.Type {
		case orchestrator.GateGatewayCoach:
			m.content = ContentCoaching
			m.gatewayResult = event.Gate.GatewayResult
			// Create answer fields pre-filled with defaults
			m.answerFields = make([]textarea.Model, len(m.gatewayResult.Questions))
			for i, q := range m.gatewayResult.Questions {
				ta := textarea.New()
				ta.SetWidth(m.effectiveWidth() - 10)
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
			m.planOutput = event.Gate.PlanOutput
			m.hasPlan = true
		}

	case orchestrator.EventPlanReady:
		m.planOutput = event.PlanOutput
		m.hasPlan = true

	case orchestrator.EventQADone:
		if event.HasQA {
			m.qaReport = event.QAReport
			m.hasQA = true
		}

	case orchestrator.EventComplete:
		m.content = ContentCompletion
		if event.HasQA {
			m.qaReport = event.QAReport
			m.hasQA = true
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

	return waitForEvent(channels.Events)
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
	if m.width > 0 {
		return m.width
	}
	return 80
}

func (m Model) viewPromptScreen() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(" Orqestra"))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n\n")
	b.WriteString(" Enter a task description. Be specific about the end state.\n\n")
	b.WriteString(m.prompt.View())
	b.WriteString("\n\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render(" [Enter] submit | [Ctrl+S] skip gateway | [^C^C] quit"))
	return b.String()
}

func (m Model) viewPipelineScreen() string {
	// Help overlay takes precedence
	if m.showHelp {
		return m.viewHelp()
	}

	// Full dashboard override
	if m.showDashboard {
		return m.viewDashboard()
	}

	var b strings.Builder

	// Header
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	title := headerStyle.Render(" Orqestra")
	phase := phaseStyle.Render(fmt.Sprintf("▶ %s", m.phase))
	timeStr := elapsedStyle.Render(elapsed.String())
	b.WriteString(fmt.Sprintf("%s  %s  %s\n", title, phase, timeStr))
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n")

	// Split: content (75%) | sidebar (25%)
	contentWidth := m.effectiveWidth() * 3 / 4
	sidebarWidth := m.effectiveWidth() - contentWidth - 1 // -1 for separator

	contentView := m.viewContent(contentWidth)
	sidebarView := m.viewSidebar(sidebarWidth)

	// Join horizontally
	contentLines := strings.Split(contentView, "\n")
	sidebarLines := strings.Split(sidebarView, "\n")

	maxLines := len(contentLines)
	if len(sidebarLines) > maxLines {
		maxLines = len(sidebarLines)
	}

	for i := 0; i < maxLines; i++ {
		cl := ""
		if i < len(contentLines) {
			cl = contentLines[i]
		}
		sl := ""
		if i < len(sidebarLines) {
			sl = sidebarLines[i]
		}
		// Pad content to width
		if len(cl) < contentWidth {
			cl += strings.Repeat(" ", contentWidth-len(cl))
		}
		b.WriteString(fmt.Sprintf("%s│%s\n", cl, sl))
	}

	// Footer
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n")
	b.WriteString(m.viewFooter())

	return b.String()
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

func (m Model) viewStreaming(_ int) string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	b.WriteString(fmt.Sprintf(" Phase: %s\n", m.phase))
	if m.lastErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("\n Error: %v\n", m.lastErr)))
	}
	if m.confirmNew {
		b.WriteString(warnStyle.Render("\n Pipeline is active. Start new run? [Y] yes / [any] cancel"))
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

func (m Model) viewPlanReview(_ int) string {
	if !m.hasPlan {
		return " Waiting for plan...\n"
	}
	spec := m.planOutput.Spec
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(spec.Goal)))
	b.WriteString(" Steps:\n")
	for i, s := range spec.Steps {
		b.WriteString(fmt.Sprintf("   %d. %s\n", i+1, stepStyle.Render(s)))
	}
	b.WriteString("\n Acceptance:\n")
	for _, a := range spec.Acceptance {
		b.WriteString(fmt.Sprintf("   • %s\n", a))
	}
	return b.String()
}

func (m Model) viewCompletion(_ int) string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	if m.hasQA {
		verdictStyle := passStyle
		if m.qaReport.Verdict == agent.VerdictFail {
			verdictStyle = failStyle
		} else if m.qaReport.Verdict == agent.VerdictWarn {
			verdictStyle = warnStyle
		}
		b.WriteString(fmt.Sprintf(" QA: %s\n", verdictStyle.Render(string(m.qaReport.Verdict))))
		b.WriteString(fmt.Sprintf(" %s\n", m.qaReport.Summary))
	}
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf("\n Elapsed: %s\n", elapsed))
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
	b.WriteString(" Agent          State     Time\n")
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
		if a.Elapsed > 0 {
			elapsed = a.Elapsed.Truncate(time.Second).String()
		}
		b.WriteString(fmt.Sprintf(" %-14s %s %-8s %s\n", a.ID, icon, stateStr, elapsed))
	}
	b.WriteString("\n Press [Esc] to return to split view\n")
	return b.String()
}

func (m Model) viewHelp() string {
	return ` Orqestra Keybindings
─────────────────────────────────
 [Enter]      Submit prompt / confirm
 [Ctrl+S]     Skip gateway / save edits
 [Tab]        Next field (coaching)
 [Shift+Tab]  Previous field (coaching)
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
		b.WriteString(fmt.Sprintf(" %s %s\n", icon, a.ID))
	}
	if len(m.agents) == 0 {
		b.WriteString(" (waiting)\n")
	}
	return b.String()
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
