package orchestrator

import (
	"fmt"
)

// HumanGatePosition marks where in the pipeline a human gate fires.
type HumanGatePosition int

const (
	GateAfterDeliberation HumanGatePosition = iota
)

// IsPlanGate reports whether this gate position requires plan review (rich gate).
func (p HumanGatePosition) IsPlanGate() bool { return p == GateAfterDeliberation }

// String returns a human-readable name for the gate position.
func (p HumanGatePosition) String() string {
	switch p {
	case GateAfterDeliberation:
		return "after deliberation"
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

// Toggle returns a new set with pos added if absent, or removed if present.
func (h HumanGateSet) Toggle(pos HumanGatePosition) HumanGateSet {
	for i, p := range h {
		if p == pos {
			out := make(HumanGateSet, 0, len(h)-1)
			out = append(out, h[:i]...)
			return append(out, h[i+1:]...)
		}
	}
	return append(append(HumanGateSet(nil), h...), pos)
}

// RestartPhase identifies which phase to restart from.
type RestartPhase string

const (
	RestartDeliberation RestartPhase = "deliberation"
	RestartExecution    RestartPhase = "execution"
	RestartValidation   RestartPhase = "validation"
)
