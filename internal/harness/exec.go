package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// OutputMode controls the output format passed to the claude CLI.
type OutputMode int

const (
	// OutputStreamJSON is the default: --output-format stream-json with --verbose --include-partial-messages.
	OutputStreamJSON OutputMode = iota
	// OutputJSON uses --print --output-format json (batch, no streaming).
	OutputJSON
)

// SessionRef identifies a Claude session for continuation.
// Zero value means a new session; use ResumeSession to set a valid ref.
type SessionRef struct {
	ID    string
	Valid bool
}

// NewSession returns a zero SessionRef (start a fresh session).
func NewSession() SessionRef { return SessionRef{} }

// ResumeSession returns a SessionRef that resumes the given session ID.
func ResumeSession(id string) SessionRef { return SessionRef{ID: id, Valid: id != ""} }

// ProcessSpec is a pure value type: two identical specs run identical processes.
// Continuation is the explicit Resume field — not hidden session state.
// AgentID, SteerOnLoop, Timeout, and LoopGuard are runtime orchestration knobs —
// they do NOT enter buildSpecArgs, so identical subprocess args still imply
// identical subprocesses.
type ProcessSpec struct {
	Model        ModelSpec
	SystemPrompt string  // merged into --append-system-prompt
	Prompt       string  // initial -p prompt; empty when using input plane (in != nil)
	Resume       SessionRef
	WorkDir      string
	Binary       string     // "" => "claude"
	ExtraArgs    []string   // permission-mode, allowed/disallowed tools, MCP, etc.
	Inline       []InlineMCP
	Sandbox      SandboxConfig
	Output       OutputMode

	// Orchestration runtime knobs (not passed to subprocess).
	AgentID        string        // role label used by steering and report capture
	SteerOnLoop    bool          // enable steering executor loop detection
	Timeout        time.Duration // wall-clock cap; 0 means no limit
	LoopGuard      LoopGuardSpec // thresholds for the steering executor
	PreTimeoutNudge string       // role-specific message sent 60 s before deadline and on silence; "" = disabled
}

// Message is a user turn sent to a running process via the input plane.
type Message struct{ Text string }

// Sink is the one-way observation handle.
// Observe is called from a dedicated goroutine; it MUST NOT block.
// Implementations that may be slow buffer-and-drop internally.
type Sink interface{ Observe(Event) }

// Executor is the narrow seam that P2 (Step) depends on.
// Decorators (budget, retry) implement this interface.
type Executor interface {
	Run(ctx context.Context, spec ProcessSpec, in <-chan Message, sink Sink) (RunResult, error)
}

// NonZeroExitError is returned when the subprocess exits with a non-zero code.
type NonZeroExitError struct {
	Code   int
	Stderr string
}

func (e *NonZeroExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("claude CLI exited with code %d: %s", e.Code, e.Stderr)
	}
	return fmt.Sprintf("claude CLI exited with code %d", e.Code)
}

