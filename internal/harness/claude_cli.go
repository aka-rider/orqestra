package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/xiii/orqestra/internal/config"
)

// TokenUsage captures token consumption from an LLM call.
type TokenUsage struct {
	Input  int64
	Output int64
}

// Total returns the sum of input and output tokens.
func (u TokenUsage) Total() int64 { return u.Input + u.Output }

// RunResult captures the output and token usage from a CLIRunner invocation.
// Kept for backward compatibility with existing callers.
type RunResult struct {
	Output       string
	Usage        TokenUsage // zero value if the harness did not report usage
	SessionID    string     // populated from stream-json result event when available
	PlanFilePath string     // plan file path captured from result event (may be empty)
}

// CLIRunner is the legacy interface for running claude CLI commands.
// Deprecated: use Runner interface instead.
type CLIRunner interface {
	RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error)
	RunStreaming(ctx context.Context, prompt, systemPrompt string, events chan<- Event) (RunResult, error)
}

// ContinuableRunner extends Runner with session continuation support.
// Deprecated: use Runner interface instead.
type ContinuableRunner interface {
	Runner
	// RunContinue resumes a previous session with a new prompt.
	// The session retains its tool state, conversation history, and sandbox.
	RunContinue(ctx context.Context, sessionID, prompt string, events chan<- Event) (RunResult, error)
}

// ClaudeCLI implements the Runner interface for the claude CLI binary.
type ClaudeCLI struct {
	resolved           config.ResolvedModel
	small              *config.ResolvedModel // optional small/fast model
	extraArgs          []string
	binary             string                  // path to claude binary, defaults to "claude"
	inlineMCPServers   map[string]inlineMCPDef // MCP servers injected at runtime
	appendSystemPrompt string                  // text appended to default system prompt via --append-system-prompt
	workDir            string                  // working directory for subprocess; empty inherits process CWD
	mu                 sync.Mutex
	session            *session
}

// inlineMCPDef defines an MCP server to inject into --mcp-config.
type inlineMCPDef struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// NewClaudeCLI creates a Runner backed by the claude CLI binary.
func NewClaudeCLI(resolved config.ResolvedModel, opts ...ClaudeCLIOption) *ClaudeCLI {
	c := &ClaudeCLI{
		resolved: resolved,
		binary:   "claude",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewClaudeCLIFromConfig creates a Runner from a model_ref.
// Returns an error if modelRef is empty or cannot be resolved.
// Model-level runtime options are applied before caller-supplied options.
// The returned value implements both Runner and ContinuableRunner.
func NewClaudeCLIFromConfig(cfg *config.Config, modelRef string, opts ...ClaudeCLIOption) (ContinuableRunner, error) {
	if modelRef == "" {
		return nil, fmt.Errorf("missing model_ref")
	}
	resolved, err := cfg.ResolveModel(modelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve model_ref %q: %w", modelRef, err)
	}
	mopts, err := modelOptions(cfg, modelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve model options for %q: %w", modelRef, err)
	}
	return NewClaudeCLI(resolved, append(mopts, opts...)...), nil
}

func modelOptions(cfg *config.Config, modelRef string) ([]ClaudeCLIOption, error) {
	runtime, err := cfg.RuntimeOptions(modelRef)
	if err != nil {
		return nil, fmt.Errorf("runtime options: %w", err)
	}
	var opts []ClaudeCLIOption
	if runtime.Binary != "" {
		opts = append(opts, WithBinary(runtime.Binary))
	}
	if utility := cfg.ResolveUtilityModel(); utility != nil {
		opts = append(opts, WithSmallModel(*utility))
	}
	return opts, nil
}

// ClaudeCLIOption configures ClaudeCLI.
type ClaudeCLIOption func(*ClaudeCLI)

// WithSmallModel sets a small/fast model for cheap inference.
func WithSmallModel(small config.ResolvedModel) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.small = &small
	}
}

// WithExtraArgs appends extra CLI arguments.
func WithExtraArgs(args ...string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.extraArgs = append(c.extraArgs, args...)
	}
}

