//go:build darwin

package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/project"
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
		socketPath, agentID, invocationID, ok := parseMCPBridgeArgs(args[2:])
		if !ok {
			fmt.Fprintf(stderr, "Usage: orqestra mcp-bridge --socket <path> --agent-id <id> [--invocation-id <nonce>]\n")
			return exitInvalidInput
		}
		if err := mcp.RunServer(socketPath, agentID, invocationID); err != nil {
			slog.Error("mcp-bridge failed", "err", err)
			return exitDomainFailure
		}
		return exitOK
	}

	// Global flags
	var configPath string
	var promptFlag string
	var autoApprove bool
	var planOnly bool
	var verboseStream bool

	fs := flag.NewFlagSet("orqestra", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "orqestra.yaml", "config file name or absolute path")
	fs.StringVar(&promptFlag, "prompt", "", "run headless with this prompt instead of the TUI (WP16)")
	fs.BoolVar(&autoApprove, "auto-approve", false, "headless: never gate on human review (no gate fires); requires --prompt")
	fs.BoolVar(&planOnly, "plan-only", false, "headless: stop after the plan (skip execution and validation); requires --prompt")
	fs.BoolVar(&verboseStream, "verbose-stream", false, "headless: print streamed agent text (deltas) to stdout; requires --prompt")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(stderr, "Usage: orqestra [--config path|preset] [--prompt text [--auto-approve] [--plan-only] [--verbose-stream]]\n")
		return exitInvalidInput
	}

	promptFlag = strings.TrimSpace(promptFlag)
	if promptFlag == "" && (autoApprove || planOnly || verboseStream) {
		fmt.Fprintf(stderr, "Error: --auto-approve/--plan-only/--verbose-stream require --prompt\n")
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

	// Fail fast, before the TUI launches, when claude isn't on PATH — leash
	// resolves everything else (HOME, sandbox-exec, tool detection) itself
	// internally and fails closed the same way sandbox.New used to if
	// something required is missing.
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintf(stderr, "Error: claude CLI not found in PATH: %v\n", err)
		return exitInvalidInput
	}

	switch {
	case promptFlag != "" && len(cmdArgs) > 0:
		fmt.Fprintf(stderr, "Error: --prompt cannot be combined with a subcommand (%q)\n", cmdArgs[0])
		return exitInvalidInput
	case promptFlag == "" && len(cmdArgs) > 0:
		fmt.Fprintf(stderr, "Unknown command: %s\n", cmdArgs[0])
		fmt.Fprintf(stderr, "Available commands: init\n")
		return exitInvalidInput
	}

	// Chokepoint: the only place in production code that builds the engine.
	// Both the headless and TUI paths below share this one construction — a
	// future change to per-role sandbox/spec wiring has exactly one call site
	// to find and touch, not several that can silently drift (which is
	// precisely what happened here: the original draft of this migration
	// missed headless.go's now-removed independent buildEngine call entirely).
	engine := buildEngine(cfg, repoPath)

	// Headless path (WP16/J18): --prompt bypasses the TUI entirely.
	if promptFlag != "" {
		return runHeadlessCommand(engine, promptFlag, autoApprove, planOnly, verboseStream, stdout, stderr)
	}

	// Silence slog during TUI mode — stderr leaks through the alt screen buffer.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := tui.Run(engine, filepath.Base(configPath)); err != nil {
		slog.Error("TUI error", "err", err)
		return exitDomainFailure
	}
	return exitOK
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

