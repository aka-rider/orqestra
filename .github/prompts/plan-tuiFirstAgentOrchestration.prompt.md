# Plan: Platform Architecture — TUI-First Agent Orchestration

## TL;DR

Transform Orqestra from a CLI-arg-driven linear pipeline into a TUI-first platform where: (1) user enters prompt interactively, (2) intent is recognized and confirmed, (3) plan is generated and validated, (4) a YAML-defined execution graph schedules Developer + QA agents in parallel via a configurable provider/model system, (5) results are presented in the TUI. Rejection loops back instead of exiting. **All LLM interaction happens through `claude` CLI subprocesses** — Go code does zero direct HTTP LLM calls. Providers (like `copilot-proxy` in Docker, Ollama, or direct Anthropic) are configured in `orqestra.yaml` and run independently of the main application.

---

## Architecture Overview

**Everything is a `claude` CLI subprocess.** There are no direct HTTP calls to LLM APIs from Go code. The planner, intent agent, validators, and execution agents all run as `claude` processes with provider-specific env vars. Go code is purely orchestration logic — scheduling, state machine, TUI rendering.

```
TUI (prompt input)
  → Intent Agent (claude CLI --print, fast model via provider)
  → Intent Confirmation (TUI)
  → Planner (claude CLI --print, frontier model via provider)
  → Plan Validator (claude CLI --print, cooperative with planner)
  → Plan Display + Approval (TUI, loops on rejection)
  → Scheduler (deterministic, not LLM)
      → reads execution_graph from orqestra.yaml
      → runs agents per DAG: parallel / series / parallel-with-concurrency-N
      → Agent: Developer (claude CLI subprocess → provider's ANTHROPIC_BASE_URL)
          → Validator: Dev Validator (claude CLI --print)
              checks: spec compliance, compilation, linting, CLAUDE.md conventions
      → Agent: QA (claude CLI subprocess → provider's ANTHROPIC_BASE_URL)
          → Validator: QA Validator (claude CLI --print)
              checks: tests match spec, no gaps, code compiles
      → Validators communicate drifts back to Dev/QA
  → Results presented in TUI
  → Failed validation renders step erroneous in TUI
```

### Why Everything Is Claude CLI

The `claude` CLI provides workspace-aware tool use, `CLAUDE.md` context, MCP integrations, and intelligent reasoning loops. Using `claude --print` for single-shot tasks (intent, planning, validation) gives us these capabilities without reimplementing them in Go. The `--print` flag runs non-interactively: it processes the prompt and exits, making it suitable for pipeline stages that produce structured output.

This eliminates the need for `internal/llm/openai.go` and `internal/harness/client.go` as direct HTTP clients. The existing `internal/llm.Provider` interface and `OpenAIProvider` become dead code and should be removed as part of Phase 2.

---

## Core Philosophy: The Harness

Harnesses (like Claude Code, Opencode, VS Code Copilot) **define** model behavior. Calling an underlying model directly via a raw API loses MCP integrations, persistent memory (`CLAUDE.md`, `/memory`), reasoning loops, autonomous tool usage, execution hooks, and prompt polishing. The same model behaves radically differently when invoked via an intelligent harness versus a direct API.

The entire point of Orqestra's execution graph is to utilize what the harness can do best (contextual reasoning, tool application, workspace awareness) but to systematically automate the repetitive back-and-forth loops that usually require a human operator.

---

## Provider Architecture

### The Problem

There is no way to programmatically invoke "Claude mode" GitHub Copilot chats. GitHub Copilot models are accessible only through specific proxy bridges. The application itself should not embed auth flows or proxy servers — that is the provider's job.

### The Solution: External Providers

Providers are **external processes** that expose an API-compatible endpoint. They are NOT part of the Orqestra binary. They are configured in `orqestra.yaml` and may run in Docker, as a system service, or as a local process.

**Example: copilot-proxy** — runs `npx copilot-api@latest start --claude-code` in Docker, exposes `localhost:4141` as an Anthropic-compatible endpoint backed by GitHub Copilot models.

**Example: Ollama** — runs on `192.168.50.212:11434`, exposes OpenAI-compatible endpoint for local models.

**Example: Anthropic direct** — uses `api.anthropic.com` with a real API key.

### YAML Schema: `providers` + `models`

