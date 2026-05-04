package intent

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/sandbox"
)

// intakeSystemPrompt is the system prompt instructing the intake agent to process
// the user's input and produce a structured output file.
const intakeSystemPrompt = `You are an intake agent for Orqestra, an LLM orchestration system.

Your task:
1. Read the user's request from /workspace/.orqestra/<session>/input.md
2. Analyze it for clarity, feasibility, and scope
3. Write your structured output to /workspace/.orqestra/<session>/output.md

Your output MUST be a valid markdown document. Include:
- A rephrased, unambiguous version of the request
- Clear acceptance criteria
- Any risks or constraints identified
- A confidence score (0.0-1.0)

When done, exit naturally. Do not wait for further input.`

// IntakeCallbacks allows the caller to receive PTY lifecycle events
// without coupling the runner to any particular UI framework.
type IntakeCallbacks struct {
	// OnOutput is called with raw PTY output bytes.
	OnOutput func(data []byte)
	// OnDone is called when the PTY process exits.
	OnDone func(exitCode int)
}

// IntakeRunner orchestrates a single intake agent session using sandbox + PTY primitives.
type IntakeRunner struct {
	sandbox  *sandbox.DockerSandbox
	resolved config.ResolvedModel
	small    *config.ResolvedModel
}

// NewIntakeRunner creates an IntakeRunner with the given sandbox and model configuration.
func NewIntakeRunner(sb *sandbox.DockerSandbox, resolved config.ResolvedModel, small *config.ResolvedModel) *IntakeRunner {
	return &IntakeRunner{
		sandbox:  sb,
		resolved: resolved,
		small:    small,
	}
}

// Execute runs the intake agent inside the sandbox PTY session.
// It stages the prompt, launches the agent, streams PTY output via callbacks,
// and extracts the artifact on completion.
func (r *IntakeRunner) Execute(ctx context.Context, sess sandbox.Session, prompt string, cb IntakeCallbacks) ([]byte, error) {
	// Stage the input into the container.
	if err := r.sandbox.StageInputs(ctx, sess, prompt, intakeSystemPrompt); err != nil {
		return nil, fmt.Errorf("intake stage inputs: %w", err)
	}

	// Build the CLI command for non-interactive PTY execution.
	cliPrompt := fmt.Sprintf("Process /workspace/.orqestra/%s/input.md and write output to /workspace/.orqestra/%s/output.md", sess.Name, sess.Name)
	cmd := harness.BuildPTYCommand(cliPrompt, false)

	// Build environment variables for model routing.
	env := harness.BuildModelEnv(r.resolved, r.small)

	// Get the container ID from the sandbox.
	containerID := r.sandbox.ContainerID()
	if containerID == "" {
		return nil, fmt.Errorf("intake runner: sandbox not provisioned (no container ID)")
	}

	// Get the Docker client from the sandbox.
	cli := r.sandbox.Client()
	if cli == nil {
		return nil, fmt.Errorf("intake runner: sandbox has no docker client")
	}

	// Create and start the PTY session.
	pty := sandbox.NewPTYSession(sess.Name+"-intake", sess.Name, cli)
	if err := pty.Start(ctx, containerID, cmd, env, 120, 40); err != nil {
		return nil, fmt.Errorf("intake pty start: %w", err)
	}
	defer pty.Close()

	// Read goroutine: stream PTY output via callback.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := pty.Read(buf)
			if n > 0 && cb.OnOutput != nil {
				data := make([]byte, n)
				copy(data, buf[:n])
				cb.OnOutput(data)
			}
			if err != nil {
				if err != io.EOF {
					slog.Warn("intake pty read error", "err", err)
				}
				return
			}
		}
	}()

	// Wait for the PTY to close (process exit).
	<-done

	// Check exit code.
	exitCode := pty.ExitCode()
	if cb.OnDone != nil {
		cb.OnDone(exitCode)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("intake agent exited with code %d", exitCode)
	}

	// Extract the output artifact.
	artifact, err := r.sandbox.ExtractArtifact(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("intake extract artifact: %w", err)
	}

	slog.Info("intake runner completed", "session", sess.Name, "artifact_size", len(artifact))
	return artifact, nil
}
