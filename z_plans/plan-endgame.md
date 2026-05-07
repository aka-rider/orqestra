# Plan: Orqestra v3 — Programmatic Harness Architecture

**Date**: 2026-05-07
**Status**: Validated and implementation-ready after plan repair.

**TL;DR**: Rewrite Orqestra from "tmux for Claude sessions" to "mission control for programmatic Claude queries." Harness becomes a programmatic `Query()` API wrapping `claude --print --output-format stream-json`. Orchestrator becomes a hardcoded Go control plane with typed agent packages wired in code and invocation-level control (`--max-turns`, `--allowedTools`, `--disallowedTools`, `--continue`). YAML config controls parameters such as models, prompts, limits, and run directories, not topology. TUI becomes a full Bubble Tea dashboard with connected state transitions and circular buffer-backed stream rendering.

---

## SchemaVersion

1

## Goal

Implement Orqestra v3 as a programmatic Claude harness pipeline with typed stream events, current `internal/agent` domain ownership, a hardcoded Go orchestrator, and a connected Bubble Tea dashboard in `cmd/orqestra/main.go`.

## Context

The current repository stores planner, project manager, validator, QA, intent, `Specification`, `PlanOutput`, `ProjectPlan`, and `ValidationReport` in `internal/agent`. The current harness package owns `CLIRunner`, `ClaudeCLI`, `RunPrint`, `RunStreaming`, `BuildModelEnv`, and the sandbox CLI runner. The current TUI entrypoint in `cmd/orqestra/main.go` is a stub. The current config still includes dynamic `ExecutionGraphConfig`. The worker execution prompt contains only `Specification.Goal`, `Specification.Steps`, and `Specification.Acceptance`, while `PlanOutput.ValidationCommands` and `PlanOutput.ExpectedArtifacts` are QA metadata.

---

## Core Insight

`--print` + `stream-json` + `--max-turns` + `--allowedTools` gives the orchestrator direct control. Claude does not decide when to stop, what tools it can use, or whether the user must intervene. The orchestrator decides those things by parsing stream-json output and controlling continuation through `--continue <session-id>`.

The worker execution prompt remains intentionally narrow: goal, ordered steps, and acceptance. QA metadata lives beside the spec in `agent.PlanOutput`, not inside `agent.Specification`.

---

## Current Codebase

```text
internal/
  agent/              intent.go, planner.go, plan_validator.go, pm.go, project.go,
                      qa.go, session.go, spec.go, validation.go, work_audit.go
  config/             config.go, graph.go, pipeline.yaml
  harness/            claude_cli.go, sandbox_cli_runner.go
  plan/               artifact.go, spec.go
  sandbox/            builder.go, env.go, path.go, profile.go, sandbox.go
    detect/           detect.go, user_profile.go
  scheduler/          event.go, graph.go, scheduler.go
  tokenlimit/         limiter.go, runner.go, store.go
cmd/orqestra/         main.go
```

### Current Package Ownership

- `internal/agent` owns all agent domain types and implementations:
  `Specification`, `PlanOutput`, `ProjectPlan`, `WorkPackage`, `ValidationReport`, `Planner`, `ProjectManager`, `PlanValidator`, `Gate`, and `Recognizer`.
- `internal/harness` owns CLI execution, sandbox CLI runners, environment routing, stream parsing, stream buffers, and live stream stats.
- `internal/plan` owns markdown persistence only. It converts to and from `agent.PlanOutput`.
- `internal/config` owns config loading and validation.
- `internal/sandbox` owns macOS sandbox profile construction and process wrapping.
- `internal/scheduler` may remain compiling, but v3 orchestration must not depend on config-driven dynamic DAG topology.

### Packages Not To Recreate

Do not create or import these old split packages:

- `internal/types`
- `internal/planner`
- `internal/pm`
- `internal/qa`
- `internal/validator`
- `internal/intent`

---

## Target Interfaces

### Harness Query API

Add `internal/harness/query.go` with:

```go
type QueryRunner interface {
    Query(ctx context.Context, cfg QueryConfig) (<-chan StreamEvent, error)
}

type QueryConfig struct {
    Prompt          string
    SystemPrompt    string
    SessionID       string
    MaxTurns        int
    AllowedTools    []string
    DisallowedTools []string
    WorkDir         string
    Env             []string
    Binary          string
}

type StreamEvent interface{ streamEvent() }

type TextDelta struct { Text string }
type ToolUse struct { Name string; Args json.RawMessage }
type ToolResult struct { Name string; Output string; TokensAdded int64 }
type UsageDelta struct { InputTokens int64; OutputTokens int64 }
type Result struct { SessionID string; Output string; Usage TokenUsage }
type ErrorEvent struct { Err error }
```

