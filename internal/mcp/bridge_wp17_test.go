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

// bridge_wp17_test.go covers the WP17 cross-run lifecycle hardening gates:
// zombie-question answer theft (F2), a hung peer's connection being torn
// down by deadline and its goroutine joined (A7).

// TestQuestionBridge_ZombieQuestionNeverStealsNextAnswer is the WP17/F2 QA
// gate: a question whose connection dies (the agent process was killed)
// while unanswered must never consume an answer meant for a LATER, live
// question.
//
// RED-first proof (quoted verbatim in the WP17 report): against the
// pre-fix shared cap-1 pendingAnswer design (both the zombie's and the
// live question's handleQuestion goroutines competing to receive from the
// SAME channel), this test is flaky-to-reliably-failing depending on Go's
// channel receiver scheduling — since the zombie has been blocked longest,
// it wins the race for the answer meant for the live question, discovers
// the ID mismatch, logs it, and discards it; the live question's own
// sendQuestion call never returns, and this test times out. The per-question
// waiter map (the real fix) makes theft structurally impossible: SendAnswer
// routes directly to the live question's own channel, so there is nothing
// left for the zombie to steal.
func TestQuestionBridge_ZombieQuestionNeverStealsNextAnswer(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-zombie-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// The zombie: its connection dies (simulating the agent process being
	// killed) before it is ever answered.
	conn1, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial (zombie): %v", err)
	}
	payload1, _ := json.Marshal(ToolCall{Question: "zombie question"})
	env1 := bridgeEnvelope{Kind: "question", AgentID: "architect", Payload: payload1}
	envData1, _ := json.Marshal(env1)
	if err := writeFrame(conn1, envData1); err != nil {
		t.Fatalf("write (zombie): %v", err)
	}

	select {
	case <-bridge.Questions():
	case <-ctx.Done():
		t.Fatal("timeout waiting for the zombie question to reach the bridge")
	}
	// Kill the zombie's connection: the agent died mid-question.
	if err := conn1.Close(); err != nil {
		t.Fatalf("close zombie conn: %v", err)
	}

	// The real, live question — asked and answered normally.
	done := make(chan error, 1)
	var gotAnswer Answer
	go func() {
		ans, sendErr := sendQuestion(sockPath, "worker", ToolCall{Question: "real question"})
		if sendErr != nil {
			done <- sendErr
			return
		}
		gotAnswer = ans
		done <- nil
	}()

	var q2 ToolCall
	select {
	case q2 = <-bridge.Questions():
	case <-ctx.Done():
		t.Fatal("timeout waiting for the real question")
	}

	bridge.SendAnswer(Answer{ID: q2.ID, FreeformText: "REAL ANSWER"})

	select {
	case sendErr := <-done:
		if sendErr != nil {
			t.Fatalf("sendQuestion (real): %v", sendErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the real question's answer never arrived — stolen by the zombie question's handler (F2)")
	}

	if gotAnswer.FreeformText != "REAL ANSWER" {
		t.Errorf("answer = %+v, want FreeformText=%q — a zombie question's handler consumed the real answer (F2)", gotAnswer, "REAL ANSWER")
	}
}

// TestQuestionBridge_HungPeerTornDownByDeadline is the WP17/A7 QA gate: a
// connection that completes its dial but never finishes sending a frame
// must be torn down by the read deadline rather than leaking the
// connection (and its handling goroutine) past Run's return.
//
// RED-first proof (quoted verbatim in the WP17 report): with readFrame
// given no deadline (the pre-fix shape), this test hangs until its own
// bounded wait fails — bridge.Run(ctx) never returns within the grace
// period because acceptLoop's per-connection goroutine for the hung peer
// is still blocked in io.ReadFull, and Run's new connWG.Wait() (A7's other
// half) can't proceed either. Giving readFrame a deadline (the real fix)
// makes the connection — and Run itself once ctx is cancelled — unblock
// promptly.
func TestQuestionBridge_HungPeerTornDownByDeadline(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-hung-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)
	// White-box: shrink the frame deadline so this test does not need to
	// wait out the real 30s production bound (connFrameTimeout) — set
	// BEFORE Run() is called, so there is no concurrent-mutation window
	// (every reader of frameTimeout is spawned by Run/acceptLoop).
	bridge.frameTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runDone := runBridge(t, ctx, bridge, sockPath)

	// Dial and connect, but never send a single byte — a hung peer that
	// completed its TCP/Unix handshake and then stalled.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The hung connection must be torn down (readFrameDeadline fires) well
	// within the frame deadline plus scheduling slack — proven indirectly:
	// the bridge must still shut down cleanly and promptly once ctx is
	// cancelled, which requires connWG.Wait() (A7) to have already joined
	// this connection's handler goroutine. If the goroutine were still
	// blocked in an undead-lined io.ReadFull, Run would never return.
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge.Run did not return after ctx cancellation — the hung peer's connection handler leaked past the frame deadline (A7)")
	}
}
