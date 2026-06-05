// E2E headless test
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/plan"
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
		if len(args) < 4 || args[2] != "--socket" {
			fmt.Fprintf(stderr, "Usage: orqestra mcp-bridge --socket <path>\n")
			return exitInvalidInput
		}
		if err := mcp.RunServer(args[3]); err != nil {
			slog.Error("mcp-bridge failed", "err", err)
			return exitDomainFailure
		}
		return exitOK
	}

	// Global flags
	var (
		configPath  string
		jsonOutput  bool
		noExecute   bool
		planPath    string
		promptFlag  string
		autoApprove bool
		autoReject  bool
		autoInit    bool
	)

	fs := flag.NewFlagSet("orqestra", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "orqestra.yaml", "config file name or absolute path")
	fs.BoolVar(&jsonOutput, "json", false, "output JSON instead of human-friendly text")
	fs.BoolVar(&noExecute, "no-execute", false, "plan only, skip execution")
	fs.StringVar(&planPath, "plan", "", "path to a plan markdown file; skips prompting and planning")
	fs.StringVar(&promptFlag, "prompt", "", "non-interactive prompt; requires --auto-approve or --auto-reject for headless mode")
	fs.BoolVar(&autoApprove, "auto-approve", false, "auto-approve all gates (headless mode, requires --prompt)")
	fs.BoolVar(&autoReject, "auto-reject", false, "run pipeline through planning then stop (no worker execution; headless, requires --prompt)")
	fs.BoolVar(&autoInit, "auto-init", false, "auto-initialize project in headless mode (creates .orqestra at git root)")

	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(stderr, "Usage: orqestra [--config path|preset] [--json] [--no-execute] [--plan file.md]\n")
		return exitInvalidInput
	}

	cmdArgs := fs.Args()

	// Project root detection and initialization gate — runs before config.
	// The 'init' subcommand also needs this to work without orqestra.yaml.
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

	isHeadless := promptFlag != "" || planPath != ""

	var repoPath string
	if isHeadless {
		// Headless mode: text errors to stderr.
		var gateErr error
		repoPath, gateErr = ensureProjectRoot(baseDir, true, autoInit, stderr)
		if gateErr != nil {
			fmt.Fprintf(stderr, "Error: %v\n", gateErr)
			return exitInvalidInput
		}
	} else {
		// TUI mode: need a real terminal.
		if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
			fmt.Fprintf(stderr, "Error: orqestra requires an interactive terminal.\n")
			fmt.Fprintf(stderr, "Usage: orqestra [flags]\n")
			fmt.Fprintf(stderr, "       orqestra [flags] plan <prompt>\n")
			fmt.Fprintf(stderr, "       orqestra --plan <file.md>\n")
			return exitInvalidInput
		}
		// Render the project-root gate in the TUI.
		switch tui.RunInitGate(baseDir) {
		case tui.InitGateOK, tui.InitGateInitDone:
			repoPath = baseDir
		default:
			return exitUserCancelled
		}
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

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

	// --prompt --auto-reject: headless plan-only mode (no worker execution).
	if promptFlag != "" && autoReject {
		if autoApprove {
			fmt.Fprintf(stderr, "Error: --auto-approve and --auto-reject are mutually exclusive\n")
			return exitInvalidInput
		}
		engine := buildEngine(cfg, sandboxProfiles, repoPath)
		result, err := tui.RunHeadlessPlanOnly(ctx, engine, promptFlag)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]any{"error": err.Error(), "stage": "headless-plan-only"})
			} else {
				slog.Error("headless plan-only failed", "err", err)
			}
			return exitProviderError
		}
		if jsonOutput {
			outputJSON(map[string]any{"status": "plan_only", "prompt": promptFlag, "plan": result.FinalPlan, "run_dir": result.RunDir})
		} else {
			fmt.Fprintln(stdout, result.FinalPlan)
		}
		return exitOK
	}

	// --prompt --auto-approve: headless mode via orchestrator channels.
	if promptFlag != "" && autoApprove {
		engine := buildEngine(cfg, sandboxProfiles, repoPath)
		if err := tui.RunHeadless(ctx, engine, promptFlag); err != nil {
			if jsonOutput {
				outputJSON(map[string]any{"error": err.Error(), "stage": "headless"})
			} else {
				slog.Error("headless execution failed", "err", err)
			}
			return exitProviderError
		}
		if jsonOutput {
			outputJSON(map[string]any{"status": "done", "prompt": promptFlag})
		} else {
			fmt.Println("\nDone.")
		}
		return exitOK
	}
	if promptFlag != "" && !autoApprove {
		fmt.Fprintf(stderr, "Error: --prompt requires --auto-approve or --auto-reject for headless execution\n")
		return exitInvalidInput
	}

	// --plan: load a pre-written plan file and skip the researcher/architect phase.
	if planPath != "" {
		data, readErr := os.ReadFile(planPath)
		if readErr != nil {
			fmt.Fprintf(stderr, "error: cannot read plan file %s: %v\n", planPath, readErr)
			return exitDomainFailure
		}
		content := strings.TrimSpace(string(data))

		if agent.IsNewFormat(content) {
			// New markdown format — pass directly to engine
			engine := buildEngine(cfg, sandboxProfiles, repoPath)
			channels := engine.Start(ctx, orchestrator.Input{
				Prompt:      content,
				AutoApprove: autoApprove || noExecute,
				PlanFile:    content,
				NoExecute:   noExecute,
			})
			for event := range channels.Events {
				if event.Type == orchestrator.EventError {
					slog.Error("pipeline error", "err", event.Err)
					return exitProviderError
				}
			}
			if !noExecute {
				fmt.Println("\nDone.")
			} else {
				fmt.Println("\nPlan loaded (--no-execute). Exiting.")
			}
		} else {
			// Legacy JSON format
			ps, parseErr := plan.LoadFromFile(planPath)
			if parseErr != nil {
				fmt.Fprintf(stderr, "error: cannot parse plan file %s: %v\n", planPath, parseErr)
				return exitDomainFailure
			}
			po := plan.ToPlanOutput(ps)
			spec := po.Spec

			if noExecute {
				if jsonOutput {
					outputJSON(map[string]any{"status": "plan_only", "spec": spec})
				} else {
					fmt.Println("\nPlan loaded (--no-execute). Exiting.")
				}
				return exitOK
			}

			// Execute legacy spec
			resolved, resolveErr := cfg.ResolveModel(cfg.Worker.Model)
			if resolveErr != nil {
				slog.Error("failed to resolve worker model", "err", resolveErr)
				return exitInvalidInput
			}
			modelEnv, modelEnvErr := harness.BuildModelEnv(resolved, cfg.ResolveUtilityModel())
			if modelEnvErr != nil {
				slog.Error("invalid model configuration", "err", modelEnvErr)
				return exitInvalidInput
			}
			workerRunner := harness.NewSandboxCLIRunner(harness.SandboxCLIRunnerConfig{
				Cfg:      cfg.Sandbox,
				Profiles: sandboxProfiles,
				RepoPath: repoPath,
				Env:      modelEnv,
				Writable: true,
			})

			var stdout io.Writer = stdout
			if jsonOutput {
				stdout = io.Discard
			}

			result, execErr := runStreamingToWriter(ctx, workerRunner, agent.BuildExecutionPrompt(spec), "", stdout)
			if execErr != nil {
				slog.Error("execution failed", "err", execErr)
				return exitProviderError
			}

			if jsonOutput {
				outputJSON(map[string]any{"status": "done", "output": result.Output})
			} else {
				fmt.Println("\nDone.")
			}
		}
		return exitOK
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

	// Route subcommands
	switch cmdArgs[0] {
	case "plan":
		if len(cmdArgs) < 2 {
			fmt.Fprintf(stderr, "Usage: orqestra plan <prompt>\n")
			return exitInvalidInput
		}
		prompt := strings.Join(cmdArgs[1:], " ")
		runPlanOnly(ctx, cfg, prompt, jsonOutput, repoPath)
	case "validate":
		if len(cmdArgs) < 2 {
			fmt.Fprintf(stderr, "Usage: orqestra validate <plan-file.md>\n")
			return exitInvalidInput
		}
		runValidateOnly(cmdArgs[1])
	case "exec":
		if len(cmdArgs) < 2 {
			fmt.Fprintf(stderr, "Usage: orqestra exec <plan-file.md>\n")
			return exitInvalidInput
		}
		runExecOnly(ctx, cfg, sandboxProfiles, cmdArgs[1], jsonOutput, repoPath)
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n", cmdArgs[0])
		fmt.Fprintf(stderr, "Available commands: plan, validate, exec, init\n")
		return exitInvalidInput
	}
	return exitOK
}

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// ensureProjectRoot checks that cwd is a git project root and is initialized.
// If not initialized, prompts (TUI) or requires --auto-init (headless).
func ensureProjectRoot(cwd string, isHeadless, autoInit bool, stderr io.Writer) (string, error) {
	if err := project.CheckGitRoot(cwd); err != nil {
		return "", fmt.Errorf("orqestra must be run from the project root directory (no .git found in %s)", cwd)
	}

	if project.IsInitialized(cwd) {
		return cwd, nil
	}

	// Not initialized — gate on init.
	if !isHeadless {
		fmt.Fprintf(stderr, "%s is not initialized.\nInitialize .orqestra? [Y/n] ", cwd)
		var input string
		fmt.Fscanln(os.Stdin, &input)
		if input == "n" || input == "no" || input == "N" || input == "No" {
			return "", fmt.Errorf("not initialized: run 'orqestra init' to set up the project")
		}
		if initErr := project.Init(cwd); initErr != nil {
			return "", fmt.Errorf("failed to initialize project: %w", initErr)
		}
		fmt.Fprintf(stderr, "Initialized .orqestra in %s\n", cwd)
		return cwd, nil
	}

	if autoInit {
		if initErr := project.Init(cwd); initErr != nil {
			return "", fmt.Errorf("failed to auto-initialize project: %w", initErr)
		}
		return cwd, nil
	}

	return "", fmt.Errorf("project %s not initialized: use --auto-init or run 'orqestra init' first", cwd)
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

// buildEngine constructs the full Engine with all runners.
func buildEngine(cfg *config.Config, sandboxProfiles []sandbox.Snapshot, repoPath string) *orchestrator.Engine {
	// Budget guard from pipeline config
	usage := orchestrator.NewRunUsage(cfg.Pipeline.TokenBudget)
	guard := orchestrator.NewBudgetGuard(usage)

	// Question bridge for AskUserQuestion MCP tool
	socketPath := filepath.Join("/tmp", fmt.Sprintf("orqestra-q-%d.sock", os.Getpid()))
	bridge := mcp.NewQuestionBridge(socketPath)

	selfBin, selfErr := os.Executable()
	if selfErr != nil {
		slog.Warn("cannot determine self path for MCP bridge, questions disabled", "err", selfErr)
	}

	// bridgeOpt injects the orqestra MCP server into each runner
	var bridgeOpt harness.ClaudeCLIOption
	if selfErr == nil {
		bridgeOpt = harness.WithInlineMCPServer("orqestra", selfBin, []string{"mcp-bridge", "--socket", socketPath})
	}

	// Researcher
	resOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Researcher.BaseAgentConfig)...)
	if bridgeOpt != nil {
		resOpts = append(resOpts, bridgeOpt)
	}
	researcherRunner, err := harness.NewClaudeCLIFromConfig(cfg, cfg.Researcher.Model, resOpts...)
	if err != nil {
		slog.Error("failed to create researcher runner", "err", err)
		os.Exit(exitInvalidInput)
	}

	// Architect
	plnOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Architect.BaseAgentConfig)...)
	if bridgeOpt != nil {
		plnOpts = append(plnOpts, bridgeOpt)
	}
	architectRunner, err := harness.NewClaudeCLIFromConfig(cfg, cfg.Architect.Model, plnOpts...)
	if err != nil {
		slog.Error("failed to create architect runner", "err", err)
		os.Exit(exitInvalidInput)
	}

	// Critic
	criticOpts := append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Critic.BaseAgentConfig)...)
	if bridgeOpt != nil {
		criticOpts = append(criticOpts, bridgeOpt)
	}
	criticRunner, err := harness.NewClaudeCLIFromConfig(cfg, cfg.Critic.Model, criticOpts...)
	if err != nil {
		slog.Error("failed to create critic runner", "err", err)
		os.Exit(exitInvalidInput)
	}

	// Worker (sandboxed) — no bridge, workers don't ask questions
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
	sandboxRunner := harness.NewSandboxCLIRunner(harness.SandboxCLIRunnerConfig{
		Cfg:      cfg.Sandbox,
		Profiles: sandboxProfiles,
		RepoPath: repoPath,
		Env:      modelEnv,
		Writable: true,
	})

	// WorktreeRunnerFactory creates a read-only-repo runner scoped to a worktree.
	// BudgetGuard wraps the worktree runner for token accounting.
	sandboxRunnerCfg := harness.SandboxCLIRunnerConfig{
		Cfg:      cfg.Sandbox,
		Profiles: sandboxProfiles,
		RepoPath: repoPath,
		Env:      modelEnv,
		Writable: false, // repo is read-only; worktree is read-write via WorktreePath
	}
	worktreeRunnerFactory := func(worktreePath string) harness.ContinuableRunner {
		wtCfg := sandboxRunnerCfg
		wtCfg.WorktreePath = worktreePath
		return guard.WrapContinuable(harness.NewSandboxCLIRunner(wtCfg), "worker")
	}

	return &orchestrator.Engine{
		Config:   cfg,
		RepoPath: repoPath,
		Runners: orchestrator.Runners{
			Researcher: guard.WrapContinuable(researcherRunner, "researcher"),
			Architect:  guard.WrapContinuable(architectRunner, "architect"),
			Critic:     guard.WrapContinuable(criticRunner, "critic"),
			Worker:     guard.WrapContinuable(sandboxRunner, "worker"),
		},
		RunDirFactory:         orchestrator.DefaultRunDirFactory(repoPath),
		QuestionBridge:        bridge,
		WorktreeRunnerFactory: worktreeRunnerFactory,
	}
}

