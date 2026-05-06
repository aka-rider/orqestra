package agent

// RunCallbacks receives lifecycle events from the agent runner.
// All callbacks are called from the runner goroutine — callers must ensure
// thread safety (e.g., use p.Send for Bubble Tea integration).
type RunCallbacks struct {
	// OnOutput delivers raw PTY output bytes.
	OnOutput func(data []byte)
	// OnBEL is called when a BEL character (\x07) is detected in the output.
	// The BEL is detected outside of OSC sequences to avoid false positives.
	OnBEL func()
	// OnDone is called when the PTY process exits.
	OnDone func(exitCode int, err error)
	// OnState is called when the agent lifecycle state changes.
	OnState func(state AgentState)
}

// RunConfig configures a single agent execution.
type RunConfig struct {
	Spec      AgentSpec
	Session   SessionDir
	RepoPath  string // host-side repo path
	Callbacks RunCallbacks
}
