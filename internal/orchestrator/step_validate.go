package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// ValidateStep runs worker self-validation via session continuation.
// Advisory: errors are logged but do not propagate (validation is best-effort).
type ValidateStep struct {
	Spec            harness.ProcessSpec
	Meta            AgentMeta
	ValidationRetries int
}

func (s *ValidateStep) ID() AgentID { return "validator" }

func (s *ValidateStep) Run(ctx context.Context, in ValidateInput, sc StepContext) (ValidateOutput, error) {
	sc.Obs.AgentStarted(s.ID(), s.Meta)

	retries := s.ValidationRetries
	if retries < 1 {
		retries = 1
	}
	validationPrompt := agent.WorkerValidationPrompt(retries)

	start := time.Now()
	spec := s.Spec
	spec.Prompt = validationPrompt

	if in.WorkerSessionID != "" {
		spec.Resume = harness.ResumeSession(in.WorkerSessionID)
	}

	sink := SinkFromObserver(s.ID(), sc.Obs)
	res, err := sc.Exec.Run(ctx, spec, nil, sink)
	if err != nil {
		sc.Log.Warn("worker self-validation failed (advisory, continuing)", "err", err)
		sc.Obs.AgentFailed(s.ID(), err)
		s.writeMeta(sc, in.WorkerSessionID, start, "failed", err, harness.TokenUsage{})
		// Advisory: return empty output without propagating the error.
		return ValidateOutput{}, nil
	}

	output := res.Output
	parsed := agent.ParseValidationOutput(output)

	sc.Artifacts.WriteBestEffort("worker_validation.txt", []byte(output))
	s.writeMeta(sc, res.SessionID, start, "done", nil, res.Usage)
	sc.Obs.AgentDone(s.ID(), res.Usage)

	return ValidateOutput{Output: output, Parsed: parsed}, nil
}

func (s *ValidateStep) writeMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	if err != nil {
		// best-effort: don't let meta write block
		meta := StepMeta{
			AgentID:         string(s.ID()),
			ModelRef:        s.Meta.ModelRef,
			ClaudeSessionID: sid,
			StartTime:       start,
			EndTime:         time.Now(),
			Status:          status,
			Error:           fmt.Sprintf("%v", err),
		}
		data, jsonErr := json.MarshalIndent(meta, "", "  ")
		if jsonErr != nil {
			return
		}
		sc.Artifacts.WriteBestEffort("validator_meta.json", data)
		return
	}
	meta := StepMeta{
		AgentID:         string(s.ID()),
		ModelRef:        s.Meta.ModelRef,
		ModelDisplay:    s.Meta.ModelDisplay,
		Provider:        s.Meta.Provider,
		ContextWindow:   s.Meta.ContextWindow,
		ClaudeSessionID: sid,
		StartTime:       start,
		EndTime:         time.Now(),
		Status:          status,
		InputTokens:     usage.Input,
		OutputTokens:    usage.Output,
	}
	data, jsonErr := json.MarshalIndent(meta, "", "  ")
	if jsonErr != nil {
		return
	}
	sc.Artifacts.WriteBestEffort("validator_meta.json", data)
}