```yaml
providers:
  copilot-proxy:
    base_url: http://localhost:4141
    api_key: dummy                      # copilot-proxy ignores this, but claude CLI requires it
    type: anthropic                     # "anthropic" | "openai" — determines env var mapping

  ollama:
    base_url: http://192.168.50.212:11434
    type: openai

  anthropic:
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}       # env var interpolation
    type: anthropic

models:
  claude-sonnet:
    provider: copilot-proxy
    model: claude-sonnet-4.6            # the model name sent in API requests

  claude-sonnet-small:
    provider: copilot-proxy
    model: claude-sonnet-4.6

  gemini-flash:
    provider: copilot-proxy
    model: gemini-3-flash-preview

  qwen-local:
    provider: ollama
    model: qwen36
```

### How Providers Map to Environment Variables

When the harness wrapper (`internal/harness/`) spawns a `claude` subprocess, it reads the provider config and constructs the environment:

**For `type: anthropic` providers:**
```bash
ANTHROPIC_BASE_URL=<provider.base_url>
ANTHROPIC_AUTH_TOKEN=<provider.api_key>     # or "dummy" if empty
ANTHROPIC_MODEL=<model.model>
ANTHROPIC_DEFAULT_SONNET_MODEL=<model.model>
ANTHROPIC_SMALL_FAST_MODEL=<small_model>    # from execution_graph agent config
ANTHROPIC_DEFAULT_HAIKU_MODEL=<small_model>
```

**For `type: openai` providers:**
```bash
OPENAI_BASE_URL=<provider.base_url>/v1
OPENAI_API_KEY=<provider.api_key>
```

### Hardcoded Harness Environment (always set by `harness.go`, never in YAML)

These are operational flags that the harness wrapper always injects, regardless of provider:

```bash
DISABLE_NON_ESSENTIAL_MODEL_CALLS=1
CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

These prevent the `claude` CLI from making extraneous calls (telemetry, update checks, etc.) that waste tokens and slow execution.

### Provider Setup Scripts (Outside Orqestra)

Providers are managed by dedicated scripts in `scripts/`, not by the main application:

- `scripts/copilot-proxy-up.sh` — starts `npx copilot-api@latest start --claude-code` in Docker
- `scripts/copilot-proxy-down.sh` — stops the Docker container

These are convenience scripts. The user can also run providers manually or via docker-compose.

---

## Phase 1: Command Bar & Slash Commands — The Primary Control Element

**Goal**: A persistent Claude Code-style input bar at the bottom of the screen is the sole interaction point. It accepts natural language prompts AND `/commands` with autocomplete. The log panel is removed from the default layout (moved behind `/logs` command). Rejection and all navigation happen through the command bar, never by exiting.

### Layout (top to bottom)

```
┌─────────────────────────────────────────────────┐
│ [Tab Bar] Planner │ Developer │ QA │ Pipeline   │  ← tabsView (existing)
│                                                 │
│ (active tab content — streaming, plan, etc.)    │  ← streamView/planView
│                                                 │
│                                                 │
├─────────────────────────────────────────────────┤
│ > type a prompt or /command...                  │  ← commandBar (NEW, always visible)
│   /help  /logs  /quit                           │  ← autocomplete ghost (when typing /)
└─────────────────────────────────────────────────┘
```

### Command Registry Architecture

Every command is a `Command` struct registered in a central registry. Help is not an afterthought — it's a required field on registration. Contextual help (what commands are available NOW, given the current state) is computed dynamically.

```go
type Command struct {
    Name        string            // e.g. "plan", "approve", "reject"
    Aliases     []string          // e.g. ["y"] for approve
    Help        string            // one-line description (REQUIRED)
    DetailHelp  string            // multi-line help shown by /help <cmd>
    ValidStates []State           // states where this command is available (empty = always)
    Run         func(args string) tea.Cmd  // executes the command
}
```

### Built-in Commands

| Command       | Aliases       | Available In                      | Help                                                            |
| ------------- | ------------- | --------------------------------- | --------------------------------------------------------------- |
| `/help [cmd]` | `/h`, `/?`    | Always                            | Show available commands or detailed help for a specific command |
| `/plan`       | —             | `StateIdle`                       | Submit the current prompt for intent recognition + planning     |
| `/logs`       | `/log`        | Always                            | Toggle log panel visibility                                     |
| `/status`     | `/s`          | Always                            | Show pipeline state summary                                     |
| `/quit`       | `/q`, `/exit` | Always                            | Exit orqestra                                                   |
| `/clear`      | —             | `StateIdle`                       | Clear the prompt and output                                     |
| `/abort`      | —             | `StatePlanning`, `StateExecuting` | Cancel the current operation                                    |

_Note: Contextual actions like approving or rejecting plans/intents should NOT be exposed as global `/commands`. See **Interactive Prompts** below._

### Contextual Interactive Prompts

Rather than polluting the command bar with global `/approve` / `/reject` commands, confirmation flows (like `StateConfirming`) operate contextually. When a prompt requires a binary decision, the command bar hints at the choices:
`Do you want to [A]pprove or [R]eject?`
(Make sure to color-code interactive elements of the prompt to draw attention: i.e. styling `[A]` and `[R]`). Typing `a` or `r` (or their full words, if configured) processes the confirmation logic.

### New State Machine

```
StateIdle → StateIntentConfirm → StatePlanning → StateValidating → StateConfirming → StateExecuting → StateDone → StateIdle
   ↑              │ (reject)                                            │ (reject)       │ (auto)
   └──────────────┘                                                     └────────────────┘
