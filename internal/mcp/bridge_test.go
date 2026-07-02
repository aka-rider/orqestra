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
	runBridge(t, ctx, bridge, sockPath)

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

func TestQuestionBridge_TakeReport_ByAgentID_AfterSessionRegistered(t *testing.T) {
	// Regression for the architect "no valid report produced" race: the report is
	// stored under the session-id key (handleReport looks up sessions[agentID]),
	// and harvesting must succeed when keyed by agentID alone — even though the
	// caller's RunResult.SessionID is empty after an early stop.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sess-corr-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "architect"
	const sessionID = "sess-real-xyz"
	const reportText = "## Goal\nShip it.\n## Work Packages\n- one"

	// Supervisor observed EventSessionStart and re-armed the slot with the real id.
	bridge.RegisterSession(agentID, sessionID)

	// ReportSignal keyed by agentID must arm the waiter for the session key.
	sig := bridge.ReportSignal(agentID)

	if err := sendReport(sockPath, agentID, reportText, ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("ReportSignal(agentID) did not fire after a report stored under the session key")
	}

	// Harvest by agentID — resolves sessions[agentID] internally; no RunResult.SessionID needed.
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected TakeReport(agentID) to find the session-keyed report")
	}
	if got != reportText {
		t.Errorf("report = %q, want %q", got, reportText)
	}
}

func TestQuestionBridge_RegisterSessionClearsStaleReport(t *testing.T) {
	// Run() no longer clears stale state. RegisterSession does.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-stale-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// Deliver report for "critic" and let it sit (simulate cancelled-before-TakeReport run).
	if err := sendReport(sockPath, "critic", "stale report", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}
	// Verify it's there.
	if _, ok := bridge.TakeReport("critic"); !ok {
		t.Fatal("expected report to be stored initially")
	}

	// Simulate the next run: deliver again without taking, then RegisterSession.
	if err := sendReport(sockPath, "critic", "stale report 2", ""); err != nil {
		t.Fatalf("sendReport 2: %v", err)
	}

	// RegisterSession (no sessionID → agentID key) must clear the stale report.
	bridge.RegisterSession("critic", "")

	_, ok := bridge.TakeReport("critic")
	if ok {
		t.Error("expected stale report to be cleared by RegisterSession")
	}
}

func TestReportSignal_OpenBeforeDelivery(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-open-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	sig := bridge.ReportSignal("architect")

	// Signal must be open (not yet closed) before delivery.
	select {
	case <-sig:
		t.Fatal("signal should not be closed before report delivery")
	default:
	}

	// Deliver the report.
	if err := sendReport(sockPath, "architect", "# Plan\n## Goal\nTest.", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	// Signal must close promptly after delivery.
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("signal did not close after report delivery")
	}
}

func TestReportSignal_PreClosedWhenReportExists(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-pre-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// Deliver the report first.
	if err := sendReport(sockPath, "researcher", "## Goal\nTest.", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	// ReportSignal called after delivery must return a pre-closed channel.
	sig := bridge.ReportSignal("researcher")
	select {
	case <-sig:
	default:
		t.Fatal("signal should be pre-closed when report already exists")
	}
}

func TestReportSignal_ReArmedAfterTakeReport(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-rearm-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// First delivery.
	if err := sendReport(sockPath, "architect", "# Plan\n## Goal\nFirst.", ""); err != nil {
		t.Fatalf("sendReport 1: %v", err)
	}
	bridge.TakeReport("architect") // consume first report

	// Re-arm: signal for a second delivery should be open again.
	sig2 := bridge.ReportSignal("architect")
	select {
	case <-sig2:
		t.Fatal("re-armed signal should be open before second delivery")
	default:
	}

	// Second delivery closes the re-armed signal.
	if err := sendReport(sockPath, "architect", "# Plan\n## Goal\nSecond.", ""); err != nil {
		t.Fatalf("sendReport 2: %v", err)
	}
	select {
	case <-sig2:
	case <-time.After(time.Second):
		t.Fatal("re-armed signal did not close after second delivery")
	}
}

func TestReportSignal_ReArmedAfterRegisterSession(t *testing.T) {
	// RegisterSession (not Run) is the re-arm trigger.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-restart-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// Subscribe before the first run completes — simulates a run that was cancelled
	// before the agent ever submitted a report.
	sigBefore := bridge.ReportSignal("architect")
	select {
	case <-sigBefore:
		t.Fatal("signal should be open before delivery")
	default:
	}

	// RegisterSession re-arms the slot: sigBefore is orphaned (never closed; the old
	// supervisor goroutine already exited when the run was cancelled).
	bridge.RegisterSession("architect", "")

	// New subscription must return a fresh open channel.
	sigAfter := bridge.ReportSignal("architect")
	select {
	case <-sigAfter:
		t.Fatal("re-armed signal after RegisterSession should be open, not pre-closed")
	default:
	}

	// sigBefore is still open — it was orphaned, not closed.
	select {
	case <-sigBefore:
		t.Fatal("orphaned signal should remain open after RegisterSession")
	default:
	}

	// Delivery on the new run closes sigAfter.
	if err := sendReport(sockPath, "architect", "# Plan\n## Goal\nAfter restart.", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}
	select {
	case <-sigAfter:
	case <-time.After(time.Second):
		t.Fatal("re-armed signal did not close after delivery")
	}
	// sigBefore must remain open.
	select {
	case <-sigBefore:
		t.Fatal("orphaned signal must not fire on second run's delivery")
	default:
	}
}

func TestReportSignal_TwoDeliveriesSameAgentNoPanic(t *testing.T) {
	// Two SubmitReport calls for the same agentID across a TakeReport must not
	// double-close the waiter channel (which would panic).
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-dup-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// Subscribe before first delivery.
	sig := bridge.ReportSignal("critic")

	// First delivery — closes the waiter.
	if err := sendReport(sockPath, "critic", "## Critic Report\n### Blockers Found\nNone.", ""); err != nil {
		t.Fatalf("sendReport 1: %v", err)
	}
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("signal did not close after first delivery")
	}
	bridge.TakeReport("critic")

	// Second delivery must not panic (no open waiter to double-close).
	if err := sendReport(sockPath, "critic", "## Critic Report\n### Blockers Found\nNone again.", ""); err != nil {
		t.Fatalf("sendReport 2: %v", err)
	}
	// Verify second report is stored correctly.
	got, ok := bridge.TakeReport("critic")
	if !ok {
		t.Fatal("expected second report to be stored")
	}
	if got == "" {
		t.Error("second report text is empty")
	}
}
