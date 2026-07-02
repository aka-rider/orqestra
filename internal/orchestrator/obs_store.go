package orchestrator

import (
	"sync"
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

// ObsSnapshot is a point-in-time copy of the run state. Pure value — no
// aliasing across goroutines. TUI reads this from a tick or notify wakeup.
type ObsSnapshot struct {
	Phase        Phase
	Agents       []AgentSnapshot
	Gate         GateRequest
	HasGate      bool
	UserQuestion mcp.ToolCall
	HasQuestion  bool
	Terminal     TerminalState
	Rev          uint64
}

// TerminalState captures the final outcome once the pipeline completes.
type TerminalState struct {
	Done   bool
	Result Result
	Err    error
}

// ObsStore is the shared observation snapshot. The pipeline writes non-blocking
// updates; the TUI reads value snapshots. The pipeline can never block on the UI.
type ObsStore struct {
	mu          sync.Mutex
	phase       Phase
	agents      map[AgentID]*agentEntry
	agentIDs    []AgentID // insertion order
	ring        *StreamRing
	gate        GateRequest
	hasGate     bool
	question    mcp.ToolCall
	hasQuestion bool
	terminal    TerminalState
	rev         uint64
	notify      chan struct{}      // capacity-1 coalescing wake signal
	streamCh    chan StreamEntry   // non-blocking stream relay to TUI frame list
}

type agentEntry struct {
	snapshot AgentSnapshot
}

// NewObsStore creates an ObsStore with an empty state.
func NewObsStore() *ObsStore {
	return &ObsStore{
		agents:   make(map[AgentID]*agentEntry),
		ring:     NewStreamRing(defaultRingCapacity),
		notify:   make(chan struct{}, 1),
		streamCh: make(chan StreamEntry, 512),
	}
}

// StreamCh returns the non-blocking stream relay channel. The TUI drains this
// on each tick to update the frame list. Writes are best-effort (dropped if full).
func (s *ObsStore) StreamCh() <-chan StreamEntry { return s.streamCh }

// NotifyCh returns the coalescing wake channel. TUI selects on it alongside
// its tick timer. A send is non-blocking; multiple rapid writes coalesce.
func (s *ObsStore) NotifyCh() <-chan struct{} { return s.notify }

// poke sends a non-blocking wake signal.
func (s *ObsStore) poke() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Snapshot returns a point-in-time copy of the current state.
func (s *ObsStore) Snapshot() ObsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := make([]AgentSnapshot, 0, len(s.agentIDs))
	for _, id := range s.agentIDs {
		if e, ok := s.agents[id]; ok {
			agents = append(agents, e.snapshot)
		}
	}

	return ObsSnapshot{
		Phase:        s.phase,
		Agents:       agents,
		Gate:         s.gate,
		HasGate:      s.hasGate,
		UserQuestion: s.question,
		HasQuestion:  s.hasQuestion,
		Terminal:     s.terminal,
		Rev:          s.rev,
	}
}

// UserQuestion stores an incoming MCP user question in the snapshot.
func (s *ObsStore) UserQuestion(q mcp.ToolCall) {
	s.mu.Lock()
	s.question = q
	s.hasQuestion = true
	s.rev++
	s.mu.Unlock()
	s.poke()
}

// ClearQuestion removes the pending user question (called after the TUI answers it).
func (s *ObsStore) ClearQuestion() {
	s.mu.Lock()
	s.question = mcp.ToolCall{}
	s.hasQuestion = false
	s.rev++
	s.mu.Unlock()
	s.poke()
}

// --- Observer interface implementation ---

func (s *ObsStore) PhaseChanged(p Phase) {
	s.mu.Lock()
	s.phase = p
	s.rev++
	s.mu.Unlock()
	s.poke()
}

func (s *ObsStore) AgentStarted(id AgentID, meta AgentMeta) {
	s.mu.Lock()
	if _, ok := s.agents[id]; !ok {
		s.agentIDs = append(s.agentIDs, id)
	}
	s.agents[id] = &agentEntry{snapshot: AgentSnapshot{
		AgentID:   string(id),
		Meta:      meta,
		Status:    "running",
		StartTime: time.Now(),
	}}
	s.rev++
	s.mu.Unlock()
	s.ring.SetAgent(string(id))
	s.poke()
}

func (s *ObsStore) AgentDone(id AgentID, usage harness.TokenUsage) {
	s.mu.Lock()
	if e, ok := s.agents[id]; ok {
		e.snapshot.Status = "done"
		e.snapshot.Input = usage.Input
		e.snapshot.Output = usage.Output
		e.snapshot.EndTime = time.Now()
		e.snapshot.CallCount++
	}
	s.rev++
	s.mu.Unlock()
	s.poke()
}

func (s *ObsStore) AgentFailed(id AgentID, err error) {
	s.mu.Lock()
	if e, ok := s.agents[id]; ok {
		e.snapshot.Status = "failed"
		e.snapshot.EndTime = time.Now()
		if err != nil {
			e.snapshot.Error = err.Error()
		}
	}
	s.rev++
	s.mu.Unlock()
	s.poke()
}

func (s *ObsStore) Stream(_ AgentID, ev harness.Event) {
	// Convert harness.Event → StreamEntry and append to the ring.
	var entry StreamEntry
	switch {
	case ev.IsDelta:
		entry = StreamEntry{Kind: EntryDelta, Text: ev.Text}
	case ev.Text != "":
		entry = StreamEntry{Kind: EntryText, Text: ev.Text}
	case ev.Tool != "":
		entry = StreamEntry{Kind: EntryToolUse, Tool: ev.Tool, Detail: ev.Detail}
	case ev.Kind == harness.EventToolResult:
		entry = StreamEntry{Kind: EntryToolResult, ToolErr: ev.IsError}
	case ev.Kind == harness.EventUsage:
		entry = StreamEntry{Kind: EntryStats, Stats: StreamStats{Input: ev.Input, Output: ev.Output, Valid: true}}
	default:
		return
	}
	s.ring.Append(entry)
	select {
	case s.streamCh <- entry:
	default: // TUI is slow; drop rather than block the pipeline
	}
	s.poke()
}

func (s *ObsStore) GateOpened(req GateRequest) {
	s.mu.Lock()
	s.gate = req
	s.hasGate = true
	s.rev++
	s.mu.Unlock()
	s.poke()
}

func (s *ObsStore) GateClosed() {
	s.mu.Lock()
	s.hasGate = false
	s.gate = GateRequest{}
	s.rev++
	s.mu.Unlock()
	s.poke()
}

func (s *ObsStore) Finished(res Result, err error) {
	s.mu.Lock()
	s.terminal.Done = true
	s.terminal.Result = res
	s.terminal.Err = err
	s.rev++
	s.mu.Unlock()
	s.poke()
}

// Ring returns the underlying StreamRing for TUI snapshot reads.
func (s *ObsStore) Ring() *StreamRing { return s.ring }
