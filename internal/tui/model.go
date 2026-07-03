package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
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
	ContentStreaming   ContentMode = iota // streaming + the always-present chat (hosts questions and gates)
	ContentCompletion                     // QA report, summary
	ContentEditConfirm                    // Ctrl+E edit confirmation prompt
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

	// Meta is captured at AgentStarted and reused when building the done/failed
	// summary line (EventAgentDone/EventAgentFailed carry no Meta of their own).
	Meta orchestrator.AgentMeta

	// Activities accumulates every tool call observed for this agent identity
	// across all its passes (WP10 Tier-B: replaces the deleted ring buffer's
	// per-agent activity accumulator — the completion screen's file-activity
	// log is now TUI-accumulated event state, not a ring read).
	Activities []toolActivity

	// currentPassStart is the start time of the pass currently in flight
	// (set on AgentStarted, consumed on AgentDone/AgentFailed to accumulate
	// Elapsed). Unexported: internal bookkeeping only.
	currentPassStart time.Time
}

// toolActivity is one tool invocation recorded against an AgentRow — the
// TUI's own minimal replacement for the deleted orchestrator ring buffer's
// per-agent activity type (Tier-B deletion, WP10).
type toolActivity struct {
	Tool   string
	Detail string
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

	// Pipeline observation + control (WP10 — event bus "one pipe out, one
	// pipe in"; replaces the pre-WP10 snapshot-store + gate-control pair).
	events      <-chan orchestrator.RunEvent
	intents     chan<- orchestrator.Intent
	cancelCause context.CancelCauseFunc

	// activeRunID is the RunID of the run this Model is currently willing to
	// accept events from (WP17/F1,A3 — "run identity on the event/intent
	// chain"). Zero means no active run. Set the instant a run starts
	// (startPipeline) and cleared/advanced the instant the user abandons it
	// (ConfirmNewRunIntent/NavigateToPromptIntent, model_intents.go) — BEFORE
	// the replacement run (if any) is even started, so a late event from a
	// just-abandoned run can never be mistaken for the new one's.
	activeRunID orchestrator.RunID

	// intentsDone is closed exactly once the active run is over — either it
	// finished naturally (Update's runEventMsg/EventRunFinished case) or the
	// user abandoned it (ConfirmNewRunIntent/NavigateToPromptIntent) — see
	// closeIntentsDone. sendIntent (model_intents.go) selects on it so a
	// send to m.intents can never block its Cmd goroutine forever once
	// nobody is left to drain that channel (WP17 hardening note). Created
	// fresh per run in startPipeline; nil (never fires in a select) when no
	// run has ever started.
	intentsDone chan struct{}

	// Engine
	engine *orchestrator.Engine

	// keys is the single source of truth for key bindings (validated at startup).
	keys keymap.Bindings

	// Shared rune setup bundle (built once, threaded into prompt/gate sub-models)
	runeUI runeUI

	// Per-screen sub-models
	promptScreen    PromptScreen
	pipelineScreen  PipelineScreen
	runsListScreen  RunsListScreen
	runDetailScreen RunDetailScreen

	// Global UI state
	ctrlCPending  bool
	ctrlCDeadline time.Time
	lastErr       error // navigation-level errors (e.g. loading runs)

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
		state:           StatePrompt,
		keys:            keys,
		promptScreen:    NewPromptScreen(ui, keys),
		pipelineScreen:  NewPipelineScreen(configName, ui, keys),
		engine:          engine,
		runeUI:          ui,
		runsListScreen:  NewRunsListScreen(keys),
		runDetailScreen: NewRunDetailScreen(keys),
		setupScreen:     newSetupModel(keys),
		confirmedSetup:  orchestrator.DefaultPipelineSetup(),
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
			// Show the confirmation dialog instead of an immediate DecisionEdit.
			m.pipelineScreen.editConfirm = newEditConfirm(edited)
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
		// Elapsed-time refresh ONLY (WP10 — stream draining/tick-drain duality
		// is gone; live content now arrives via runEventMsg). Keep the tick loop
		// alive while the pipeline screen is visible OR a run is still active in
		// the background (user navigated to runs list), so elapsed timers stay
		// fresh and returning to the screen shows an up-to-date value.
		if m.state == StatePipeline || m.pipelineScreen.active {
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

	case runEventMsg:
		// WP17/F1,A3: a runEventMsg whose runID no longer matches the
		// model's active run is DROPPED outright — no ApplyEvent, no
		// re-arm. This is what makes a stale event chain (run N's late
		// EventRunFinished arriving after the user cancelled it and started
		// run N+1) die on its very first delivery, instead of painting a
		// false terminal state over the new run or double-arming a second
		// concurrent consumer on the new run's channel.
		if msg.runID != m.activeRunID {
			return m, nil
		}
		// runEventMsg only ever originates from waitForEvent(m.activeRunID,
		// m.events) — by construction m.events is already non-nil whenever
		// this case is reached via the real production Cmd chain
		// (startPipeline sets it before the first waitForEvent call, and
		// every re-arm below reuses the same values) — no nil-guard needed.
		prevInputH := m.pipelineScreen.inputZoneHeight()
		m.pipelineScreen.ApplyEvent(msg.ev, m.width)

		if m.state == StatePipeline {
			if m.pipelineScreen.inputZoneHeight() != prevInputH {
				m.recalculateLayout()
			}
		}
		if _, done := msg.ev.(orchestrator.EventRunFinished); done {
			// Events closes immediately after this event (emitter.go) — stop
			// re-arming rather than issuing one more (harmless but pointless)
			// waitForEvent that would just see the closed channel. Also
			// release any sendIntent Cmd still waiting to send on this run's
			// (now-draining) intents channel (WP17 hardening note).
			m.closeIntentsDone()
			return m, nil
		}
		return m, waitForEvent(msg.runID, m.events)
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
		inputHeight = m.pipelineScreen.inputZoneHeight()
	case StateRunsList, StateRunDetail:
		inputHeight = 0
	}

	if m.pipelineScreen.chat.QuestionOpen() {
		m.pipelineScreen.chat.SetWidth(m.width)
	}

	// Pipeline alt-screen layout: timeline + input + footer (no status bar).
	if m.state == StatePipeline {
		chromeH := inputHeight + constFooterHeight
		timelineH := max(0, m.height-chromeH)

		m.pipelineScreen.chat.SetWidth(m.width)

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
}
