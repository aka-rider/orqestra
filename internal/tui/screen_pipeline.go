package tui

import (
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// PipelineScreen manages the pipeline execution view with all content modes.
type PipelineScreen struct {
	content   ContentMode
	phase     orchestrator.Phase
	goal      string
	startTime time.Time

	// Sidebar state
	agents []AgentRow

	// Content state
	finalPlan        string
	hasPlan          bool
	workerValidation string
	hasValidation    bool
	lastErr          error

	// Streaming output — shared buffer polled on tick
	streamBuf *orchestrator.StreamRing
	history   *orchestrator.StreamHistoryStore

	// Frame list — persistent store for scrollable frame rendering
	frameList         *FrameList
	planFrameIdx      int // index of PlanFrame in frameList; set on EventGateRequest
	planDiffLineOffset int // line offset of the diff separator within PlanFrame

	// Plan review state
	planComment    textarea.Model
	hasPlanComment bool
	editorRunning  bool

	// Edit confirmation state
	pendingEditContent string // holds edited plan until confirmed
	editConfirmCursor  int    // 0 = Yes, 1 = No
	editConfirmComment textarea.Model
	hasEditComment     bool // true when Tab has opened the comment textarea

	// Conversation state during plan review
	chatHistory     []ChatEntry
	planDiff        string // unified diff from git micro-repo
	reviewTokensIn  int64
	reviewTokensOut int64

	// Plan history (Ctrl+Y viewer) — set from EventGateRequest.
	planHistoryDir     string
	planHistoryHeadSHA string

	// Agent history navigation
	focusedAgent int

	// Run directory and plan file
	runDir       string
	planFilePath string
	cwd          string

	awaitingPlanDecision bool

	// User question state (MCP AskUserQuestion bridge)
	question    userQuestionModel
	hasQuestion bool

	// Merge conflict state
	mergeConflict orchestrator.MergeConflictInfo

	// Merge error state (merge failed, branch preserved)
	mergeErrorMsg    string
	mergeErrorBranch string
	mergeErrorPath   string

	// UI state
	configName     string
	showDashboard  bool
	showHelp       bool
	ctrlCPending   bool // set by parent model when Ctrl+C time gate is active
	active bool // true while pipeline is running

	// Animation state
	animFrame int // incremented by animTickMsg for shimmer/pulse effects

	// Live streaming metrics (polled from StreamRing on tick)
	liveInput  int64
	liveOutput int64
	liveStart  time.Time

	// Viewports
	contentVP viewport.Model
	dashboard DashboardModel
	bounds    layoutBounds

	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewPipelineScreen creates a new pipeline screen.
func NewPipelineScreen(configName string) PipelineScreen {
	cvp := viewport.New()
	cvp.MouseWheelEnabled = true

	return PipelineScreen{
		configName: configName,
		contentVP:  cvp,
		dashboard:  NewDashboardModel(),
		frameList:  NewFrameList(80),
	}
}

// Start prepares the screen for a new pipeline run.
func (s *PipelineScreen) Start(goal string) {
	s.Reset()
	s.goal = goal
	s.startTime = time.Now()
	if wd, err := os.Getwd(); err == nil {
		s.cwd = wd
	}
	s.content = ContentStreaming
	s.active = true
}

// Reset clears all pipeline state for a fresh run.
func (s *PipelineScreen) Reset() {
	s.agents = nil
	s.lastErr = nil
	s.finalPlan = ""
	s.hasPlan = false
	s.workerValidation = ""
	s.hasValidation = false
	s.streamBuf = nil
	s.history = nil
	s.frameList = NewFrameList(80)
	s.planFrameIdx = 0
	s.planDiffLineOffset = 0
	s.hasPlanComment = false
	s.editorRunning = false
	s.awaitingPlanDecision = false
	s.chatHistory = nil
	s.planDiff = ""
	s.reviewTokensIn = 0
	s.reviewTokensOut = 0
	s.focusedAgent = 0
	s.runDir = ""
	s.planFilePath = ""
	s.showDashboard = false
	s.showHelp = false
	s.active = false
	s.phase = ""
	s.question = userQuestionModel{activeEditor: -1}
	s.hasQuestion = false
	s.mergeConflict = orchestrator.MergeConflictInfo{}
	s.pendingEditContent = ""
	s.editConfirmCursor = 0
	s.hasEditComment = false
	s.animFrame = 0
	s.liveInput = 0
	s.liveOutput = 0
	s.liveStart = time.Time{}
	s.contentVP.SetContent("")
	s.contentVP.GotoTop()
	s.dashboard = NewDashboardModel()
}

// SetStreamBuf sets the shared stream buffer for live output.
func (s *PipelineScreen) SetStreamBuf(buf *orchestrator.StreamRing) {
	s.streamBuf = buf
}

// SetHistoryStore sets the historical stream store for per-agent read-only views.
func (s *PipelineScreen) SetHistoryStore(store *orchestrator.StreamHistoryStore) {
	s.history = store
}

// DrainStreamUpdates consumes currently buffered stream updates without blocking.
func (s *PipelineScreen) DrainStreamUpdates(updates <-chan orchestrator.StreamEntry) {
	if updates == nil || s.streamBuf == nil {
		return
	}
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return
			}
			switch u.Kind {
			case orchestrator.EntryText:
				s.streamBuf.AppendText(u.Text)
				s.frameList.UpdateActive(func(f *Frame) {
					if u.Text != "" {
						f.AppendText(u.Text)
					}
				})
			case orchestrator.EntryToolUse:
				s.streamBuf.AppendActivity(u.Tool, u.Detail)
				s.frameList.UpdateActive(func(f *Frame) {
					f.AppendTool(ToolBlock{
						Icon:   IconForAction(u.Tool),
						Name:   u.Tool,
						Detail: u.Detail,
					})
				})
			case orchestrator.EntryStats:
				s.streamBuf.RecordUsage(u.Stats.Input, u.Stats.Output)
				s.streamBuf.AppendStats(u.Stats.Input, u.Stats.Output)
			}
		default:
			return
		}
	}
}

