package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

const (
	initialScanBufferBytes = 64 << 10
	maxJSONLLineBytes      = 2 << 20
)

// streamEvent represents a parsed event from Claude CLI's stream-json output.
type streamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype,omitempty"`
	Result       string          `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Delta        streamDeltaText `json:"delta,omitempty"`
	Message      json.RawMessage `json:"message,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
	Usage        *streamUsage    `json:"usage,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PlanFilePath string          `json:"planFilePath,omitempty"`
	Event        json.RawMessage `json:"event,omitempty"`
}

// streamUsage captures token usage from the Claude CLI result event.
type streamUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type streamDeltaText struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// extractAssistantText pulls text content from an assistant message event.
func (e *streamEvent) extractAssistantText() string {
	if e.Message == nil {
		return ""
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

type toolUseBlock struct {
	Name  string
	Input json.RawMessage
}

func (e *streamEvent) extractAssistantToolUses() []toolUseBlock {
	if e.Message == nil {
		return nil
	}
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return nil
	}
	var tools []toolUseBlock
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			tools = append(tools, toolUseBlock{Name: block.Name, Input: block.Input})
		}
	}
	return tools
}

// extractToolUse extracts the tool name and input args from a content_block_start event.
func (e *streamEvent) extractToolUse() (name string, args json.RawMessage) {
	if e.ContentBlock == nil {
		return "", nil
	}
	var block struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(e.ContentBlock, &block); err == nil && block.Type == "tool_use" {
		return block.Name, block.Input
	}
	return "", nil
}

// emitStreamEvents converts a stream-json event into typed Event entries and
// sends them to the events channel. Nil-safe.
func emitStreamEvents(event streamEvent, events chan<- Event) {
	if events == nil {
		return
	}
	for _, e := range streamEventsFrom(event) {
		events <- e
	}
}

func streamEventsFrom(event streamEvent) []Event {
	var out []Event
	switch event.Type {
	case "assistant":
		if text := event.extractAssistantText(); text != "" {
			out = append(out, Event{Kind: EventChunk, Text: text})
		}
		for _, tu := range event.extractAssistantToolUses() {
			out = append(out, Event{Kind: EventToolUse, Tool: tu.Name, Detail: ToolDetail(tu.Name, tu.Input), Args: normalizeArgs(tu.Input)})
		}
	case "content_block_delta":
		if event.Delta.Text != "" {
			out = append(out, Event{Kind: EventChunk, Text: event.Delta.Text, IsDelta: true})
		}
	case "content_block_start":
		if name, args := event.extractToolUse(); name != "" {
			out = append(out, Event{Kind: EventToolUse, Tool: name, Detail: ToolDetail(name, args), Args: normalizeArgs(args)})
		}
	case "user":
		for _, isErr := range extractToolResults(event.Message) {
			out = append(out, Event{Kind: EventToolResult, IsError: isErr})
		}
	case "stream_event":
		if event.Event == nil {
			return out
		}
		var inner streamEvent
		if err := json.Unmarshal(event.Event, &inner); err != nil {
			return out
		}
		switch inner.Type {
		case "content_block_start":
			if name, args := inner.extractToolUse(); name != "" {
				out = append(out, Event{Kind: EventToolUse, Tool: name, Detail: ToolDetail(name, args), Args: normalizeArgs(args)})
			}
		case "content_block_delta":
			if inner.Delta.Text != "" {
				out = append(out, Event{Kind: EventChunk, Text: inner.Delta.Text, IsDelta: true})
			}
		}
	}
	return out
}

// extractToolResults parses a user message's content for tool_result blocks
// and returns a slice of is_error booleans — one per tool result.
func extractToolResults(msg json.RawMessage) []bool {
	if msg == nil {
		return nil
	}
	var m struct {
		Content []struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil
	}
	var out []bool
	for _, block := range m.Content {
		if block.Type == "tool_result" {
			out = append(out, block.IsError)
		}
	}
	return out
}

// parseStream scans stream-json lines from r, dispatches display events to
// events (nil-safe), and returns the accumulated result, error state, usage,
// session ID, and plan file path.
func parseStream(r io.Reader, events chan<- Event) (result string, isError bool, usage TokenUsage, sessionID, planFilePath string, err error) {
	var raw string
	raw, err = parseStreamLines(r, events)
	if err != nil {
		return
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type         string       `json:"type"`
			Subtype      string       `json:"subtype,omitempty"`
			Result       string       `json:"result,omitempty"`
			IsError      bool         `json:"is_error,omitempty"`
			SessionID    string       `json:"session_id,omitempty"`
			PlanFilePath string       `json:"planFilePath,omitempty"`
			Usage        *streamUsage `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		// Capture session_id from the first event that carries one (the system/init
		// event leads the stream). Keeping this outside the result-type guard means
		// RunResult.SessionID survives an early stop — e.g. the report-arrival SIGKILL
		// where the terminal result event never arrives. First-wins (not last-wins):
		// if a subagent spawned mid-stream emits its own session_id, RunResult.SessionID
		// must stay pinned to the run's own session, matching the supervisor's own
		// first-wins capturedSID logic (agent_supervisor.go's supervise loop, WP12) —
		// every EventSessionStart reaches it (fanoutSink forwards all events
		// unfiltered), and it keeps only the first.
		if event.SessionID != "" && sessionID == "" {
			sessionID = event.SessionID
		}
		if event.Type == "result" {
			result = event.Result
			isError = event.IsError || strings.HasPrefix(event.Subtype, "error_")
			if event.PlanFilePath != "" {
				planFilePath = event.PlanFilePath
			}
			if event.Usage != nil {
				usage = TokenUsage{
					Input:  event.Usage.InputTokens,
					Output: event.Usage.OutputTokens,
				}
			}
		}
	}
	return
}

// parseStreamLines reads stream-json NDJSON from src line by line, routes each
// parsed event through emitStreamEvents, and returns the full raw NDJSON for
// post-processing. events may be nil.
func parseStreamLines(src io.Reader, events chan<- Event) (string, error) {
	var rawBuf bytes.Buffer
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		rawBuf.Write(line)
		rawBuf.WriteByte('\n')
		if len(line) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Debug("non-JSON stream line from claude CLI", "err", err, "line_len", len(line))
			if events != nil {
				events <- Event{Kind: EventChunk, Text: string(line) + "\n"}
			}
			continue
		}

		if event.SessionID != "" && events != nil {
			events <- Event{Kind: EventSessionStart, SessionID: event.SessionID}
		}

		if event.Type == "result" && events != nil {
			events <- Event{Kind: EventSessionDone}
		}

		emitStreamEvents(event, events)

		if event.Usage != nil && events != nil {
			events <- Event{
				Kind:   EventUsage,
				Input:  event.Usage.InputTokens,
				Output: event.Usage.OutputTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return rawBuf.String(), err
	}
	return rawBuf.String(), nil
}