// WithNoTools disables all built-in and MCP tools, making the CLI a pure
// text-in/text-out runner. This dramatically reduces input token count and
// prevents the model from entering agentic tool-use mode.
func WithNoTools() ClaudeCLIOption {
	return WithExtraArgs("--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
}

// WithMCPServers restricts which MCP servers start. Reads the user's MCP config
// and passes only the named servers via --strict-mcp-config. Servers not in the
// list never start and their tool definitions never reach the model.
// An empty slice means no MCP servers (equivalent to WithNoTools for MCP).
func WithMCPServers(names []string) ClaudeCLIOption {
	mcpCfg := filterMCPConfig(names)
	return WithExtraArgs("--strict-mcp-config", "--mcp-config", mcpCfg)
}

// WithAllowedTools restricts the CLI to only the specified tools.
// Tool names support patterns (e.g. "mcp__context7__*", "Read", "Grep").
func WithAllowedTools(tools []string) ClaudeCLIOption {
	return WithExtraArgs("--allowedTools", strings.Join(tools, ","))
}

// WithDisallowedTools blocks the specified tools from the CLI.
// Tool names support patterns (e.g. "mcp__MCP_DOCKER__*", "Bash").
func WithDisallowedTools(tools []string) ClaudeCLIOption {
	return WithExtraArgs("--disallowedTools", strings.Join(tools, ","))
}

// WithAppendSystemPrompt adds text to the --append-system-prompt flag.
// This preserves Claude Code's default identity, tool guidance, CLAUDE.md
// rules, and safety instructions (layer 4) while adding extra rules (layer 5).
// Multiple sources (role system prompt + this option) are merged at invocation.
func WithAppendSystemPrompt(text string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.appendSystemPrompt = text
	}
}

// mergeAppendPrompts concatenates non-empty prompt fragments into a single
// --append-system-prompt value. All system prompt steering now goes through
// layer 5 (append) to preserve CLAUDE.md and the default prompt (layer 4).
func mergeAppendPrompts(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// WithPermissionMode sets the --permission-mode flag (e.g. "plan", "default").
func WithPermissionMode(mode string) ClaudeCLIOption {
	if mode == "" {
		return func(*ClaudeCLI) {}
	}
	return WithExtraArgs("--permission-mode", mode)
}

// WithSettings passes a JSON settings object to the CLI via --settings.
// Example: WithSettings(`{"plansDirectory":"/tmp/plans"}`)
func WithSettings(jsonStr string) ClaudeCLIOption {
	return WithExtraArgs("--settings", jsonStr)
}

// WithBinary overrides the claude binary path.
func WithBinary(path string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		c.binary = path
	}
}

// WithInlineMCPServer injects an MCP server definition that will be merged
// into the --mcp-config JSON at CLI invocation time. This is used to inject
// the orqestra question bridge as an MCP tool available to the model.
// When no explicit WithMCPServers filtering is active, inline servers are
// additive — the user's default MCPs from ~/.claude.json remain available.
func WithInlineMCPServer(name, command string, args []string) ClaudeCLIOption {
	return func(c *ClaudeCLI) {
		if c.inlineMCPServers == nil {
			c.inlineMCPServers = make(map[string]inlineMCPDef)
		}
		c.inlineMCPServers[name] = inlineMCPDef{Command: command, Args: args}
	}
}

// WithWorkDir sets the working directory for the claude subprocess.
// When empty (the zero value), the subprocess inherits the process CWD.
func WithWorkDir(dir string) ClaudeCLIOption {
	return func(c *ClaudeCLI) { c.workDir = dir }
}

// --- Runner interface implementation ---

// session holds the state for a running Claude CLI session.
type session struct {
	runner      *ClaudeCLI
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	sessionID   string
	planPath    string
	usage       TokenUsage
	resultError bool
	isError     bool
	done        chan struct{}
	events      chan Event
	mu          sync.Mutex
}

