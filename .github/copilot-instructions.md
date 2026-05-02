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
6. **Harness over Direct API** — Harnesses (like VS Code Copilot, Opencode, Claude Code, and VS Code Third-Party Agents) define model behavior. A raw model API call loses MCP integrations, memory context (`CLAUDE.md`, `/memory`), reasoning loops, autonomous tool usage, execution hooks (`/hooks`), and prompt polishing logic built securely by the providers.
   - Orqestra exists to utilize the full power of these intelligent harnesses—in particular their native third-party agentic setups like Anthropic's Claude SDK built into VS Code Copilot—while automating the repetitive human operator back-and-forth interactions required to keep them on task.
   - Never degrade integration to raw API endpoints when a native, fully-featured agent harness (with workspace editing permissions, built-in debug tooling, and context-awareness) is accessible via `vscode.lm` or local hooks.
7. **Fail fast on corruption** — If data looks inconsistent, abort immediately with a clear error. Never propagate suspect state. Silent failures are bugs.
8. **No silent errors** — Every error must be logged, surfaced, or returned. `_ = err` is banned unless the operation is truly fire-and-forget AND documented why.
9. **User sees truth** — The TUI must never show stale state. If something is running, show what and for how long. If something failed, show why and hold it visible until acknowledged. Errors are specific, actionable, never "something went wrong". Activity is always observable.
10. **Security boundary at LLM output** — LLM-generated content (specs, commands, file paths) is untrusted input. Validate, sanitize, or gate before execution. Never exec() LLM output without human approval.
11. **Idiomatic Go** — `(T, error)` over Result types. Value receivers in Bubble Tea models. No generics where interfaces suffice. Blend into the ecosystem; no surprises.

## Banned Patterns

These are concrete code patterns that violate the core principles. Reject them in code review and never generate them:

1. **Silent fallback on missing user input** — If the user explicitly specifies a file path, URL, model name, or any resource identifier, its absence is ALWAYS an error. Never fall back to defaults when the user expressed intent. `--config foo.yaml` → file must exist or fatal.
2. **`if os.IsNotExist(err) { /* use defaults */ }`** — This is the canonical silent-failure footgun. The only acceptable use is for truly optional files that are auto-discovered (not user-specified).
3. **`_ = err` without `// fire-and-forget: <reason>`** — Banned without explicit doc comment explaining why.
4. **`err != nil` followed by `log` but no `return`** — Log-and-continue is silent degradation. If you log an error, you must also return it or surface it to the user.
5. **Default values that mask misconfiguration** — If a config field is required for operation, its zero value must cause a clear error at startup, not silently produce broken behavior at runtime.
6. **Fallback model/provider resolution** — If `model_ref` doesn't resolve, fail. Don't silently try a different resolution path or return a degraded runner.

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
- **Passing Pointers in Messages**: Never pass structs containing mutable pointers in `tea.Msg` (via `p.Send()`) when streaming from goroutines. BubbleTea requires deep immutability to avoid concurrent map read/write panics. Pass copies or values.
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
