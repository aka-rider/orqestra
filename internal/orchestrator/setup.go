package orchestrator

import "fmt"

// PipelineSetup configures which pipeline phases run and which gates fire.
type PipelineSetup struct {
	Research           bool
	Execution          bool
	Validation         bool
	DeliberationRounds int
	HumanGates         HumanGateSet
}

// DefaultPipelineSetup returns the default pipeline configuration.
func DefaultPipelineSetup() PipelineSetup {
	return PipelineSetup{
		Research:           true,
		Execution:          true,
		Validation:         true,
		DeliberationRounds: 1,
		HumanGates:         HumanGateSet{GateAfterDeliberation},
	}
}

// Validate checks invariant constraints on PipelineSetup.
func (p PipelineSetup) Validate() error {
	if !p.Research && !p.Execution && !p.Validation {
		return fmt.Errorf("at least one of Research, Execution, Validation must be enabled")
	}
	if p.DeliberationRounds < 1 || p.DeliberationRounds > 3 {
		return fmt.Errorf("DeliberationRounds must be in [1,3], got %d", p.DeliberationRounds)
	}
	return nil
}

// isZeroSetup reports whether s is the zero value (no fields set by the caller).
// Can't use == because HumanGateSet is a slice.
func isZeroSetup(s PipelineSetup) bool {
	return !s.Research && !s.Execution && !s.Validation && s.DeliberationRounds == 0 && len(s.HumanGates) == 0
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
