package tui

import (
	"os"
	"regexp"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiEscRe.ReplaceAllString(s, "")
}

// PipelineScreen manages the pipeline execution view.
type PipelineScreen struct {
	keys       keymap.Bindings
	configName string
	goal       string
	startTime  time.Time
	active     bool

	// Tool frame collapse state — true when the user has toggled expanded mode.
	toolFrameExpanded bool

	// Active content mode: streaming (hosts the chat — plain, a question, or a
	// plan gate), completion, or the edit-confirm dialog.
	content ContentMode

	// Edit-confirmation sub-model (valid when content == ContentEditConfirm)
	editConfirm    editConfirmModel
	editorFilePath string // temp file the external editor edits in place

	// Chat is the bottom input (always visible during ContentStreaming); it
	// hosts a plan-gate revision reply or an open AskUserQuestion.
	chat chat

	// pendingQuestions queues a question that arrived while the chat already
	// had one open (WP17/F3): the pre-fix behavior silently dropped it
	// (onQuestionAsked's `if !s.chat.QuestionOpen()` guard). A live agent's
	// second question is real, truthful state — surfacing it, even delayed
	// until the first resolves, beats losing it outright. FIFO order.
	pendingQuestions []mcp.ToolCall

	// Plan tracking
	finalPlan            string
	hasPlan              bool
	awaitingPlanDecision bool
	// gateID is the GateID of the currently-open gate (set by onGateOpened),
	// carried on every GateDecisionIntent this screen's decisions produce so
	// the pipeline can correlate — and drop stale/mismatched — decisions
	// (WP10/WP4a).
	gateID orchestrator.GateID

	// Agent tracking for status bar. Keyed by agent identity (string(AgentID))
	// so repeated passes of the same agent (e.g. architect across revise
	// rounds) accumulate onto ONE row rather than resetting it (J40).
	agents        []AgentRow
	agentRowIndex map[string]int

	// currentTurn is the active TurnGroup (live ⏺ header + tool rows). The same
	// pointer is set as the Timeline's tail so both views share the state.
	// Nil between turns (after Seal+Promote and before the next agent starts).
	currentTurn *frame.TurnGroup

	// pendingTools tracks unresolved tool entries by local TurnGroup index so
	// resolvePendingTool can update the correct slot.
	pendingTools []pendingTool

	// Completion state
	lastErr          error
	workerValidation string
	// validationVerdict is the worker self-validation verdict as a lowercase
	// string ("pass"/"warn"/"fail", or "" when validation did not run —
	// J33/WP8); rendered PASS/WARN/FAIL/UNKNOWN by viewCompletion.
	validationVerdict string
	conflictFiles     []string // populated when Integrate gives up on a merge conflict

	cwd string

	// Animation frame counter (for spinner)
	animFrame int

	// Alt-screen sub-models
	timeline    Timeline
	lastAgentID string

	// md builds Plan frames (markdownedit) handed to the timeline; the timeline
	// itself is frame-agnostic, so the deps live here with the frame builders.
	md frame.MDDeps

	PendingIntent tea.Msg
}

// NewPipelineScreen creates a new pipeline screen.
func NewPipelineScreen(configName string, ui runeUI, keys keymap.Bindings) PipelineScreen {
	return PipelineScreen{
		keys:          keys,
		configName:    configName,
		agentRowIndex: make(map[string]int),
		md:            ui.mdDeps(),
		timeline:      NewTimeline(keys, timelineStyles{selectionBg: selectionBg}),
		chat:          newChat(keys),
	}
}

// Start prepares the screen for a new pipeline run and returns the timeline
// blink command (must be batched by the caller).
func (s *PipelineScreen) Start(goal string) tea.Cmd {
	s.Reset()
	s.goal = goal
	s.startTime = time.Now()
	if wd, err := os.Getwd(); err == nil { // fire-and-forget: cwd is display-only; missing it just omits file hyperlinks
		s.cwd = wd
	}
	s.active = true
	s.enterStreaming()
	var cmd tea.Cmd
	s.timeline, cmd = s.timeline.Start()
	// TurnGroup owns the ⏺ heartbeat and accumulates prose+tools for this turn.
	tg := frame.NewTurnGroup()
	s.currentTurn = tg
	s.timeline.SetTail(tg)
	s.timeline.Append(frame.NewSteer(goal))
	return cmd
}

// Reset clears all pipeline state for a fresh run.
func (s *PipelineScreen) Reset() {
	s.agents = nil
	s.agentRowIndex = make(map[string]int)
	s.lastErr = nil
	s.finalPlan = ""
	s.hasPlan = false
	s.awaitingPlanDecision = false
	s.gateID = 0
	s.pendingTools = nil
	s.currentTurn = nil
	s.active = false
	s.editConfirm = editConfirmModel{}
	s.animFrame = 0
	s.toolFrameExpanded = false
	s.workerValidation = ""
	s.validationVerdict = ""
	s.conflictFiles = nil
	s.timeline.Clear()
	s.timeline.styles = timelineStyles{selectionBg: selectionBg}
	s.lastAgentID = ""
	s.pendingQuestions = nil
	s.chat.Reset()
	s.chat.Blur()
}

