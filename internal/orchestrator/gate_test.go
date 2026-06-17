package orchestrator

import (
	"testing"
)

func TestHumanGatePosition_IsPlanGate(t *testing.T) {
	tests := []struct {
		pos    HumanGatePosition
		isPlan bool
	}{
		{GateAfterResearch, true},
		{GateAfterDeliberation, true},
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
	set := HumanGateSet{GateAfterDeliberation}
	if !set.Active(GateAfterDeliberation) {
		t.Error("GateAfterDeliberation should be active")
	}
	if set.Active(GateAfterResearch) {
		t.Error("GateAfterResearch should not be active")
	}
}
