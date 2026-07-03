package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/project"
	"github.com/xiii/orqestra/internal/sandbox"
	"github.com/xiii/orqestra/internal/sandbox/detect"
	"github.com/xiii/orqestra/internal/tui"
)

// Exit codes per PLAN.md policy.
const (
	exitOK            = 0
	exitDomainFailure = 1
	exitInvalidInput  = 2
	exitProviderError = 3
	exitUserCancelled = 130
)

func run(args []string, stdout, stderr io.Writer) int {
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Subcommand: mcp-bridge (invoked by Claude CLI as MCP server)
	if len(args) >= 2 && args[1] == "mcp-bridge" {
		socketPath, agentID, ok := parseMCPBridgeArgs(args[2:])
		if !ok {
			fmt.Fprintf(stderr, "Usage: orqestra mcp-bridge --socket <path> --agent-id <id>\n")
			return exitInvalidInput
		}
		if err := mcp.RunServer(socketPath, agentID); err != nil {
			slog.Error("mcp-bridge failed", "err", err)
			return exitDomainFailure
		}
		return exitOK
	}

	// Global flags
	var configPath string

	fs := flag.NewFlagSet("orqestra", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "orqestra.yaml", "config file name or absolute path")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(stderr, "Usage: orqestra [--config path|preset]\n")
		return exitInvalidInput
	}

	cmdArgs := fs.Args()

	// Project root detection and initialization gate.
	baseDir, dirErr := os.Getwd()
	if dirErr != nil {
		fmt.Fprintf(stderr, "Error: cannot determine working directory: %v\n", dirErr)
		return exitInvalidInput
	}

	// Handle 'init' subcommand early — no config needed.
	if len(cmdArgs) > 0 && cmdArgs[0] == "init" {
		if err := runInitCommand(baseDir, stderr); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return exitInvalidInput
		}
		return exitOK
	}

	var repoPath string
	switch tui.RunInitGate(baseDir) {
	case tui.InitGateOK, tui.InitGateInitDone:
		repoPath = baseDir
	default:
		return exitUserCancelled
	}

	configPath, err := resolveConfigPath(configPath, repoPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitInvalidInput
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: failed to load config: %v\n", err)
		return exitInvalidInput
	}

	// Detect seatbelt profiles at startup.
	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintf(stderr, "Error: HOME environment variable is not set\n")
		return exitInvalidInput
	}
	sandboxProfiles, profileErr := detect.AllProfiles(home, "claude", cfg.Sandbox)
	if profileErr != nil {
		fmt.Fprintf(stderr, "Error: sandbox profile detection failed: %v\n", profileErr)
		return exitInvalidInput
	}

	if len(cmdArgs) == 0 {
		engine := buildEngine(cfg, sandboxProfiles, repoPath)

		// Silence slog during TUI mode — stderr leaks through the alt screen buffer.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

		if err := tui.Run(engine, filepath.Base(configPath)); err != nil {
			slog.Error("TUI error", "err", err)
			return exitDomainFailure
		}
		return exitOK
	}

	fmt.Fprintf(stderr, "Unknown command: %s\n", cmdArgs[0])
	fmt.Fprintf(stderr, "Available commands: init\n")
	return exitInvalidInput
}

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// runInitCommand handles the 'orqestra init' subcommand.
func runInitCommand(baseDir string, stderr io.Writer) error {
	if err := project.CheckGitRoot(baseDir); err != nil {
		return fmt.Errorf("%s is not a git repository: run 'git init' first", baseDir)
	}

	if project.IsInitialized(baseDir) {
		return fmt.Errorf("%s is already initialized", baseDir)
	}

	if err := project.Init(baseDir); err != nil {
		return fmt.Errorf("failed to initialize %s: %w", baseDir, err)
	}

	fmt.Fprintf(stderr, "Initialized orqestra project at %s\n", baseDir)
	fmt.Fprintf(stderr, "  Created .orqestra/sessions/\n")
	fmt.Fprintf(stderr, "  Added .orqestra/ to .gitignore\n")
	return nil
}