// SyncViewports updates pipeline viewport content from current screen state.
func (s *PipelineScreen) SyncViewports() {
	w := s.effectiveWidth()

	if s.showDashboard {
		// Feed live agent data to dashboard
		s.dashboard.SetAgents(s.agents)
	} else {
		// When using frame list rendering, skip SetContent if nothing changed.
		// FrameList.Render() returns cached content when not dirty.
		frameListMode := (s.content == ContentStreaming || s.content == ContentPlanReview ||
			s.content == ContentCompletion || s.content == ContentAgentHistory) &&
			s.frameList.FrameCount() > 0

		var contentView string
		if s.showHelp {
			contentView = s.viewHelp()
		} else if frameListMode && !s.frameList.IsDirty() {
			// Frame list unchanged — skip SetContent to preserve scroll
			goto pollMetrics
		} else {
			contentView = s.viewContent(w)
		}
		atBottom := s.contentVP.AtBottom()
		s.contentVP.SetContent(contentView)
		if atBottom && (s.content == ContentStreaming || s.content == ContentPlanReview || s.content == ContentCompletion) {
			s.contentVP.GotoBottom()
		}
	}

pollMetrics:
	// Poll live metrics from stream ring
	if s.streamBuf != nil && s.active {
		in, out, start := s.streamBuf.SnapshotUsage()
		s.liveInput = in
		s.liveOutput = out
		s.liveStart = start
	}
}

func (s PipelineScreen) effectiveWidth() int {
	if s.contentVP.Width() >= minWidth {
		return s.contentVP.Width()
	}
	return minWidth
}

// RecalculateLayout updates viewport dimensions from parent-computed values.
func (s *PipelineScreen) RecalculateLayout(width, contentHeight int) {
	s.contentVP.SetWidth(width)
	s.contentVP.SetHeight(contentHeight)
	s.dashboard.SetSize(width, contentHeight)
	s.frameList.SetWidth(width)
}

// ApplyEvent updates the screen based on a single orchestrator event.
func (s *PipelineScreen) ApplyEvent(event orchestrator.Event, width int) {
	slog.Debug("tui event", "type", event.Type, "phase", event.Phase, "agentID", event.AgentID)

	switch event.Type {
	case orchestrator.EventPhaseChange:
		if !s.awaitingPlanDecision {
			s.phase = event.Phase
		}

	case orchestrator.EventAgentStarted:
		if s.streamBuf != nil {
			s.streamBuf.SetAgent(event.AgentID)
		}
		s.agents = append(s.agents, AgentRow{
			ID:            event.AgentID,
			State:         AgentStateRunning,
			StartedAt:     time.Now(),
			ModelRef:      event.Meta.ModelRef,
			ModelDisplay:  event.Meta.ModelDisplay,
			Provider:      event.Meta.Provider,
			ContextWindow: event.Meta.ContextWindow,
		})
		s.frameList.AppendFrame(Frame{
			Kind:       AgentFrame,
			State:      FrameInProgress,
			AgentID:    event.AgentID,
			AgentModel: event.Meta.ModelDisplay,
			StartedAt:  time.Now(),
		})

	case orchestrator.EventAgentDone:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = AgentStateDone
				s.agents[i].Elapsed = time.Since(s.agents[i].StartedAt)
				s.agents[i].InputTokens = event.InputTokens
				s.agents[i].OutputTokens = event.OutputTokens
			}
		}
		// Accumulate review tokens when in conversation mode
		if event.AgentID == "architect" && len(s.chatHistory) > 0 {
			s.reviewTokensIn += event.InputTokens
			s.reviewTokensOut += event.OutputTokens
		}
		var agentElapsed time.Duration
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				agentElapsed = s.agents[i].Elapsed
				break
			}
		}
		s.frameList.FinishActive(agentElapsed, event.InputTokens, event.OutputTokens)

	case orchestrator.EventAgentFailed:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = AgentStateFailed
			}
		}
		s.lastErr = event.Err
		s.frameList.FinishActive(0, 0, 0)

	case orchestrator.EventAgentCancelled:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = AgentStateCancelled
			}
		}

	case orchestrator.EventGateRequest:
		switch event.Gate.Type {
		case orchestrator.GatePlanApproval:
			s.planDiff = event.Gate.PlanDiff
			s.planHistoryDir = event.Gate.PlanHistoryDir
			s.planHistoryHeadSHA = event.Gate.PlanHistoryHeadSHA
			if len(s.chatHistory) > 0 && s.planDiff != "" {
				s.chatHistory = append(s.chatHistory, ChatEntry{
					Role: ChatRoleArchitect, Text: "(plan revised — see diff with [^D])", HasPlanChange: true,
				})
			}
			s.awaitingPlanDecision = true
			s.content = ContentPlanReview
			s.finalPlan = event.Gate.FinalPlanMarkdown
			s.hasPlan = true
			s.planFilePath = event.Gate.PlanFilePath
			contentWidth := max(1, width)
			s.planComment = textarea.New()
			s.planComment.Placeholder = "Ask a question or request changes..."
			s.planComment.SetWidth(max(1, contentWidth-4))
			s.planComment.SetHeight(2)
			s.planComment.CharLimit = 1024
			s.planComment.Focus()
			s.hasPlanComment = true
			// Build plan text with optional inline diff
			planText := event.Gate.FinalPlanMarkdown
			if event.Gate.PlanDiff != "" {
				s.planDiffLineOffset = strings.Count(event.Gate.FinalPlanMarkdown, "\n") + 2
				planText += "\n── plan diff ──\n" + stripAnsi(event.Gate.PlanDiff)
			}
			s.frameList.AppendFrame(Frame{
				Kind:  PlanFrame,
				State: FrameInProgress,
				Parts: []ContentPart{{IsText: true, Text: planText}},
			})
			s.planFrameIdx = s.frameList.FrameCount() - 1
		}

	case orchestrator.EventPlanReady:
		s.finalPlan = event.FinalPlan
		s.hasPlan = true

	case orchestrator.EventRunDirReady:
		s.runDir = event.RunDir

	case orchestrator.EventChatResponse:
		s.chatHistory = append(s.chatHistory, ChatEntry{Role: ChatRoleArchitect, Text: event.ChatText})
		s.content = ContentPlanReview
		s.awaitingPlanDecision = true
		contentWidth := max(1, width)
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(max(1, contentWidth-4))
		s.planComment.SetHeight(2)
		s.planComment.CharLimit = 1024
		s.planComment.Focus()
		// Update the PlanFrame with the new chat response
		s.frameList.UpdateActive(func(f *Frame) {
			f.AppendText("Architect: " + event.ChatText)
		})
		s.hasPlanComment = true

	case orchestrator.EventError:
		s.lastErr = event.Err

	case orchestrator.EventUserQuestion:
		s.content = ContentUserQuestion
		s.question = newUserQuestion(event.UserQuestion, width)
		s.hasQuestion = true
		s.contentVP.GotoTop()

	case orchestrator.EventMergeConflict:
		s.mergeConflict = event.MergeConflict
		s.content = ContentMergeConflict
		s.contentVP.GotoTop()

	case orchestrator.EventMergeError:
		s.mergeErrorMsg = event.MergeError
		s.mergeErrorBranch = event.MergeBranch
		s.mergeErrorPath = event.MergeWorktreePath

	case orchestrator.EventComplete:
		s.content = ContentCompletion
		s.active = false
		if event.WorkerValidation != "" {
			s.workerValidation = event.WorkerValidation
			s.hasValidation = true
		}
		// Append completion frame with summary
		s.frameList.AppendFrame(Frame{
			Kind:    CompletionFrame,
			State:   FrameFinished,
			Elapsed: time.Since(s.startTime),
			Parts:   []ContentPart{{IsText: true, Text: s.buildCompletionSummary()}},
		})
	}
}