Keep the existing `CLIRunner` interface unchanged:

```go
type CLIRunner interface {
    RunPrint(ctx context.Context, prompt, systemPrompt string) (RunResult, error)
    RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (RunResult, error)
}
```

The macOS sandbox CLI runner belongs in `internal/harness`:

```go
func NewSandboxCLIRunner(cfg SandboxCLIRunnerConfig) *SandboxCLIRunner
```

### Planner Output API

Change planner APIs in `internal/agent/planner.go` to return `PlanOutput`, not bare `Specification`:

```go
func (p *Planner) Plan(ctx context.Context, prompt string) (PlanOutput, error)
func (p *Planner) PlanStreaming(ctx context.Context, prompt string, stdout io.Writer) (PlanOutput, error)
func (p *Planner) ParsePlanOutput(raw string) (PlanOutput, error)
```

Keep worker prompt construction narrow:

```go
func BuildExecutionPrompt(spec Specification) string
```

`BuildExecutionPrompt` must render only:

1. `Goal`
2. ordered `Steps`
3. `Acceptance Criteria`

It must not render `ValidationCommands`, `ExpectedArtifacts`, assumptions, risks, or QA-only metadata.

### QA Input API

Keep QA metadata out of `Specification` and pass it explicitly:

```go
type QAInput struct {
    Spec               Specification
    WorkOutput         string
    ValidationCommands []ValidationCommand
    ExpectedArtifacts  []string
}

func (v *Gate) ValidateWork(ctx context.Context, input *QAInput) (*ValidationReport, error)
```

`Gate.ValidateWork` must run `input.ValidationCommands`, not `input.Spec.ValidationCommands`.

### Orchestrator API

Add `internal/orchestrator/orchestrator.go` with:

```go
type Engine struct {
    Config        *config.Config
    Runners       Runners
    RunDirFactory RunDirFactory
}

type Runners struct {
    Intent         harness.CLIRunner
    Planner        harness.CLIRunner
    Validator      harness.CLIRunner
    ProjectManager harness.CLIRunner
    Worker         harness.CLIRunner
    QA             harness.CLIRunner
}

type Event struct {
    Type             EventType
    Screen           tui.Screen
    AgentStats       *harness.AgentStats
    PlanOutput       *agent.PlanOutput
    ValidationReport *agent.ValidationReport
    QAReport         *agent.ValidationReport
    Err              error
}

func (e *Engine) Run(ctx context.Context, input Input, emit func(Event)) (Result, error)
```

The engine must run this order:

```go
intentResult := recognizer.Recognize(ctx, rawPrompt)
planOutput := planner.Plan(ctx, intentResult.Rephrased)
spec := planOutput.Spec
planReport := planValidator.ValidatePlan(ctx, spec)
projectPlan := projectManager.Decompose(ctx, spec)
workerOutput := executeWorkerWaves(ctx, spec, projectPlan)
qaReport := gate.ValidateWork(ctx, &agent.QAInput{
    Spec: spec,
    WorkOutput: workerOutput,
    ValidationCommands: planOutput.ValidationCommands,
    ExpectedArtifacts: planOutput.ExpectedArtifacts,
})
```

---

## Architecture

### Layer Stack

```text
TUI (Bubble Tea)         — full-screen dashboard, review, failures, QA decisions
  ↕ tea.Msg
Orchestrator             — hardcoded Go control plane, run state, artifacts
  ↕ Query() / CLIRunner
Harness                  — claude CLI launch, stream-json parser, ring buffer, stats
  ↕ sandbox-exec wrapper
Sandbox                  — kernel-level process capability restriction
```

### Data Flow

```text
User prompt
  ↓
TUI ScreenPrompt
  ↓ SubmitPromptMsg
Orchestrator intent stage
  ↓ accept / clarify / reject
Planner returns agent.PlanOutput
  ↓
Plan validator returns agent.ValidationReport
  ↓
TUI ScreenPlanReview
  ↓ ApprovePlanMsg
Project manager returns agent.ProjectPlan
  ↓
Worker packages execute in dependency waves
  ↓
QA gate validates work output with PlanOutput metadata
  ↓
TUI ScreenQAResult
  ↓ AcceptQAWithWarningsMsg or RepairWorkMsg
TUI ScreenCompletion
```

