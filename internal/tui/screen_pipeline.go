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
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/harness"
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
	streamBuf *orchestrator.StreamBuffer

	// Plan edit state
	planEditor    textarea.Model
	hasPlanEditor bool

	// Plan review state
	planComment    textarea.Model
	hasPlanComment bool
	editorRunning  bool

	// Conversation state during plan review
	chatHistory     []ChatEntry
	planDiff        string         // unified diff from git micro-repo
	diffViewport    viewport.Model // paginated viewport for diff rendering
	reviewTokensIn  int64
	reviewTokensOut int64

	// Agent history navigation
	focusedAgent int

	// Run directory and plan file
	runDir       string
	planFilePath string

	awaitingPlanDecision bool

	// User question state (MCP AskUserQuestion bridge)
	userQuestion         harness.MCPToolCall
	questionCursor       int
	questionSelected     map[int]bool   // for multi-select
	questionCustom       map[int]string // per-option custom context text
	questionCustomActive int            // index of option with custom input open, -1 = none
	questionTextarea     textarea.Model // for freeform or inline custom text
	hasQuestionTA        bool           // true when freeform textarea is active

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
	s.hasPlanEditor = false
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
	s.userQuestion = harness.MCPToolCall{}
	s.questionCursor = 0
	s.questionSelected = nil
	s.questionCustom = nil
	s.questionCustomActive = -1
	s.hasQuestionTA = false
	s.mergeConflict = orchestrator.MergeConflictInfo{}
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

	// Keep diff viewport dimensions in sync
	s.diffViewport.SetWidth(contentWidth)
	s.diffViewport.SetHeight(max(1, s.contentVP.Height()-3))

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
			contentWidth := max(1, int(float64(width)*splitRatio))
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
		contentWidth := max(1, int(float64(width)*splitRatio))
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
		s.userQuestion = event.UserQuestion
		s.questionCursor = 0
		s.questionSelected = make(map[int]bool)
		s.questionCustom = make(map[int]string)
		s.questionCustomActive = -1
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
		if s.content != ContentPlanEdit && s.content != ContentPlanReview && s.content != ContentPlanDiff && s.content != ContentUserQuestion {
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
	if idx, ok := altKeys[msg.String()]; ok && s.content != ContentPlanEdit && s.content != ContentPlanReview && s.content != ContentUserQuestion {
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
		return s.handleUserQuestionKey(msg)
	case ContentPlanReview:
		return s.handlePlanReviewKey(msg)
	case ContentPlanEdit:
		return s.handlePlanEditKey(msg)
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

	// Custom text input is active — forward keys to textarea unless special
	if s.questionCustomActive >= 0 {
		switch msg.Code {
		case tea.KeyTab:
			// Confirm custom text and return to option navigation
			text := strings.TrimSpace(s.questionTextarea.Value())
			if text != "" {
				s.questionCustom[s.questionCustomActive] = text
			} else {
				delete(s.questionCustom, s.questionCustomActive)
			}
			s.questionTextarea.Blur()
			s.questionCustomActive = -1
			s.hasQuestionTA = false
			s.SyncViewports()
			return s, nil
		case tea.KeyEscape:
			// Cancel custom edit, discard changes
			s.questionTextarea.Blur()
			s.questionCustomActive = -1
			s.hasQuestionTA = false
			s.SyncViewports()
			return s, nil
		case tea.KeyEnter:
			// Confirm custom text (same as Tab-collapse) — do NOT submit the whole answer
			if !msg.Mod.Contains(tea.ModShift) && !msg.Mod.Contains(tea.ModAlt) {
				text := strings.TrimSpace(s.questionTextarea.Value())
				if text != "" {
					s.questionCustom[s.questionCustomActive] = text
				} else {
					delete(s.questionCustom, s.questionCustomActive)
				}
				s.questionTextarea.Blur()
				s.questionCustomActive = -1
				s.hasQuestionTA = false
				s.SyncViewports()
				return s, nil
			}
		}
		var cmd tea.Cmd
		s.questionTextarea, cmd = s.questionTextarea.Update(msg)
		return s, cmd
	}

	// Options mode (no custom input active)
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
	case tea.KeyTab:
		// Expand inline custom text input for highlighted option
		contentWidth := max(1, int(float64(s.contentVP.Width()+s.sidebarVP.Width()+1)*splitRatio))
		ta := textarea.New()
		ta.Placeholder = "Add context..."
		ta.SetWidth(max(1, contentWidth-6))
		ta.SetHeight(1)
		ta.CharLimit = 512
		// Restore any previously entered custom text
		if prev, ok := s.questionCustom[s.questionCursor]; ok {
			ta.SetValue(prev)
		}
		ta.Focus()
		s.questionTextarea = ta
		s.questionCustomActive = s.questionCursor
		s.hasQuestionTA = true
		s.SyncViewports()
		return s, nil
	case tea.KeyEnter:
		// Single-select shortcut: if nothing selected, select cursor AND confirm
		answer := s.buildQuestionAnswer()
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
		return s, nil
	}

	switch msg.String() {
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
	// Populate custom texts from per-option context input
	var customTexts map[int]string
	for idx, text := range s.questionCustom {
		if strings.TrimSpace(text) != "" {
			if customTexts == nil {
				customTexts = make(map[int]string)
			}
			customTexts[idx] = strings.TrimSpace(text)
		}
	}
	return harness.MCPAnswer{SelectedIndices: selected, CustomTexts: customTexts}
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
	case "ctrl+shift+e":
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
	case "ctrl+e":
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
	case "ctrl+d":
		if s.planDiff != "" {
			s.content = ContentPlanDiff
			s.hasPlanComment = false
			s.contentVP.GotoTop()
			s.SyncViewports()
		}
		return s, nil
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
	case ContentPlanEdit:
		// Cancel edit and return to review — don't cancel the whole pipeline
		s.content = ContentPlanReview
		s.SyncViewports()
	case ContentUserQuestion:
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: harness.MCPAnswer{Skipped: true}}
	default:
		s.PendingIntent = CancelPipelineIntent{}
	}
	return s
}