// Run executes spec as a claude CLI subprocess and returns EXACTLY ONCE.
//
// CONTRACT:
//   - Returns once, only after either (a) the process exited (stdout EOF +
//     cmd.Wait returned), or (b) ctx is Done — in which case Run SIGKILLs the
//     process GROUP (Setpgid always set), reaps it, and returns ctx.Err().
//   - A non-zero process exit returns a *NonZeroExitError, never swallowed.
//   - ctx is an argument, never stored.
//   - in (nil-safe) is the input plane: Run drains it in a dedicated writer
//     goroutine. Closing in closes stdin. Run NEVER blocks the control path
//     on in; a Post can never hang Run, and Run's termination is independent
//     of in.
//   - sink (nil-safe) is the output plane: one-way, best-effort, lossy. Run
//     pushes; nothing pulls. A slow/stuck/panicking sink cannot block Run.
//   - Run owns every goroutine/channel it creates and joins them before
//     returning. No channel the caller owns is closed by Run.
func Run(ctx context.Context, spec ProcessSpec, in <-chan Message, sink Sink) (RunResult, error) {
	binary := spec.Binary
	if binary == "" {
		binary = "claude"
	}

	hasInputPlane := in != nil
	args := buildSpecArgs(spec, hasInputPlane)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	env, err := buildEnvFromSpec(spec)
	if err != nil {
		return RunResult{}, fmt.Errorf("exec: build env: %w", err)
	}
	cmd.Env = env

	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}

	// Sandbox wrapping (before opening pipes).
	var sb *sandbox.Sandbox
	if spec.Sandbox.RepoPath != "" || len(spec.Sandbox.Profiles) > 0 {
		mEnv, mEnvErr := buildModelEnvFromSpec(spec)
		if mEnvErr != nil {
			return RunResult{}, fmt.Errorf("exec: sandbox model env: %w", mEnvErr)
		}
		sb, err = sandbox.New(sandbox.Config{
			RepoPath:     spec.Sandbox.RepoPath,
			WorktreePath: spec.Sandbox.WorktreePath,
			RepoWritable: spec.Sandbox.Writable,
			Profiles:     spec.Sandbox.Profiles,
			HarnessEnv:   append(mEnv, spec.Sandbox.Env...),
		})
		if err != nil {
			return RunResult{}, fmt.Errorf("exec: sandbox: %w", err)
		}
		if err := sb.Wrap(cmd); err != nil {
			_ = sb.Close()
			return RunResult{}, fmt.Errorf("exec: sandbox wrap: %w", err)
		}
	}

	var stdinPipe io.WriteCloser
	if hasInputPlane {
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			if sb != nil {
				_ = sb.Close()
			}
			return RunResult{}, fmt.Errorf("exec: stdin pipe: %w", err)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if sb != nil {
			_ = sb.Close()
		}
		return RunResult{}, fmt.Errorf("exec: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		if sb != nil {
			_ = sb.Close()
		}
		return RunResult{}, fmt.Errorf("exec: start: %w", err)
	}

	// runDone is closed when the process exits. The stdin writer goroutine
	// selects on both in and runDone so it exits cleanly on process termination.
	runDone := make(chan struct{})

	// stdin writer goroutine: drains in and writes NDJSON to stdin.
	// Exits when in is closed, ctx is done, or runDone fires.
	if hasInputPlane {
		go func() {
			defer stdinPipe.Close()
			for {
				select {
				case msg, ok := <-in:
					if !ok {
						return
					}
					data, err := json.Marshal(map[string]interface{}{
						"type":               "user",
						"message":            map[string]interface{}{"role": "user", "content": msg.Text},
						"parent_tool_use_id": nil,
					})
					if err != nil {
						continue
					}
					if _, err := stdinPipe.Write(append(data, '\n')); err != nil {
						return
					}
				case <-ctx.Done():
					return
				case <-runDone:
					return
				}
			}
		}()
	}

	// Sink goroutine: isolates the sink from the parse path.
	// A slow/panicking sink cannot block Run.
	var sinkCh chan Event
	var sinkDone chan struct{}
	if sink != nil {
		sinkCh = make(chan Event, 512)
		sinkDone = make(chan struct{})
		go func() {
			defer close(sinkDone)
			for ev := range sinkCh {
				func() {
					defer func() { recover() }() // never let a panicking sink kill Run
					sink.Observe(ev)
				}()
			}
		}()
	}

	// Bridge channel: parseStream writes here; we forward to sinkCh with drop.
	var parseEvents chan Event
	if sinkCh != nil {
		parseEvents = make(chan Event, 256)
		go func() {
			for ev := range parseEvents {
				select {
				case sinkCh <- ev:
				default: // drop — sink is lossy by contract
				}
			}
		}()
	}

	// Parse stdout (blocking: returns when stdout hits EOF).
	rawResult, isError, usage, sessionID, planFilePath, parseErr := parseStream(stdout, parseEvents)

	// Signal stdin writer that the process is done.
	close(runDone)

	// Close the parse→sink bridge; wait for the sink to drain.
	if parseEvents != nil {
		close(parseEvents)
	}
	if sinkCh != nil {
		close(sinkCh)
		<-sinkDone
	}
	if sb != nil {
		_ = sb.Close()
	}

	// Reap the process.
	cmdErr := cmd.Wait()

	if ctx.Err() != nil {
		return RunResult{
			Output:       rawResult,
			SessionID:    sessionID,
			PlanFilePath: planFilePath,
			Usage:        usage,
			ExitCode:     exitCode(cmdErr),
			Stderr:       stderrBuf.String(),
		}, ctx.Err()
	}

	if parseErr != nil {
		return RunResult{
			Output:       rawResult,
			SessionID:    sessionID,
			PlanFilePath: planFilePath,
			Usage:        usage,
			Stderr:       stderrBuf.String(),
		}, fmt.Errorf("exec: stream parse: %w", parseErr)
	}

	if isError {
		return RunResult{
			Output:       rawResult,
			SessionID:    sessionID,
			PlanFilePath: planFilePath,
			Usage:        usage,
			Stderr:       stderrBuf.String(),
		}, fmt.Errorf("exec: claude error: %s", rawResult)
	}

	if cmdErr != nil {
		code := exitCode(cmdErr)
		return RunResult{
			Output:       rawResult,
			SessionID:    sessionID,
			PlanFilePath: planFilePath,
			Usage:        usage,
			ExitCode:     code,
			Stderr:       stderrBuf.String(),
		}, &NonZeroExitError{Code: code, Stderr: stderrBuf.String()}
	}

	return RunResult{
		Output:       rawResult,
		SessionID:    sessionID,
		PlanFilePath: planFilePath,
		Usage:        usage,
	}, nil
}

// exitCode extracts the exit code from an *exec.ExitError; returns 1 otherwise.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

