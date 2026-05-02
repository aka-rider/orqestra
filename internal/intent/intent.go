package intent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xiii/orqestra/internal/harness"
)

// Intent is the parsed result of intent recognition.
type Intent struct {
	Rephrased  string  `json:"rephrased"`
	Outcome    string  `json:"outcome"`
	Confidence float64 `json:"confidence"`
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

	if intent.Rephrased == "" {
		return Intent{}, errors.New("intent recognition returned empty rephrased field")
	}

	return intent, nil
}
