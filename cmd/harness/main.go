// Package main implements the harness binary — a standalone Bubble Tea TUI
// that demonstrates bidirectional interaction with the Claude CLI through
// the InteractiveRunner interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

func main() {
	var (
		configPath   string
		prompt       string
		systemPrompt string
		modelRef     string
	)

	fs := flag.NewFlagSet("harness", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "orqestra.yaml", "config file path")
	fs.StringVar(&prompt, "prompt", "", "initial prompt (required)")
	fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt (optional)")
	fs.StringVar(&modelRef, "model", "", "model ref from config (required if config has no models)")
	fs.Parse(os.Args[1:])

	if prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: --prompt is required\n")
		os.Exit(1)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Resolve model: explicit --model flag takes precedence.
	// If not provided, use the first model in config.Models (sorted).
	model := modelRef
	if model == "" {
		names := modelNames(cfg)
		if len(names) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no models in config, use --model\n")
			os.Exit(1)
		}
		model = names[0]
	}

	// Create runner from config.
	runner, err := harness.NewClaudeCLIFromConfig(cfg, model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating runner: %v\n", err)
		os.Exit(1)
	}

	// Create TUI model.
	m := NewModel()
	m.modelRef = model

	// Run program with context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Start session via the Runner interface.
	// SetEvents injects the events channel for stream capture.
	// Post sends the initial prompt as NDJSON, which also starts the process.
	streamUpdates := make(chan harness.Event, 512)
	runner.SetEvents(streamUpdates)
	runner.Post(prompt)
	m.runner = runner

	// Run TUI.
	p := tea.NewProgram(m, tea.WithContext(ctx))

	// Bridge runner.Receive() → Bubble Tea message loop.
	// The Runner.Receive() channel emits typed Event values.
	go func() {
		for ev := range runner.Receive() {
			p.Send(ev)
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// modelNames returns the sorted model keys from the config.
func modelNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Models))
	for k := range cfg.Models {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
