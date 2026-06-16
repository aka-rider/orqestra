package orchestrator

import (
	"testing"
)

func TestDefaultPipelineSetup(t *testing.T) {
	def := DefaultPipelineSetup()
	if !def.Research {
		t.Error("Research should be true")
	}
	if def.DeliberationLoops != 1 {
		t.Errorf("DeliberationLoops should be 1, got %d", def.DeliberationLoops)
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
			name:    "loops zero is invalid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: 0, Execution: true},
			wantErr: true,
		},
		{
			name:    "loops one is valid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: 1, Execution: true},
			wantErr: false,
		},
		{
			name:    "loops ten is valid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: 10, Execution: true},
			wantErr: false,
		},
		{
			name:    "loops eleven is invalid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: 11, Execution: true},
			wantErr: true,
		},
		{
			name:    "loops negative is invalid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: -1, Execution: true},
			wantErr: true,
		},
		{
			name:    "all disabled is invalid",
			setup:   PipelineSetup{Research: false, Execution: false, Validation: false},
			wantErr: true,
		},
		{
			name:    "research only is valid",
			setup:   PipelineSetup{Research: true, DeliberationLoops: 1, Execution: false, Validation: false},
			wantErr: false,
		},
		{
			name:    "execution only is valid",
			setup:   PipelineSetup{Research: false, DeliberationLoops: 1, Execution: true, Validation: false},
			wantErr: false,
		},
		{
			name:    "validation only is valid",
			setup:   PipelineSetup{Research: false, DeliberationLoops: 1, Execution: false, Validation: true},
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
		wantLoops int
		wantExec  bool
	}{
		{
			name:     "zero input uses defaults",
			input:    Input{},
			wantLoops: 1,
			wantExec:  true,
		},
		{
			name:     "NoExecute disables execution and validation",
			input:    Input{NoExecute: true},
			wantLoops: 1,
			wantExec:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := resolveSetup(tt.input)
			if s.DeliberationLoops != tt.wantLoops {
				t.Errorf("DeliberationLoops = %d, want %d", s.DeliberationLoops, tt.wantLoops)
			}
			if s.Execution != tt.wantExec {
				t.Errorf("Execution = %v, want %v", s.Execution, tt.wantExec)
			}
		})
	}
}
