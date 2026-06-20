package mcp

import (
	"context"
	"encoding/json"
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
type QuestionBridge struct {
	socketPath    string
	listener      net.Listener
	questions     chan ToolCall
	pendingAnswer chan Answer
	done          chan struct{}
	mu            sync.Mutex
	stopped       bool
	reportsMu     sync.Mutex
	reports       map[string]ReportSubmission
}

// NewQuestionBridge creates a bridge that will listen on the given socket path.
func NewQuestionBridge(socketPath string) *QuestionBridge {
	return &QuestionBridge{
		socketPath:    socketPath,
		questions:     make(chan ToolCall, 1),
		pendingAnswer: make(chan Answer, 1),
		done:          make(chan struct{}),
		reports:       make(map[string]ReportSubmission),
	}
}

// Start creates the Unix socket and begins accepting connections.
// Each connection handles exactly one question/answer or report exchange.
// Start is safe to call after Stop — the bridge resets its internal state.
func (b *QuestionBridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.stopped {
		// Reset for reuse after a previous Stop
		b.done = make(chan struct{})
		b.stopped = false
	}
	b.mu.Unlock()

	// Clear any reports left by a cancelled run so they don't leak into this run.
	b.reportsMu.Lock()
	b.reports = make(map[string]ReportSubmission)
	b.reportsMu.Unlock()

	os.Remove(b.socketPath) // fire-and-forget: may not exist

	ln, err := net.Listen("unix", b.socketPath)
	if err != nil {
		return err
	}

	// Capture done under mu so the spawned goroutine holds a reference to the
	// channel valid for this run — a second Start() may replace b.done, and the
	// old goroutine must not race on the field.
	b.mu.Lock()
	b.listener = ln
	done := b.done
	b.mu.Unlock()

	go b.acceptLoop(ctx, ln, done)
	return nil
}

func (b *QuestionBridge) acceptLoop(ctx context.Context, ln net.Listener, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		default:
		}

		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			default:
				slog.Debug("question bridge accept error", "err", err)
				continue
			}
		}

		b.handleConnection(ctx, conn, done)
	}
}

func (b *QuestionBridge) handleConnection(ctx context.Context, conn net.Conn, done <-chan struct{}) {
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
		b.handleQuestion(ctx, conn, env, done)
	case "report":
		b.handleReport(conn, env)
	default:
		slog.Debug("question bridge unknown envelope kind", "kind", env.Kind)
	}
}

func (b *QuestionBridge) handleQuestion(ctx context.Context, conn net.Conn, env bridgeEnvelope, done <-chan struct{}) {
	var question ToolCall
	if err := json.Unmarshal(env.Payload, &question); err != nil {
		slog.Debug("question bridge question unmarshal error", "err", err)
		return
	}

	select {
	case b.questions <- question:
	case <-ctx.Done():
		return
	case <-done:
		return
	}

	var answer Answer
	select {
	case answer = <-b.pendingAnswer:
	case <-ctx.Done():
		answer = Answer{Skipped: true}
	case <-done:
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
	b.reportsMu.Unlock()

	ack, _ := json.Marshal(map[string]bool{"ok": true})
	if err := writeFrame(conn, ack); err != nil {
		slog.Debug("question bridge write report ack error", "err", err)
	}
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

// Stop closes the bridge and cleans up the socket file.
// Safe to call multiple times.
func (b *QuestionBridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.stopped = true
	close(b.done)
	if b.listener != nil {
		b.listener.Close()
	}
	os.Remove(b.socketPath) // fire-and-forget: best-effort cleanup
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
func (b *QuestionBridge) SendAnswer(answer Answer) {
	select {
	case b.pendingAnswer <- answer:
	case <-b.done:
	}
}
