package orchestrator

import "sync"

// AgentID is the typed identifier for orchestrator agents.
type AgentID string

// AgentStreamSnapshot stores all historical stream artifacts for one agent.
type AgentStreamSnapshot struct {
	Entries []StreamEntry
	Usage   AgentUsageSnapshot
}

// StreamHistoryStore keeps historical per-agent stream artifacts.
// This is intentionally separate from StreamRing, which only tracks live display state.
type StreamHistoryStore struct {
	mu     sync.Mutex
	agents map[AgentID]AgentStreamSnapshot
}

// NewStreamHistoryStore creates an empty history store.
func NewStreamHistoryStore() *StreamHistoryStore {
	return &StreamHistoryStore{
		agents: make(map[AgentID]AgentStreamSnapshot),
	}
}

// Capture stores completed entries and usage for an agent.
func (h *StreamHistoryStore) Capture(agentID AgentID, entries []StreamEntry, usage AgentUsageSnapshot) {
	if agentID == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	snapshot := h.agents[agentID]

	if len(entries) > 0 {
		entryCopy := make([]StreamEntry, len(entries))
		copy(entryCopy, entries)
		snapshot.Entries = entryCopy
	}

	if usage.Input > 0 || usage.Output > 0 {
		snapshot.Usage = usage
	}

	h.agents[agentID] = snapshot
}

// AgentEntries returns a copy of recorded entries for an agent.
func (h *StreamHistoryStore) AgentEntries(id AgentID) []StreamEntry {
	h.mu.Lock()
	defer h.mu.Unlock()

	agent, ok := h.agents[id]
	if !ok {
		return nil
	}
	entries := agent.Entries
	out := make([]StreamEntry, len(entries))
	copy(out, entries)
	return out
}

// AgentActivities returns tool-use activities for an agent.
func (h *StreamHistoryStore) AgentActivities(id AgentID) []Activity {
	entries := h.AgentEntries(id)
	var activities []Activity
	for _, e := range entries {
		if e.Kind == EntryToolUse {
			activities = append(activities, Activity{Tool: e.Tool, Detail: e.Detail})
		}
	}
	return activities
}

// AgentUsage returns recorded usage for a completed agent.
func (h *StreamHistoryStore) AgentUsage(id AgentID) AgentUsageSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	agent, ok := h.agents[id]
	if !ok || (agent.Usage.Input == 0 && agent.Usage.Output == 0) {
		return AgentUsageSnapshot{}
	}
	return agent.Usage
}
