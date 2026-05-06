//go:build darwin

// Package chrome implements the momentary BubbleTea overlay UI that appears
// when the user presses the prefix key (Ctrl+B) in the mux. It shows pipeline
// status, tab list, workspace changes, and recent logs as a snapshot view.
package chrome

import (
	"time"
)

// PipelinePhase represents the current phase of the pipeline.
type PipelinePhase int

const (
	PhaseIntake PipelinePhase = iota
	PhasePlanner
	PhaseValidator
	PhaseWorker
	PhaseDone
)

// String returns a human-readable phase name.
func (p PipelinePhase) String() string {
	switch p {
	case PhaseIntake:
		return "Intake"
	case PhasePlanner:
		return "Planner"
	case PhaseValidator:
		return "Validator"
	case PhaseWorker:
		return "Workers"
	case PhaseDone:
		return "Done"
	default:
		return "Unknown"
	}
}

// TabInfo describes a single agent tab for chrome rendering.
type TabInfo struct {
	Name      string
	Index     int
	State     TabState
	Attention bool
	StartedAt time.Time
	ExitCode  int
}

// TabState describes the lifecycle state of a tab.
type TabState int

const (
	TabStateRunning TabState = iota
	TabStateDone
)

// LogEntry is a single log message for the chrome overlay.
type LogEntry struct {
	Time    time.Time
	Level   string
	Message string
}

// Snapshot captures the full state passed to the chrome overlay.
// Chrome is a snapshot view — no live updates while displayed.
type Snapshot struct {
	// Pipeline state.
	Phase PipelinePhase
	Goal  string

	// Tabs.
	Tabs      []TabInfo
	ActiveTab int

	// Recent log entries.
	Logs []LogEntry

	// Terminal dimensions.
	Width  int
	Height int
}
