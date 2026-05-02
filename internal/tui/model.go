package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// State represents the current phase of the TUI.
type State int

const (
	StateIdle          State = iota // Command bar focused, waiting for input
	StateIntentConfirm              // Show rephrased intent, [A]pprove/[R]eject
	StatePlanning                   // Planning tab active, claude running
	StateValidating                 // Plan validation in progress
	StateConfirming                 // Plan ready, waiting for [A]pprove/[R]eject
	StateExecuting                  // Worker tab active, claude running
	StateDone                       // Everything finished, will cycle back to idle
)

// PipelineFuncs holds the functions the TUI drives.
type PipelineFuncs struct {
	// Plan runs the planner and streams output. Returns the spec.
	Plan func(ctx context.Context, prompt string, stdout io.Writer) (types.Specification, error)
	// ValidatePlan runs plan validation. Returns nil report if validation is disabled.
	ValidatePlan func(ctx context.Context, spec types.Specification) (*types.ValidationReport, error)
	// Execute runs the worker and streams output.
	Execute func(ctx context.Context, spec types.Specification, stdout io.Writer) error
	// ValidateWork runs work validation. Returns nil report if validation is disabled.
	ValidateWork func(ctx context.Context, spec types.Specification, workOutput string) (*types.ValidationReport, error)
	// SessionManager is the shared session manager whose events drive TUI tabs.
	// If non-nil, sessions auto-create and update tabs.
	SessionManager *harness.SessionManager
	// Send delivers a tea.Msg into the TUI event loop. Wired after program creation.
	Send func(tea.Msg)
}

// Model is the main Bubble Tea model that drives the full pipeline.
type Model struct {
	state    State
	spec     types.Specification
	approved bool
	prompt   string // current prompt from command bar

	tabsView    tabsView
	confirmView confirmView
	commandBar  commandBarModel
	registry    *CommandRegistry
	logPanel    *logPanel
	showLogs    bool

	// Help/intent ephemeral content displayed in tab area
	helpContent   string
	intentContent string

	pipeline PipelineFuncs
	program  *tea.Program // set after program creation for execution coordination
	ctx      context.Context
	cancel   context.CancelFunc

	width  int
	height int
	err    error

	planTabIdx int
	execTabIdx int

	// Session->tab mapping: sessionID -> tab index
	sessionTabs map[string]int
}

