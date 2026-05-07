package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// GatewayVerdict classifies the gateway evaluation outcome.
type GatewayVerdict string

const (
	GatewayVerdictAccept GatewayVerdict = "accept"
	GatewayVerdictCoach  GatewayVerdict = "clarify"
)

// GatewayResult is the parsed result of gateway evaluation.
type GatewayResult struct {
	Verdict                GatewayVerdict `json:"verdict"`
	Rephrased              string         `json:"rephrased"`
	EndState               string         `json:"end_state"`
	Reason                 string         `json:"reason"`
	Questions              []string       `json:"questions"`
	ImprovedPromptExamples []string       `json:"improved_prompt_examples"`
	Confidence             float64        `json:"confidence"`
}

// Gateway uses a CLIRunner to rephrase and coach user prompts.
type Gateway struct {
	runner harness.CLIRunner
	cfg    *config.GatewayConfig
}

// NewGateway creates a new Gateway.
func NewGateway(runner harness.CLIRunner, cfg *config.GatewayConfig) *Gateway {
	return &Gateway{runner: runner, cfg: cfg}
}

// Evaluate sends the raw prompt to the LLM and parses the structured response.
func (g *Gateway) Evaluate(ctx context.Context, rawPrompt string) (GatewayResult, error) {
	result, err := g.runner.RunPrint(ctx, rawPrompt, g.cfg.SystemPrompt)
	if err != nil {
		return GatewayResult{}, fmt.Errorf("gateway evaluation failed: %w", err)
	}

	var gwResult GatewayResult
	if err := json.Unmarshal([]byte(result.Output), &gwResult); err != nil {
		return GatewayResult{}, fmt.Errorf("parsing gateway response: %w", err)
	}

	switch gwResult.Verdict {
	case GatewayVerdictAccept, GatewayVerdictCoach:
		// valid
	default:
		return GatewayResult{}, fmt.Errorf("gateway evaluation returned invalid verdict %q", gwResult.Verdict)
	}

	if gwResult.Rephrased == "" {
		return GatewayResult{}, errors.New("gateway evaluation returned empty rephrased field")
	}
	if gwResult.Verdict == GatewayVerdictAccept && gwResult.EndState == "" {
		return GatewayResult{}, errors.New("gateway evaluation accepted but returned empty end_state")
	}

	return gwResult, nil
}
