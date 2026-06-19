package tui

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
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
	configName   string
	goal         string
	phase        orchestrator.Phase
	startTime    time.Time
	active       bool
	ctrlCPending bool

	// Active content mode (mutually exclusive)
	content ContentMode

	// Plan-gate sub-model (valid when content == ContentHumanGate)
	activeChat HumanChatMode

	// User-question sub-model (valid when content == ContentUserQuestion)
	question    userQuestionModel
	hasQuestion bool

	// Edit-confirmation sub-model (valid when content == ContentEditConfirm)
	pendingEditContent string
	editConfirmCursor  int
	editConfirmComment textarea.Model
	hasEditComment     bool
	editorRunning      bool

	// Post-message input (always visible during ContentStreaming)
	postInput textarea.Model

	// Plan tracking
	finalPlan            string
	hasPlan              bool
	planFilePath         string
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
	hasValidation    bool
	mergeErrorMsg    string

	// Live stream (written by orchestrator, polled by TUI on tick)
	streamBuf *orchestrator.StreamRing
	cwd       string

	// Live metrics polled from streamBuf on each tick
	liveInput  int64
	liveOutput int64
	liveStart  time.Time

	// Animation frame counter (for spinner)
	animFrame int

	// Alt-screen sub-models
	transcript  Transcript
	streaming   streamingConsole
	lastAgentID string
	bottom      bottomMode // sum type; nil means use streaming console directly

	// Computed layout heights (set by model.recalculateLayout)
	transcriptH int
	streamH     int

	PendingIntent tea.Msg
}

// NewPipelineScreen creates a new pipeline screen.
func NewPipelineScreen(configName string) PipelineScreen {
	ta := textarea.New()
	ta.Placeholder = "post a message to the agent…"
	ta.SetHeight(1)
	ta.CharLimit = 4096
	return PipelineScreen{
		configName:  configName,
		knownAgents: make(map[string]string),
		transcript:  NewTranscript(transcriptStyles{selectionBg: selectionBg, rule: dividerStyle}),
		streaming:   newStreamingConsole(80),
		postInput:   ta,
	}
}

// Start prepares the screen for a new pipeline run and returns the streaming
// console's blink command (must be batched by the caller).
func (s *PipelineScreen) Start(goal string) tea.Cmd {
	s.Reset()
	s.goal = goal
	s.startTime = time.Now()
	if wd, err := os.Getwd(); err == nil { // fire-and-forget: cwd is display-only; missing it just omits file hyperlinks
		s.cwd = wd
	}
	s.content = ContentStreaming
	s.active = true
	s.postInput.Focus()
	var blinkCmd tea.Cmd
	s.streaming, blinkCmd = s.streaming.Start()
	return blinkCmd
}

// Reset clears all pipeline state for a fresh run.
func (s *PipelineScreen) Reset() {
	s.agents = nil
	s.lastErr = nil
	s.finalPlan = ""
	s.hasPlan = false
	s.workerValidation = ""
	s.hasValidation = false
	s.mergeErrorMsg = ""
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
	s.pendingEditContent = ""
	s.editConfirmCursor = 0
	s.hasEditComment = false
	s.editorRunning = false
	s.animFrame = 0
	s.liveInput = 0
	s.liveOutput = 0
	s.liveStart = time.Time{}
	s.transcript = NewTranscript(transcriptStyles{selectionBg: selectionBg, rule: dividerStyle})
	s.streaming = newStreamingConsole(80)
	s.lastAgentID = ""
	s.bottom = nil
	s.postInput.Reset()
	s.postInput.Blur()
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
				s.streaming = s.streaming.AppendDelta(u.Text)
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
					s.transcript.Append(newTextLine(line))
					s.streaming = s.streaming.CompletePartial()
				}
			case orchestrator.EntryToolUse:
				if u.Detail != "" {
					if s.streamBuf != nil {
						s.streamBuf.AppendActivity(u.Tool, u.Detail)
					}
					s.streaming = s.streaming.AddPendingTool(stripAnsi(formatActivityLine(u.Tool, u.Detail, s.cwd)))
				}
			case orchestrator.EntryToolResult:
				s.streaming = s.streaming.ResolveLastTool(u.ToolErr)
			case orchestrator.EntryStats:
				if s.streamBuf != nil {
					s.streamBuf.RecordUsage(u.Stats.Input, u.Stats.Output)
					s.streamBuf.AppendStats(u.Stats.Input, u.Stats.Output)
				}
			}
		default:
			return
		}
	}
}

// SyncLiveMetrics polls live token metrics from the stream buffer.
// Called from Update paths (ticks, obs notifications, resize) — never from View().
func (s *PipelineScreen) SyncLiveMetrics() {
	if s.streamBuf != nil && s.active {
		in, out, start := s.streamBuf.SnapshotUsage()
		s.liveInput = in
		s.liveOutput = out
		s.liveStart = start
	}
}

