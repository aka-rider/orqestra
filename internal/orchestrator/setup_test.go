package orchestrator

import (
	"testing"
)

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
			name:    "plan-only (no execution/validation) is valid",
			setup:   PipelineSetup{Execution: false, Validation: false, DeliberationRounds: 1},
			wantErr: false,
		},
		{
			name:    "execution only is valid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: 1},
			wantErr: false,
		},
		{
			name:    "validation only is valid",
			setup:   PipelineSetup{Validation: true, DeliberationRounds: 1},
			wantErr: false,
		},
		{
			name:    "rounds=0 invalid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: 0},
			wantErr: true,
		},
		{
			name:    "rounds=1 valid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: 1},
			wantErr: false,
		},
		{
			name:    "rounds=3 valid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: 3},
			wantErr: false,
		},
		{
			name:    "rounds=4 invalid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: 4},
			wantErr: true,
		},
		{
			name:    "rounds=-1 invalid",
			setup:   PipelineSetup{Execution: true, DeliberationRounds: -1},
			wantErr: true,
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

func TestIsZeroSetup_DeliberationRounds(t *testing.T) {
	// Pure zero value — nothing set by caller.
	if !isZeroSetup(PipelineSetup{}) {
		t.Error("zero PipelineSetup should be zero")
	}

	// Execution=true alone breaks zero-ness.
	if isZeroSetup(PipelineSetup{Execution: true}) {
		t.Error("PipelineSetup{Execution:true} should not be zero")
	}

	// DeliberationRounds=1 alone breaks zero-ness (caller explicitly set it).
	if isZeroSetup(PipelineSetup{DeliberationRounds: 1}) {
		t.Error("PipelineSetup{DeliberationRounds:1} should not be zero")
	}
}
