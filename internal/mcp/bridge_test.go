package mcp

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

// bridge_test.go holds the core question round-trip suite. Report/nonce
// correlation tests live in bridge_report_test.go (WP17/A8: split out once
// this file grew past 500 lines); zombie-question/conn-death/hung-peer
// coverage lives in bridge_wp17_test.go.

// awaitSocket polls until the socket file at path exists or ctx is done.
func awaitSocket(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("socket %s did not appear before deadline", path)
		case <-tick.C:
		}
	}
}

// runBridge launches bridge.Run(ctx) in a goroutine and waits until the socket is ready.
// Returns a channel that closes when Run() exits (including its deferred cleanup).
func runBridge(t *testing.T, ctx context.Context, bridge *QuestionBridge, sockPath string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = bridge.Run(ctx)
		close(done)
	}()
	awaitSocket(t, ctx, sockPath)
	return done
}

// sendQuestion dials the bridge and sends a question envelope, returning the answer.
func sendQuestion(sockPath, agentID string, q ToolCall) (Answer, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return Answer{}, err
	}
	defer conn.Close()

	payload, _ := json.Marshal(q)
	env := bridgeEnvelope{Kind: "question", AgentID: agentID, Payload: payload}
	envData, _ := json.Marshal(env)
	if err := writeFrame(conn, envData); err != nil {
		return Answer{}, err
	}
	data, err := readFrame(conn)
	if err != nil {
		return Answer{}, err
	}
	var ans Answer
	return ans, json.Unmarshal(data, &ans)
}

func TestQuestionBridge_RoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-rt-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}

	done := make(chan error, 1)
	var gotAnswer Answer

	go func() {
		question := ToolCall{
			Question: "What color do you prefer?",
			Options: []ToolOption{
				{Label: "Red", Hint: "warm color"},
				{Label: "Blue", Hint: "cool color"},
			},
		}
		ans, err := sendQuestion(sockPath, "researcher", question)
		if err != nil {
			done <- err
			return
		}
		gotAnswer = ans
		done <- nil
	}()

	var question ToolCall
	select {
	case question = <-bridge.Questions():
		if question.Question != "What color do you prefer?" {
			t.Errorf("question = %q, want 'What color do you prefer?'", question.Question)
		}
		if len(question.Options) != 2 {
			t.Errorf("options count = %d, want 2", len(question.Options))
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for question")
	}

	bridge.SendAnswer(Answer{
		ID:              question.ID,
		SelectedIndices: []int{1},
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mock MCP server error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock MCP server")
	}

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
	runBridge(t, ctx, bridge, sockPath)

	done := make(chan error, 1)
	var gotAnswer Answer

	go func() {
		ans, err := sendQuestion(sockPath, "architect", ToolCall{Question: "What is your name?"})
		if err != nil {
			done <- err
			return
		}
		gotAnswer = ans
		done <- nil
	}()

	var question ToolCall
	select {
	case question = <-bridge.Questions():
		if question.Question != "What is your name?" {
			t.Errorf("question = %q", question.Question)
		}
		if len(question.Options) != 0 {
			t.Errorf("expected no options, got %d", len(question.Options))
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}

	bridge.SendAnswer(Answer{ID: question.ID, FreeformText: "Alice"})

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
