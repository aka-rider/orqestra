package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
)

// bridgeEnvelope is the framed wire format for bridge messages.
// Kind is "question" or "report"; AgentID identifies the sending role.
type bridgeEnvelope struct {
	Kind    string          `json:"kind"`
	AgentID string          `json:"agent_id"`
	Payload json.RawMessage `json:"payload"`
}

// ReportSubmission holds a report delivered by an agent via SubmitReport.
type ReportSubmission struct {
	Report  string `json:"report"`
	Summary string `json:"summary,omitempty"`
}

// QuestionBridge listens on a Unix socket for questions from MCP bridge
// subprocesses and routes them to the orchestrator via channels.
//
// Flow: MCP server (subprocess) → Unix socket → QuestionBridge → channel → orchestrator → TUI
//
//	TUI answer → orchestrator → SendAnswer() → channel → QuestionBridge → socket → MCP server
//
// Lifetime: call Run(ctx) once; it blocks until ctx is cancelled and then cleans up.
type QuestionBridge struct {
	socketPath    string
	questions     chan ToolCall
	pendingAnswer chan Answer
	reportsMu     sync.Mutex
	reports       map[string]ReportSubmission
	reportWaiters map[string]chan struct{}
}

// NewQuestionBridge creates a bridge that will listen on the given socket path.
func NewQuestionBridge(socketPath string) *QuestionBridge {
	return &QuestionBridge{
		socketPath:    socketPath,
		questions:     make(chan ToolCall, 1),
		pendingAnswer: make(chan Answer, 1),
		reports:       make(map[string]ReportSubmission),
		reportWaiters: make(map[string]chan struct{}),
	}
}

// Run starts listening on the Unix socket and blocks until ctx is cancelled.
// It cleans up the socket on return. Safe to call sequentially for multiple runs.
func (b *QuestionBridge) Run(ctx context.Context) error {
	_ = os.Remove(b.socketPath) // fire-and-forget: stale socket from a prior run

	// Clear stale state so it does not leak into this run.
	b.reportsMu.Lock()
	b.reports = make(map[string]ReportSubmission)
	b.reportWaiters = make(map[string]chan struct{})
	b.reportsMu.Unlock()

	ln, err := net.Listen("unix", b.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", b.socketPath, err)
	}
	defer func() { _ = os.Remove(b.socketPath) }() // fire-and-forget: best-effort cleanup
	defer ln.Close()

	// Close listener when ctx is cancelled to unblock any in-progress ln.Accept().
	stopAfter := context.AfterFunc(ctx, func() { ln.Close() })
	defer stopAfter()

	b.acceptLoop(ctx, ln)
	return nil
}

func (b *QuestionBridge) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("question bridge accept error", "err", err)
				continue
			}
		}

		b.handleConnection(ctx, conn)
	}
}

func (b *QuestionBridge) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	data, err := readFrame(conn)
	if err != nil {
		slog.Debug("question bridge read error", "err", err)
		return
	}

	var env bridgeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Debug("question bridge envelope unmarshal error", "err", err)
		return
	}

	switch env.Kind {
	case "question":
		b.handleQuestion(ctx, conn, env)
	case "report":
		b.handleReport(conn, env)
	default:
		slog.Debug("question bridge unknown envelope kind", "kind", env.Kind)
	}
}

func (b *QuestionBridge) handleQuestion(ctx context.Context, conn net.Conn, env bridgeEnvelope) {
	var question ToolCall
	if err := json.Unmarshal(env.Payload, &question); err != nil {
		slog.Debug("question bridge question unmarshal error", "err", err)
		return
	}

	select {
	case b.questions <- question:
	case <-ctx.Done():
		return
	}

	var answer Answer
	select {
	case answer = <-b.pendingAnswer:
	case <-ctx.Done():
		answer = Answer{Skipped: true}
	}

	answerData, err := json.Marshal(answer)
	if err != nil {
		slog.Debug("question bridge marshal answer error", "err", err)
		return
	}
	if err := writeFrame(conn, answerData); err != nil {
		slog.Debug("question bridge write answer error", "err", err)
	}
}

func (b *QuestionBridge) handleReport(conn net.Conn, env bridgeEnvelope) {
	var sub ReportSubmission
	if err := json.Unmarshal(env.Payload, &sub); err != nil {
		slog.Debug("question bridge report unmarshal error", "err", err)
		return
	}
	b.reportsMu.Lock()
	b.reports[env.AgentID] = sub
	if ch, ok := b.reportWaiters[env.AgentID]; ok {
		close(ch)
		delete(b.reportWaiters, env.AgentID)
	}
	b.reportsMu.Unlock()

	ack, _ := json.Marshal(map[string]bool{"ok": true})
	if err := writeFrame(conn, ack); err != nil {
		slog.Debug("question bridge write report ack error", "err", err)
	}
}

// ReportSignal returns a channel that is closed when the given agent submits a
// report. If a report has already arrived, the returned channel is pre-closed.
// Callers must not close the returned channel.
func (b *QuestionBridge) ReportSignal(agentID string) <-chan struct{} {
	b.reportsMu.Lock()
	defer b.reportsMu.Unlock()
	if _, ok := b.reports[agentID]; ok {
		// Report already in hand — return a pre-closed channel.
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if ch, ok := b.reportWaiters[agentID]; ok {
		return ch
	}
	ch := make(chan struct{})
	b.reportWaiters[agentID] = ch
	return ch
}

// TakeReport returns and removes the report submitted by the given agent, if any.
func (b *QuestionBridge) TakeReport(agentID string) (string, bool) {
	b.reportsMu.Lock()
	defer b.reportsMu.Unlock()
	sub, ok := b.reports[agentID]
	if !ok {
		return "", false
	}
	delete(b.reports, agentID)
	return sub.Report, true
}

// SocketPath returns the Unix socket path for MCP config injection.
func (b *QuestionBridge) SocketPath() string {
	return b.socketPath
}

// Questions returns the channel that receives questions from MCP bridge subprocesses.
func (b *QuestionBridge) Questions() <-chan ToolCall {
	return b.questions
}

// SendAnswer delivers a user's answer back to the waiting MCP bridge subprocess.
// Non-blocking: drops the answer if no question is pending (bridge not running
// or connection already dropped).
func (b *QuestionBridge) SendAnswer(answer Answer) {
	select {
	case b.pendingAnswer <- answer:
	default:
	}
}
