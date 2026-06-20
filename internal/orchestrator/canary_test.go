package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCanarySpectraCorpus replays recorded role outputs through each role's
// spectral canary. The *_bad_* fixtures are REAL recordings from run
// 2026-06-19-172044 (a role-collapse run) copied verbatim into testdata; the
// *_good_* fixtures are golden compliant samples. Real role-breaks must be
// rejected; compliant output must pass.
func TestCanarySpectraCorpus(t *testing.T) {
	tests := []struct {
		name    string
		spec    canarySpectrum
		file    string
		wantErr bool
	}{
		{"researcher real jumped-to-implementation", researchSpectrum, "researcher_bad_plan.md", true},
		{"critic real role-collapse", criticSpectrum, "critic_bad_collapse.md", true},
		{"researcher compliant fact report", researchSpectrum, "researcher_good.md", false},
		{"architect compliant plan", architectSpectrum, "architect_good.md", false},
		{"critic compliant report", criticSpectrum, "critic_good.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("testdata", "canary", tt.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", path, err)
			}
			gotErr := tt.spec.check(string(data))
			if (gotErr != nil) != tt.wantErr {
				frac, failed, vetoed := tt.spec.score(string(data))
				t.Fatalf("%s.check(%s) error = %v, wantErr %v (frac=%.2f failed=%v vetoed=%v)",
					tt.spec.role, tt.file, gotErr, tt.wantErr, frac, failed, vetoed)
			}
		})
	}
}

// TestCanarySignals exercises individual role-adherence signals: vetoes
// (unambiguous role breaks) must reject regardless of fraction, while minor
// incompleteness is tolerated under the 30% threshold.
func TestCanarySignals(t *testing.T) {
	tests := []struct {
		name    string
		spec    canarySpectrum
		md      string
		wantErr bool
	}{
		{
			name:    "researcher veto: emits ## Plan + ### File work packages",
			spec:    researchSpectrum,
			md:      "## Goal\nx\n## Codebase Facts\n- f\n## Constraints Discovered\n- c\n## Gotchas\n- g\n## Plan\n### File 1: foo.go\n1. Add bar",
			wantErr: true,
		},
		{
			name:    "researcher veto: announces implementation",
			spec:    researchSpectrum,
			md:      "## Goal\nx\n## Codebase Facts\n- f\n## Constraints Discovered\n- c\n## Gotchas\n- g\n\nLet me implement all four files now.",
			wantErr: true,
		},
		{
			name:    "researcher ok: one missing section tolerated",
			spec:    researchSpectrum,
			md:      "## Goal\nx\n## Codebase Facts\n- foo.go does X\n## Constraints Discovered\n- bar",
			wantErr: false,
		},
		{
			name:    "architect veto: offers to implement",
			spec:    architectSpectrum,
			md:      "# Plan\n## Work Packages\n### 1. x\n## Verification\ngo test\n\nReady to implement whenever you say go.",
			wantErr: true,
		},
		{
			name:    "architect ok: clean plan",
			spec:    architectSpectrum,
			md:      "# Plan\n## Goal\ng\n## Work Packages\n### 1. x\n**Done when:** go build\n## Verification\ngo test ./...",
			wantErr: false,
		},
		{
			name:    "critic veto: stops critiquing to implement",
			spec:    criticSpectrum,
			md:      "## Critic Report\n### Blockers Found\nnone\n### Summary\nok\n\nI'll stop critiquing and start implementing.",
			wantErr: true,
		},
		{
			name:    "critic ok: zero blockers is valid",
			spec:    criticSpectrum,
			md:      "## Critic Report\n### Blockers Found\nZero blockers found.\n### Summary\n- Total blockers: 0",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr := tt.spec.check(tt.md)
			if (gotErr != nil) != tt.wantErr {
				frac, failed, vetoed := tt.spec.score(tt.md)
				t.Fatalf("check() error = %v, wantErr %v (frac=%.2f failed=%v vetoed=%v)",
					gotErr, tt.wantErr, frac, failed, vetoed)
			}
		})
	}
}
