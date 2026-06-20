package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// Runner is the unified interface for all Claude CLI invocations.
// It is always sandboxed — sandboxing is configured at construction time.
type Runner interface {
	// Post sends a user message over NDJSON stdin. Fire-and-forget.
	Post(string)

	// Receive returns the single channel for all output events from the session.
	// The channel closes when the process exits.
	Receive() <-chan Event

	// ExtractPlan pulls plan content from the currently running session's
	// metadata. Uses internally stored sessionID and cwd to locate the
	// Claude CLI JSONL log and extract the plan file path.
	ExtractPlan(ctx context.Context) (string, error)

	// SetEvents injects an events channel for stream capture. Called once
	// before Post(). If nil, events are not forwarded (nil-safe).
	SetEvents(chan<- Event)

	// SessionID returns the Claude session identifier extracted from the
	// stream. Empty until the first session event is parsed.
	SessionID() string

	// Cancel terminates the session immediately. For processes not yet
	// started, it is a no-op. For running processes, it sends SIGKILL
	// to the process group.
	Cancel() error
}

// RunnerFactory creates a Runner from configuration.
type RunnerFactory func(RunnerConfig, context.Context) (Runner, error)

// ModelSpec is a harness-internal model specification.
// No dependency on config.ResolvedModel — decouples config format from harness.
type ModelSpec struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	SmallModel string
}

// RunnerConfig is the single source of truth for runner construction.
type RunnerConfig struct {
	Model          ModelSpec
	SystemPrompt   string
	SessionID      string            // empty = new session
	Binary         string
	WorkDir        string
	ExtraArgs      []string
	SmallModel     *ModelSpec
	MCPConfig      map[string][]string // server -> allowed tools
	AllowedTools   []string
	DisallowedTools []string
	PermissionMode string
	Settings       json.RawMessage
	InlineMCPServers map[string]InlineMCP
	Sandbox        SandboxConfig
	BudgetLimit    int64 // if > 0, orchestrator.BudgetGuard enforces this limit
}