// enterStreaming returns the screen to the live streaming mode with the Chat
// input focused. Every path back to streaming goes through here, so "streaming
// with an unfocused input" — the prompt silently dropping keys — is an
// unrepresentable transition (was bug: the ^E edit-confirm path forgot to focus).
func (s *PipelineScreen) enterStreaming() {
	s.content = ContentStreaming
	s.chat.Focus()
}

// closeGate ends a plan gate and resumes the live flow.
func (s *PipelineScreen) closeGate() {
	s.awaitingPlanDecision = false
	s.enterStreaming()
}

// inputZoneHeight is the number of rows the bottom input zone needs: one line
// normally, grown to fit an open question's options. The layout recalculates
// when this value changes, so question open/close drives the input grow/shrink
// without a separate content mode.
func (s PipelineScreen) inputZoneHeight() int {
	if s.chat.QuestionOpen() {
		return max(constPipelineInputHeight, len(s.chat.question.q.Options)+2)
	}
	return constPipelineInputHeight
}

// SetToolFrameExpanded sets the tool frame expanded/collapsed state on the
// active TurnGroup. No-op when no active turn.
func (s *PipelineScreen) SetToolFrameExpanded(expanded bool) {
	if !s.active || s.currentTurn == nil {
		return
	}
	s.toolFrameExpanded = expanded
	s.currentTurn.SetExpanded(expanded)
}

// pendingTool tracks a tool entry awaiting its result by local TurnGroup index.
type pendingTool struct {
	localIdx int
}

// resolvePendingTool resolves the most recently started tool to ok/err. Results
// arrive newest-first (LIFO), matching how parallel tool calls unwind.
func (s *PipelineScreen) resolvePendingTool(isErr bool) {
	n := len(s.pendingTools)
	if n == 0 || s.currentTurn == nil {
		return
	}
	pt := s.pendingTools[n-1]
	s.pendingTools = s.pendingTools[:n-1]
	status := frame.ToolOK
	if isErr {
		status = frame.ToolErr
	}
	s.currentTurn.ResolveTool(pt.localIdx, status)
}

// reconcilePendingTools marks any still-pending tools Unknown when an agent
// finishes without sending results.
func (s *PipelineScreen) reconcilePendingTools() {
	if s.currentTurn != nil {
		for _, pt := range s.pendingTools {
			s.currentTurn.ResolveTool(pt.localIdx, frame.ToolUnknown)
		}
	}
	s.pendingTools = nil
}

// Update handles key events for the pipeline screen.
func (s PipelineScreen) Update(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch s.content {
	case ContentCompletion:
		return s.handleCompletionKey(msg)
	case ContentStreaming:
		// Streaming hosts the chat, which may be plain, answering a question, or
		// at a plan gate; handleStreamingKey routes to the right one.
		return s.handleStreamingKey(msg)
	case ContentEditConfirm:
		return s.handleEditConfirmKey(msg)
	}
	return s, nil
}

// UpdateSubModel passes non-key messages to focused sub-models (textareas,
// timeline autoscroll and blink ticks).
func (s PipelineScreen) UpdateSubModel(msg tea.Msg) (PipelineScreen, tea.Cmd) {
	switch msg.(type) {
	case timelineAutoscrollMsg, timelineBlinkMsg:
		var cmd tea.Cmd
		s.timeline, cmd = s.timeline.Update(msg)
		return s, cmd
	}
	if s.content == ContentStreaming {
		// The chat routes to the open question (its blink/cursor) or the text input.
		var cmd tea.Cmd
		s.chat, cmd = s.chat.Update(msg)
		return s, cmd
	}
	if s.content == ContentEditConfirm {
		var cmd tea.Cmd
		s.editConfirm, _, cmd = s.editConfirm.Update(msg, s.keys)
		return s, cmd
	}
	return s, nil
}

// HandleCtrlCCancel handles the first Ctrl+C press by emitting the appropriate
// cancel intent based on current content mode.
func (s PipelineScreen) HandleCtrlCCancel() PipelineScreen {
	switch {
	case s.content == ContentStreaming && s.chat.QuestionOpen():
		// A question open → the first ^C skips it.
		s = s.resolveQuestion(s.chat.question.Cancel())
	case s.content == ContentStreaming && s.awaitingPlanDecision:
		// At a plan gate → ^C aborts the plan.
		s.closeGate()
		s.PendingIntent = CancelPlanIntent{}
	default:
		s.PendingIntent = CancelPipelineIntent{}
	}
	return s
}

// resolveQuestion closes the chat's open question, echoes the answer to the
// timeline, and queues the answer for the model. Shared by the Enter and the
// ^C-skip paths so both stay in lockstep. If another question arrived while
// this one was open (WP17/F3 — queued by onQuestionAsked instead of being
// silently dropped), the next one in FIFO order is opened immediately
// afterward so it is never left invisible once the first resolves.
func (s PipelineScreen) resolveQuestion(q userQuestionModel) PipelineScreen {
	s.chat.question = q
	s.timeline.Append(frame.NewAnswer(q.QuestionText(), q.AnswerSummary()))
	s.PendingIntent = SubmitQuestionAnswerIntent{Answer: q.Answer()}
	s.chat.CloseQuestion()
	if len(s.pendingQuestions) > 0 {
		next := s.pendingQuestions[0]
		s.pendingQuestions = s.pendingQuestions[1:]
		s.chat.OpenQuestion(next, s.chat.width)
	}
	s.enterStreaming()
	return s
}
