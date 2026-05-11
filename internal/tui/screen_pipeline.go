package tui

import (
	"fmt"
	"image"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// PipelineScreen manages the pipeline execution view with all content modes.
type PipelineScreen struct {
	content   ContentMode
	phase     orchestrator.Phase
	goal      string
	startTime time.Time

	// Sidebar state
	agents []AgentRow

	// Content state
	gatewayResult    agent.GatewayResult
	finalPlan        string
	hasPlan          bool
	workerValidation string
	hasValidation    bool
	lastErr          error

	// Streaming output — shared buffer polled on tick
	streamBuf *orchestrator.StreamBuffer

	// Coaching input
	answerFields []textarea.Model
	answerCursor int

	// Plan edit state
	planEditor    textarea.Model
	hasPlanEditor bool

	// Plan review state
	planComment    textarea.Model
	hasPlanComment bool
	editorRunning  bool

	// Agent history navigation
	focusedAgent int

	// Run directory and plan file
	runDir       string
	planFilePath string

	awaitingPlanDecision bool

	// User question state (MCP AskUserQuestion bridge)
	userQuestion     harness.MCPToolCall
	questionCursor   int
	questionSelected map[int]bool   // for multi-select
	questionTextarea textarea.Model // for freeform or custom text
	hasQuestionTA    bool           // true when freeform textarea is active

	// UI state
	configName    string
	showDashboard bool
	showHelp      bool
	confirmNew    bool
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
	s.content = ContentStreaming
	s.active = true
}

// Reset clears all pipeline state for a fresh run.
func (s *PipelineScreen) Reset() {
	s.agents = nil
	s.lastErr = nil
	s.gatewayResult = agent.GatewayResult{}
	s.finalPlan = ""
	s.hasPlan = false
	s.workerValidation = ""
	s.hasValidation = false
	s.streamBuf = nil
	s.answerFields = nil
	s.answerCursor = 0
	s.hasPlanEditor = false
	s.hasPlanComment = false
	s.editorRunning = false
	s.awaitingPlanDecision = false
	s.focusedAgent = 0
	s.runDir = ""
	s.planFilePath = ""
	s.showDashboard = false
	s.showHelp = false
	s.confirmNew = false
	s.active = false
	s.phase = ""
	s.contentVP.SetContent("")
	s.sidebarVP.SetContent("")
	s.dashboardVP.SetContent("")
	s.contentVP.GotoTop()
	s.sidebarVP.GotoTop()
	s.dashboardVP.GotoTop()
}

// SetStreamBuf sets the shared stream buffer for live output.
func (s *PipelineScreen) SetStreamBuf(buf *orchestrator.StreamBuffer) {
	s.streamBuf = buf
}

// SyncViewports updates pipeline viewport content from current screen state.
func (s *PipelineScreen) SyncViewports() {
	w := s.effectiveWidth()
	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)

	if s.showDashboard {
		s.dashboardVP.SetContent(s.viewDashboard())
	} else {
		var contentView string
		if s.showHelp {
			contentView = s.viewHelp()
		} else {
			contentView = s.viewContent(contentWidth)
		}
		atBottom := s.contentVP.AtBottom()
		s.contentVP.SetContent(contentView)
		if atBottom && s.content == ContentStreaming {
			s.contentVP.GotoBottom()
		}
		s.sidebarVP.SetContent(s.viewSidebar(sidebarWidth))
	}
}

func (s PipelineScreen) effectiveWidth() int {
	if s.contentVP.Width()+s.sidebarVP.Width()+1 >= minWidth {
		return s.contentVP.Width() + s.sidebarVP.Width() + 1
	}
	return minWidth
}

