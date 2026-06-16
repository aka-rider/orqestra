package orchestrator

import (
	"testing"
)

func TestHumanGatePosition_IsPlanGate(t *testing.T) {
	tests := []struct {
		pos      HumanGatePosition
		isPlan   bool
	}{
		{GateAfterResearch, true},
		{GateAfterDeliberation, true},
		{GateAfterExecution, false},
		{GateAfterValidation, false},
	}
	for _, tt := range tests {
		t.Run(tt.pos.String(), func(t *testing.T) {
			if got := tt.pos.IsPlanGate(); got != tt.isPlan {
				t.Errorf("IsPlanGate() = %v, want %v", got, tt.isPlan)
			}
		})
	}
}

func TestHumanGateSet_Active(t *testing.T) {
	set := HumanGateSet{GateAfterDeliberation, GateAfterExecution}
	if !set.Active(GateAfterDeliberation) {
		t.Error("GateAfterDeliberation should be active")
	}
	if !set.Active(GateAfterExecution) {
		t.Error("GateAfterExecution should be active")
	}
	if set.Active(GateAfterResearch) {
		t.Error("GateAfterResearch should not be active")
	}
	if set.Active(GateAfterValidation) {
		t.Error("GateAfterValidation should not be active")
	}
}

func TestPhaseDir(t *testing.T) {
	tests := []struct {
		pos HumanGatePosition
		want string
	}{
		{GateAfterResearch, "research"},
		{GateAfterDeliberation, "deliberation"},
		{GateAfterExecution, "execution"},
		{GateAfterValidation, "validation"},
	}
	for _, tt := range tests {
		t.Run(tt.pos.String(), func(t *testing.T) {
			if got := phaseDir(tt.pos); got != tt.want {
				t.Errorf("phaseDir(%s) = %q, want %q", tt.pos, got, tt.want)
			}
		})
	}
}

func TestRestartPhase_String(t *testing.T) {
	tests := []RestartPhase{
		RestartResearch, RestartDeliberation, RestartExecution, RestartValidation,
	}
	for _, rp := range tests {
		t.Run(string(rp), func(t *testing.T) {
			if string(rp) != phaseDir(rpToPos(rp)) {
				t.Errorf("RestartPhase %s should match phaseDir", rp)
			}
		})
	}
}

func rpToPos(rp RestartPhase) HumanGatePosition {
	switch rp {
	case RestartResearch:
		return GateAfterResearch
	case RestartDeliberation:
		return GateAfterDeliberation
	case RestartExecution:
		return GateAfterExecution
	case RestartValidation:
		return GateAfterValidation
	default:
		return -1
	}
}
