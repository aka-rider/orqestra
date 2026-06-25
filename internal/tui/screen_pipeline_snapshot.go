package tui

import (
	"errors"
	"time"

	"github.com/xiii/orqestra/internal/orchestrator"
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
			// Flush live partial and emit phase separator rule on agent transition.
			s.timeline.FlushLive()
			ruleLabel := agentDisplayName(string(a.AgentID))
			if a.Meta.ModelDisplay != "" {
				ruleLabel += ": " + a.Meta.ModelDisplay
			} else if a.Meta.ModelRef != "" {
				ruleLabel += ": " + a.Meta.ModelRef
			}
			s.timeline.AppendPhase(ruleLabel)
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
				if a.AgentID == "architect" && len(s.chatHistory) > 0 {
					s.reviewTokensIn += a.Input
					s.reviewTokensOut += a.Output
				}
				s.timeline.ReconcilePendingTools()
				s.timeline.AppendAgentSummary(agentSummaryLine("Done:", "✓", a, elapsed))
			case "failed":
				for i := range s.agents {
					if s.agents[i].ID == a.AgentID {
						s.agents[i].State = AgentStateFailed
					}
				}
				if a.Error != "" {
					s.lastErr = errors.New(a.Error)
				}
				s.timeline.AppendAgentSummary(agentSummaryLine("Failed:", "✗", a, a.EndTime.Sub(a.StartTime)))
			}
			s.knownAgents[a.AgentID] = curr
		}
	}

	// Gate: open when markdown changes.
	if snap.HasGate && snap.Gate.FinalPlanMarkdown != "" && snap.Gate.FinalPlanMarkdown != s.seenGateMarkdown {
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
			s.activeChat = newHumanChatMode(snap.Gate, s.keys)
			s.timeline.AppendPlan(snap.Gate.FinalPlanMarkdown)
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
	}

	// Terminal: pipeline finished.
	if snap.Terminal.Done && s.active && !s.awaitingPlanDecision {
		s.SetToolFrameExpanded(true) // auto-expand tool frame on turn end
		s.content = ContentCompletion
		s.active = false
		s.timeline.Stop() // halt the live blink loop (bug: ⏺ blinked forever)
		if snap.Terminal.Err != nil {
			s.lastErr = snap.Terminal.Err
		}
		if snap.Terminal.Result.WorkerValidation != "" {
			s.workerValidation = snap.Terminal.Result.WorkerValidation
			s.hasValidation = true
		}
	}
}
