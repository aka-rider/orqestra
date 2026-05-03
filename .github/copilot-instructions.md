# Orqestra — Copilot Instructions

## Project

Orqestra is an LLM agent orchestration system. It coordinates planning, validation, and execution of complex tasks using a pipeline of specialized agents backed by frontier and local LLM models.

## Stack

- **Language**: Go 1.26+
- **Proxy**: CLIProxyAPI SDK (embedded) — routes to Claude Code, Codex, llama-server
- **Testing**: `go test`, TDD-first
- **Distribution**: static binary via `go build`

## Core Principles

1. **Mature solutions** — no cowboy code. Strong types, explicit error handling, no errors swallowed.
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
3. **Swallowing `os.Stat` errors** — E.g., `if _, err := os.Stat(path); err == nil { return path, nil } return "", fmt.Errorf("not found")`. This swallows permission denied or other system level errors. Always propagate the actual error (`%w`).
4. **`_ = err` without `// fire-and-forget: <reason>`** — Banned without explicit doc comment explaining why.
5. **`err != nil` followed by `log` but no `return`** — Log-and-continue is silent degradation. If you log an error, you must also return it or surface it to the user.
6. **Default values that mask misconfiguration** — If a config field is required for operation, its zero value must cause a clear error at startup, not silently produce broken behavior at runtime.
7. **Fallback model/provider resolution** — If `model_ref` doesn't resolve, fail. Don't silently try a different resolution path or return a degraded runner.

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

## Go Engineering DOs and DON'Ts

### DO

- Write the failing test first, then the smallest production change, then run the narrowest relevant `go test` package before broadening.
- Return `(T, error)` from constructors, factories, parsers, resolvers, and runners when configuration, IO, subprocesses, or model references can fail.
- Keep interfaces small and consumer-owned. Prefer concrete structs internally until a seam is needed for tests or alternate implementations.
- Validate all config at load time. Required provider, model, runtime, sandbox, and prompt references must fail before orchestration starts.
- Wrap errors with operation and resource context: `fmt.Errorf("resolve worker model %q: %w", ref, err)`.
- Use table-driven tests for validation matrices and state transitions. Name cases after the behavior, not the implementation detail.
- Prefer channels, `sync.WaitGroup`, contexts, or deterministic test hooks for goroutine coordination.
- Keep package boundaries honest: config resolves config, harnesses run harnesses, TUI renders state, sandbox owns isolation.
- Treat LLM text, file paths, command args, JSON, YAML, and streamed events as hostile until parsed and validated.

### DON'T

- Do not return `nil` interfaces to represent construction failure. Return `(Interface, error)` so callers cannot confuse "disabled" with "misconfigured".
- Do not log and continue after errors that affect correctness, state truth, model selection, sandbox setup, validation, or user-visible output.
- Do not use `time.Sleep` to synchronize tests. Sleeps are timing guesses, not correctness guarantees.
- Do not introduce package-level mutable state for orchestration, UI state, config, or test fixtures.
- Do not add generic abstractions, option plumbing, or framework layers unless there are at least two real call sites that need them now.
- Do not parse structured formats with string slicing when `encoding/json`, `yaml.v3`, shell quoting helpers, or typed structs can do the job.
- Do not silently truncate, drop, or ignore LLM/harness output unless the discarded data is explicitly non-critical and observable in diagnostics.

## Bubble Tea TUI DOs and DON'Ts

### DO

- Keep the Bubble Tea model as the single owner of UI state. External goroutines communicate only by sending immutable `tea.Msg` values.
- Use value receivers for models and sub-models. `Update` returns the updated value plus a command; parents assign the returned child model back.
- Convert callbacks, log sinks, subprocess events, validator results, and sandbox state changes into typed messages with enough context to render truthfully.
- Keep `View()` pure: no IO, no mutation, no goroutines, no logging side effects, no time reads for display state that belongs in the model.
- Model focus explicitly. Keyboard and mouse handling should route through the focused sub-model instead of duplicating key logic in the parent.
- Recalculate viewports and dimensions on every `tea.WindowSizeMsg`; clamp sizes before creating or updating viewport models.
- Use `tea.Batch` only for independent commands and `tea.Sequence` only when ordering is semantically required.

### DON'T