// RecalculateLayout updates viewport dimensions from parent-computed values.
func (s *PipelineScreen) RecalculateLayout(width, contentHeight int) {
	contentWidth := max(0, int(float64(width)*splitRatio))
	sidebarWidth := max(0, width-contentWidth-1)

	s.contentVP.SetWidth(contentWidth)
	s.contentVP.SetHeight(contentHeight)
	s.sidebarVP.SetWidth(sidebarWidth)
	s.sidebarVP.SetHeight(contentHeight)
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
		case orchestrator.GateGatewayCoach:
			s.content = ContentCoaching
			s.gatewayResult = event.Gate.GatewayResult
			contentWidth := max(1, int(float64(width)*splitRatio))
			s.answerFields = make([]textarea.Model, len(s.gatewayResult.Questions))
			for i, q := range s.gatewayResult.Questions {
				ta := textarea.New()
				ta.SetWidth(max(1, contentWidth-4))
				ta.SetHeight(1)
				ta.CharLimit = 512
				if q.Default != "" {
					ta.SetValue(q.Default)
				}
				if i == 0 {
					ta.Focus()
				}
				s.answerFields[i] = ta
			}
			s.answerCursor = 0

		case orchestrator.GatePlanApproval:
			s.awaitingPlanDecision = true
			s.content = ContentPlanReview
			s.finalPlan = event.Gate.FinalPlanMarkdown
			s.hasPlan = true
			s.planFilePath = event.Gate.PlanFilePath
			contentWidth := max(1, int(float64(width)*splitRatio))
			s.planComment = textarea.New()
			s.planComment.Placeholder = "Comment to refine the plan..."
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

	case orchestrator.EventError:
		s.lastErr = event.Err

	case orchestrator.EventUserQuestion:
		s.content = ContentUserQuestion
		s.userQuestion = event.UserQuestion
		s.questionCursor = 0
		s.questionSelected = make(map[int]bool)
		s.contentVP.GotoTop()
		if len(event.UserQuestion.Options) == 0 {
			// Freeform mode
			contentWidth := max(1, int(float64(width)*splitRatio))
			ta := textarea.New()
			ta.Placeholder = "Type your answer..."
			ta.SetWidth(max(1, contentWidth-4))
			ta.SetHeight(3)
			ta.CharLimit = 1024
			ta.Focus()
			s.questionTextarea = ta
			s.hasQuestionTA = true
		} else {
			s.hasQuestionTA = false
		}

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
	case "?":
		s.showHelp = !s.showHelp
		s.SyncViewports()
		return s, nil
	case "d", "D":
		if s.content != ContentCoaching && s.content != ContentPlanEdit && s.content != ContentPlanReview && s.content != ContentUserQuestion {
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

	// Number keys for agent navigation (1-9)
	if msg.String() >= "1" && msg.String() <= "9" && s.content != ContentCoaching && s.content != ContentPlanEdit && s.content != ContentPlanReview && s.content != ContentUserQuestion {
		idx := int(msg.String()[0] - '0')
		if idx <= len(s.agents) {
			s.focusedAgent = idx
			s.content = ContentAgentHistory
			s.contentVP.GotoTop()
			s.SyncViewports()
			return s, nil
		}
	}

	switch s.content {
	case ContentCoaching:
		return s.handleCoachingKey(msg)
	case ContentUserQuestion:
		return s.handleUserQuestionKey(msg)
	case ContentPlanReview:
		return s.handlePlanReviewKey(msg)
	case ContentPlanEdit:
		return s.handlePlanEditKey(msg)
	case ContentAgentHistory:
		return s.handleAgentHistoryKey(msg)
	case ContentCompletion:
		return s.handleCompletionKey(msg)
	case ContentStreaming:
		return s.handleStreamingKey(msg)
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
	if s.content == ContentCoaching {
		if s.answerCursor < len(s.answerFields) {
			var cmd tea.Cmd
			s.answerFields[s.answerCursor], cmd = s.answerFields[s.answerCursor].Update(msg)
			return s, cmd
		}
	}
	if s.content == ContentUserQuestion && s.hasQuestionTA {
		var cmd tea.Cmd
		s.questionTextarea, cmd = s.questionTextarea.Update(msg)
		return s, cmd
	}
	if s.content == ContentPlanEdit && s.hasPlanEditor {
		var cmd tea.Cmd
		s.planEditor, cmd = s.planEditor.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s PipelineScreen) handleCoachingKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.String() == "ctrl+s" {
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SkipGatewayIntent{}
		return s, nil
	}
	switch msg.Code {
	case tea.KeyEnter:
		if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
			// Shift+Enter / Alt+Enter inserts a newline in the active answer field
			if s.answerCursor < len(s.answerFields) {
				s.answerFields[s.answerCursor].InsertString("\n")
			}
			return s, nil
		}
		answers := make([]orchestrator.GatewayAnswer, len(s.answerFields))
		for i, f := range s.answerFields {
			answers[i] = orchestrator.GatewayAnswer{
				QuestionIndex: i,
				Answer:        strings.TrimSpace(f.Value()),
			}
		}
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitGatewayIntent{Answers: answers}
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			if s.answerCursor > 0 {
				s.answerFields[s.answerCursor].Blur()
				s.answerCursor--
				s.answerFields[s.answerCursor].Focus()
			}
			return s, nil
		}
		if s.answerCursor < len(s.answerFields)-1 {
			s.answerFields[s.answerCursor].Blur()
			s.answerCursor++
			s.answerFields[s.answerCursor].Focus()
		}
		return s, nil
	}
	// Pass to active answer field
	if s.answerCursor < len(s.answerFields) {
		var cmd tea.Cmd
		s.answerFields[s.answerCursor], cmd = s.answerFields[s.answerCursor].Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s PipelineScreen) handleUserQuestionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	opts := s.userQuestion.Options

	// Freeform mode (no options)
	if len(opts) == 0 && s.hasQuestionTA {
		switch msg.Code {
		case tea.KeyEscape:
			s.content = ContentStreaming
			s.SyncViewports()
			s.PendingIntent = SubmitQuestionAnswerIntent{Answer: harness.MCPAnswer{Skipped: true}}
			return s, nil
		case tea.KeyEnter:
			if !msg.Mod.Contains(tea.ModShift) && !msg.Mod.Contains(tea.ModAlt) {
				text := strings.TrimSpace(s.questionTextarea.Value())
				s.content = ContentStreaming
				s.SyncViewports()
				s.PendingIntent = SubmitQuestionAnswerIntent{Answer: harness.MCPAnswer{FreeformText: text}}
				return s, nil
			}
			s.questionTextarea.InsertString("\n")
			return s, nil
		}
		var cmd tea.Cmd
		s.questionTextarea, cmd = s.questionTextarea.Update(msg)
		return s, cmd
	}

	// Options mode
	switch msg.Code {
	case tea.KeyEscape:
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: harness.MCPAnswer{Skipped: true}}
		return s, nil
	case tea.KeyUp:
		if s.questionCursor > 0 {
			s.questionCursor--
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyDown:
		if s.questionCursor < len(opts)-1 {
			s.questionCursor++
		}
		s.SyncViewports()
		return s, nil
	case tea.KeyEnter:
		answer := s.buildQuestionAnswer()
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
		return s, nil
	}

	switch msg.String() {
	case "j":
		if s.questionCursor < len(opts)-1 {
			s.questionCursor++
		}
		s.SyncViewports()
		return s, nil
	case "k":
		if s.questionCursor > 0 {
			s.questionCursor--
		}
		s.SyncViewports()
		return s, nil
	case " ":
		if s.userQuestion.MultiSelect {
			s.questionSelected[s.questionCursor] = !s.questionSelected[s.questionCursor]
		} else {
			// Single-select: clear all, select current
			for k := range s.questionSelected {
				delete(s.questionSelected, k)
			}
			s.questionSelected[s.questionCursor] = true
		}
		s.SyncViewports()
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) buildQuestionAnswer() harness.MCPAnswer {
	var selected []int
	for i := range s.userQuestion.Options {
		if s.questionSelected[i] {
			selected = append(selected, i)
		}
	}
	// If nothing was explicitly selected in single-select mode, select cursor
	if len(selected) == 0 && !s.userQuestion.MultiSelect && len(s.userQuestion.Options) > 0 {
		selected = []int{s.questionCursor}
	}
	return harness.MCPAnswer{SelectedIndices: selected}
}

func (s PipelineScreen) handlePlanReviewKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.String() == "ctrl+e" {
		if s.planFilePath != "" {
			s.editorRunning = true
			s.PendingIntent = OpenExternalEditorIntent{FilePath: s.planFilePath}
			return s, nil
		}
		return s, nil
	}
	switch msg.Code {
	case tea.KeyEnter:
		if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
			// Shift+Enter / Alt+Enter inserts a newline in the comment textarea
			if s.hasPlanComment {
				s.planComment.InsertString("\n")
			}
			return s, nil
		}
		if s.hasPlanComment {
			comment := strings.TrimSpace(s.planComment.Value())
			if comment != "" {
				s.planComment.Reset()
				s.hasPlanComment = false
				s.awaitingPlanDecision = false
				s.content = ContentStreaming
				s.SyncViewports()
				s.PendingIntent = CommentPlanIntent{Comment: comment}
				return s, nil
			}
		}
		return s, nil
	}

	switch msg.String() {
	case "a", "A":
		s.awaitingPlanDecision = false
		s.hasPlanComment = false
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = ApprovePlanIntent{}
		return s, nil
	case "e", "E":
		s.awaitingPlanDecision = false
		contentWidth := max(1, int(float64(s.contentVP.Width()+s.sidebarVP.Width()+1)*splitRatio))
		contentHeight := max(4, s.contentVP.Height())
		ta := textarea.New()
		ta.SetWidth(max(1, contentWidth-2))
		ta.SetHeight(max(1, contentHeight-2))
		ta.CharLimit = 65536
		if s.hasPlan {
			ta.SetValue(s.finalPlan)
		}
		ta.Focus()
		s.planEditor = ta
		s.hasPlanEditor = true
		s.hasPlanComment = false
		s.content = ContentPlanEdit
		s.SyncViewports()
		return s, nil
	case "s", "S":
		s.awaitingPlanDecision = false
		s.PendingIntent = CancelPlanIntent{}
		return s, nil
	}

	// Pass remaining keys to comment textarea
	if s.hasPlanComment {
		var cmd tea.Cmd
		s.planComment, cmd = s.planComment.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s PipelineScreen) handlePlanEditKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.String() == "ctrl+s" {
		edited := s.planEditor.Value()
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = EditPlanIntent{ModifiedMarkdown: edited}
		return s, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		s.content = ContentPlanReview
		s.SyncViewports()
		return s, nil
	}
	if s.hasPlanEditor {
		var cmd tea.Cmd
		s.planEditor, cmd = s.planEditor.Update(msg)
		return s, cmd
	}
	return s, nil
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
	if s.confirmNew {
		switch msg.String() {
		case "y", "Y":
			s.confirmNew = false
			s.PendingIntent = ConfirmNewRunIntent{}
			return s, nil
		default:
			s.confirmNew = false
			return s, nil
		}
	}

	switch msg.String() {
	case "s", "S":
		s.PendingIntent = CancelPipelineIntent{}
		return s, nil
	case "n", "N":
		if s.active && s.content == ContentStreaming {
			s.confirmNew = true
			return s, nil
		}
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	}
	return s, nil
}

