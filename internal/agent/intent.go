package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// IntentVerdict classifies the intent recognition outcome.
type IntentVerdict string

const (
	IntentVerdictAccept  IntentVerdict = "accept"
	IntentVerdictClarify IntentVerdict = "clarify"
	IntentVerdictReject  IntentVerdict = "reject"
)

// Intent is the parsed result of intent recognition.
type Intent struct {
	Verdict                IntentVerdict `json:"verdict"`
	Rephrased              string        `json:"rephrased"`
	EndState               string        `json:"end_state"`
	Reason                 string        `json:"reason"`
	Questions              []string      `json:"questions"`
	ImprovedPromptExamples []string      `json:"improved_prompt_examples"`
	Confidence             float64       `json:"confidence"`
}

// Recognizer uses a CLIRunner to rephrase and clarify user prompts.
type Recognizer struct {
	runner harness.CLIRunner
	cfg    *config.IntentConfig
}

// NewRecognizer creates a new intent Recognizer.
func NewRecognizer(runner harness.CLIRunner, cfg *config.IntentConfig) *Recognizer {
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
	case IntentVerdictAccept, IntentVerdictClarify, IntentVerdictReject:
		// valid
	default:
		return Intent{}, fmt.Errorf("intent recognition returned invalid verdict %q", intent.Verdict)
	}

	if intent.Rephrased == "" {
		return Intent{}, errors.New("intent recognition returned empty rephrased field")
	}
	if intent.Verdict == IntentVerdictAccept && intent.EndState == "" {
		return Intent{}, errors.New("intent recognition accepted but returned empty end_state")
	}

	return intent, nil
}
