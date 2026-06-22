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

// isZeroSetup reports whether s is the zero value (no fields set by the caller).
// Can't use == because HumanGateSet is a slice.
func isZeroSetup(s PipelineSetup) bool {
	return !s.Execution && !s.Validation && s.DeliberationRounds == 0 && len(s.HumanGates) == 0
}

// resolveSetup converts user Input into a PipelineSetup.
// A zero-value Input.Setup falls back to DefaultPipelineSetup.
// An explicitly-set Input.Setup is used as-is so callers that want
// no gates pass HumanGates: nil and the gate does not fire.
func resolveSetup(in Input) PipelineSetup {
	if isZeroSetup(in.Setup) {
		return DefaultPipelineSetup()
	}
	return in.Setup
}
