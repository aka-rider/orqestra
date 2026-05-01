# Orqestra — Implementation Plan

## Mission

Build a Go CLI that turns a user prompt into a validated task specification using Claude Code (via embedded CLIProxyAPI SDK), validates it through a local llama-server, gates it through human review, then executes through an external coding-agent harness.

The shared contract is the **Specification**. Planner, validators, worker, and CLI must communicate through typed values (Go structs) and explicit results rather than hidden process state.

## Current State

- [x] Go module initialized (`go 1.26`)
- [x] CLIProxyAPI SDK embedded as proxy layer
- [x] Planner sends prompt to Claude Code (plan mode) via proxy
- [x] Human gate displays plan and waits for confirmation
- [x] docker-compose.yml with llama-server for validation
- [x] CLI entry point (`cmd/orqestra/main.go`)
- [x] Plan validator (llama-server validates specification)
- [x] Worker (executes against spec)
- [x] Work validator

## Non-Negotiables

- Go idiomatic code. Errors are values (e.g., `(T, error)` signatures), no panics in library code.
- Standard Go project layout (`cmd/orqestra`, `internal/`).
- TDD-first: define interfaces, write tests, implement, validate. Use standard `go test`.
- All external calls routed through CLIProxyAPI (never call Anthropic API directly).
- Claude Code does the heavy lifting (planning, execution). Orqestra orchestrates.
- Local llama-server provides cheap, independent validation.
- The MVP is stateless except for trace logs during a run.

## Target Architecture

```text
CLI (cmd/orqestra/main.go)
  -> Config Loader (internal/config)
  -> Embedded CLIProxyAPI (internal/harness/proxy)
  -> Planner (internal/planner)
      -> Claude Code via proxy (plan mode)
      -> Specification
  -> PlanValidator (internal/validator)
      -> llama-server via proxy
      -> ValidationReport
  -> Interactive TUI (internal/tui) OR HumanGate (internal/gate)
  -> Worker (internal/worker)
      -> Claude Code via os/exec (full mode)
      -> WorkOutput
  -> WorkValidator (internal/validator)
      -> llama-server via proxy
      -> ValidationReport
```

## IPC Strategy (Inter-Process Communication)

For the MVP, internal components (Planner, PlanValidator, Agent) compile into a single Go binary and communicate via in-memory Go structs, so IPC is not needed internally.

When the Worker shells out to external CLI harnesses like `claude-code`, standard `os/exec` with stdin/stdout streams is the only compatible method, as these external tools are generally built to consume text or JSON via stdio.

**Post-MVP Sandboxed Workers & Plugins:**
If future architectures introduce isolated Go worker daemons or plugins:

1. **hashicorp/go-plugin**: The Go standard for plugin IPC. Uses gRPC over a local loopback connection (stdio is only used for the initial handshake). Features protocol versioning, robust error handling, and streams.
2. **JSON-RPC over Stdio**: Built into standard library (`net/rpc/jsonrpc`). Clean, zero-dependency, easily debuggable.
3. **Msgpack over Stdio**: Faster binary serialization, but loses the debuggability of JSON. Unnecessary unless pushing massive binary blobs, which LLM prompts are not.

---

## Phase 0: Scaffold and Core Primitives

> Goal: Create a runnable, testable Go project skeleton with the shared primitives every later phase depends on.

- [ ] Ensure `go.mod` exists with Go 1.26+.
- [ ] Set up `golangci-lint` for idiomatic linting.
- [ ] Create flat internal source packages:
  - [ ] `internal/config/`
  - [ ] `internal/llm/`
  - [ ] `internal/spec/`
  - [ ] `internal/planner/`
  - [ ] `internal/tui/`
  - [ ] `internal/validator/`
  - [ ] `internal/worker/`
  - [ ] `internal/agent/`
- [ ] Create co-located test files (`*_test.go`) for all packages.
- [ ] Implement shared domain error shapes implementing the `error` interface:
  - [ ] `Code` string
  - [ ] `Message` string
  - [ ] `Category` string
  - [ ] optional `Cause` error
- [ ] Ensure `go test ./...` and `go build ./cmd/orqestra` succeed.

**Tests**

- [ ] Error construction formatting and unwrapping (`errors.Is`, `errors.As`).

**Deliverable**
`go test ./...` succeeds on a clean Go project skeleton.

## Phase 1: Typed Config and Runtime Policy

