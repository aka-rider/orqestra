package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// errGateCancelled is returned when the user cancels at a gate.
var errGateCancelled = fmt.Errorf("gate cancelled by user")

// runGateLoop runs the interactive gate loop for a given phase position.
// It handles rich gates (plan review with edit/comment/approve/cancel)
// and pause gates (approve/cancel only).
//
// Parameters:
//   - ctx: cancellation context
//   - emit: event emitter for TUI
//   - decisions: channel for user decisions
//   - pos: gate position (determines phase dir and behavior)
//   - session: session directory
//   - planMarkdown: initial plan markdown
//   - planFilePath: path to the plan file on disk
//   - planWarnings: plan health warnings
//   - planSessionID: Claude session ID for continuation; "" for cold start
//   - stream: stream capture for agent output
//   - streamOut: output channel for streaming events
func (e *Engine) runGateLoop(
	ctx context.Context,
	emit func(Event),
	decisions <-chan Decision,
	pos HumanGatePosition,
	session agent.SessionDir,
	planMarkdown string,
	planFilePath string,
	planWarnings []string,
	planSessionID string,
	stream *streamCapture,
	streamOut chan<- harness.Event,
) (Decision, string, string, error) {
	phase := phaseDir(pos)
	dir := session.SubDir(phase)
	isRich := pos.IsPlanGate()

	// gateRunner returns the runner and agent name for this gate position.
	gateRunner := func() (harness.Runner, string) {
		switch pos {
		case GateAfterResearch:
			return e.Runners.Researcher, "researcher"
		case GateAfterDeliberation:
			return e.Runners.Architect, "architect"
		default:
			return nil, ""
		}
	}

	// For pause gates, just wait for approve/cancel.
	if !isRich {
		for {
			select {
			case decision := <-decisions:
				switch decision.Type {
				case DecisionApprove:
					writeArtifactJSONIn(session, phase, "gate_decision.json", map[string]string{
						"Type":      "approve",
						"Timestamp": time.Now().UTC().Format(time.RFC3339),
						"Phase":     string(pos),
					})
					return decision, planMarkdown, planSessionID, nil
				case DecisionCancel:
					writeArtifactJSONIn(session, phase, "gate_decision.json", map[string]string{
						"Type":      "cancel",
						"Timestamp": time.Now().UTC().Format(time.RFC3339),
						"Phase":     string(pos),
					})
					return decision, planMarkdown, planSessionID, errGateCancelled
				case DecisionComment:
					// Stray comment on pause gate — no-op, log it.
					appendDialog(dir, "Agent", "(chat only — pause gate does not accept revisions)")
					emit(Event{Type: EventChatResponse, ChatText: "(Pause gate: comments are logged but do not trigger agent re-execution)"})
				}
			case <-ctx.Done():
				return Decision{}, planMarkdown, planSessionID, ctx.Err()
			}
		}
	}

	// --- Rich gate (plan review) ---
	runner, agentName := gateRunner()
	if runner == nil {
		return Decision{}, planMarkdown, planSessionID, fmt.Errorf("gate %s: no runner configured", pos)
	}

	// Create the planner.
	planner := agent.NewPlanner(runner, e.Config.Architect.SystemPrompt)

	// Resolve agent metadata for meta files.
	archMeta := resolveAgentMeta(e.Config, e.Config.Architect.Model)

	for {
		// Emit gate request with Position field.
		emit(Event{
			Type:        EventGateRequest,
			Phase:       PhasePlanning,
			Gate:        GateRequest{Position: pos, FinalPlanMarkdown: planMarkdown, PlanFilePath: planFilePath, PlanWarnings: planWarnings},
			FinalPlan:   planMarkdown,
		})

		select {
		case decision := <-decisions:
			switch decision.Type {
			case DecisionCancel:
				writeArtifactJSONIn(session, phase, "gate_decision.json", map[string]string{
					"Type":      "cancel",
					"Timestamp": time.Now().UTC().Format(time.RFC3339),
					"Phase":     string(pos),
				})
				return decision, planMarkdown, planSessionID, errGateCancelled
			case DecisionApprove:
				writeArtifactJSONIn(session, phase, "gate_decision.json", map[string]string{
					"Type":      "approve",
					"Timestamp": time.Now().UTC().Format(time.RFC3339),
					"Phase":     string(pos),
				})
				return decision, planMarkdown, planSessionID, nil
			case DecisionEdit:
				// User edited the plan externally.
				edited := decision.EditedContent
				if edited == "" {
					edited = planMarkdown
				}
				planMarkdown = edited
				planWarnings = agent.CheckPlanHealth(edited)

				// If user provided a comment, re-engage the architect.
				if decision.Comment != "" && planSessionID != "" {
					planMarkdown, planSessionID = e.doRevisionTurn(ctx, emit, planner, agentName, archMeta,
						planMarkdown, planFilePath, planSessionID, stream, streamOut, dir, decision.Comment, phase, agent.ContinuePrompt)
				}

				// Auto-approve path: user confirmed edit, skip re-show.
				if decision.AutoApprove && decision.Comment == "" {
					return decision, planMarkdown, planSessionID, nil
				}
				// Otherwise re-show the gate.
				continue
			case DecisionComment:
				// User commented — re-engage the architect.
				var err error
				planMarkdown, planSessionID, err = e.doCommentTurn(ctx, emit, planner, agentName, archMeta,
					planMarkdown, planSessionID, stream, streamOut, dir, phase, decision.Comment)
				if err != nil {
					// Planner error — re-show gate with existing plan.
					emit(Event{Type: EventError, Err: fmt.Errorf("architect revision: %w", err)})
					continue
				}
				// Re-show gate with (possibly revised) plan.
				continue
			}
		case <-ctx.Done():
			return Decision{}, planMarkdown, planSessionID, ctx.Err()
		}
	}
}

