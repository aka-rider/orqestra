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

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

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

	bridge.SendAnswer(Answer{
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

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

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

	bridge.SendAnswer(Answer{FreeformText: "Alice"})

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

// sendReport dials the bridge and sends a report envelope, returning the ack.
func sendReport(sockPath, agentID, report, summary string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	payload, _ := json.Marshal(ReportSubmission{Report: report, Summary: summary})
	env := bridgeEnvelope{Kind: "report", AgentID: agentID, Payload: payload}
	envData, _ := json.Marshal(env)
	if err := writeFrame(conn, envData); err != nil {
		return err
	}
	ackData, err := readFrame(conn)
	if err != nil {
		return err
	}
	var ack struct{ OK bool `json:"ok"` }
	return json.Unmarshal(ackData, &ack)
}

func TestQuestionBridge_ReportRoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-report-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

	const agentID = "researcher"
	const reportText = "## Goal\nTest report.\n## Codebase Facts\n- fact1"

	if err := sendReport(sockPath, agentID, reportText, "test summary"); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	// TakeReport should return the report and remove it.
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected TakeReport to return true")
	}
	if got != reportText {
		t.Errorf("report = %q, want %q", got, reportText)
	}

	// Second call should return nothing.
	_, ok = bridge.TakeReport(agentID)
	if ok {
		t.Error("expected TakeReport to return false on second call")
	}
}

func TestQuestionBridge_StartClearsStaleReport(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-stale-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("first bridge start: %v", err)
	}

	if err := sendReport(sockPath, "critic", "stale report", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	bridge.Stop()

	// Second Start should clear the stale report.
	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("second bridge start: %v", err)
	}
	defer bridge.Stop()

	_, ok := bridge.TakeReport("critic")
	if ok {
		t.Error("expected stale report to be cleared on second Start")
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

	cancel()
	bridge.Stop()

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

