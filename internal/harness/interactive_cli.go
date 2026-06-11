package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// InteractiveRunner is implemented by *ClaudeCLI.
// It starts a persistent Claude CLI session with bidirectional JSON streaming.
type InteractiveRunner interface {
	// RunInteractive starts the Claude CLI with --input-format stream-json
	// and --output-format stream-json. It returns immediately after the
	// initial prompt is consumed, while the CLI stays alive waiting for
	// follow-up messages via stdin.
	//
	// The returned *InteractiveSession owns the process lifecycle.
	// Callers interact via Post(), Done(), Usage(), and the streamUpdates channel.
	RunInteractive(ctx context.Context, prompt, systemPrompt string,
		streamUpdates chan<- StreamUpdate) (*InteractiveSession, error)
}

// InteractiveSession is returned by RunInteractive.
// It owns the Claude CLI process lifecycle and provides bidirectional
// communication via NDJSON over stdin/stdout.
type InteractiveSession struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	done        <-chan error
	updates     <-chan StreamUpdate
	sessionID   string
	planPath    string
	usage       TokenUsage
	resultError bool
}

// RunInteractive starts a persistent Claude CLI session with bidirectional
// JSON streaming. It builds the CLI args (same as RunStreaming but with
// stream-json input/output formats), starts the process, and returns a
// session handle immediately while a background goroutine drains stdout.
func (c *ClaudeCLI) RunInteractive(ctx context.Context, prompt, systemPrompt string,
	streamUpdates chan<- StreamUpdate) (*InteractiveSession, error) {

	// Build CLI args matching RunStreaming but with --input-format stream-json
	// for NDJSON communication over stdin.
	//
	// NOTE: --print is intentionally omitted. The --print flag forces the
	// Claude CLI into one-shot mode — it processes the initial prompt and
	// exits immediately, killing the bidirectional session before the TUI
	// can receive any updates. Without --print the CLI stays alive: it
	// processes the initial NDJSON prompt written below, streams the
	// response, then waits for follow-up NDJSON messages via stdin
	// (written by Post()).
	//
	// The -p flag is still passed but the Claude CLI in stream-json mode
	// expects the prompt via stdin NDJSON — without it the CLI blocks
	// forever waiting for input.
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if merged := mergeAppendPrompts(systemPrompt, c.appendSystemPrompt); merged != "" {
		args = append(args, "--append-system-prompt", merged)
	}
	args = append(args, c.buildFinalArgs()...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	env, err := c.buildEnv()
	if err != nil {
		return nil, fmt.Errorf("build env: %w", err)
	}
	cmd.Env = env
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	done := make(chan error)
	updates := make(chan StreamUpdate, 512)

	var (
		sessID      string
		planPath    string
		usage       TokenUsage
		resultError bool
		once        sync.Once
	)

	// parseStream runs in the background, draining stdout until the
	// process exits. It populates the session metadata as events arrive.
	go func() {
		_, isError, u, sid, pp, _ := parseStream(cmdStdout, updates)
		once.Do(func() {
			sessID = sid
			planPath = pp
			usage = u
			resultError = isError
		})
		close(updates)
		done <- cmd.Wait()
	}()

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("claude CLI start: %w", err)
	}

	// The Claude CLI in stream-json mode expects the user prompt via
	// stdin NDJSON rather than the -p flag. Write the initial prompt
	// as NDJSON and close stdin to signal EOF so the CLI processes the
	// prompt and produces output.
	initMsg := map[string]interface{}{
		"type":               "user",
		"message":            map[string]interface{}{"role": "user", "content": prompt},
		"parent_tool_use_id": nil,
		"session_id":         "",
	}
	initData, err := json.Marshal(initMsg)
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("marshal initial prompt: %w", err)
	}
	if _, err := stdin.Write(append(initData, '\n')); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("claude CLI initial prompt: %w", err)
	}
	stdin.Close()

	return &InteractiveSession{
		cmd:         cmd,
		stdin:       stdin,
		done:        done,
		updates:     updates,
		sessionID:   sessID,
		planPath:    planPath,
		usage:       usage,
		resultError: resultError,
	}, nil
}

// Post writes a user message as NDJSON to the Claude CLI stdin.
// The message is formatted per the verified companion-repo wire format:
//
//	{"type":"user","message":{"role":"user","content":"<msg>"},
//	 "parent_tool_use_id":null,"session_id":"<session_id>"}
//
// Returns an error if the write fails (process exited, stdin closed).
func (s *InteractiveSession) Post(msg string) error {
	ndjson := map[string]interface{}{
		"type":               "user",
		"message":            map[string]interface{}{"role": "user", "content": msg},
		"parent_tool_use_id": nil,
		"session_id":         s.sessionID,
	}
	data, err := json.Marshal(ndjson)
	if err != nil {
		return fmt.Errorf("marshal post message: %w", err)
	}
	_, err = s.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("write to stdin: %w", err)
	}
	return nil
}

// Done returns the done channel. It is closed when the Claude CLI process
// exits (either naturally or via Kill). The error value is nil on clean
// exit, non-nil on signal termination or context cancellation.
func (s *InteractiveSession) Done() <-chan error {
	return s.done
}

// Updates returns the updates channel. It is closed when the Claude CLI
// process exits and parseStream finishes draining stdout.
func (s *InteractiveSession) Updates() <-chan StreamUpdate {
	return s.updates
}

// Usage returns the final token usage from the result event.
// Returns zero values if the result event has not yet been parsed.
func (s *InteractiveSession) Usage() TokenUsage {
	return s.usage
}

// ResultError reports whether the session ended with is_error:true
// in the result event. Returns false if the result event has not yet
// been parsed.
func (s *InteractiveSession) ResultError() bool {
	return s.resultError
}

// SessionID returns the session ID extracted from the system/init or
// result event. Empty until the first such event is parsed.
func (s *InteractiveSession) SessionID() string {
	return s.sessionID
}

// PlanPath returns the plan file path extracted from the result event.
// Empty if no plan file was produced.
func (s *InteractiveSession) PlanPath() string {
	return s.planPath
}

// Kill terminates the Claude CLI process. The session's Done and Updates
// channels will close once the process exits.
func (s *InteractiveSession) Kill() error {
	return s.cmd.Process.Kill()
}