> Goal: Define the runtime configuration before providers, validators, workers, or CLI behavior depend on it.

- [ ] Implement `Config` Go struct with `yaml` and `env` tags.
- [ ] Support configuration sources in this order:
  - [ ] hard-coded safe defaults
  - [ ] environment variables (e.g., `ORQESTRA_...`)
  - [ ] optional `orqestra.yaml`
  - [ ] CLI flags where applicable
- [ ] Define model routing config:
  - [ ] `Orchestrator`
  - [ ] `Planner`
  - [ ] `PlanValidator`
  - [ ] `WorkValidator`
- [ ] Define provider config:
  - [ ] Anthropic API provider
  - [ ] OpenAI-compatible provider
  - [ ] Ollama through OpenAI-compatible local endpoint
- [ ] Define harness config:
  - [ ] `Type`
  - [ ] `Command`
  - [ ] `Args`
  - [ ] `Cwd`
  - [ ] `Env`
  - [ ] `Timeout` (time.Duration)
  - [ ] `CaptureDiff`
- [ ] Define retry policy:
  - [ ] planner structured-output retries default to `2`
  - [ ] plan validation repair attempts default to `3`
  - [ ] work validation repair attempts default to `1` for MVP
- [ ] Define trace policy:
  - [ ] trace directory default `.orqestra/traces/`
  - [ ] prompt hashing enabled by default
- [ ] Define CLI exit-code policy:
  - [ ] `0` success
  - [ ] `1` validation or expected domain failure
  - [ ] `2` invalid user input or config
  - [ ] `3` external provider or harness failure
  - [ ] `130` user cancelled

**Tests**

- [ ] Defaults parse successfully.
- [ ] Env vars override defaults.
- [ ] Configuration securely redacts sensitive tokens in `.String()` implementations.

**Deliverable**
`config.Load()` returns a `(*Config, error)` that future modules can depend on.

## Phase 2: LLM Provider Abstraction and Tracing

> Goal: A unified interface to call supported models, request structured output, validate responses, and trace calls.

- [ ] Define `LLMProvider` interface in `internal/llm`:

```go
type Provider interface {
 ID() string
 Capabilities() Capabilities
 Generate(ctx context.Context, req *Request) (*Response, error)
}
```

- [ ] Define Go structs for requests and responses (`Request`, `Response`, `Message`).
- [ ] Implement Anthropic provider adapter logic.
- [ ] Implement OpenAI-compatible provider adapter logic.
- [ ] Implement standard `encoding/json` structured response parsing:
  - [ ] Fall back to prompt-constrained JSON with explict parse/validate errors.
  - [ ] Return validation failures as domain Errors.
- [ ] Implement tracing logic (prompt hash, latency, tokens, cost).
- [ ] Add a MockProvider struct for tests.

**Tests**

- [ ] Mock provider returns valid JSON mapped to generic types.
- [ ] Invalid structured output returns explicit typed error.
- [ ] Traces redact raw content unless explicit raw logging is configured.

**Deliverable**
`llm.Generate(ctx, request)` returns typed, validated JSON and trace metadata.

## Phase 3: Specification Contract

> Goal: Define the full shared contract the Planner produces, the Worker executes, and the validators judge.

- [ ] Implement `Specification` Go structs with `json` tags.
- [ ] Required fields:
  - [ ] `SchemaVersion`
  - [ ] `ID`, `Title`, `Goal`, `Context`
  - [ ] `Scope` (globs, dependencies, limits)
  - [ ] `Constraints`, `Assumptions`, `Risks`
  - [ ] `AcceptanceCriteria`
  - [ ] `Steps`
  - [ ] `ValidationCommands`
  - [ ] `AllowedOperations`
  - [ ] `ExpectedArtifacts`
- [ ] Scope glob matching uses a standard Go glob library (like `gobwas/glob`).
- [ ] Implement Markdown rendering for human gating and harness prompts.
- [ ] Include JSON blocks nested inside Markdown to ensure the spec can be unmarshaled deterministically.

**Tests**

- [ ] JSON roundtrip preserves data (`json.Marshal` / `json.Unmarshal`).
- [ ] Minimum valid spec satisfies structural requirements.
- [ ] Step dependency IDs reference existing steps.

**Deliverable**
A Go-native versioned `Specification` contract that is precise enough for execution.

## Phase 4: Validation Report Contract

> Goal: Define one validation result shape for all validators.