func runPlanOnly(ctx context.Context, cfg *config.Config, prompt string, jsonOutput bool, repoPath string) {
	// Researcher
	researcherRunner, err := harness.NewClaudeCLIFromConfig(cfg, cfg.Researcher.Model,
		append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Researcher.BaseAgentConfig)...)...)
	if err != nil {
		slog.Error("failed to create researcher runner", "err", err)
		os.Exit(exitInvalidInput)
	}
	researcherPlanner := agent.NewPlanner(researcherRunner, cfg.Researcher.SystemPrompt)

	researchPrompt, _ := agent.CheckPromptIntegrity(prompt, prompt)
	researchResult, err := researcherPlanner.Run(ctx, researchPrompt, nil)
	if err != nil {
		slog.Error("research failed", "err", err)
		os.Exit(exitProviderError)
	}

	// Architect
	architectRunner, planErr := harness.NewClaudeCLIFromConfig(cfg, cfg.Architect.Model,
		append([]harness.ClaudeCLIOption{harness.WithWorkDir(repoPath)}, bridgeToolOpts(cfg.Architect.BaseAgentConfig)...)...)
	if planErr != nil {
		slog.Error("failed to create architect runner", "err", planErr)
		os.Exit(exitInvalidInput)
	}
	architectPlanner := agent.NewPlanner(architectRunner, cfg.Architect.SystemPrompt)

	archPrompt, _ := agent.CheckPromptIntegrity(agent.ArchitectPrompt(prompt, researchResult.Plan), prompt)
	archResult, archErr := architectPlanner.Run(ctx, archPrompt, nil)
	if archErr != nil {
		slog.Error("planning failed", "err", archErr)
		os.Exit(exitProviderError)
	}

	if jsonOutput {
		outputJSON(map[string]any{"plan": archResult.Plan})
	} else {
		fmt.Println(archResult.Plan)
	}
}

