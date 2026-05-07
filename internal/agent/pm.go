package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// ProjectManager decomposes an approved Specification into independent work
// packages that separate worker sessions execute in parallel.
type ProjectManager struct {
	runner harness.CLIRunner
	cfg    *config.ProjectManagerConfig
}

// NewProjectManager creates a ProjectManager backed by the given CLIRunner.
func NewProjectManager(runner harness.CLIRunner, cfg *config.ProjectManagerConfig) *ProjectManager {
	return &ProjectManager{runner: runner, cfg: cfg}
}

// Decompose sends the specification to the PM model and returns a ProjectPlan.
func (pm *ProjectManager) Decompose(ctx context.Context, spec Specification) (ProjectPlan, error) {
	prompt := buildDecomposePrompt(spec)
	result, err := pm.runner.RunPrint(ctx, prompt, pm.cfg.SystemPrompt)
	if err != nil {
		return ProjectPlan{}, fmt.Errorf("project manager: %w", err)
	}
	return pm.parsePlan(result.Output)
}

// DecomposeStreaming sends the specification to the PM model, streams output,
// and returns the parsed ProjectPlan.
func (pm *ProjectManager) DecomposeStreaming(ctx context.Context, spec Specification, stdout io.Writer) (ProjectPlan, error) {
	prompt := buildDecomposePrompt(spec)
	result, err := pm.runner.RunStreaming(ctx, prompt, pm.cfg.SystemPrompt, stdout)
	if err != nil {
		return ProjectPlan{}, fmt.Errorf("project manager: %w", err)
	}
	return pm.parsePlan(result.Output)
}

func (pm *ProjectManager) parsePlan(raw string) (ProjectPlan, error) {
	content := strings.TrimSpace(raw)
	content = pmStripCodeFences(content)

	// Handle claude envelope: {"type":"result","result":"..."}
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = pmStripCodeFences(strings.TrimSpace(envelope.Result))
	}

	var plan ProjectPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return ProjectPlan{}, fmt.Errorf("parse project plan JSON: %w (raw: %s)", err, content)
	}

	if err := ValidateProjectPlan(plan); err != nil {
		return ProjectPlan{}, err
	}

	return plan, nil
}

// buildDecomposePrompt serializes the specification into the PM prompt.
func buildDecomposePrompt(spec Specification) string {
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	return fmt.Sprintf("Decompose this approved specification into independent work packages:\n\n%s", string(specJSON))
}

// pmStripCodeFences removes markdown code fences from LLM output.
func pmStripCodeFences(s string) string {
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
