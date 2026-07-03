package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
)

// handleQuestion forwards one AskUserQuestion to Questions() and blocks
// until an answer arrives for it, the connection dies, or ctx is cancelled.
//
// WP17/F2,A7: three independent ways this can end, all handled without ever
// touching another question's state:
//  1. SendAnswer(answer) with answer.ID == question.ID arrives — the normal
//     path, routed directly to this question's own waiter channel.
//  2. The peer (the MCP subprocess) disconnects before answering — detected
//     by connDead below — nothing to write back to, so this simply exits.
//  3. ctx (the bridge's whole-session Run context) is cancelled — the
//     process is shutting down; answer with Skipped so the subprocess's
//     tool-call at least returns instead of hanging.
func (b *QuestionBridge) handleQuestion(ctx context.Context, conn net.Conn, env bridgeEnvelope) {
	var question ToolCall
	if err := json.Unmarshal(env.Payload, &question); err != nil {
		slog.Debug("question bridge question unmarshal error", "err", err)
		return
	}
	// Generate a unique ID BEFORE forwarding (WP5/J17,J25): this is the only
	// question this call will ever accept an answer for.
	question.ID = fmt.Sprintf("q-%d", b.questionSeq.Add(1))

	answerCh := make(chan Answer, 1)
	b.registerWaiter(question.ID, answerCh)
	defer b.releaseWaiter(question.ID)

	select {
	case b.questions <- question:
	case <-ctx.Done():
		return
	}

	// WP17/F2: detect the peer disconnecting (its process died, its
	// connection reset, etc.) while we wait for an answer. The MCP
	// subprocess client (server.go's sendQuestionToBridge) never sends
	// anything more on this connection after the question — it goes
	// straight to blocking on the answer frame — so a blocking
	// zero-length-buffer Read here only ever returns for one of two
	// reasons: the peer is genuinely gone (EOF/reset), or THIS handler's
	// own teardown (handleConnection's deferred conn.Close(), which runs
	// immediately after handleQuestion returns via any other branch) broke
	// the read. Either way it means "stop waiting on this connection" —
	// its own lifetime already bounds this goroutine; nothing further to
	// track or join beyond the connWG entry acceptLoop already holds for
	// the surrounding handleConnection call.
	connDead := make(chan struct{})
	go func() {
		defer close(connDead)
		var buf [1]byte
		_, _ = conn.Read(buf[:]) // fire-and-forget: any return means "stop waiting for an answer on this conn"
	}()

	var answer Answer
	select {
	case answer = <-answerCh:
	case <-ctx.Done():
		answer = Answer{ID: question.ID, Skipped: true}
	case <-connDead:
		// The peer disconnected before answering. Nothing left to write an
		// answer back to, and — critically — this exits WITHOUT ever
		// touching pendingAnswer/waiters for any OTHER question: the
		// per-question waiter map means there is nothing shared to steal
		// or corrupt (F2).
		return
	}

	answerData, err := json.Marshal(answer)
	if err != nil {
		slog.Debug("question bridge marshal answer error", "err", err)
		return
	}
	if err := writeFrameDeadline(conn, answerData, b.frameTimeout); err != nil {
		slog.Debug("question bridge write answer error", "err", err)
	}
}

// registerWaiter records the dedicated answer channel for an in-flight
// question, keyed by its ID (WP17/F2).
func (b *QuestionBridge) registerWaiter(id string, ch chan Answer) {
	b.waitersMu.Lock()
	b.waiters[id] = ch
	b.waitersMu.Unlock()
}

// releaseWaiter removes a question's waiter entry once its handleQuestion
// call is returning, on any exit path — a no-op if already removed (there
// is no other remover, but this stays defensive/idempotent).
func (b *QuestionBridge) releaseWaiter(id string) {
	b.waitersMu.Lock()
	delete(b.waiters, id)
	b.waitersMu.Unlock()
}

// SendAnswer delivers a user's answer to the ONE question it echoes (by
// ID) — see the waiters field doc for why per-question routing replaced a
// single shared slot. A stale or unknown ID (no in-flight question
// currently registered under it — a double-submit, a bridge restart, or an
// answer for a question that already timed out and released its waiter) is
// dropped HERE, at the router, logged — it can never reach some other,
// unrelated pending question (WP17/F2: the "theft" class is now
// structurally impossible, not merely unlikely).
func (b *QuestionBridge) SendAnswer(answer Answer) {
	b.waitersMu.Lock()
	ch, ok := b.waiters[answer.ID]
	b.waitersMu.Unlock()

	if !ok {
		slog.Debug("question bridge: dropped answer for an unknown/stale question id", "id", answer.ID)
		return
	}
	select {
	case ch <- answer:
	default: // fire-and-forget: a double-submit for the same question — the first answer already occupies the cap-1 channel, drop the second
	}
}
