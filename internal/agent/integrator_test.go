package agent

import "testing"

func TestParseIntegratorGiveUp(t *testing.T) {
	// INV-ROLE-INTEGRATE: the give-up sentinel is recognized despite the
	// formatting drift a 30B-class model may introduce (case, bullet
	// markers, bold markdown, a missing colon) — never silently missed.
	tests := []struct {
		name       string
		raw        string
		wantReason string
		wantGaveUp bool
	}{
		{
			name:       "exact prefix",
			raw:        "INTEGRATOR-GIVE-UP: both sides rewrote parseConfig",
			wantReason: "both sides rewrote parseConfig",
			wantGaveUp: true,
		},
		{
			name:       "lowercase sentinel",
			raw:        "integrator-give-up: conflicting edits in main.go",
			wantReason: "conflicting edits in main.go",
			wantGaveUp: true,
		},
		{
			name:       "mixed case sentinel",
			raw:        "Integrator-Give-Up: unclear intent",
			wantReason: "unclear intent",
			wantGaveUp: true,
		},
		{
			name:       "leading markdown bullet",
			raw:        "- INTEGRATOR-GIVE-UP: both sides changed the same function",
			wantReason: "both sides changed the same function",
			wantGaveUp: true,
		},
		{
			name:       "bold wraps sentinel and colon",
			raw:        "**INTEGRATOR-GIVE-UP:** reason unclear",
			wantReason: "reason unclear",
			wantGaveUp: true,
		},
		{
			name:       "bold wraps sentinel only, colon outside",
			raw:        "**INTEGRATOR-GIVE-UP**: reason unclear",
			wantReason: "reason unclear",
			wantGaveUp: true,
		},
		{
			name:       "missing colon",
			raw:        "INTEGRATOR-GIVE-UP both sides rewrote parseConfig",
			wantReason: "both sides rewrote parseConfig",
			wantGaveUp: true,
		},
		{
			name:       "extra whitespace before colon",
			raw:        "INTEGRATOR-GIVE-UP  :  extra spacing around colon",
			wantReason: "extra spacing around colon",
			wantGaveUp: true,
		},
		{
			name:       "sentinel on later line of longer output",
			raw:        "I read both diffs.\nThey conflict irreconcilably.\nINTEGRATOR-GIVE-UP: cannot merge safely",
			wantReason: "cannot merge safely",
			wantGaveUp: true,
		},
		{
			name:       "no sentinel present",
			raw:        "I resolved the conflict by keeping both additions.",
			wantReason: "",
			wantGaveUp: false,
		},
		{
			name:       "empty input",
			raw:        "",
			wantReason: "",
			wantGaveUp: false,
		},
		{
			name:       "sentinel mentioned mid-sentence is not a give-up",
			raw:        "I will not need to use INTEGRATOR-GIVE-UP for this merge.",
			wantReason: "",
			wantGaveUp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, gaveUp := ParseIntegratorGiveUp(tt.raw)
			if gaveUp != tt.wantGaveUp {
				t.Fatalf("gaveUp = %v, want %v", gaveUp, tt.wantGaveUp)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