// buildSpecArgs consolidates all five CLI arg assembly sites into one builder.
// Ordering: -p → output-format → input-format (input plane only) →
// --verbose/--include-partial-messages (stream-json only) →
// --append-system-prompt → --resume → ExtraArgs → inline MCP merge.
func buildSpecArgs(spec ProcessSpec, hasInputPlane bool) []string {
	var args []string

	// -p prompt (empty string when input plane handles it via NDJSON stdin)
	prompt := spec.Prompt
	if hasInputPlane {
		prompt = ""
	}
	args = append(args, "-p", prompt)

	// Output format and associated flags
	switch spec.Output {
	case OutputJSON:
		args = append(args, "--output-format", "json", "--print")
	default: // OutputStreamJSON
		args = append(args, "--output-format", "stream-json")
		if hasInputPlane {
			args = append(args, "--input-format", "stream-json")
		}
		args = append(args, "--verbose", "--include-partial-messages")
	}

	// System prompt
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}

	// Session continuation
	if spec.Resume.Valid {
		args = append(args, "--resume", spec.Resume.ID)
	}

	// Caller-supplied extra args (allowedTools, disallowedTools, permission-mode, etc.)
	args = append(args, spec.ExtraArgs...)

	// Merge inline MCP servers into --mcp-config (same logic as buildFinalArgs).
	if len(spec.Inline) > 0 {
		args = mergeInlineMCP(args, spec.Inline)
	}

	return args
}

// mergeInlineMCP merges named inline MCP server definitions into an existing
// --mcp-config arg or appends a new one. Mirrors the logic in buildFinalArgs.
func mergeInlineMCP(args []string, inline []InlineMCP) []string {
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	var existing mcpConfig
	mcpIdx := -1
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			mcpIdx = i + 1
			if err := json.Unmarshal([]byte(args[mcpIdx]), &existing); err != nil {
				existing = mcpConfig{MCPServers: make(map[string]json.RawMessage)}
			}
			break
		}
	}
	if existing.MCPServers == nil {
		existing.MCPServers = make(map[string]json.RawMessage)
	}

	for _, srv := range inline {
		if srv.Name == "" {
			continue
		}
		def := struct {
			Command string   `json:"command"`
			Args    []string `json:"args,omitempty"`
		}{Command: srv.Command, Args: srv.Args}
		data, err := json.Marshal(def)
		if err != nil {
			continue
		}
		existing.MCPServers[srv.Name] = data
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		return args
	}

	out := make([]string, len(args))
	copy(out, args)
	if mcpIdx >= 0 {
		out[mcpIdx] = string(merged)
	} else {
		out = append(out, "--mcp-config", string(merged))
	}
	return out
}

// buildModelEnvFromSpec returns only the model-routing env vars derived from spec.Model.
// Used to inject ANTHROPIC_BASE_URL/ANTHROPIC_MODEL into the sandbox HarnessEnv so that
// sandboxed processes can reach the configured model regardless of spec.Sandbox.Env.
// Returns nil, nil for native-provider specs (no env override needed).
func buildModelEnvFromSpec(spec ProcessSpec) ([]string, error) {
	resolved := config.ResolvedModel{
		Type:    spec.Model.Provider,
		Model:   spec.Model.Model,
		BaseURL: spec.Model.BaseURL,
		APIKey:  spec.Model.APIKey,
	}
	var utility *config.ResolvedModel
	if spec.Model.SmallModel != "" {
		u := config.ResolvedModel{
			Type:    spec.Model.Provider,
			Model:   spec.Model.SmallModel,
			BaseURL: spec.Model.BaseURL,
		}
		utility = &u
	}
	return BuildModelEnv(resolved, utility)
}

// buildEnvFromSpec constructs the subprocess environment from a ProcessSpec.
// Reuses buildEnv logic: filters blocked parent vars, applies model env overrides.
func buildEnvFromSpec(spec ProcessSpec) ([]string, error) {
	resolved := config.ResolvedModel{
		Type:    spec.Model.Provider,
		Model:   spec.Model.Model,
		BaseURL: spec.Model.BaseURL,
		APIKey:  spec.Model.APIKey,
	}

	var utility *config.ResolvedModel
	if spec.Model.SmallModel != "" {
		u := config.ResolvedModel{
			Type:    spec.Model.Provider,
			Model:   spec.Model.SmallModel,
			BaseURL: spec.Model.BaseURL,
		}
		utility = &u
	}

	// Reuse the existing ClaudeCLI env builder via a temporary instance.
	// This correctly filters CLAUDE_CODE_SESSION_ID, ANTHROPIC_API_KEY, etc.
	tmp := &ClaudeCLI{resolved: resolved, small: utility}
	return tmp.buildEnv()
}

// RunFunc is a function that satisfies the Executor interface. Useful for tests.
type RunFunc func(ctx context.Context, spec ProcessSpec, in <-chan Message, sink Sink) (RunResult, error)

func (f RunFunc) Run(ctx context.Context, spec ProcessSpec, in <-chan Message, sink Sink) (RunResult, error) {
	return f(ctx, spec, in, sink)
}
