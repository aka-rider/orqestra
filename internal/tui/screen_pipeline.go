package tui

import (
	"os"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
	phase      orchestrator.Phase
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

	// Chat is the bottom steering input (always visible during ContentStreaming).
	chat chat

	// Plan tracking
	finalPlan            string
	hasPlan              bool
	seenGateMarkdown     string
	awaitingPlanDecision bool

	// Agent tracking for status bar
	agents      []AgentRow
	knownAgents map[string]string

	// Completion state
	lastErr          error
	workerValidation string

	// Live stream (written by orchestrator, polled by TUI on tick)
	streamBuf *orchestrator.StreamRing
	cwd       string

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
		keys:        keys,
		configName:  configName,
		knownAgents: make(map[string]string),
		md:          ui.mdDeps(),
		timeline:    NewTimeline(keys, timelineStyles{selectionBg: selectionBg}),
		chat:        newChat(keys),
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
	// Post the opening prompt to the timeline through the same path every later
	// message uses, so the user's first input is visible (bug: first prompt was
	// never shown). Full Chat unification (deleting PromptScreen) follows.
	s.timeline.Append(frame.NewSteer(goal, dimStyle))
	return cmd
}

// Reset clears all pipeline state for a fresh run.
func (s *PipelineScreen) Reset() {
	s.agents = nil
	s.lastErr = nil
	s.finalPlan = ""
	s.hasPlan = false
	s.workerValidation = ""
	s.streamBuf = nil
	s.awaitingPlanDecision = false
	s.seenGateMarkdown = ""
	s.knownAgents = make(map[string]string)
	s.active = false
	s.phase = ""
	s.editConfirm = editConfirmModel{}
	s.animFrame = 0
	s.toolFrameExpanded = false
	s.timeline.Clear()
	s.timeline.styles = timelineStyles{selectionBg: selectionBg}
	s.lastAgentID = ""
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

// closeGate ends a plan gate and resumes the live flow. seenGateMarkdown is
// cleared so the next gate (e.g. a revised plan) re-triggers.
func (s *PipelineScreen) closeGate() {
	s.awaitingPlanDecision = false
	s.seenGateMarkdown = ""
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

// SetToolFrameExpanded sets the tool frame expanded/collapsed state and
// syncs it to the timeline's own expanded field.
func (s *PipelineScreen) SetToolFrameExpanded(expanded bool) {
	s.toolFrameExpanded = expanded
	s.timeline.expanded = expanded
}

// SetStreamBuf sets the shared stream buffer for live output.
func (s *PipelineScreen) SetStreamBuf(buf *orchestrator.StreamRing) {
	s.streamBuf = buf
}

// DrainStreamUpdates consumes currently buffered stream updates without blocking.
// Completed text lines go to the transcript sub-model; tool events and streaming
// deltas go to the streaming console.
func (s *PipelineScreen) DrainStreamUpdates(updates <-chan orchestrator.StreamEntry) {
	if updates == nil {
		return
	}
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return
			}
			switch u.Kind {
			case orchestrator.EntryDelta:
				s.timeline.AppendDelta(u.Text)
				if s.streamBuf != nil {
					s.streamBuf.AppendDelta(u.Text)
				}
			case orchestrator.EntryText:
				var line string
				if s.streamBuf != nil {
					// Prefer the partial accumulated from prior EntryDelta events.
					// EntryText.Text repeats that same content; re-appending doubles the line.
					line = s.streamBuf.CurrentPartial()
					s.streamBuf.FlushPartial()
					if line == "" {
						line = strings.TrimRight(u.Text, "\n\r")
					}
				} else {
					line = strings.TrimRight(u.Text, "\n\r")
				}
				if line != "" {
					s.timeline.ClearLive()
					s.timeline.Append(frame.NewProse(line))
				}
			case orchestrator.EntryToolUse:
				if u.Detail != "" {
					if s.streamBuf != nil {
						s.streamBuf.AppendActivity(u.Tool, u.Detail)
					}
					s.timeline.FlushLive()
					s.timeline.AppendToolPending(stripAnsi(formatActivityLine(u.Tool, u.Detail, s.cwd)))
				}
			case orchestrator.EntryToolResult:
				s.timeline.ResolveLastTool(u.ToolErr)
			}
		default:
			return
		}
	}
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
// ^C-skip paths so both stay in lockstep.
func (s PipelineScreen) resolveQuestion(q userQuestionModel) PipelineScreen {
	s.chat.question = q
	s.timeline.Append(frame.NewAnswer(q.QuestionText(), q.AnswerSummary(), phaseStyle, dimStyle))
	s.PendingIntent = SubmitQuestionAnswerIntent{Answer: q.Answer()}
	s.chat.CloseQuestion()
	s.enterStreaming()
	return s
}