// buildEngine constructs the Engine with ProcessSpecs for the RunPipeline path.
func buildEngine(cfg *config.Config, sandboxProfiles []sandbox.Snapshot, repoPath string) *orchestrator.Engine {
	// Question bridge for AskUserQuestion MCP tool
	socketPath := filepath.Join("/tmp", fmt.Sprintf("orqestra-q-%d.sock", os.Getpid()))
	bridge := mcp.NewQuestionBridge(socketPath)

	selfBin, selfErr := os.Executable()
	if selfErr != nil {
		slog.Warn("cannot determine self path for MCP bridge, questions disabled", "err", selfErr)
	}

	// bridgeOptFor returns a per-role MCP server option.
	// Returns a no-op when selfBin is unavailable.
	bridgeOptFor := func(agentID string) harness.ClaudeCLIOption {
		if selfErr != nil {
			return func(*harness.ClaudeCLI) {}
		}
		return harness.WithInlineMCPServer("orqestra", selfBin, []string{"mcp-bridge", "--socket", socketPath, "--agent-id", agentID})
	}

	// The researcher is no longer a standalone stage: it is an inline subagent the
	// planners spawn on demand via the Agent tool. Build its definition once from the
	// curated researcher persona (cfg.Researcher) and attach it to architect + critic.
	// Model is intentionally OMITTED → "inherit": cfg.Researcher.Model is an orqestra
	// alias the CLI cannot read, and models are env-routed, so the subagent inherits
	// the parent's model. The subagent has no MCP — it returns its report as its final
	// message (its prompt's COMPLETION clause says so).
	researcherDef := harness.AgentDef{
		Description:     cfg.Researcher.Description,
		Prompt:          cfg.Researcher.SystemPrompt,
		Tools:           cfg.Researcher.AllowedTools,
		DisallowedTools: cfg.Researcher.DisallowedTools,
	}

	// Build per-agent ClaudeCLIOptions for BuildProcessSpec.
	plnOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Architect.BaseAgentConfig)...)
	plnOpts = append(plnOpts, bridgeOptFor("architect"), harness.WithMaxTurns(cfg.Architect.MaxTurns), harness.WithInlineAgent("orqestra-researcher", researcherDef))
	criticOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Critic.BaseAgentConfig)...)
	criticOpts = append(criticOpts, bridgeOptFor("critic"), harness.WithMaxTurns(cfg.Critic.MaxTurns), harness.WithInlineAgent("orqestra-researcher", researcherDef))

	// Worker sandbox environment (model-specific env vars for API keys etc.)
	resolved, resolveErr := cfg.ResolveModel(cfg.Worker.Model)
	if resolveErr != nil {
		slog.Error("failed to resolve worker model", "err", resolveErr)
		os.Exit(exitInvalidInput)
	}
	modelEnv, modelEnvErr := harness.BuildModelEnv(resolved, cfg.ResolveUtilityModel())
	if modelEnvErr != nil {
		slog.Error("invalid model configuration", "err", modelEnvErr)
		os.Exit(exitInvalidInput)
	}

	// Integrator sandbox environment (model env for conflict-resolution mode).
	integratorResolved, intResolveErr := cfg.ResolveModel(cfg.Integrator.Model)
	if intResolveErr != nil {
		slog.Error("failed to resolve integrator model", "err", intResolveErr)
		os.Exit(exitInvalidInput)
	}
	integratorEnv, intEnvErr := harness.BuildModelEnv(integratorResolved, cfg.ResolveUtilityModel())
	if intEnvErr != nil {
		slog.Error("invalid integrator model configuration", "err", intEnvErr)
		os.Exit(exitInvalidInput)
	}

	workerSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath,
		Profiles: sandboxProfiles,
		Env:      modelEnv,
		Writable: true,
	}
	worktreeSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath,
		Profiles: sandboxProfiles,
		Env:      modelEnv,
		Writable: false,
	}
	roSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath,
		Profiles: sandboxProfiles,
		Writable: false,
	}

	// Allow the exact orqestra binary to exec inside the read-only planning sandbox.
	// This is required for the mcp-bridge subprocess (AskUserQuestion) to start.
	if selfErr == nil {
		home := os.Getenv("HOME")
		orqProfile := sandbox.NewToolProfile("orqestra-bridge", home)
		if err := orqProfile.Allow(selfBin, sandbox.Exec); err != nil {
			slog.Error("cannot allow orqestra binary in sandbox; AskUserQuestion unavailable", "err", err)
			os.Exit(exitInvalidInput)
		}
		roSandboxCfg.Profiles = append(roSandboxCfg.Profiles, orqProfile.Snapshot())
		workerSandboxCfg.Profiles = append(workerSandboxCfg.Profiles, orqProfile.Snapshot())
		worktreeSandboxCfg.Profiles = append(worktreeSandboxCfg.Profiles, orqProfile.Snapshot())
	}

	// BuildProcessSpec inherits all options already set in plnOpts/criticOpts,
	// including the role system prompt that bridgeToolOpts now delivers via
	// MergeAppendPrompts(SystemPrompt, AppendSystemPrompt). Do NOT add another
	// WithAppendSystemPrompt here — the last one wins and would drop the role prompt.
	archSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Architect.Model, roSandboxCfg, plnOpts...)
	if specErr != nil {
		slog.Error("failed to build architect spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	archSpec.AgentID = "architect"
	archSpec.ExpectsReport = true
	archSpec.PlanMode = cfg.Architect.PermissionMode == "plan"
	archSpec.Timeout = cfg.Architect.Timeout.Duration
	archSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Architect.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Architect.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Architect.LoopGuard.CooldownTurns,
	}
	archSpec.SilenceGuard = harness.SilenceGuardSpec{
		SilenceSecs: cfg.Architect.SilenceGuard.SilenceSecs,
		NudgeText:   cfg.Architect.SilenceGuard.NudgeText,
		MaxNudges:   cfg.Architect.SilenceGuard.MaxNudges,
	}
	archSpec.PreTimeoutNudge = preTimeoutNudgeFor("architect")

	criticSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Critic.Model, roSandboxCfg, criticOpts...)
	if specErr != nil {
		slog.Error("failed to build critic spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	criticSpec.AgentID = "critic"
	criticSpec.ExpectsReport = true
	criticSpec.Timeout = cfg.Critic.Timeout.Duration
	criticSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Critic.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Critic.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Critic.LoopGuard.CooldownTurns,
	}
	criticSpec.SilenceGuard = harness.SilenceGuardSpec{
		SilenceSecs: cfg.Critic.SilenceGuard.SilenceSecs,
		NudgeText:   cfg.Critic.SilenceGuard.NudgeText,
		MaxNudges:   cfg.Critic.SilenceGuard.MaxNudges,
	}
	criticSpec.PreTimeoutNudge = preTimeoutNudgeFor("critic")

	// The worker does not route through bridgeToolOpts (it never asks questions);
	// deliver its permission mode and system prompt explicitly. The seatbelt sandbox
	// is the security boundary — bypassPermissions + --dangerously-skip-permissions
	// disables Claude Code's own permission prompts so headless execution is unblocked.
	workerOpts := []harness.ClaudeCLIOption{
		harness.WithWorkDir(repoPath),
		harness.WithPermissionMode(cfg.Worker.PermissionMode),
		bridgeOptFor("worker"),
		harness.WithAppendSystemPrompt(harness.MergeAppendPrompts(cfg.Worker.SystemPrompt, cfg.Worker.AppendSystemPrompt)),
	}
	workerOpts = append(workerOpts, openAgentOpts()...)
	workerSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Worker.Model, workerSandboxCfg, workerOpts...)
	if specErr != nil {
		slog.Error("failed to build worker spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	workerSpec.AgentID = "worker"
	workerSpec.ExpectsReport = true
	workerSpec.Timeout = cfg.Worker.Timeout.Duration
	workerSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Worker.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Worker.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Worker.LoopGuard.CooldownTurns,
	}
	workerSpec.SilenceGuard = harness.SilenceGuardSpec{
		SilenceSecs: cfg.Worker.SilenceGuard.SilenceSecs,
		NudgeText:   cfg.Worker.SilenceGuard.NudgeText,
		MaxNudges:   cfg.Worker.SilenceGuard.MaxNudges,
	}
	workerSpec.PreTimeoutNudge = preTimeoutNudgeFor("worker")

	wtSpecFn := func(wtPath string) harness.ProcessSpec {
		spec := workerSpec
		sc := worktreeSandboxCfg
		sc.WorktreePath = wtPath
		spec.Sandbox = sc
		spec.WorkDir = wtPath
		return spec
	}

	// Integrator commit-message spec: RO sandbox, no tools, fresh process.
	integratorCommitOpts := []harness.ClaudeCLIOption{
		harness.WithWorkDir(repoPath),
		harness.WithNoTools(),
		harness.WithAppendSystemPrompt(harness.MergeAppendPrompts(cfg.Integrator.SystemPrompt, cfg.Integrator.AppendSystemPrompt)),
	}
	commitMsgSpec, commitSpecErr := harness.BuildProcessSpec(cfg, cfg.Integrator.Model, roSandboxCfg, integratorCommitOpts...)
	if commitSpecErr != nil {
		slog.Error("failed to build integrator commit-msg spec", "err", commitSpecErr)
		os.Exit(exitInvalidInput)
	}
	commitMsgSpec.AgentID = "integrator"
	commitMsgSpec.Timeout = cfg.Integrator.Timeout.Duration

	// Integrator conflict-resolution spec fn: worktree-writable sandbox, Read/Edit tools.
	// A build error is returned (never a zero ProcessSpec, J19) — the caller
	// (IntegrateStep.handleConflict) treats it as give-up-and-preserve, carrying
	// this error in the give-up reason.
	integratorConflictSpecFn := func(wtPath string) (harness.ProcessSpec, error) {
		conflictSandboxCfg := harness.SandboxConfig{
			RepoPath:     repoPath,
			Profiles:     sandboxProfiles,
			Env:          integratorEnv,
			Writable:     false,
			WorktreePath: wtPath,
		}
		conflictOpts := []harness.ClaudeCLIOption{
			harness.WithWorkDir(wtPath),
			harness.WithPermissionMode(cfg.Integrator.PermissionMode),
			harness.WithAllowedTools(cfg.Integrator.AllowedTools),
			harness.WithDisallowedTools(cfg.Integrator.DisallowedTools),
			harness.WithAppendSystemPrompt(harness.MergeAppendPrompts(cfg.Integrator.SystemPrompt, cfg.Integrator.AppendSystemPrompt)),
		}
		conflictSpec, conflictSpecErr := harness.BuildProcessSpec(cfg, cfg.Integrator.Model, conflictSandboxCfg, conflictOpts...)
		if conflictSpecErr != nil {
			return harness.ProcessSpec{}, fmt.Errorf("build integrator conflict spec for worktree %q: %w", wtPath, conflictSpecErr)
		}
		conflictSpec.AgentID = "integrator"
		conflictSpec.Timeout = cfg.Integrator.Timeout.Duration
		return conflictSpec, nil
	}

	return &orchestrator.Engine{
		Config:   cfg,
		RepoPath: repoPath,
		Specs: orchestrator.ProcessSpecs{
			Architect:                archSpec,
			Critic:                   criticSpec,
			Worker:                   workerSpec,
			WorktreeSpecFn:           wtSpecFn,
			Integrator:               commitMsgSpec,
			IntegratorConflictSpecFn: integratorConflictSpecFn,
		},
		RunDirFactory:  orchestrator.DefaultRunDirFactory(repoPath),
		QuestionBridge: bridge,
	}
}

