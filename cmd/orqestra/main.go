// E2E headless test
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

	// Build per-agent ClaudeCLIOptions for BuildProcessSpec.
	resOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Researcher.BaseAgentConfig)...)
	resOpts = append(resOpts, bridgeOptFor("researcher"), harness.WithMaxTurns(cfg.Researcher.MaxTurns))
	plnOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Architect.BaseAgentConfig)...)
	plnOpts = append(plnOpts, bridgeOptFor("architect"), harness.WithMaxTurns(cfg.Architect.MaxTurns))
	criticOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Critic.BaseAgentConfig)...)
	criticOpts = append(criticOpts, bridgeOptFor("critic"), harness.WithMaxTurns(cfg.Critic.MaxTurns))

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

	// BuildProcessSpec inherits all options already set in resOpts/plnOpts/criticOpts
	// (including AppendSystemPrompt from bridgeToolOpts). Do NOT add an extra
	// WithAppendSystemPrompt here — that would overwrite the one bridgeToolOpts set
	// with the wrong field (BaseAgentConfig.SystemPrompt ≠ AppendSystemPrompt).
	resSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Researcher.Model, roSandboxCfg, resOpts...)
	if specErr != nil {
		slog.Error("failed to build researcher spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	resSpec.AgentID = "researcher"
	resSpec.SteerOnLoop = true
	resSpec.Timeout = cfg.Researcher.Timeout.Duration
	resSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Researcher.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Researcher.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Researcher.LoopGuard.CooldownTurns,
		SilenceSecs:     cfg.Researcher.LoopGuard.SilenceSecs,
	}
	resSpec.PreTimeoutNudge = preTimeoutNudgeFor("researcher")

	archSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Architect.Model, roSandboxCfg, plnOpts...)
	if specErr != nil {
		slog.Error("failed to build architect spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	archSpec.AgentID = "architect"
	archSpec.SteerOnLoop = true
	archSpec.Timeout = cfg.Architect.Timeout.Duration
	archSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Architect.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Architect.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Architect.LoopGuard.CooldownTurns,
		SilenceSecs:     cfg.Architect.LoopGuard.SilenceSecs,
	}
	archSpec.PreTimeoutNudge = preTimeoutNudgeFor("architect")

	criticSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Critic.Model, roSandboxCfg, criticOpts...)
	if specErr != nil {
		slog.Error("failed to build critic spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	criticSpec.AgentID = "critic"
	criticSpec.SteerOnLoop = true
	criticSpec.Timeout = cfg.Critic.Timeout.Duration
	criticSpec.LoopGuard = harness.LoopGuardSpec{
		RepeatThreshold: cfg.Critic.LoopGuard.RepeatThreshold,
		MaxNudges:       cfg.Critic.LoopGuard.MaxNudges,
		CooldownTurns:   cfg.Critic.LoopGuard.CooldownTurns,
		SilenceSecs:     cfg.Critic.LoopGuard.SilenceSecs,
	}
	criticSpec.PreTimeoutNudge = preTimeoutNudgeFor("critic")

	workerSpec, specErr := harness.BuildProcessSpec(cfg, cfg.Worker.Model, workerSandboxCfg,
		harness.WithWorkDir(repoPath))
	if specErr != nil {
		slog.Error("failed to build worker spec", "err", specErr)
		os.Exit(exitInvalidInput)
	}
	workerSpec.AgentID = "worker"
	workerSpec.SteerOnLoop = false
	workerSpec.Timeout = cfg.Worker.Timeout.Duration
	workerSpec.PreTimeoutNudge = preTimeoutNudgeFor("worker")

	wtSpecFn := func(wtPath string) harness.ProcessSpec {
		spec := workerSpec
		sc := worktreeSandboxCfg
		sc.WorktreePath = wtPath
		spec.Sandbox = sc
		spec.WorkDir = wtPath
		return spec
	}

	return &orchestrator.Engine{
		Config:   cfg,
		RepoPath: repoPath,
		Specs: orchestrator.ProcessSpecs{
			Researcher:     resSpec,
			Architect:      archSpec,
			Critic:         criticSpec,
			Worker:         workerSpec,
			WorktreeSpecFn: wtSpecFn,
		},
		RunDirFactory:  orchestrator.DefaultRunDirFactory(repoPath),
		QuestionBridge: bridge,
	}
}


// preTimeoutNudgeFor returns the role-specific steering message sent to an actor
// 60 s before its hard deadline and whenever the event stream has been silent for
// SilenceSecs seconds. The message is designed to prompt the model to emit its
// expected output format immediately rather than continuing to work.
func preTimeoutNudgeFor(role string) string {
	switch role {
	case "researcher":
		return "[Orchestrator] Session deadline in ~60 s. " +
			"Stop exploring and submit whatever you have gathered. " +
			"Call SubmitReport with sections: ## Goal, ## Codebase Facts, " +
			"## Constraints Discovered, ## Gotchas. Partial is fine."
	case "architect":
		return "[Orchestrator] Session deadline in ~60 s. " +
			"Call SubmitReport with your implementation plan. " +
			"Required: # Plan → ## Goal, ## Context, ## Constraints, ## Risks, " +
			"## Work Packages (each with Steps + Done when), ## Verification, " +
			"## Assumptions, ## Gotchas. Submit what you have."
	case "critic":
		return "[Orchestrator] Session deadline in ~60 s. " +
			"Call SubmitReport with your critic report. " +
			"Required: ## Critic Report → ### Blockers Found (Category, Severity, " +
			"Evidence, Impact, Suggested fix), ### Verified Claims, ### Summary."
	case "worker":
		return "[Orchestrator] Session deadline in ~60 s. " +
			"Describe what you are doing and what the next step is. " +
			"If stuck, explain why. Your file changes are already saved — " +
			"do NOT run cleanup, exit, or discard commands."
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

	// MCP deferred tools (loaded after session start) are not pre-approved by
	// --allowedTools alone — Claude CLI's permission system evaluates them
	// separately. Inject a --settings override that adds the bridge tool to
	// the permissions.allow list, ensuring it is approved at tool-use time
	// even when discovered late via deferred_tools_delta.
	opts = append(opts, harness.WithSettings(
		`{"permissions":{"allow":["mcp__orqestra__*"]}}`,
	))

	if base.AppendSystemPrompt != "" {
		opts = append(opts, harness.WithAppendSystemPrompt(base.AppendSystemPrompt))
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
