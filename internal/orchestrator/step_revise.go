package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// ReviseStep handles a gate-loop revision turn: the user submitted a comment
// or edit, and the architect is asked to revise the plan accordingly.
type ReviseStep struct {
	ArchSpec harness.ProcessSpec
	ArchMeta AgentMeta
}

func (s *ReviseStep) ID() AgentID { return "architect" }

func (s *ReviseStep) Run(ctx context.Context, in ReviseInput, sc StepContext) (PlanOutput, error) {
	sc.Obs.AgentStarted(s.ID(), s.ArchMeta)

	// Build the continuation prompt from the decision type.
	var prompt string
	switch in.Decision.Type {
	case DecisionComment:
		prompt = agent.ContinuePrompt(in.Plan.Markdown, in.Decision.Comment)
	case DecisionEdit:
		edited := in.Decision.EditedContent
		if edited == "" {
			edited = in.Plan.Markdown
		}
		if in.Decision.Comment != "" {
			prompt = agent.ContinuePrompt(edited, in.Decision.Comment)
		} else {
			// Pure edit, no comment — auto-approve path returns the edited plan unchanged.
			return PlanOutput{
				Markdown:  edited,
				Warnings:  agent.CheckPlanHealth(edited),
				SessionID: in.Plan.SessionID,
			}, nil
		}
	default:
		return in.Plan, nil
	}

	spec := s.ArchSpec
	spec.Prompt = prompt
	spec.Resume = harness.ResumeSession(in.Plan.SessionID)

	sink := SinkFromObserver(s.ID(), sc.Obs)
	res, err := sc.Exec.Run(ctx, spec, nil, sink)
	if err != nil {
		sc.Obs.AgentFailed(s.ID(), err)
		return PlanOutput{}, fmt.Errorf("revise: %w", err)
	}

	revised, _, readErr := preferReport(sc, "architect", res, false)
	if readErr != nil {
		// Chat-only continuation — the architect responded without revising the plan.
		sc.Log.Debug("revise: plan unchanged (chat continuation)", "err", readErr)
		if chat := strings.TrimSpace(res.Output); chat != "" {
			sc.Log.Info("architect chat response (no plan revision)", "text_len", len(chat))
		}
		revised = in.Plan.Markdown
	}

	sc.Obs.AgentDone(s.ID(), res.Usage)

	// Write revision version artifact.
	sc.Artifacts.WriteBestEffort("plan_revision.md", []byte(revised))

	return PlanOutput{
		Markdown:  revised,
		Warnings:  agent.CheckPlanHealth(revised),
		SessionID: in.Plan.SessionID,
	}, nil
}