// Update handles key events for the pipeline screen.
func (s PipelineScreen) Update(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// Global pipeline keys first
	switch msg.String() {
	case "ctrl+h":
		s.showHelp = !s.showHelp
		s.SyncViewports()
		return s, nil
	case "ctrl+d":
		if s.content != ContentPlanReview && s.content != ContentUserQuestion {
			s.showDashboard = !s.showDashboard
			s.SyncViewports()
			return s, nil
		}
	}

	// PgUp / PgDown — delegate to active viewport
	switch msg.Code {
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		if s.showDashboard {
			s.dashboard, cmd = s.dashboard.Update(msg)
		} else {
			s.frameList.ClearFocus()
			s.contentVP, cmd = s.contentVP.Update(msg)
		}
		return s, cmd
	}

	// If help is showing, any other key dismisses it
	if s.showHelp {
		s.showHelp = false
		s.SyncViewports()
		return s, nil
	}

	// If dashboard is showing, route all keys to the DashboardModel FSM
	if s.showDashboard {
		var cmd tea.Cmd
		s.dashboard, cmd = s.dashboard.Update(msg)
		// Check for close intent
		if s.dashboard.PendingIntent != nil {
			if _, ok := s.dashboard.PendingIntent.(CloseDashboardIntent); ok {
				s.showDashboard = false
				s.dashboard.PendingIntent = nil
				s.SyncViewports()
			}
		}
		return s, cmd
	}

	// Alt+1-9 for agent navigation — scroll to the agent's frame
	altKeys := map[string]int{
		"alt+1": 1, "alt+2": 2, "alt+3": 3, "alt+4": 4, "alt+5": 5,
		"alt+6": 6, "alt+7": 7, "alt+8": 8, "alt+9": 9,
	}
	if idx, ok := altKeys[msg.String()]; ok && s.content != ContentPlanReview && s.content != ContentUserQuestion {
		if idx <= len(s.agents) {
			s.focusedAgent = idx
			// Scroll to the frame in the frame list (frame indices are 0-based, idx is 1-based)
			if s.frameList.FrameCount() > 0 {
				s.content = ContentStreaming
				s.SyncViewports() // ensure content is set before scroll
				topLine := s.frameList.FrameTopLine(idx - 1)
				s.contentVP.SetYOffset(topLine)
			} else {
				s.content = ContentAgentHistory
				s.contentVP.GotoTop()
				s.SyncViewports()
			}
			return s, nil
		}
	}

	switch s.content {
	case ContentUserQuestion:
		var cmd tea.Cmd
		s.question, cmd = s.question.Update(msg)
		// Refresh the cached contentVP after every keystroke so the textarea's
		// new value is visible immediately, rather than at the next tick.
		s.SyncViewports()
		if s.question.Done() {
			answer := s.question.Answer()
			s.content = ContentStreaming
			s.hasQuestion = false
			s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
			s.SyncViewports()
		}
		return s, cmd
	case ContentPlanReview:
		return s.handlePlanReviewKey(msg)
	case ContentAgentHistory:
		return s.handleAgentHistoryKey(msg)
	case ContentCompletion:
		return s.handleCompletionKey(msg)
	case ContentStreaming:
		return s.handleStreamingKey(msg)
	case ContentMergeConflict:
		return s.handleMergeConflictKey(msg)
	case ContentEditConfirm:
		return s.handleEditConfirmKey(msg)
	}
	return s, nil
}

