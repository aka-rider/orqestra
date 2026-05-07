package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// DashboardState tracks agent rows in the pipeline dashboard.
type DashboardState struct {
	Agents   []harness.AgentStats
	Selected int
}

// ClarificationState holds the clarification UI state.
type ClarificationState struct {
	Questions []string
	Answers   []string
	Current   int
}

// FailureState holds the failure screen state.
type FailureState struct {
	AgentID string
	Err     error
}

// QAState holds the QA result screen state.
type QAState struct {
	Report   *agent.ValidationReport
	ShowFull bool
}

// CompletionState holds the completion screen state.
type CompletionState struct {
	Result   orchestrator.Result
	ShowDiff bool
}

// Model is the top-level Bubble Tea model for the Orqestra TUI.
type Model struct {
	screen    Screen
	previous  Screen
	width     int
	height    int
	startTime time.Time
	goal      string
	phase     orchestrator.Phase

	// Sub-states
	prompt        textarea.Model
	clarification ClarificationState
	dashboard     DashboardState
	planOutput    *agent.PlanOutput
	failure       FailureState
	qa            QAState
	completion    CompletionState

	// Pipeline references
	engine *orchestrator.Engine
	ctrlC  int                // double-press counter
	cancel context.CancelFunc // cancels the running pipeline

	// Last error for display
	lastErr error
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
		screen:    ScreenPrompt,
		startTime: time.Now(),
		prompt:    ta,
		engine:    engine,
	}
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

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

	case RunCompleteMsg:
		if msg.Err != nil {
			m.screen = ScreenFailure
			m.failure = FailureState{AgentID: "pipeline", Err: msg.Err}
			return m, nil
		}
		m.screen = ScreenCompletion
		m.completion = CompletionState{Result: msg.Result}
		return m, nil
	}

	// Pass to sub-models
	switch m.screen {
	case ScreenPrompt:
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}

	return m, nil
}

// handleKey processes key events based on the current screen.
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

	switch m.screen {
	case ScreenPrompt:
		return m.handlePromptKey(msg)
	case ScreenClarification:
		return m.handleClarificationKey(msg)
	case ScreenDashboard:
		return m.handleDashboardKey(msg)
	case ScreenAgentDetail:
		return m.handleAgentDetailKey(msg)
	case ScreenPlanReview:
		return m.handlePlanReviewKey(msg)
	case ScreenFailure:
		return m.handleFailureKey(msg)
	case ScreenQAResult:
		return m.handleQAResultKey(msg)
	case ScreenCompletion:
		return m.handleCompletionKey(msg)
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
		m.screen = ScreenDashboard
		m.startTime = time.Now()
		return m, m.startPipeline(prompt)
	case tea.KeyCtrlD:
		m.ctrlC++
		if m.ctrlC >= 2 {
			return m, tea.Quit
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
}

func (m Model) handleClarificationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		answers := make([]ClarificationAnswer, len(m.clarification.Questions))
		for i, q := range m.clarification.Questions {
			answers[i] = ClarificationAnswer{Question: q, Answer: m.clarification.Answers[i]}
		}
		m.screen = ScreenDashboard
		return m, func() tea.Msg { return SubmitClarificationMsg{Answers: answers} }
	case tea.KeyEsc:
		m.screen = ScreenPrompt
		return m, nil
	}
	return m, nil
}

func (m Model) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.previous = m.screen
		m.screen = ScreenAgentDetail
		return m, nil
	case "s", "S":
		m.previous = m.screen
		m.screen = ScreenFailure
		m.failure = FailureState{AgentID: "pipeline", Err: fmt.Errorf("user stopped pipeline")}
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case "q", "Q":
		return m, tea.Quit
	}
	switch msg.Type {
	case tea.KeyUp:
		if m.dashboard.Selected > 0 {
			m.dashboard.Selected--
		}
	case tea.KeyDown:
		if m.dashboard.Selected < len(m.dashboard.Agents)-1 {
			m.dashboard.Selected++
		}
	}
	return m, nil
}

func (m Model) handleAgentDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.screen = ScreenDashboard
		return m, nil
	}
	switch msg.String() {
	case "f", "F":
		// Toggle follow mode (future: scroll to bottom)
		return m, nil
	case "s", "S":
		m.previous = m.screen
		m.screen = ScreenFailure
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handlePlanReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.screen = ScreenDashboard
		return m, func() tea.Msg { return ApprovePlanMsg{} }
	case "n", "N":
		// Pre-fill prompt with the prior goal and validator feedback
		m.screen = ScreenPrompt
		if m.planOutput != nil {
			m.prompt.SetValue(m.planOutput.Spec.Goal)
		}
		return m, func() tea.Msg { return RejectPlanMsg{Feedback: m.goal} }
	case "e", "E":
		return m, func() tea.Msg { return EditPlanMsg{} }
	}
	return m, nil
}