// Post sends a user message over NDJSON stdin.
// For the first call on a new session, it starts the process and sends
// the initial prompt as NDJSON. For subsequent calls, it sends follow-up
// messages as NDJSON.
func (c *ClaudeCLI) Post(msg string) {
	sess := c.initSession()

	// Start the process on first message.
	sess.mu.Lock()
	if sess.cmd == nil {
		sess.mu.Unlock()
		c.mu.Lock()
		c.startSession(sess, msg)
		c.mu.Unlock()
	} else {
		sess.mu.Unlock()
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	ndjson := map[string]interface{}{
		"type":               "user",
		"message":            map[string]interface{}{"role": "user", "content": msg},
		"parent_tool_use_id": nil,
		"session_id":         sess.sessionID,
	}
	data, err := json.Marshal(ndjson)
	if err != nil {
		slog.Error("ClaudeCLI.Post: marshal message", "err", err)
		return
	}
	if _, err := sess.stdin.Write(append(data, '\n')); err != nil {
		slog.Error("ClaudeCLI.Post: write to stdin", "err", err)
	}
}

// Receive returns the single channel for all output events from the session.
// The channel closes when the process exits.
func (c *ClaudeCLI) Receive() <-chan Event {
	sess := c.initSession()
	return sess.events
}

// ExtractPlan pulls plan content from the currently running session's
// metadata. Uses internally stored sessionID and cwd to locate the
// Claude CLI JSONL log and extract the plan file path.
func (c *ClaudeCLI) ExtractPlan(ctx context.Context) (string, error) {
	sess := c.initSession()
	sess.mu.Lock()
	sid := sess.sessionID
	sess.mu.Unlock()

	if sid == "" {
		return "", fmt.Errorf("no session ID available for plan extraction")
	}

	// Resolve session JSONL and extract plan file path.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}

	jsonlPath, err := ResolveSessionLogPath(cwd, sid)
	if err != nil {
		return "", fmt.Errorf("resolve session log for %s: %w", sid, err)
	}

	planFilePath, err := ExtractPlanFilePath(jsonlPath)
	if err != nil {
		// Fallback: scan ~/.claude/plans/ for recently modified .md files.
		slog.Debug("no plan_mode attachment in JSONL, scanning plans directory",
			"session_id", sid, "err", err)
		return scanPlansDirectoryFallback()
	}

	content, err := readSecurePlanFile(planFilePath)
	if err != nil {
		return "", fmt.Errorf("read plan file for session %s: %w", sid, err)
	}
	return strings.TrimSpace(content), nil
}

// SetEvents injects an events channel for stream capture. Called once
// before Post(). If nil, events are not forwarded (nil-safe).
func (c *ClaudeCLI) SetEvents(ch chan<- Event) {
	sess := c.initSession()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// Store the channel as a regular chan Event for internal use.
	// The caller's chan<- Event is converted to chan Event.
	// If ch is nil, events are not forwarded (nil-safe).
	if ch != nil {
		sess.events = make(chan Event, 512)
		// Fan-out: copy events from internal channel to the injected channel.
		go func() {
			for ev := range sess.events {
				select {
				case ch <- ev:
				default:
				}
			}
		}()
	} else {
		sess.events = make(chan Event, 512)
	}
}

// SessionID returns the Claude session identifier extracted from the stream.
// Empty until the first session event is parsed.
func (c *ClaudeCLI) SessionID() string {
	sess := c.initSession()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.sessionID
}

// Cancel terminates the session immediately. For processes not yet
// started, it is a no-op. For running processes, it sends SIGKILL
// to the process group.
func (c *ClaudeCLI) Cancel() error {
	sess := c.initSession()
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.cmd == nil || sess.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-sess.cmd.Process.Pid, syscall.SIGKILL)
}

// initSession returns the session, creating it lazily on first access.
// The process is started lazily on the first Post() call via startSession.
func (c *ClaudeCLI) initSession() *session {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session != nil {
		return c.session
	}

	s := &session{
		runner:   c,
		done:     make(chan struct{}),
		events:   make(chan Event, 512),
	}
	c.session = s
	return s
}

