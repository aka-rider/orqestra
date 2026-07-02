package tui

import (
	"errors"
	"time"

	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/frame"
)

// agentSummaryLine formats the end-of-agent transcript summary line, e.g.
// "Done: ✓ architect (qwen3.6)  ↑236k ↓456k  3m28s". Tokens and elapsed are the
// real values reported at agent completion.
func agentSummaryLine(prefix, icon string, a orchestrator.AgentSnapshot, elapsed time.Duration) string {
	model := a.Meta.ModelDisplay
	if model == "" {
		model = a.Meta.ModelRef
	}
	line := prefix + " " + icon + " " + agentDisplayName(a.AgentID)
	if model != "" {
		line += " (" + model + ")"
	}
	line += "  ↑" + formatTokens(a.Input) + " ↓" + formatTokens(a.Output)
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
			// Seal the previous turn (if any), emit the phase separator, then start
			// a fresh TurnGroup for the new agent.
			s.sealAndPromoteTurn()
			ruleLabel := agentDisplayName(string(a.AgentID))
			if a.Meta.ModelDisplay != "" {
				ruleLabel += ": " + a.Meta.ModelDisplay
			} else if a.Meta.ModelRef != "" {
				ruleLabel += ": " + a.Meta.ModelRef
			}
			s.timeline.Append(frame.NewPhase(ruleLabel))
			tg := frame.NewTurnGroup()
			s.currentTurn = tg
			s.timeline.SetTail(tg)
			s.lastAgentID = a.AgentID
			if s.streamBuf != nil {
				s.streamBuf.SetAgent(a.AgentID)
			}
			s.agents = append(s.agents, AgentRow{
				ID:        a.AgentID,
				State:     AgentStateRunning,
				StartedAt: a.StartTime,
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
				s.reconcilePendingTools()
				s.sealAndPromoteTurn()
				s.timeline.Append(frame.NewSummary(agentSummaryLine("Done:", "✓", a, elapsed)))
			case "failed":
				for i := range s.agents {
					if s.agents[i].ID == a.AgentID {
						s.agents[i].State = AgentStateFailed
					}
				}
				if a.Error != "" {
					s.lastErr = errors.New(a.Error)
				}
				s.reconcilePendingTools()
				s.sealAndPromoteTurn()
				s.timeline.Append(frame.NewSummary(agentSummaryLine("Failed:", "✗", a, a.EndTime.Sub(a.StartTime))))
			}
			s.knownAgents[a.AgentID] = curr
		}
	}

	// Gate: open on the always-focused chat when the plan markdown changes. The
	// plan goes to the timeline; the chat takes the approve/edit/revise keys. No
	// separate mode — awaitingPlanDecision is the gate's only state.
	if snap.HasGate && snap.Gate.FinalPlanMarkdown != "" && snap.Gate.FinalPlanMarkdown != s.seenGateMarkdown {
		s.seenGateMarkdown = snap.Gate.FinalPlanMarkdown
		if !s.awaitingPlanDecision {
			s.awaitingPlanDecision = true
			s.finalPlan = snap.Gate.FinalPlanMarkdown
			s.hasPlan = snap.Gate.Position.IsPlanGate()
			s.timeline.Append(frame.NewPlan(snap.Gate.FinalPlanMarkdown, s.md))
		} else {
			// Plan revised — update without reopening the gate.
			s.finalPlan = snap.Gate.FinalPlanMarkdown
		}
	}

	// UserQuestion: open it on the chat once per arrival. No mode change — the
	// chat surfaces the question while the run stays in its streaming state.
	if snap.HasQuestion && !s.chat.QuestionOpen() {
		s.chat.OpenQuestion(snap.UserQuestion, width)
	}

	// Terminal: pipeline finished. The active turn is already sealed by the
	// agent-done path; Stop() just halts the blink loop.
	if snap.Terminal.Done && s.active && !s.awaitingPlanDecision {
		s.content = ContentCompletion
		s.active = false
		s.timeline.Stop()
		if snap.Terminal.Err != nil {
			s.lastErr = snap.Terminal.Err
		}
		if snap.Terminal.Result.WorkerValidation != "" {
			s.workerValidation = snap.Terminal.Result.WorkerValidation
		}
		if len(snap.Terminal.Result.ConflictFiles) > 0 {
			s.conflictFiles = snap.Terminal.Result.ConflictFiles
		}
	}
}
