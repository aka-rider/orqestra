package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/sandbox"
)

// PipelineState tracks which phase the pipeline is currently in.
type PipelineState int

const (
	PipelineIdle       PipelineState = iota
	PipelineIntake                   // intake agent running
	PipelinePlanning                 // planner agent running
	PipelineValidating               // plan-validator running (human gate)
	PipelineExecuting                // worker(s) running
	PipelineDone                     // pipeline complete
	PipelineHalted                   // pipeline halted by rejection or error
)

func (s PipelineState) String() string {
	switch s {
	case PipelineIdle:
		return "idle"
	case PipelineIntake:
		return "intake"
	case PipelinePlanning:
		return "planning"
	case PipelineValidating:
		return "validating"
	case PipelineExecuting:
		return "executing"
	case PipelineDone:
		return "done"
	case PipelineHalted:
		return "halted"
	default:
		return "unknown"
	}
}

// ValidationVerdict is the user's decision at the human gate.
type ValidationVerdict struct {
	SchemaVersion string   `json:"schema_version"`
	Verdict       string   `json:"verdict"`
	Issues        []string `json:"issues,omitempty"`
	Summary       string   `json:"summary"`
	UserDecision  string   `json:"user_decision"`
	UserFeedback  string   `json:"user_feedback,omitempty"`
}

// PipelineCallbacks delivers pipeline lifecycle events to the TUI.
type PipelineCallbacks struct {
	// OnStateChange is called when the pipeline transitions between phases.
	OnStateChange func(state PipelineState)
	// OnAgentOutput delivers raw PTY output from the active agent.
	OnAgentOutput func(role Role, data []byte)
	// OnAgentBEL signals that an agent needs user attention.
	OnAgentBEL func(role Role)
	// OnAgentDone signals that an agent finished.
	OnAgentDone func(role Role, exitCode int, err error)
	// OnSandboxState delivers sandbox lifecycle events.
	OnSandboxState func(sandboxID string, state sandbox.State)
}

// PipelineConfig configures the agent pipeline.
type PipelineConfig struct {
	Config   *config.Config
	RepoPath string
	Session  SessionDir
}

// Pipeline drives the sequence: intake → planner → validator → workers.
type Pipeline struct {
	cfg    PipelineConfig
	runner *Runner
	cb     PipelineCallbacks
	state  PipelineState
}

// NewPipeline creates a new pipeline orchestrator.
func NewPipeline(cfg PipelineConfig, cb PipelineCallbacks) *Pipeline {
	return &Pipeline{
		cfg:    cfg,
		runner: NewRunner(),
		cb:     cb,
		state:  PipelineIdle,
	}
}

// State returns the current pipeline state.
func (p *Pipeline) State() PipelineState {
	return p.state
}

func (p *Pipeline) setState(s PipelineState) {
	p.state = s
	if p.cb.OnStateChange != nil {
		p.cb.OnStateChange(s)
	}
}

// RunIntake executes the intake agent phase.
// The intake agent processes the user's prompt and produces a structured artifact.
func (p *Pipeline) RunIntake(ctx context.Context, userPrompt string) ([]byte, error) {
	p.setState(PipelineIntake)

	resolved, err := p.cfg.Config.ResolveModel(p.cfg.Config.Intent.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve intake model %q: %w", p.cfg.Config.Intent.ModelRef, err)
	}

	spec := AgentSpec{
		Role:         RoleIntake,
		ModelRef:     p.cfg.Config.Intent.ModelRef,
		SystemPrompt: p.cfg.Config.Intent.SystemPrompt,
		OutputFile:   ".orqestra/agent/output/01.intake.json",
		Command:      buildAgentCommand(userPrompt, "/workspace/.orqestra/agent/system-prompt.md", true),
		Env:          harness.BuildModelEnv(resolved, p.cfg.Config.ResolveSmallModel()),
		Interactive:  true,
	}

	artifact, err := p.runner.Run(ctx, RunConfig{
		Spec:     spec,
		Session:  p.cfg.Session,
		Sandbox:  sandboxCfgFrom(p.cfg.Config.Sandbox),
		RepoPath: p.cfg.RepoPath,
		Callbacks: RunCallbacks{
			OnOutput:       p.makeOnOutput(RoleIntake),
			OnBEL:          p.makeOnBEL(RoleIntake),
			OnDone:         p.makeOnDone(RoleIntake),
			OnSandboxState: p.cb.OnSandboxState,
		},
	})
	if err != nil {
		p.setState(PipelineHalted)
		return nil, err
	}

	return artifact, nil
}