// HandleMouse routes mouse events to the appropriate viewport.
func (s PipelineScreen) HandleMouse(msg tea.MouseMsg) (PipelineScreen, tea.Cmd) {
	mouse := msg.Mouse()
	p := image.Pt(mouse.X, mouse.Y)

	if p.In(s.bounds.textarea) {
		return s, nil
	}

	var cmd tea.Cmd
	if s.showDashboard {
		s.dashboard, cmd = s.dashboard.Update(msg)
	} else if p.In(s.bounds.content) {
		s.contentVP, cmd = s.contentVP.Update(msg)
	}
	return s, cmd
}

// UpdateSubModel passes non-key messages to focused sub-models (textareas).
func (s PipelineScreen) UpdateSubModel(msg tea.Msg) (PipelineScreen, tea.Cmd) {
	if s.content == ContentUserQuestion && s.hasQuestion {
		var cmd tea.Cmd
		s.question, cmd = s.question.Update(msg)
		// Keep the cached contentVP in sync on blink/timer-driven messages
		// too. Completion handling is owned by the key path (Model.handleKey
		// drives Update; this path is fed by Model.Update tail and does not
		// drain PendingIntent), so do NOT transition state here.
		s.SyncViewports()
		return s, cmd
	}
	if s.content == ContentEditConfirm && s.hasEditComment {
		var cmd tea.Cmd
		s.editConfirmComment, cmd = s.editConfirmComment.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s PipelineScreen) handlePlanReviewKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// When the comment textarea is focused, forward all non-modifier keys to it.
	// Only Ctrl+* combos, Esc, and Enter pass through as actions.
	if s.hasPlanComment {
		switch msg.Code {
		case tea.KeyEscape:
			s.planComment.Blur()
			s.hasPlanComment = false
			s.SyncViewports()
			return s, nil
		case tea.KeyEnter:
			if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
				s.planComment.InsertString("\n")
				return s, nil
			}
			comment := strings.TrimSpace(s.planComment.Value())
			if comment != "" {
				s.chatHistory = append(s.chatHistory, ChatEntry{Role: ChatRoleUser, Text: comment})
				s.planComment.Reset()
				s.hasPlanComment = false
				s.awaitingPlanDecision = false
				s.content = ContentStreaming
				// Append user comment to PlanFrame before it gets re-activated
				s.frameList.UpdateActive(func(f *Frame) {
					f.AppendText("You: " + comment)
				})
				s.SyncViewports()
				s.PendingIntent = CommentPlanIntent{Comment: comment}
				return s, nil
			}
			return s, nil
		}
		// Ctrl/Alt combos fall through to action handlers below
		if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) && !msg.Mod.Contains(tea.ModMeta) {
			var cmd tea.Cmd
			s.planComment, cmd = s.planComment.Update(msg)
			return s, cmd
		}
	}

	switch msg.String() {
	case "ctrl+e", "ctrl+shift+e":
		if s.planFilePath != "" {
			s.editorRunning = true
			s.PendingIntent = OpenExternalEditorIntent{FilePath: s.planFilePath}
			return s, nil
		}
		return s, nil
	case "ctrl+a":
		s.awaitingPlanDecision = false
		s.hasPlanComment = false
		s.content = ContentStreaming
		s.frameList.FinishActive(0, 0, 0) // seal the PlanFrame
		s.SyncViewports()
		s.PendingIntent = ApprovePlanIntent{}
		return s, nil
	case "ctrl+d":
		if s.planDiff != "" {
			s.contentVP.SetYOffset(s.frameList.FrameTopLine(s.planFrameIdx) + s.planDiffLineOffset)
		}
		return s, nil
	case "ctrl+y":
		if s.planHistoryDir != "" {
			s.PendingIntent = OpenPlanHistoryIntent{
				HistoryDir: s.planHistoryDir,
				HeadSHA:    s.planHistoryHeadSHA,
				ReadOnly:   false,
			}
		}
		return s, nil
	}

	return s, nil
}

// HandleCtrlCCancel handles the first Ctrl+C press by emitting the appropriate
// cancel intent based on current content mode.
func (s PipelineScreen) HandleCtrlCCancel() PipelineScreen {
	switch s.content {
	case ContentPlanReview:
		s.awaitingPlanDecision = false
		s.hasPlanComment = false
		s.PendingIntent = CancelPlanIntent{}
	case ContentStreaming, ContentAgentHistory:
		s.PendingIntent = CancelPipelineIntent{}
	case ContentUserQuestion:
		s.question = s.question.Cancel()
		answer := s.question.Answer()
		s.content = ContentStreaming
		s.hasQuestion = false
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
		s.SyncViewports()
	default:
		s.PendingIntent = CancelPipelineIntent{}
	}
	return s
}

func (s PipelineScreen) handleAgentHistoryKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.content = ContentStreaming
		s.focusedAgent = 0
		s.contentVP.GotoBottom()
		s.SyncViewports()
		return s, nil
	}
	return s, nil
}


// --- View rendering ---

// View renders the pipeline screen.
func (s PipelineScreen) View(width, height int) string {
	w := width
	if w < minWidth {
		w = minWidth
	}
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.viewFooter()

	// Input zone
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.viewInputZone() + "\n"

	// Status bar (1 line replacing old 6-line sidebar)
	sidebar := s.viewStatusLine(w) + "\n"

	// Content zone — viewports already synced in Update()
	contentHeight := max(0, s.contentVP.Height())
	var body string
	if s.showDashboard {
		body = s.dashboard.View()
	} else {
		body = s.contentVP.View()
	}
	if contentHeight > 0 {
		body = lipgloss.NewStyle().MaxHeight(contentHeight).Render(body)
		return body + "\n" + input + sidebar + footer
	}

	// No body zone — omit the body line terminator to preserve height invariant.
	return input + sidebar + footer
}

