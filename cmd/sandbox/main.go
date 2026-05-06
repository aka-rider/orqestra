//go:build darwin

// cmd/sandbox is a standalone Darwin-only local sandbox runner for Claude/tool execution.
// It composes detection profiles, constructs a Seatbelt sandbox, and runs commands locally.
//
// Usage:
//
//	sandbox --workspace <path> [options] -- <command> [args...]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/xiii/orqestra/internal/seatbelt"
	"github.com/xiii/orqestra/internal/seatbelt/detect"
)

type arrayFlags []string

func (a *arrayFlags) String() string { return strings.Join(*a, ", ") }
func (a *arrayFlags) Set(s string) error {
	*a = append(*a, s)
	return nil
}

func main() {
	var (
		workspace    string
		writable     bool
		claudeBinary string
		proxyEnvs    arrayFlags
		extraEnvs    arrayFlags

		// Convenience flags for harness env
		anthropicBaseURL   string
		anthropicAPIKey    string
		anthropicAuthToken string
		anthropicModel     string
		smallModelURL      string
		smallModelName     string
	)

	flag.StringVar(&workspace, "workspace", "", "required: workspace directory path")
	flag.BoolVar(&writable, "writable", false, "allow writes to workspace (worker mode)")
	flag.StringVar(&claudeBinary, "claude-binary", "claude", "claude CLI binary name or path")
	flag.Var(&proxyEnvs, "proxy-env", "env var NAME to forward from host (repeatable, must exist)")
	flag.Var(&extraEnvs, "env", "KEY=value env override (repeatable)")

	flag.StringVar(&anthropicBaseURL, "anthropic-base-url", "", "ANTHROPIC_BASE_URL for model routing")
	flag.StringVar(&anthropicAPIKey, "anthropic-api-key", "", "ANTHROPIC_API_KEY for auth")
	flag.StringVar(&anthropicAuthToken, "anthropic-auth-token", "", "ANTHROPIC_AUTH_TOKEN for OAuth")
	flag.StringVar(&anthropicModel, "anthropic-model", "", "ANTHROPIC_MODEL name")
	flag.StringVar(&smallModelURL, "small-model-url", "", "ANTHROPIC_SMALL_FAST_MODEL_BASE_URL")
	flag.StringVar(&smallModelName, "small-model-name", "", "ANTHROPIC_SMALL_FAST_MODEL_NAME")

	flag.Parse()

	// Validate workspace
	if workspace == "" {
		fmt.Fprintln(os.Stderr, "error: --workspace is required")
		flag.Usage()
		os.Exit(2)
	}

	// Command after --
	cmdArgs := flag.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "error: command required after --")
		flag.Usage()
		os.Exit(2)
	}

	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "error: HOME environment variable not set")
		os.Exit(1)
	}

	// --- Compose detect profiles ---

	// Claude is mandatory
	claude, err := detect.DetectClaude(home, claudeBinary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	profiles := []seatbelt.Snapshot{claude}

	// Optional tool detections
	type optionalDetect struct {
		name string
		fn   func() (*seatbelt.Snapshot, error)
	}
	optionals := []optionalDetect{
		{"homebrew", func() (*seatbelt.Snapshot, error) { return detect.DetectHomebrew(home) }},
		{"docker", func() (*seatbelt.Snapshot, error) { return detect.DetectDocker(home) }},
		{"git", func() (*seatbelt.Snapshot, error) { return detect.DetectGit(home) }},
		{"npm", func() (*seatbelt.Snapshot, error) { return detect.DetectNPM(home) }},
	}
	for _, opt := range optionals {
		snap, err := opt.fn()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error detecting %s: %v\n", opt.name, err)
			os.Exit(1)
		}
		if snap != nil {
			profiles = append(profiles, *snap)
		}
	}

	// --- Build harness env ---
	var harnessEnv []string
	harnessFlags := map[string]string{
		"ANTHROPIC_BASE_URL":                  anthropicBaseURL,
		"ANTHROPIC_API_KEY":                   anthropicAPIKey,
		"ANTHROPIC_AUTH_TOKEN":                anthropicAuthToken,
		"ANTHROPIC_MODEL":                     anthropicModel,
		"ANTHROPIC_SMALL_FAST_MODEL_BASE_URL": smallModelURL,
		"ANTHROPIC_SMALL_FAST_MODEL_NAME":     smallModelName,
	}
	for k, v := range harnessFlags {
		if v != "" {
			harnessEnv = append(harnessEnv, k+"="+v)
		}
	}

	// --- Build extra env ---
	extraEnv := make(map[string]string)
	for _, kv := range extraEnvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "error: --env value must be KEY=value, got %q\n", kv)
			os.Exit(2)
		}
		extraEnv[k] = v
	}

	// --- Create sandbox ---
	sb, err := seatbelt.New(seatbelt.Config{
		RepoPath:     workspace,
		RepoWritable: writable,
		Profiles:     profiles,
		HarnessEnv:   harnessEnv,
		ProxyEnv:     []string(proxyEnvs),
		ExtraEnv:     extraEnv,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer sb.Close()

	// --- Run command ---
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := sb.Run(ctx, cmd); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
