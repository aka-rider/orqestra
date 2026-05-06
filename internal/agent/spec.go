package agent

// Role identifies the function of an agent within the pipeline.
type Role string

const (
	RoleIntake        Role = "intake"
	RolePlanner       Role = "planner"
	RolePlanValidator Role = "plan-validator"
	RoleWorker        Role = "worker"
)

// AgentState represents the lifecycle state of an agent execution.
type AgentState string

const (
	StateStarting AgentState = "starting"
	StateRunning  AgentState = "running"
	StateDone     AgentState = "done"
	StateFailed   AgentState = "failed"
)

// AgentSpec defines what to run in a sandbox.
type AgentSpec struct {
	Role         Role              // "intake", "planner", "plan-validator", "worker"
	ModelRef     string            // model tier from config
	SystemPrompt string            // staged as file in sandbox
	InputFiles   map[string][]byte // staged into sandbox; keys are relative to /workspace/.orqestra/agent/input/
	OutputFile   string            // expected artifact path (relative to /workspace)
	Command      []string          // claude CLI invocation
	Env          []string          // environment variables for the subprocess
	Interactive  bool              // true if the agent can receive user input
}
