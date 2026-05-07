# Plan: Orqestra v3 — Programmatic Harness Architecture

**Date**: 2026-05-07
**Status**: Cleanup complete. Implementation ready.

**TL;DR**: Rewrite Orqestra from "tmux for Claude sessions" to "mission control for programmatic Claude queries." Harness becomes a `query()` API wrapping `claude --print --output-format stream-json`. Orchestrator is a hardcoded Go control plane — typed agent packages wired in code with invocation-level control (`--max-turns`, `--allowedTools`, `--continue`). YAML config controls parameters (models, prompts, limits), not topology. TUI becomes a full Bubble Tea dashboard with circular buffer-backed stream rendering.

---

## Core Insight

`--print` + `stream-json` + `--max-turns` + `--allowedTools` gives the orchestrator FULL control. Claude never decides when to stop, what tools it can use, or if it needs human input. The orchestrator decides ALL of that by parsing the stream-json output and controlling continuation via `--continue <session-id>`.

---

## Current Codebase (post-cleanup)

```
internal/
  agent/              sandbox_cli_runner.go, session.go, work_audit.go
  config/             config.go, graph.go
  harness/            claude_cli.go
  intent/             intent.go
  plan/               artifact.go, spec.go
  planner/            planner.go
  pm/                 pm.go
  qa/                 gate.go
  sandbox/            builder.go, env.go, path.go, profile.go, sandbox.go
    detect/           detect.go, user_profile.go
  scheduler/          event.go, graph.go, scheduler.go
  tokenlimit/         limiter.go, runner.go, store.go
  types/              types.go
  validator/          plan_validator.go
cmd/orqestra/         main.go
```

### What survived the cleanup

- `sandbox/` — kernel-level process capability restriction (unchanged)
- `config/` — provider/model resolution, execution graph config
- `scheduler/` — DAG execution with Kahn's topological sort, wave parallelism
- `harness/claude_cli.go` — `CLIRunner` interface, `ClaudeCLI`, `RunPrint`, `RunStreaming`, `BuildModelEnv`
- `types/` — `Specification`, `ProjectPlan`, `ValidationReport`, `WorkPackage`, `TopoWaves()`
- `tokenlimit/` — token budget enforcement + SQLite store
- `validator/plan_validator.go` — deterministic + LLM plan validation
- `qa/gate.go` — command execution + LLM work validation
- `planner/` — spec generation via CLIRunner
- `pm/` — work package decomposition with cycle detection
- `intent/` — prompt classification (accept/clarify/reject)
- `agent/sandbox_cli_runner.go` — wraps `claude --print` under sandbox-exec

### What was killed

- `mux/` — raw terminal passthrough multiplexer
- `chrome/` — momentary overlay TUI (Bubble Tea)
- `orchestrator/` — PTY-wired orchestrator (was unused)
- `gate/` — stdin `[y/N]` prompt
- `testutil/` — orphaned fixtures with `init()` pattern
- `agent/{agent,pipeline,pty_native,runner,seatbelt_runner,spec,frontmatter}.go`
- `harness/{client,session}.go` — OpenAI HTTP client + old session manager
- Dependencies: `creack/pty`, `bubbletea`, `lipgloss`, `charmbracelet/x/term`, `testify`

---

## Design Decisions

### 1. Agent architecture: hardcoded packages, not generic config

All 5 agent packages (planner, pm, validator, qa, intent) follow the same skeleton:

```
New(runner CLIRunner, cfg *XConfig) → struct
Do(ctx, input) → (output, error):
    1. Serialize input → prompt string
    2. runner.RunPrint(ctx, prompt, cfg.SystemPrompt)
    3. stripCodeFences() + envelope unwrap
    4. json.Unmarshal → typed Go struct
    5. Post-parse validation
    6. Return typed struct
```

~70% is uniform (runner call, envelope strip, JSON unmarshal). ~30% is agent-specific hooks:

