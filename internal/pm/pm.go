package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

// ProjectManager decomposes an approved Specification into independent work
// packages that separate worker sessions execute in parallel.
type ProjectManager struct {
	runner harness.CLIRunner
	cfg    *config.ProjectManagerConfig
}

// New creates a ProjectManager backed by the given CLIRunner.
func New(runner harness.CLIRunner, cfg *config.ProjectManagerConfig) *ProjectManager {
	return &ProjectManager{runner: runner, cfg: cfg}
}

// Decompose sends the specification to the PM model and returns a ProjectPlan.
func (pm *ProjectManager) Decompose(ctx context.Context, spec types.Specification) (types.ProjectPlan, error) {
	prompt := buildDecomposePrompt(spec)
	result, err := pm.runner.RunPrint(ctx, prompt, pm.cfg.SystemPrompt)
	if err != nil {
		return types.ProjectPlan{}, fmt.Errorf("project manager: %w", err)
	}
	return pm.parsePlan(result.Output)
}

// DecomposeStreaming sends the specification to the PM model, streams output,
// and returns the parsed ProjectPlan.
func (pm *ProjectManager) DecomposeStreaming(ctx context.Context, spec types.Specification, stdout io.Writer) (types.ProjectPlan, error) {
	prompt := buildDecomposePrompt(spec)
	result, err := pm.runner.RunStreaming(ctx, prompt, pm.cfg.SystemPrompt, stdout)
	if err != nil {
		return types.ProjectPlan{}, fmt.Errorf("project manager: %w", err)
	}
	return pm.parsePlan(result.Output)
}

func (pm *ProjectManager) parsePlan(raw string) (types.ProjectPlan, error) {
	content := strings.TrimSpace(raw)
	content = stripCodeFences(content)

	// Handle claude envelope: {"type":"result","result":"..."}
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = stripCodeFences(strings.TrimSpace(envelope.Result))
	}

	var plan types.ProjectPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return types.ProjectPlan{}, fmt.Errorf("parse project plan JSON: %w (raw: %s)", err, content)
	}

	if err := validatePlan(plan); err != nil {
		return types.ProjectPlan{}, err
	}

	return plan, nil
}

// validatePlan checks structural integrity of a ProjectPlan.
func validatePlan(plan types.ProjectPlan) error {
	if len(plan.Packages) == 0 {
		return fmt.Errorf("project plan has no packages")
	}

	ids := make(map[string]bool, len(plan.Packages))
	for _, pkg := range plan.Packages {
		if pkg.ID == "" {
			return fmt.Errorf("work package missing id")
		}
		if ids[pkg.ID] {
			return fmt.Errorf("duplicate work package id %q", pkg.ID)
		}
		ids[pkg.ID] = true

		if len(pkg.Steps) == 0 {
			return fmt.Errorf("work package %q has no steps", pkg.ID)
		}
	}

	// Check dependency references and detect cycles.
	for _, pkg := range plan.Packages {
		for _, dep := range pkg.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("work package %q depends on unknown package %q", pkg.ID, dep)
			}
			if dep == pkg.ID {
				return fmt.Errorf("work package %q depends on itself", pkg.ID)
			}
		}
	}

	if err := detectCycles(plan.Packages); err != nil {
		return err
	}

	return nil
}

// detectCycles uses Kahn's algorithm to detect cycles in the dependency graph.
func detectCycles(packages []types.WorkPackage) error {
	idx := make(map[string]int, len(packages))
	for i, pkg := range packages {
		idx[pkg.ID] = i
	}

	inDegree := make([]int, len(packages))
	for i, pkg := range packages {
		for _, dep := range pkg.DependsOn {
			_ = dep
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	processed := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		processed++

		curID := packages[cur].ID
		for i, pkg := range packages {
			for _, dep := range pkg.DependsOn {
				if dep == curID {
					inDegree[i]--
					if inDegree[i] == 0 {
						queue = append(queue, i)
					}
				}
			}
		}
	}

	if processed != len(packages) {
		return fmt.Errorf("cycle detected in work package dependencies")
	}
	return nil
}

// buildDecomposePrompt serializes the specification into the PM prompt.
func buildDecomposePrompt(spec types.Specification) string {
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	return fmt.Sprintf("Decompose this approved specification into independent work packages:\n\n%s", string(specJSON))
}

// stripCodeFences removes markdown code fences from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
