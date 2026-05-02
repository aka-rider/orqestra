package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client calls an OpenAI-compatible API (e.g. llama-server).
type Client struct {
	Model          string
	AllowedTools   []string
	PermissionMode string // "plan" or "full" (metadata only, not enforced by server)
	BaseURL        string // e.g. "http://192.168.50.212:11434"
	HTTPClient     *http.Client
}

type Response struct {
	Content string
	Latency time.Duration
	Usage   *TokenUsage
}

// NewClient creates a harness client targeting an OpenAI-compatible server.
func NewClient(model string, allowedTools []string) *Client {
	return &Client{
		Model:          model,
		AllowedTools:   allowedTools,
		PermissionMode: "plan",
		BaseURL:        "http://192.168.50.212:11434",
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute, // generous for long generations
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
	}
}

// chatMessage is an OpenAI chat message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is an OpenAI-compatible chat completion request.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// chatChoice is an OpenAI chat choice (non-streaming).
type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatResponse is an OpenAI-compatible chat completion response.
type chatResponse struct {
	Choices []chatChoice    `json:"choices"`
	Usage   *chatUsageField `json:"usage,omitempty"`
}

// chatUsageField captures token usage from OpenAI-compatible responses.
type chatUsageField struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// streamDelta is the delta in a streaming chunk.
type streamDelta struct {
	Content string `json:"content"`
}

// streamChoice is a choice in a streaming chunk.
type streamChoice struct {
	Delta streamDelta `json:"delta"`
}

// streamChunk is an OpenAI-compatible streaming chunk.
type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

// Run calls the chat completions endpoint (non-streaming).
func (c *Client) Run(ctx context.Context, prompt, systemPrompt string) (*Response, error) {
	start := time.Now()

	messages := c.buildMessages(prompt, systemPrompt)
	reqBody := chatRequest{
		Model:    c.Model,
		Messages: messages,
		Stream:   false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}

	result := &Response{
		Content: chatResp.Choices[0].Message.Content,
		Latency: time.Since(start),
	}
	if chatResp.Usage != nil {
		result.Usage = &TokenUsage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:  chatResp.Usage.TotalTokens,
		}
	}
	return result, nil
}

// RunStreaming calls the chat completions endpoint with streaming, writing
// incremental content to out. The full output is also returned as a Response.
func (c *Client) RunStreaming(ctx context.Context, prompt, systemPrompt string, out io.Writer) (*Response, error) {
	start := time.Now()

	messages := c.buildMessages(prompt, systemPrompt)
	reqBody := chatRequest{
		Model:    c.Model,
		Messages: messages,
		Stream:   true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Use a client without overall timeout for streaming (context handles cancellation)
	streamClient := &http.Client{
		Transport: c.HTTPClient.Transport,
		Timeout:   0,
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var content strings.Builder
	var parseFailures int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: {...}" or "data: [DONE]"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			parseFailures++
			slog.Debug("malformed SSE chunk", "err", err, "data", truncateForLog(data, 200))
			if parseFailures >= 5 {
				return nil, fmt.Errorf("aborting: %d consecutive malformed SSE chunks (last: %s)", parseFailures, truncateForLog(data, 200))
			}
			continue
		}
		parseFailures = 0 // reset on successful parse

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				out.Write([]byte(choice.Delta.Content))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading stream: %w", err)
	}

	return &Response{
		Content: content.String(),
		Latency: time.Since(start),
	}, nil
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (c *Client) buildMessages(prompt, systemPrompt string) []chatMessage {
	var messages []chatMessage
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})
	return messages
}
