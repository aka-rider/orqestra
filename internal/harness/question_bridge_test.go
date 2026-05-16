package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQuestionBridge_RoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-rt-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

	// Verify socket file exists
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}

	// Simulate MCP server: dial, send question, read answer
	done := make(chan error, 1)
	var gotAnswer MCPAnswer

	go func() {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		question := MCPToolCall{
			Question: "What color do you prefer?",
			Options: []MCPToolOption{
				{Label: "Red", Hint: "warm color"},
				{Label: "Blue", Hint: "cool color"},
			},
		}
		payload, _ := json.Marshal(question)
		if err := writeFrame(conn, payload); err != nil {
			done <- err
			return
		}

		answerData, err := readFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if err := json.Unmarshal(answerData, &gotAnswer); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	// Read question from bridge
	select {
	case q := <-bridge.Questions():
		if q.Question != "What color do you prefer?" {
			t.Errorf("question = %q, want 'What color do you prefer?'", q.Question)
		}
		if len(q.Options) != 2 {
			t.Errorf("options count = %d, want 2", len(q.Options))
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for question")
	}

	// Send answer
	bridge.SendAnswer(MCPAnswer{
		SelectedIndices: []int{1},
	})

	// Wait for mock MCP server to finish
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mock MCP server error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock MCP server")
	}

	// Verify answer
	if len(gotAnswer.SelectedIndices) != 1 || gotAnswer.SelectedIndices[0] != 1 {
		t.Errorf("answer = %+v, want selected index 1", gotAnswer)
	}
}

func TestQuestionBridge_FreeformRoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-free-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

	done := make(chan error, 1)
	var gotAnswer MCPAnswer

	go func() {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		question := MCPToolCall{Question: "What is your name?"}
		payload, _ := json.Marshal(question)
		if err := writeFrame(conn, payload); err != nil {
			done <- err
			return
		}

		answerData, err := readFrame(conn)
		if err != nil {
			done <- err
			return
		}
		json.Unmarshal(answerData, &gotAnswer)
		done <- nil
	}()

	select {
	case q := <-bridge.Questions():
		if q.Question != "What is your name?" {
			t.Errorf("question = %q", q.Question)
		}
		if len(q.Options) != 0 {
			t.Errorf("expected no options, got %d", len(q.Options))
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}

	bridge.SendAnswer(MCPAnswer{FreeformText: "Alice"})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}

	if gotAnswer.FreeformText != "Alice" {
		t.Errorf("freeform = %q, want Alice", gotAnswer.FreeformText)
	}
}

func TestQuestionBridge_ContextCancellation(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-cancel-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithCancel(context.Background())

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}

	// Cancel immediately
	cancel()
	bridge.Stop()

	// Socket should be cleaned up
	socketRemoved := false
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			socketRemoved = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !socketRemoved {
		t.Error("socket file should be removed after stop")
	}
}
