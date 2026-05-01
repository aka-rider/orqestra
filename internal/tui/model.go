package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// State represents the current phase of the TUI.
type State int

const (
	StatePlanning   State = iota // Planning tab active, claude running
	StateValidating              // Plan validation in progress
	StateConfirming              // Plan ready, waiting for y/N
	StateExecuting               // Worker tab active, claude running
	StateDone                    // Everything finished
)

// PipelineFuncs holds the functions the TUI drives.
type PipelineFuncs struct {
	// Plan runs the planner and streams output. Returns the spec.
	Plan func(ctx context.Context, stdout io.Writer) (types.Specification, error)
	// ValidatePlan runs plan validation. Returns nil report if validation is disabled.
	ValidatePlan func(ctx context.Context, spec types.Specification) (*types.ValidationReport, error)
	// Execute runs the worker and streams output.
	Execute func(ctx context.Context, spec types.Specification, stdout io.Writer) error
	// ValidateWork runs work validation. Returns nil report if validation is disabled.
	ValidateWork func(ctx context.Context, spec types.Specification, workOutput string) (*types.ValidationReport, error)
	// SessionManager is the shared session manager whose events drive TUI tabs.
	// If non-nil, sessions auto-create and update tabs.
	SessionManager *harness.SessionManager
}

// Model is the main Bubble Tea model that drives the full pipeline.
type Model struct {
	state    State
	spec     types.Specification
	approved bool

	tabsView    tabsView
	confirmView confirmView
	logPanel    *logPanel

	pipeline PipelineFuncs
	program  *tea.Program // set after program creation for execution coordination
	ctx      context.Context
	cancel   context.CancelFunc

	width  int
	height int
	err    error

	planTabIdx int
	execTabIdx int

	// Session→tab mapping: sessionID → tab index
	sessionTabs map[string]int
}