// RunPlanner executes the planner agent phase.
func (p *Pipeline) RunPlanner(ctx context.Context, intakeArtifact []byte) ([]byte, error) {
	p.setState(PipelinePlanning)

	resolved, err := p.cfg.Config.ResolveModel(p.cfg.Config.Planner.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve planner model %q: %w", p.cfg.Config.Planner.ModelRef, err)
	}

	spec := AgentSpec{
		Role:         RolePlanner,
		ModelRef:     p.cfg.Config.Planner.ModelRef,
		SystemPrompt: p.cfg.Config.Planner.SystemPrompt,
		InputFiles: map[string][]byte{
			"01.intake.json": intakeArtifact,
		},
		OutputFile: ".orqestra/agent/output/02.plan.json",
		Command:    buildAgentCommand("Read the intake artifact at /workspace/.orqestra/agent/input/01.intake.json and produce an engineering specification. Write it to /workspace/.orqestra/agent/output/02.plan.json", "/workspace/.orqestra/agent/system-prompt.md", false),
		Env:        harness.BuildModelEnv(resolved, p.cfg.Config.ResolveSmallModel()),
	}

	artifact, err := p.runner.Run(ctx, RunConfig{
		Spec:     spec,
		Session:  p.cfg.Session,
		Sandbox:  sandboxCfgFrom(p.cfg.Config.Sandbox),
		RepoPath: p.cfg.RepoPath,
		Callbacks: RunCallbacks{
			OnOutput:       p.makeOnOutput(RolePlanner),
			OnBEL:          p.makeOnBEL(RolePlanner),
			OnDone:         p.makeOnDone(RolePlanner),
			OnSandboxState: p.cb.OnSandboxState,
		},
	})
	if err != nil {
		p.setState(PipelineHalted)
		return nil, err
	}

	return artifact, nil
}

// RunValidator executes the plan validator (human gate) phase.
func (p *Pipeline) RunValidator(ctx context.Context, intakeArtifact, planArtifact []byte) ([]byte, error) {
	p.setState(PipelineValidating)

	resolved, err := p.cfg.Config.ResolveModel(p.cfg.Config.Validator.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("resolve validator model %q: %w", p.cfg.Config.Validator.ModelRef, err)
	}

	spec := AgentSpec{
		Role:         RolePlanValidator,
		ModelRef:     p.cfg.Config.Validator.ModelRef,
		SystemPrompt: p.cfg.Config.Validator.SystemPrompt,
		InputFiles: map[string][]byte{
			"01.intake.json": intakeArtifact,
			"02.plan.json":   planArtifact,
		},
		OutputFile:  ".orqestra/agent/output/03.validation.json",
		Command:     buildAgentCommand("Read the intake and plan artifacts from /workspace/.orqestra/agent/input/. Evaluate the plan, present it to the user, and write your decision to /workspace/.orqestra/agent/output/03.validation.json", "/workspace/.orqestra/agent/system-prompt.md", true),
		Env:         harness.BuildModelEnv(resolved, p.cfg.Config.ResolveSmallModel()),
		Interactive: true,
	}

	artifact, err := p.runner.Run(ctx, RunConfig{
		Spec:     spec,
		Session:  p.cfg.Session,
		Sandbox:  sandboxCfgFrom(p.cfg.Config.Sandbox),
		RepoPath: p.cfg.RepoPath,
		Callbacks: RunCallbacks{
			OnOutput:       p.makeOnOutput(RolePlanValidator),
			OnBEL:          p.makeOnBEL(RolePlanValidator),
			OnDone:         p.makeOnDone(RolePlanValidator),
			OnSandboxState: p.cb.OnSandboxState,
		},
	})
	if err != nil {
		p.setState(PipelineHalted)
		return nil, err
	}

	// Parse validation verdict.
	var verdict ValidationVerdict
	if err := json.Unmarshal(artifact, &verdict); err != nil {
		p.setState(PipelineHalted)
		return nil, fmt.Errorf("validation artifact is not valid JSON: %w", err)
	}

	if verdict.Verdict != "approved" {
		p.setState(PipelineHalted)
		slog.Info("pipeline: plan rejected", "verdict", verdict.Verdict, "decision", verdict.UserDecision)
		return artifact, fmt.Errorf("plan %s: %s", verdict.Verdict, verdict.Summary)
	}

	return artifact, nil
}

