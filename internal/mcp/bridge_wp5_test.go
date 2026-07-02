package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestQuestionBridge_StaleAnswerDoesNotSatisfyNextQuestion is the WP5/J17
// gate proof: SendAnswer is a non-blocking send into a cap-1 buffer that is
// never drained when no question is pending. A stale/double-submitted
// answer that lands there before any question is asked must NOT be handed
// to the NEXT (unrelated) question — only a fresh answer whose ID matches
// that question's bridge-generated ID may satisfy it.
func TestQuestionBridge_StaleAnswerDoesNotSatisfyNextQuestion(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-stale-ans-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	// A decision submitted with no question open — the double-submit / dropped-
	// connection race J17 describes. Non-blocking: lands in the cap-1 buffer.
	bridge.SendAnswer(Answer{FreeformText: "STALE — must not be delivered"})

	done := make(chan error, 1)
	var gotAnswer Answer
	go func() {
		ans, err := sendQuestion(sockPath, "architect", ToolCall{Question: "real question?"})
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
		if question.Question != "real question?" {
			t.Fatalf("question = %q, want %q", question.Question, "real question?")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for question")
	}

	// Deliver the REAL answer, correlated to the question's bridge-generated ID.
	bridge.SendAnswer(Answer{ID: question.ID, FreeformText: "REAL"})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendQuestion: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never received an answer for the real question")
	}

	if gotAnswer.FreeformText != "REAL" {
		t.Errorf("answer = %+v, want FreeformText=%q — the stale pre-buffered answer must not "+
			"have satisfied this question (J17)", gotAnswer, "REAL")
	}
}
