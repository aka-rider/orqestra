package orchestrator

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Activity records a single tool invocation observed in the agent stream.
type Activity struct {
	Tool   string // e.g. "Read", "Bash", "Write"
	Detail string // human-readable context, e.g. file path or truncated command
}

const maxActivities = 20

// StreamEntryKind classifies entries in the StreamRing.
type StreamEntryKind int

const (
	EntryText    StreamEntryKind = iota // completed text line
	EntryToolUse                        // tool invocation
	EntryStats                          // token usage snapshot
)

// EntryStats carries token usage at a point in time.
// Zero value is safe to read (Valid=false means not populated).
type StreamStats struct {
	Input  int64
	Output int64
	Valid  bool
}

// StreamEntry is the unified entry type for the StreamRing.
// Pure value type — no pointers, no aliasing across goroutines.
type StreamEntry struct {
	Kind   StreamEntryKind
	Text   string      // EntryText: completed line content
	Tool   string      // EntryToolUse: tool name
	Detail string      // EntryToolUse: human-readable detail
	Stats  StreamStats // EntryStats: token snapshot
}

const defaultRingCapacity = 200

// AgentUsageSnapshot records accumulated token usage for a completed agent phase.
type AgentUsageSnapshot struct {
	Input  int64
	Output int64
	Start  time.Time
	End    time.Time
}

// StreamRing is a concurrent-safe ring buffer of StreamEntry values shared
// between the orchestrator (writer) and the TUI (reader). The TUI polls it
// on tick to avoid channel backpressure that would block the subprocess.
type StreamRing struct {
	mu         sync.Mutex
	entries    []StreamEntry
	maxEntries int
	agentID    string
	partial    string // incomplete line accumulator (not yet in entries)
	history    *StreamHistoryStore

	// Live token accumulation for the current agent.
	liveInput  int64
	liveOutput int64
	liveStart  time.Time
}

// NewStreamRing creates a StreamRing with the given entry capacity.
func NewStreamRing(maxEntries int) *StreamRing {
	if maxEntries <= 0 {
		maxEntries = defaultRingCapacity
	}
	return &StreamRing{
		maxEntries: maxEntries,
		history:    NewStreamHistoryStore(),
	}
}

// SetAgent resets the ring for a new active agent, snapshotting the previous
// agent's entries and capturing accumulated usage.
func (r *StreamRing) SetAgent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Flush any partial line as a completed entry before snapshotting.
	if r.partial != "" {
		r.entries = append(r.entries, StreamEntry{Kind: EntryText, Text: r.partial})
		r.partial = ""
	}

	if r.agentID != "" {
		usage := AgentUsageSnapshot{
			Input:  r.liveInput,
			Output: r.liveOutput,
			Start:  r.liveStart,
			End:    time.Now(),
		}
		r.history.Capture(AgentID(r.agentID), r.entries, usage)
	}

	r.agentID = id
	r.entries = nil
	r.liveInput = 0
	r.liveOutput = 0
	r.liveStart = time.Now()
}

// Append adds an entry to the ring buffer.
func (r *StreamRing) Append(entry StreamEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	if len(r.entries) > r.maxEntries {
		r.entries = r.entries[len(r.entries)-r.maxEntries:]
	}
}

// RecordUsage accumulates token counts for the current active agent.
// Called by streamWriter.OnUsage on every usage report from the harness.
func (r *StreamRing) RecordUsage(input, output int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveInput += input
	r.liveOutput += output
}

// SnapshotUsage returns the accumulated token usage for the current agent
// and the time the agent started. Safe for concurrent reads from the TUI tick.
func (r *StreamRing) SnapshotUsage() (input, output int64, start time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.liveInput, r.liveOutput, r.liveStart
}

// AgentUsage returns the accumulated usage snapshot for a completed agent.
// Returns zero value with zero times if the agent has no recorded usage.
func (r *StreamRing) AgentUsage(id string) AgentUsageSnapshot {
	return r.history.AgentUsage(AgentID(id))
}

// Snapshot returns the current agent ID and a copy of all entries.
func (r *StreamRing) Snapshot() (agentID string, entries []StreamEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]StreamEntry, len(r.entries))
	copy(cp, r.entries)
	return r.agentID, cp
}

