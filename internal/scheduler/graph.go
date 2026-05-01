package scheduler

// AgentNode represents a single agent in the execution graph.
type AgentNode struct {
	Role             string   // e.g. "developer", "qa"
	ModelRef         string   // references key in config models map
	SmallModelRef    string   // fast model for non-essential calls
	SystemPromptFile string   // path to system prompt file
	DependsOn        []string // role names this agent depends on
	Validator        *ValidatorNode
}

// ValidatorNode represents a validator attached to an agent.
type ValidatorNode struct {
	Role             string
	ModelRef         string
	SystemPromptFile string
}

// ExecutionGraph is the full DAG of agents.
type ExecutionGraph struct {
	Agents      []AgentNode
	Concurrency int // 0 = unlimited parallel, 1 = serial, N = max N concurrent
}