- [ ] Implement `ValidationReport` Go struct.
- [ ] Required fields:
  - [ ] `SchemaVersion`
  - [ ] `Verdict`: `"pass"`, `"warn"`, or `"fail"`
  - [ ] `Summary`
  - [ ] `Issues` (array of `Issue` structs with Severity, ID)
  - [ ] `Suggestions`
  - [ ] `Evidence`
- [ ] Define `ValidationCommandResult` struct:
  - [ ] Command, Args, Cwd
  - [ ] Expected vs Actual Exit Code
  - [ ] Stdout/Stderr excerpts

**Tests**

- [ ] Verdict derivation logic correctly aggregates Issue severity.
- [ ] JSON rendering output matches expected LLM constraints.

**Deliverable**
Unified go structs for Plan validation, Work validation, and Agent retry evaluation.

## Phase 5: Planner

> Goal: Given a user prompt and optional context, produce a valid Specification.

- [ ] Implement Planner service:

```go
type Planner interface {
 Plan(ctx context.Context, input *PlanInput) (*spec.Specification, error)
}
```

- [ ] Prompt the LLM using the generic Provider to output JSON matching the `Specification` schema.
- [ ] Parse JSON; handle struct validation errors internally.
- [ ] On schema failure, retry with validation error feedback until attempt budget exhausts.

**Tests**

- [ ] Mock LLM returning valid JSON creates a `*Specification`.
- [ ] Exhausted retries return a wrapped error indicating failure.

**Deliverable**
`planner.Plan(ctx, input)` returns a `*Specification` or an explicit error.

## Phase 6: Plan Validator

> Goal: Independently judge whether a specification is complete, executable, non-contradictory, and testable.

- [ ] Implement `PlanValidator`:

```go
type PlanValidator interface {
 Validate(ctx context.Context, specification *spec.Specification) (*validator.ValidationReport, error)
}
```

- [ ] Add pre-LLM deterministic checks (unique IDs, defined validation commands).
- [ ] Use LLM to generate a `ValidationReport` json object judging the plan's logic.

**Tests**

- [ ] A missing validation command correctly fails the deterministic check without LLM call.
- [ ] Mock invalid and valid report JSON map correctly.

**Deliverable**
Independent safety check for generated plans via llama-server proxy.

## Phase 7: Worker and Harness Adapter

> Goal: Execute a Specification by passing a rendered spec to an external CLI harness via `os/exec`.

- [ ] Target MVP harness: `claude-code`.
- [ ] Define `HarnessAdapter`:

```go
type HarnessAdapter interface {
 Type() string
 Execute(ctx context.Context, input *HarnessInput) (*worker.WorkOutput, error)
}
```

- [ ] Implement invocation protocol using `os/exec.CommandContext`:
  - [ ] Write spec markdown to temporary file with `os.CreateTemp`.
  - [ ] Pass the temp file path or pipe the prompt via `stdin`.
  - [ ] Assign configured standard working directory and environment variables.
    - [ ] Capture `stdout` and `stderr` streams, including support for streaming output to an `io.Writer`.
- [ ] Mock harness execution succeeds and returns bounded `WorkOutput`.
- [ ] Timeout correctly terminates the underlying process.
- [ ] Temporary files are cleaned up correctly using `defer os.Remove()`.

**Deliverable**
`worker.Execute(ctx, spec, harnessConfig)` shells out securely and captures output.

## Phase 8: Work Validator

> Goal: Independently validate the work output against the original Specification.

- [ ] Implement `WorkValidator`:

```go
type WorkValidator interface {
 Validate(ctx context.Context, input *WorkValidationInput) (*validator.ValidationReport, error)
}
```

- [ ] Run deterministic checks (did the `ValidationCommands` exit code `0`?).
- [ ] Prompt the validator model with diff excerpts and work output logs to verify `must` criteria.
- [ ] Output `ValidationReport` indicating if retries are required.

**Deliverable**
Safety barrier verifying that the worker actually accomplished the plan accurately.

## Phase 9: Agent Pipeline

> Goal: Wire Planner, PlanValidator, HumanGate, Worker, and WorkValidator into one explicit runtime flow.

- [ ] Implement `Agent` service encapsulating the explicit pipeline.
- [ ] Stages:
  1. `Planning`
  2. `Plan Validation` (loop back to 1 on fail)
  3. `Human Gate` (via `internal/tui` or `os.Stdin`)
  4. `Execution` (streaming output to TUI tabs)
  5. `Work Validation` (loop back to 4 on fail)
  6. `Complete`
