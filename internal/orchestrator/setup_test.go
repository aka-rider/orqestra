package orchestrator

import (
	"testing"
)

func TestDefaultPipelineSetup(t *testing.T) {
	def := DefaultPipelineSetup()
	if !def.Research {
		t.Error("Research should be true")
	}
	if !def.Execution {
		t.Error("Execution should be true")
	}
	if !def.Validation {
		t.Error("Validation should be true")
	}
	if !def.HumanGates.Active(GateAfterDeliberation) {
		t.Error("GateAfterDeliberation should be active by default")
	}
}

func TestPipelineSetup_Validate(t *testing.T) {
	tests := []struct {
		name    string
		setup   PipelineSetup
		wantErr bool
	}{
		{
			name:    "default is valid",
			setup:   DefaultPipelineSetup(),
			wantErr: false,
		},
		{
			name:    "all disabled is invalid",
			setup:   PipelineSetup{Research: false, Execution: false, Validation: false},
			wantErr: true,
		},
		{
			name:    "research only is valid",
			setup:   PipelineSetup{Research: true, Execution: false, Validation: false},
			wantErr: false,
		},
		{
			name:    "execution only is valid",
			setup:   PipelineSetup{Research: false, Execution: true, Validation: false},
			wantErr: false,
		},
		{
			name:    "validation only is valid",
			setup:   PipelineSetup{Research: false, Execution: false, Validation: true},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.setup.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveSetup(t *testing.T) {
	tests := []struct {
		name     string
		input    Input
		wantExec bool
		wantGate bool
	}{
		{
			name:     "zero input uses defaults",
			input:    Input{},
			wantExec: true,
			wantGate: true,
		},
		{
			name: "explicit setup with no gates",
			input: Input{Setup: PipelineSetup{
				Research: true, Execution: true, Validation: true,
				HumanGates: HumanGateSet{},
			}},
			wantExec: true,
			wantGate: false,
		},
		{
			name: "explicit setup disables execution",
			input: Input{Setup: PipelineSetup{
				Research: true, Execution: false, Validation: false,
				HumanGates: HumanGateSet{},
			}},
			wantExec: false,
			wantGate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := resolveSetup(tt.input)
			if s.Execution != tt.wantExec {
				t.Errorf("Execution = %v, want %v", s.Execution, tt.wantExec)
			}
			if s.HumanGates.Active(GateAfterDeliberation) != tt.wantGate {
				t.Errorf("GateAfterDeliberation active = %v, want %v", s.HumanGates.Active(GateAfterDeliberation), tt.wantGate)
			}
		})
	}
}