func (m Model) handleFailureKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r", "R":
		m.screen = ScreenDashboard
		return m, func() tea.Msg { return RetryAgentMsg{AgentID: m.failure.AgentID} }
	case "s", "S":
		m.screen = ScreenDashboard
		return m, func() tea.Msg { return SkipAgentMsg{AgentID: m.failure.AgentID} }
	case "a", "A":
		m.screen = ScreenCompletion
		m.completion = CompletionState{Result: orchestrator.Result{Status: orchestrator.StatusAborted}}
		return m, nil
	}
	return m, nil
}

func (m Model) handleQAResultKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "f", "F":
		m.screen = ScreenDashboard
		return m, func() tea.Msg { return RepairWorkMsg{} }
	case "a", "A":
		m.screen = ScreenCompletion
		m.completion = CompletionState{Result: orchestrator.Result{Status: orchestrator.StatusAcceptedWithWarn}}
		return m, nil
	case "r", "R":
		m.qa.ShowFull = !m.qa.ShowFull
		return m, nil
	}
	return m, nil
}

func (m Model) handleCompletionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Reset for new task
		m.screen = ScreenPrompt
		m.prompt.Reset()
		m.goal = ""
		m.planOutput = nil
		m.dashboard = DashboardState{}
		return m, nil
	case "d", "D":
		m.completion.ShowDiff = !m.completion.ShowDiff
		return m, nil
	case "q", "Q":
		return m, tea.Quit
	}
	return m, nil
}

// handleOrchestratorEvent updates the model based on orchestrator events.
func (m Model) handleOrchestratorEvent(event orchestrator.Event) (tea.Model, tea.Cmd) {
	switch event.Type {
	case orchestrator.EventPhaseChange:
		m.phase = event.Phase
	case orchestrator.EventPlanReady:
		m.planOutput = event.PlanOutput
		m.screen = ScreenPlanReview
	case orchestrator.EventQADone:
		m.qa.Report = event.QAReport
		if event.QAReport.Verdict != agent.VerdictPass {
			m.screen = ScreenQAResult
		}
	case orchestrator.EventAgentFailed:
		m.screen = ScreenFailure
		m.failure = FailureState{AgentID: event.AgentID, Err: event.Err}
	case orchestrator.EventComplete:
		m.screen = ScreenCompletion
	}
	return m, nil
}

// startPipeline launches the orchestrator in a goroutine and sends events as tea.Msg.
func (m Model) startPipeline(prompt string) tea.Cmd {
	engine := m.engine
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		_ = cancel // stored on model via OrchestratorEventMsg in production
		result, err := engine.Run(
			ctx,
			orchestrator.Input{Prompt: prompt},
			func(event orchestrator.Event) {
				// In production: p.Send(OrchestratorEventMsg{Event: event})
			},
		)
		return RunCompleteMsg{Result: result, Err: err}
	}
}

// View renders the current screen.
func (m Model) View() string {
	var b strings.Builder

	// Header
	b.WriteString(m.viewHeader())
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n")

	// Content
	b.WriteString(m.viewContent())

	// Footer
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", m.effectiveWidth())))
	b.WriteString("\n")
	b.WriteString(m.viewFooter())

	return b.String()
}

func (m Model) effectiveWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

func (m Model) viewHeader() string {
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	title := headerStyle.Render(" Orqestra v3")
	phase := ""
	if m.phase != "" {
		phase = phaseStyle.Render(fmt.Sprintf("▶ %s", m.phase))
	} else {
		switch m.screen {
		case ScreenPrompt:
			phase = phaseStyle.Render("ready")
		case ScreenPlanReview:
			phase = phaseStyle.Render("○ review plan")
		case ScreenCompletion:
			phase = phaseStyle.Render("✓ complete")
		case ScreenFailure:
			phase = failStyle.Render("✗ failed")
		case ScreenQAResult:
			phase = phaseStyle.Render("○ QA review")
		}
	}
	time := elapsedStyle.Render(elapsed.String())
	return fmt.Sprintf("%s  %s  %s", title, phase, time)
}

func (m Model) viewContent() string {
	switch m.screen {
	case ScreenPrompt:
		return m.viewPrompt()
	case ScreenClarification:
		return m.viewClarification()
	case ScreenDashboard:
		return m.viewDashboard()
	case ScreenAgentDetail:
		return m.viewAgentDetail()
	case ScreenPlanReview:
		return m.viewPlanReview()
	case ScreenFailure:
		return m.viewFailure()
	case ScreenQAResult:
		return m.viewQAResult()
	case ScreenCompletion:
		return m.viewCompletion()
	}
	return ""
}

func (m Model) viewPrompt() string {
	return " Enter a task description. Be specific about the end state.\n\n" + m.prompt.View()
}

func (m Model) viewClarification() string {
	var b strings.Builder
	b.WriteString(" The following needs clarification:\n\n")
	for i, q := range m.clarification.Questions {
		marker := "  "
		if i == m.clarification.Current {
			marker = "> "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, q))
	}
	return b.String()
}

