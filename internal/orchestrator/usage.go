package orchestrator

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// AgentMeta describes the model configuration for an agent role.
type AgentMeta struct {
	ModelRef      string `json:"model_ref"`
	ModelDisplay  string `json:"model_display,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// AgentSnapshot captures the execution state of a single agent.
type AgentSnapshot struct {
	AgentID   string    `json:"agent_id"`
	Meta      AgentMeta `json:"meta"`
	Input     int64     `json:"input"`
	Output    int64     `json:"output"`
	CallCount int       `json:"call_count"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Status    string    `json:"status"` // "running", "done", "failed", "cancelled"
}

// RunSnapshot is the universal data shape consumed by both the live dashboard
// and historical run detail screens.
type RunSnapshot struct {
	Input  int64           `json:"input"`
	Output int64           `json:"output"`
	Limit  int64           `json:"limit,omitempty"`
	Agents []AgentSnapshot `json:"agents"`
}

// Total returns the sum of input and output tokens.
func (s RunSnapshot) Total() int64 { return s.Input + s.Output }

// AgentByID finds an agent snapshot by ID.
func (s RunSnapshot) AgentByID(id string) (AgentSnapshot, bool) {
	for _, a := range s.Agents {
		if a.AgentID == id {
			return a, true
		}
	}
	return AgentSnapshot{}, false
}

// BudgetPercent returns the percentage of budget consumed (0-100+).
// Returns 0 if limit is 0 (unlimited).
func (s RunSnapshot) BudgetPercent() float64 {
	if s.Limit == 0 {
		return 0
	}
	return float64(s.Total()) / float64(s.Limit) * 100
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
// It is concurrent-safe and serves both budget enforcement and snapshot reads.
type RunUsage struct {
	mu     sync.Mutex
	limit  int64
	agents map[string]*agentAccum
	order  []string
}

// NewRunUsage creates a RunUsage with the given budget limit.
// A limit of 0 means unlimited.
func NewRunUsage(limit int64) *RunUsage {
	return &RunUsage{
		limit:  limit,
		agents: make(map[string]*agentAccum),
	}
}

// StartAgent registers a new agent in the run.
func (u *RunUsage) StartAgent(agentID string, meta AgentMeta) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if _, exists := u.agents[agentID]; !exists {
		u.order = append(u.order, agentID)
	}
	u.agents[agentID] = &agentAccum{
		meta:      meta,
		startTime: time.Now(),
		status:    "running",
	}
}

// EndAgent marks an agent as completed with the given status.
func (u *RunUsage) EndAgent(agentID string, status string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if a, ok := u.agents[agentID]; ok {
		a.endTime = time.Now()
		a.status = status
	}
}

// Record accumulates token usage for an agent.
func (u *RunUsage) Record(agentID string, input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()

	a, ok := u.agents[agentID]
	if !ok {
		// Agent not started yet — auto-register with minimal meta.
		u.order = append(u.order, agentID)
		a = &agentAccum{startTime: time.Now(), status: "running"}
		u.agents[agentID] = a
	}
	a.input += input
	a.output += output
	a.callCount++
}

// Snapshot returns an immutable copy of the current run state.
func (u *RunUsage) Snapshot() RunSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()

	snap := RunSnapshot{Limit: u.limit}
	for _, id := range u.order {
		a := u.agents[id]
		snap.Agents = append(snap.Agents, AgentSnapshot{
			AgentID:   id,
			Meta:      a.meta,
			Input:     a.input,
			Output:    a.output,
			CallCount: a.callCount,
			StartTime: a.startTime,
			EndTime:   a.endTime,
			Status:    a.status,
		})
		snap.Input += a.input
		snap.Output += a.output
	}
	return snap
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

// LoadRunSnapshot reads a run_snapshot.json from disk.
func LoadRunSnapshot(path string) (RunSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunSnapshot{}, err
	}
	var snap RunSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return RunSnapshot{}, err
	}
	return snap, nil
}

// WriteRunSnapshot persists a RunSnapshot as JSON.
func WriteRunSnapshot(path string, snap RunSnapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
