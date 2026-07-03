package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// ReviseStep handles a gate-loop revision turn: the user submitted a comment
// or edit, and the architect is asked to revise the plan accordingly.
type ReviseStep struct {
	ArchSpec harness.ProcessSpec
	ArchMeta AgentMeta

	round int // meta filename counter — this step instance is reused across gate-loop iterations
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
				Markdown:     edited,
				Warnings:     agent.CheckPlanHealth(edited),
				SessionID:    in.Plan.SessionID,
				PlanFilePath: in.Plan.PlanFilePath, // plan file untouched by a pure edit
			}, nil
		}
	default:
		return in.Plan, nil
	}

	spec := s.ArchSpec
	spec.Prompt = prompt
	spec.Resume = harness.ResumeSession(in.Plan.SessionID)

	// J35: snapshot the plan file's PRE-invocation state using the prior
	// known session+path (in.Plan carries what the last Deliberate/Revise
	// call learned) so Harvest can tell whether THIS turn actually rewrote
	// it, instead of unconditionally trusting whatever is on disk.
	harvester := NewReportHarvester(sc, RoleReporter)
	if spec.PlanMode {
		harvester.SnapshotPlanFile(in.Plan.SessionID, in.Plan.PlanFilePath, sc.RepoPath)
	}

	revStart := time.Now()
	sink := SinkFromObserver(s.ID(), sc.Obs)
	res, err := sc.Exec.Run(ctx, spec, nil, sink)
	if err != nil {
		sc.Obs.AgentFailed(s.ID(), err)
		return PlanOutput{}, fmt.Errorf("revise: %w", err)
	}

	revised, prov, harvestErr := harvester.Harvest(ctx, spec, res, nil)
	fallback := false
	if harvestErr != nil {
		// Chat-only continuation — the architect responded without revising the plan.
		sc.Log.Debug("revise: plan unchanged (chat continuation)", "err", harvestErr)
		if chat := strings.TrimSpace(res.Output); chat != "" {
			sc.Log.Info("architect chat response (no plan revision)", "text_len", len(chat))
		}
		revised = in.Plan.Markdown
		fallback = true
	} else {
		sc.Obs.ReportHarvested(s.ID(), prov)
	}

	sc.Obs.AgentDone(s.ID(), res.Usage)

	s.round++
	status := "done"
	var metaErr error
	if fallback {
		status = "fallback"
		metaErr = harvestErr
	}
	writeMeta(sc, fmt.Sprintf("architect_revise_meta_round%d.json", s.round), string(s.ID()), s.ArchMeta, in.Plan.SessionID, revStart, status, metaErr, res.Usage, prov)

	// Write revision version artifact.
	sc.Artifacts.WriteBestEffort("plan_revision.md", []byte(revised))

	nextPlanFilePath := res.PlanFilePath
	if nextPlanFilePath == "" {
		nextPlanFilePath = in.Plan.PlanFilePath
	}

	// J13: advance the session ID (like step_deliberate.go's runRound) only
	// when the plan actually changed — a successful revision that produced a
	// fresh report but happened to echo identical content, or the chat-only
	// fallback path, both keep resuming the PRIOR session; only a genuine
	// content change means the next gate-loop turn must resume THIS
	// invocation's session (architect memory) instead of a stale one.
	sessionID := in.Plan.SessionID
	if revised != "" && revised != in.Plan.Markdown {
		sessionID = res.SessionID
	}

	return PlanOutput{
		Markdown:     revised,
		Warnings:     agent.CheckPlanHealth(revised),
		SessionID:    sessionID,
		PlanFilePath: nextPlanFilePath,
	}, nil
}
