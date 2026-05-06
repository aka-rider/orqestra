//go:build darwin

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidatorApproved(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		approved bool
	}{
		{"pass verdict", []byte(`{"verdict":"pass"}`), true},
		{"warn verdict", []byte(`{"verdict":"warn"}`), true},
		{"fail verdict", []byte(`{"verdict":"fail"}`), false},
		{"empty data", nil, false},
		{"invalid json", []byte(`not json`), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.approved, validatorApproved(tt.data))
		})
	}
}

func TestPhaseTransitions(t *testing.T) {
	// Test that phase constants align with chrome package.
	assert.Equal(t, Phase(0), PhaseIntake)
	assert.Equal(t, Phase(1), PhasePlanner)
	assert.Equal(t, Phase(2), PhaseValidator)
	assert.Equal(t, Phase(3), PhaseWorker)
	assert.Equal(t, Phase(4), PhaseDone)
}