func (s PipelineScreen) handleCompletionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.String() == "ctrl+r" {
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	}
	switch msg.String() {
	case "n", "N":
		s.PendingIntent = NavigateToPromptIntent{PreFillGoal: s.goal}
		return s, nil
	case "q", "Q":
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

	// Header (2 lines)
	elapsed := time.Since(s.startTime).Truncate(time.Second)
	title := headerStyle.Render(" Orqestra")
	phase := phaseStyle.Render(fmt.Sprintf("▶ %s", s.phase))
	timeStr := elapsedStyle.Render(elapsed.String())
	var runInfo string
	if s.configName != "" {
		runInfo = dimStyle.Render(fmt.Sprintf(" [%s]", s.configName))
	}
	header := fmt.Sprintf("%s%s  %s  %s\n", title, runInfo, phase, timeStr) +
		dividerStyle.Render(strings.Repeat("─", w)) + "\n"

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.viewFooter()

	// Input zone height depends on content mode
	inputHeight := constPipelineInputHeight
	if s.content == ContentPlanReview {
		inputHeight = constPlanReviewInputHeight
	}

	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.viewInputZone() + "\n"

	// Content zone dimensions
	contentHeight := max(0, height-constHeaderHeight-inputHeight-constFooterHeight)
	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)

	// Render body — viewports already synced in Update()
	var body string
	if s.showDashboard {
		body = s.dashboardVP.View()
	} else {
		body = joinSplitView(s.contentVP.View(), s.sidebarVP.View(), contentWidth, sidebarWidth, contentHeight)
	}

	return header + body + "\n" + input + footer
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
		if s.confirmNew {
			status = warnStyle.Render(" Pipeline is active. Start new run? [Y] yes / [any] cancel")
		}
		return status
	case ContentCoaching:
		return keyStyle.Render(" Answer the questions above, then [Enter] to confirm")
	case ContentUserQuestion:
		if s.hasQuestionTA {
			return keyStyle.Render(" Type your answer, then [Enter] to submit | [Esc] skip")
		}
		return keyStyle.Render(" Select an option, then [Enter] to confirm | [Esc] skip")
	case ContentPlanReview:
		if s.hasPlanComment {
			return s.planComment.View()
		}
		return keyStyle.Render(" [A] accept | [E] edit | [Ctrl+E] editor | [Enter] comment | [S] cancel")
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard")
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
	case ContentCoaching:
		return s.viewCoaching(width)
	case ContentUserQuestion:
		return s.viewUserQuestion(width)
	case ContentPlanReview:
		return s.viewPlanReview(width)
	case ContentPlanEdit:
		return s.viewPlanEdit(width)
	case ContentAgentHistory:
		return s.viewAgentHistory(width)
	case ContentCompletion:
		return s.viewCompletion(width)
	}
	return ""
}