// doRevisionTurn handles an edit+comment revision turn.
func (e *Engine) doRevisionTurn(
	ctx context.Context,
	emit func(Event),
	planner *agent.Planner,
	agentName string,
	archMeta AgentMeta,
	planMarkdown, planFilePath, planSessionID string,
	stream *streamCapture, streamOut chan<- harness.Event,
	dir string,
	phase string,
	comment string,
	continueFn func(string, string) string,
) (finalPlan string, finalSessionID string) {
	architectAttempt := 1
	revStart := time.Now()

	// Extract baseline before revision.
	baseline, baselineErr := planner.ExtractPlan(ctx)
	if baselineErr != nil {
		slog.Debug("could not snapshot plan file baseline before edit revision", "err", baselineErr)
	}

	// Continue with the planner.
	result, err := continuePlanner(ctx, planner, planSessionID, continueFn(planMarkdown, comment), stream, streamOut)
	if err != nil {
		e.writeGateAgentMeta(planSessionID, agentName, archMeta, architectAttempt, revStart, "failed", err)
		return planMarkdown, planSessionID
	}

	chatResponse := result.Chat
	revisedPlan := agent.DetectPlanRevision(result.Plan, baseline, baselineErr, planMarkdown)
	finalSessionID = result.SessionID

	e.writeGateAgentMeta(planSessionID, agentName, archMeta, architectAttempt, revStart, "done", nil)

	if revisedPlan != nil {
		finalPlan = revisedPlan.Markdown
		// Write the revised plan to the phase dir.
		n := highestPlanVersion(dir) + 1
		newPath := writeArtifactIn(agent.SessionDir{Path: sessionRoot(dir)}, phase,
			fmt.Sprintf("plan-v%d.md", n), finalPlan)
		planFilePath = newPath
	} else if chatResponse != "" {
		emit(Event{Type: EventChatResponse, ChatText: chatResponse})
		appendDialog(dir, "Agent", chatResponse+" (chat only)")
	}

	appendDialog(dir, "Agent", "Re: "+truncateMsg(comment, 50))
	return finalPlan, finalSessionID
}

