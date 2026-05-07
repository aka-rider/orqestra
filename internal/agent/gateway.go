package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// GatewayVerdict classifies the gateway evaluation outcome.
type GatewayVerdict string

const (
	GatewayVerdictAccept GatewayVerdict = "accept"
	GatewayVerdictCoach  GatewayVerdict = "clarify"
)

// PromptBrief is the gateway's structured interpretation of user intent.
type PromptBrief struct {
	Task            string   `json:"task"`
	EndState        string   `json:"end_state"`
	Deliverables    []string `json:"deliverables"`
	Scope           []string `json:"scope"`
	NonScope        []string `json:"non_scope"`
	AcceptanceHints []string `json:"acceptance_hints"`
}

// Question is a coaching question with options and a pre-filled default.
type Question struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Default string   `json:"default"`
}

// GatewayResult is the parsed result of gateway evaluation.
type GatewayResult struct {
	Verdict         GatewayVerdict `json:"verdict"`
	Brief           PromptBrief    `json:"brief"`
	Questions       []Question     `json:"questions"`
	Confidence      float64        `json:"confidence"`
	PlannerQuestion string         `json:"planner_question"`
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
	result, err := g.runner.RunStreaming(ctx, rawPrompt, g.cfg.SystemPrompt, io.Discard)
	if err != nil {
		return GatewayResult{}, fmt.Errorf("gateway evaluation failed: %w", err)
	}

	slog.Debug("gateway raw output", "len", len(result.Output), "first_200", truncate(result.Output, 200))

	jsonStr := extractJSON(result.Output)
	if jsonStr == "" {
		return GatewayResult{}, fmt.Errorf("parsing gateway response: no JSON object found in output")
	}

	var gwResult GatewayResult
	if err := json.Unmarshal([]byte(jsonStr), &gwResult); err != nil {
		return GatewayResult{}, fmt.Errorf("parsing gateway response: %w", err)
	}

	switch gwResult.Verdict {
	case GatewayVerdictAccept, GatewayVerdictCoach:
		// valid
	default:
		return GatewayResult{}, fmt.Errorf("gateway evaluation returned invalid verdict %q", gwResult.Verdict)
	}

	if gwResult.Brief.Task == "" {
		return GatewayResult{}, errors.New("gateway evaluation returned empty brief.task")
	}
	if gwResult.Verdict == GatewayVerdictAccept {
		if gwResult.Brief.EndState == "" {
			return GatewayResult{}, errors.New("gateway accepted but returned empty brief.end_state")
		}
		if gwResult.PlannerQuestion == "" {
			return GatewayResult{}, errors.New("gateway accepted but returned empty planner_question")
		}
	}
	if gwResult.Verdict == GatewayVerdictCoach && len(gwResult.Questions) == 0 {
		return GatewayResult{}, errors.New("gateway coach verdict requires at least one question")
	}
	if len(gwResult.Questions) > 3 {
		return GatewayResult{}, fmt.Errorf("gateway returned %d questions, max is 3", len(gwResult.Questions))
	}

	return gwResult, nil
}

// extractJSON finds the outermost JSON object in s.
// Small models often wrap JSON in prose ("I'll help...\n{...}\n").
// Returns the extracted JSON string, or empty string if none found.
func extractJSON(s string) string {
	// Fast path: output is already valid JSON.
	s = strings.TrimSpace(s)
	if len(s) > 0 && s[0] == '{' {
		return s
	}

	// Find first '{' and match to its closing '}'.
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