// genericReportNudge is sent to report roles (researcher, architect, critic) both
// 60 s before the hard deadline and when the driftPolicy detects implementation intent.
const genericReportNudge = "[Orchestrator] Session deadline approaching. " +
	"Stop what you are doing and submit your report now by calling " +
	"mcp__orqestra__SubmitReport with the full markdown in the \"report\" argument. " +
	"Partial output is acceptable."

// preTimeoutNudgeFor returns the steering message for a role.
// architect/critic share the generic report-nudge text; worker is kept verbatim.
// The researcher runs as an inline subagent (buildEngine), not as its own
// top-level role passed through this selector.
func preTimeoutNudgeFor(role string) string {
	switch role {
	case "architect", "critic":
		return genericReportNudge
	case "worker":
		return "[Orchestrator] Session deadline approaching. " +
			"If your Validation Report is complete, call mcp__orqestra__SubmitReport " +
			"with the full report markdown now. " +
			"Otherwise continue executing — your file changes are already saved."
	default:
		return ""
	}
}

func resolveConfigPath(name, repoPath string) (string, error) {
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("config file %q stat failed: %w", name, err)
		}
		return name, nil
	}

	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(repoPath, name),
		filepath.Join(repoPath, ".orqestra", name),
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".orqestra", name),
			filepath.Join(home, ".config", "orqestra", name),
		)
	}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}

	var statErr error
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			statErr = err
		}
	}

	if statErr != nil {
		return "", fmt.Errorf("config file search encountered error: %w", statErr)
	}
	return "", fmt.Errorf("config file %q not found in any of: %v", name, candidates)
}

