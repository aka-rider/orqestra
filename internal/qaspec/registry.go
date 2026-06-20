// Package qaspec loads the QA invariant registry (docs/qa/invariants.yaml) and
// runs the static integrity checks that defend docs/qa/qa-spec.md from rot.
//
// The same checks run two ways so the spec is falsifiable on every push:
//   - as a Go test (TestSpecIntegrity) so `make test` fails on spec rot, and
//   - via the cmd/qaverify CLI, which adds --write to regenerate the §9 ledger.
package qaspec

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Repo-relative paths of the two QA documents.
const (
	RegistryRel = "docs/qa/invariants.yaml"
	SpecRel     = "docs/qa/qa-spec.md"
)

// Anchor is a stable code symbol an invariant points at. Line numbers are
// banned (they rot silently); a renamed/removed symbol fails the anchor check.
type Anchor struct {
	File   string `yaml:"file"`
	Symbol string `yaml:"symbol"`
}

// Invariant is one registry entry. Status is gap | covered | defect.
type Invariant struct {
	ID      string   `yaml:"id"`
	Pillar  string   `yaml:"pillar"`
	Layer   string   `yaml:"layer"`
	Status  string   `yaml:"status"`
	Anchors []Anchor `yaml:"anchors"`
	Tests   []string `yaml:"tests"`
}

// Canary records a confirmed defect and the test that proves it is still live.
// CanaryTest, when set, is a NORMAL test (it runs in `make test`): it passes
// while the bug is live and fails the moment the bug is fixed.
type Canary struct {
	ID         string `yaml:"id"`
	Invariant  string `yaml:"invariant"`
	Summary    string `yaml:"summary"`
	State      string `yaml:"state"`
	CanaryTest string `yaml:"canary_test"`
	GateTest   string `yaml:"gate_test"`
}

// Registry is the parsed invariants.yaml.
type Registry struct {
	Invariants []Invariant `yaml:"invariants"`
	Canaries   []Canary    `yaml:"canaries"`
}

// RepoRoot walks up from the current working directory until it finds go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// Load reads and parses the registry under root.
func Load(root string) (Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, RegistryRel))
	if err != nil {
		return Registry{}, fmt.Errorf("read registry: %w", err)
	}
	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", RegistryRel, err)
	}
	return reg, nil
}
