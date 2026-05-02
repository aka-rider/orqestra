package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/xiii/orqestra/internal/types"
)

// completionMessage is a single message in an OpenAI-compatible chat request.
type completionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model    string              `json:"model"`
	Messages []completionMessage `json:"messages"`
}

type completionResponseChoice struct {
	Message completionMessage `json:"message"`
}

type completionResponse struct {
	Choices []completionResponseChoice `json:"choices"`
}

// Validator validates work output against spec acceptance criteria via a direct
// HTTP call to a llama-server OpenAI-compatible endpoint.
type Validator struct {
	EndpointURL string
	httpClient  *http.Client
}

// New creates a Validator targeting the given llama-server base URL.
// Returns error if endpointURL is empty or not a valid URL.
func New(endpointURL string) (*Validator, error) {
	if endpointURL == "" {
		return nil, fmt.Errorf("endpointURL is required")
	}
	if _, err := url.ParseRequestURI(endpointURL); err != nil {
		return nil, fmt.Errorf("invalid endpointURL %q: %w", endpointURL, err)
	}
	return &Validator{
		EndpointURL: strings.TrimRight(endpointURL, "/"),
		httpClient:  &http.Client{},
	}, nil
}

// Validate checks work output against the spec's acceptance criteria.
// Returns an error if the spec has no acceptance criteria, if the HTTP call
// fails, if the response is malformed, or if required fields are missing.
func (v *Validator) Validate(ctx context.Context, spec types.Specification, output types.WorkOutput) (types.ValidationResult, error) {
	if len(spec.Acceptance) == 0 {
		return types.ValidationResult{}, fmt.Errorf("spec has no acceptance criteria — validation contract is empty")
	}

	reqBody := completionRequest{
		Model: "local",
		Messages: []completionMessage{
			{Role: "system", Content: workValidatorHTTPSystemPrompt},
			{Role: "user", Content: buildValidationUserMessage(spec, output)},
		},
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return types.ValidationResult{}, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := v.EndpointURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return types.ValidationResult{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return types.ValidationResult{}, fmt.Errorf("llama-server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		return types.ValidationResult{}, fmt.Errorf("llama-server error %d: %s", resp.StatusCode, string(body))
	}

	var apiResp completionResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return types.ValidationResult{}, fmt.Errorf("decode response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return types.ValidationResult{}, fmt.Errorf("empty choices in llama-server response")
	}

	raw := apiResp.Choices[0].Message.Content
	slog.Debug("work validator raw response", "content", raw)

	// Use *bool to detect the missing "passed" field — bool zero value is false,
	// so we cannot distinguish "explicitly false" from "field omitted".
	var strictResult struct {
		Passed         *bool                   `json:"passed"`
		Score          float64                 `json:"score"`
		FailedCriteria []types.FailedCriterion `json:"failed_criteria"`
	}
	if err := json.Unmarshal([]byte(raw), &strictResult); err != nil {
		return types.ValidationResult{}, fmt.Errorf("parse validation result: %w", err)
	}
	if strictResult.Passed == nil {
		return types.ValidationResult{}, fmt.Errorf("validation result missing required field 'passed'")
	}

	return types.ValidationResult{
		Passed:         *strictResult.Passed,
		Score:          strictResult.Score,
		FailedCriteria: strictResult.FailedCriteria,
	}, nil
}

const workValidatorHTTPSystemPrompt = `You are a strict acceptance-test evaluator. Given a specification's acceptance criteria and the worker's execution output, determine if each criterion was met.

Respond with a JSON object with exactly these fields:
- "passed": boolean — true if ALL criteria are met, false otherwise
- "score": float 0.0–1.0 — fraction of criteria passed
- "failed_criteria": array of {"criterion": "...", "reason": "..."} — empty array if passed=true

Respond ONLY with valid JSON. No markdown fences, no commentary.`

func buildValidationUserMessage(spec types.Specification, output types.WorkOutput) string {
	var sb strings.Builder

	sb.WriteString("Acceptance Criteria:\n")
	for i, criterion := range spec.Acceptance {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, criterion)
	}

	sb.WriteString("\nWork Output:\n")
	fmt.Fprintf(&sb, "Exit code: %d\n", output.ExitCode)

	if output.Stdout != "" {
		fmt.Fprintf(&sb, "\nStdout:\n%s\n", truncate(output.Stdout, 4000))
	}

	if output.Stderr != "" {
		fmt.Fprintf(&sb, "\nStderr:\n%s\n", truncate(output.Stderr, 2000))
	}

	if len(output.MutatedPaths) > 0 {
		sb.WriteString("\nMutated files:\n")
		for _, p := range output.MutatedPaths {
			fmt.Fprintf(&sb, "- %s\n", p)
		}
	}

	return sb.String()
}