func (m Model) viewDashboard() string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	if len(m.dashboard.Agents) == 0 {
		b.WriteString(" Pipeline running...\n")
	} else {
		for i, a := range m.dashboard.Agents {
			marker := "  "
			if i == m.dashboard.Selected {
				marker = "▶ "
			}
			b.WriteString(fmt.Sprintf("%s%-12s | %5d in | %5d out | %.1f tok/s | %.0f%% ctx\n",
				marker, a.AgentID, a.InputTokens, a.OutputTokens, a.ThroughputPS, a.CtxPercent))
		}
	}
	return b.String()
}

func (m Model) viewAgentDetail() string {
	if m.dashboard.Selected >= len(m.dashboard.Agents) {
		return " No agent selected.\n"
	}
	a := m.dashboard.Agents[m.dashboard.Selected]
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" ▶ %s | %d in | %d out | %.1f tok/s | %.0f%% ctx\n\n",
		a.AgentID, a.InputTokens, a.OutputTokens, a.ThroughputPS, a.CtxPercent))
	b.WriteString(" Tool Calls:\n")
	for _, tc := range a.ToolCalls {
		b.WriteString(fmt.Sprintf("   %s (%s)\n", tc.Name, tc.Duration))
	}
	return b.String()
}

func (m Model) viewPlanReview() string {
	if m.planOutput == nil {
		return " Waiting for plan...\n"
	}
	spec := m.planOutput.Spec
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(spec.Goal)))
	b.WriteString(" Steps:\n")
	for i, s := range spec.Steps {
		b.WriteString(fmt.Sprintf("   %d. %s\n", i+1, stepStyle.Render(s)))
	}
	b.WriteString("\n Acceptance Criteria:\n")
	for _, a := range spec.Acceptance {
		b.WriteString(fmt.Sprintf("   • %s\n", a))
	}
	return b.String()
}

func (m Model) viewFailure() string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	b.WriteString(errorStyle.Render(fmt.Sprintf(" Error: %v\n", m.failure.Err)))
	b.WriteString(fmt.Sprintf("\n Agent: %s\n", m.failure.AgentID))
	return b.String()
}

func (m Model) viewQAResult() string {
	if m.qa.Report == nil {
		return " Waiting for QA results...\n"
	}
	var b strings.Builder
	verdictStyle := warnStyle
	if m.qa.Report.Verdict == agent.VerdictFail {
		verdictStyle = failStyle
	}
	b.WriteString(fmt.Sprintf(" QA Gate: %s\n\n", verdictStyle.Render(string(m.qa.Report.Verdict))))
	b.WriteString(fmt.Sprintf(" Summary: %s\n\n", m.qa.Report.Summary))
	if m.qa.ShowFull {
		for _, issue := range m.qa.Report.Issues {
			tag := "info"
			if issue.Blocking {
				tag = "blocker"
			}
			b.WriteString(fmt.Sprintf("   [%s] %s: %s\n", tag, issue.ID, issue.Message))
		}
	}
	return b.String()
}

func (m Model) viewCompletion() string {
	var b strings.Builder
	if m.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(m.goal)))
	}
	status := m.completion.Result.Status
	statusStyle := passStyle
	switch status {
	case orchestrator.StatusFailed, orchestrator.StatusAborted:
		statusStyle = failStyle
	case orchestrator.StatusAcceptedWithWarn:
		statusStyle = warnStyle
	}
	b.WriteString(fmt.Sprintf(" Status: %s\n", statusStyle.Render(string(status))))
	if m.completion.Result.RunDir != "" {
		b.WriteString(fmt.Sprintf(" Run: %s\n", m.completion.Result.RunDir))
	}
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf(" Elapsed: %s\n", elapsed))
	return b.String()
}

func (m Model) viewFooter() string {
	switch m.screen {
	case ScreenPrompt:
		return keyStyle.Render(" [Enter] submit | [Ctrl+C Ctrl+C] exit")
	case ScreenClarification:
		return keyStyle.Render(" [↑↓] navigate | [Space] toggle | [Tab] next | [Enter] submit | [Esc] back")
	case ScreenDashboard:
		return keyStyle.Render(" [Enter] expand agent | [S] stop | [Ctrl+C Ctrl+C] exit")
	case ScreenAgentDetail:
		return keyStyle.Render(" [↑↓] scroll | [F] follow | [S] stop | [Esc] back")
	case ScreenPlanReview:
		return keyStyle.Render(" [Y] approve | [N] reject | [E] edit | [↑↓] scroll")
	case ScreenFailure:
		return keyStyle.Render(" [R] retry | [S] skip | [A] abort")
	case ScreenQAResult:
		return keyStyle.Render(" [A] accept anyway | [F] fix | [R] full report | [↑↓] scroll")
	case ScreenCompletion:
		return keyStyle.Render(" [Enter] new task | [D] diff detail | [Q] quit")
	}
	return ""
}