// RunWorker executes a worker agent phase.
func (p *Pipeline) RunWorker(ctx context.Context, planArtifact []byte) error {
	p.setState(PipelineExecuting)

	resolved, err := p.cfg.Config.ResolveModel(p.cfg.Config.Worker.ModelRef)
	if err != nil {
		return fmt.Errorf("resolve worker model %q: %w", p.cfg.Config.Worker.ModelRef, err)
	}

	spec := AgentSpec{
		Role:     RoleWorker,
		ModelRef: p.cfg.Config.Worker.ModelRef,
		InputFiles: map[string][]byte{
			"02.plan.json": planArtifact,
		},
		Command:     buildAgentCommand("Execute the plan at /workspace/.orqestra/agent/input/02.plan.json. Work autonomously. Escalate to user only if stuck.", "/workspace/.orqestra/agent/system-prompt.md", true),
		Env:         harness.BuildModelEnv(resolved, p.cfg.Config.ResolveSmallModel()),
		Interactive: true,
	}

	_, err = p.runner.Run(ctx, RunConfig{
		Spec:     spec,
		Session:  p.cfg.Session,
		Sandbox:  sandboxCfgFrom(p.cfg.Config.Sandbox),
		RepoPath: p.cfg.RepoPath,
		Callbacks: RunCallbacks{
			OnOutput:       p.makeOnOutput(RoleWorker),
			OnBEL:          p.makeOnBEL(RoleWorker),
			OnDone:         p.makeOnDone(RoleWorker),
			OnSandboxState: p.cb.OnSandboxState,
		},
	})
	if err != nil {
		p.setState(PipelineHalted)
		return err
	}

	p.setState(PipelineDone)
	return nil
}

func (p *Pipeline) makeOnOutput(role Role) func([]byte) {
	return func(data []byte) {
		if p.cb.OnAgentOutput != nil {
			p.cb.OnAgentOutput(role, data)
		}
	}
}

func (p *Pipeline) makeOnBEL(role Role) func() {
	return func() {
		if p.cb.OnAgentBEL != nil {
			p.cb.OnAgentBEL(role)
		}
	}
}

func (p *Pipeline) makeOnDone(role Role) func(int, error) {
	return func(exitCode int, err error) {
		if p.cb.OnAgentDone != nil {
			p.cb.OnAgentDone(role, exitCode, err)
		}
	}
}

// buildAgentCommand constructs the Claude Code CLI command for an agent.
// Uses --append-system-prompt-file to inject role behavior.
// Non-interactive agents use -p mode; interactive agents launch the full CLI.
func buildAgentCommand(prompt, systemPromptFile string, interactive bool) []string {
	return harness.BuildPTYCommandWithPromptFile(prompt, systemPromptFile, interactive)
}

// sandboxCfgFrom converts the config-layer sandbox config to the sandbox package's Config type.
func sandboxCfgFrom(cfg config.SandboxConfig) sandbox.Config {
	sc := sandbox.DefaultConfig()
	if cfg.Image != "" {
		sc.Image = cfg.Image
	}
	if cfg.Memory != "" {
		sc.Memory = cfg.Memory
	}
	if cfg.CPUs > 0 {
		sc.CPUs = cfg.CPUs
	}
	if cfg.PidsLimit > 0 {
		sc.PidsLimit = cfg.PidsLimit
	}
	if cfg.MaxLifetime.Duration > 0 {
		sc.MaxLifetime = cfg.MaxLifetime.Duration
	}
	sc.Network = cfg.Network
	sc.AllowedExecutables = cfg.AllowedExecutables
	if cfg.MCP.SocketPath != "" {
		sc.MCP.SocketPath = cfg.MCP.SocketPath
	}
	for _, m := range cfg.ReadOnlyMounts {
		sc.ReadOnlyMounts = append(sc.ReadOnlyMounts, sandbox.MountConfig{
			HostPath: os.ExpandEnv(m.Host), ContainerPath: m.Container,
		})
	}
	for _, m := range cfg.BindMounts {
		sc.BindMounts = append(sc.BindMounts, sandbox.MountConfig{
			HostPath: os.ExpandEnv(m.Host), ContainerPath: m.Container,
		})
	}
	return sc
}