// NewModel creates the main TUI model starting at StateIdle.
func NewModel(pipeline PipelineFuncs) Model {
	ctx, cancel := context.WithCancel(context.Background())
	registry := NewCommandRegistry()
	RegisterBuiltins(registry)

	m := Model{
		state:       StateIdle,
		tabsView:    newTabsView(),
		confirmView: newConfirmView(),
		commandBar:  newCommandBar(registry),
		registry:    registry,
		logPanel:    newLogPanel(),
		showLogs:    false,
		pipeline:    pipeline,
		ctx:         ctx,
		cancel:      cancel,
		planTabIdx:  -1,
		execTabIdx:  -1,
		sessionTabs: make(map[string]int),
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return spinner.New().Tick
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		commandBarHeight := 3
		logHeight := 0
		if m.showLogs {
			logHeight = 8
		}
		tabHeight := m.height - commandBarHeight - logHeight

		if m.showLogs {
			m.logPanel.SetWidth(m.width)
			m.logPanel.SetHeight(logHeight)
		}

		m.commandBar.SetWidth(m.width)

		tabMsg := tea.WindowSizeMsg{Width: m.width, Height: tabHeight}
		tv, cmd := m.tabsView.Update(tabMsg)
		m.tabsView = tv
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)

	case PromptSubmitMsg:
		return m.handlePromptSubmit(msg)

	case CommandMsg:
		return m.handleCommand(msg)

	case ToggleLogsMsg:
		m.showLogs = !m.showLogs
		if m.width > 0 {
			return m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
		return m, nil

	case CycleBackToIdleMsg:
		m.state = StateIdle
		m.commandBar.SetState(StateIdle)
		m.commandBar.Focus()
		m.confirmView = newConfirmView()
		m.helpContent = ""
		m.intentContent = ""
		return m, nil

	case PlanReadyMsg:
		m.state = StateConfirming
		m.commandBar.SetState(StateConfirming)
		return m, nil

	case addTabMsg:
		idx := m.tabsView.AddTab(msg.name)
		if m.state == StatePlanning && m.planTabIdx == -1 {
			m.planTabIdx = idx
			m.tabsView.active = idx
		}
		return m, nil

	case planCompleteMsg:
		m.spec = msg.spec
		if m.pipeline.ValidatePlan == nil {
			m.state = StateConfirming
			m.commandBar.SetState(StateConfirming)
			return m, nil
		}
		m.state = StateValidating
		m.commandBar.SetState(StateValidating)
		go m.startPlanValidation()
		return m, nil

	case PlanValidatedMsg:
		if msg.Err != nil {
			slog.Warn("plan validation error, proceeding to confirm", "err", msg.Err)
			m.state = StateConfirming
			m.commandBar.SetState(StateConfirming)
			return m, nil
		}
		if msg.Report != nil && msg.Report.Verdict == types.VerdictFail {
			m.err = fmt.Errorf("plan validation failed: %s", msg.Report.Summary)
			m.state = StateDone
			m.commandBar.SetState(StateDone)
			return m, nil
		}
		if msg.Report != nil && msg.Report.Verdict == types.VerdictWarn {
			slog.Warn("plan validation warnings", "summary", msg.Report.Summary)
		}
		m.state = StateConfirming
		m.commandBar.SetState(StateConfirming)
		return m, nil

	case ConfirmMsg:
		m.approved = msg.Approved
		if !msg.Approved {
			m.state = StateDone
			m.commandBar.SetState(StateDone)
			return m, func() tea.Msg { return CycleBackToIdleMsg{} }
		}
		m.state = StateExecuting
		m.commandBar.SetState(StateExecuting)
		if m.pipeline.SessionManager != nil && m.program != nil {
			go StartExecutionSession(m.program, m.ctx, m.pipeline.SessionManager, m.spec, m.pipeline)
		} else if m.program != nil {
			m.execTabIdx = m.tabsView.AddTab("Worker")
			m.tabsView.active = m.execTabIdx
			go RunExecution(m.program, m.ctx, m.pipeline, m.spec, m.execTabIdx)
		}
		return m, nil

	case IntentConfirmMsg:
		m.state = StatePlanning
		m.commandBar.SetState(StatePlanning)
		m.intentContent = ""
		if m.planTabIdx == -1 {
			m.planTabIdx = m.tabsView.AddTab("Planner")
			m.sessionTabs["plan"] = m.planTabIdx
		}
		m.tabsView.active = m.planTabIdx
		go m.startPlanning()
		return m, nil

	case IntentRejectMsg:
		m.state = StateIdle
		m.commandBar.SetState(StateIdle)
		m.commandBar.Focus()
		m.intentContent = ""
		return m, nil

	case StreamChunkMsg:
		if msg.SessionID != "" {
			if idx, ok := m.sessionTabs[msg.SessionID]; ok {
				msg.TabIndex = idx
			}
		}
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd

	case HarnessDoneMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		if msg.TabIndex == m.execTabIdx || (m.state == StateExecuting && m.execTabIdx == -1) {
			if msg.Err != nil {
				m.state = StateDone
				m.commandBar.SetState(StateDone)
				m.err = msg.Err
				return m, nil
			} else if m.pipeline.ValidateWork != nil && msg.WorkOutput != "" {
				go m.startWorkValidation(msg.WorkOutput)
			} else {
				m.state = StateDone
				m.commandBar.SetState(StateDone)
				return m, func() tea.Msg { return CycleBackToIdleMsg{} }
			}
		}
		return m, cmd

	case WorkValidatedMsg:
		if msg.Err != nil {
			slog.Warn("work validation error", "err", msg.Err)
		}
		if msg.Report != nil && msg.Report.Verdict == types.VerdictFail {
			m.err = fmt.Errorf("work validation failed: %s", msg.Report.Summary)
		} else if msg.Report != nil && msg.Report.Verdict == types.VerdictWarn {
			slog.Warn("work validation warnings", "summary", msg.Report.Summary)
		}
		m.state = StateDone
		m.commandBar.SetState(StateDone)
		if m.err != nil {
			return m, nil
		}
		return m, func() tea.Msg { return CycleBackToIdleMsg{} }

	case LogMsg:
		m.logPanel.Add(msg.Entry)
		return m, nil

	case SessionEventMsg:
		return m.handleSessionEvent(msg.Event)

	case SandboxStateMsg:
		m.logPanel.Add(LogEntry{
			Time:    time.Now(),
			Level:   "INFO",
			Message: fmt.Sprintf("sandbox %s: %s", msg.SandboxID[:8], msg.State),
		})
		return m, nil

	case ErrorMsg:
		m.err = msg.Err
		m.state = StateDone
		m.commandBar.SetState(StateDone)
		return m, nil

	case TokenLimitExceededMsg:
		m.err = msg.Err
		m.state = StateDone
		m.commandBar.SetState(StateDone)
		// Stay visible — don't cycle back to idle so the user sees the budget error
		return m, nil

	case setProgramMsg:
		m.program = msg.program
		return m, nil

	case spinner.TickMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd
	}

	return m, nil
}

