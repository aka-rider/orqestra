package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// State represents the current phase of the TUI.
// State represents the current phase of the TUI.
type State int

const (
	StateIdle    State = iota // Waiting for agent launch
	StateRunning              // Agent(s) running in tabs
	StateDone                 // All agents finished
)

// PipelinePhase tracks which agent is currently active in the pipeline.
type PipelinePhase int

const (
	PhaseIntake PipelinePhase = iota
	PhasePlanner
	PhaseValidator
	PhaseWorker
	PhaseDone
)

// Model is the main Bubble Tea model.
type Model struct {
	state State

	tabsView tabsView
	logPanel logPanel
	showLogs bool

	pipeline PipelineFuncs
	program  *tea.Program
	ctx      context.Context
	cancel   context.CancelFunc

	width  int
	height int
	err    error

	intakeTabIdx int
	gotSize      bool // true once first WindowSizeMsg has been processed
	gotProgram   bool // true once setProgramMsg has been received
	launched     bool // true once the agent goroutine has been started

	phase     PipelinePhase
	artifacts map[string][]byte // keyed by role name
}

// NewModel creates the main TUI model.
func NewModel(pipeline PipelineFuncs) Model {
	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		state:        StateIdle,
		tabsView:     newTabsView(),
		logPanel:     newLogPanel(),
		showLogs:     true,
		pipeline:     pipeline,
		ctx:          ctx,
		cancel:       cancel,
		intakeTabIdx: -1,
	}
}

func (m Model) Init() tea.Cmd {
	return spinner.New().Tick
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.gotSize = true

		logHeight := 0
		if m.showLogs {
			logHeight = 8
		}
		tabHeight := m.height - logHeight

		if m.showLogs {
			m.logPanel = m.logPanel.SetWidth(m.width)
			m.logPanel = m.logPanel.SetHeight(logHeight)
		}

		tabMsg := tea.WindowSizeMsg{Width: m.width, Height: tabHeight}
		tv, cmd := m.tabsView.Update(tabMsg)
		m.tabsView = tv

		// If we have both size and program but haven't launched yet, do it now.
		if m2, launchCmd := tryLaunch(m); launchCmd != nil {
			return m2, tea.Batch(cmd, launchCmd)
		}
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)

	case ToggleLogsMsg:
		m.showLogs = !m.showLogs
		if m.width > 0 {
			return m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
		return m, nil

	case attachPTYMsg:
		if tv := m.tabsView.TermTab(msg.tabIndex); tv != nil {
			tv.AttachPTY(msg.pty)
			// Immediately resize the PTY to match the terminal emulator dimensions.
			// The tab may have been created before WindowSizeMsg arrived, or the PTY
			// was started with default dimensions that don't match the TUI layout.
			if tv.width > 0 && tv.height > 1 {
				vtHeight := tv.height - 1
				if vtHeight < 1 {
					vtHeight = 1
				}
				_ = msg.pty.Resize(uint(tv.width), uint(vtHeight))
			}
		}
		return m, nil

	case PTYOutputMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd

	case PTYDoneMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd

	case AttentionMsg:
		m.tabsView.SetAttention(msg.TabIndex)
		return m, nil

	case IntakeCompleteMsg:
		if msg.Err != nil {
			m.logPanel = m.logPanel.Add(LogEntry{
				Time:    time.Now(),
				Level:   "ERROR",
				Message: "agent failed",
				Attrs:   map[string]string{"err": msg.Err.Error()},
			})
			m.err = msg.Err
			m.state = StateDone
			return m, nil
		}
		// Legacy single-agent path: agent exited cleanly.
		m.state = StateDone
		return m, nil

	case AgentCompleteMsg:
		if msg.Err != nil {
			m.logPanel = m.logPanel.Add(LogEntry{
				Time:    time.Now(),
				Level:   "ERROR",
				Message: fmt.Sprintf("agent %s failed", msg.Role),
				Attrs:   map[string]string{"err": msg.Err.Error()},
			})
			m.err = msg.Err
			m.state = StateDone
			return m, nil
		}
		if m.artifacts == nil {
			m.artifacts = make(map[string][]byte)
		}
		m.artifacts[msg.Role] = msg.Artifact
		return m.advancePipeline()

	case ErrorMsg:
		m.logPanel = m.logPanel.Add(LogEntry{
			Time:    time.Now(),
			Level:   "ERROR",
			Message: "error",
			Attrs:   map[string]string{"err": msg.Err.Error()},
		})
		m.err = msg.Err
		m.state = StateDone
		return m, nil

	case LogMsg:
		m.logPanel = m.logPanel.Add(msg.Entry)
		return m, nil

	case SandboxStateMsg:
		m.logPanel = m.logPanel.Add(LogEntry{
			Time:    time.Now(),
			Level:   "INFO",
			Message: fmt.Sprintf("sandbox %s: %s", truncID(msg.SandboxID), msg.State),
		})
		return m, nil

	case setProgramMsg:
		m.program = msg.program
		m.gotProgram = true
		// If we already have window dimensions, launch now.
		if m2, cmd := tryLaunch(m); cmd != nil {
			return m2, cmd
		}
		return m, nil

	case PulseTickMsg:
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd

	case spinner.TickMsg:
		tv, tvCmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, tvCmd
	}

	return m, nil
}