// doCommentTurn handles a comment-only revision turn.
func (e *Engine) doCommentTurn(
	ctx context.Context,
	emit func(Event),
	planner *agent.Planner,
	agentName string,
	archMeta AgentMeta,
	planMarkdown, planSessionID string,
	stream *streamCapture, streamOut chan<- harness.Event,
	dir string,
	phase string,
	comment string,
) (finalPlan string, finalSessionID string, err error) {
	architectAttempt := 1
	revStart := time.Now()

	baseline, baselineErr := planner.ExtractPlan(ctx)
	if baselineErr != nil {
		slog.Debug("could not snapshot plan file baseline before comment revision", "err", baselineErr)
	}

	var result agent.PlanResult

	if planSessionID != "" {
		result, err = continuePlanner(ctx, planner, planSessionID, agent.ContinuePrompt(planMarkdown, comment), stream, streamOut)
	} else {
		// Cold start — no session to resume.
		coldPrompt := guardPrompt(agent.ArchitectRevisionPrompt(planMarkdown, comment), comment, "architect (cold-start)")
		coldResult, coldErr := runPlanner(ctx, planner, coldPrompt, stream, streamOut)
		if coldErr != nil {
			return "", "", coldErr
		}
		result = agent.PlanResult{
			Plan:      coldResult.Plan,
			Chat:      coldResult.Chat,
			Usage:     coldResult.Usage,
			SessionID: coldResult.SessionID,
		}
		planSessionID = coldResult.SessionID
		err = nil
	}

	if err != nil {
		e.writeGateAgentMeta(planSessionID, agentName, archMeta, architectAttempt, revStart, "failed", err)
		return "", "", fmt.Errorf("architect revision: %w", err)
	}

	chatResponse := result.Chat
	revisedPlan := agent.DetectPlanRevision(result.Plan, baseline, baselineErr, planMarkdown)
	finalSessionID = result.SessionID

	e.writeGateAgentMeta(planSessionID, agentName, archMeta, architectAttempt, revStart, "done", nil)

	if revisedPlan != nil {
		finalPlan = revisedPlan.Markdown
		n := highestPlanVersion(dir) + 1
		_ = writeArtifactIn(agent.SessionDir{Path: sessionRoot(dir)}, phase, fmt.Sprintf("plan-v%d.md", n), finalPlan)
	} else if chatResponse != "" {
		emit(Event{Type: EventChatResponse, ChatText: chatResponse})
		appendDialog(dir, "Agent", chatResponse+" (chat only)")
	}

	appendDialog(dir, "Agent", "Re: "+truncateMsg(comment, 50))
	return finalPlan, finalSessionID, nil
}

// writeGateAgentMeta writes a step meta JSON file for gate agent revisions.
func (e *Engine) writeGateAgentMeta(sessionID string, agentName string, archMeta AgentMeta, attempt int, start time.Time, status string, err error) {
	meta := agent.StepMeta{
		AgentID:         agentName,
		ModelRef:        archMeta.ModelRef,
		ModelDisplay:    archMeta.ModelDisplay,
		Provider:        archMeta.Provider,
		ContextWindow:   archMeta.ContextWindow,
		StartTime:       start,
		EndTime:         time.Now(),
		ClaudeSessionID: sessionID,
		Status:          status,
	}
	if err != nil {
		meta.Error = err.Error()
	}
	// Write to session root — gate meta files are flat.
	writeArtifactJSONIn(agent.SessionDir{}, "", fmt.Sprintf("architect_revision_%d_meta.json", attempt), meta)
}

// sessionRoot extracts the session root directory from a phase subdirectory path.
func sessionRoot(phaseSubdir string) string {
	// phaseSubdir is like "/path/to/session/deliberation"
	// Go up one level to get the session root.
	for len(phaseSubdir) > 0 {
		dir := phaseSubdir[:len(phaseSubdir)-1] // remove trailing slash
		base := dir
		for i := len(dir) - 1; i >= 0; i-- {
			if dir[i] == '/' {
				base = dir[:i]
				break
			}
		}
		if base != "" && base != dir {
			return base
		}
		return "/"
	}
	return "/"
}
