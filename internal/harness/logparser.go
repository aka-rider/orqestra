package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// ParseSessionLogStream reads a Claude CLI JSONL session log from a reader
// and returns extracted typed events.
func ParseSessionLogStream(r io.Reader) ([]Event, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)

	var events []Event

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not a valid JSON line — skip
			continue
		}

		events = append(events, streamEventsFrom(event)...)
		if event.Usage != nil {
			events = append(events, Event{Kind: EventUsage, Input: event.Usage.InputTokens, Output: event.Usage.OutputTokens})
		}
	}

	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("parse session log: scanner: %w", err)
	}

	return events, nil
}
