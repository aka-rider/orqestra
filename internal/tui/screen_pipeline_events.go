package tui

import (
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/frame"
)

// agentSummaryLine formats the end-of-agent transcript summary line, e.g.
// "Done: ✓ architect (qwen3.6)  ↑236k ↓456k  3m28s". Tokens and elapsed are
// the real, accumulated-across-passes values on the AgentRow (J40).
func agentSummaryLine(prefix, icon, agentID string, meta orchestrator.AgentMeta, input, output int64, elapsed time.Duration) string {
	model := meta.ModelDisplay
	if model == "" {
		model = meta.ModelRef
	}
	line := prefix + " " + icon + " " + agentDisplayName(agentID)
	if model != "" {
		line += " (" + model + ")"
	}
	line += "  ↑" + formatTokens(input) + " ↓" + formatTokens(output)
	if elapsed > 0 {
		line += "  " + elapsed.Round(time.Second).String()
	}
	return line
}

// sealAndPromoteTurn seals the active TurnGroup (auto-expands tools, marks
// inactive) and promotes it to a static TurnSnapshot in the timeline. The
// currentTurn pointer is cleared so the next agent starts fresh.
func (s *PipelineScreen) sealAndPromoteTurn() {
	if s.currentTurn != nil {
		s.currentTurn.Seal()
	}
	s.timeline.PromoteTail()
	s.currentTurn = nil
	s.toolFrameExpanded = false
}

// ensureAgentRow returns the AgentRow for id, creating and appending it (in
// s.agents) on first sight. Agent identity is keyed by ID string, not by
// pass, so repeated AgentStarted events for the same ID (e.g. "architect"
// across deliberation + revise rounds) accumulate onto ONE row rather than
// each starting a fresh one (J40).
func (s *PipelineScreen) ensureAgentRow(id string) *AgentRow {
	if idx, ok := s.agentRowIndex[id]; ok {
		return &s.agents[idx]
	}
	s.agents = append(s.agents, AgentRow{ID: id})
	idx := len(s.agents) - 1
	s.agentRowIndex[id] = idx
	return &s.agents[idx]
}

// ApplyEvent updates the pipeline screen from one orchestrator.RunEvent —
// the WP10 replacement for the pre-WP10 snapshot-store diffing method (RC2).
// Every event here is edge-triggered (delivered exactly once per real
// transition), so the hand-maintained dedup state the old diffing method
// needed (a per-agent last-seen-status map, a last-seen-plan-markdown
// string) is gone by construction.
func (s *PipelineScreen) ApplyEvent(ev orchestrator.RunEvent, width int) {
	switch e := ev.(type) {
	case orchestrator.EventPhaseStarted:
		// No direct rendering today — agent turn boundaries (AgentStarted)
		// already drive the phase-rule separator in the timeline.
	case orchestrator.EventAgentStarted:
		s.onAgentStarted(e.AgentID, e.Meta)
	case orchestrator.EventAgentDone:
		s.onAgentDone(e.AgentID, e.Usage)
	case orchestrator.EventAgentFailed:
		s.onAgentFailed(e.AgentID, e.Err)
	case orchestrator.EventDelta:
		s.timeline.AppendDelta(e.Text)
	case orchestrator.EventToolCall:
		s.onToolCall(e.AgentID, e.Tool, e.Detail)
	case orchestrator.EventToolResult:
		s.resolvePendingTool(e.IsError)
	case orchestrator.EventStats:
		// Not rendered directly — EventAgentDone's final usage is what the
		// completion summary and status bar show (matches pre-WP10 behavior:
		// EntryStats was never consumed by DrainStreamUpdates either).
	case orchestrator.EventGateOpened:
		s.onGateOpened(e.GateID, e.Request)
	case orchestrator.EventGateClosed:
		s.onGateClosed(e.GateID)
	case orchestrator.EventQuestionAsked:
		s.onQuestionAsked(e.ToolCall, width)
	case orchestrator.EventRunFinished:
		s.onRunFinished(e.Result, e.Err)
	}
}

// onAgentStarted seals any prior turn, opens a fresh TurnGroup for the new
// turn, and starts (or resumes) this agent identity's AgentRow.
func (s *PipelineScreen) onAgentStarted(id orchestrator.AgentID, meta orchestrator.AgentMeta) {
	s.sealAndPromoteTurn()

	ruleLabel := agentDisplayName(string(id))
	if meta.ModelDisplay != "" {
		ruleLabel += ": " + meta.ModelDisplay
	} else if meta.ModelRef != "" {
		ruleLabel += ": " + meta.ModelRef
	}
	s.timeline.Append(frame.NewPhase(ruleLabel))

	tg := frame.NewTurnGroup()
	s.currentTurn = tg
	s.timeline.SetTail(tg)
	s.lastAgentID = string(id)

	row := s.ensureAgentRow(string(id))
	row.State = AgentStateRunning
	row.Meta = meta
	row.currentPassStart = time.Now()
	if row.StartedAt.IsZero() {
		row.StartedAt = row.currentPassStart
	}
}