// startSession starts the Claude CLI process and begins draining stdout.
// Called from Post() on the first message. The caller must hold c.mu.
func (c *ClaudeCLI) startSession(s *session, initialPrompt string) {
	args := []string{
		"-p", initialPrompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if merged := mergeAppendPrompts("", c.appendSystemPrompt); merged != "" {
		args = append(args, "--append-system-prompt", merged)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.Command(c.binary, args...)
	env, err := c.buildEnv()
	if err != nil {
		slog.Error("ClaudeCLI.startSession: build env", "err", err)
		return
	}
	cmd.Env = env
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("ClaudeCLI.startSession: stdout pipe", "err", err)
		return
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		slog.Error("ClaudeCLI.startSession: stdin pipe", "err", err)
		return
	}

	if err := cmd.Start(); err != nil {
		slog.Error("ClaudeCLI.startSession: start", "err", err)
		return
	}

	s.cmd = cmd
	s.stdin = stdin

	// Drain stdout in background.
	go func() {
		parseStream(cmdStdout, s.events)
		cmd.Wait()
		close(s.events)
	}()
}

// --- Legacy methods (backward compatibility) ---

// RunPrint runs `claude --print -p <prompt> --append-system-prompt <systemPrompt> --output-format json`
// and returns the output.
func (c *ClaudeCLI) RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error) {
	args := []string{"--print", "-p", prompt, "--output-format", "json"}
	if merged := mergeAppendPrompts(systemPrompt, c.appendSystemPrompt); merged != "" {
		args = append(args, "--append-system-prompt", merged)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	env, err := c.buildEnv()
	if err != nil {
		return RunResult{}, err
	}
	cmd.Env = env
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI error: %w (stderr: %s)", err, stderr.String())
	}

	raw := stdout.String()

	// Extract token usage from JSON envelope if present.
	var envelope struct {
		Usage *streamUsage `json:"usage"`
	}
	var usage TokenUsage
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Usage != nil {
		usage = TokenUsage{
			Input:  envelope.Usage.InputTokens,
			Output: envelope.Usage.OutputTokens,
		}
	}

	return RunResult{Output: raw, Usage: usage}, nil
}

// RunStreaming runs `claude -p <prompt> --append-system-prompt <systemPrompt> --output-format stream-json --verbose`
// and streams displayable content to stdout. Returns the final result text.
func (c *ClaudeCLI) RunStreaming(ctx context.Context, prompt, systemPrompt string, events chan<- Event) (RunResult, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if merged := mergeAppendPrompts(systemPrompt, c.appendSystemPrompt); merged != "" {
		args = append(args, "--append-system-prompt", merged)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	env, err := c.buildEnv()
	if err != nil {
		return RunResult{}, err
	}
	cmd.Env = env
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("claude CLI stdout pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI start error: %w", err)
	}

	var result string
	var resultIsError bool
	var usage TokenUsage
	var sessionID string
	var planFilePath string
	var parseErr error
	result, resultIsError, usage, sessionID, planFilePath, parseErr = parseStream(cmdStdout, events)

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		// If we got a result with is_error:true, surface that as the error message
		if resultIsError && result != "" {
			return RunResult{}, fmt.Errorf("claude CLI error: %s", result)
		}
		return RunResult{}, fmt.Errorf("claude CLI error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if parseErr != nil {
		return RunResult{}, fmt.Errorf("claude CLI stream parse error: %w", parseErr)
	}

	if resultIsError {
		return RunResult{}, fmt.Errorf("claude CLI error: %s", result)
	}

	if result == "" {
		return RunResult{}, fmt.Errorf("claude CLI produced no result message in stream")
	}

	return RunResult{Output: result, Usage: usage, SessionID: sessionID, PlanFilePath: planFilePath}, nil
}

// RunContinue resumes a previous Claude CLI session with a follow-up prompt.
// Used for worker self-validation: the worker validates its own work in the
// same session that performed the implementation.
func (c *ClaudeCLI) RunContinue(ctx context.Context, sessionID, prompt string, events chan<- Event) (RunResult, error) {
	args := []string{"claude", "--resume", sessionID, "-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	env, err := c.buildEnv()
	if err != nil {
		return RunResult{}, err
	}
	cmd.Env = env
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("claude CLI stdout pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("claude CLI start error: %w", err)
	}

	var result string
	var resultIsError bool
	var usage TokenUsage
	var newSessionID string
	var planFilePath string
	var parseErr error
	result, resultIsError, usage, newSessionID, planFilePath, parseErr = parseStream(cmdStdout, events)

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		if resultIsError && result != "" {
			return RunResult{}, fmt.Errorf("claude CLI continue error: %s", result)
		}
		return RunResult{}, fmt.Errorf("claude CLI continue error: %w (stderr: %s)", cmdErr, stderr.String())
	}

	if parseErr != nil {
		return RunResult{}, fmt.Errorf("claude CLI stream parse error: %w", parseErr)
	}

	if resultIsError {
		return RunResult{}, fmt.Errorf("claude CLI continue error: %s", result)
	}

	return RunResult{Output: result, Usage: usage, SessionID: newSessionID, PlanFilePath: planFilePath}, nil
}