- [ ] Keep track of retry budgets for loops natively in Go logic loops.
- [ ] Emit observable events or callback hooks for tracing pipelines (`StageStart`, `StageComplete`).

**Deliverable**
`agent.Run(ctx, prompt)` executes the fully-featured orchestrator loop.

## Phase 10: CLI

> Goal: Provide the user-facing Go binary interface.

- [ ] Map CLI commands in `cmd/orqestra/main.go` using a lightweight framework like `flag` or `spf13/cobra`.
  - [ ] `run`
  - [ ] `plan`
  - [ ] `validate`
  - [ ] `exec`
- [ ] Read flags (e.g., `--json`, `--config`, `--no-execute`).
- [ ] Output human-friendly text to `stdout` by default; output strict JSON structs when `--json` is explicitly requested.
- [ ] Implement mapping out to standard `os.Exit(code)` responses based on the Pipeline failure dictionary policies.
- [ ] Build via `go build -o orqestra cmd/orqestra/main.go`.

**Deliverable**
Shippable compiled `./orqestra` tool.

## Phase 11: Interactive Bubble Tea TUI

> Goal: Provide a rich, tabbed terminal dashboard for planning, confirmation, and streaming execution logs.

- [ ] Add `charmbracelet/bubbletea`, `charmbracelet/lipgloss`, and `charmbracelet/bubbles` dependencies.
- [ ] Implement `internal/tui` package using the Elm Architecture (Model-View-Update).
  - [ ] `model.go`: Main model routing states (`StatePlanning`, `StateConfirming`, `StateExecuting`, `StateDone`) and signal recovery logic.
  - [ ] `messages.go`: Define custom async IPC messages (`PlanReadyMsg`, `ConfirmMsg`, `StreamChunkMsg`, `HarnessDoneMsg`, `TabSwitchMsg`).
  - [ ] `styles.go`: Shared lipgloss styling for borders, colors, and tabs.
  - [ ] Build child sub-models for plan display (`view_plan`), confirmation gate (`view_confirm`), tabbed navigation (`view_tabs`), and streaming logs (`view_stream`).
- [ ] Wire TUI into CLI entry point (`cmd/orqestra/main.go`).
  - [ ] Use `isatty.IsTerminal` to conditionally route to TUI vs headless `internal/gate`.
  - [ ] Suppress or redirect `slog` output while the alt-screen TUI is active to prevent display corruption.
- [ ] Update `internal/harness` to support streaming outputs with `RunStreaming(ctx, prompt, systemPrompt, stdout)`.
- [ ] Update `.github/copilot-instructions.md` with Bubble Tea TUI patterns, anti-patterns, and gotchas.

**Deliverable**
A fully interactive, tabbed dashboard overlay replacing the bare stdin prompt, cleanly recovering terminal state on panic or ctrl+c.

---

## Model Routing

| Stage | Default Provider | Default Model | Notes |
|-------|------------------|---------------|-------|
| Orchestrator | Anthropic | `claude-opus-4-20250514` | Used by future orchestration policy; Agent itself remains deterministic code |
| Planner | Anthropic | `claude-opus-4-20250514` | Deep reasoning and spec generation |
| PlanValidator | Ollama | `qwen3:32b` | Different model/provider for adversarial independence |
| WorkValidator | Ollama | `qwen3:32b` | Cheap bounded validation loop |
| Worker | Harness-controlled | Configured by harness env | External coding agent owns execution model |

## Pipeline Failure Policy

| Failure | Owner | Retry? | Final Result |
|---------|-------|--------|--------------|
| Invalid config | CLI/config loader | No | typed error, exit `2` |
| Planner returns invalid JSON/schema | Planner | Yes, up to configured attempt budget | planner error if exhausted |
| PlanValidator returns `fail` | Agent | Yes, re-run Planner with feedback report | failed pipeline, exit `1` |
| PlanValidator infrastructure error | Agent | No by default | provider/validator error, exit `3` |
| Human rejects | HumanGate | No | cancelled pipeline, exit `130` |
| Harness command fails | Worker | No by default | harness error, exit `3` |
| WorkValidator returns `fail` | Agent | Yes, re-run Worker with repair context | failed pipeline, exit `1` |
| WorkValidator infrastructure error | Agent | No by default | provider/validator error, exit `3` |