// handleKey routes keystrokes based on state.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global: ctrl+c always quits
	if key == "ctrl+c" {
		m.cancel()
		return m, tea.Quit
	}

	// Alt+N for tab switching (always available)
	switch key {
	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd
	}

	// In confirming/intent states, A/R keys take priority
	if m.state == StateConfirming {
		switch key {
		case "a", "A", "y", "Y":
			cv, cmd := m.confirmView.Update(msg)
			m.confirmView = cv
			return m, cmd
		case "r", "R", "n", "N":
			cv, cmd := m.confirmView.Update(msg)
			m.confirmView = cv
			return m, cmd
		}
	}

	if m.state == StateIntentConfirm {
		switch key {
		case "a", "A":
			return m, func() tea.Msg { return IntentConfirmMsg{} }
		case "r", "R":
			return m, func() tea.Msg { return IntentRejectMsg{} }
		}
	}

	// In StateDone with error, any key dismisses back to idle
	if m.state == StateDone && m.err != nil {
		m.err = nil
		return m, func() tea.Msg { return CycleBackToIdleMsg{} }
	}

	// Everything else goes to command bar
	cb, cmd := m.commandBar.Update(msg)
	m.commandBar = cb
	return m, cmd
}

// handlePromptSubmit processes a prompt submission from the command bar.
func (m Model) handlePromptSubmit(msg PromptSubmitMsg) (tea.Model, tea.Cmd) {
	if m.state != StateIdle {
		return m, nil
	}

	m.prompt = msg.Prompt
	m.helpContent = ""

	// Phase 1: Skip intent recognition, go directly to planning
	m.state = StatePlanning
	m.commandBar.SetState(StatePlanning)

	// Create planner tab if needed
	if m.planTabIdx == -1 {
		m.planTabIdx = m.tabsView.AddTab("Planner")
		m.sessionTabs["plan"] = m.planTabIdx
	} else {
		// Reset existing planner tab for new run
		m.tabsView.tabs[m.planTabIdx] = newStreamView(m.planTabIdx)
		if m.tabsView.width > 0 {
			m.tabsView.tabs[m.planTabIdx].SetSize(m.tabsView.width, m.tabsView.height-3)
		}
		m.tabsView.tabNames[m.planTabIdx] = "Planner"
	}
	m.tabsView.active = m.planTabIdx

	if m.program != nil {
		go m.startPlanning()
	}
	return m, nil
}