func (s PipelineScreen) handlePlanDiffKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	if msg.Code == tea.KeyEscape || msg.String() == "ctrl+d" {
		s.content = ContentPlanReview
		s.contentVP.GotoTop()
		contentWidth := max(1, int(float64(s.contentVP.Width()+s.sidebarVP.Width()+1)*splitRatio))
		s.planComment = textarea.New()
		s.planComment.Placeholder = "Ask a question or request changes..."
		s.planComment.SetWidth(max(1, contentWidth-4))
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
	if s.content == ContentPlanReview && s.hasPlanComment {
		inputHeight = constPlanReviewInputHeight
	}

	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.viewInputZone() + "\n"

	// Content zone dimensions. The "\n" separator between body and input
	// consumes one terminal line, so subtract it from available content height.
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
	// Clamp body to contentHeight. lipgloss.Place does not truncate when content
	// exceeds the requested height, so if viewport heights are stale after a
	// content-mode transition the body can overflow and push the footer off-screen.
	if contentHeight > 0 {
		body = lipgloss.NewStyle().MaxHeight(contentHeight).Render(body)
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
		return status
	case ContentUserQuestion:
		if s.questionCustomActive >= 0 {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard")
		}
		if s.hasQuestionTA {
			return keyStyle.Render(" Type your answer, then [Enter] to submit | [Esc] skip")
		}
		if s.userQuestion.MultiSelect {
			return keyStyle.Render(" [Space] toggle | [Tab] add context | [Enter] confirm | [Esc] skip")
		}
		return keyStyle.Render(" [Space] select | [Tab] add context | [Enter] confirm | [Esc] skip")
	case ContentPlanReview:
		if s.hasPlanComment {
			return s.planComment.View() + "\n" +
				keyStyle.Render(" [Enter] send | [Shift+Enter] newline | [Esc] cancel")
		}
		return keyStyle.Render(" [^A] accept | [^E] edit | [^⇧E] editor | [Enter] comment | [^D] diff")
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard")
	case ContentMergeConflict:
		return keyStyle.Render(" [^A] abort merge and keep worktree branch | [Esc] back to stream")
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
		return s.viewUserQuestion(width)
	case ContentPlanReview:
		return s.viewPlanReview(width)
	case ContentPlanEdit:
		return s.viewPlanEdit(width)
	case ContentPlanDiff:
		return s.viewPlanDiff(width)
	case ContentAgentHistory:
		return s.viewAgentHistory(width)
	case ContentCompletion:
		return s.viewCompletion(width)
	case ContentMergeConflict:
		return s.viewMergeConflict(width)
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
		b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
		b.WriteString("\n")
		maxLineWidth := width - constContentInset
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

func (s PipelineScreen) viewUserQuestion(width int) string {
	var b strings.Builder
	q := s.userQuestion

	// Question header — wrapped to content width
	b.WriteString(renderPrefixedText(phaseStyle, " ", q.Question, width))
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
			b.WriteString(fmt.Sprintf("  %s", questionHintStyle.Render(opt.Hint)))
		}
		// Show custom text indicator if text was added for this option
		if text, ok := s.questionCustom[i]; ok && text != "" && s.questionCustomActive != i {
			b.WriteString(fmt.Sprintf("  %s", questionHintStyle.Render("✎ "+text)))
		}
		b.WriteString("\n")

		// Render inline custom text editor when expanded for this option
		if s.questionCustomActive == i {
			b.WriteString(fmt.Sprintf("     %s %s\n", questionGutterStyle.Render("┊"), s.questionTextarea.View()))
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

func (s PipelineScreen) viewAgentHistory(width int) string {
	var b strings.Builder
	if s.focusedAgent > 0 && s.focusedAgent <= len(s.agents) {
		a := s.agents[s.focusedAgent-1]
		b.WriteString(fmt.Sprintf(" Agent: %s (%s)\n", goalStyle.Render(a.ID), a.State))
		b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
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
 [Ctrl+S]      Save edits
 [PgUp/PgDn]   Scroll content
 [Ctrl+A]      Accept plan / abort merge
 [Ctrl+E]      Edit plan (in-TUI)
 [Ctrl+⇧E]     Open external editor
 [Ctrl+D]      Toggle dashboard / diff
 [Ctrl+N]      New run
 [Ctrl+R]      Historical runs
 [Ctrl+Q]      Quit (at completion)
 [Ctrl+H]      Toggle this help
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
		if s.hasQuestionTA {
			return keyStyle.Render(" [Enter] submit | [Esc] skip                           [^H] help  ") + ctrlCHint
		}
		if s.userQuestion.MultiSelect {
			return keyStyle.Render(" [↑↓] navigate | [Space] toggle | [Enter] confirm | [Esc] skip   [^H] help  ") + ctrlCHint
		}
		return keyStyle.Render(" [↑↓] navigate | [Enter] select | [Esc] skip            [^H] help  ") + ctrlCHint
	case ContentPlanReview:
		footer := " [^A] accept | [^E] edit | [^⇧E] editor | [Enter] comment | [Shift+Enter] newline"
		if s.planDiff != "" {
			footer += " | [^D] diff"
		}
		footer += "  " + ctrlCHint
		if len(s.chatHistory) > 0 && (s.reviewTokensIn+s.reviewTokensOut > 0) {
			footer += dimStyle.Render(fmt.Sprintf("  Review: %s", formatTokens(s.reviewTokensIn+s.reviewTokensOut)))
		}
		return keyStyle.Render(footer)
	case ContentPlanDiff:
		return keyStyle.Render(" [Esc] return to plan | [^D] return to plan              [^H] help  ") + ctrlCHint
	case ContentPlanEdit:
		return keyStyle.Render(" [Ctrl+S] save edits | [Esc] discard                     [^H] help  ") + ctrlCHint
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
