package orchestrator

import "fmt"

// PipelineSetup configures which pipeline phases run and which gates fire.
// Deliberation always runs (it produces the plan); Execution and Validation are
// optional. There is no Research toggle — the architect researches on demand via the
// orqestra-researcher subagent.
type PipelineSetup struct {
	Execution          bool
	Validation         bool
	DeliberationRounds int
	HumanGates         HumanGateSet

	// BlockMergeOnValidationFail mirrors config.PipelineConfig.BlockMergeOnValidationFail
	// (set by the caller from Config, not a TUI-facing knob — J33/WP8). When true
	// and worker self-validation's parsed verdict is agent.VerdictFail, RunPipeline
	// skips Integrate and returns StatusFailed with an explicit reason instead of
	// merging silently. Default false preserves today's behavior: validation stays
	// advisory and Integrate always runs.
	BlockMergeOnValidationFail bool
}

// DefaultPipelineSetup returns the default pipeline configuration.
func DefaultPipelineSetup() PipelineSetup {
	return PipelineSetup{
		Execution:          true,
		Validation:         true,
		DeliberationRounds: 1,
		HumanGates:         HumanGateSet{GateAfterDeliberation},
	}
}

// Validate checks invariant constraints on PipelineSetup. Deliberation always runs and
// a plan is a valid terminal output, so a plan-only run (Execution=Validation=false) is
// legal — the only constraint is the deliberation-rounds range.
func (p PipelineSetup) Validate() error {
	if p.DeliberationRounds < 1 || p.DeliberationRounds > 3 {
		return fmt.Errorf("DeliberationRounds must be in [1,3], got %d", p.DeliberationRounds)
	}
	return nil
}

// resolveSetup converts user Input into a PipelineSetup.
// SetupValid=false (the caller did not provide a setup) falls back to
// DefaultPipelineSetup. SetupValid=true uses in.Setup AS-IS — including an
// all-zero-fields "everything off" request — so callers that want no gates
// pass HumanGates: nil and the gate does not fire. The caller (engine_pipeline.go)
// enforces PipelineSetup.Validate() on the result and fails the run via
// obs.Finished rather than silently substituting defaults (J24): an explicit
// but invalid setup must surface as an error, never as a quiet default that
// could enable Execution when the caller asked only to plan.
func resolveSetup(in Input) PipelineSetup {
	if !in.SetupValid {
		return DefaultPipelineSetup()
	}
	return in.Setup
}
