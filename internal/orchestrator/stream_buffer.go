package orchestrator

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
)

// Activity records a single tool invocation observed in the agent stream.
type Activity struct {
	Tool   string // e.g. "Read", "Bash", "Write"
	Detail string // human-readable context, e.g. file path or truncated command
}

const maxActivities = 20

// StreamBuffer is a concurrent-safe line buffer shared between the orchestrator
// (writer) and the TUI (reader). The TUI polls it on tick to avoid channel
// backpressure that would block the subprocess.
type StreamBuffer struct {
	mu             sync.Mutex
	lines          []string
	agentID        string
	maxLines       int
	activities     []Activity
	agentSnapshots map[string][]Activity
}

// NewStreamBuffer creates a StreamBuffer with the given line capacity.
func NewStreamBuffer(maxLines int) *StreamBuffer {
	if maxLines <= 0 {
		maxLines = 200
	}
	return &StreamBuffer{
		maxLines:       maxLines,
		agentSnapshots: make(map[string][]Activity),
	}
}

// SetAgent resets the buffer for a new active agent.
func (sb *StreamBuffer) SetAgent(id string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.agentID != "" && len(sb.activities) > 0 {
		snapshot := make([]Activity, len(sb.activities))
		copy(snapshot, sb.activities)
		sb.agentSnapshots[sb.agentID] = snapshot
	}

	sb.agentID = id
	sb.lines = nil
	sb.activities = nil
}

// AgentActivities returns a copy of the recorded activities for the given agent.
func (sb *StreamBuffer) AgentActivities(id string) []Activity {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.agentID == id {
		out := make([]Activity, len(sb.activities))
		copy(out, sb.activities)
		return out
	}

	activities, ok := sb.agentSnapshots[id]
	if !ok {
		return nil
	}
	out := make([]Activity, len(activities))
	copy(out, activities)
	return out
}

// knownStreamEventTypes enumerates Claude CLI stream-json event types that
// must never reach the user-facing presentation surface as raw NDJSON.
// Used by looksLikeStreamEventFrame as a defense-in-depth filter when a
// streaming-path regression places raw frames into the TUI buffer.
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
// stream-json event frame that leaked past the harness parser. It returns
// the event "type" and true only when the trimmed line begins with '{',
// is valid JSON, decodes a non-empty "type" field, and that type is one of
// knownStreamEventTypes. Anything else returns ("", false) so genuine text
// content with stray braces is preserved.
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

// Append adds text to the buffer, accumulating into the current line
// until a newline is encountered. Completed lines that decode as a known
// Claude CLI stream-json event frame are dropped and logged via slog.Warn:
// the harness is the single source of parsed text, and a raw frame at the
// presentation boundary indicates a regression.
func (sb *StreamBuffer) Append(text string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for len(text) > 0 {
		nlIdx := strings.IndexByte(text, '\n')
		if nlIdx == -1 {
			if len(sb.lines) == 0 {
				sb.lines = append(sb.lines, text)
			} else {
				sb.lines[len(sb.lines)-1] += text
			}
			break
		}
		fragment := text[:nlIdx]
		var completed string
		if len(sb.lines) == 0 {
			completed = fragment
		} else {
			completed = sb.lines[len(sb.lines)-1] + fragment
		}
		if t, ok := looksLikeStreamEventFrame(completed); ok {
			slog.Warn("stream buffer: dropping raw stream-event frame", "type", t, "len", len(completed))
			if len(sb.lines) > 0 {
				sb.lines = sb.lines[:len(sb.lines)-1]
			}
		} else {
			if len(sb.lines) == 0 {
				sb.lines = append(sb.lines, fragment)
			} else {
				sb.lines[len(sb.lines)-1] += fragment
			}
		}
		sb.lines = append(sb.lines, "")
		text = text[nlIdx+1:]
	}

	if len(sb.lines) > sb.maxLines {
		sb.lines = sb.lines[len(sb.lines)-sb.maxLines:]
	}
}

// AppendActivity records a tool invocation in the activity ring.
func (sb *StreamBuffer) AppendActivity(tool, detail string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.activities = append(sb.activities, Activity{Tool: tool, Detail: detail})
	if len(sb.activities) > maxActivities {
		sb.activities = sb.activities[len(sb.activities)-maxActivities:]
	}
}

// Snapshot returns the current agent ID, a copy of buffered lines, and recent activities.
func (sb *StreamBuffer) Snapshot() (agentID string, lines []string, activities []Activity) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	cp := make([]string, len(sb.lines))
	copy(cp, sb.lines)
	act := make([]Activity, len(sb.activities))
	copy(act, sb.activities)
	return sb.agentID, cp, act
}

// streamWriter implements io.Writer and harness.ActivitySink by appending
// to a StreamBuffer.
type streamWriter struct {
	buf *StreamBuffer
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.buf.Append(string(p))
	}
	return len(p), nil
}

// OnToolUse implements harness.ActivitySink.
func (w *streamWriter) OnToolUse(name, detail string) {
	w.buf.AppendActivity(name, detail)
}
