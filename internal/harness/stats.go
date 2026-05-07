package harness

import (
	"sync"
	"time"
)

// StatsTracker accumulates live agent statistics from stream events.
type StatsTracker struct {
	mu        sync.Mutex
	agentID   string
	phase     string
	start     time.Time
	inTokens  int64
	outTokens int64
	toolCalls []ToolCallSummary
}

// NewStatsTracker creates a stats tracker for the given agent.
func NewStatsTracker(agentID, phase string) *StatsTracker {
	return &StatsTracker{
		agentID: agentID,
		phase:   phase,
		start:   time.Now(),
	}
}

// Record processes a StreamEvent and updates stats.
func (s *StatsTracker) Record(event StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch e := event.(type) {
	case UsageDelta:
		s.inTokens += e.InputTokens
		s.outTokens += e.OutputTokens
	case ToolUse:
		s.toolCalls = append(s.toolCalls, ToolCallSummary{Name: e.Name})
	case Result:
		s.inTokens += e.Usage.InputTokens
		s.outTokens += e.Usage.OutputTokens
	}
}

// Stats returns the current AgentStats snapshot.
func (s *StatsTracker) Stats() AgentStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	elapsed := time.Since(s.start).Seconds()
	throughput := 0.0
	if elapsed > 0 {
		throughput = float64(s.outTokens) / elapsed
	}

	return AgentStats{
		AgentID:      s.agentID,
		Phase:        s.phase,
		InputTokens:  s.inTokens,
		OutputTokens: s.outTokens,
		ThroughputPS: throughput,
		ToolCalls:    append([]ToolCallSummary(nil), s.toolCalls...),
	}
}
