package orchestrator

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/xiii/orqestra/internal/harness"
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

// StreamRing is a concurrent-safe ring buffer of StreamEntry values shared
// between the orchestrator (writer) and the TUI (reader). The TUI polls it
// on tick to avoid channel backpressure that would block the subprocess.
type StreamRing struct {
	mu             sync.Mutex
	entries        []StreamEntry
	maxEntries     int
	agentID        string
	partial        string // incomplete line accumulator (not yet in entries)
	agentSnapshots map[string][]StreamEntry
}

// NewStreamRing creates a StreamRing with the given entry capacity.
func NewStreamRing(maxEntries int) *StreamRing {
	if maxEntries <= 0 {
		maxEntries = defaultRingCapacity
	}
	return &StreamRing{
		maxEntries:     maxEntries,
		agentSnapshots: make(map[string][]StreamEntry),
	}
}

// SetAgent resets the ring for a new active agent, snapshotting the previous
// agent's entries.
func (r *StreamRing) SetAgent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Flush any partial line as a completed entry before snapshotting.
	if r.partial != "" {
		r.entries = append(r.entries, StreamEntry{Kind: EntryText, Text: r.partial})
		r.partial = ""
	}

	if r.agentID != "" && len(r.entries) > 0 {
		snapshot := make([]StreamEntry, len(r.entries))
		copy(snapshot, r.entries)
		r.agentSnapshots[r.agentID] = snapshot
	}

	r.agentID = id
	r.entries = nil
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
	defer r.mu.Unlock()

	if r.agentID == id {
		out := make([]StreamEntry, len(r.entries))
		copy(out, r.entries)
		return out
	}

	entries, ok := r.agentSnapshots[id]
	if !ok {
		return nil
	}
	out := make([]StreamEntry, len(entries))
	copy(out, entries)
	return out
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

// --- streamWriter: adapts StreamRing to io.Writer + harness.ActivitySink + harness.UsageSink ---

// streamWriter implements io.Writer, harness.ActivitySink, and harness.UsageSink
// by appending to a StreamRing. Created per-agent call, writes to the long-lived ring.
type streamWriter struct {
	ring *StreamRing
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.ring.AppendText(string(p))
	}
	return len(p), nil
}

// OnToolUse implements harness.ActivitySink.
func (w *streamWriter) OnToolUse(name, detail string) {
	w.ring.AppendActivity(name, detail)
}

// OnUsage implements harness.UsageSink.
func (w *streamWriter) OnUsage(input, output int64) {
	w.ring.AppendStats(input, output)
}

// Verify interface compliance at compile time.
var _ harness.ActivitySink = (*streamWriter)(nil)
var _ harness.UsageSink = (*streamWriter)(nil)
