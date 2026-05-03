package intent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xiii/orqestra/internal/harness"
)

// Verdict classifies the intent recognition outcome.
type Verdict string

const (
	VerdictAccept  Verdict = "accept"
	VerdictClarify Verdict = "clarify"
	VerdictReject  Verdict = "reject"
)

// Intent is the parsed result of intent recognition.
type Intent struct {
	Verdict                Verdict  `json:"verdict"`
	Rephrased              string   `json:"rephrased"`
	EndState               string   `json:"end_state"`
	Reason                 string   `json:"reason"`
	Questions              []string `json:"questions"`
	ImprovedPromptExamples []string `json:"improved_prompt_examples"`
	Confidence             float64  `json:"confidence"`
}

// IntentConfig configures the intent recognizer.
type IntentConfig struct {
	ModelRef     string `yaml:"model_ref"`
	SystemPrompt string `yaml:"system_prompt"`
}

// Recognizer uses a CLIRunner to rephrase and clarify user prompts.
type Recognizer struct {
	runner harness.CLIRunner
	cfg    *IntentConfig
}

// New creates a new intent Recognizer.
func New(runner harness.CLIRunner, cfg *IntentConfig) *Recognizer {
	return &Recognizer{runner: runner, cfg: cfg}
}

// Recognize sends the raw prompt to the LLM and parses the structured response.
func (r *Recognizer) Recognize(ctx context.Context, rawPrompt string) (Intent, error) {
	result, err := r.runner.RunPrint(ctx, rawPrompt, r.cfg.SystemPrompt)
	if err != nil {
		return Intent{}, fmt.Errorf("intent recognition failed: %w", err)
	}

	var intent Intent
	if err := json.Unmarshal([]byte(result.Output), &intent); err != nil {
		return Intent{}, fmt.Errorf("parsing intent response: %w", err)
	}

	switch intent.Verdict {
	case VerdictAccept, VerdictClarify, VerdictReject:
		// valid
	default:
		return Intent{}, fmt.Errorf("intent recognition returned invalid verdict %q", intent.Verdict)
	}

	if intent.Rephrased == "" {
		return Intent{}, errors.New("intent recognition returned empty rephrased field")
	}
	if intent.Verdict == VerdictAccept && intent.EndState == "" {
		return Intent{}, errors.New("intent recognition accepted but returned empty end_state")
	}

	return intent, nil
}