// --- Status Bar (1-line replacement for the old 6-line sidebar) ---

// shimmerFrames are the 5-frame animation for the status bar tail.
var shimmerFrames = []string{"·∘○∘·", "∘○∘·∘", "○∘·∘○", "∘·∘○∘", "·∘○∘·"}

// viewStatusLine renders the 1-line status bar with agent chain, live metrics,
// and shimmer animation. Applies left-overflow truncation when width is tight.
func (s PipelineScreen) viewStatusLine(width int) string {
	if len(s.agents) == 0 {
		if s.configName != "" {
			return dimStyle.Render(" " + s.configName)
		}
		return ""
	}

	// Build agent chain: "✓res ✓arch ▶work"
	var chain strings.Builder
	var activeRow *AgentRow
	for i := range s.agents {
		a := &s.agents[i]
		var icon string
		switch a.State {
		case AgentStateDone:
			icon = "✓"
		case AgentStateFailed:
			icon = "✗"
		case AgentStateCancelled:
			icon = "⊘"
		case AgentStateGate:
			icon = "●"
		case AgentStateRunning:
			icon = "▶"
			activeRow = a
		default:
			icon = "○"
		}
		name := a.ID
		if len(name) > 4 {
			name = name[:4]
		}
		if chain.Len() > 0 {
			chain.WriteString(" ")
		}
		chain.WriteString(icon)
		chain.WriteString(name)
	}

	// Build active agent detail (metrics)
	var detail string
	if activeRow != nil {
		var d strings.Builder
		if activeRow.ModelDisplay != "" {
			model := activeRow.ModelDisplay
			if len(model) > 16 {
				model = model[:16]
			}
			d.WriteString(model)
			d.WriteString(" ")
		}
		d.WriteString(fmt.Sprintf("↑%s ↓%s", formatTokenCompact(s.liveInput), formatTokenCompact(s.liveOutput)))

		if activeRow.ContextWindow > 0 {
			pct := (s.liveInput + s.liveOutput) * 100 / activeRow.ContextWindow
			d.WriteString(fmt.Sprintf(" ⊞%d%%", pct))
		}

		elapsed := time.Since(s.liveStart).Seconds()
		if elapsed > 0 && s.liveOutput > 0 {
			tokPS := float64(s.liveOutput) / elapsed
			d.WriteString(fmt.Sprintf(" %dt/s", int(tokPS)))
		}
		detail = d.String()
	}

	// Compose full line: " chain: detail"
	var full string
	if detail != "" {
		full = " " + chain.String() + ": " + detail
	} else {
		full = " " + chain.String()
	}

	// Truncation: drop tok/s, then truncate chain from left
	if len(full) > width && width > 4 {
		// Truncate from the left
		excess := len(full) - width + 3 // room for "<.."
		if excess < len(full) {
			full = " <.." + full[excess+1:]
		}
	}
	if len(full) > width {
		full = full[:width]
	}

	return dimStyle.Render(full)
}

// formatTokenCompact formats a token count as a compact string (e.g. 1234 → "1.2k").
func formatTokenCompact(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

func (s PipelineScreen) viewInputZone() string {
	if s.showHelp {
		return keyStyle.Render(" Press any key to dismiss help")
	}
	if s.showDashboard {
		return keyStyle.Render(" [Esc] return to split view")
	}
	switch s.content {
	case ContentStreaming:
		status := fmt.Sprintf(" %s running...", s.phase)
		if s.lastErr != nil {
			status = errorStyle.Render(fmt.Sprintf(" Error: %v", s.lastErr))
		}
		return status
	case ContentUserQuestion:
		return keyStyle.Render(s.question.InputZone())
	case ContentPlanReview:
		if s.hasPlanComment {
			return s.planComment.View() + "\n" +
				keyStyle.Render(" [Enter] send | [Shift+Enter] newline | [Esc] cancel")
		}
		return keyStyle.Render(" [^A] accept | [^E] edit in editor | [Enter] comment | [^D] diff")
	case ContentMergeConflict:
		return keyStyle.Render(" [^A] abort merge and keep worktree branch | [Esc] back to stream")
	case ContentEditConfirm:
		if s.hasEditComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard")
		}
		return keyStyle.Render(" [Enter] confirm | [Tab] add context | [Esc] discard")
	case ContentAgentHistory:
		agentName := ""
		if s.focusedAgent > 0 && s.focusedAgent <= len(s.agents) {
			agentName = s.agents[s.focusedAgent-1].ID
		}
		return keyStyle.Render(fmt.Sprintf(" viewing %s history (read-only)", agentName))
	case ContentCompletion:
		if s.lastErr != nil {
			return errorStyle.Render(fmt.Sprintf(" Error: %v", s.lastErr))
		}
		return keyStyle.Render(" Pipeline complete")
	}
	return ""
}

func (s PipelineScreen) viewContent(width int) string {
	switch s.content {
	case ContentStreaming, ContentPlanReview, ContentCompletion, ContentAgentHistory:
		if s.frameList.FrameCount() > 0 {
			return s.frameList.Render()
		}
		// Fallback to legacy rendering when no frames exist
		switch s.content {
		case ContentPlanReview:
			return s.viewPlanReview(width)
		case ContentCompletion:
			return s.viewCompletion(width)
		case ContentAgentHistory:
			return s.viewAgentHistory(width)
		default:
			return s.viewStreaming(width)
		}
	case ContentUserQuestion:
		return s.question.View(width)
	case ContentMergeConflict:
		return s.viewMergeConflict(width)
	case ContentEditConfirm:
		return s.viewEditConfirm(width)
	}
	return ""
}

