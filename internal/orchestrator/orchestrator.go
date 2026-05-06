//go:build darwin

// Package orchestrator implements the pipeline state machine that drives
// agent execution through intake → planner → validator → worker phases.
// It communicates with the mux via channels and is fully testable without
// any terminal or UI dependency.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/chrome"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mux"
)

// Phase represents the pipeline phase.
type Phase = chrome.PipelinePhase

const (
	PhaseIntake    = chrome.PhaseIntake
	PhasePlanner   = chrome.PhasePlanner
	PhaseValidator = chrome.PhaseValidator
	PhaseWorker    = chrome.PhaseWorker
	PhaseDone      = chrome.PhaseDone
)

// Config holds orchestrator configuration.
type Config struct {
	// Model references and prompts per role.
	IntakeModelRef    string
	IntakePrompt      string
	PlannerModelRef   string
	PlannerPrompt     string
	ValidatorModelRef string
	ValidatorPrompt   string
	WorkerModelRef    string

	// ResolveModel resolves a model ref to connection details.
	ResolveModel func(ref string) (config.ResolvedModel, error)

	// ResolveSmallModel returns the small model for tool use, or nil.
	ResolveSmallModel func() *config.ResolvedModel

	// RepoPath is the working directory.
	RepoPath string

	// Goal is the user's top-level intent (set after intake).
	Goal string
}

// Orchestrator manages the pipeline state machine.
type Orchestrator struct {
	cfg       Config
	runner    *agent.SeatbeltRunner
	mux       *mux.Mux
	session   agent.SessionDir
	phase     Phase
	artifacts map[string][]byte
	logs      []chrome.LogEntry
	goal      string
	tabRoles  map[int]string // tab index → role
	mu        sync.Mutex
}

// New creates a new orchestrator.
func New(cfg Config, runner *agent.SeatbeltRunner, m *mux.Mux) *Orchestrator {
	return &Orchestrator{
		cfg:       cfg,
		runner:    runner,
		mux:       m,
		phase:     PhaseIntake,
		artifacts: make(map[string][]byte),
		tabRoles:  make(map[int]string),
	}
}

// Snapshot returns the current state for chrome rendering.
func (o *Orchestrator) Snapshot() chrome.Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()

	tabs := o.mux.Tabs()
	var tabInfos []chrome.TabInfo
	for _, t := range tabs {
		state := chrome.TabStateRunning
		tabState, exitCode := t.Status()
		if tabState == mux.TabDone {
			state = chrome.TabStateDone
		}
		tabInfos = append(tabInfos, chrome.TabInfo{
			Name:      t.Name,
			Index:     t.Index,
			State:     state,
			Attention: t.NeedsAttention(),
			StartedAt: t.StartedAt,
			ExitCode:  exitCode,
		})
	}

	// Copy logs.
	logs := make([]chrome.LogEntry, len(o.logs))
	copy(logs, o.logs)

	return chrome.Snapshot{
		Phase:     o.phase,
		Goal:      o.goal,
		Tabs:      tabInfos,
		ActiveTab: o.mux.Active(),
		Logs:      logs,
	}
}

// Phase returns the current pipeline phase.
func (o *Orchestrator) Phase() Phase {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.phase
}

// AddLog adds an orchestrator-level log entry.
func (o *Orchestrator) AddLog(level, msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logs = append(o.logs, chrome.LogEntry{
		Time:    time.Now(),
		Level:   level,
		Message: msg,
	})
	// Keep only last 50 entries.
	if len(o.logs) > 50 {
		o.logs = o.logs[len(o.logs)-50:]
	}
}

// Run starts the pipeline. It creates the session directory and launches
// the first agent (intake). Subsequent agents are launched via HandleTabExited.
func (o *Orchestrator) Run(ctx context.Context) error {
	sessDir, err := agent.NewSessionDir(o.cfg.RepoPath, "pipeline")
	if err != nil {
		return fmt.Errorf("orchestrator: session dir: %w", err)
	}
	o.session = sessDir

	// Launch intake agent.
	return o.launchPhase(ctx, PhaseIntake)
}

// HandleTabExited is called by the mux when a tab's process exits.
// It reads artifacts and advances the pipeline.
func (o *Orchestrator) HandleTabExited(ctx context.Context, event mux.TabExitedEvent) {
	o.mu.Lock()
	role := o.tabRoles[event.Index]
	o.mu.Unlock()

	if role == "" {
		return
	}

	o.AddLog("INFO", fmt.Sprintf("%s exited (code %d)", role, event.ExitCode))

	if event.ExitCode != 0 {
		o.AddLog("ERROR", fmt.Sprintf("%s failed", role))
		o.mu.Lock()
		o.phase = PhaseDone
		o.mu.Unlock()
		return
	}

	// Read artifact from session.
	artifact, err := o.readArtifact(role)
	if err != nil {
		slog.Warn("orchestrator: read artifact", "role", role, "err", err)
	}

	o.mu.Lock()
	o.artifacts[role] = artifact
	o.mu.Unlock()

	// Advance pipeline.
	o.advance(ctx, role)
}