func (s PipelineScreen) viewStreaming(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n", goalStyle.Render(s.goal)))
	}

	var streamAgent string
	var streamLines []string
	var activities []orchestrator.Activity
	if s.streamBuf != nil {
		streamAgent, streamLines, activities = s.streamBuf.Snapshot()
	}

	b.WriteString(fmt.Sprintf(" Phase: %s", s.phase))
	if streamAgent != "" {
		b.WriteString(fmt.Sprintf("  (%s)", streamAgent))
	}
	b.WriteString("\n")

	if len(activities) > 0 {
		b.WriteString(renderActivityLog(activities, width))
	}

	if len(streamLines) > 0 {
		b.WriteString(dividerStyle.Render(strings.Repeat("─", width-2)))
		b.WriteString("\n")
		maxLineWidth := width - 2
		if maxLineWidth < 1 {
			maxLineWidth = 1
		}
		for _, line := range streamLines {
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

func (s PipelineScreen) viewCoaching(_ int) string {
	var b strings.Builder
	brief := s.gatewayResult.Brief
	b.WriteString(fmt.Sprintf(" Task: %s\n", goalStyle.Render(brief.Task)))
	if brief.EndState != "" {
		b.WriteString(fmt.Sprintf(" End State: %s\n", brief.EndState))
	}
	b.WriteString("\n Questions:\n")
	for i, q := range s.gatewayResult.Questions {
		marker := "  "
		if i == s.answerCursor {
			marker = "▶ "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, q.Text))
		if i < len(s.answerFields) {
			b.WriteString(fmt.Sprintf("   %s\n", s.answerFields[i].View()))
		}
	}
	return b.String()
}

func (s PipelineScreen) viewUserQuestion(_ int) string {
	var b strings.Builder
	q := s.userQuestion

	// Header: agent + question
	b.WriteString(fmt.Sprintf(" %s asks:\n", phaseStyle.Render(q.Question)))
	b.WriteString("\n")

	if len(q.Options) == 0 && s.hasQuestionTA {
		// Freeform mode
		b.WriteString(fmt.Sprintf("   %s\n", s.questionTextarea.View()))
		return b.String()
	}

	// Options mode
	for i, opt := range q.Options {
		cursor := "  "
		if i == s.questionCursor {
			cursor = phaseStyle.Render("▶ ")
		}

		var marker string
		if q.MultiSelect {
			if s.questionSelected[i] {
				marker = passStyle.Render("☑ ")
			} else {
				marker = dimStyle.Render("☐ ")
			}
		} else {
			if s.questionSelected[i] {
				marker = passStyle.Render("● ")
			} else {
				marker = dimStyle.Render("○ ")
			}
		}

		label := opt.Label
		if i == s.questionCursor {
			label = goalStyle.Render(opt.Label)
		}

		b.WriteString(fmt.Sprintf("%s%s%s", cursor, marker, label))
		if opt.Hint != "" {
			b.WriteString(fmt.Sprintf("  %s", dimStyle.Render(opt.Hint)))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (s PipelineScreen) viewPlanReview(width int) string {
	if !s.hasPlan {
		return " Waiting for plan...\n"
	}
	return renderMarkdown(s.finalPlan, width)
}

func (s PipelineScreen) viewCompletion(_ int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(fmt.Sprintf(" Goal: %s\n\n", goalStyle.Render(s.goal)))
	}
	if s.lastErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf(" Error: %v", s.lastErr)))
		b.WriteString("\n")
	}
	if s.hasValidation {
		b.WriteString(" Validation:\n")
		for _, line := range strings.Split(s.workerValidation, "\n") {
			b.WriteString(fmt.Sprintf("   %s\n", line))
		}
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
	return b.String()
}

func (s PipelineScreen) viewPlanEdit(_ int) string {
	var b strings.Builder
	b.WriteString(" Editing plan (Ctrl+S to save, Esc to discard):\n\n")
	if s.hasPlanEditor {
		b.WriteString(s.planEditor.View())
	}
	return b.String()
}

func (s PipelineScreen) viewAgentHistory(_ int) string {
	var b strings.Builder
	if s.focusedAgent > 0 && s.focusedAgent <= len(s.agents) {
		a := s.agents[s.focusedAgent-1]
		b.WriteString(fmt.Sprintf(" Agent: %s (%s)\n", goalStyle.Render(a.ID), a.State))
		b.WriteString(dividerStyle.Render(strings.Repeat("─", 40)))
		b.WriteString("\n")
		b.WriteString(" (output history not captured in this mode)\n")
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
			pct := float64(a.InputTokens+a.OutputTokens) / 200000.0 * 100
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
 [Enter]      Submit prompt / confirm
 [Ctrl+S]     Skip gateway / save edits
 [Tab]        Next field (coaching)
 [Shift+Tab]  Previous field (coaching)
 [PgUp/PgDn]  Scroll content
 [A]          Accept plan
 [E]          Edit plan
 [S]          Stop/cancel running agent
 [N]          New run
 [D]          Toggle full dashboard
 [1-9]        View agent output
 [Esc]        Back / dismiss
 [?]          Toggle this help
 [Q]          Quit (at completion)
 [Ctrl+C x2]  Force quit
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
	switch s.content {
	case ContentCoaching:
		return keyStyle.Render(" [Enter] confirm | [Shift+Enter] newline | [Ctrl+S] skip | [Tab] next   [?] help  [D] expand  [S] stop  [^C^C] quit")
	case ContentUserQuestion:
		if s.hasQuestionTA {
			return keyStyle.Render(" [Enter] submit | [Esc] skip                               [?] help  [^C^C] quit")
		}
		if s.userQuestion.MultiSelect {
			return keyStyle.Render(" [↑↓] navigate | [Space] toggle | [Enter] confirm | [Esc] skip   [?] help  [^C^C] quit")
		}
		return keyStyle.Render(" [↑↓] navigate | [Enter] select | [Esc] skip                [?] help  [^C^C] quit")
	case ContentPlanReview:
		return keyStyle.Render(" [A] accept | [E] edit | [Ctrl+E] editor | [Enter] comment | [Shift+Enter] newline | [S] cancel  [^C^C] quit")
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard                       [?] help  [^C^C] quit")
	case ContentAgentHistory:
		return keyStyle.Render(" [Esc] back to live                                        [?] help  [D] expand  [S] stop  [N] new  [^C^C] quit")
	case ContentCompletion:
		return keyStyle.Render(" [N] new run | [Ctrl+R] runs | [Q] quit                    [?] help")
	default:
		return keyStyle.Render(" [S] stop | [N] new run                                    [?] help  [D] expand  [1-9] agent  [^C^C] quit")
	}
}

// --- Shared helper functions ---

// renderActivityLog renders recent tool activities as a multi-line vertical log.
func renderActivityLog(activities []orchestrator.Activity, width int) string {
	const maxShow = 8
	start := 0
	if len(activities) > maxShow {
		start = len(activities) - maxShow
	}
	recent := activities[start:]

	var b strings.Builder
	for _, act := range recent {
		toolLabel := activityToolStyle.Render(fmt.Sprintf(" %-10s", act.Tool))
		detail := act.Detail
		if isFilePathTool(act.Tool) && detail != "" {
			detail = fileHyperlink(detail)
			b.WriteString(toolLabel + " " + activityPathStyle.Render(detail))
		} else {
			b.WriteString(toolLabel + " " + activityDetailStyle.Render(detail))
		}
		b.WriteString("\n")
	}
	return b.String()
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
func fileHyperlink(path string) string {
	if !strings.HasPrefix(path, "/") {
		return path
	}
	return fmt.Sprintf("\033]8;;file://%s\033\\%s\033]8;;\033\\", path, filepath.Base(path))
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