// NewModel creates the main TUI model.
func NewModel(pipeline PipelineFuncs) Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := Model{
		state:       StatePlanning,
		tabsView:    newTabsView(),
		confirmView: newConfirmView(),
		logPanel:    newLogPanel(),
		pipeline:    pipeline,
		ctx:         ctx,
		cancel:      cancel,
		planTabIdx:  -1,
		execTabIdx:  -1,
		sessionTabs: make(map[string]int),
	}
	// Create the Planner tab immediately so UI renders from the start
	m.planTabIdx = m.tabsView.AddTab("Planner")
	m.sessionTabs["plan"] = m.planTabIdx
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

		logHeight := 8
		tabHeight := m.height - logHeight

		m.logPanel.SetWidth(m.width)
		m.logPanel.SetHeight(logHeight)

		tabMsg := tea.WindowSizeMsg{Width: m.width, Height: tabHeight}
		tv, cmd := m.tabsView.Update(tabMsg)
		m.tabsView = tv
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "q":
			if m.state == StateDone {
				return m, tea.Quit
			}
		case "ctrl+l":
			// Toggle log panel focus
			m.logPanel.SetFocused(!m.logPanel.IsFocused())
			return m, nil
		}
		// If log panel is focused, route scroll keys to it
		if m.logPanel.IsFocused() {
			switch msg.String() {
			case "up", "down", "pgup", "pgdown", "home", "end", "j", "k":
				cmd := m.logPanel.Update(msg)
				return m, cmd
			case "esc":
				m.logPanel.SetFocused(false)
				return m, nil
			}
		}
		// In confirming state, route keys to confirm view
		if m.state == StateConfirming {
			cv, cmd := m.confirmView.Update(msg)
			m.confirmView = cv
			return m, cmd
		}
		// Otherwise route to tabs (for tab switching)
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd

	case PlanReadyMsg:
		m.state = StateConfirming
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
			// No validator configured — go straight to confirm
			m.state = StateConfirming
			return m, nil
		}
		m.state = StateValidating
		// Start plan validation in a goroutine
		go m.startPlanValidation()
		return m, nil

	case PlanValidatedMsg:
		if msg.Err != nil {
			// Validation infrastructure error — log and proceed to confirm anyway
			slog.Warn("plan validation error, proceeding to confirm", "err", msg.Err)
			m.state = StateConfirming
			return m, nil
		}
		if msg.Report != nil && msg.Report.Verdict == types.VerdictFail {
			// Plan failed validation
			m.err = fmt.Errorf("plan validation failed: %s", msg.Report.Summary)
			m.state = StateDone
			return m, tea.Quit
		}
		// Validation passed or warned — proceed to confirm
		if msg.Report != nil && msg.Report.Verdict == types.VerdictWarn {
			slog.Warn("plan validation warnings", "summary", msg.Report.Summary)
		}
		m.state = StateConfirming
		return m, nil

	case ConfirmMsg:
		m.approved = msg.Approved
		if !msg.Approved {
			m.state = StateDone
			return m, tea.Quit
		}
		m.state = StateExecuting
		// Start execution — session manager or legacy
		if m.pipeline.SessionManager != nil && m.program != nil {
			go StartExecutionSession(m.program, m.ctx, m.pipeline.SessionManager, m.spec, m.pipeline)
		} else if m.program != nil {
			m.execTabIdx = m.tabsView.AddTab("Worker")
			m.tabsView.active = m.execTabIdx
			go RunExecution(m.program, m.ctx, m.pipeline, m.spec, m.execTabIdx)
		}
		return m, nil

	case StreamChunkMsg:
		// Resolve session-keyed chunks to tab index
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
		// Detect completion: either exec tab finished, or this is the only running harness
		if msg.TabIndex == m.execTabIdx || (m.state == StateExecuting && m.execTabIdx == -1) {
			if msg.Err != nil {
				m.state = StateDone
				m.err = msg.Err
			} else if m.pipeline.ValidateWork != nil && msg.WorkOutput != "" {
				// Start work validation
				go m.startWorkValidation(msg.WorkOutput)
			} else {
				m.state = StateDone
			}
		}
		return m, cmd

	case WorkValidatedMsg:
		if msg.Err != nil {
			slog.Warn("work validation error", "err", msg.Err)
			m.state = StateDone
			return m, nil
		}
		if msg.Report != nil && msg.Report.Verdict == types.VerdictFail {
			m.err = fmt.Errorf("work validation failed: %s", msg.Report.Summary)
		} else if msg.Report != nil && msg.Report.Verdict == types.VerdictWarn {
			slog.Warn("work validation warnings", "summary", msg.Report.Summary)
		}
		m.state = StateDone
		return m, nil

	case LogMsg:
		m.logPanel.Add(msg.Entry)
		return m, nil

	case SessionEventMsg:
		return m.handleSessionEvent(msg.Event)

	case ErrorMsg:
		m.err = msg.Err
		m.state = StateDone
		return m, tea.Quit

	case setProgramMsg:
		m.program = msg.program
		// Now that we have the program, kick off planning
		go m.startPlanning()
		return m, nil

	case spinner.TickMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	// Top area: tabs with streaming content
	topView := m.tabsView.View()

	// Overlay confirm prompt if in confirming state
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
			topView += "\n\n" + errorStyle.Render("✗ Execution failed: "+m.err.Error())
		} else {
			topView += "\n\n" + goalStyle.Render("✓ Execution complete")
		}
		topView += "\n" + statusStyle.Render("Press q to quit")
	}

	// Bottom area: log panel
	logView := m.logPanel.View()

	// Help line
	var helpLine string
	if m.logPanel.IsFocused() {
		helpLine = statusStyle.Render(" ↑/↓/j/k scroll logs • esc unfocus • ctrl+c quit")
	} else {
		helpLine = statusStyle.Render(" tab switch • ctrl+l logs • ctrl+c quit")
	}

	return lipgloss.JoinVertical(lipgloss.Left, topView, logView, helpLine)
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
// Creates tabs for new sessions, updates tab state for existing ones.
func (m Model) handleSessionEvent(evt harness.SessionEvent) (tea.Model, tea.Cmd) {
	tabIdx, exists := m.sessionTabs[evt.SessionID]

	switch evt.State {
	case harness.SessionPending:
		// If tab already exists (pre-created), skip
		if exists {
			return m, nil
		}
		// Create a new tab for this session
		idx := m.tabsView.AddTab(evt.Name)
		m.sessionTabs[evt.SessionID] = idx
		// Auto-focus new tabs
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
		// If this is an execution session (not planner), we're done
		if m.state == StateExecuting && evt.SessionID != "plan" {
			m.state = StateDone
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
		// If this is an execution session, we're done with error
		if m.state == StateExecuting && evt.SessionID != "plan" {
			m.state = StateDone
			m.err = evt.Err
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

	// Mark running
	p.Send(SessionEventMsg{Event: harness.SessionEvent{
		SessionID: "plan",
		Name:      "Planner",
		State:     harness.SessionRunning,
	}})

	spec, err := m.pipeline.Plan(m.ctx, pw)
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
		// No validator configured — skip to confirm
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
