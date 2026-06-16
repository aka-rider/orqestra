package orchestrator

import (
	"fmt"
)

// PipelineSetup configures which pipeline phases run and which gates fire.
type PipelineSetup struct {
	Research          bool
	DeliberationLoops int          // 1..10; 0 → 1
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

// resolveSetup converts user Input into a PipelineSetup.
// Zero-value fields fall back to defaults; DeliberationLoops of 0 → 1.
func resolveSetup(in Input) PipelineSetup {
	def := DefaultPipelineSetup()
	if in.NoExecute {
		return PipelineSetup{
			Research: true, DeliberationLoops: def.DeliberationLoops,
			Execution: false, Validation: false, HumanGates: def.HumanGates,
		}
	}
	s := PipelineSetup{
		Research:          true,
		DeliberationLoops: def.DeliberationLoops,
		Execution:         true,
		Validation:        true,
		HumanGates:        def.HumanGates,
	}
	// Apply any explicit overrides from Input.Setup if present.
	// (Input.Setup is populated by the TUI setup panel.)
	return s
}