// --- Stream parsing ---

// parseStream scans stream-json lines from r, dispatches display events to
// events (nil-safe), and accumulates the result, error state, usage, session
// ID, and plan file path from the result event. This is the single source of
// truth for Claude CLI stream parsing — used by RunStreaming and RunContinue.
func parseStream(r io.Reader, events chan<- Event) (result string, isError bool, usage TokenUsage, sessionID, planFilePath string, err error) {
	var raw string
	raw, err = parseStreamLines(r, events)
	if err != nil {
		return
	}
	// Extract result, usage, session ID, and plan file path from raw NDJSON.
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type         string          `json:"type"`
			Result       string          `json:"result,omitempty"`
			IsError      bool            `json:"is_error,omitempty"`
			SessionID    string          `json:"session_id,omitempty"`
			PlanFilePath string          `json:"planFilePath,omitempty"`
			Usage        *streamUsage    `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "result" {
			result = event.Result
			isError = event.IsError
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
			if event.PlanFilePath != "" {
				planFilePath = event.PlanFilePath
			}
			if event.Usage != nil {
				usage = TokenUsage{
					Input:  event.Usage.InputTokens,
					Output: event.Usage.OutputTokens,
				}
			}
		}
	}
	return
}

// parseStreamLines reads stream-json NDJSON from src line by line, routes each
// parsed event through emitStreamEvents, and returns
// the full raw NDJSON for post-processing (usage + session ID extraction).
// events may be nil. Non-JSON lines are emitted as Text events when provided.
func parseStreamLines(src io.Reader, events chan<- Event) (string, error) {
	var rawBuf bytes.Buffer
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		rawBuf.Write(line)
		rawBuf.WriteByte('\n')
		if len(line) == 0 {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Debug("non-JSON stream line from claude CLI", "err", err, "line_len", len(line))
			if events != nil {
				events <- Event{Kind: EventChunk, Text: string(line) + "\n"}
			}
			continue
		}

		if event.SessionID != "" && events != nil {
			events <- Event{Kind: EventChunk, Text: "[session:" + event.SessionID + "]"}
		}

		emitStreamEvents(event, events)

		// Emit usage on any event carrying token stats.
		if event.Usage != nil && events != nil {
			events <- Event{
				Kind:   EventUsage,
				Input:  event.Usage.InputTokens,
				Output: event.Usage.OutputTokens,
			}
		}

		_ = event.Type // consumed by emitStreamEvents
	}
	if err := scanner.Err(); err != nil {
		return rawBuf.String(), err
	}
	return rawBuf.String(), nil
}

// streamEvent represents a parsed event from Claude CLI's stream-json output.
type streamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype,omitempty"`
	Result       string          `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Delta        streamDeltaText `json:"delta,omitempty"`
	Message      json.RawMessage `json:"message,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
	Usage        *streamUsage    `json:"usage,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PlanFilePath string          `json:"planFilePath,omitempty"` // plan file path from result event
	Event        json.RawMessage `json:"event,omitempty"`        // inner event for stream_event wrapper
}

// streamUsage captures token usage from the Claude CLI result event.
type streamUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type streamDeltaText struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// extractAssistantText pulls text content from an assistant message event.
func (e *streamEvent) extractAssistantText() string {
	if e.Message == nil {
		return ""
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

type toolUseBlock struct {
	Name  string
	Input json.RawMessage
}

func (e *streamEvent) extractAssistantToolUses() []toolUseBlock {
	if e.Message == nil {
		return nil
	}
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return nil
	}
	var tools []toolUseBlock
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			tools = append(tools, toolUseBlock{Name: block.Name, Input: block.Input})
		}
	}
	return tools
}

// extractToolUse extracts the tool name and input args from a content_block_start event.
// Claude CLI emits: {"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{...}}}
func (e *streamEvent) extractToolUse() (name string, args json.RawMessage) {
	if e.ContentBlock == nil {
		return "", nil
	}
	var block struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(e.ContentBlock, &block); err == nil && block.Type == "tool_use" {
		return block.Name, block.Input
	}
	return "", nil
}

// emitStreamEvents converts a stream-json event into typed Event entries.
// Used by both ClaudeCLI and SandboxCLIRunner to avoid duplicating switch logic.
func emitStreamEvents(event streamEvent, events chan<- Event) {
	if events == nil {
		return
	}
	for _, e := range streamEventsFrom(event) {
		events <- e
	}
}

func streamEventsFrom(event streamEvent) []Event {
	var out []Event
	switch event.Type {
	case "assistant":
		if text := event.extractAssistantText(); text != "" {
			out = append(out, Event{Kind: EventChunk, Text: text})
		}
		for _, tu := range event.extractAssistantToolUses() {
			out = append(out, Event{Kind: EventToolUse, Tool: tu.Name, Detail: ToolDetail(tu.Name, tu.Input)})
		}
	case "content_block_delta":
		if event.Delta.Text != "" {
			out = append(out, Event{Kind: EventChunk, Text: event.Delta.Text, IsDelta: true})
		}
	case "content_block_start":
		if name, args := event.extractToolUse(); name != "" {
			out = append(out, Event{Kind: EventToolUse, Tool: name, Detail: ToolDetail(name, args)})
		}
	case "stream_event":
		if event.Event == nil {
			return out
		}
		var inner streamEvent
		if err := json.Unmarshal(event.Event, &inner); err != nil {
			return out
		}
		switch inner.Type {
		case "content_block_start":
			if name, args := inner.extractToolUse(); name != "" {
				out = append(out, Event{Kind: EventToolUse, Tool: name, Detail: ToolDetail(name, args)})
			}
		case "content_block_delta":
			if inner.Delta.Text != "" {
				out = append(out, Event{Kind: EventChunk, Text: inner.Delta.Text, IsDelta: true})
			}
		}
	}
	return out
}

// --- Environment and args ---

// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
// Returns an error if the provider type is empty or unknown — no fallback to native.
func BuildModelEnv(resolved config.ResolvedModel, utility *config.ResolvedModel) ([]string, error) {
	switch resolved.Type {
	case config.ProviderTypeNative:
		// no override — use native Claude CLI with logged-in credentials
		return nil, nil
	case config.ProviderTypeAnthropic:
		env := []string{
			"ANTHROPIC_BASE_URL=" + resolved.BaseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	case config.ProviderTypeOpenAI:
		baseURL := strings.TrimRight(resolved.BaseURL, "/")
		env := []string{
			"ANTHROPIC_BASE_URL=" + baseURL,
			"ANTHROPIC_MODEL=" + resolved.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + resolved.Model,
		}
		if utility != nil {
			env = append(env,
				"ANTHROPIC_SMALL_FAST_MODEL="+utility.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+utility.Model,
			)
		}
		return env, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q for model %q (valid: %q, %q, %q)",
			resolved.Type, resolved.Model,
			config.ProviderTypeNative, config.ProviderTypeAnthropic, config.ProviderTypeOpenAI)
	}
}

// buildEnv constructs the environment variables for the claude subprocess.
func (c *ClaudeCLI) buildEnv() ([]string, error) {
	modelEnv, err := BuildModelEnv(c.resolved, c.small)
	if err != nil {
		return nil, fmt.Errorf("build model env: %w", err)
	}
	// Filter out any existing ANTHROPIC_API_KEY from the parent environment
	// to prevent leakage or conflicts with the runner's configured auth.
	var clean []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			clean = append(clean, kv)
		}
	}
	return append(clean, modelEnv...), nil
}

// filterMCPConfig reads the user's ~/.claude.json MCP server definitions and
// returns a JSON string containing only the named servers. This is passed to
// --mcp-config so only selected servers start, keeping token overhead minimal.
func filterMCPConfig(names []string) string {
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("cannot determine home dir for MCP config", "err", err)
		return `{"mcpServers":{}}`
	}

	data, err := os.ReadFile(home + "/.claude.json")
	if err != nil {
		slog.Debug("no ~/.claude.json found, using empty MCP config")
		return `{"mcpServers":{}}`
	}

	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse ~/.claude.json", "err", err)
		return `{"mcpServers":{}}`
	}

	filtered := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		if server, ok := cfg.MCPServers[name]; ok {
			filtered[name] = server
		} else {
			slog.Warn("MCP server not found in ~/.claude.json", "name", name)
		}
	}

	result := mcpConfig{MCPServers: filtered}
	out, err := json.Marshal(result)
	if err != nil {
		return `{"mcpServers":{}}`
	}
	return string(out)
}

// BuildTestArgs applies the given options to a fresh ClaudeCLI and returns the
// resulting CLI arguments. Intended for tests in packages that compose
// ClaudeCLIOption slices and need to verify the resulting argument list.
func BuildTestArgs(opts ...ClaudeCLIOption) []string {
	c := &ClaudeCLI{binary: "claude"}
	for _, opt := range opts {
		opt(c)
	}
	return c.buildFinalArgs()
}

// buildFinalArgs returns extraArgs with inline MCP servers merged in.
// buildFinalArgs merges inlineMCPServers into existing --mcp-config if present.
// When --strict-mcp-config was already set (via WithMCPServers), inline servers
// are merged into the existing filtered config. When no strict filtering was
// requested, inline servers are added via --mcp-config only (additive — the
// user's default MCPs from ~/.claude.json remain available).
func (c *ClaudeCLI) buildFinalArgs() []string {
	if len(c.inlineMCPServers) == 0 {
		return c.extraArgs
	}

	// Deep copy extraArgs so we can modify
	args := make([]string, len(c.extraArgs))
	copy(args, c.extraArgs)

	// Find and merge --mcp-config
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

	// Merge inline servers
	for name, def := range c.inlineMCPServers {
		data, err := json.Marshal(def)
		if err != nil {
			slog.Error("buildFinalArgs: marshal inline MCP server", "name", name, "err", err)
			continue
		}
		existing.MCPServers[name] = data
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		slog.Error("buildFinalArgs: marshal merged MCP config", "err", err)
		return c.extraArgs
	}

	if mcpIdx >= 0 {
		// --mcp-config already exists (with --strict-mcp-config from WithMCPServers);
		// update the config in place, keeping strict filtering.
		args[mcpIdx] = string(merged)
	} else {
		// No explicit MCP filtering — add inline servers additively.
		// Do NOT add --strict-mcp-config: let user's default MCPs from
		// ~/.claude.json remain available alongside the inline servers.
		args = append(args, "--mcp-config", string(merged))
	}

	return args
}

// --- Plan extraction helpers ---

// readSecurePlanFile reads a plan file after verifying it resides under ~/.claude/plans/.
func readSecurePlanFile(planFilePath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	allowedPrefix := filepath.Join(home, ".claude", "plans") + string(filepath.Separator)
	absPath, err := filepath.Abs(planFilePath)
	if err != nil {
		return "", fmt.Errorf("resolve plan file path: %w", err)
	}
	if !strings.HasPrefix(absPath, allowedPrefix) {
		return "", fmt.Errorf("plan file %q is outside allowed directory %q", absPath, allowedPrefix)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", absPath, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("plan file %q is empty", absPath)
	}
	return content, nil
}

// scanPlansDirectoryFallback finds the most recently modified .md file in ~/.claude/plans/.
// Used as a fallback when the JSONL log doesn't contain a plan_mode attachment.
func scanPlansDirectoryFallback() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	plansDir := filepath.Join(home, ".claude", "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return "", fmt.Errorf("read plans directory %q: %w", plansDir, err)
	}

	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		modTime := info.ModTime().Unix()
		if modTime > newestMod {
			newestMod = modTime
			newest = filepath.Join(plansDir, e.Name())
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no .md files found in %q", plansDir)
	}

	slog.Info("plan file resolved via directory scan fallback", "path", newest)
	data, err := os.ReadFile(newest)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", newest, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("plan file %q is empty", newest)
	}
	return content, nil
}