// handleKey routes keystrokes.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global: ctrl+c always quits
	if key == "ctrl+c" {
		m.cancel()
		return m, tea.Quit
	}

	// Toggle logs
	if key == "ctrl+l" {
		m.showLogs = !m.showLogs
		if m.width > 0 {
			return m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
		return m, nil
	}

	// Alt+N for tab switching
	switch key {
	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd
	case "alt+!":
		if idx := m.tabsView.FirstAttentionTab(); idx >= 0 {
			m.tabsView.active = idx
			m.tabsView.ClearAttention(idx)
		}
		return m, nil
	}

	// In StateDone with error, any key quits.
	if m.state == StateDone && m.err != nil {
		return m, tea.Quit
	}

	// Forward all other keys to the active term tab (user is typing in Claude Code).
	if m.state == StateRunning && len(m.tabsView.termTabs) > 0 {
		tv, cmd := m.tabsView.Update(msg)
		m.tabsView = tv
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	var topView string

	if len(m.tabsView.tabNames) == 0 && m.state == StateIdle {
		topView = titleStyle.Render("orqestra") + "\n\n" +
			statusStyle.Render("Starting agent...")
	} else if m.state == StateRunning && len(m.tabsView.termTabs) > 0 && m.tabsView.termTabs[m.tabsView.active].ptySession == nil {
		topView = m.tabsView.View() + "\n\n" +
			statusStyle.Render("⟳ Provisioning sandbox... (ctrl+c to quit)")
	} else {
		topView = m.tabsView.View()
	}

	if m.state == StateDone {
		if m.err != nil {
			topView += "\n\n" + errorStyle.Render("✗ Error: "+m.err.Error()) + "\n" + dimStyle.Render("  press any key to exit")
		} else {
			topView += "\n\n" + goalStyle.Render("✓ Agent session complete")
		}
	}

	var logView string
	if m.showLogs {
		logView = m.logPanel.View()
	}

	var parts []string
	parts = append(parts, topView)
	if logView != "" {
		parts = append(parts, logView)
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// tryLaunch starts the interactive agent if both WindowSizeMsg and setProgramMsg
// have been received. Returns the updated model and a tea.Cmd if launch happened.
func tryLaunch(m Model) (Model, tea.Cmd) {
	if m.launched || !m.gotSize || !m.gotProgram {
		return m, nil
	}
	if m.pipeline.LaunchAgent == nil && m.pipeline.LaunchInteractive == nil {
		return m, nil
	}

	m.launched = true
	m.state = StateRunning

	// Pipeline mode: start with intake agent.
	if m.pipeline.LaunchAgent != nil {
		m.phase = PhaseIntake
		tabIdx := m.tabsView.AddTermTab("Intake")
		m.tabsView.active = tabIdx
		m.tabsView.focused = true
		m.tabsView.termTabs[tabIdx].focused = true
		m.intakeTabIdx = tabIdx
		go m.startAgent("intake", tabIdx, nil)
		return m, pulseTickCmd()
	}

	// Legacy mode: single interactive agent.
	m.intakeTabIdx = m.tabsView.AddTermTab("Claude")
	m.tabsView.active = m.intakeTabIdx
	m.tabsView.focused = true
	m.tabsView.termTabs[m.intakeTabIdx].focused = true
	go m.startInteractiveAgent("")
	return m, pulseTickCmd()
}

// startAgent launches a pipeline agent in its own tab.
// Safe to call from a goroutine — only reads stable fields and uses p.Send.
func (m Model) startAgent(role string, tabIdx int, inputFiles map[string][]byte) {
	p := m.program

	ptyWriter, waitFn, err := m.pipeline.LaunchAgent(m.ctx, role, inputFiles, p.Send, tabIdx)
	if err != nil {
		p.Send(AgentCompleteMsg{Role: role, TabIndex: tabIdx, Err: err})
		return
	}

	p.Send(attachPTYMsg{tabIndex: tabIdx, pty: ptyWriter})

	artifact, err := waitFn(m.ctx)
	p.Send(AgentCompleteMsg{Role: role, TabIndex: tabIdx, Artifact: artifact, Err: err})
}

// startInteractiveAgent launches Claude Code in interactive mode inside a
// sandbox term tab. Legacy single-agent path.
func (m Model) startInteractiveAgent(prompt string) {
	p := m.program
	tabIdx := m.intakeTabIdx

	ptyWriter, waitFn, err := m.pipeline.LaunchInteractive(m.ctx, prompt, p.Send, tabIdx)
	if err != nil {
		p.Send(IntakeCompleteMsg{Err: err})
		return
	}

	p.Send(attachPTYMsg{tabIndex: tabIdx, pty: ptyWriter})

	artifact, err := waitFn(m.ctx)
	if err != nil {
		p.Send(IntakeCompleteMsg{Err: err})
		return
	}
	p.Send(IntakeCompleteMsg{Artifact: artifact})
}

// advancePipeline transitions to the next agent phase after the current one completes.
func (m Model) advancePipeline() (Model, tea.Cmd) {
	switch m.phase {
	case PhaseIntake:
		m.phase = PhasePlanner
		tabIdx := m.tabsView.AddTermTab("Planner")
		m.tabsView.active = tabIdx
		m.tabsView.focused = true
		m.tabsView.termTabs[tabIdx].focused = true
		inputs := map[string][]byte{"01.intake.json": m.artifacts["intake"]}
		go m.startAgent("planner", tabIdx, inputs)
		return m, nil

	case PhasePlanner:
		m.phase = PhaseValidator
		tabIdx := m.tabsView.AddTermTab("Validator")
		m.tabsView.active = tabIdx
		m.tabsView.focused = true
		m.tabsView.termTabs[tabIdx].focused = true
		inputs := map[string][]byte{
			"01.intake.json": m.artifacts["intake"],
			"02.plan.json":   m.artifacts["planner"],
		}
		go m.startAgent("plan-validator", tabIdx, inputs)
		return m, nil

	case PhaseValidator:
		// Check if validator approved the plan.
		if !validatorApproved(m.artifacts["plan-validator"]) {
			m.err = fmt.Errorf("plan rejected by validator")
			m.state = StateDone
			return m, nil
		}
		m.phase = PhaseWorker
		tabIdx := m.tabsView.AddTermTab("Worker")
		m.tabsView.active = tabIdx
		m.tabsView.focused = true
		m.tabsView.termTabs[tabIdx].focused = true
		inputs := map[string][]byte{"02.plan.json": m.artifacts["planner"]}
		go m.startAgent("worker", tabIdx, inputs)
		return m, nil

	case PhaseWorker:
		m.phase = PhaseDone
		m.state = StateDone
		return m, nil
	}
	return m, nil
}

// validatorApproved checks if the validator artifact indicates approval.
func validatorApproved(artifact []byte) bool {
	if artifact == nil {
		return true // no artifact = auto-approve (interactive validator)
	}
	var v struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(artifact, &v); err != nil {
		return true // unparseable = assume approved (user handled interactively)
	}
	return v.Verdict == "approved" || v.Verdict == ""
}

// truncID safely truncates an ID for display.
func truncID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
