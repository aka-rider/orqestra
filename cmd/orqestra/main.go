// E2E headless test
package main

import (
	"context"
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
	sandboxRunner, err := harness.NewRunner(harness.RunnerConfig{
		Sandbox: harness.SandboxConfig{
			RepoPath:     repoPath,
			WorktreePath: "",
			Profiles:     sandboxProfiles,
			Env:          modelEnv,
			Writable:     true,
		},
	}, context.Background())
	if err != nil {
		slog.Error("failed to create sandbox runner", "err", err)
		os.Exit(exitInvalidInput)
	}

	// WorktreeRunnerFactory creates a read-only-repo runner scoped to a worktree.
	// BudgetGuard wraps the worktree runner for token accounting.
	sandboxRunnerCfg := harness.SandboxConfig{
		RepoPath:     repoPath,
		Env:          modelEnv,
		Writable:     false, // repo is read-only; worktree is read-write via WorktreePath
		Profiles:     sandboxProfiles,
		WorktreePath: "",    // set per-call
	}
	worktreeRunnerFactory := func(worktreePath string) harness.Runner {
		wtCfg := sandboxRunnerCfg
		wtCfg.WorktreePath = worktreePath
		r, err := harness.NewRunner(harness.RunnerConfig{
			Sandbox: wtCfg,
			Binary:  "claude",
		}, context.Background())
		if err != nil {
			slog.Error("failed to create worktree runner", "err", err)
			os.Exit(exitInvalidInput)
		}
		return guard.Wrap(r, "worker")
	}

	return &orchestrator.Engine{
		Config:   cfg,
		RepoPath: repoPath,
		Runners: orchestrator.Runners{
			Researcher: guard.Wrap(researcherRunner, "researcher"),
			Architect:  guard.Wrap(architectRunner, "architect"),
			Critic:     guard.Wrap(criticRunner, "critic"),
			Worker:     guard.Wrap(sandboxRunner, "worker"),
		},
		RunDirFactory:         orchestrator.DefaultRunDirFactory(repoPath),
		QuestionBridge:        bridge,
		WorktreeRunnerFactory: worktreeRunnerFactory,
	}
}

func runStreamingToWriter(
	ctx context.Context,
	runner harness.Runner,
	prompt, systemPrompt string,
	w io.Writer,
) (harness.RunResult, error) {
	if w == nil {
		runner.SetEvents(nil)
		runner.Post(systemPrompt)
		runner.Post(prompt)
		var result harness.RunResult
		for ev := range runner.Receive() {
			if ev.Kind == harness.EventError {
				return result, fmt.Errorf("runner error: %s", ev.Text)
			}
			if ev.Kind == harness.EventUsage {
				result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
			}
			if ev.Kind == harness.EventChunk && ev.Text != "" {
				result.Output += ev.Text
			}
		}
		return result, nil
	}

	updates := make(chan harness.Event, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range updates {
			if u.Text != "" {
				_, _ = io.WriteString(w, u.Text)
			}
		}
	}()

	runner.SetEvents(updates)
	runner.Post(systemPrompt)
	runner.Post(prompt)

	var result harness.RunResult
	for ev := range runner.Receive() {
		if ev.Kind == harness.EventError {
			close(updates)
			<-done
			return result, fmt.Errorf("runner error: %s", ev.Text)
		}
		if ev.Kind == harness.EventUsage {
			result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
		}
		if ev.Kind == harness.EventChunk && ev.Text != "" {
			result.Output += ev.Text
		}
	}
	close(updates)
	<-done
	return result, nil
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