func (o *Orchestrator) advance(ctx context.Context, completedRole string) {
	o.mu.Lock()
	currentPhase := o.phase
	o.mu.Unlock()

	var nextPhase Phase
	switch currentPhase {
	case PhaseIntake:
		nextPhase = PhasePlanner
	case PhasePlanner:
		nextPhase = PhaseValidator
	case PhaseValidator:
		// Check if validator approved.
		o.mu.Lock()
		approved := validatorApproved(o.artifacts["plan-validator"])
		o.mu.Unlock()
		if !approved {
			o.AddLog("ERROR", "Plan rejected by validator")
			o.mu.Lock()
			o.phase = PhaseDone
			o.mu.Unlock()
			return
		}
		nextPhase = PhaseWorker
	case PhaseWorker:
		o.mu.Lock()
		o.phase = PhaseDone
		o.mu.Unlock()
		o.AddLog("INFO", "Pipeline complete")
		return
	default:
		return
	}

	o.mu.Lock()
	o.phase = nextPhase
	o.mu.Unlock()

	if err := o.launchPhase(ctx, nextPhase); err != nil {
		o.AddLog("ERROR", fmt.Sprintf("launch %s: %v", nextPhase.String(), err))
	}
}

func (o *Orchestrator) launchPhase(ctx context.Context, phase Phase) error {
	var role string
	var modelRef, prompt, outputFile string
	var inputFiles map[string][]byte

	o.mu.Lock()
	artifacts := o.artifacts
	o.mu.Unlock()

	switch phase {
	case PhaseIntake:
		role = "intake"
		modelRef = o.cfg.IntakeModelRef
		prompt = o.cfg.IntakePrompt
		outputFile = "01.intake.json"
	case PhasePlanner:
		role = "planner"
		modelRef = o.cfg.PlannerModelRef
		prompt = o.cfg.PlannerPrompt
		outputFile = "02.plan.json"
		inputFiles = map[string][]byte{"01.intake.json": artifacts["intake"]}
	case PhaseValidator:
		role = "plan-validator"
		modelRef = o.cfg.ValidatorModelRef
		prompt = o.cfg.ValidatorPrompt
		outputFile = "03.validation.json"
		inputFiles = map[string][]byte{
			"01.intake.json": artifacts["intake"],
			"02.plan.json":   artifacts["planner"],
		}
	case PhaseWorker:
		role = "worker"
		modelRef = o.cfg.WorkerModelRef
		outputFile = ""
		inputFiles = map[string][]byte{"02.plan.json": artifacts["planner"]}
	default:
		return fmt.Errorf("unknown phase %d", phase)
	}

	if modelRef == "" {
		return fmt.Errorf("no model_ref for role %s", role)
	}

	resolved, err := o.cfg.ResolveModel(modelRef)
	if err != nil {
		return fmt.Errorf("resolve model for %s: %w", role, err)
	}
	env := harness.BuildModelEnv(resolved, o.cfg.ResolveSmallModel())

	// Build system prompt with paths.
	roleDir := filepath.Join(o.session.Path, role)
	inputDir := filepath.Join(roleDir, "input")
	outputDir := filepath.Join(roleDir, "output")
	var systemPrompt string
	if prompt != "" {
		systemPrompt = prompt + fmt.Sprintf(
			"\n\n---\nRuntime paths:\n- Input directory: %s\n- Output directory: %s\n- Output file: %s/%s\n- Working directory (repo): %s\n",
			inputDir, outputDir, outputDir, outputFile, o.cfg.RepoPath,
		)
	}

	promptFile := filepath.Join(roleDir, "agent.md")
	spec := agent.AgentSpec{
		Role:         agent.Role(role),
		ModelRef:     modelRef,
		SystemPrompt: systemPrompt,
		InputFiles:   inputFiles,
		OutputFile:   outputFile,
		Command:      harness.BuildPTYCommandWithPromptFile("", promptFile, true),
		Env:          env,
		Interactive:  true,
	}

	liveSession, err := o.runner.RunInteractive(ctx, agent.RunConfig{
		Spec:     spec,
		Session:  o.session,
		RepoPath: o.cfg.RepoPath,
		Callbacks: agent.RunCallbacks{
			OnState: func(state agent.AgentState) {
				o.AddLog("INFO", fmt.Sprintf("%s: %s", role, state))
			},
		},
	})
	if err != nil {
		return fmt.Errorf("launch %s: %w", role, err)
	}

	// Register tab in mux.
	tabName := role
	switch phase {
	case PhaseIntake:
		tabName = "Intake"
	case PhasePlanner:
		tabName = "Planner"
	case PhaseValidator:
		tabName = "Validator"
	case PhaseWorker:
		tabName = "Worker"
	}

	idx := o.mux.AddTab(tabName, liveSession.PTY())
	o.mux.SetActive(idx)

	o.mu.Lock()
	o.tabRoles[idx] = role
	o.mu.Unlock()

	o.AddLog("INFO", fmt.Sprintf("Started %s (tab %d)", tabName, idx+1))
	return nil
}

func (o *Orchestrator) readArtifact(role string) ([]byte, error) {
	var outputFile string
	switch role {
	case "intake":
		outputFile = "01.intake.json"
	case "planner":
		outputFile = "02.plan.json"
	case "plan-validator":
		outputFile = "03.validation.json"
	default:
		return nil, nil
	}
	if outputFile == "" {
		return nil, nil
	}

	path := filepath.Join(o.session.Path, role, "output", outputFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// validatorApproved checks if the validator approved the plan.
func validatorApproved(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var result struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return false
	}
	return result.Verdict == "pass" || result.Verdict == "warn"
}