```

- `StateIdle` replaces `StatePrompt` — command bar is focused, user types prompt or commands
- `StateDone` auto-transitions back to `StateIdle` (prompt bar re-focuses, output stays)
- There is no hard exit state — only `/quit` exits

### Steps

1. **Create command registry** — `internal/tui/commands.go`
   - `CommandRegistry` struct with `Register(Command)`, `Lookup(name) *Command`, `Available(state) []Command`, `Complete(prefix, state) []Command`
   - All commands self-declare `Help` (enforced: `Register` panics if `Help` is empty)
   - `Available(state)` filters commands by `ValidStates` — drives contextual autocomplete

2. **Create command bar sub-model** — `internal/tui/view_commandbar.go`
   - Uses `charmbracelet/bubbles/textinput` for input
   - Persistent at screen bottom, always focused when no modal is active
   - Typing `/` triggers autocomplete overlay showing commands valid for current state
   - Autocomplete filters as user types (fuzzy match on name + aliases)
   - `Enter` on plain text → `PromptSubmitMsg{Prompt}`
   - `Enter` on `/command args` → look up command, call `Run(args)`
   - `Tab` cycles autocomplete suggestions
   - `Esc` dismisses autocomplete without clearing input
   - Contextual hint line below input: shows available commands for current state (dimmed)

3. **Replace state enum** in `internal/tui/model.go`
   - Rename `StatePrompt` concept to `StateIdle` (command bar focused, no active operation)
   - Add `StateIntentConfirm` between idle and planning
   - `StateDone` auto-returns to `StateIdle` after brief display

4. **Restructure Model.View() layout** in `internal/tui/model.go`
   - Top: tabsView (existing, no change)
   - Middle: state-specific overlays (plan confirm, intent confirm) render INSIDE the active tab
   - Bottom: commandBar.View() (always visible, 2-3 lines)
   - Remove logPanel from default View — it toggles on via `/logs` command
   - Help line replaced by contextual command hint from the commandBar

5. **Key routing changes** in `Model.Update()`
   - All keystrokes route to commandBar FIRST (it's always focused)
   - CommandBar intercepts `Enter`, `/`, `Tab`, `Esc`
   - CommandBar passes through `ctrl+c` → model handles quit
   - Tab switching: `alt+1..9` (not bare `tab`, which is now autocomplete; `ctrl+1..9` is unreliable across terminal emulators)

6. **Remove prompt from CLI** in `cmd/orqestra/main.go`
   - `orqestra` with no args → launches TUI at `StateIdle`
   - Keep `runHeadless` for piped stdin

7. **Rework `PipelineFuncs` and `tui.Run()` for looping**
   - Add `prompt string` parameter to `PipelineFuncs.Plan`: `func(ctx context.Context, prompt string, stdout io.Writer) (Specification, error)`
   - `Model` stores the current prompt from `PromptSubmitMsg` on its struct before calling `startPlanning()`
   - `tui.Run()` only returns on `/quit` or `ctrl+c` — **not** on `StateDone`. Current signature `(Specification, bool, error)` changes to just `error` (the TUI owns its own lifecycle now)
   - `StateDone` no longer emits `tea.Quit`. Instead, it auto-transitions back to `StateIdle` (command bar re-focuses, previous output stays visible in tabs)
   - `main.go` simplifies: call `tui.Run(pipeline)`, handle the single error return, exit
   - The looping contract: `StateIdle → (prompt) → ... → StateDone → StateIdle` is internal to the TUI; the caller never sees intermediate specs

8. **Create help view** — `internal/tui/view_help.go`
   - `/help` with no args → renders all available commands for current state in a formatted table
   - `/help <cmd>` → renders `DetailHelp` for that command
   - Rendered as content in the active tab area (ephemeral, replaced on next action)

### New Messages

- `PromptSubmitMsg{Prompt string}` — user entered a natural language prompt
- `CommandMsg{Name string, Args string}` — user entered a /command
- `IntentResultMsg{Rephrased string, Outcome string, Err error}`
- `IntentConfirmMsg{}` / `IntentRejectMsg{}`
- `ToggleLogsMsg{}` — toggle log panel

### Files to create

- `internal/tui/commands.go` — `CommandRegistry`, `Command` struct, built-in command registration
- `internal/tui/commands_test.go` — registry tests (lookup, filtering, autocomplete, help enforcement)
- `internal/tui/view_commandbar.go` — command bar sub-model with input + autocomplete
- `internal/tui/view_commandbar_test.go` — input parsing, autocomplete behavior
- `internal/tui/view_help.go` — help rendering
- `internal/tui/view_intent.go` — intent display (shown in tab area)

### Files to modify

- `internal/tui/model.go` — new states (`StateIdle`, `StateIntentConfirm`), new layout in `View()`, commandBar integration, remove logPanel from default view, store prompt on Model, `StateDone` → `StateIdle` transition (no `tea.Quit`)
- `internal/tui/tui.go` — `Run()` return signature changes to `error`, only returns on quit
- `internal/tui/messages.go` — new message types
- `internal/tui/styles.go` — command bar styles, autocomplete styles, help styles
- `internal/tui/view_confirm.go` — wire to contextual `[A]pprove` or `[R]eject` interactions from the command bar.
- `cmd/orqestra/main.go` — remove prompt arg, simplify to `err := tui.Run(pipeline); os.Exit(...)` pattern

### Verification

- Unit test: `CommandRegistry.Register` panics on empty Help
- Unit test: `Available(StateIdle)` returns only idle-valid commands
- Unit test: plain text Enter → `PromptSubmitMsg`
- Unit test: state transitions — `StateIdle` → submit → `StateIntentConfirm` → (approve) → `StatePlanning`; (reject) → `StateIdle`
- Unit test: `StateDone` auto-transitions to `StateIdle` (does NOT emit `tea.Quit`)
- Unit test: `Run()` only returns after quit command
- Manual: launch `orqestra`, type `/`, see contextual autocomplete; type `/help`, see command table
- Manual: complete a full pipeline cycle, verify TUI returns to `StateIdle` with command bar focused

---

## Phase 2: Provider & Model Configuration System

**Goal**: Replace the current per-component `base_url`/`model` config with a centralized `providers` + `models` system that cleanly separates "where to send requests" from "which model to use".

### YAML Schema

```yaml
providers:
  copilot-proxy:
    base_url: http://localhost:4141
    api_key: dummy
    type: anthropic

  ollama:
    base_url: http://192.168.50.212:11434
    type: openai