// buildEngine constructs the Engine with ProcessSpecs for the RunPipeline
// path. WP14/RC4: detects the sandbox tiers once, then calls
// harness.SpecForRole per role — adding a role is one YAML block
// (config.Config.ExtraRoles) plus one such call, not ~200 lines of
// per-role option-function assembly.
func buildEngine(cfg *config.Config, repoPath string) *orchestrator.Engine {
	// Question bridge for AskUserQuestion/SubmitReport MCP tools.
	socketPath := filepath.Join("/tmp", fmt.Sprintf("orqestra-q-%d.sock", os.Getpid()))
	bridge := mcp.NewQuestionBridge(socketPath)

	// selfBin == "" disables the bridge for every role (degraded, not
	// fatal — CLAUDE.md SS5.3): reporter/executor specs simply omit the
	// inline "orqestra" MCP server, and AskUserQuestion/SubmitReport are
	// unavailable for this run.
	selfBin, selfErr := os.Executable()
	if selfErr != nil {
		slog.Warn("cannot determine self path for MCP bridge, questions disabled", "err", selfErr)
		selfBin = ""
	}

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

	// Resolve the user's allow_read/allow_write/allow_exec + proxy_env config
	// once, into the flat grant lists harness.SandboxConfig wants.
	home := os.Getenv("HOME")
	userReads, userWrites, userExecs, grantErr := resolveSandboxGrants(home, cfg.Sandbox)
	if grantErr != nil {
		slog.Error("invalid sandbox configuration", "err", grantErr)
		os.Exit(exitInvalidInput)
	}
	proxyEnv := resolveProxyEnv(cfg.Sandbox.ProxyEnv)

	// Allow the exact orqestra binary's directory to exec inside the sandbox.
	// This is required for the mcp-bridge subprocess (AskUserQuestion) to start.
	bridgeExecs := userExecs
	if selfDir, selfDirErr := selfExecDir(); selfDirErr == nil {
		bridgeExecs = append([]string{selfDir}, userExecs...)
	} else {
		slog.Error("cannot allow orqestra binary in sandbox; AskUserQuestion unavailable", "err", selfDirErr)
		os.Exit(exitInvalidInput)
	}

	// The four sandbox tiers (config.SandboxTierReadOnly/WorkerWritable/
	// Worktree/Conflict), built once at startup.
	roSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath, Writable: false,
		Reads: userReads, Writes: userWrites, Execs: bridgeExecs,
		ExtraEnv: cfg.Sandbox.ExtraEnv, ProxyEnv: proxyEnv,
	}
	workerSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath, Writable: true,
		Reads: userReads, Writes: userWrites, Execs: bridgeExecs,
		Env: modelEnv, ExtraEnv: cfg.Sandbox.ExtraEnv, ProxyEnv: proxyEnv,
	}
	worktreeSandboxCfg := harness.SandboxConfig{
		RepoPath: repoPath, Writable: false,
		Reads: userReads, Writes: userWrites, Execs: bridgeExecs,
		Env: modelEnv, ExtraEnv: cfg.Sandbox.ExtraEnv, ProxyEnv: proxyEnv,
	}

	// specInput returns the shared per-role invocation input: work dir at the
	// repo root and the bridge wired in (SpecForRole only attaches it for
	// reporter/executor-class roles).
	specInput := func(name string) config.RoleSpecInput {
		return config.RoleSpecInput{
			Name:             name,
			WorkDir:          repoPath,
			BridgeSocketPath: socketPath,
			BridgeBinary:     selfBin,
		}
	}

	archSpec, err := harness.SpecForRole(cfg, specInput("architect"), roSandboxCfg)
	if err != nil {
		slog.Error("failed to build architect spec", "err", err)
		os.Exit(exitInvalidInput)
	}

	criticSpec, err := harness.SpecForRole(cfg, specInput("critic"), roSandboxCfg)
	if err != nil {
		slog.Error("failed to build critic spec", "err", err)
		os.Exit(exitInvalidInput)
	}

	workerSpec, err := harness.SpecForRole(cfg, specInput("worker"), workerSandboxCfg)
	if err != nil {
		slog.Error("failed to build worker spec", "err", err)
		os.Exit(exitInvalidInput)
	}

	// wtSpecFn scopes the already-built worker spec to a worktree — a plain
	// value copy (infallible, matches ProcessSpecs.WorktreeSpecFn's
	// no-error signature), not a second SpecForRole call. It also widens the
	// worktree sandbox's Writes/FutureWrites with the .git-internals grants a
	// linked worktree needs for the worker's own Bash tool to run git
	// successfully (best-effort — see worktreeGitGrants).
	wtSpecFn := func(wtPath string) harness.ProcessSpec {
		spec := workerSpec
		sc := worktreeSandboxCfg
		sc.WorktreePath = wtPath
		gitWrites, gitFutureWrites := worktreeGitGrants(repoPath, wtPath)
		sc.Writes = append(sc.Writes, gitWrites...)
		sc.FutureWrites = append(sc.FutureWrites, gitFutureWrites...)
		spec.Sandbox = sc
		spec.WorkDir = wtPath
		return spec
	}

	// Integrator commit-message spec: RO sandbox, no tools, fresh process.
	commitIn := specInput("integrator")
	commitIn.ToolsOverride = &config.RoleToolsOverride{NoTools: true}
	commitMsgSpec, err := harness.SpecForRole(cfg, commitIn, roSandboxCfg)
	if err != nil {
		slog.Error("failed to build integrator commit-msg spec", "err", err)
		os.Exit(exitInvalidInput)
	}

	// Integrator conflict-resolution spec fn: worktree-writable sandbox,
	// Read/Edit tools (the role's own AllowedTools/DisallowedTools — no
	// ToolsOverride). A build error is returned (never a zero ProcessSpec,
	// J19) — the caller (IntegrateStep.handleConflict) treats it as
	// give-up-and-preserve, carrying this error in the give-up reason.
	// Deliberately does NOT get bridgeExecs (the self-exec/MCP-bridge grant)
	// — conflict mode never wires the MCP bridge, matching today's asymmetry.
	integratorConflictSpecFn := func(wtPath string) (harness.ProcessSpec, error) {
		gitWrites, gitFutureWrites := worktreeGitGrants(repoPath, wtPath)
		conflictSandboxCfg := harness.SandboxConfig{
			RepoPath: repoPath, Writable: false, WorktreePath: wtPath,
			Reads: userReads, Writes: append(append([]string{}, userWrites...), gitWrites...),
			Execs: userExecs, FutureWrites: gitFutureWrites,
			Env: integratorEnv, ExtraEnv: cfg.Sandbox.ExtraEnv, ProxyEnv: proxyEnv,
		}
		in := specInput("integrator")
		in.WorkDir = wtPath
		conflictSpec, specErr := harness.SpecForRole(cfg, in, conflictSandboxCfg)
		if specErr != nil {
			return harness.ProcessSpec{}, fmt.Errorf("build integrator conflict spec for worktree %q: %w", wtPath, specErr)
		}
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

// parseMCPBridgeArgs extracts --socket, --agent-id, and --invocation-id from
// the mcp-bridge subcommand args. --invocation-id (WP12/J34-reports) is
// optional: AgentSupervisor.Run injects it per invocation, but a missing flag
// (a degraded/pre-WP12 launch) is not a usage error — RunServer falls back to
// agent_id correlation and logs the degradation, never fails closed here.
func parseMCPBridgeArgs(args []string) (socketPath, agentID, invocationID string, ok bool) {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--socket":
			socketPath = args[i+1]
			i++
		case "--agent-id":
			agentID = args[i+1]
			i++
		case "--invocation-id":
			invocationID = args[i+1]
			i++
		}
	}
	return socketPath, agentID, invocationID, socketPath != ""
}
