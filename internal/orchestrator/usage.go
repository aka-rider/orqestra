package orchestrator

import (
	"sync"
	"time"
)

// AgentID is the typed identifier for orchestrator agents. Moved here from
// the deleted stream_history.go (Tier B/WP10): this is a core cross-cutting
// type (Observer, StepContext, RunEvent all use it), not stream-display
// state, so it survives the stream-ring deletion.
type AgentID string

// AgentMeta describes the model configuration for an agent role.
type AgentMeta struct {
	ModelRef      string `json:"model_ref"`
	ModelDisplay  string `json:"model_display,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// agentAccum is the mutable internal state for a single agent.
type agentAccum struct {
	meta      AgentMeta
	input     int64
	output    int64
	callCount int
	startTime time.Time
	endTime   time.Time
	status    string
}

// RunUsage is the golden source for token accounting during a pipeline run.
// It is concurrent-safe and serves budget enforcement.
type RunUsage struct {
	mu     sync.Mutex
	limit  int64
	agents map[string]*agentAccum
}

// NewRunUsage creates a RunUsage with the given budget limit.
// A limit of 0 means unlimited.
func NewRunUsage(limit int64) *RunUsage {
	return &RunUsage{
		limit:  limit,
		agents: make(map[string]*agentAccum),
	}
}

// Record accumulates token usage for an agent.
func (u *RunUsage) Record(agentID string, input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()

	a, ok := u.agents[agentID]
	if !ok {
		// Agent not started yet — auto-register with minimal meta.
		a = &agentAccum{startTime: time.Now(), status: "running"}
		u.agents[agentID] = a
	}
	a.input += input
	a.output += output
	a.callCount++
}

// Limit returns the configured budget limit.
func (u *RunUsage) Limit() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.limit
}

// TotalUsed returns the total tokens consumed so far.
func (u *RunUsage) TotalUsed() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	var total int64
	for _, a := range u.agents {
		total += a.input + a.output
	}
	return total
}