func (s PipelineScreen) viewStreaming(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(renderPrefixedText(goalStyle, " Goal: ", s.goal, width))
	}

	var streamAgent string
	var completedLines []string
	var partial string
	var activities []orchestrator.Activity
	if s.streamBuf != nil {
		streamAgent, completedLines, partial, activities = s.streamBuf.SnapshotText()
	}

	b.WriteString(fmt.Sprintf(" Phase: %s", s.phase))
	if streamAgent != "" {
		b.WriteString(fmt.Sprintf("  (%s)", streamAgent))
	}
	b.WriteString("\n\n")

	if len(activities) > 0 {
		b.WriteString(renderActivityLog(activities, width, s.cwd, 20))
	}

	if len(completedLines) > 0 || partial != "" {
		b.WriteString("\n")
		b.WriteString(streamStyle.Render(" Stream"))
		b.WriteString("\n")

		// innerWidth is the content width inside the bordered block
		// (subtract 2 for borders + 2 for 1-char left/right padding).
		innerWidth := max(1, width-constContentInset-4)

		// Remove earlier occurrences of any repeated line; keep last.
		unique := deduplicateLines(completedLines)

		// Build block content lines (show last 15 lines when many).
		const previewMax = 15
		start := 0
		if len(unique) > previewMax {
			start = len(unique) - previewMax
		}
		var contentLines []string
		contentLines = append(contentLines, unique[start:]...)

		// Partial shown as one trailing line (last innerWidth bytes).
		if partial != "" {
			display := partial
			if len(display) > innerWidth {
				display = display[len(display)-innerWidth:]
			}
			contentLines = append(contentLines, display)
		}

		// Defensive height clamp.
		if vpH := s.contentVP.Height(); vpH > 0 {
			maxLines := max(3, vpH-8)
			if len(contentLines) > maxLines {
				contentLines = contentLines[len(contentLines)-maxLines:]
			}
		}

		// Word-wrap and render inside bordered block.
		// lipgloss .Width(innerWidth) word-wraps each \n-delimited line.
		content := strings.Join(contentLines, "\n")
		b.WriteString(streamBlockStyle.Width(innerWidth).Render(content))
		b.WriteString("\n")
	}
	return b.String()
}

