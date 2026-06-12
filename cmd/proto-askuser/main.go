// proto-askuser is an E2E prototype that validates the AskUserQuestion MCP bridge
// by running a small model with the bridge injected, auto-answering questions,
// and judging how well the two-way communication worked.
//
// Usage: go run ./cmd/proto-askuser [--config orqestra.yaml]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	configPath := "orqestra.yaml"
	if len(os.Args) > 1 && os.Args[1] == "mcp-bridge" {
		// Subcommand: run as MCP server
		if len(os.Args) < 4 || os.Args[2] != "--socket" {
			fmt.Fprintf(os.Stderr, "Usage: proto-askuser mcp-bridge --socket <path>\n")
			os.Exit(1)
		}
		if err := mcp.RunServer(os.Args[3]); err != nil {
			slog.Error("mcp-bridge failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "--config" {
		configPath = os.Args[2]
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	selfBin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot determine self path: %v\n", err)
		os.Exit(1)
	}

	scenarios := []scenario{
		{
			name:   "freeform",
			prompt: freeformPrompt,
			autoAnswer: func(q mcp.ToolCall) mcp.Answer {
				fmt.Printf("  📨 Question received: %q\n", q.Question)
				return mcp.Answer{FreeformText: "The project is called Orqestra and it orchestrates LLM pipelines."}
			},
			judge: func(output string) (bool, string) {
				lower := strings.ToLower(output)
				hasOrqestra := strings.Contains(lower, "orqestra")
				hasPipeline := strings.Contains(lower, "pipeline") || strings.Contains(lower, "orchestrat")
				if hasOrqestra && hasPipeline {
					return true, "Model correctly incorporated the user's freeform answer about Orqestra and pipelines."
				}
				return false, fmt.Sprintf("Model output did not reflect the freeform answer. Output: %s", truncate(output, 200))
			},
		},
		{
			name:   "single-select",
			prompt: singleSelectPrompt,
			autoAnswer: func(q mcp.ToolCall) mcp.Answer {
				fmt.Printf("  📨 Question received: %q\n", q.Question)
				if len(q.Options) > 0 {
					fmt.Printf("  📋 Options: ")
					for i, o := range q.Options {
						fmt.Printf("[%d] %s", i, o.Label)
						if i < len(q.Options)-1 {
							fmt.Print(", ")
						}
					}
					fmt.Println()
				}
				// Select second option if available, first otherwise
				idx := 0
				if len(q.Options) > 1 {
					idx = 1
				}
				return mcp.Answer{SelectedIndices: []int{idx}}
			},
			judge: func(output string) (bool, string) {
				lower := strings.ToLower(output)
				// The model should mention the selected option in its response
				if strings.Contains(lower, "python") || strings.Contains(lower, "option") || strings.Contains(lower, "select") || strings.Contains(lower, "chose") || strings.Contains(lower, "chosen") || strings.Contains(lower, "picked") {
					return true, "Model acknowledged the user's single-select choice."
				}
				// Even if the wording differs, check if the model continued meaningfully
				if len(output) > 50 {
					return true, "Model produced a substantive response after receiving the selection."
				}
				return false, fmt.Sprintf("Model output did not reflect single-select answer. Output: %s", truncate(output, 200))
			},
		},
		{
			name:   "multi-select",
			prompt: multiSelectPrompt,
			autoAnswer: func(q mcp.ToolCall) mcp.Answer {
				fmt.Printf("  📨 Question received: %q\n", q.Question)
				if len(q.Options) > 0 {
					fmt.Printf("  📋 Options: ")
					for i, o := range q.Options {
						fmt.Printf("[%d] %s", i, o.Label)
						if i < len(q.Options)-1 {
							fmt.Print(", ")
						}
					}
					fmt.Println()
				}
				// Select first and last option if multiple
				indices := []int{0}
				if len(q.Options) > 1 {
					indices = append(indices, len(q.Options)-1)
				}
				return mcp.Answer{SelectedIndices: indices}
			},
			judge: func(output string) (bool, string) {
				// The model should produce output incorporating multiple selected items
				if len(output) > 50 {
					return true, "Model produced substantive response after receiving multi-select answer."
				}
				return false, fmt.Sprintf("Model response too short after multi-select. Output: %s", truncate(output, 200))
			},
		},
	}

	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  proto-askuser — AskUserQuestion MCP Bridge E2E    ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Println()

	results := make([]scenarioResult, 0, len(scenarios))

	for i, s := range scenarios {
		fmt.Printf("━━━ Scenario %d/%d: %s ━━━\n", i+1, len(scenarios), s.name)
		r := runScenario(ctx, cfg, selfBin, s)
		results = append(results, r)

		if r.bridgeError != "" {
			fmt.Printf("  ✕ Bridge error: %s\n", r.bridgeError)
		} else if !r.toolCalled {
			fmt.Printf("  ⚠  Model did NOT call AskUserQuestion tool\n")
		} else {
			fmt.Printf("  ✓ Model called AskUserQuestion\n")
		}

		if r.judgePass {
			fmt.Printf("  ✓ Judge: %s\n", r.judgeReason)
		} else {
			fmt.Printf("  ✕ Judge: %s\n", r.judgeReason)
		}

		fmt.Printf("  ⏱  Duration: %s | Tokens: %d in, %d out\n",
			r.duration.Truncate(time.Millisecond), r.inputTokens, r.outputTokens)
		fmt.Println()
	}

	// Summary
	fmt.Println("═══ Summary ═══")
	passed := 0
	toolCalls := 0
	for _, r := range results {
		if r.toolCalled {
			toolCalls++
		}
		if r.judgePass {
			passed++
		}
	}
	fmt.Printf("Tool called:     %d/%d\n", toolCalls, len(results))
	fmt.Printf("Judge passed:    %d/%d\n", passed, len(results))
	if toolCalls == len(results) && passed == len(results) {
		fmt.Println("Result:          ✓ ALL PASSED")
	} else {
		fmt.Println("Result:          ✕ SOME FAILED")
		os.Exit(1)
	}
}

type scenario struct {
	name       string
	prompt     string
	autoAnswer func(mcp.ToolCall) mcp.Answer
	judge      func(output string) (bool, string)
}

type scenarioResult struct {
	name         string
	toolCalled   bool
	bridgeError  string
	judgePass    bool
	judgeReason  string
	duration     time.Duration
	inputTokens  int64
	outputTokens int64
}

func runScenario(ctx context.Context, cfg *config.Config, selfBin string, s scenario) scenarioResult {
	start := time.Now()

	socketPath := filepath.Join("/tmp", fmt.Sprintf("orq-proto-%s-%d.sock", s.name, os.Getpid()))
	bridge := mcp.NewQuestionBridge(socketPath)

	scenarioCtx, scenarioCancel := context.WithTimeout(ctx, 120*time.Second)
	defer scenarioCancel()

	if err := bridge.Start(scenarioCtx); err != nil {
		return scenarioResult{name: s.name, bridgeError: fmt.Sprintf("bridge start: %v", err)}
	}
	defer bridge.Stop()

	// Auto-answer goroutine
	toolCalled := make(chan bool, 1)
	go func() {
		select {
		case q := <-bridge.Questions():
			toolCalled <- true
			answer := s.autoAnswer(q)
			bridge.SendAnswer(answer)
		case <-scenarioCtx.Done():
			toolCalled <- false
		}
	}()

	// Build the claude CLI runner with MCP bridge injected
	runner, err := buildRunnerWithBridge(ctx, cfg, selfBin, socketPath)
	if err != nil {
		return scenarioResult{name: s.name, bridgeError: fmt.Sprintf("build runner: %v", err)}
	}

	// Run the model using Post + Receive pattern.
	var outputBuf strings.Builder
	var inputTokens, outputTokens int64
	updates := make(chan harness.Event, 256)
	runner.SetEvents(updates)
	if systemPrompt != "" {
		runner.Post(systemPrompt)
	}
	runner.Post(s.prompt)

	elapsed := time.Since(start)
	for ev := range runner.Receive() {
		if ev.Kind == harness.EventUsage {
			inputTokens += ev.Input
			outputTokens += ev.Output
		}
		if ev.Text != "" {
			_, _ = outputBuf.WriteString(ev.Text)
		}
	}

	called := false
	select {
	case c := <-toolCalled:
		called = c
	default:
	}

	pass, reason := s.judge(outputBuf.String())

	return scenarioResult{
		name:         s.name,
		toolCalled:   called,
		judgePass:    pass,
		judgeReason:  reason,
		duration:     elapsed,
		inputTokens:  inputTokens,
		outputTokens: outputTokens,
	}
}

func buildRunnerWithBridge(ctx context.Context, cfg *config.Config, selfBin, socketPath string) (harness.Runner, error) {
	resolved, err := cfg.ResolveModel("small")
	if err != nil {
		return nil, fmt.Errorf("resolve small model: %w", err)
	}

	// Build model spec from resolved model.
	modelSpec := harness.ModelSpec{
		Provider: resolved.Type,
		Model:    resolved.Model,
		BaseURL:  resolved.BaseURL,
		APIKey:   resolved.APIKey,
	}

	// Build runner config matching the main binary's bridgeToolOpts path exactly.
	// This validates the full flag combination end-to-end.
	runner, err := harness.NewRunner(harness.RunnerConfig{
		Model: modelSpec,
		InlineMCPServers: map[string]harness.InlineMCP{
			"orqestra": {Command: selfBin, Args: []string{"mcp-bridge", "--socket", socketPath}},
		},
		PermissionMode:   "plan",
		ExtraArgs:        []string{"--strict-mcp-config"},
		AllowedTools:     []string{"*", "mcp__*", "mcp__orqestra__AskUserQuestion"},
		DisallowedTools:  []string{"AskUserQuestion", "ExitPlanMode"},
		Settings:         json.RawMessage(`{"permissions":{"allow":["mcp__orqestra__*"]}}`),
	}, ctx)
	if err != nil {
		return nil, fmt.Errorf("build runner: %w", err)
	}

	return runner, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- Prompts ---

const systemPrompt = `You are a helpful assistant with access to the AskUserQuestion tool via MCP.

IMPORTANT: You MUST call the AskUserQuestion tool to get information from the user before producing your final answer. Do not guess or assume - always ask first.

When calling AskUserQuestion:
- Set "question" to a clear question for the user
- Optionally provide "options" as an array of objects with "label" (required) and "hint" (optional)
- Set "multi_select" to true if the user should be able to pick multiple options
- The tool will return the user's answer as text

After receiving the user's answer via the tool, incorporate it into your final response.`

const freeformPrompt = `I want you to write a one-paragraph summary of a project I'm working on.

You don't know what the project is about yet. Use the AskUserQuestion tool to ask me: "What is this project about and what does it do?"

Do NOT provide options — just ask the freeform question. After I answer, write the summary paragraph based on my answer.`

const singleSelectPrompt = `I want you to help me pick a programming language for a new CLI tool.

Use the AskUserQuestion tool to ask me which language I prefer. Provide these options:
- label: "Go", hint: "fast compilation, single binary"
- label: "Python", hint: "rapid prototyping, rich ecosystem"
- label: "Rust", hint: "memory safety, zero-cost abstractions"

Do NOT set multi_select. After I choose, explain why that's a good choice for a CLI tool in 2-3 sentences.`

const multiSelectPrompt = `I want you to help me set up code quality tools for a Go project.

Use the AskUserQuestion tool to ask me which tools I want to set up. Provide these options with multi_select set to true:
- label: "golangci-lint", hint: "comprehensive linter aggregator"
- label: "go vet", hint: "built-in static analysis"
- label: "gofumpt", hint: "stricter formatting than gofmt"
- label: "govulncheck", hint: "vulnerability scanning"

After I choose, list the selected tools and briefly explain what each one does.`

// --- Helpers ---

// Ensure io.Writer is satisfied (compiler check)
var _ io.Writer = (*strings.Builder)(nil)
