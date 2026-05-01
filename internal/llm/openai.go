package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider implements Provider using an OpenAI-compatible chat completions API.
// Works with Ollama, llama-server, and any OpenAI-compatible endpoint.
type OpenAIProvider struct {
	id       string
	baseURL  string
	apiKey   string
	model    string
	client   *http.Client
}

// NewOpenAIProvider creates a provider targeting an OpenAI-compatible endpoint.
func NewOpenAIProvider(id, baseURL, model, apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		id:      id,
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (p *OpenAIProvider) ID() string {
	return p.id
}

func (p *OpenAIProvider) Generate(ctx context.Context, req *Request) (*Response, error) {
	start := time.Now()

	model := req.Model
	if model == "" {
		model = p.model
	}

	messages := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		messages = append(messages, openAIMessage{Role: m.Role, Content: m.Content})
	}

	body := openAIChatRequest{
		Model:    model,
		Messages: messages,
	}

	if req.ResponseJSON {
		body.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", p.id, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", p.id, err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned status %d: %s", p.id, httpResp.StatusCode, string(respBody))
	}

	var chatResp openAIChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response from %s: %w", p.id, err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("%s returned no choices", p.id)
	}

	return &Response{
		Content: chatResp.Choices[0].Message.Content,
		Model:   chatResp.Model,
		Latency: time.Since(start),
		Tokens: TokenUsage{
			Input:  chatResp.Usage.PromptTokens,
			Output: chatResp.Usage.CompletionTokens,
		},
	}, nil
}

// OpenAI API types

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model          string              `json:"model"`
	Messages       []openAIMessage     `json:"messages"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