// onAgentDone accumulates this pass's usage/elapsed onto the agent's row
// (J40: sum across passes, never overwrite), seals the turn, and appends the
// end-of-agent summary frame.
func (s *PipelineScreen) onAgentDone(id orchestrator.AgentID, usage harness.TokenUsage) {
	row := s.ensureAgentRow(string(id))
	row.State = AgentStateDone
	row.InputTokens += usage.Input
	row.OutputTokens += usage.Output
	if !row.currentPassStart.IsZero() {
		row.Elapsed += time.Since(row.currentPassStart)
	}

	s.reconcilePendingTools()
	s.sealAndPromoteTurn()
	s.timeline.Append(frame.NewSummary(agentSummaryLine("Done:", "✓", string(id), row.Meta, row.InputTokens, row.OutputTokens, row.Elapsed)))
}

// onAgentFailed marks the row failed, records the (typed) error, seals the
// turn, and appends the end-of-agent summary frame.
func (s *PipelineScreen) onAgentFailed(id orchestrator.AgentID, err error) {
	row := s.ensureAgentRow(string(id))
	row.State = AgentStateFailed
	if !row.currentPassStart.IsZero() {
		row.Elapsed += time.Since(row.currentPassStart)
	}
	if err != nil {
		s.lastErr = err
	}

	s.reconcilePendingTools()
	s.sealAndPromoteTurn()
	s.timeline.Append(frame.NewSummary(agentSummaryLine("Failed:", "✗", string(id), row.Meta, row.InputTokens, row.OutputTokens, row.Elapsed)))
}

// onToolCall records a pending tool row on the active turn and accumulates
// the activity onto the agent's row (for the completion screen's file
// activity log — WP10 Tier-B: replaces the deleted ring buffer's activity
// accumulator).
func (s *PipelineScreen) onToolCall(id orchestrator.AgentID, tool, detail string) {
	if detail == "" {
		return
	}
	row := s.ensureAgentRow(string(id))
	row.Activities = append(row.Activities, toolActivity{Tool: tool, Detail: detail})

	text := stripAnsi(detail)
	if s.currentTurn != nil {
		localIdx := s.currentTurn.AddTool(tool, text)
		s.pendingTools = append(s.pendingTools, pendingTool{localIdx: localIdx})
	}
}

// onGateOpened opens the plan gate. Unlike the pre-WP10 snapshot-diffing
// path (which had to dedup a still-open gate observed twice via a
// last-seen-plan-markdown string), EventGateOpened is edge-triggered — every
// delivery is a genuinely NEW gate, so this always (re-)opens.
func (s *PipelineScreen) onGateOpened(gid orchestrator.GateID, req orchestrator.GateRequest) {
	s.gateID = gid
	s.awaitingPlanDecision = true
	s.finalPlan = req.FinalPlanMarkdown
	s.hasPlan = req.Position.IsPlanGate()
	s.timeline.Append(frame.NewPlan(req.FinalPlanMarkdown, s.md))
}

// onGateClosed defensively closes the gate if it matches the currently
// tracked GateID — covers the case where the pipeline closes a gate for a
// reason other than a local user decision (e.g. ctx cancellation while a
// gate is open), so the TUI never gets stuck awaiting a decision no one will
// ever send.
func (s *PipelineScreen) onGateClosed(gid orchestrator.GateID) {
	if gid == s.gateID {
		s.closeGate()
	}
}

// onQuestionAsked surfaces a model question on the chat. EventQuestionAsked
// is edge-triggered (one event per real question), so the J25 re-open class
// dies by construction; QuestionOpen() is still checked defensively.
func (s *PipelineScreen) onQuestionAsked(tc mcp.ToolCall, width int) {
	if !s.chat.QuestionOpen() {
		s.chat.OpenQuestion(tc, width)
	}
}

// onRunFinished renders the completion screen from the terminal Result.
func (s *PipelineScreen) onRunFinished(res orchestrator.Result, err error) {
	s.content = ContentCompletion
	s.active = false
	s.awaitingPlanDecision = false
	s.timeline.Stop()
	if err != nil {
		s.lastErr = err
	}
	if res.WorkerValidation != "" {
		s.workerValidation = res.WorkerValidation
	}
	if res.ValidationVerdict != "" {
		s.validationVerdict = string(res.ValidationVerdict)
	}
	if len(res.ConflictFiles) > 0 {
		s.conflictFiles = res.ConflictFiles
	}
}
