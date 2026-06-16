package orchestrator

import (
	"fmt"
)

// HumanGatePosition marks where in the pipeline a human gate fires.
type HumanGatePosition int

const (
	GateAfterResearch     HumanGatePosition = iota
	GateAfterDeliberation
	GateAfterExecution
	GateAfterValidation
)

// IsPlanGate reports whether this gate position requires plan review (rich gate).
func (p HumanGatePosition) IsPlanGate() bool { return p == GateAfterResearch || p == GateAfterDeliberation }

// String returns a human-readable name for the gate position.
func (p HumanGatePosition) String() string {
	switch p {
	case GateAfterResearch:
		return "after research"
	case GateAfterDeliberation:
		return "after deliberation"
	case GateAfterExecution:
		return "after execution"
	case GateAfterValidation:
		return "after validation"
	default:
		return fmt.Sprintf("gate position %d", int(p))
	}
}

// HumanGateSet is an ordered list of active gate positions.
type HumanGateSet []HumanGatePosition

// Active reports whether pos is in the set.
func (h HumanGateSet) Active(pos HumanGatePosition) bool {
	for _, p := range h {
		if p == pos {
			return true
		}
	}
	return false
}

// phaseDir returns the unified session subdirectory name for a gate position.
func phaseDir(pos HumanGatePosition) string {
	switch pos {
	case GateAfterResearch:
		return "research"
	case GateAfterDeliberation:
		return "deliberation"
	case GateAfterExecution:
		return "execution"
	case GateAfterValidation:
		return "validation"
	default:
		panic(fmt.Sprintf("orchestrator: unknown gate position %d", int(pos)))
	}
}

// RestartPhase identifies which phase to restart from.
type RestartPhase string

const (
	RestartResearch     RestartPhase = "research"
	RestartDeliberation RestartPhase = "deliberation"
	RestartExecution    RestartPhase = "execution"
	RestartValidation   RestartPhase = "validation"
)
