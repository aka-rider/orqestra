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
	notify      chan struct{}    // capacity-1 coalescing wake signal
	streamCh    chan StreamEntry // non-blocking stream relay to TUI frame list

	// em is the WP9 event-bus forwarder. Nil unless AttachEmitter was called
	// (only startNew does this) — every emit call below is nil-guarded, so
	// every existing caller/test that never attaches an emitter observes
	// ObsStore behaving byte-for-bit as before WP9 (§ design note in
	// AttachEmitter).
	em          *emitter
	gateSeq     uint64 // WP9: monotonic GateID source; only Control.Gate opens gates, one at a time
	currentGate GateID // WP9: GateID of the currently-open gate, for the matching GateClosed event
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

// AttachEmitter wires the WP9 event bus behind this ObsStore: every Observer
// mutation below (PhaseChanged, AgentStarted, ..., GateOpened/GateClosed,
// Finished) additionally emits the equivalent RunEvent. This is deliberately
// an optional, nil-guarded field rather than a wrapping "teeObserver" type,
// because Control (control.go) holds a concrete *ObsStore — not the Observer
// interface — and calls GateClosed, which is not part of Observer at all.
// Wrapping ObsStore would force either changing NewControl's signature
// (breaking internal/tui and every existing orchestrator test that calls
// NewControl(*ObsStore)) or duplicating gate dispatch outside the interface.
// Attaching the emitter to ObsStore itself means every existing call site —
// control.go, run_pipeline.go, every step_*.go, engine_pipeline.go, and every
// pre-WP9 test that never calls AttachEmitter — keeps behaving exactly as
// before; only ObsStore's method bodies gain an additional (no-op when
// em==nil) emit. Must be called before any goroutine begins using obs (as
// startNew does, right after NewObsStore and before any `go` statement) so
// no additional locking is required to make the attach visible to later
// readers under the Go memory model; the emit calls below still take s.mu
// like every other field access, for consistency and race-detector clarity.
func (s *ObsStore) AttachEmitter(em *emitter) {
	s.mu.Lock()
	s.em = em
	s.mu.Unlock()
}

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
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventQuestionAsked{ToolCall: q})
	}
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
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventPhaseStarted{Phase: p})
	}
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
	em := s.em
	s.mu.Unlock()
	s.ring.SetAgent(string(id))
	if em != nil {
		em.Emit(EventAgentStarted{AgentID: id, Meta: meta})
	}
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
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventAgentDone{AgentID: id, Usage: usage})
	}
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
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventAgentFailed{AgentID: id, Err: err})
	}
	s.poke()
}

func (s *ObsStore) Stream(id AgentID, ev harness.Event) {
	// Convert harness.Event → StreamEntry (legacy ring/TUI relay) AND → the
	// matching WP9 RunEvent (bus) in the SAME switch, so the two can never
	// disagree about what an event meant. harness.Event kinds the legacy
	// ring ignores (session start/done, error) get no RunEvent either — WP9
	// did not request new bus kinds for them.
	var entry StreamEntry
	var busEvent RunEvent
	switch {
	case ev.IsDelta:
		entry = StreamEntry{Kind: EntryDelta, Text: ev.Text}
		busEvent = EventDelta{AgentID: id, Text: ev.Text}
	case ev.Text != "":
		entry = StreamEntry{Kind: EntryText, Text: ev.Text}
		busEvent = EventDelta{AgentID: id, Text: ev.Text}
	case ev.Tool != "":
		entry = StreamEntry{Kind: EntryToolUse, Tool: ev.Tool, Detail: ev.Detail}
		busEvent = EventToolCall{AgentID: id, Tool: ev.Tool, Detail: ev.Detail}
	case ev.Kind == harness.EventToolResult:
		entry = StreamEntry{Kind: EntryToolResult, ToolErr: ev.IsError}
		busEvent = EventToolResult{AgentID: id, IsError: ev.IsError}
	case ev.Kind == harness.EventUsage:
		entry = StreamEntry{Kind: EntryStats, Stats: StreamStats{Input: ev.Input, Output: ev.Output, Valid: true}}
		busEvent = EventStats{AgentID: id, Input: ev.Input, Output: ev.Output}
	default:
		return
	}
	s.ring.Append(entry)
	select {
	case s.streamCh <- entry:
	default: // TUI is slow; drop rather than block the pipeline
	}
	s.mu.Lock()
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(busEvent)
	}
	s.poke()
}

func (s *ObsStore) GateOpened(req GateRequest) {
	s.mu.Lock()
	s.gate = req
	s.hasGate = true
	s.rev++
	s.gateSeq++
	gid := GateID(s.gateSeq)
	s.currentGate = gid
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventGateOpened{GateID: gid, Request: req})
	}
	s.poke()
}

func (s *ObsStore) GateClosed() {
	s.mu.Lock()
	s.hasGate = false
	s.gate = GateRequest{}
	s.rev++
	gid := s.currentGate
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventGateClosed{GateID: gid})
	}
	s.poke()
}

func (s *ObsStore) Finished(res Result, err error) {
	s.mu.Lock()
	s.terminal.Done = true
	s.terminal.Result = res
	s.terminal.Err = err
	s.rev++
	em := s.em
	s.mu.Unlock()
	if em != nil {
		// EventRunFinished is the terminal, always-last event on the bus
		// (WP2's single terminal writer — obs.Finished is called exactly
		// once per run, from startNew's finish closure).
		em.Emit(EventRunFinished{Result: res, Err: err})
	}
	s.poke()
}

// Ring returns the underlying StreamRing for TUI snapshot reads.
func (s *ObsStore) Ring() *StreamRing { return s.ring }

// ReportHarvested is a WP9-bus-only signal (see AttachEmitter): unlike every
// other Observer method, it carries no legacy-snapshot state to mutate — the
// report text itself is already persisted as an artifact by the calling
// step — so this only forwards to the emitter when one is attached. A
// report-producing step still behaves identically when no emitter is
// attached (e.g. every pre-WP9 test), matching AttachEmitter's contract.
func (s *ObsStore) ReportHarvested(id AgentID, prov ReportProvenance) {
	s.mu.Lock()
	em := s.em
	s.mu.Unlock()
	if em != nil {
		em.Emit(EventReportHarvested{AgentID: id, Provenance: prov})
	}
}
