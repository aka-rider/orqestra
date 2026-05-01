package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunStreaming_CapturesOutput(t *testing.T) {
	// Set up a fake OpenAI-compatible SSE server
	chunks := []string{"Hello", " from", " llama"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, chunk := range chunks {
			data := streamChunk{
				Choices: []streamChoice{{Delta: streamDelta{Content: chunk}}},
			}
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := NewClient("test-model", nil)
	c.BaseURL = srv.URL

	var buf bytes.Buffer
	resp, err := c.RunStreaming(context.Background(), "test", "you are helpful", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Hello from llama"
	if resp.Content != expected {
		t.Fatalf("expected content %q, got %q", expected, resp.Content)
	}
	if buf.String() != expected {
		t.Fatalf("expected streamed %q, got %q", expected, buf.String())
	}
}

func TestRunStreaming_CancelledContext(t *testing.T) {
	c := NewClient("test-model", nil)
	c.BaseURL = "http://127.0.0.1:1" // unreachable

	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.RunStreaming(ctx, "test", "", &buf)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "llm request failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_NonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "pong"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient("test-model", nil)
	c.BaseURL = srv.URL

	resp, err := c.Run(context.Background(), "ping", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "pong" {
		t.Fatalf("expected 'pong', got %q", resp.Content)
	}
}
