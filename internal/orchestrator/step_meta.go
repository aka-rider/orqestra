package orchestrator

import "time"

// StepMeta is the per-agent metadata persisted as JSON in the session directory.
type StepMeta struct {
	AgentID              string    `json:"agent_id"`
	ModelRef             string    `json:"model_ref,omitempty"`
	ModelDisplay         string    `json:"model_display,omitempty"`
	Provider             string    `json:"provider,omitempty"`
	ContextWindow        int64     `json:"context_window,omitempty"`
	StartTime            time.Time `json:"start_time"`
	EndTime              time.Time `json:"end_time"`
	ClaudeSessionID      string    `json:"claude_session_id,omitempty"`
	ClaudeProjectPath    string    `json:"claude_project_path,omitempty"`
	ClaudeSessionLogPath string    `json:"claude_session_log_path,omitempty"`
	ClaudePlanFilePath   string    `json:"claude_plan_file_path,omitempty"`
	Status               string    `json:"status"` // "done" or "failed"
	Error                string    `json:"error,omitempty"`
	PlanSource           string    `json:"plan_source,omitempty"` // "plan_file" (default) or "stream_fallback"
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
}

// KnownAgents lists the canonical agent IDs in pipeline execution order.
var KnownAgents = []string{"researcher", "architect", "critic", "worker"}
