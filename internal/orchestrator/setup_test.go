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
			name:    "all disabled is invalid",
			setup:   PipelineSetup{Research: false, Execution: false, Validation: false},
			wantErr: true,
		},
		{
			name:    "research only is valid",
			setup:   PipelineSetup{Research: true, DeliberationRounds: 1},
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
			setup:   PipelineSetup{Research: true, DeliberationRounds: 0},
			wantErr: true,
		},
		{
			name:    "rounds=1 valid",
			setup:   PipelineSetup{Research: true, DeliberationRounds: 1},
			wantErr: false,
		},
		{
			name:    "rounds=3 valid",
			setup:   PipelineSetup{Research: true, DeliberationRounds: 3},
			wantErr: false,
		},
		{
			name:    "rounds=4 invalid",
			setup:   PipelineSetup{Research: true, DeliberationRounds: 4},
			wantErr: true,
		},
		{
			name:    "rounds=-1 invalid",
			setup:   PipelineSetup{Research: true, DeliberationRounds: -1},
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

	// Research=true alone breaks zero-ness.
	if isZeroSetup(PipelineSetup{Research: true}) {
		t.Error("PipelineSetup{Research:true} should not be zero")
	}

	// DeliberationRounds=1 alone breaks zero-ness (caller explicitly set it).
	if isZeroSetup(PipelineSetup{DeliberationRounds: 1}) {
		t.Error("PipelineSetup{DeliberationRounds:1} should not be zero")
	}
}