### Artifact Flow

The orchestrator writes these artifacts under `Config.Pipeline.RunDir`:

- `specification.json`
- `plan_output.json`
- `project_plan.json`
- `validation_report.json`
- `qa_report.json`
- `summary.json`

Every write error must include the artifact path.

---

## TUI Design

### Layout Contract

Every screen uses the same 3-zone structure:

```text
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

Header, input, and keys stay in the same terminal rows. Content is the only scrollable area. The key legend must describe the current screen only.

### TUI State API

Add `internal/tui/app.go` with:

```go
type Screen int

const (
    ScreenPrompt Screen = iota
    ScreenClarification
    ScreenDashboard
    ScreenAgentDetail
    ScreenPlanReview
    ScreenFailure
    ScreenQAResult
    ScreenCompletion
)

type Model struct {
    screen        Screen
    previous      Screen
    runState      RunState
    agents        map[string]harness.AgentStats
    prompt        textarea.Model
    clarification ClarificationState
    planReview    PlanReviewState
    failure       FailureState
    qa            QAState
}
```

Add TUI command messages:

```go
type SubmitPromptMsg struct { Prompt string }
type SubmitClarificationMsg struct { Answers []ClarificationAnswer }
type ApprovePlanMsg struct{}
type RejectPlanMsg struct { Feedback string }
type EditPlanMsg struct { Path string }
type RetryAgentMsg struct { AgentID string }
type SkipAgentMsg struct { AgentID string }
type AbortRunMsg struct{}
type RepairWorkMsg struct{}
type AcceptQAWithWarningsMsg struct{}
```

### Screen 1: Prompt Entry

```text
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

Implementation requirements:

- Use `github.com/charmbracelet/bubbles/textarea`.
- `Enter` sends `SubmitPromptMsg` when the input is non-empty.
- `Ctrl+C` and `Ctrl+D` require a second press before exit.
- Rejected plans return here with the prior goal and validator feedback prefilled.

### Screen 2: Clarification

```text
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

Implementation requirements:

- Render questions from `agent.Intent.Questions`.
- Support single-select, multi-select, and freeform answers through `ClarificationState`.
- `Enter` sends `SubmitClarificationMsg`.
- `Esc` returns to `ScreenPrompt` without starting worker execution.

### Screen 3: Pipeline Dashboard

```text
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

Implementation requirements:

- Render one row per `harness.AgentStats` entry.
- Context bar uses `ModelConfig.ContextWindow` when present.
- `Enter` opens `ScreenAgentDetail` for the selected row.
- `S` cancels the selected running agent and transitions to `ScreenFailure`.

### Screen 4: Agent Detail

```text
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

Implementation requirements:

- Render the selected agent's circular stream buffer.
- Render tool calls from `ToolCallSummary`.
- `F` toggles follow mode.
- `Esc` returns to `ScreenDashboard`.
- `S` cancels the selected running agent and transitions to `ScreenFailure`.

### Screen 5: Plan Review

```text
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
   • Unit test coverage >= 90% for ratelimit package

 Constraints:
   • No external dependencies (no Redis)
   • Must not break existing middleware chain
─────────────────────────────────────────────────────────────────
 Approve this plan?
 [Y] approve | [N] reject (re-plan) | [E] edit | [↑↓] scroll
```

Implementation requirements:

- Render `agent.PlanOutput.Spec`.
- `Y` sends `ApprovePlanMsg` and continues to project manager and workers.
- `N` sends `RejectPlanMsg`, returns to `ScreenPrompt`, and pre-fills feedback.
- `E` opens `$EDITOR`, reloads the edited plan through `internal/plan`, and remains on `ScreenPlanReview`.
- Worker execution must not start after rejection.

### Screen 6: Agent Failure

```text
 Orqestra v3                  ✗ worker-1 failed              4m32s
─────────────────────────────────────────────────────────────────
 Goal: Add rate limiting to the API gateway

 Agent          State     Time   In Tok   Out Tok  Tok/s  Context
 worker-1       ✗ FAIL    1m12s  22,481   8,102    -      ████████ 89%
 worker-2       ✓ done    58s    18,200   6,440    -      ██████░░ 72%

 Error: context window exhausted (89% -> budget exceeded)
