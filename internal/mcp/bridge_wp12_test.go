package mcp

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWP12_ReportDeliveredWhileQuestionPending is the WP12/J16 gate (a): a
// SubmitReport must be deliverable while a question is pending on the same
// bridge, over the real Unix socket. Before WP12, handleConnection ran
// inline in the accept loop and handleQuestion blocked until answered — a
// concurrent SubmitReport would queue behind it in the socket backlog (see
// the RED-first proof quoted in the WP12 report).
func TestWP12_ReportDeliveredWhileQuestionPending(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-wp12-a-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// Ask a question but never answer it — it blocks handleQuestion until
	// ctx is done. With per-connection goroutines (WP12), this must not
	// delay any other connection.
	askDone := make(chan error, 1)
	go func() {
		_, err := sendQuestion(sockPath, "architect", ToolCall{Question: "pending, never answered"})
		askDone <- err
	}()

	var pending ToolCall
	select {
	case pending = <-bridge.Questions():
	case <-time.After(2 * time.Second):
		t.Fatal("question never reached the bridge")
	}

	reportDone := make(chan error, 1)
	go func() {
		reportDone <- sendReport(sockPath, "worker", "## Report\nDone.", "")
	}()

	select {
	case err := <-reportDone:
		if err != nil {
			t.Fatalf("sendReport failed: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("SubmitReport was blocked behind the pending question (J16)")
	}

	// Clean up the still-pending question so its goroutine exits promptly.
	bridge.SendAnswer(Answer{ID: pending.ID, Skipped: true})
	select {
	case <-askDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pending question goroutine never unblocked")
	}
}

// TestWP12_DialNeverRefusedAfterReady is the WP12/J36 gate (b): dialing
// immediately after Ready() closes must never see ECONNREFUSED, across 100
// iterations — the exact pattern a bridge starter (tui.Run) plus an agent
// dialing the socket must be able to rely on.
func TestWP12_DialNeverRefusedAfterReady(t *testing.T) {
	const iterations = 100
	for i := 0; i < iterations; i++ {
		sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-wp12-b-%d-%d.sock", os.Getpid(), i))
		bridge := NewQuestionBridge(sockPath)

		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = bridge.Run(ctx) }()

		<-bridge.Ready()

		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			cancel()
			os.Remove(sockPath)
			t.Fatalf("iteration %d: dial after Ready() failed: %v", i, err)
		}
		conn.Close()
		cancel()
		os.Remove(sockPath)
	}
}