// handleCommand processes a slash command.
func (m Model) handleCommand(msg CommandMsg) (tea.Model, tea.Cmd) {
	switch msg.Name {
	case "/help":
		m.helpContent = renderHelp(m.registry, m.state, msg.Args)
		return m, nil
	case "/status":
		m.helpContent = statusStyle.Render("State: " + stateName(m.state))
		return m, nil
	case "/clear":
		m.helpContent = ""
		if m.state == StateIdle && len(m.tabsView.tabs) > 0 && m.tabsView.active < len(m.tabsView.tabs) {
			m.tabsView.tabs[m.tabsView.active] = newStreamView(m.tabsView.active)
			if m.tabsView.width > 0 {
				m.tabsView.tabs[m.tabsView.active].SetSize(m.tabsView.width, m.tabsView.height-3)
			}
		}
		return m, nil
	case "/abort":
		if m.state == StatePlanning || m.state == StateExecuting {
			m.cancel()
			// Create new context for next cycle
			m.ctx, m.cancel = context.WithCancel(context.Background())
			m.state = StateIdle
			m.commandBar.SetState(StateIdle)
			m.commandBar.Focus()
		}
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	var topView string

	if m.helpContent != "" {
		topView = m.tabsView.View() + "\n\n" + m.helpContent
	} else if m.intentContent != "" {
		topView = m.intentContent
	} else {
		topView = m.tabsView.View()
	}

	if m.state == StateConfirming {
		topView += "\n\n" + m.confirmView.View()
	}

	if m.state == StateValidating {
		topView += "\n\n" + statusStyle.Render("⟳ Validating plan...")
	}

	if m.state == StateDone {
		if !m.approved {
			topView += "\n\n" + titleStyle.Render("Plan rejected.")
		} else if m.err != nil {
			topView += "\n\n" + errorStyle.Render("✗ Error: "+m.err.Error()) + "\n" + dimStyle.Render("  press any key to dismiss")
		} else {
			topView += "\n\n" + goalStyle.Render("✓ Complete")
		}
	}

	if m.state == StateIdle && len(m.tabsView.tabs) == 0 {
		topView = titleStyle.Render("orqestra") + "\n\n" +
			subtitleStyle.Render("Type a prompt to start planning, or /help for commands.")
	}

	// Log panel (only if toggled on)
	var logView string
	if m.showLogs {
		logView = m.logPanel.View()
	}

	// Autocomplete overlay
	acView := m.commandBar.ViewAutocomplete()

	// Command bar
	cmdBarView := m.commandBar.View()

	var parts []string
	parts = append(parts, topView)
	if logView != "" {
		parts = append(parts, logView)
	}
	if acView != "" {
		parts = append(parts, acView)
	}
	parts = append(parts, cmdBarView)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// IsApproved returns whether the user approved the plan.
func (m Model) IsApproved() bool {
	return m.approved
}

// Error returns any error from execution.
func (m Model) Error() error {
	return m.err
}

// Spec returns the spec produced by planning.
func (m Model) Spec() types.Specification {
	return m.spec
}

// SetSpec sets the spec after planning completes.
func (m *Model) SetSpec(spec types.Specification) {
	m.spec = spec
}

// handleSessionEvent processes a harness session lifecycle event.
func (m Model) handleSessionEvent(evt harness.SessionEvent) (tea.Model, tea.Cmd) {
	tabIdx, exists := m.sessionTabs[evt.SessionID]

	switch evt.State {
	case harness.SessionPending:
		if exists {
			return m, nil
		}
		idx := m.tabsView.AddTab(evt.Name)
		m.sessionTabs[evt.SessionID] = idx
		m.tabsView.active = idx
		return m, nil

	case harness.SessionRunning:
		if exists {
			m.tabsView.tabNames[tabIdx] = evt.Name + " ⟳"
		}
		return m, nil

	case harness.SessionDone:
		if exists {
			m.tabsView.tabNames[tabIdx] = evt.Name + " ✓"
			if tabIdx < len(m.tabsView.tabs) {
				m.tabsView.tabs[tabIdx].done = true
			}
		}
		if m.state == StateExecuting && evt.SessionID != "plan" {
			m.state = StateDone
			m.commandBar.SetState(StateDone)
			return m, func() tea.Msg { return CycleBackToIdleMsg{} }
		}
		return m, nil

	case harness.SessionFailed:
		if exists {
			m.tabsView.tabNames[tabIdx] = evt.Name + " ✗"
			if tabIdx < len(m.tabsView.tabs) {
				m.tabsView.tabs[tabIdx].done = true
				m.tabsView.tabs[tabIdx].err = evt.Err
			}
		}
		if m.state == StateExecuting && evt.SessionID != "plan" {
			m.state = StateDone
			m.commandBar.SetState(StateDone)
			m.err = evt.Err
			return m, func() tea.Msg { return CycleBackToIdleMsg{} }
		}
		return m, nil
	}

	return m, nil
}

// TabIndexForSession returns the tab index for a given session ID, or -1 if not found.
func (m Model) TabIndexForSession(sessionID string) int {
	idx, ok := m.sessionTabs[sessionID]
	if !ok {
		return -1
	}
	return idx
}

// startPlanning runs the pipeline.Plan function in a goroutine, streaming
// output into the Planner tab.
func (m Model) startPlanning() {
	p := m.program
	pw := &sessionWriter{program: p, sessionID: "plan"}

	p.Send(SessionEventMsg{Event: harness.SessionEvent{
		SessionID: "plan",
		Name:      "Planner",
		State:     harness.SessionRunning,
	}})

	spec, err := m.pipeline.Plan(m.ctx, m.prompt, pw)
	if err != nil {
		p.Send(SessionEventMsg{Event: harness.SessionEvent{
			SessionID: "plan",
			Name:      "Planner",
			State:     harness.SessionFailed,
			Err:       err,
		}})
		p.Send(ErrorMsg{Err: err})
		return
	}

	p.Send(SessionEventMsg{Event: harness.SessionEvent{
		SessionID: "plan",
		Name:      "Planner",
		State:     harness.SessionDone,
	}})
	p.Send(planCompleteMsg{spec: spec})
}

// startPlanValidation runs ValidatePlan in a goroutine and sends the result.
func (m Model) startPlanValidation() {
	p := m.program
	if m.pipeline.ValidatePlan == nil {
		p.Send(PlanValidatedMsg{})
		return
	}

	slog.Info("validating plan", "goal", m.spec.Goal)
	report, err := m.pipeline.ValidatePlan(m.ctx, m.spec)
	p.Send(PlanValidatedMsg{Report: report, Err: err})
}

// startWorkValidation runs ValidateWork in a goroutine and sends the result.
func (m Model) startWorkValidation(workOutput string) {
	p := m.program
	if m.pipeline.ValidateWork == nil {
		p.Send(WorkValidatedMsg{})
		return
	}

	slog.Info("validating work output")
	report, err := m.pipeline.ValidateWork(m.ctx, m.spec, workOutput)
	p.Send(WorkValidatedMsg{Report: report, Err: err})
}