- Do not put `sync.Mutex`, channels, pointers to mutable shared state, or background writers inside Bubble Tea models.
- Do not mutate a sub-model through a pointer receiver from outside the parent `Update` path.
- Do not start goroutines directly from child views. Parent state transitions own async boundaries and cancellation context.
- Do not block `Update` with IO, sleeps, subprocess waits, network calls, validation, or filesystem work.
- Do not pass maps, slices, buffers, pointers, or mutable structs through `p.Send` unless they are deep-copied first.
- Do not let transient errors disappear from the UI. Store the error in model state and keep it visible until the user moves on.

## Common Pitfalls and Gotchas

- **Nil interface trap**: an interface value holding a typed nil is not equal to nil. Avoid nullable interface returns from factories.
- **Loop variable capture**: copy loop variables before launching goroutines or creating closures that outlive the iteration.
- **Scanner token limits**: `bufio.Scanner` has a small default token limit. Set an explicit buffer for streamed LLM JSON lines and handle `scanner.Err()`.
- **Map iteration order**: never rely on map order in rendered output, logs, tests, or generated specs. Sort keys before display or comparison.
- **Context cancellation**: subprocesses, validators, sandboxes, and harness sessions must accept and respect `context.Context`.
- **Deferred cleanup errors**: cleanup may be best-effort, but errors still need to be logged or surfaced with enough resource identity to debug.
- **Pointer receiver drift**: one pointer-style sub-model tends to force mutexes and shared ownership. Convert it back to value-style before building on it.
- **Validation warnings vs failures**: warnings may continue only when the UI clearly displays them; failures must stop the pipeline.
- **Config defaults**: defaults are acceptable for omitted optional settings, not for user-specified references that fail to resolve.

## Repo Audit Corrections for Future Agents

These are the top three instruction-worthy issues found in this repo. Prefer fixing them before building features on top of the affected code.

For a detailed LLM-ready refactoring pass, follow `plan-eliminate-audit-findings.md`.

1. **Make harness construction fail explicitly** — `internal/harness/claude_cli.go` returns `nil` from `NewClaudeCLIFromConfig` when `modelRef` is empty or cannot resolve, and logs instead of returning the error. Change this API to return `(CLIRunner, error)` or split optional construction from required construction. User-specified model references must fail fast.
2. **Convert TUI log panel back to Elm-style state** — `internal/tui/view_log.go` uses pointer receivers plus `sync.Mutex` inside a Bubble Tea sub-model. Replace external mutation with `LogMsg`/typed messages sent through `p.Send`, store entries as plain value state, and update the panel only from `Update`.
3. **Replace sleep-based async tests** — `internal/harness/session_test.go` and `internal/scheduler/scheduler_test.go` use `time.Sleep` to wait for goroutines. Replace with completion channels, `sync.WaitGroup`, context deadlines, or eventually-style polling with explicit timeout and assertion messages.

## Available MCP Servers

The following Model Context Protocol (MCP) servers are currently accessible to the AI orchestrator:

1. **awesome-copilot** (`mcp_awesome-copil_*`): Loads and searches custom instructions, skills, agents, and prompts from the repository.
2. **context7** (`mcp_context7_*`): Queries up-to-date documentation and API references.
3. **markitdown** (`mcp_markitdown_*`): Converts URIs (http, file, data) to Markdown format.
4. **mcp_docker** (`mcp_mcp_docker_*`): Provides Docker-based capabilities including browser interactions, Knowledge Graph observations, Wikipedia search, and dynamic MCP catalog exploration (`mcp-find`, `mcp-add`, etc.).
5. **microsoft_mar** (`mcp_microsoft_mar_*`): Secondary markdown conversion tool.
6. **microsoft_pla** (`mcp_microsoft_pla_*`): Browser and file interaction tools (drag/drop, console messages, file upload).
7. **pylance_mcp_s** (`mcp_pylance_mcp_s_*`): Search Pylance documentation, analyze Python imports, configure environments, and validate syntax.
8. **postgresql_mc** (`mcp_postgresql_mc_*`): Connects to PostgreSQL, gets metrics, server capabilities, query execution, and migration tools.

Always ensure the latest MCP capabilities are used via the active tools instead of attempting raw API interactions.
