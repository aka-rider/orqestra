package tui

import (
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiEscRe.ReplaceAllString(s, "")
}

// ChatRole identifies who authored a ChatEntry.
type ChatRole string

const (
	ChatRoleUser      ChatRole = "you"
	ChatRoleArchitect ChatRole = "architect"
)

// ChatEntry is one turn in the user-architect conversation during plan review.
type ChatEntry struct {
	Role          ChatRole
	Text          string
	HasPlanChange bool // true if this entry accompanies a plan revision
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

	// Active content mode (mutually exclusive)
	content ContentMode

	// Plan-gate sub-model (valid when content == ContentHumanGate)
	activeChat HumanChatMode

	// User-question sub-model (valid when content == ContentUserQuestion)
	question    userQuestionModel
	hasQuestion bool

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

	// Plan-review conversation
	chatHistory     []ChatEntry
	reviewTokensIn  int64
	reviewTokensOut int64

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
	s.activeChat = nil
	s.chatHistory = nil
	s.reviewTokensIn = 0
	s.reviewTokensOut = 0
	s.active = false
	s.phase = ""
	s.question = userQuestionModel{activeEditor: -1}
	s.hasQuestion = false
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
	case ContentUserQuestion:
		var cmd tea.Cmd
		s.question, cmd = s.question.Update(msg)
		if s.question.Done() {
			answer := s.question.Answer()
			s.timeline.Append(frame.NewAnswer(s.question.QuestionText(), s.question.AnswerSummary(), phaseStyle, dimStyle))
			s.hasQuestion = false
			s.enterStreaming()
			s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
		}
		return s, cmd
	case ContentHumanGate:
		if s.activeChat == nil {
			return s, nil
		}
		// ^E opens the plan in $EDITOR. Intercepted here, above the chat, because
		// editing is a page-level concern (write temp → ExecProcess → read back),
		// not part of the approve/cancel conversation.
		if key.Matches(msg, s.keys.OpenPlanInEditor) && s.hasPlan && s.finalPlan != "" {
			path, err := planTempFile(s.finalPlan)
			if err != nil {
				s.lastErr = err // fail closed: stay in the gate, surface the error, change nothing
				return s, nil
			}
			s.editorFilePath = path
			s.PendingIntent = OpenExternalEditorIntent{FilePath: path}
			return s, nil
		}
		var cmd tea.Cmd
		s.activeChat, cmd = s.activeChat.Update(msg)
		if pending := s.activeChat.Pending(); pending != nil {
			s.activeChat = nil
			s.awaitingPlanDecision = false
			s.seenGateMarkdown = "" // allow next gate to re-trigger
			s.enterStreaming()
			switch p := pending.(type) {
			case *orchestrator.Decision:
				switch p.Type {
				case orchestrator.DecisionApprove:
					s.timeline.Append(frame.NewSteer("approved plan", dimStyle))
					s.PendingIntent = ApprovePlanIntent{}
				case orchestrator.DecisionCancel:
					s.timeline.Append(frame.NewSteer("cancelled", dimStyle))
					s.PendingIntent = CancelPlanIntent{}
				case orchestrator.DecisionComment:
					s.timeline.Append(frame.NewSteer(p.Comment, dimStyle))
					s.PendingIntent = CommentPlanIntent{Comment: p.Comment}
				}
			}
		}
		return s, cmd
	case ContentCompletion:
		return s.handleCompletionKey(msg)
	case ContentStreaming:
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
		var cmd tea.Cmd
		s.chat, cmd = s.chat.Update(msg)
		return s, cmd
	}
	if s.content == ContentUserQuestion && s.hasQuestion {
		var cmd tea.Cmd
		s.question, cmd = s.question.Update(msg)
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
	switch s.content {
	case ContentHumanGate:
		s.awaitingPlanDecision = false
		if s.activeChat != nil {
			s.activeChat, _ = s.activeChat.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if pending := s.activeChat.Pending(); pending != nil {
				switch p := pending.(type) {
				case *orchestrator.Decision:
					switch p.Type {
					case orchestrator.DecisionApprove:
						s.PendingIntent = ApprovePlanIntent{}
					case orchestrator.DecisionCancel:
						s.PendingIntent = CancelPlanIntent{}
					case orchestrator.DecisionComment:
						s.PendingIntent = CommentPlanIntent{Comment: p.Comment}
					}
				}
			}
		} else {
			s.PendingIntent = CancelPlanIntent{}
		}
	case ContentStreaming:
		s.PendingIntent = CancelPipelineIntent{}
	case ContentUserQuestion:
		s.question = s.question.Cancel()
		answer := s.question.Answer()
		s.timeline.Append(frame.NewAnswer(s.question.QuestionText(), s.question.AnswerSummary(), phaseStyle, dimStyle))
		s.hasQuestion = false
		s.enterStreaming()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
	default:
		s.PendingIntent = CancelPipelineIntent{}
	}
	return s
}
