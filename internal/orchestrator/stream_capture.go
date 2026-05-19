package orchestrator

import (
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// streamCapture collects per-agent stream artifacts for history persistence.
// It is intentionally separate from StreamRing, which is a UI display cache.
type streamCapture struct {
	history *StreamHistoryStore

	agentID    string
	entries    []StreamEntry
	partial    string
	liveInput  int64
	liveOutput int64
	liveStart  time.Time
}

func newStreamCapture(history *StreamHistoryStore) *streamCapture {
	return &streamCapture{history: history}
}

func (c *streamCapture) SetAgent(id string) {
	if c == nil {
		return
	}

	if c.partial != "" {
		c.entries = append(c.entries, StreamEntry{Kind: EntryText, Text: c.partial})
		c.partial = ""
	}

	if c.agentID != "" && c.history != nil {
		usage := AgentUsageSnapshot{
			Input:  c.liveInput,
			Output: c.liveOutput,
			Start:  c.liveStart,
			End:    time.Now(),
		}
		c.history.Capture(AgentID(c.agentID), c.entries, usage)
	}

	c.agentID = id
	c.entries = nil
	c.liveInput = 0
	c.liveOutput = 0
	c.liveStart = time.Now()
}

func (c *streamCapture) OnUpdate(ev harness.StreamUpdate) {
	if c == nil {
		return
	}

	if ev.Text != "" {
		c.appendText(ev.Text)
	}
	if ev.Tool != "" {
		c.entries = append(c.entries, StreamEntry{Kind: EntryToolUse, Tool: ev.Tool, Detail: ev.Detail})
	}
	if ev.UsageValid {
		c.liveInput += ev.Input
		c.liveOutput += ev.Output
		c.entries = append(c.entries, StreamEntry{Kind: EntryStats, Stats: StreamStats{Input: ev.Input, Output: ev.Output, Valid: true}})
	}
}

func (c *streamCapture) appendText(text string) {
	for len(text) > 0 {
		nlIdx := strings.IndexByte(text, '\n')
		if nlIdx == -1 {
			c.partial += text
			break
		}

		completed := c.partial + text[:nlIdx]
		c.partial = ""

		if _, ok := looksLikeStreamEventFrame(completed); !ok && completed != "" {
			c.entries = append(c.entries, StreamEntry{Kind: EntryText, Text: completed})
		}

		text = text[nlIdx+1:]
	}
}
