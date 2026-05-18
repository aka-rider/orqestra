package tui

import (
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// ChatEntry is one turn in the user-architect conversation during plan review.
type ChatEntry struct {
	Role          string // "you" or "architect"
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
	planDiff        string         // unified diff from git micro-repo
	diffViewport    viewport.Model // paginated viewport for diff rendering
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

	// UI state
	configName    string
	showDashboard bool
	showHelp      bool
	ctrlCPending  bool // set by parent model when Ctrl+C time gate is active
	active        bool // true while pipeline is running

	// Viewports
	contentVP   viewport.Model
	sidebarVP   viewport.Model
	dashboardVP viewport.Model
	bounds      layoutBounds

	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewPipelineScreen creates a new pipeline screen.
func NewPipelineScreen(configName string) PipelineScreen {
	cvp := viewport.New()
	cvp.MouseWheelEnabled = true
	svp := viewport.New()
	svp.MouseWheelEnabled = true
	dvp := viewport.New()
	dvp.MouseWheelEnabled = true

	return PipelineScreen{
		configName:  configName,
		contentVP:   cvp,
		sidebarVP:   svp,
		dashboardVP: dvp,
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
	s.hasPlanComment = false
	s.editorRunning = false
	s.awaitingPlanDecision = false
	s.chatHistory = nil
	s.planDiff = ""
	s.diffViewport = viewport.New()
	s.diffViewport.MouseWheelEnabled = true
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
	s.contentVP.SetContent("")
	s.sidebarVP.SetContent("")
	s.dashboardVP.SetContent("")
	s.contentVP.GotoTop()
	s.sidebarVP.GotoTop()
	s.dashboardVP.GotoTop()
}

// SetStreamBuf sets the shared stream buffer for live output.
func (s *PipelineScreen) SetStreamBuf(buf *orchestrator.StreamRing) {
	s.streamBuf = buf
}

// SyncViewports updates pipeline viewport content from current screen state.
func (s *PipelineScreen) SyncViewports() {
	w := s.effectiveWidth()

	// Keep diff viewport dimensions in sync
	s.diffViewport.SetWidth(w)
	s.diffViewport.SetHeight(max(1, s.contentVP.Height()-3))

	if s.showDashboard {
		s.dashboardVP.SetContent(s.viewDashboard())
	} else {
		var contentView string
		if s.showHelp {
			contentView = s.viewHelp()
		} else {
			contentView = s.viewContent(w)
		}
		atBottom := s.contentVP.AtBottom()
		s.contentVP.SetContent(contentView)
		if atBottom && s.content == ContentStreaming {
			s.contentVP.GotoBottom()
		}
		s.sidebarVP.SetContent(s.viewSidebar(w))
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
	s.sidebarVP.SetWidth(width)
	s.sidebarVP.SetHeight(constSidebarHeight)
	s.dashboardVP.SetWidth(width)
	s.dashboardVP.SetHeight(contentHeight)
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
		s.agents = append(s.agents, AgentRow{ID: event.AgentID, State: "running", StartedAt: time.Now()})

	case orchestrator.EventAgentDone:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = "done"
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

	case orchestrator.EventAgentFailed:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = "failed"
			}
		}
		s.lastErr = event.Err

	case orchestrator.EventAgentCancelled:
		for i := range s.agents {
			if s.agents[i].ID == event.AgentID {
				s.agents[i].State = "cancelled"
			}
		}

	case orchestrator.EventGateRequest:
		s.contentVP.GotoTop()
		switch event.Gate.Type {
		case orchestrator.GatePlanApproval:
			s.planDiff = event.Gate.PlanDiff
			s.planHistoryDir = event.Gate.PlanHistoryDir
			s.planHistoryHeadSHA = event.Gate.PlanHistoryHeadSHA
			s.diffViewport.SetContent(s.planDiff)
			if len(s.chatHistory) > 0 && s.planDiff != "" {
				s.chatHistory = append(s.chatHistory, ChatEntry{
					Role: "architect", Text: "(plan revised — see diff with [^D])", HasPlanChange: true,
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
		}

	case orchestrator.EventPlanReady:
		s.finalPlan = event.FinalPlan
		s.hasPlan = true

	case orchestrator.EventRunDirReady:
		s.runDir = event.RunDir

	case orchestrator.EventChatResponse:
		s.chatHistory = append(s.chatHistory, ChatEntry{Role: "architect", Text: event.ChatText})
		s.content = ContentPlanReview
		s.awaitingPlanDecision = true
		contentWidth := max(1, width)
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(max(1, contentWidth-4))
		s.planComment.SetHeight(2)
		s.planComment.CharLimit = 1024
		s.planComment.Focus()
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

	case orchestrator.EventComplete:
		s.content = ContentCompletion
		s.active = false
		s.contentVP.GotoTop()
		if event.WorkerValidation != "" {
			s.workerValidation = event.WorkerValidation
			s.hasValidation = true
		}
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
		if s.content != ContentPlanReview && s.content != ContentPlanDiff && s.content != ContentUserQuestion {
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
			s.dashboardVP, cmd = s.dashboardVP.Update(msg)
		} else {
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

	// If dashboard is showing, Esc returns to split view
	if s.showDashboard {
		if msg.Code == tea.KeyEscape {
			s.showDashboard = false
			s.SyncViewports()
		}
		return s, nil
	}

	// Alt+1-9 for agent navigation
	altKeys := map[string]int{
		"alt+1": 1, "alt+2": 2, "alt+3": 3, "alt+4": 4, "alt+5": 5,
		"alt+6": 6, "alt+7": 7, "alt+8": 8, "alt+9": 9,
	}
	if idx, ok := altKeys[msg.String()]; ok && s.content != ContentPlanReview && s.content != ContentUserQuestion {
		if idx <= len(s.agents) {
			s.focusedAgent = idx
			s.content = ContentAgentHistory
			s.contentVP.GotoTop()
			s.SyncViewports()
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
	case ContentPlanDiff:
		return s.handlePlanDiffKey(msg)
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
		s.dashboardVP, cmd = s.dashboardVP.Update(msg)
	} else if p.In(s.bounds.sidebar) {
		s.sidebarVP, cmd = s.sidebarVP.Update(msg)
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
				s.chatHistory = append(s.chatHistory, ChatEntry{Role: "you", Text: comment})
				s.planComment.Reset()
				s.hasPlanComment = false
				s.awaitingPlanDecision = false
				s.content = ContentStreaming
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
		s.SyncViewports()
		s.PendingIntent = ApprovePlanIntent{}
		return s, nil
	case "ctrl+d":
		if s.planDiff != "" {
			s.content = ContentPlanDiff
			s.hasPlanComment = false
			s.contentVP.GotoTop()
			s.SyncViewports()
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

func (s PipelineScreen) handlePlanDiffKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.Code == tea.KeyEscape || msg.String() == "ctrl+d" {
		s.content = ContentPlanReview
		s.contentVP.GotoTop()
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(max(1, s.contentVP.Width()-4))
		s.planComment.SetHeight(2)
		s.planComment.CharLimit = 1024
		s.planComment.Focus()
		s.hasPlanComment = true
		s.awaitingPlanDecision = true
		s.SyncViewports()
		return s, nil
	}

	// Pass navigation keys to the diff viewport
	var cmd tea.Cmd
	s.diffViewport, cmd = s.diffViewport.Update(msg)
	return s, cmd
}

func (s PipelineScreen) handleAgentHistoryKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.content = ContentStreaming
		s.focusedAgent = 0
		s.contentVP.GotoTop()
		s.SyncViewports()
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) handleStreamingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.String() {
	case "ctrl+n":
		if s.active {
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) handleCompletionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	switch msg.String() {
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case "ctrl+n":
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case "ctrl+q":
		return s, tea.Quit
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

	// Sidebar strip (full-width agent list below input)
	var sidebarDiv string
	if s.configName != "" {
		title := " " + s.configName + " "
		repeat := max(0, w-len(title))
		sidebarDiv = dividerStyle.Render(title + strings.Repeat("─", repeat))
	} else {
		sidebarDiv = dividerStyle.Render(strings.Repeat("─", w))
	}
	sidebar := sidebarDiv + "\n" +
		s.sidebarVP.View()

	// Content zone — viewports already synced in Update()
	contentHeight := max(0, s.contentVP.Height())
	var body string
	if s.showDashboard {
		body = s.dashboardVP.View()
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
	case ContentStreaming:
		return s.viewStreaming(width)
	case ContentUserQuestion:
		return s.question.View(width)
	case ContentPlanReview:
		return s.viewPlanReview(width)
	case ContentPlanDiff:
		return s.viewPlanDiff(width)
	case ContentAgentHistory:
		return s.viewAgentHistory(width)
	case ContentCompletion:
		return s.viewCompletion(width)
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
	var streamLines []string
	var activities []orchestrator.Activity
	if s.streamBuf != nil {
		streamAgent, streamLines, activities = s.streamBuf.SnapshotCompat()
	}

	b.WriteString(fmt.Sprintf(" Phase: %s", s.phase))
	if streamAgent != "" {
		b.WriteString(fmt.Sprintf("  (%s)", streamAgent))
	}
	b.WriteString("\n\n")

	if len(activities) > 0 {
		b.WriteString(renderActivityLog(activities, width, s.cwd, 20))
	}

	if len(streamLines) > 0 {
		b.WriteString("\n")
		b.WriteString(streamStyle.Render(" Stream"))
		b.WriteString("\n")

		start := 0
		if len(streamLines) > streamPreviewLines {
			start = len(streamLines) - streamPreviewLines
		}

		maxLineWidth := width - constContentInset
		if maxLineWidth < 1 {
			maxLineWidth = 1
		}
		for _, line := range streamLines[start:] {
			for len(line) > maxLineWidth {
				b.WriteString(" ")
				b.WriteString(streamStyle.Render(line[:maxLineWidth]))
				b.WriteString("\n")
				line = line[maxLineWidth:]
			}
			b.WriteString(" ")
			b.WriteString(streamStyle.Render(line))
			b.WriteString("\n")
		}
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
			if entry.Role == "architect" {
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

func (s PipelineScreen) viewPlanDiff(width int) string {
	var b strings.Builder
	b.WriteString(goalStyle.Render(" Plan Diff (last revision)"))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
	b.WriteString("\n")
	if s.planDiff == "" {
		b.WriteString(" No diff available.\n")
	} else {
		b.WriteString(s.diffViewport.View())
	}
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

		if s.streamBuf != nil {
			activities := s.streamBuf.AgentActivities(a.ID)
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

		if s.streamBuf != nil {
			activities := s.streamBuf.AgentActivities(a.ID)
			if len(activities) > 0 {
				b.WriteString(renderActivityLog(activities, width, s.cwd, len(activities)))
			} else {
				b.WriteString(fmt.Sprintf(" No activity recorded for %s\n", a.ID))
			}
		} else {
			b.WriteString(fmt.Sprintf(" No activity recorded for %s\n", a.ID))
		}
	} else {
		b.WriteString(" No agent selected\n")
	}
	return b.String()
}

func (s PipelineScreen) viewDashboard() string {
	w := s.dashboardVP.Width()
	if w < minWidth {
		w = minWidth
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(" %-14s   %-7s %5s %8s %8s %7s  %s\n",
		"Agent", "State", "Time", "In Tok", "Out Tok", "Tok/s", "Context"))
	b.WriteString(strings.Repeat("─", w))
	b.WriteString("\n")
	for _, a := range s.agents {
		icon := "○"
		stateStr := "wait"
		switch a.State {
		case "running":
			icon = "▶"
			stateStr = "run"
		case "done":
			icon = "✓"
			stateStr = "done"
		case "failed":
			icon = "✗"
			stateStr = "fail"
		case "cancelled":
			icon = "⊘"
			stateStr = "cancel"
		case "gate":
			icon = "●"
			stateStr = "gate"
		}

		elapsed := "-"
		if a.State == "running" && !a.StartedAt.IsZero() {
			elapsed = time.Since(a.StartedAt).Truncate(time.Second).String()
		} else if a.Elapsed > 0 {
			elapsed = a.Elapsed.Truncate(time.Second).String()
		}

		inTok := "-"
		outTok := "-"
		tokPS := "-"
		ctxBar := "░░░░░░░░  -"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			inTok = formatTokenCount(a.InputTokens)
			outTok = formatTokenCount(a.OutputTokens)
			if a.Elapsed.Seconds() > 0 {
				tokPS = fmt.Sprintf("%.1f", float64(a.OutputTokens)/a.Elapsed.Seconds())
			}
			pct := float64(a.InputTokens+a.OutputTokens) / constDefaultContextWindow * 100
			if pct > 100 {
				pct = 100
			}
			filled := int(pct / 12.5)
			empty := 8 - filled
			if empty < 0 {
				empty = 0
			}
			ctxBar = fmt.Sprintf("%s%s %2.0f%%",
				strings.Repeat("█", filled),
				strings.Repeat("░", empty),
				pct)
		}

		b.WriteString(fmt.Sprintf(" %-14s %s %-7s %5s %8s %8s %7s  %s\n",
			a.ID, icon, stateStr, elapsed, inTok, outTok, tokPS, ctxBar))
	}
	return b.String()
}

func (s PipelineScreen) viewHelp() string {
	return ` Orqestra Keybindings
─────────────────────────────────
 [Enter]       Submit prompt / confirm
 [PgUp/PgDn]   Scroll content
 [Ctrl+A]      Accept plan / abort merge
 [Ctrl+E]      Edit plan in external editor
 [Ctrl+D]      Toggle dashboard / diff
 [Ctrl+N]      New run
 [Ctrl+R]      Historical runs
 [Ctrl+Q]      Quit (at completion)
 [Ctrl+H]      Toggle this help
 [Ctrl+Y]      Plan history viewer
 [Alt+1-9]     View agent output
 [Ctrl+C]      Cancel → exit (time-gated)
 [Esc]         Back / dismiss
`
}

func (s PipelineScreen) viewSidebar(width int) string {
	var b strings.Builder
	b.WriteString(" Agents\n")
	b.WriteString(strings.Repeat("─", width))
	b.WriteString("\n")

	var totalTokens int64
	for _, a := range s.agents {
		icon := "○"
		switch a.State {
		case "running":
			icon = "▶"
		case "done":
			icon = "✓"
		case "failed":
			icon = "✗"
		case "cancelled":
			icon = "⊘"
		case "gate":
			icon = "●"
		}

		elapsed := "-"
		if a.State == "running" && !a.StartedAt.IsZero() {
			elapsed = time.Since(a.StartedAt).Truncate(time.Second).String()
		} else if a.Elapsed > 0 {
			elapsed = a.Elapsed.Truncate(time.Second).String()
		}

		tokens := "-"
		total := a.InputTokens + a.OutputTokens
		if total > 0 {
			tokens = formatTokens(total)
		}
		totalTokens += total

		id := a.ID
		maxID := width - 16
		if maxID < 4 {
			maxID = 4
		}
		if len(id) > maxID {
			id = id[:maxID]
		}

		b.WriteString(fmt.Sprintf(" %s %-*s %5s %6s\n", icon, maxID, id, elapsed, tokens))
	}
	if len(s.agents) == 0 {
		b.WriteString(" (waiting)\n")
	}

	if totalTokens > 0 || len(s.agents) > 0 {
		b.WriteString(strings.Repeat("─", width))
		b.WriteString("\n")
		totalElapsed := time.Since(s.startTime).Truncate(time.Second)
		b.WriteString(fmt.Sprintf(" total: %s | %s\n", formatTokens(totalTokens), totalElapsed))
	}

	return b.String()
}

func (s PipelineScreen) viewFooter() string {
	ctrlCHint := "[^C] cancel"
	if s.ctrlCPending {
		ctrlCHint = warnStyle.Render("[^C] EXIT")
	}
	switch s.content {
	case ContentUserQuestion:
		return keyStyle.Render(s.question.Footer()+"  [^H] help  ") + ctrlCHint
	case ContentPlanReview:
		footer := " [^A] accept | [^E] edit in editor | [Enter] comment | [Shift+Enter] newline"
		if s.planDiff != "" {
			footer += " | [^D] diff"
		}
		if s.planHistoryDir != "" {
			footer += " | [^Y] history"
		}
		footer += "  " + ctrlCHint
		if len(s.chatHistory) > 0 && (s.reviewTokensIn+s.reviewTokensOut > 0) {
			footer += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
		}
		return keyStyle.Render(footer)
	case ContentEditConfirm:
		if s.hasEditComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard                    [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] discard  [^H] help  ") + ctrlCHint
	case ContentPlanDiff:
		return keyStyle.Render(" [Esc] return to plan | [^D] return to plan              [^H] help  ") + ctrlCHint
	case ContentMergeConflict:
		return keyStyle.Render(" [^A] abort merge | [Esc] continue                       [^H] help  ") + ctrlCHint
	case ContentAgentHistory:
		return keyStyle.Render(" [Esc] back to live                  [^D] expand  [^N] new  [^H] help  ") + ctrlCHint
	case ContentCompletion:
		return keyStyle.Render(" [^N] new run | [^R] runs | [^Q] quit                    [^H] help")
	default:
		return keyStyle.Render(" [^N] new run                    [^D] expand  [Alt+N] agent  [^H] help  ") + ctrlCHint
	}
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

// formatTokenCount formats a token count with comma separators.
func formatTokenCount(n int64) string {
	if n == 0 {
		return "-"
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