func toolOpts(mcpServers *[]string, allowed, disallowed []string, permissionMode string) []harness.ClaudeCLIOption {
	var opts []harness.ClaudeCLIOption
	if permissionMode != "" {
		opts = append(opts, harness.WithPermissionMode(permissionMode))
	}

	// MCP server selection: controls which servers start and how many tool
	// definitions reach the model (context-window impact).
	// nil = all user MCPs available, [] = no user MCPs, ["x"] = only x.
	if mcpServers != nil {
		if len(*mcpServers) == 0 {
			opts = append(opts, harness.WithNoTools())
		} else {
			opts = append(opts, harness.WithMCPServers(*mcpServers))
		}
	}

	// Tool permission pre-approval. Orqestra agents run in -p pipe mode
	// where interactive permission prompts are impossible. Process
	// allowed/disallowed unconditionally — even in no-tools mode the
	// orqestra bridge MCP tool must be pre-approved.
	if len(allowed) > 0 {
		opts = append(opts, harness.WithAllowedTools(allowed))
	}

	if len(disallowed) > 0 {
		opts = append(opts, harness.WithDisallowedTools(disallowed))
	}
	return opts
}

// openAgentOpts returns permission options applied to every headless agent:
// all MCP tools pre-approved and interactive prompts bypassed. Single
// chokepoint — add here, not per-agent.
func openAgentOpts() []harness.ClaudeCLIOption {
	return []harness.ClaudeCLIOption{
		harness.WithExtraArgs("--dangerously-skip-permissions"),
		harness.WithSettings(`{"permissions":{"allow":["mcp__*"]}}`),
	}
}

