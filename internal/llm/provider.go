package llm

import (
	"context"
	"time"
)

// Provider is the unified interface to call supported LLM models.
type Provider interface {
	ID() string
	Generate(ctx context.Context, req *Request) (*Response, error)
}

// Request is a structured LLM request.
type Request struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	// ResponseJSON requests structured JSON output.
	ResponseJSON bool
}

// Message is a single message in the conversation.
type Message struct {
	Role    string
	Content string
}

// Response is the result of an LLM call.
type Response struct {
	Content string
	Model   string
	Latency time.Duration
	Tokens  TokenUsage
}

// TokenUsage tracks input/output token consumption.
type TokenUsage struct {
	Input  int
	Output int
}