// AgentEntries returns a copy of the recorded entries for the given agent.
func (r *StreamRing) AgentEntries(id string) []StreamEntry {
	r.mu.Lock()
	if r.agentID == id {
		out := make([]StreamEntry, len(r.entries))
		copy(out, r.entries)
		r.mu.Unlock()
		return out
	}
	r.mu.Unlock()

	return r.history.AgentEntries(AgentID(id))
}

// History returns the store used for historical per-agent data.
func (r *StreamRing) History() *StreamHistoryStore {
	return r.history
}

// --- Compatibility helpers ---

// SnapshotCompat returns the current agent ID, text lines, and tool activities
// in the same shape as the old StreamBuffer.Snapshot() for TUI compatibility.
// Includes the current partial line (if any) as the last element of lines.
func (r *StreamRing) SnapshotCompat() (agentID string, lines []string, activities []Activity) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, e := range r.entries {
		switch e.Kind {
		case EntryText:
			lines = append(lines, e.Text)
		case EntryToolUse:
			activities = append(activities, Activity{Tool: e.Tool, Detail: e.Detail})
		}
	}
	if r.partial != "" {
		lines = append(lines, r.partial)
	}
	return r.agentID, lines, activities
}

// AgentActivities returns tool-use entries for the given agent as Activity
// values, preserving backward compatibility with TUI code.
func (r *StreamRing) AgentActivities(id string) []Activity {
	entries := r.AgentEntries(id)
	var activities []Activity
	for _, e := range entries {
		if e.Kind == EntryToolUse {
			activities = append(activities, Activity{Tool: e.Tool, Detail: e.Detail})
		}
	}
	return activities
}

// --- Text accumulation + frame filtering (ported from StreamBuffer) ---

// knownStreamEventTypes enumerates Claude CLI stream-json event types that
// must never reach the user-facing presentation surface as raw NDJSON.
var knownStreamEventTypes = map[string]struct{}{
	"system":              {},
	"assistant":           {},
	"user":                {},
	"result":              {},
	"rate_limit_event":    {},
	"content_block_start": {},
	"content_block_delta": {},
	"stream_event":        {},
	"tool_use":            {},
	"tool_result":         {},
}

// looksLikeStreamEventFrame reports whether line is a complete Claude CLI
// stream-json event frame that leaked past the harness parser.
func looksLikeStreamEventFrame(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	if !json.Valid([]byte(trimmed)) {
		return "", false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return "", false
	}
	if probe.Type == "" {
		return "", false
	}
	if _, ok := knownStreamEventTypes[probe.Type]; !ok {
		return "", false
	}
	return probe.Type, true
}

// AppendText handles raw text from io.Writer, accumulating partial lines and
// emitting EntryText on newline boundaries. Completed lines that decode as
// known Claude CLI stream-json event frames are dropped.
func (r *StreamRing) AppendText(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for len(text) > 0 {
		nlIdx := strings.IndexByte(text, '\n')
		if nlIdx == -1 {
			// No newline: accumulate into partial.
			r.partial += text
			break
		}

		// Complete a line: partial + fragment up to newline.
		completed := r.partial + text[:nlIdx]
		r.partial = ""

		if t, ok := looksLikeStreamEventFrame(completed); ok {
			slog.Warn("stream ring: dropping raw stream-event frame", "type", t, "len", len(completed))
		} else if completed != "" {
			r.entries = append(r.entries, StreamEntry{Kind: EntryText, Text: completed})
		}

		text = text[nlIdx+1:]
	}

	if len(r.entries) > r.maxEntries {
		r.entries = r.entries[len(r.entries)-r.maxEntries:]
	}
}

// AppendActivity adds a tool-use entry. Maintains the same capped behavior
// as the old StreamBuffer (last 20 tool entries per agent visible as activities).
func (r *StreamRing) AppendActivity(tool, detail string) {
	r.Append(StreamEntry{Kind: EntryToolUse, Tool: tool, Detail: detail})
}

// AppendStats adds a stats entry with token usage.
func (r *StreamRing) AppendStats(input, output int64) {
	r.Append(StreamEntry{Kind: EntryStats, Stats: StreamStats{Input: input, Output: output, Valid: true}})
}