─────────────────────────────────────────────────────────────────
 Worker failed. What next?
 [R] retry worker-1 | [S] skip (continue with worker-2 only) | [A] abort
```

Implementation requirements:

- `R` sends `RetryAgentMsg` for the failed agent or work package.
- `S` sends `SkipAgentMsg` only when orchestrator marks the package skippable.
- `A` sends `AbortRunMsg` and transitions to failed `ScreenCompletion`.
- The failure screen must display the concrete error returned by the failed agent.

### Screen 7: QA Gate Result

```text
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

Implementation requirements:

- Render `agent.ValidationReport` from QA.
- `F` sends `RepairWorkMsg` and re-runs worker with QA feedback.
- `A` sends `AcceptQAWithWarningsMsg` and transitions to completion.
- `R` toggles the full report view without leaving `ScreenQAResult`.

### Screen 8: Completion Summary

```text
 Orqestra v3                  ✓ complete                     7m42s
─────────────────────────────────────────────────────────────────
 Goal: Add rate limiting to the API gateway

 Pipeline:  ✓ intake -> ✓ planner -> ✓ validator -> ✓ workers -> ✓ QA

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

Implementation requirements:

- Render success, failed, aborted, and accepted-with-warnings statuses.
- `Enter` resets the model and returns to `ScreenPrompt`.
- `D` toggles diff detail.
- `Q` exits.

### TUI State Transitions

| Current screen | User action | Message | Next screen | Required effect |
| --- | --- | --- | --- | --- |
| Prompt | Enter | `SubmitPromptMsg` | Dashboard | Start intent and planning run. |
| Prompt | Ctrl+C twice or Ctrl+D twice | `AbortRunMsg` | exit | Exit program. |
| Clarification | Enter | `SubmitClarificationMsg` | Dashboard | Re-run planner with clarified prompt. |
| Clarification | Esc | none | Prompt | Return to editable prompt. |
| Dashboard | Enter on agent row | none | AgentDetail | Show selected agent event stream and stats. |
| Dashboard | S | `AbortRunMsg` or stop selected agent | Failure | Cancel current agent context and show failure action screen. |
| AgentDetail | Esc | none | Dashboard | Return to dashboard. |
| AgentDetail | F | none | AgentDetail | Toggle stream follow mode. |
| AgentDetail | S | `AbortRunMsg` or stop selected agent | Failure | Cancel current agent context and show failure action screen. |
| PlanReview | Y | `ApprovePlanMsg` | Dashboard | Continue to project manager and workers. |
| PlanReview | N | `RejectPlanMsg` | Prompt | Pre-fill prompt with planner feedback and previous goal. |
| PlanReview | E | `EditPlanMsg` | PlanReview | Open `$EDITOR`, reload edited plan, and re-render review. |
| Failure | R | `RetryAgentMsg` | Dashboard | Retry failed agent or failed work package only. |
| Failure | S | `SkipAgentMsg` | Dashboard | Continue only when failed work package is marked skippable by orchestrator. |
| Failure | A | `AbortRunMsg` | Completion | Cancel run and display failed summary. |
| QAResult | F | `RepairWorkMsg` | Dashboard | Re-run worker with QA feedback. |
| QAResult | A | `AcceptQAWithWarningsMsg` | Completion | Complete run with accepted-with-warnings status. |
| QAResult | R | none | QAResult | Toggle full QA report view. |
| Completion | Enter | none | Prompt | Reset model for a new task. |
| Completion | D | none | Completion | Toggle diff detail view. |
| Completion | Q | none | exit | Exit program. |

### Interaction Summary

| Action | Key | Available on |
| --- | --- | --- |
| Submit prompt / confirm | `Enter` | prompt, clarification, plan review |
| Navigate list / scroll | `↑↓` | all scrollable screens |
| Toggle option | `Space` | clarification |
| Next question | `Tab` | clarification |
| Expand agent | `Enter` | dashboard |
| Back / cancel | `Esc` | agent detail, plan edit |
| Stop running agent | `S` | dashboard, agent detail |
| Follow auto-scroll | `F` | agent detail |
| Approve plan | `Y` | plan review |
| Reject plan | `N` | plan review |
| Edit plan | `E` | plan review |
| Retry failed agent | `R` | failure screen |
| Skip failed agent | `S` | failure screen |
| Abort pipeline | `A` | failure screen |
| Exit graceful | `Ctrl+C Ctrl+C` or `Ctrl+D Ctrl+D` | everywhere |
| New task | `Enter` | completion |
| Quit | `Q` | dashboard, completion |

---

## Implementation Phases

### Phase 1: Harness Output Parsing And Stream Events

**Files**: `internal/harness/output.go`, `internal/harness/query.go`, `internal/harness/ringbuf.go`, `internal/harness/stats.go`

1. Add shared response parsing with `ParseLLMOutput(raw string, target any) error`.
2. Add the `QueryRunner` API and typed stream events.
3. Decode stream-json lines into `TextDelta`, `ToolUse`, `ToolResult`, `UsageDelta`, `Result`, and `ErrorEvent`.
4. Add the circular event buffer.
5. Add `AgentStats` updates from stream events.

**Acceptance Criteria**:

- `go test ./internal/harness -run TestParseLLMOutput` passes.
- `go test ./internal/harness -run TestQueryStream` passes.
- `go test ./internal/harness -run TestRingBuffer` passes.
- `go test ./internal/harness -run TestAgentStats` passes.

### Phase 2: Agent Contracts And Plan Metadata Flow

**Files**: `internal/agent/planner.go`, `internal/agent/qa.go`, `internal/agent/spec.go`, `internal/plan/spec.go`

1. Change planner methods to return `PlanOutput`.
2. Keep `Specification` limited to worker-visible execution fields.
3. Pass QA metadata through `QAInput`.
4. Keep markdown plan persistence as an adapter around `PlanOutput`.

**Acceptance Criteria**:

- `go test ./internal/agent -run TestPlan` passes.
- `go test ./internal/agent -run TestGate` passes.
- `go test ./internal/plan` passes.

### Phase 3: Configuration Streamlining

**Files**: `internal/config/config.go`, `internal/config/pipeline.yaml`, `internal/config/config_test.go`

1. Add `PipelineConfig{TokenBudget int64, RunDir string, WorkerConcurrency int}`.
2. Add `WorkerConfig.MaxTurns int`.
3. Add `WorkerConfig.DisallowedTools []string`.
4. Add `ModelConfig.ContextWindow int64`.
5. Remove dynamic execution graph config from `Config`.
6. Keep `internal/scheduler` compiling but unused by v3 orchestrator.

**Acceptance Criteria**:

- `go test ./internal/config` passes.
- No source file references `Config.ExecutionGraph`.
- No source file references `ExecutionGraphConfig`, `AgentNodeConfig`, or `ValidatorNodeConfig`.

### Phase 4: Orchestrator Engine

**Files**: `internal/orchestrator/orchestrator.go`, `internal/orchestrator/orchestrator_test.go`, `internal/agent/run_dir.go`

1. Add the hardcoded intent → planner → validator → PM → worker waves → QA pipeline.
2. Execute worker dependency waves with `errgroup.WithContext(ctx)`.
3. Copy each package loop variable before `g.Go`.
4. Stop later waves and QA after the first worker error.
5. Emit events for every state change consumed by the TUI.
6. Persist run artifacts under `Config.Pipeline.RunDir`.

**Acceptance Criteria**:

- `go test ./internal/orchestrator` verifies stage order.
- `go test ./internal/orchestrator` verifies worker error behavior.
- `go test ./internal/orchestrator` verifies reject, approve, retry, skip, abort, QA repair, and accept-with-warnings paths.
- `go test ./internal/orchestrator` verifies run artifact files exist.

### Phase 5: Bubble Tea Dashboard

**Files**: `internal/tui/app.go`, `internal/tui/screens.go`, `internal/tui/styles.go`, `internal/tui/app_test.go`, `cmd/tui_test/main.go`

1. Add `Screen`, `Model`, screen state structs, and TUI command messages.
2. Implement every state transition from the TUI transition table.
3. Render all eight screens with the restored mockup layouts.
4. Add mock event demo command in `cmd/tui_test/main.go`.

**Acceptance Criteria**:

- `go test ./internal/tui` verifies every transition table row.
- `go test ./internal/tui` verifies all screens render header, content, input/status, and key legend.
- `go run ./cmd/tui_test` runs without invoking Claude or reading provider credentials.

### Phase 6: CLI Entrypoint And Headless Compatibility

**Files**: `cmd/orqestra/main.go`, `cmd/orqestra` tests

1. Replace the no-argument TUI stub with Bubble Tea startup.
2. Construct the orchestrator engine from existing config and harness runners.
3. Start `Engine.Run` after prompt submission and send events to Bubble Tea with `Program.Send`.
4. Keep headless commands working: `plan`, `validate`, `exec`, `usage`, `reset-usage`, and `--plan`.

**Acceptance Criteria**:

- `go test ./cmd/orqestra` verifies no-argument TTY path no longer prints `interactive TUI is not yet implemented in v3`.
- `go test ./cmd/orqestra` verifies headless command argument routing.
- `go build ./cmd/orqestra` passes.

---

## Steps

1. Add `internal/harness/output.go` and `internal/harness/output_test.go` with `ParseLLMOutput(raw string, target any) error`.
2. Update `internal/agent/planner.go`, `internal/agent/pm.go`, `internal/agent/qa.go`, and `internal/agent/intent.go` to use `harness.ParseLLMOutput`.
3. Change `internal/agent/planner.go` so `Plan`, `PlanStreaming`, and `ParsePlanOutput` return `agent.PlanOutput`.
4. Update `internal/plan/spec.go`, `cmd/orqestra/main.go`, and planner tests to use `plan.ToPlanOutput` and `plan.FromPlanOutput`.
5. Update `internal/agent/qa.go` so `QAInput` contains `ValidationCommands []ValidationCommand` and `ExpectedArtifacts []string`.
6. Add `internal/harness/query.go` and `internal/harness/query_test.go` with the target stream-query API.
7. Implement `Query(ctx, QueryConfig)` in `internal/harness/query.go`.
8. Implement the stream-json scanner in `internal/harness/query.go` with a 1 MiB scanner buffer.
9. Send `ErrorEvent` before closing the stream channel when process start, process wait, scanner, or JSON parsing returns an error.
10. Add `internal/harness/ringbuf.go`, `internal/harness/ringbuf_test.go`, `internal/harness/stats.go`, and `internal/harness/stats_test.go`.
11. Update `internal/config/config.go`, `internal/config/pipeline.yaml`, and `internal/config/config_test.go` with pipeline globals, worker turn/tool limits, and model context windows.
12. Remove dynamic execution graph config from `internal/config/config.go`.
13. Add `internal/agent/run_dir.go` and `internal/agent/run_dir_test.go`.
14. Add `internal/orchestrator/orchestrator.go` and `internal/orchestrator/orchestrator_test.go`.
15. Add `internal/tui/app.go`, `internal/tui/screens.go`, `internal/tui/styles.go`, and `internal/tui/app_test.go`.
16. Add `cmd/tui_test/main.go`.
17. Update `cmd/orqestra/main.go` to start the TUI and preserve headless command paths.
18. Update all imports and tests so no Go file imports old split packages.

---

## Acceptance

1. `go test ./internal/harness -run TestParseLLMOutput` verifies direct JSON, fenced JSON, and Claude result envelopes parse into `agent.PlanOutput` and `agent.ValidationReport`.
2. `go test ./internal/agent -run TestPlan` verifies `Planner.Plan`, `Planner.PlanStreaming`, and `Planner.ParsePlanOutput` return `PlanOutput` with `Spec`, `ValidationCommands`, and `ExpectedArtifacts` populated.
3. `go test ./internal/agent -run TestGate` verifies `Gate.ValidateWork` runs `QAInput.ValidationCommands` and does not require validation commands on `Specification`.
4. `go test ./internal/harness -run TestQueryStream` verifies a fixture emits every stream event type without invoking the real Claude binary.
5. `go test ./internal/harness -run TestRingBuffer` verifies the ring buffer retains exactly the newest 1000 stream events after more than 1000 appends.
6. `go test ./internal/harness -run TestAgentStats` verifies live agent stats from stream events.
7. `go test ./internal/config` verifies new pipeline, worker, and model config fields.
8. `go test ./internal/config` verifies invalid `model_ref` values return errors from `config.Load`.
9. `go test ./internal/orchestrator` verifies stage order, worker waves, error handling, retry, skip, abort, QA repair, and artifact writes.
10. `go test ./internal/tui` verifies every state transition and every restored screen layout.
11. `go run ./cmd/tui_test` displays mock data and exits without invoking Claude, reading provider API keys, or modifying repository files.
12. `go test ./cmd/orqestra` verifies the no-argument TTY path constructs the TUI instead of printing the current stub error.
13. `go test ./cmd/orqestra` verifies all headless commands still parse and route.
14. `go test ./...` exits 0.
15. `go build ./cmd/orqestra` exits 0.
16. `grep -R "internal/types" --include='*.go' .` exits 1.
17. `grep -R "internal/planner" --include='*.go' .` exits 1.
18. `grep -R "internal/pm" --include='*.go' .` exits 1.
19. `grep -R "internal/qa" --include='*.go' .` exits 1.
20. `grep -R "internal/validator" --include='*.go' .` exits 1.
21. `grep -R "internal/intent" --include='*.go' .` exits 1.
22. `test ! -d internal/types` exits 0.
23. `test ! -d internal/planner` exits 0.
24. `test ! -d internal/pm` exits 0.
25. `test ! -d internal/qa` exits 0.
26. `test ! -d internal/validator` exits 0.
27. `test ! -d internal/intent` exits 0.

---

## Constraints

1. Do not create split packages named `internal/types`, `internal/planner`, `internal/pm`, `internal/qa`, `internal/validator`, or `internal/intent`.
2. Do not put validation commands, expected artifacts, or QA-only metadata back onto `agent.Specification`.
3. Do not change `BuildExecutionPrompt` to include fields other than goal, steps, and acceptance.
4. Do not route new stream-query APIs through `internal/agent`; they belong in `internal/harness`.
5. Do not reintroduce `creack/pty`, old raw terminal passthrough mux code, OpenAI raw HTTP harness clients, or stdin `[y/N]` gates in the interactive TUI path.
6. Do not add `viper`; keep using the existing `internal/config` loader unless a separate approved plan changes config loading.
7. Do not parse JSON, YAML, or stream-json with ad hoc string slicing when `encoding/json`, `yaml.v3`, or typed structs can parse the data.
8. Do not use `time.Sleep` in tests; use channels, contexts, fake clocks, or deterministic test hooks.
9. Do not swallow process, scanner, artifact, config, model resolution, or TUI startup errors.
10. Do not start worker execution after plan rejection; rejection returns to `ScreenPrompt` with feedback.

---

## ValidationCommands

```sh
go test ./internal/harness -run TestParseLLMOutput
go test ./internal/agent -run TestPlan
go test ./internal/agent -run TestGate
go test ./internal/harness -run TestQueryStream
go test ./internal/harness -run TestRingBuffer
go test ./internal/harness -run TestAgentStats
go test ./internal/config
go test ./internal/orchestrator
go test ./internal/tui
go run ./cmd/tui_test
go test ./cmd/orqestra
go test ./...
go build ./cmd/orqestra
test ! -d internal/types
test ! -d internal/planner
test ! -d internal/pm
test ! -d internal/qa
test ! -d internal/validator
test ! -d internal/intent
```

---

## ExpectedArtifacts

1. `internal/harness/output.go`
2. `internal/harness/output_test.go`
3. `internal/harness/query.go`
4. `internal/harness/query_test.go`
5. `internal/harness/ringbuf.go`
6. `internal/harness/ringbuf_test.go`
7. `internal/harness/stats.go`
8. `internal/harness/stats_test.go`
9. `internal/agent/planner.go`
10. `internal/agent/planner_test.go`
11. `internal/agent/qa.go`
12. `internal/agent/qa_test.go`
13. `internal/agent/spec.go`
14. `internal/agent/run_dir.go`
15. `internal/agent/run_dir_test.go`
16. `internal/plan/spec.go`
17. `internal/config/config.go`
18. `internal/config/pipeline.yaml`
19. `internal/config/config_test.go`
20. `internal/orchestrator/orchestrator.go`
21. `internal/orchestrator/orchestrator_test.go`
22. `internal/tui/app.go`
23. `internal/tui/screens.go`
24. `internal/tui/styles.go`
25. `internal/tui/app_test.go`
26. `cmd/tui_test/main.go`
27. `cmd/orqestra/main.go`
28. `go.mod`
29. `go.sum`

---

## Open Questions

1. Token budget policy: when a node approaches the context window, should the orchestrator abort, summarize, or warn?
2. Headless structured output: should `--json` emit run events, only final summaries, or both?
3. Stream-json schema stability: unknown event types should be logged and ignored unless they indicate command failure.
