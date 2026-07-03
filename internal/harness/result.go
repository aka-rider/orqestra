package harness

// TokenUsage captures token consumption from an LLM call.
type TokenUsage struct {
	Input  int64
	Output int64
}

// Total returns the sum of input and output tokens.
func (u TokenUsage) Total() int64 { return u.Input + u.Output }

// RunResult captures the output and token usage from a CLIRunner invocation.
type RunResult struct {
	Output       string
	Usage        TokenUsage // zero value if the harness did not report usage
	SessionID    string     // populated from stream-json result event when available
	PlanFilePath string     // plan file path captured from result event (may be empty)
	ExitCode     int        // non-zero when the subprocess exited with an error (supporting evidence)
	Stderr       string     // stderr output captured from the subprocess
}