models:
  claude-sonnet:
    provider: copilot-proxy
    model: claude-sonnet-4.6

  gemini-flash:
    provider: copilot-proxy
    model: gemini-3-flash-preview

  qwen-local:
    provider: ollama
    model: qwen36
```

### Go Types

```go
type ProviderConfig struct {
    BaseURL string `yaml:"base_url"`
    APIKey  string `yaml:"api_key"`  // supports ${ENV_VAR} interpolation
    Type    string `yaml:"type"`     // "anthropic" | "openai"
}

type ModelConfig struct {
    Provider string `yaml:"provider"` // references key in providers map
    Model    string `yaml:"model"`    // model name sent in API requests
}

type Config struct {
    Providers map[string]ProviderConfig `yaml:"providers"`
    Models    map[string]ModelConfig    `yaml:"models"`
    // ... existing fields reference models by key
    Planner   PlannerConfig   `yaml:"planner"`
    Validator ValidatorConfig `yaml:"validator"`
    // ...
}
```

### Existing Config Migration

The old per-component `base_url`/`model` schema is **dropped entirely** — no backward compatibility. Current fields like `planner.base_url` and `planner.model` are removed. All components reference model keys:

```yaml
planner:
  model: qwen-local    # references key in models map
  system_prompt: ...
```

The `base_url` is resolved from `models[key].provider → providers[key].base_url`. Loading an old-format `orqestra.yaml` will produce a parse error, forcing migration.

### Steps

1. Add `ProviderConfig`, `ModelConfig` types to `internal/config/config.go`
2. Add `Providers map[string]ProviderConfig` and `Models map[string]ModelConfig` to `Config`
3. Add `ResolveModel(name string) (ResolvedModel, error)` method that returns `{BaseURL, APIKey, Model, Type}`
4. Add env var interpolation for `api_key` field (expand `${VAR}` from `os.Getenv`)
5. Remove old per-component `base_url`/`model` fields from `PlannerConfig`, `ValidatorConfig`, `WorkerConfig`, `WorkValidatorConfig` — all components now reference model keys exclusively
6. Remove `internal/llm/openai.go`, `internal/llm/provider.go`, `internal/llm/mock.go` — no direct HTTP LLM calls from Go code (everything goes through `claude` CLI). Remove `internal/harness/client.go` (replaced by `claude_cli.go` in Phase 5)
7. Update `internal/planner/planner.go` to use `ClaudeCLI` in `--print` mode instead of `harness.Client`
8. Update `internal/validator/plan_validator.go` and `work_validator.go` to use `ClaudeCLI --print` instead of `llm.Provider`
9. Validate at load time: every referenced model key exists, every model's provider key exists
10. Rewrite `orqestra.yaml` with the new `providers`/`models` schema — **no backward compatibility** with the old per-component `base_url`/`model` format

### Files to modify

- `internal/config/config.go` — new types, `ResolveModel`, env interpolation, remove old per-component URL/model fields
- `internal/config/config_test.go` — provider/model resolution tests, validation tests
- `internal/planner/planner.go` — use `ClaudeCLI --print` instead of `harness.Client`
- `internal/planner/planner_test.go` — update for new interface
- `internal/validator/plan_validator.go` — use `ClaudeCLI --print` instead of `llm.Provider`
- `internal/validator/plan_validator_test.go` — update for new interface
- `internal/validator/work_validator.go` — use `ClaudeCLI --print` instead of `llm.Provider`
- `internal/validator/work_validator_test.go` — update for new interface
- `cmd/orqestra/main.go` — remove `llm.NewOpenAIProvider` calls, wire `ClaudeCLI`
- `orqestra.yaml` — full rewrite with new schema

### Files to delete

- `internal/llm/openai.go` — replaced by `claude` CLI subprocess
- `internal/llm/provider.go` — interface no longer needed
- `internal/llm/mock.go` — replaced by CLI mock/test harness
- `internal/llm/provider_test.go` — tests for deleted code
- `internal/harness/client.go` — replaced by `claude_cli.go`
- `internal/harness/client_test.go` — tests for deleted code

### Files to create

- `scripts/copilot-proxy-up.sh` — Docker convenience script for copilot-proxy
- `scripts/copilot-proxy-down.sh` — stop copilot-proxy container

### Verification

- Unit test: `ResolveModel("claude-sonnet")` returns correct base_url, api_key, model, type
- Unit test: missing provider reference → error at load time
- Unit test: `${ENV_VAR}` interpolation in api_key works
- Unit test: old-style `base_url`/`model` config → parse error (no backward compat)

---

## Phase 3: Intent Recognition Agent

**Goal**: Fast `claude --print` call rephrases user prompt and clarifies end-result expectations.

**Steps**:

1. Create `internal/intent/intent.go` — `Recognizer` struct with `Recognize(ctx, rawPrompt) (Intent, error)`
2. `Intent` struct: `Rephrased string`, `Outcome string` (artifacts/scaffold/draft/ready-made), `Confidence float64`
3. System prompt focuses on: "What is the concrete end result? Artifacts produced, readiness state (scaffold vs draft vs production-ready), components involved."
4. Uses `ClaudeCLI` in `--print` mode, configured via `intent.model` key in YAML pointing to a fast model (e.g. `gemini-flash` via copilot-proxy, or `qwen-local` via Ollama)
5. Add `IntentConfig` to `Config` struct: `Model` (key into `models` map), `SystemPrompt`
6. Wire into TUI: on `PromptSubmitMsg`, goroutine calls `Recognizer.Recognize()`, sends `IntentResultMsg`
7. TUI displays intent in the active tab area. Command bar hint shows a contextual prompt (e.g. `[A]pprove or [R]eject`) to confirm or revise.

**Files to create**:

- `internal/intent/intent.go`
- `internal/intent/intent_test.go`

**Files to modify**:

- `internal/config/config.go` — add `IntentConfig`
- `internal/tui/model.go` — handle `PromptSubmitMsg` → call intent agent
- `orqestra.yaml` — add `intent:` section

**Verification**:

- Unit test with mock `ClaudeCLI` returning structured intent JSON
- Integration: type vague prompt → see concrete rephrasing

---

## Phase 4: Execution Graph & Deterministic Scheduler

**Goal**: YAML-configurable DAG of agent roles with deterministic scheduling (parallel / series / concurrency-N).

**YAML Schema** (inside `orqestra.yaml`):

```yaml
execution_graph:
  agents:
    - role: developer
      model: claude-sonnet              # references key in models map
      small_model: gemini-flash         # fast model for non-essential calls
      system_prompt_file: .orqestra/prompts/developer.md
      depends_on: []
      validator:
        role: dev-validator
        model: claude-sonnet
        system_prompt_file: .orqestra/prompts/dev-validator.md

    - role: qa
      model: claude-sonnet
      small_model: gemini-flash
      system_prompt_file: .orqestra/prompts/qa.md
      depends_on: []
      validator:
        role: qa-validator
        model: claude-sonnet
        system_prompt_file: .orqestra/prompts/qa-validator.md

  concurrency: 0 # 0 = unlimited parallel, 1 = serial, N = max N concurrent