func (s PipelineScreen) viewPlanReview(width int) string {
	if !s.hasPlan {
		return " Waiting for plan...\n"
	}
	var b strings.Builder
	if len(s.chatHistory) > 0 {
		for _, entry := range s.chatHistory {
			var roleLabel string
			var labelLen int
			if entry.Role == ChatRoleArchitect {
				roleLabel = goalStyle.Render(" Architect: ")
				labelLen = len(" Architect: ")
			} else {
				roleLabel = dimStyle.Render(" You: ")
				labelLen = len(" You: ")
			}
			b.WriteString(roleLabel)
			lines := strings.SplitN(entry.Text, "\n", 4)
			for i, line := range lines {
				if i == 3 {
					b.WriteString(dimStyle.Render("    ...\n"))
					break
				}
				if i > 0 {
					b.WriteString("    ")
				}
				var avail int
				if i == 0 {
					avail = width - labelLen
				} else {
					avail = width - 4
				}
				if avail > 0 && len(line) > avail {
					line = line[:avail]
				}
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
		b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
		b.WriteString("\n")
	}
	b.WriteString(renderMarkdown(s.finalPlan, width))
	return b.String()
}

func (s PipelineScreen) viewCompletion(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(renderPrefixedText(goalStyle, " Goal: ", s.goal, width))
		b.WriteString("\n")
	}
	if s.lastErr != nil {
		b.WriteString(renderPrefixedText(errorStyle, " Error: ", s.lastErr.Error(), width))
	}
	if s.hasValidation {
		b.WriteString(" Validation:\n")
		b.WriteString(renderPrefixedText(lipgloss.NewStyle(), "   ", s.workerValidation, width))
	}
	if s.mergeErrorMsg != "" {
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(" ⚠ Merge failed — manual recovery required"))
		b.WriteString("\n")
		b.WriteString(renderPrefixedText(dimStyle, "   ", s.mergeErrorMsg, width))
		b.WriteString("\n")
		if s.mergeErrorBranch != "" {
			b.WriteString(renderPrefixedText(dimStyle, "   ", "Preserved branch: "+s.mergeErrorBranch, width))
			b.WriteString("\n")
		}
		if s.mergeErrorPath != "" {
			b.WriteString(renderPrefixedText(dimStyle, "   ", "Preserved worktree: "+s.mergeErrorPath, width))
			b.WriteString("\n")
		}
		if s.mergeErrorBranch != "" {
			b.WriteString(keyStyle.Render("   To merge manually from the repo root: git merge " + s.mergeErrorBranch))
		}
		b.WriteString("\n")
	}
	elapsed := time.Since(s.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf("\n Elapsed: %s\n", elapsed))

	var totalIn, totalOut int64
	for _, a := range s.agents {
		totalIn += a.InputTokens
		totalOut += a.OutputTokens
	}
	if totalIn+totalOut > 0 {
		b.WriteString(fmt.Sprintf(" Tokens: %s in, %s out (%s total)\n",
			formatTokens(totalIn), formatTokens(totalOut), formatTokens(totalIn+totalOut)))
	}

	b.WriteString("\n Run Summary\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
	b.WriteString("\n")

	for _, a := range s.agents {
		agentElapsed := "-"
		if a.Elapsed > 0 {
			agentElapsed = a.Elapsed.Round(time.Second).String()
		} else if !a.StartedAt.IsZero() {
			agentElapsed = time.Since(a.StartedAt).Round(time.Second).String()
		}

		tokens := "-"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			tokens = fmt.Sprintf("↓%s ↑%s", formatTokens(a.InputTokens), formatTokens(a.OutputTokens))
		}

		b.WriteString(fmt.Sprintf(" Agent: %s (%s)  ⏱ %s  Tokens: %s\n", goalStyle.Render(a.ID), a.State, agentElapsed, tokens))

		var activities []orchestrator.Activity
		if s.history != nil {
			activities = s.history.AgentActivities(orchestrator.AgentID(a.ID))
		} else if s.streamBuf != nil {
			activities = s.streamBuf.AgentActivities(a.ID)
		}
		var fileActivities []orchestrator.Activity
		for _, act := range activities {
			if isFilePathTool(act.Tool) {
				fileActivities = append(fileActivities, act)
			}
		}
		if len(fileActivities) > 0 {
			b.WriteString(renderActivityLog(fileActivities, width, s.cwd, 3))
		} else {
			b.WriteString("   (no file activities)\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (s PipelineScreen) viewAgentHistory(width int) string {
	var b strings.Builder
	if s.focusedAgent > 0 && s.focusedAgent <= len(s.agents) {
		a := s.agents[s.focusedAgent-1]
		tokens := "Tokens: -"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			tokens = fmt.Sprintf("Tokens: ↓%s ↑%s", formatTokens(a.InputTokens), formatTokens(a.OutputTokens))
		}

		var elapsed string
		if a.Elapsed > 0 {
			elapsed = a.Elapsed.Round(time.Second).String()
		} else if !a.StartedAt.IsZero() {
			elapsed = time.Since(a.StartedAt).Round(time.Second).String()
		} else {
			elapsed = "-"
		}

		b.WriteString(fmt.Sprintf(" Agent: %s (%s)  %s  ⏱ %s\n", goalStyle.Render(a.ID), a.State, tokens, elapsed))
		b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
		b.WriteString("\n")

		var activities []orchestrator.Activity
		if s.history != nil {
			activities = s.history.AgentActivities(orchestrator.AgentID(a.ID))
		} else if s.streamBuf != nil {
			activities = s.streamBuf.AgentActivities(a.ID)
		}
		if len(activities) > 0 {
			b.WriteString(renderActivityLog(activities, width, s.cwd, len(activities)))
		} else {
			b.WriteString(fmt.Sprintf(" No activity recorded for %s\n", a.ID))
		}
	} else {
		b.WriteString(" No agent selected\n")
	}
	return b.String()
}

// buildCompletionSummary constructs the text content for the CompletionFrame.
func (s *PipelineScreen) buildCompletionSummary() string {
	var b strings.Builder

	if s.lastErr != nil {
		b.WriteString("Error: " + s.lastErr.Error() + "\n\n")
	}
	if s.hasValidation {
		b.WriteString("Validation:\n" + s.workerValidation + "\n\n")
	}
	if s.mergeErrorMsg != "" {
		b.WriteString("⚠ Merge failed — manual recovery required\n")
		b.WriteString("  " + s.mergeErrorMsg + "\n")
		if s.mergeErrorBranch != "" {
			b.WriteString("  Branch: " + s.mergeErrorBranch + "\n")
			b.WriteString("  git merge " + s.mergeErrorBranch + "\n")
		}
		b.WriteString("\n")
	}

	elapsed := time.Since(s.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf("Elapsed: %s\n", elapsed))

	var totalIn, totalOut int64
	for _, a := range s.agents {
		totalIn += a.InputTokens
		totalOut += a.OutputTokens
	}
	if totalIn+totalOut > 0 {
		b.WriteString(fmt.Sprintf("Tokens: %s in, %s out (%s total)\n",
			formatTokens(totalIn), formatTokens(totalOut), formatTokens(totalIn+totalOut)))
	}

	b.WriteString("\nRun Summary\n")
	for _, a := range s.agents {
		agentElapsed := "-"
		if a.Elapsed > 0 {
			agentElapsed = a.Elapsed.Round(time.Second).String()
		}
		tokens := "-"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			tokens = fmt.Sprintf("↓%s ↑%s", formatTokens(a.InputTokens), formatTokens(a.OutputTokens))
		}
		b.WriteString(fmt.Sprintf("  %s (%s) ⏱ %s  %s\n", a.ID, a.State, agentElapsed, tokens))
	}

	return b.String()
}


// --- Shared helper functions ---

// renderActivityLog renders recent tool activities as a multi-line vertical log.
func renderActivityLog(activities []orchestrator.Activity, width int, cwd string, maxShow int) string {
	start := 0
	if len(activities) > maxShow {
		start = len(activities) - maxShow
	}
	recent := activities[start:]

	var b strings.Builder
	for _, act := range recent {
		toolName := act.Tool
		icon := IconForAction(toolName)

		toolLabel := activityToolStyle.Render(fmt.Sprintf(" %s %-10s", icon, toolName))
		detail := act.Detail
		if isFilePathTool(act.Tool) && detail != "" {
			detail = fileHyperlink(detail, cwd)
			b.WriteString(toolLabel + " " + activityPathStyle.Render(detail))
		} else {
			b.WriteString(toolLabel + " " + activityDetailStyle.Render(detail))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s PipelineScreen) handleMergeConflictKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.String() {
	case "ctrl+a":
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = AbortMergeIntent{}
		return s, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		s.content = ContentStreaming
		s.SyncViewports()
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) viewMergeConflict(width int) string {
	var b strings.Builder
	b.WriteString(warnStyle.Render(" Merge Conflict"))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf(" The worktree branch %s has conflicts with %s.\n\n",
		goalStyle.Render(s.mergeConflict.WorktreeBranch),
		goalStyle.Render(s.mergeConflict.TargetBranch)))
	b.WriteString(" Conflicting files:\n")
	for _, f := range s.mergeConflict.ConflictFiles {
		b.WriteString(fmt.Sprintf("   %s %s\n", errorStyle.Render("✗"), f))
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf(" The worktree branch %q is preserved.\n", s.mergeConflict.WorktreeBranch)))
	if s.mergeConflict.WorktreePath != "" {
		b.WriteString(dimStyle.Render(fmt.Sprintf(" Preserved worktree: %s\n", s.mergeConflict.WorktreePath)))
	}
	b.WriteString(dimStyle.Render(" Resolve manually: git merge " + s.mergeConflict.WorktreeBranch + "\n"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render(" [^A] abort (keep branch for manual merge)   [Esc] continue"))
	return b.String()
}

func (s PipelineScreen) handleEditConfirmKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	// If comment textarea is active, forward keys to it
	if s.hasEditComment {
		switch msg.Code {
		case tea.KeyTab:
			// Collapse comment textarea
			s.hasEditComment = false
			s.SyncViewports()
			return s, nil
		case tea.KeyEscape:
			// Cancel comment, return to option selection
			s.editConfirmComment.Reset()
			s.hasEditComment = false
			s.SyncViewports()
			return s, nil
		case tea.KeyEnter:
			if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
				s.editConfirmComment.InsertString("\n")
				return s, nil
			}
			// Confirm with comment
			comment := strings.TrimSpace(s.editConfirmComment.Value())
			s.PendingIntent = ConfirmEditIntent{
				EditedContent: s.pendingEditContent,
				Comment:       comment,
				AutoApprove:   comment == "",
			}
			s.pendingEditContent = ""
			s.hasEditComment = false
			s.awaitingPlanDecision = false
			s.content = ContentStreaming
			s.SyncViewports()
			return s, nil
		}
		if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) && !msg.Mod.Contains(tea.ModMeta) {
			var cmd tea.Cmd
			s.editConfirmComment, cmd = s.editConfirmComment.Update(msg)
			return s, cmd
		}
		return s, nil
	}

	// Option selection mode
	switch msg.Code {
	case tea.KeyUp:
		if s.editConfirmCursor > 0 {
			s.editConfirmCursor--
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyDown:
		if s.editConfirmCursor < 1 {
			s.editConfirmCursor++
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyTab:
		if s.editConfirmCursor == 0 {
			// Open comment textarea for "Yes"
			ta := textarea.New()
			ta.Placeholder = "Describe your changes..."
			ta.SetWidth(max(1, s.contentVP.Width()-6))
			ta.SetHeight(2)
			ta.CharLimit = 1024
			ta.Focus()
			s.editConfirmComment = ta
			s.hasEditComment = true
			s.SyncViewports()
			return s, nil
		}
		return s, nil
	case tea.KeyEnter:
		if s.editConfirmCursor == 0 {
			// "Yes" — confirm edit
			comment := ""
			if s.hasEditComment {
				comment = strings.TrimSpace(s.editConfirmComment.Value())
			}
			s.PendingIntent = ConfirmEditIntent{
				EditedContent: s.pendingEditContent,
				Comment:       comment,
				AutoApprove:   comment == "",
			}
			s.pendingEditContent = ""
			s.hasEditComment = false
			s.awaitingPlanDecision = false
			s.content = ContentStreaming
			s.SyncViewports()
			return s, nil
		}
		// "No" — discard edit, return to plan review
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentPlanReview
		s.awaitingPlanDecision = true
		contentWidth := max(1, s.contentVP.Width()-4)
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(contentWidth)
		s.planComment.SetHeight(2)
		s.planComment.CharLimit = 1024
		s.planComment.Focus()
		s.hasPlanComment = true
		s.SyncViewports()
		return s, nil
	case tea.KeyEscape:
		// Same as "No"
		s.pendingEditContent = ""
		s.hasEditComment = false
		s.content = ContentPlanReview
		s.awaitingPlanDecision = true
		contentWidth := max(1, s.contentVP.Width()-4)
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(contentWidth)
		s.planComment.SetHeight(2)
		s.planComment.CharLimit = 1024
		s.planComment.Focus()
		s.hasPlanComment = true
		s.SyncViewports()
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) viewEditConfirm(width int) string {
	var b strings.Builder

	b.WriteString(goalStyle.Render("  Plan was modified"))
	b.WriteString("\n\n")
	b.WriteString("  Apply these changes?\n\n")

	options := []string{"Yes, apply changes", "No, discard changes"}
	for i, opt := range options {
		cursor := "  "
		style := dimStyle
		if i == s.editConfirmCursor {
			cursor = "> "
			style = phaseStyle.Bold(true)
		}
		b.WriteString(style.Render(cursor + opt))
		if i == 0 && s.editConfirmCursor == 0 {
			b.WriteString(dimStyle.Render("  [Tab: add context]"))
		}
		b.WriteString("\n")
	}

	if s.hasEditComment {
		b.WriteString("\n")
		b.WriteString(s.editConfirmComment.View())
		b.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// isFilePathTool returns true if the tool's detail field is a file path.
func isFilePathTool(tool string) bool {
	switch tool {
	case "Read", "Write", "MultiEdit", "TodoRead", "TodoWrite":
		return true
	}
	return false
}

// fileHyperlink wraps a file path in an OSC 8 terminal hyperlink sequence.
func fileHyperlink(path string, cwd string) string {
	absPath := path
	if !strings.HasPrefix(path, "/") {
		absPath = filepath.Join(cwd, path)
	}
	return fmt.Sprintf("\033]8;;file://%s\033\\%s\033]8;;\033\\", absPath, path)
}

// formatTokens renders a token count in compact form (e.g., 1.2k, 12.4k, 128k).
func formatTokens(n int64) string {
	if n == 0 {
		return "-"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}