func runValidateOnly(planPath string) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		slog.Error("reading plan file", "err", err)
		os.Exit(exitInvalidInput)
	}

	content := strings.TrimSpace(string(data))
	if !agent.IsNewFormat(content) {
		fmt.Fprintf(os.Stderr, "Plan file does not start with '# Plan' — not a valid v3 plan.\n")
		os.Exit(exitDomainFailure)
	}
	if !strings.Contains(content, "## Work Packages") {
		fmt.Fprintf(os.Stderr, "Plan file missing '## Work Packages' section.\n")
		os.Exit(exitDomainFailure)
	}
	fmt.Println("Plan structure OK.")
}

func runExecOnly(ctx context.Context, cfg *config.Config, sandboxProfiles []sandbox.Snapshot, planPath string, jsonOutput bool, repoPath string) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		slog.Error("reading plan file", "err", err)
		os.Exit(exitInvalidInput)
	}

	content := strings.TrimSpace(string(data))
	var execPrompt string

	if agent.IsNewFormat(content) {
		execPrompt = agent.BuildExecutionPromptFromPlan(content)
	} else {
		// Legacy JSON spec
		var spec agent.Specification
		if err := json.Unmarshal(data, &spec); err != nil {
			slog.Error("parsing plan file", "err", err)
			os.Exit(exitInvalidInput)
		}
		execPrompt = agent.BuildExecutionPrompt(spec)
	}

	resolved, _ := cfg.ResolveModel(cfg.Worker.Model)
	modelEnv, modelEnvErr := harness.BuildModelEnv(resolved, cfg.ResolveUtilityModel())
	if modelEnvErr != nil {
		slog.Error("invalid model configuration", "err", modelEnvErr)
		os.Exit(exitInvalidInput)
	}
	workerRunner := harness.NewSandboxCLIRunner(harness.SandboxCLIRunnerConfig{
		Cfg:      cfg.Sandbox,
		Profiles: sandboxProfiles,
		RepoPath: repoPath,
		Env:      modelEnv,
		Writable: true,
	})

	// Budget guard for exec-only mode
	usage := orchestrator.NewRunUsage(cfg.Pipeline.TokenBudget)
	guard := orchestrator.NewBudgetGuard(usage)
	runner := guard.WrapContinuable(workerRunner, "worker")

	var stdout io.Writer = os.Stdout
	if jsonOutput {
		stdout = io.Discard
	}

	result, execErr := runStreamingToWriter(ctx, runner, execPrompt, "", stdout)
	if execErr != nil {
		slog.Error("execution failed", "err", execErr)
		os.Exit(exitProviderError)
	}

	if jsonOutput {
		outputJSON(map[string]any{"status": "done", "output": result.Output})
	} else {
		fmt.Println("\nDone.")
	}
}

func outputJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("failed to write JSON output", "err", err)
		os.Exit(exitDomainFailure)
	}
}

func runStreamingToWriter(
	ctx context.Context,
	runner harness.ContinuableRunner,
	prompt, systemPrompt string,
	w io.Writer,
) (harness.RunResult, error) {
	if w == nil {
		return runner.RunStreaming(ctx, prompt, systemPrompt, nil)
	}

	updates := make(chan harness.StreamUpdate, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range updates {
			if u.Text != "" {
				_, _ = io.WriteString(w, u.Text)
			}
		}
	}()

	result, err := runner.RunStreaming(ctx, prompt, systemPrompt, updates)
	close(updates)
	<-done
	return result, err
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
	} else if permissionMode == "plan" {
		opts = append(opts, harness.WithAllowedTools([]string{"*", "mcp__*"}))
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

	// Orqestra agents always run in -p pipe mode where interactive permission
	// prompts are impossible. Pre-approve all built-in and MCP tools
	// unconditionally — the sandbox is the security boundary.
	allowed := append([]string(nil), base.AllowedTools...)
	for _, tool := range []string{"*", "mcp__*", "mcp__orqestra__AskUserQuestion"} {
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
