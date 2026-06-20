package harness

import (
	"github.com/xiii/orqestra/internal/sandbox"
)

// ModelSpec is a harness-internal model specification.
// No dependency on config.ResolvedModel — decouples config format from harness.
type ModelSpec struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	SmallModel string
}

// InlineMCP defines an MCP server to inject at runtime.
// Name is the key in the mcpServers map; empty entries are skipped.
type InlineMCP struct {
	Name    string
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// SandboxConfig configures seatbelt sandboxing.
// Zero value = no sandboxing (direct execution).
type SandboxConfig struct {
	RepoPath     string
	WorktreePath string
	Profiles     []sandbox.Snapshot
	Env          []string
	Writable     bool
}

// LoopGuardSpec configures the LoopBreaker middleware's loop-detection thresholds.
// Mirrors config.LoopGuard without importing the config package.
type LoopGuardSpec struct {
	RepeatThreshold int // identical tool calls before nudging
	MaxNudges       int // nudges before escalating to cancel
	CooldownTurns   int // turns to skip checking after a nudge
}

// SilenceGuardSpec configures the SilenceDetector middleware.
// Zero value disables silence detection.
type SilenceGuardSpec struct {
	SilenceSecs int    // seconds of event-stream silence before nudging; 0 = disabled
	NudgeText   string // "" falls back to spec.PreTimeoutNudge
}

// EventKind identifies the type of a Runner event.
type EventKind int

const (
	EventChunk EventKind = iota
	EventToolUse
	EventToolResult
	EventUsage
	EventSessionStart
	EventSessionDone
	EventError
)

// Event is a typed event emitted during Claude CLI streaming.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	Detail    string
	Args      string // compact JSON of tool input; used for loop fingerprinting
	Input     int64
	Output    int64
	SessionID string
	IsDelta   bool // true for content_block_delta (partial text)
	IsError   bool
}

// ErrBudgetExhausted is returned when a token budget is exceeded.
var ErrBudgetExhausted = &BudgetExhaustedError{}

// BudgetExhaustedError is returned when the token budget is exceeded.
type BudgetExhaustedError struct{}

func (*BudgetExhaustedError) Error() string { return "token budget exhausted" }
