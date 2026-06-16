package orchestrator

import (
	"fmt"
)

// PipelineSetup configures which pipeline phases run and which gates fire.
type PipelineSetup struct {
	Research          bool
	DeliberationLoops int // 1..10; 0 → 1
	Execution         bool
	Validation        bool
	HumanGates        HumanGateSet
}

// DefaultPipelineSetup returns the default pipeline configuration.
func DefaultPipelineSetup() PipelineSetup {
	return PipelineSetup{
		Research: true, DeliberationLoops: 1, Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
}

// Validate checks invariant constraints on PipelineSetup.
func (p PipelineSetup) Validate() error {
	if p.DeliberationLoops < 1 || p.DeliberationLoops > 10 {
		return fmt.Errorf("DeliberationLoops must be 1..10, got %d", p.DeliberationLoops)
	}
	if !p.Research && !p.Execution && !p.Validation {
		return fmt.Errorf("at least one of Research, Execution, Validation must be enabled")
	}
	return nil
}

// isZeroSetup reports whether s is the zero value (no fields set by the caller).
// Can't use == because HumanGateSet is a slice.
func isZeroSetup(s PipelineSetup) bool {
	return !s.Research && !s.Execution && !s.Validation &&
		s.DeliberationLoops == 0 && len(s.HumanGates) == 0
}

// resolveSetup converts user Input into a PipelineSetup.
// A zero-value Input.Setup falls back to DefaultPipelineSetup.
// An explicitly-set Input.Setup is used as-is (with loop clamping), so callers
// that want no gates pass HumanGates: nil and the gate does not fire.
func resolveSetup(in Input) PipelineSetup {
	if isZeroSetup(in.Setup) {
		return DefaultPipelineSetup()
	}
	s := in.Setup
	if s.DeliberationLoops < 1 {
		s.DeliberationLoops = 1
	}
	if s.DeliberationLoops > 10 {
		s.DeliberationLoops = 10
	}
	return s
}
