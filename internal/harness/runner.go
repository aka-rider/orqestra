package harness

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

// SandboxConfig configures per-role leash sandboxing. Zero value = no
// sandboxing (direct execution) — RepoPath is the discriminator Run checks.
//
// RepoPath is the sandbox's primary grant: writable when Writable is true,
// read-only otherwise. WorktreePath, when set, is always granted write
// (worktree isolation keeps the repo read-only while the worktree stays
// writable). Reads/Writes/Execs carry additional grants translated from
// config.SandboxConfig's allow_read/allow_write/allow_exec plus role-specific
// extras (the orqestra self-exec grant, worktree .git-internals writes).
// FutureWrites grants write on paths that don't exist yet but may be created
// by the child inside the sandbox (git's packed-refs lock in worktree mode).
// Env carries the harness-computed model-routing env (unchanged from today);
// ExtraEnv/ProxyEnv carry the user's sandbox.extra_env/proxy_env config.
type SandboxConfig struct {
	RepoPath     string
	WorktreePath string
	Writable     bool
	Reads        []string
	Writes       []string
	Execs        []string
	FutureWrites []string
	Env          []string
	ExtraEnv     map[string]string
	ProxyEnv     []string
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
	MaxNudges   int    // nudges tolerated after a confirmed empty turn before escalating; <=0 = default (3)
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