```

**Steps**:

1. Create `internal/scheduler/scheduler.go` — `Scheduler` struct
   - `Run(ctx, graph ExecutionGraph, spec Specification, notify func(Event)) error`
   - Topological sort of `depends_on` → determine execution waves
   - Add structural validation during initialization implementing **Cycle Detection** via Kahn's algorithm or Tarjan's strongly connected components algorithm. If cycles exist (e.g. A depends on B, B depends on A), safely abort scheduling and display a detailed configuration validation failure in the TUI to prevent deterministic execution deadlocks.
   - Semaphore for concurrency limit (use `golang.org/x/sync/semaphore`)
   - Pure deterministic logic — no LLM calls
2. Create `internal/scheduler/graph.go` — `ExecutionGraph`, `AgentNode`, `ValidatorNode` types
3. Create `internal/scheduler/event.go` — `Event` types: `AgentStarted`, `AgentDone`, `AgentFailed`, `ValidationStarted`, `ValidationPassed`, `ValidationFailed`, `DriftDetected`
4. Add `ExecutionGraph` config parsing to `internal/config/config.go`
5. Scheduler lifecycle:
   - For each ready agent (no unmet `depends_on`): launch claude CLI session
   - On agent completion: run its validator
   - On validator pass: mark agent done, unblock dependents
   - On validator fail: mark agent erroneous, emit `ValidationFailed` event
   - On drift: emit `DriftDetected` to dependent agents (they receive it as context)
6. Wire scheduler events → TUI via `p.Send()` (each agent gets a tab)

**Files to create**:

- `internal/scheduler/scheduler.go`
- `internal/scheduler/scheduler_test.go`
- `internal/scheduler/graph.go`
- `internal/scheduler/event.go`

**Files to modify**:

- `internal/config/config.go` — `ExecutionGraph` config struct
- `internal/tui/model.go` — new `StateExecuting` handlers for scheduler events
- `internal/tui/messages.go` — scheduler event messages
- `orqestra.yaml` — `execution_graph:` section

**Verification**:

- Unit test: DAG with deps resolves correct execution order
- Unit test: DAG throws immediate error if cycle is detected in configured execution graph
- Unit test: concurrency=1 runs serially, concurrency=0 runs all ready agents in parallel
- Unit test: agent failure marks dependents as blocked

---

## Phase 5: Claude Code CLI Harness (Provider-Backed)

**Goal**: Execute `claude` CLI as a subprocess where LLM calls route through the configured provider in `orqestra.yaml`. The harness wrapper constructs the correct environment variables based on the provider type.

### How It Works

1. The scheduler resolves the agent's `model` key → gets `ResolvedModel{BaseURL, APIKey, Model, Type}`
2. The harness wrapper (`internal/harness/claude_cli.go`) maps this to `claude`-compatible env vars based on `Type`
3. The harness **always** injects operational flags that suppress non-essential traffic

### Environment Construction

```go
func (c *ClaudeCLI) buildEnv(resolved ResolvedModel, smallModel ResolvedModel) []string {
    env := os.Environ()

    // Always injected by harness — never in YAML
    env = append(env, "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
    env = append(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")

    switch resolved.Type {
    case "anthropic":
        env = append(env,
            "ANTHROPIC_BASE_URL="+resolved.BaseURL,
            "ANTHROPIC_AUTH_TOKEN="+resolved.APIKey,
            "ANTHROPIC_MODEL="+resolved.Model,
            "ANTHROPIC_DEFAULT_SONNET_MODEL="+resolved.Model,
        )
        if smallModel.Model != "" {
            env = append(env,
                "ANTHROPIC_SMALL_FAST_MODEL="+smallModel.Model,
                "ANTHROPIC_DEFAULT_HAIKU_MODEL="+smallModel.Model,
            )
        }
    case "openai":
        env = append(env,
            "OPENAI_BASE_URL="+resolved.BaseURL+"/v1",
            "OPENAI_API_KEY="+resolved.APIKey,
        )
    }

    return env
}
```

### Steps

1. Create `internal/harness/claude_cli.go` — `ClaudeCLI` struct
   - Uses `os/exec.CommandContext` to run the host's `claude` binary
   - Accepts `ResolvedModel` (primary) and optionally `ResolvedModel` (small/fast)
   - Constructs env based on provider type
   - Streams stdout/stderr back to TUI via callback
   - Supports `--print` mode for non-interactive validation runs
   - Supports `--system-prompt` for role-specific instructions
2. Integrate with `SessionManager` — each agent executes via `ClaudeCLI`
3. Update `internal/harness/session.go` to support `ClaudeCLI` execution path

**Files to create**:

- `internal/harness/claude_cli.go`
- `internal/harness/claude_cli_test.go`

**Files to modify**:

- `internal/harness/session.go` — `Session` execution via `ClaudeCLI`
- `internal/scheduler/scheduler.go` — resolves model config, passes to harness

**Verification**:

- Unit test: `buildEnv` produces correct vars for anthropic provider
- Unit test: `buildEnv` produces correct vars for openai provider
- Unit test: operational flags are always present regardless of provider
- Integration: `claude` subprocess starts and streams output back

---

## Phase 6: Per-Agent Validators (Claude CLI)

**Goal**: Each agent (Developer, QA) has a dedicated validator that checks compliance via a `claude` subprocess targeting the same provider.

**Validator Responsibilities**:

- **Dev Validator**: spec compliance, code compiles, linting passes, CLAUDE.md/copilot-instructions conventions honored
- **QA Validator**: tests match spec acceptance criteria, no gaps, no misinterpretations, code compiles

**Steps**:

1. Validator agents are `claude` CLI subprocesses using `--print` mode (non-interactive, single-shot)
2. Ensure strict boundary isolation:
   - **Shared Context**: Project rules via `CLAUDE.md` and `.github/copilot-instructions.md` (read globally by `claude`)
   - **Isolated Context**: Role-specific system prompt passed via `--system-prompt`
3. The Validator receives:
   - The shared `Specification` goal/steps
   - The specific diffs/output to validate
4. Produces structured `ValidationReport` (pass/warn/fail + issues)
5. On drift detection, the scheduler emits a `DriftDetected` event — injected back to the original worker
6. Create prompt templates in `.orqestra/prompts/` (default, overridable per project)
7. Scheduler runs validator immediately after its agent completes

**Files to create**:

- `.orqestra/prompts/developer.md` — default dev system prompt
- `.orqestra/prompts/qa.md` — default QA system prompt
- `.orqestra/prompts/dev-validator.md` — default dev validator prompt
- `.orqestra/prompts/qa-validator.md` — default QA validator prompt

**Files to modify**:

- `internal/scheduler/scheduler.go` — run validator via CLI after agent, handle drift
- `internal/validator/` — implement `agent_validator.go` orchestration for dynamic prompt validation

**Verification**:

- Unit test: validator receives spec + output, produces ValidationReport
- Integration: developer writes non-idiomatic code → validator catches drift

---

## Phase 7: TUI Enhancements for Multi-Agent Execution

**Goal**: TUI renders parallel agent sessions as tabs with per-step status and error highlighting.

**Steps**:

1. Each agent in the execution graph → one TUI tab (auto-created by scheduler event)
2. Each validator → inline status in parent agent's tab (not a separate tab)
3. Tab states: `⟳ running`, `✓ passed`, `⚠ warning`, `✗ failed`
4. Failed validation: tab header turns red, step marked erroneous in plan view
5. Drift notifications: toast/inline warning in affected agent tab
6. Execution progress: show which graph nodes are done/running/pending
7. Add a "Pipeline" overview tab showing the DAG status visually (simple ASCII)

**Files to modify**:

- `internal/tui/view_tabs.go` — tab state colors, inline validator status
- `internal/tui/view_stream.go` — error highlighting
- `internal/tui/model.go` — scheduler event → tab state mapping
- `internal/tui/messages.go` — scheduler event wrappers

**Files to create**:

- `internal/tui/view_pipeline.go` — DAG overview tab

**Verification**:

- Unit test: scheduler events correctly update tab states
- Manual: run 2-agent graph, see both tabs streaming, validation results inline

---

## Implementation Order & Dependencies

```
Phase 1 (Command Bar + Slash Commands)  ← no deps, start here — CRITICAL PATH
Phase 2 (Provider/Model Config)         ← independent, can parallel with 1
Phase 4 (Scheduler + Graph)             ← independent of 1-2, can parallel
Phase 3 (Intent Recognition)            ← depends on Phase 1 (command bar) + Phase 2 (model config)
Phase 5 (Claude CLI Harness)            ← depends on Phase 2 (provider env mapping)
Phase 6 (Per-Agent Validators)          ← depends on 4 + 5
Phase 7 (TUI Multi-Agent)               ← depends on 1 + 4
```

**Suggested execution order**:

1. **Phase 1 + Phase 2** in parallel — Phase 1 redefines interaction, Phase 2 redefines config backbone
2. **Phase 4** (Scheduler) — can start as soon as Phase 2 types are defined
3. **Phase 3** after Phase 1 + 2 (wires into command bar's `PromptSubmitMsg` using model config)
4. **Phase 5** after Phase 2 (Claude CLI reads provider config for env vars)
5. **Phase 6 + Phase 7** after their deps complete

---

## Decisions

- **Everything is a `claude` CLI subprocess**: Planner, intent agent, validators, and execution agents all run as `claude` processes. Go code does zero direct HTTP LLM calls. This preserves workspace awareness, MCP integrations, `CLAUDE.md` context, and tool use for all pipeline stages.
- **No `internal/llm/` package**: The `Provider` interface, `OpenAIProvider`, and `harness.Client` (direct HTTP clients) are deleted. All LLM interaction goes through `ClaudeCLI` wrapper.
- **Command bar is the sole interaction point**: persistent at bottom, accepts prompts and `/commands`.
- **Help is first-class**: every command declares `Help` (enforced at registration). Contextual help is state-aware.
- **Contextual Prompts over Global Commands**: Explicit choices like `[A]pprove` and `[R]eject` flow contextually rather than permanently occupying `/` space.
- **Logs hidden by default**: toggled via `/logs` command.
- **Prompt is TUI-only**: `orqestra` with no args launches TUI. Headless mode reads stdin pipe.
- **TUI loops, never exits on completion**: `tui.Run()` only returns on `/quit` or `ctrl+c`. `StateDone` auto-transitions back to `StateIdle`. The caller gets a single `error` return.
- **Providers are external**: Orqestra does NOT embed proxy servers or auth flows. Providers (copilot-proxy, Ollama, Anthropic direct) run independently. YAML configures where to point.
- **copilot-proxy in Docker**: `npx copilot-api@latest start --claude-code` runs in Docker, managed by convenience scripts outside the Go binary. Exposes Anthropic-compatible API on `localhost:4141`.
- **Harness wrapper owns operational flags**: `DISABLE_NON_ESSENTIAL_MODEL_CALLS` and `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` are always injected by `harness.go`, never configurable in YAML.
- **Provider type determines env var mapping**: `anthropic` type → `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`/etc. `openai` type → `OPENAI_BASE_URL`/`OPENAI_API_KEY`.
- **No backward compatibility**: Old per-component `base_url`/`model` YAML schema is dropped. The new `providers`/`models` schema is the only supported format.
- **Tab switching via `alt+1..9`**: `ctrl+1..9` is unreliable across terminal emulators (intercepted by iTerm2, VS Code terminal, Terminal.app). `alt+1..9` is reliably passed through.
- **Scheduler is deterministic Go Code (Kahn's Algorithm)**: Pre-emptively checks for node cycles.
- **Validators isolate execution context**: `CLAUDE.md` is shared rule context, while unique System Prompts bound execution parameters in subprocess invocations.
- **Concurrency modes**: full parallel, serial, semaphore-bounded.
- **BubbleTea Concurrency rule**: Absolute immutability. No pointers broadcast via `p.Send()`.

## Excluded (Future)

- Embedded proxy / auth flows inside the Go binary
- Sandboxed execution (container isolation)
- Rollbacks on failure
- Web UI
- Multi-project orchestration
- Persistent conversation history across runs
