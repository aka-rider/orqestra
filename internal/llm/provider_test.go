package llm

import (
	"context"
	"testing"
)

func TestMockProvider_Generate(t *testing.T) {
	mock := &MockProvider{
		IDValue:  "test",
		Response: `{"verdict": "pass", "summary": "all good"}`,
	}

	resp, err := mock.Generate(context.Background(), &Request{
		Model:        "test-model",
		SystemPrompt: "You are a validator.",
		Messages:     []Message{{Role: "user", Content: "validate this"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != mock.Response {
		t.Errorf("expected %q, got %q", mock.Response, resp.Content)
	}
	if mock.CallCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.CallCount)
	}
}

func TestMockProvider_Error(t *testing.T) {
	mock := &MockProvider{
		IDValue: "test",
		Err:     context.DeadlineExceeded,
	}

	_, err := mock.Generate(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
