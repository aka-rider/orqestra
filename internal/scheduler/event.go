package scheduler

// EventType classifies scheduler events.
type EventType int

const (
	EventAgentStarted EventType = iota
	EventAgentDone
	EventAgentFailed
	EventValidationStarted
	EventValidationPassed
	EventValidationFailed
	EventDriftDetected
)

// Event is emitted during scheduler execution.
type Event struct {
	Type    EventType
	Role    string
	Message string
	Err     error
}