// bridgeToolOpts returns CLI options that configure the common agent baseline
// for bridge-enabled agents: block built-in AskUserQuestion, pre-approve all
// tools for pipe mode, and inject the question-routing system prompt nudge.
func bridgeToolOpts(base config.BaseAgentConfig) []harness.ClaudeCLIOption {
	// Block built-in AskUserQuestion — the MCP bridge version replaces it.
	disallowed := append([]string(nil), base.DisallowedTools...)
	if !stringSliceContains(disallowed, "AskUserQuestion") {
		disallowed = append(disallowed, "AskUserQuestion")
	}

	// Use the role's explicit allowed list from config, then add MCP bridge tools.
	// Never add "*" — least-privilege: only the tools the role needs are approved.
	allowed := append([]string(nil), base.AllowedTools...)
	for _, tool := range []string{"mcp__*", "mcp__orqestra__AskUserQuestion"} {
		if !stringSliceContains(allowed, tool) {
			allowed = append(allowed, tool)
		}
	}

	opts := toolOpts(base.MCPServers, allowed, disallowed, base.PermissionMode)

	opts = append(opts, openAgentOpts()...)

	// Deliver the role's full system prompt to the model. It rides --append-system-prompt
	// (layer 5) alongside the question-routing default, so Claude Code's base prompt and
	// CLAUDE.md (layer 4) are preserved. Without this the role instructions never reach the
	// model and every role collapses into plain plan-mode behavior.
	if merged := harness.MergeAppendPrompts(base.SystemPrompt, base.AppendSystemPrompt); merged != "" {
		opts = append(opts, harness.WithAppendSystemPrompt(merged))
	}

	return opts
}

// stringSliceContains reports whether s contains v.
func stringSliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// parseMCPBridgeArgs extracts --socket and --agent-id from the mcp-bridge subcommand args.
func parseMCPBridgeArgs(args []string) (socketPath, agentID string, ok bool) {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--socket":
			socketPath = args[i+1]
			i++
		case "--agent-id":
			agentID = args[i+1]
			i++
		}
	}
	return socketPath, agentID, socketPath != ""
}
