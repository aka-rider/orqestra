# Orqestra — Copilot Instructions

## Project

Orqestra is an LLM agent orchestration system. It coordinates planning, validation, and execution of complex tasks using a pipeline of specialized agents backed by frontier and local LLM models.

## Stack

- **Language**: Go 1.26+
- **Proxy**: CLIProxyAPI SDK (embedded) — routes to Claude Code, Codex, llama-server
- **Testing**: `go test`, TDD-first
- **Distribution**: static binary via `go build`

## Core Principles

1. **Mature solutions** — no cowboy code. Strong types, explicit error handling.
2. **Pragmatism** — 1 contributor, keep it simple. No premature abstraction.
3. **Small iterations** — always a working MVP. Every commit should be runnable.
4. **TDD** — spec first, tests second, implementation third, validation fourth.
5. **Heavy LLM reliance** — the system orchestrates LLMs; use them for planning and validation.

## Architecture

```
User Prompt
    → Agent
        → Planner (Claude Code via CLIProxyAPI → generates specification)
        → Plan Validator (llama-server → validates specification independently)
        → Human Gate (display plan + confirm)
        → Worker (executes against spec — shells to claude code/opencode/etc)
        → Work Validator (validates output against spec independently)
```

The **specification** is the shared contract between Planner, Worker, and Validator. They operate independently against it.

## Code Conventions

- Standard Go project layout: `cmd/`, `internal/`
- One file per concern. Flat internal packages.
- Errors are values, returned explicitly. No panics in library code.
- Config via YAML (`orqestra.yaml`) + environment variables.
- Tests live next to source: `planner.go` → `planner_test.go`

## LLM Integration

- CLIProxyAPI SDK embedded in-process — handles auth, routing, load balancing
- Claude Code via OAuth (plan mode for planning, full mode for execution)
- llama-server (local) for validation and cheap inference
- All LLM calls routed through proxy's OpenAI-compatible endpoint

## CLI

- Entry point: `cmd/orqestra/main.go`
- Usage: `orqestra <prompt>`
- Human gate: interactive stdin prompt with plan display
- Output: structured plan display for humans, JSON for machine consumption

## Out of Scope (Future)

- Sandboxed workers (container isolation)
- Multi-agent orchestrator (parallel agent coordination)
- Web UI / VSCode extension
- Plugin system

## Bubble Tea (TUI)

### Architecture

The TUI uses the **Elm architecture** (Model-View-Update) via `charmbracelet/bubbletea`:

- **Model**: Immutable state struct. `Update()` returns a new model + optional `tea.Cmd`.
- **View**: Pure function rendering model → string. No side effects.
- **Update**: Handles `tea.Msg`, transitions state, returns commands for async work.

### Package Layout (`internal/tui/`)

- `tui.go` — entry point (`Run`), program setup, panic recovery, `programWriter`
- `model.go` — main model, state routing (`StatePlanning` → `StateConfirming` → `StateExecuting` → `StateDone`)
- `messages.go` — all custom message types (one file, easy to find)
- `styles.go` — shared lipgloss styles (tab, border, color definitions)
- `view_plan.go` — plan display sub-model (viewport-based)
- `view_confirm.go` — y/N confirmation sub-model
- `view_tabs.go` — tabbed container for concurrent harness sessions
- `view_stream.go` — per-tab streaming output (viewport + spinner)

### Patterns

- **Composing models**: Sub-views are plain structs with `Update(msg) (self, tea.Cmd)` and `View() string`. Parent owns routing.
- **Parent-child communication**: Children emit custom messages (e.g., `ConfirmMsg`); parent handles them in its `Update`.
- **Async work**: Return a `tea.Cmd` (a `func() tea.Msg`). Never block in `Update`.
- **Streaming from goroutines**: Use `p.Send(msg)` from external goroutines (see `programWriter` in `tui.go`).
- **Tab navigation**: `tab`/`shift+tab` to cycle, number keys for direct access.
- **State enum routing**: Main model uses `State` iota; `Update` and `View` switch on it.

### Anti-Patterns

- **Blocking in Update**: Never do IO, sleep, or network calls inside `Update`. Use `tea.Cmd`.
- **Mutating model from goroutines**: Never. Use `p.Send()` to deliver messages.
- **Massive switch statements**: Split into sub-model `Update` calls routed by state.
- **Direct IO in Init**: `Init` should only return a `tea.Cmd`, not perform IO directly.
- **Ignoring WindowSizeMsg**: Always handle it — viewport and layout depend on terminal size.

### Gotchas

- **`tea.Quit` timing**: `tea.Quit` is a command, not immediate. The model will receive one more `Update` after sending it.
- **`WindowSizeMsg` on startup**: Sent automatically by bubbletea on program start. Sub-models must be ready for it before other messages.
- **Alt screen buffer**: `tea.WithAltScreen()` uses the alternate screen. If the program panics, the terminal stays in alt screen — always use panic recovery with `p.Kill()`.
- **`Batch` vs `Sequence`**: `tea.Batch` runs commands concurrently; `tea.Sequence` runs them in order. Use Batch for independent work, Sequence for dependent steps.
- **Spinner ticking**: Spinners need `spinner.Tick` returned from `Init` or a command to keep animating.

### Panic/Signal Recovery

```go
defer func() {
    if r := recover(); r != nil {
        p.Kill()
        fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
    }
}()
```

Always wrap `p.Run()` with this pattern. Context cancellation on ctrl+c ensures harness subprocess cleanup.