// InlineMCP defines an MCP server to inject at runtime.
type InlineMCP struct {
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
// All output flows through a single channel (Runner.Receive()).
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

// NewRunner creates a Runner from configuration.
// If cfg.Sandbox is non-empty, it creates a sandbox-wrapped runner.
// Otherwise, it creates a direct ClaudeCLI runner.
// Both paths share the same Post/Receive/ExtractPlan/SetEvents/SessionID/Cancel implementation.
func NewRunner(cfg RunnerConfig, ctx context.Context) (Runner, error) {
	cli := &ClaudeCLI{
		binary:             cfg.Binary,
		workDir:            cfg.WorkDir,
		extraArgs:          cfg.ExtraArgs,
		inlineMCPServers:   make(map[string]inlineMCPDef),
	}

	// Apply runner config options.
	if cfg.Model.Provider != "" {
		cli.resolved = config.ResolvedModel{
			Type:    cfg.Model.Provider,
			Model:   cfg.Model.Model,
			BaseURL: cfg.Model.BaseURL,
			APIKey:  cfg.Model.APIKey,
		}
	}
	if cfg.SmallModel != nil {
		cli.small = &config.ResolvedModel{
			Type:    cfg.SmallModel.Provider,
			Model:   cfg.SmallModel.Model,
			BaseURL: cfg.SmallModel.BaseURL,
			APIKey:  cfg.SmallModel.APIKey,
		}
	}
	if cfg.SystemPrompt != "" {
		cli.appendSystemPrompt = cfg.SystemPrompt
	}
	if len(cfg.InlineMCPServers) > 0 {
		for name, mcp := range cfg.InlineMCPServers {
			cli.inlineMCPServers[name] = inlineMCPDef{Command: mcp.Command, Args: mcp.Args}
		}
	}
	if cfg.PermissionMode != "" {
		cli.extraArgs = append(cli.extraArgs, "--permission-mode", cfg.PermissionMode)
	}
	if len(cfg.AllowedTools) > 0 {
		cli.extraArgs = append(cli.extraArgs, "--allowedTools", jsonStr(cfg.AllowedTools))
	}
	if len(cfg.DisallowedTools) > 0 {
		cli.extraArgs = append(cli.extraArgs, "--disallowedTools", jsonStr(cfg.DisallowedTools))
	}
	if len(cfg.ExtraArgs) > 0 {
		cli.extraArgs = append(cli.extraArgs, cfg.ExtraArgs...)
	}

	// If sandbox config is non-empty, wrap with sandboxedRunner.
	if cfg.Sandbox.RepoPath != "" || len(cfg.Sandbox.Profiles) > 0 {
		return &sandboxedRunner{
			cli:          cli,
			sandboxCfg:   cfg.Sandbox,
			ctx:          ctx,
			stdin:        nil,
			cmd:          nil,
			sessionID:    "",
			initialized:  false,
		}, nil
	}

	return cli, nil
}

// jsonStr returns a JSON array string for CLI args.
func jsonStr(s []string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// sandboxedRunner wraps a ClaudeCLI and applies sandboxing during Post().
type sandboxedRunner struct {
	cli         *ClaudeCLI
	sandboxCfg  SandboxConfig
	ctx         context.Context
	mu          sync.Mutex
	stdin       io.WriteCloser
	cmd         *exec.Cmd
	sessionID   string
	initialized bool
}

// Post sends a user message over NDJSON stdin, applying sandboxing if needed.
func (r *sandboxedRunner) Post(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.initialized {
		if err := r.init(); err != nil {
			return
		}
	}

	if r.stdin == nil {
		return
	}

	ndjson := map[string]interface{}{
		"type":               "user",
		"message":            map[string]interface{}{"role": "user", "content": msg},
		"parent_tool_use_id": nil,
		"session_id":         r.sessionID,
	}
	data, err := json.Marshal(ndjson)
	if err != nil {
		return
	}
	if _, err := r.stdin.Write(append(data, '\n')); err != nil {
	}
}

func (r *sandboxedRunner) Receive() <-chan Event {
	// Delegate to the wrapped ClaudeCLI.
	return r.cli.Receive()
}

func (r *sandboxedRunner) ExtractPlan(ctx context.Context) (string, error) {
	return r.cli.ExtractPlan(ctx)
}

func (r *sandboxedRunner) SetEvents(ch chan<- Event) {
	r.cli.SetEvents(ch)
}

func (r *sandboxedRunner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

func (r *sandboxedRunner) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		return syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	}
	return nil
}

// init sets up the sandbox and starts the Claude CLI process.
func (r *sandboxedRunner) init() error {
	if r.initialized {
		return nil
	}
	defer func() { r.initialized = true }()

	// Create sandbox.
	sb, err := sandbox.New(sandbox.Config{
		RepoPath:     r.sandboxCfg.RepoPath,
		WorktreePath: r.sandboxCfg.WorktreePath,
		RepoWritable: r.sandboxCfg.Writable,
		Profiles:     r.sandboxCfg.Profiles,
		HarnessEnv:   r.sandboxCfg.Env,
	})
	if err != nil {
		return fmt.Errorf("sandboxedRunner: create sandbox: %w", err)
	}

	// Build args for the claude CLI.
	args := []string{
		"-p", "", // empty prompt — we send it via NDJSON
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	// Build command. Honor the configured binary (the `binary` config knob);
	// fall back to "claude" when unset so production behavior is unchanged.
	// DEFECT-06: this path previously hardcoded "claude", silently ignoring the
	// binary config. Honoring it is also what lets QA gates inject a replay stub.
	bin := r.cli.binary
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(r.ctx, bin, args...)
	cmd.Env = r.sandboxCfg.Env

	if r.cli.workDir != "" {
		cmd.Dir = r.cli.workDir
	}

	// Wrap with sandbox.
	if err := sb.Wrap(cmd); err != nil {
		_ = sb.Close()
		return fmt.Errorf("sandboxedRunner: wrap command: %w", err)
	}

	// Open pipes.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = sb.Close()
		return fmt.Errorf("sandboxedRunner: stdin pipe: %w", err)
	}
	r.stdin = stdin

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = sb.Close()
		return fmt.Errorf("sandboxedRunner: stdout pipe: %w", err)
	}

	// Start process.
	if err := cmd.Start(); err != nil {
		_ = sb.Close()
		return fmt.Errorf("sandboxedRunner: start: %w", err)
	}
	r.cmd = cmd

	// Drain stdout in background.
	events := make(chan Event, 256)
	go func() {
		defer sb.Close()
		for ev := range events {
			r.cli.mu.Lock()
			sess := r.cli.session
			r.cli.mu.Unlock()
			if sess != nil {
				select {
				case sess.events <- ev:
				default:
				}
			}
		}
	}()
	go func() {
		parseStreamLines(cmdStdout, events)
		_ = cmd.Wait()
	}()

	return nil
}