// ApplySnapshot updates the screen from an ObsStore snapshot, detecting state
// transitions (new agent, agent done/failed, gate open, question, terminal).
func (s *PipelineScreen) ApplySnapshot(snap orchestrator.ObsSnapshot, width int) {
	// Phase (only update when not awaiting plan decision)
	if !s.awaitingPlanDecision {
		s.phase = snap.Phase
	}

	// Agent transitions: new agents or status changes.
	for _, a := range snap.Agents {
		prev, seen := s.knownAgents[a.AgentID]
		curr := a.Status
		if !seen {
			// Emit phase separator rule + flush partial on agent transition.
			if s.transcript.HasContent() {
				// Promote any in-progress partial to transcript before separator.
				if s.streamBuf != nil {
					partial := s.streamBuf.CurrentPartial()
					if partial != "" {
						s.transcript.Append(newTextLine(partial))
					}
					s.streamBuf.FlushPartial()
				}
				ruleLabel := agentDisplayName(string(a.AgentID))
				if a.Meta.ModelDisplay != "" {
					ruleLabel += ": " + a.Meta.ModelDisplay
				} else if a.Meta.ModelRef != "" {
					ruleLabel += ": " + a.Meta.ModelRef
				}
				s.transcript.Append(newRuleLine(ruleLabel))
				s.transcript.Append(newTextLine("")) // margin-bottom-1
			}
			s.streaming = s.streaming.ClearForAgent()
			s.lastAgentID = a.AgentID
			if s.streamBuf != nil {
				s.streamBuf.SetAgent(a.AgentID)
			}
			s.agents = append(s.agents, AgentRow{
				ID:            a.AgentID,
				State:         AgentStateRunning,
				StartedAt:     a.StartTime,
				ModelRef:      a.Meta.ModelRef,
				ModelDisplay:  a.Meta.ModelDisplay,
				Provider:      a.Meta.Provider,
				ContextWindow: a.Meta.ContextWindow,
			})
			s.knownAgents[a.AgentID] = curr
		} else if prev != curr {
			switch curr {
			case "done":
				elapsed := a.EndTime.Sub(a.StartTime)
				for i := range s.agents {
					if s.agents[i].ID == a.AgentID {
						s.agents[i].State = AgentStateDone
						s.agents[i].Elapsed = elapsed
						s.agents[i].InputTokens = a.Input
						s.agents[i].OutputTokens = a.Output
					}
				}
				if a.AgentID == "architect" && len(s.chatHistory) > 0 {
					s.reviewTokensIn += a.Input
					s.reviewTokensOut += a.Output
				}
			case "failed":
				for i := range s.agents {
					if s.agents[i].ID == a.AgentID {
						s.agents[i].State = AgentStateFailed
					}
				}
				if a.Error != "" {
					s.lastErr = errors.New(a.Error)
				}
			}
			s.knownAgents[a.AgentID] = curr
		}
	}

	// Gate: open when markdown changes.
	if snap.HasGate && snap.Gate.FinalPlanMarkdown != s.seenGateMarkdown {
		s.seenGateMarkdown = snap.Gate.FinalPlanMarkdown
		if !s.awaitingPlanDecision {
			if snap.Gate.Position.IsPlanGate() && len(s.chatHistory) > 0 {
				s.chatHistory = append(s.chatHistory, ChatEntry{
					Role: ChatRoleArchitect, Text: "(plan ready for review)",
				})
			}
			s.awaitingPlanDecision = true
			s.content = ContentHumanGate
			s.finalPlan = snap.Gate.FinalPlanMarkdown
			s.hasPlan = snap.Gate.Position.IsPlanGate()
			s.planFilePath = snap.Gate.PlanFilePath
			s.activeChat = newHumanChatMode(snap.Gate, width)
			s.bottom = gateBottom{chat: s.activeChat}
		} else {
			// Plan revised — update without reopening gate.
			s.finalPlan = snap.Gate.FinalPlanMarkdown
		}
	}

	// UserQuestion: show once per question arrival.
	if snap.HasQuestion && !s.hasQuestion {
		s.content = ContentUserQuestion
		s.question = newUserQuestion(snap.UserQuestion, width)
		s.hasQuestion = true
		s.bottom = questionBottom{q: s.question}
	}

	// Terminal: pipeline finished.
	if snap.Terminal.Done && s.active && !s.awaitingPlanDecision {
		s.content = ContentCompletion
		s.active = false
		if snap.Terminal.Err != nil {
			s.lastErr = snap.Terminal.Err
		}
		if snap.Terminal.Result.WorkerValidation != "" {
			s.workerValidation = snap.Terminal.Result.WorkerValidation
			s.hasValidation = true
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
			s.content = ContentStreaming
			s.hasQuestion = false
			s.postInput.Focus()
			s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
		}
		return s, cmd
	case ContentHumanGate:
		if s.activeChat == nil {
			return s, nil
		}
		var cmd tea.Cmd
		s.activeChat, cmd = s.activeChat.Update(msg)
		if pending := s.activeChat.Pending(); pending != nil {
			s.activeChat = nil
			s.content = ContentStreaming
			s.awaitingPlanDecision = false
			s.seenGateMarkdown = "" // allow next gate to re-trigger
			s.postInput.Focus()
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
// transcript autoscroll ticks, streaming console blink ticks).
func (s PipelineScreen) UpdateSubModel(msg tea.Msg) (PipelineScreen, tea.Cmd) {
	switch msg.(type) {
	case transcriptAutoscrollMsg:
		var cmd tea.Cmd
		s.transcript, cmd = s.transcript.Update(msg)
		return s, cmd
	case streamBlinkMsg:
		var cmd tea.Cmd
		s.streaming, cmd = s.streaming.Update(msg)
		return s, cmd
	}
	if s.content == ContentStreaming {
		var cmd tea.Cmd
		s.postInput, cmd = s.postInput.Update(msg)
		return s, cmd
	}
	if s.content == ContentUserQuestion && s.hasQuestion {
		var cmd tea.Cmd
		s.question, cmd = s.question.Update(msg)
		return s, cmd
	}
	if s.content == ContentEditConfirm && s.hasEditComment {
		var cmd tea.Cmd
		s.editConfirmComment, cmd = s.editConfirmComment.Update(msg)
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
		s.content = ContentStreaming
		s.hasQuestion = false
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
	default:
		s.PendingIntent = CancelPipelineIntent{}
	}
	return s
}

