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

// sendReport dials the bridge and sends a report envelope, returning the ack.
func sendReport(sockPath, agentID, report, summary string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	payload, _ := json.Marshal(struct {
		Report  string `json:"report"`
		Summary string `json:"summary"`
	}{Report: report, Summary: summary})
	env := bridgeEnvelope{Kind: "report", AgentID: agentID, Payload: payload}
	envData, _ := json.Marshal(env)
	if err := writeFrame(conn, envData); err != nil {
		return err
	}
	_, err = readFrame(conn) // ack
	return err
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

func TestQuestionBridge_ReportRoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-report-rt-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	defer bridge.Stop()

	done := make(chan error, 1)
	go func() {
		done <- sendReport(sockPath, "researcher", "## My findings\n- fact 1", "research done")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendReport error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for report ack")
	}

	r, ok := bridge.TakeReport("researcher")
	if !ok {
		t.Fatal("TakeReport returned ok=false, expected report")
	}
	if r.Report != "## My findings\n- fact 1" {
		t.Errorf("report = %q, want '## My findings...'", r.Report)
	}
	if r.Summary != "research done" {
		t.Errorf("summary = %q, want 'research done'", r.Summary)
	}
	if r.AgentID != "researcher" {
		t.Errorf("agentID = %q, want 'researcher'", r.AgentID)
	}

	// TakeReport is delete-on-take
	_, ok = bridge.TakeReport("researcher")
	if ok {
		t.Error("second TakeReport should return ok=false (delete-on-take)")
	}
}

func TestQuestionBridge_TakeReportDeleteOnTake(t *testing.T) {
	bridge := NewQuestionBridge("/tmp/x.sock")
	bridge.putReport("architect", ReportSubmission{AgentID: "architect", Report: "r1"})

	r, ok := bridge.TakeReport("architect")
	if !ok || r.Report != "r1" {
		t.Errorf("first take: ok=%v report=%q", ok, r.Report)
	}
	_, ok = bridge.TakeReport("architect")
	if ok {
		t.Error("second take should return ok=false")
	}
}