- **Planner**: `parseFlexibleSpec()` — steps as strings OR objects
- **PM**: `validatePlan()` with cycle detection (Kahn's algorithm)
- **PlanValidator**: deterministic pre-checks BEFORE LLM, can short-circuit
- **QA Gate**: command execution phase BEFORE LLM, security allowlist
- **Intent**: own `Verdict` enum (different from `types.Verdict`)

**Decision**: Keep the packages AND the hardcoded control plane. The pipeline topology (which agents run, in what order, what feeds what) is Go code — typed, compile-checked, explicit. YAML config controls parameters only (model, prompt, limits). Extract a shared `harness.ParseLLMOutput()` helper to eliminate the duplicated envelope/fence stripping.

### 2. Control at invocation level

```
Orchestrator has TOTAL control:
  1. Decides: prompt, tools, max-turns, model
  2. Launches: claude --print (one-shot query)
  3. Observes: stream-json events in real-time
  4. Decides: done? continue? ask human? abort?
  5. If continue: --continue <session-id> + prompt
  6. Repeat until stage complete
```

### 3. Circular buffer for stream events

- Ring buffer of last ~1000 events per invocation
- Go channel as transport (bounded)
- TUI subscribes for rendering
- Orchestrator inspects for hooks/steering

### 4. Artifacts: in-memory + on-disk audit

- Typed Go structs flow between stages: `Specification`, `ProjectPlan`, `ValidationReport`
- Run directory with timestamp-slug (e.g., `2026-05-07-143022-rate-limiter`) captures final artifacts
- Existing `SessionDir` pattern renamed to `RunDir`

### 5. TUI: stable layout + complete interaction model

**Layout contract** — every screen uses the same 3-zone structure:

```
┌─────────────────────────────────────────────────────────────────┐
│ HEADER: pipeline phase + goal + elapsed                    fixed│
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│ CONTENT: varies by screen (scrollable)                          │
│                                                                 │
├─────────────────────────────────────────────────────────────────┤
│ INPUT: prompt / status / confirmation                      fixed│
│ KEYS: context-sensitive key legend                         fixed│
└─────────────────────────────────────────────────────────────────┘
```

Header, input, and keys are always in the same terminal rows. Content area is the only thing that changes between screens. Keys legend shows what's available for the CURRENT screen — no hidden shortcuts.

**Exit**: double `Ctrl+C` or double `Ctrl+D`, with "Press Ctrl+C again to exit" after first press (mimics Claude Code). Single `Ctrl+C` cancels current agent / returns to previous screen.

#### Screen 1: Prompt entry

```
 Orqestra v3                                                 ready
─────────────────────────────────────────────────────────────────
 Enter a task description. Be specific about the end state.

 Examples:
   Add token-bucket rate limiting to the API gateway
   Refactor auth middleware to support OAuth2 + OIDC
─────────────────────────────────────────────────────────────────
 > Add rate limiting to the API gateway█
 [Enter] submit | [Ctrl+C Ctrl+C] exit
```

Text input field with line editing. Multi-line support via `Shift+Enter` or `\` continuation.

#### Screen 2: Clarification (intent → clarify)

When intent returns `VerdictClarify` with questions:

```
 Orqestra v3                        ▶ intent (clarifying)    3s
─────────────────────────────────────────────────────────────────
 The following needs clarification:

 1. Which API gateway?
    ○ src/api/gateway.go (internal Go gateway)
    ● src/proxy/nginx.conf (NGINX reverse proxy)
    ○ Both

 2. Rate limit scope?
    ● Per-IP
    ○ Per-user (requires auth)
    ● Per-endpoint
    ○ Global

 3. Persistence?
    > Redis (freeform: user typed this)█
─────────────────────────────────────────────────────────────────
 [↑↓] navigate | [Space] toggle | [Tab] next question | [Enter] submit
```

Questions rendered as multi-select with radio/checkbox depending on cardinality. Last question can be freeform text. `Tab` moves between questions.

#### Screen 3: Pipeline dashboard (main view)

```
 Orqestra v3                  ▶ planning                    46s
─────────────────────────────────────────────────────────────────
 Goal: Add rate limiting to the API gateway

 Agent          State     Time   In Tok   Out Tok  Tok/s  Context
 intake         ✓ done    12s    4,218    1,102    92.3   ████░░░░ 22%
 planner        ▶ run     34s    8,741    2,319    68.1   ██████░░ 45%
 validator      ○ wait    -      -        -        -      ░░░░░░░░  -
 worker-1       ○ wait    -      -        -        -      ░░░░░░░░  -
 worker-2       ○ wait    -      -        -        -      ░░░░░░░░  -

 Total: 16.4k tokens | Budget: 483.6k remaining
─────────────────────────────────────────────────────────────────
 Pipeline running...
 [Enter] expand agent | [S] stop agent | [Ctrl+C Ctrl+C] exit
```

#### Screen 4: Agent detail (expanded)

```
 Orqestra v3                  ▶ planning                    46s
─────────────────────────────────────────────────────────────────
 ▶ planner | 8,741 in | 2,319 out | 68.1 tok/s | 45% ctx
 model: claude-opus-4 | ses_01JX... | turn 3/5
 ──────────────────────────────────────────────────────────
  🔧 Read(src/api/gateway.go)              ✓ 0.4s  +1,204 tok
  🔧 Read(src/api/middleware.go)           ✓ 0.2s  +891 tok
  🔧 Bash(grep -rn "rate" src/)            ✓ 0.8s  +342 tok
 ──────────────────────────────────────────────────────────
 ▍ Based on the codebase analysis, here is the specification:
 ▍
 ▍ Goal: Add token-bucket rate limiting to the API gateway...
 ▍ Steps:
 ▍   1. Create internal/ratelimit/bucket.go with...
 ▍   2. Add middleware in src/api/middleware.go...
 ▍   ▌
─────────────────────────────────────────────────────────────────
 Streaming...
 [↑↓] scroll | [F] follow | [S] stop agent | [Esc] back
```

#### Screen 5: Plan review

After planner completes, before execution. Scrollable spec with approve/reject/edit:

```
 Orqestra v3                  ○ review plan                  2m14s
─────────────────────────────────────────────────────────────────
 Goal: Add token-bucket rate limiting to the API gateway

 Steps:
   1. Create internal/ratelimit/bucket.go implementing
      a sliding-window token bucket with configurable
      rate and burst size.
   2. Add RateLimitMiddleware in src/api/middleware.go
      that extracts client IP and applies per-IP limits.
   3. Add configuration in config.yaml: rate_limit.enabled,
      rate_limit.requests_per_second, rate_limit.burst.
   4. Write unit tests in internal/ratelimit/bucket_test.go
      covering: normal flow, burst, exhaustion, refill.

 Acceptance:
   • Rate limiting returns 429 when exceeded
   • Configurable via YAML without code changes
   • Unit test coverage ≥ 90% for ratelimit package

 Constraints:
   • No external dependencies (no Redis)
   • Must not break existing middleware chain
─────────────────────────────────────────────────────────────────
 Approve this plan?
 [Y] approve | [N] reject (re-plan) | [E] edit | [↑↓] scroll
```

`Y` → proceed to PM decomposition + execution.
`N` → returns to prompt with feedback: "Previous plan rejected. Please re-plan with: ..."
`E` → opens plan in `$EDITOR` (like `git commit`), reloads on save, returns to review.

#### Screen 6: Agent failure

```
 Orqestra v3                  ✗ worker-1 failed              4m32s
─────────────────────────────────────────────────────────────────
 Goal: Add rate limiting to the API gateway

 Agent          State     Time   In Tok   Out Tok  Tok/s  Context
 worker-1       ✗ FAIL    1m12s  22,481   8,102    -      ████████ 89%
 worker-2       ✓ done    58s    18,200   6,440    -      ██████░░ 72%

 Error: context window exhausted (89% → budget exceeded)
─────────────────────────────────────────────────────────────────
 Worker failed. What next?
 [R] retry worker-1 | [S] skip (continue with worker-2 only) | [A] abort
```

#### Screen 7: QA gate result

```
 Orqestra v3                  ○ QA review                    6m18s
─────────────────────────────────────────────────────────────────
 QA Gate: WARN

 ✓ go build ./... — passed
 ✓ go test ./internal/ratelimit/... — 4/4 passed
 ✗ go vet ./... — 1 warning: unused variable in middleware.go:42
 ✓ Acceptance: 429 on rate exceed — verified
 ✓ Acceptance: YAML config — verified
 ~ Acceptance: 90% coverage — 87% (close but below threshold)

 Summary: Minor issues found. Code is functional but has a vet
 warning and coverage is 3% below threshold.
─────────────────────────────────────────────────────────────────
 QA found issues.
 [A] accept anyway | [F] fix (re-run worker) | [R] full report | [↑↓] scroll
```

#### Screen 8: Completion summary

```
 Orqestra v3                  ✓ complete                     7m42s
─────────────────────────────────────────────────────────────────
 Goal: Add rate limiting to the API gateway

 Pipeline:  ✓ intake → ✓ planner → ✓ validator → ✓ workers → ✓ QA

 Files changed:
   A  internal/ratelimit/bucket.go
   A  internal/ratelimit/bucket_test.go
   M  src/api/middleware.go
   M  config.yaml

 Tokens: 42,818 in | 14,207 out | Budget: 442.9k remaining
 Elapsed: 7m42s | Run: .orqestra/runs/2026-05-07-143022-rate-limiter
─────────────────────────────────────────────────────────────────
 Done.
 [Enter] new task | [D] diff detail | [Q] quit
```

#### Interaction summary

| Action | Key | Available on |
|---|---|---|
| Submit prompt / confirm | `Enter` | prompt, clarification, plan review |
| Navigate list / scroll | `↑↓` | all scrollable screens |
| Toggle option | `Space` | clarification |
| Next question | `Tab` | clarification |
| Expand agent | `Enter` | dashboard |
| Back / cancel | `Esc` | agent detail, plan edit |
| Stop running agent | `S` | dashboard, agent detail |
| Follow (auto-scroll) | `F` | agent detail |
| Approve plan | `Y` | plan review |
| Reject plan | `N` | plan review |
| Edit plan | `E` | plan review |
| Retry failed agent | `R` | failure screen |
| Skip failed agent | `S` | failure screen |
| Abort pipeline | `A` | failure screen |
| Exit (graceful) | `Ctrl+C Ctrl+C` or `Ctrl+D Ctrl+D` | everywhere |
| New task | `Enter` | completion |
| Quit | `Q` | dashboard, completion |

### 6. Agent stats data model

Every agent maintains an `AgentStats` struct, updated in real-time from stream events. This is the **primary data contract** between harness → orchestrator → TUI.

```go
type AgentStats struct {
    ID         string        // "planner", "worker-1", etc.
    State      AgentState    // Waiting, Running, Done, Failed
    Model      string        // resolved model name for display
    StartedAt  time.Time
    Elapsed    time.Duration
    InTokens   int64         // cumulative input tokens
    OutTokens  int64         // cumulative output tokens
    TokPerSec  float64       // output tokens / elapsed seconds (live)
    ContextPct float64       // (InTokens + OutTokens) / model context window
    SessionID  string        // for --continue
    Turns      int           // current turn count
    MaxTurns   int           // from config
    ToolCalls  []ToolCallSummary  // name, duration, token delta
    Error      error         // non-nil on failure
}

type ToolCallSummary struct {
    Name     string
    Duration time.Duration
    TokensIn int64         // tokens added to context by this tool result
    Status   string        // "ok", "error"
}
```

**Data flow**: stream-json `→` harness parses into `StreamEvent` `→` orchestrator updates `AgentStats` `→` emits `tea.Msg` `→` TUI renders.

**ContextPct source**: model context window comes from config (`models.<ref>.context_window`). If not set, omit the bar.

**TokPerSec source**: `OutTokens / Elapsed.Seconds()`, recalculated on each `TextDelta` event.

---

## Architecture

### Layer Stack

```
TUI (Bubble Tea)         — full-screen dashboard, event-driven rendering
  ↕ tea.Msg
Orchestrator             — hardcoded Go control plane, token budgets, artifact routing
  ↕ Query() → <-chan StreamEvent
Harness                  — query() API, launches claude --print, parses stream-json
  ↕ sandbox-exec wrapper
Sandbox                  — kernel-level process capability restriction
```

### Harness API

```go
// Query launches claude --print and returns a channel of typed stream events.
func Query(ctx context.Context, cfg QueryConfig) (<-chan StreamEvent, error)

type QueryConfig struct {
    Prompt          string
    SystemPrompt    string
    SessionID       string   // for --continue
    MaxTurns        int
    AllowedTools    []string
    DisallowedTools []string
    WorkDir         string
    Env             []string
}

type StreamEvent interface{ streamEvent() }

type TextDelta    struct { Text string }
type ToolUse      struct { Name string; Args json.RawMessage }
type ToolResult   struct { Name string; Output string; TokensAdded int64 }
type Result       struct { SessionID string; Output string; Usage TokenUsage }
type UsageDelta   struct { InputTokens, OutputTokens int64 }  // per-event running total
type ErrorEvent   struct { Err error }                        // fatal harness errors
```

`UsageDelta` is emitted from stream-json `usage` fields on each turn boundary. `ToolResult.TokensAdded` comes from the token delta between consecutive usage reports. The orchestrator uses these to maintain `AgentStats` without needing to query the model separately.

### Config Schema

YAML controls **parameters** — which model, which prompt, what limits. The pipeline topology (intent → planner → validator → pm → workers → qa) is Go code, not config.

```yaml
providers: { ... }  # unchanged
models: { ... }     # unchanged

# Per-agent parameters (model, prompt, limits)
planner:
  model_ref: large
  system_prompt: "..."

validator:
  model_ref: small
  system_prompt: "..."

qa:
  model_ref: small
  system_prompt: "..."

worker:
  model_ref: large
  max_turns: 10

project_manager:
  model_ref: large
  system_prompt: "..."

intent:
  model_ref: small
  system_prompt: "..."

# Global pipeline settings
pipeline:
  token_budget: 500000
  run_dir: .orqestra/runs
  worker_concurrency: 2
```

The orchestrator wires the pipeline in Go:

```go
// This IS the pipeline — typed, compile-checked, explicit.
spec, _ := planner.Plan(ctx, prompt)
report, _ := validator.Validate(ctx, spec)
plan, _ := pm.Decompose(ctx, spec)

for _, wave := range types.TopoWaves(plan.Packages) {
    g, waveCtx := errgroup.WithContext(ctx)
    // parallel workers per wave
    for _, pkg := range wave {
        pkg := pkg // capture loop variable
        g.Go(func() error {
            _, err := worker.Execute(waveCtx, pkg.ToSpecification(spec))
            return err
        })
    }
    if err := g.Wait(); err != nil {
        // Handle error event, halt pipeline, trigger Screen 6
        return fmt.Errorf("wave execution failed: %w", err)
    }
}
qaReport, _ := qa.Validate(ctx, qaInput)
```

---

## Implementation Phases

### Phase 1: Harness `query()` API and Event Types

**Files**: `internal/harness/query.go`, `internal/harness/ringbuf.go`, `internal/harness/stats.go`

1. **Protocol Definition**: Define `StreamEvent` interfaces: `TextDelta`, `ToolUse`, `ToolResult`, `Result`, `UsageDelta`, and `ErrorEvent`. `ErrorEvent` is critical for gracefully capturing sandbox crashes or context window exhaustion.
2. **Execution Wrapper**: Implement `Query(ctx, QueryConfig) (<-chan StreamEvent, error)` which encapsulates building CLI arguments for `claude --print --output-format stream-json` and launching it completely supervised beneath `sandbox-exec`.
3. **Internal Parsing**: Extend the JSON-line parser to continuously decode `tool_use`, `tool_result`, `session_id`, and `usage` events on the fly, emitting them on the returned bounded channel.
4. **Ring Buffer**: Implement a thread-safe circular buffer (`ringbuf.go`) capable of retaining the last ~1000 stream events per-invocation, ensuring memory safety during highly verbose sessions.
5. **Uniform Struct Mapping**: Consolidate response unmarshaling in a shared `harness.ParseLLMOutput()` helper to eliminate the duplicated envelope/fence stripping.

**Acceptance Criteria**: `go test -run TestQueryStream ./internal/harness` strictly passes. The test MUST parse a hardcoded `.jsonl` stream fixture and successfully verify that every `StreamEvent` variant (including the new `ErrorEvent`) propagates over the channel.

### Phase 2: Configuration Streamlining

**Files**: `internal/config/config.go`

1. **Explicit Pipeline Globals**: Add a strictly typed `pipeline` section representing global orchestrator state parameters: `token_budget`, `run_dir` for artifacts, and `worker_concurrency`.
2. **Prune Dynamic Topologies**: Permanently remove `ExecutionGraphConfig`, `AgentNodeConfig`, and any dynamic DAG configuration. The agent architecture is now entirely hardcoded in Go code. YAML controls variable parameters (limits, models, prompt directives), not the shape of the pipeline.
3. **Agent Node Validation**: Maintain and strictly validate existing node parameters like `model_ref`, `system_prompt`, `max_turns`, and node-level token limitations.

**Acceptance Criteria**: `go test ./internal/config` passes. It must successfully `json.Unmarshal` the new `pipeline` block and assert that an invalid model definition panics during initialization.

### Phase 3: The Orchestrator Engine

**Files**: `internal/orchestrator/orchestrator.go` (new module)

1. **Hardcoded Directed Graph**: Define the pipeline orchestrator loop expressly in Go code: `intent → planner → validator → pm → workers → qa`. Do not use dynamic graph execution routines.
2. **Event Delegation**: Develop `orchestrator.Run(ctx context.Context, cfg Config, onEmit func(harness.AgentStats)) error`. The orchestrator tracks state by parsing `harness.Result.Usage` per agent and propagates normalized `AgentStats` updates to the TUI via the `onEmit` callback.
3. **Synchronized Parallel Waves**: In the PM decomposition stage (`workers` wave), the orchestrator MUST map parallel worker execution strictly via `golang.org/x/sync/errgroup.WithContext(ctx)`.
4. **Error Bubble-Up**: If `g.Wait()` resolves with an error (e.g., failure emitted from `ErrorEvent`), halt the entire orchestration, trigger Screen 6 ("Agent Failure"), and block on user input to retry or abort.
5. **Artifact Flow**: Hardcode artifact transition (e.g. `planner.Plan() -> types.Specification`). Persist final artifacts (`types.ProjectPlan`, `ValidationReport`) asynchronously to a timestamped file within the `RunDir`.

**Acceptance Criteria**: `go test ./internal/orchestrator` passes, verifying a multi-node mock spec. The test must mock the `pm` decomposition outputting `2 waves` of workers and assert `errgroup.Wait()` functions identically block before iterating onto the final QA node.

### Phase 4: Bubble Tea Dashboard (TUI)

**Files**: `internal/tui/app.go` (new), `internal/tui/screens.go` (new), `internal/tui/styles.go` (new)

1. **App Architecture**: Develop the full terminal application using `github.com/charmbracelet/bubbletea`. Implement `Init() tea.Cmd`, `Update(msg tea.Msg) (tea.Model, tea.Cmd)`, and `View() string` adhering to the rigid 3-zone standard (Header/Scrollable Content/Keys).
2. **Channel Subscriptions**: Leverage `tea.Batch` or `tea.Sequence` internally. Intercept orchestrator updates (`AgentStats` messages) via `tea.Cmd` long-polling, injecting them straight into `Update()`. No native internal stream event parsing inside the TUI component.
3. **View Routing**: Build distinct view states matching Screens 1 through 8 (Prompt, Clarification, Main Dashboard, Node Drill-down, Plan review, Failure Screen, QA summary, Completion Screen).
4. **Line Input**: Use `github.com/charmbracelet/bubbles/textarea` for Screen 1's query builder (supports `Shift+Enter` and `\` wrapping).
5. **Graceful Exit Patterns**: Configure standard interception of `Ctrl+C` globally to cancel the current node Context. Add confirmation state ("Press Ctrl+C again to abort completely") inspired by standard modern terminal UX.

**Acceptance Criteria**: Execute a distinct `go run cmd/tui_test/main.go` that spins up the TUI purely with mock configurations. Verify switching between states 1-8 utilizing keys `Enter`, `Y`, `N` functionally triggers view updates without crashing or layout breaks.

### Phase 5: CLI Entrypoint & Bridging

**Files**: `cmd/orqestra/main.go`

1. **Load Configuration**: Instantiate `viper` and aggressively preload all internal YAML configurations before mounting TUI logic. Fail fast natively if `~/.config/orqestra.yaml` mapping is structurally degraded.
2. **Process Management**: Create a root `context.WithCancel(context.Background())`. Provide this baseline ctx downwards to properly propagate interrupts back through all active `sandbox-exec` instances.
3. **Application Startup Routine**:
   - Instantiate the internal root components (`config`, `sqlite store`).
   - Create `tea.NewProgram(model, tea.WithAltScreen())`.
   - Before executing `p.Run()`, invoke `orchestrator.Run(...)` on an isolated goroutine. The loop utilizes `p.Send()` to trigger visual refreshes sequentially.
   - Main thread safely blocks directly on `p.Run()`.

**Acceptance Criteria**: Invoking `go run cmd/orqestra/main.go` natively binds the actual components, triggers an entrypoint screen rendering in the terminal, and successfully completes a dry intake of a raw string query.

---

## Verification

1. `go build ./cmd/orqestra` — Validates all modules compile functionally and type-checked against new structures.
2. `go test ./internal/harness/...` — Verifies Event definitions and streaming circular allocations.
3. `go test ./internal/orchestrator/...` — Asserts that synchronous error pipelines do not execute `worker` nodes blindly.
4. **Manual Run**: End-to-end task (e.g. `orqestra "add a print string"`). Ensures TUI renders properly, worker detail tracks tokens precisely, and finally leaves a `runs/YYYY-MM-DD-timestamp` folder populated cleanly with JSON artifacts.

---

## Open Questions

- **Token Budget Policy**: If a node hits a dynamic context window warning or exceeds its isolated token allowance inside Phase 3, do we forcefully issue an `abort`, perform a memory `summarize`, or just `warn` via Bubble Tea logs?
- **Headless Mode (`--json` / `--log`)**: Is it worth designing a fallback mode inside Phase 5 that utilizes a standard `slog` output format if `isTerminal` detects a non-TTY interface (e.g., CI execution)?
- Stream-json schema stability across Claude Code versions — parse known, pass-through unknown
