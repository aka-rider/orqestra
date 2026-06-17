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
