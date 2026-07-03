package orchestrator

import (
	"reflect"
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

// TestResolveSetup_Invalid_UsesDefault: SetupValid=false (caller did not
// provide a setup) always falls back to DefaultPipelineSetup, regardless of
// whatever happens to be in the (ignored) Setup field.
func TestResolveSetup_Invalid_UsesDefault(t *testing.T) {
	got := resolveSetup(Input{})
	want := DefaultPipelineSetup()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveSetup(Input{}) = %+v, want default %+v", got, want)
	}

	// Even a non-zero-looking Setup is ignored when SetupValid is false.
	got2 := resolveSetup(Input{Setup: PipelineSetup{Execution: true, DeliberationRounds: 2}})
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("resolveSetup with SetupValid=false ignored Setup contents: got %+v, want default %+v", got2, want)
	}
}

// TestResolveSetup_Valid_UsesAsIs: SetupValid=true is honored AS-IS even when
// every PipelineSetup field is a zero value (J24) — a caller that explicitly
// asked for plan-only (no execution, no validation, no gates) must never be
// silently upgraded to DefaultPipelineSetup (which enables Execution).
func TestResolveSetup_Valid_UsesAsIs(t *testing.T) {
	explicit := PipelineSetup{} // all-zero: plan-only, no gates
	got := resolveSetup(Input{Setup: explicit, SetupValid: true})
	if !reflect.DeepEqual(got, explicit) {
		t.Errorf("resolveSetup(SetupValid=true, all-zero Setup) = %+v, want the explicit zero setup %+v (not defaulted)", got, explicit)
	}
	if got.Execution {
		t.Error("an explicit all-zero setup must not gain Execution:true from DefaultPipelineSetup")
	}
}
