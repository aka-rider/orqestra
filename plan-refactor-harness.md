# Harness Runner Refactor — Unified Runner Interface

## Completion Status

### Completed ✅

| Phase | Description | Status |
|-------|-------------|--------|
| Phase 1 | Type Definitions (Runner, RunnerConfig, Event, etc.) | ✅ Done |
| Phase 2 | ClaudeCLI Refactor | ✅ Done (ClaudeCLI already implements Runner) |
| Phase 3 | Sandbox Unification | ✅ Done (SandboxCLIRunner removed, sandboxedRunner in runner.go) |
| Phase 4 | BudgetGuard | ✅ Done (removed duplicate from runner.go, orchestrator/budget.go is authoritative) |
| Phase 5 | Orchestrator Migration | ✅ Done (engine.go, stream_capture.go already migrated) |
| Phase 6 | Agent Migration | ✅ Done (planner.go already migrated) |
| Phase 7a | TUI Migration (cmd/harness) | ✅ Done |
| Phase 7b | TUI Migration (proto-askuser) | ✅ Done (migrated to Runner interface) |
| Phase 8 | Test Infrastructure | ✅ Done (FakeRunner updated, assertions fixed) |
| Phase 9a | Remove obsolete files | ✅ Done (sandbox_cli_runner.go, interactive_cli.go, query.go, stats.go removed) |
| Phase 9b | Remove obsolete interfaces | ⏳ Pending (CLIRunner, ContinuableRunner, ClaudeCLIOption still in claude_cli.go) |
| Phase 9c | Remove obsolete methods | ⏳ Pending (RunPrint, RunStreaming, RunContinue on ClaudeCLI) |
| Phase 9d | cmd/orqestra/main.go migration | ⏳ Pending (still uses ClaudeCLIOption pattern) |

### Remaining Work

1. **Migrate `cmd/orqestra/main.go`** from `ClaudeCLIOption` pattern to `RunnerConfig`
   - `toolOpts()` and `bridgeToolOpts()` return `[]ClaudeCLIOption` — need to return `RunnerConfig` fields
   - `NewClaudeCLIFromConfig()` calls need `RunnerConfig` + context
   - `runStreamingToWriter()` returns `harness.RunResult` — can return simpler type

2. **Remove deprecated types** from `claude_cli.go` after callers are migrated:
   - `CLIRunner` interface
   - `ContinuableRunner` interface
   - `ClaudeCLIOption` type and functions
   - `RunPrint`, `RunStreaming`, `RunContinue` methods on `ClaudeCLI`
   - `RunResult` struct (if no longer needed by planner.go/engine.go)

3. **Remove `ReadPlanFromRun`** from `agent/plan_extract.go` (replaced by `runner.ExtractPlan(ctx)`)

### Key Files Changed

| File | Change |
|------|--------|
| `internal/harness/sandbox_cli_runner.go` | **REMOVED** |
| `internal/harness/sandbox_cli_runner_test.go` | **REMOVED** |
| `internal/harness/interactive_cli.go` | **REMOVED** |
| `internal/harness/interactive_cli_test.go` | **REMOVED** |
| `internal/harness/query.go` | **REMOVED** |
| `internal/harness/stats.go` | **REMOVED** |
| `internal/harness/stream_event.go` | Removed `StreamUpdate` struct, kept constants |
| `internal/harness/runner.go` | Removed duplicate `BudgetGuard`, kept type definitions |
| `internal/harness/claude_cli.go` | No changes (deprecated types kept for backward compat) |
| `internal/testutil/doubles.go` | Removed `RunContinue`, updated assertions |
| `internal/agent/planner_test.go` | Updated interface assertion |
| `cmd/proto-askuser/main.go` | Migrated to `Runner` interface + `RunnerConfig` |
| `internal/orchestrator/events.go` | Updated comment |

---

## Goal

Replace all overlapping runner interfaces (`CLIRunner`, `ContinuableRunner`, `InteractiveRunner`, `QueryRunner`) and the `SandboxCLIRunner` struct wrapper with a single `Runner` interface that is always sandboxed.

## Design Decisions

### Runner Interface

```go
type Runner interface {
    Post(string)
    Receive() <-chan Event
    ExtractPlan(ctx context.Context) (string, error)
    SetEvents(chan<- Event) // one-time: inject events channel for stream capture
    SessionID() string      // session identifier from Claude CLI
    Cancel() error          // terminate the session immediately
}
```

- `Post(string)` — fire-and-forget. Sends a user message over the NDJSON stdin. No return value.
- `Receive() <-chan Event` — single channel for all output events from the session.
- `ExtractPlan(ctx context.Context) (string, error)` — pulls plan content from the currently running session's metadata. Uses internally stored `sessionID` and `cwd` to locate the Claude CLI JSONL log and extract the plan file path.
- `SetEvents(chan<- Event)` — one-time configuration called before `Post()`. Injects an events channel for stream capture. If nil, events are not forwarded (nil-safe, matching current `emitStreamEvents` behavior at `claude_cli.go:541`).
- `SessionID() string` — returns the Claude session identifier extracted from the stream. Empty until the first session event is parsed.
- `Cancel() error` — terminates the session immediately. For processes not yet started, it is a no-op. For running processes, it sends `SIGKILL` to the process group. This is distinct from context cancellation because it is an explicit user action (e.g., TUI Ctrl+Q).

### RunnerConfig — Single Source of Truth

```go
type RunnerConfig struct {
    Model          ModelSpec
    SystemPrompt   string
    SessionID      string            // empty = new session
    Binary         string
    WorkDir        string
    ExtraArgs      []string
    SmallModel     *ModelSpec
    MCPConfig      map[string][]string  // server -> allowed tools
    AllowedTools   []string
    DisallowedTools []string
    PermissionMode string
    Settings       json.RawMessage
    InlineMCPServers map[string]InlineMCP
    Sandbox        SandboxConfig
    BudgetLimit    int64               // if > 0, BudgetGuard enforces this limit
}
```

### ModelSpec — Harness-Internal Model Specification

```go
type ModelSpec struct {
    Provider   string
    Model      string
    BaseURL    string
    APIKey     string
    SmallModel string
}
```

No dependency on `config.ResolvedModel`. Decouples config format from harness.

### SandboxConfig — Always Applied

```go
type SandboxConfig struct {
    RepoPath     string
    WorktreePath string
    Profiles     []sandbox.Snapshot
    Env          []string
    Writable     bool
}
```

Zero value = default seatbelt profile. `NewRunner` always applies sandboxing.

### Event

```go
type EventKind int

const (
    EventChunk EventKind = iota
    EventToolUse
    EventToolResult
    EventUsage
    EventSessionStart
    EventSessionDone
    EventError
)

type Event struct {
    Kind      EventKind
    Text      string
    Tool      string
    Detail    string
    Input     int64
    Output    int64
    SessionID string
    IsDelta   bool    // true for content_block_delta (partial text)
    IsError   bool
}
```

Replaces `StreamUpdate` + `RunResult`. All output flows through a single channel.

**IsDelta**: Required because the TUI (`cmd/harness/tui.go:351`) distinguishes streaming partial text from completed text via `TextBlock{Text: u.Text, IsDelta: u.IsDelta}`. Delta blocks render with a `▎` cursor prefix (`cmd/harness/tui.go:54-58`). Without `IsDelta`, the TUI cannot render incremental streaming correctly. The stream parser maps `streamDeltaText` events to `Event{Kind: EventChunk, IsDelta: true}`.

**SessionID**: Carried by `EventSessionStart` and `EventSessionDone` events, and also accessible via the `SessionID()` accessor method for callers that need it before `EventSessionDone` arrives.

### Session Lifecycle

- `NewRunner(RunnerConfig, context.Context) (Runner, error)`
- Cancelling the context terminates the session (best-effort; `Cancel()` provides immediate termination).
- `Receive()` channel closes when the process exits.
- No `Close()` method. No `*Session` handle. The runner IS the session.
- The runner internally stores `sessionID` and `planFilePath` extracted from the stream. `ExtractPlan()` uses the stored `sessionID` and `cwd` to locate the JSONL log and extract the plan, replacing the old `ReadPlanFromRun(result RunResult)` chain.

### BudgetGuard

`BudgetGuard` is an orchestrator-level decorator (in `internal/orchestrator/budget.go`) that enforces token budgets both **before** and **after** each session:

- **Pre-check**: `Check() error` — returns `ErrBudgetExhausted` if over budget.
- **Post-check**: `Wrap(Runner, agentID string) Runner` — decorator that wraps `Receive()` for per-agent usage recording.

This preserves the current behavior where `budgetedRunner.Wrap` records per-agent usage and checks budget.

### Runner Construction — Unified Factory

A single `NewRunner(cfg RunnerConfig, ctx context.Context) (Runner, error)` constructor inspects `cfg.Sandbox`:

- If `SandboxConfig` is non-empty, it creates a sandbox-wrapped runner (`sandboxedRunner`).
- Otherwise, it creates a direct `ClaudeCLI` runner.
- Both share the same `Post`/`Receive`/`ExtractPlan`/`SetEvents`/`SessionID`/`Cancel` implementation.
- The sandbox layer is a decorator applied inside the factory, not a separate struct like `SandboxCLIRunner`.

This eliminates the two separate code paths (`ClaudeCLI` vs `SandboxCLIRunner`) that currently have different arg building, different sandbox wrapping (`sb.Run()` vs direct `exec.CommandContext`), and different stream parsing (`parseStreamLines` vs `parseStream`).

## What's Replaced

| Old | New |
|---|---|
| `CLIRunner` (RunPrint, RunStreaming) | `Runner.Post` + `Receive` |
| `ContinuableRunner` (RunContinue) | `Runner.Post` (session via `RunnerConfig.SessionID`) |
| `InteractiveRunner` (RunInteractive -> *InteractiveSession) | `Runner.Post` + `Receive` + `SetEvents` + `SessionID` + `Cancel` |
| `SandboxCLIRunner` struct | Unified runner with `SandboxConfig` in `RunnerConfig` |
| `budgetedRunner` | `BudgetGuard` as `Runner` decorator with pre-check |
| `QueryRunner` | Removed (unused) |
| `InteractiveSession` | Removed (runner IS the session) |
| `RunResult` | Replaced by `EventSessionDone` + `TokenUsage` from `Receive()` |
| `StreamUpdate` | Replaced by `Event` |
| `ClaudeCLIOption` pattern | Replaced by `RunnerConfig` |

## What Survives

- `TokenUsage` — kept for compatibility with `agent.PlanResult`
- `RunUsage` — orchestrator-level token accounting
- `logpath.go` helpers — `ResolveSessionLogPath`, `ExtractPlanFilePath` — needed by `ExtractPlan`
- `stream_event.go` — constants only (StreamUpdate struct removed)

## Migration Scope

Callers to migrate:

1. `cmd/orqestra/main.go` — runner creation (researcher, architect, critic, worker)
2. `internal/orchestrator/engine.go` — `Runners` struct, `runRunnerStreaming`, `runRunnerContinue`, stream conversion
3. `internal/orchestrator/budget.go` — `budgetedRunner` -> `BudgetGuard` with pre-check
4. `internal/agent/planner.go` — `Planner` uses `Runner` + `SetEvents`
5. `cmd/harness/main.go` — `Runner` with `SetEvents` + `Cancel`
6. `cmd/proto-askuser/main.go` — `Runner` with `SetEvents` + `Cancel`
7. `internal/testutil/doubles.go` — `FakeRunner`
8. `internal/orchestrator/stream_capture.go` — `OnUpdate(Event)`
9. `internal/tui/screen_run_detail_log.go` — `formatLogUpdates([]Event)`
10. `internal/harness/logparser.go` — `ParseSessionLogStream` returns `[]Event`

## Explicit Type Changes

The following type references must be updated from `ContinuableRunner` / `StreamUpdate` / `RunResult` to the new types:

### Runner interface changes

| Location | Old Type | New Type |
|---|---|---|
| `Runners` struct fields (engine.go:67-70) | `harness.ContinuableRunner` | `harness.Runner` |
| `WorktreeRunnerFactory` return (engine.go:101) | `harness.ContinuableRunner` | `harness.Runner` |
| `guard.Wrap` (main.go) | returns `harness.ContinuableRunner` | returns `harness.Runner` |
| `budgetedRunner` (budget.go:44) | implements `ContinuableRunner` | implements `Runner` |
| `Planner.runner` (planner.go:33) | `harness.ContinuableRunner` | `harness.Runner` |
| `FakeRunner` (doubles.go:26) | implements `ContinuableRunner` | implements `Runner` |

### Event / StreamUpdate changes

| Location | Old Type | New Type |
|---|---|---|
| `engine.go:116` rawStream channel | `chan harness.StreamUpdate` | `chan Event` |
| `engine.go:126-138` stream-to-StreamEntry switch | `u.IsDelta`, `u.Text`, `u.Tool`, `u.UsageValid` | `e.Kind`, `e.Text`, `e.Tool`, `e.Kind == EventUsage` |
| `stream_capture.go:54` OnUpdate parameter | `ev harness.StreamUpdate` | `ev Event` |
| `stream_capture.go:65` usage check | `ev.UsageValid` | `ev.Kind == EventUsage` |
| `tui/screen_run_detail_log.go:88` formatLogUpdates | `[]harness.StreamUpdate` | `[]Event` |
| `harness/logparser.go:12` ParseSessionLogStream | returns `[]StreamUpdate` | returns `[]Event` |
| `cmd/harness/tui.go:128` Update case | `harness.StreamUpdate` | `Event` |
| `cmd/harness/tui.go:344` streamUpdateToBlock | takes `StreamUpdate` | takes `Event` |

### RunResult changes

| Location | Old Type | New Type |
|---|---|---|
| `engine.go:241` runRunnerStreaming return | `harness.RunResult` | extracted from `Receive()` channel events |
| `engine.go:247` runRunnerContinue return | `harness.RunResult` | extracted from `Receive()` channel events |
| `planner.go:47` Run return | `harness.RunResult` | extracted from `Receive()` channel events |
| `planner.go:86` Continue return | `harness.RunResult` | extracted from `Receive()` channel events |
| `doubles.go:54-67` FakeRunner | returns `RunResult` | returns events on channel + final `EventSessionDone` |

### Plan extraction changes

| Location | Old Approach | New Approach |
|---|---|---|
| `agent/plan_extract.go:18` ReadPlanFromRun | takes `RunResult{SessionID, PlanFilePath}` | `runner.ExtractPlan(ctx)` uses internally stored sessionID |
| `engine.go:759` ReadPlanFromRun call | `harness.RunResult{SessionID: planSessionID}` | `runner.ExtractPlan(ctx)` |
| `engine.go:927` ReadPlanFromRun call | `harness.RunResult{SessionID: planSessionID}` | `runner.ExtractPlan(ctx)` |
| `engine.go:1052` ReadPlanFromRun call | `harness.RunResult{SessionID: planSessionID}` | `runner.ExtractPlan(ctx)` |

## Implementation Order

### Phase 1: Type Definitions

1. Define new types in `internal/harness/`:
   - `ModelSpec`, `RunnerConfig`, `SandboxConfig`
   - `EventKind` constants and `Event` struct with `IsDelta bool`
   - `Runner` interface with `Post`, `Receive`, `ExtractPlan`, `SetEvents`, `SessionID`, `Cancel`
   - `BudgetGuard` with `Check() error` (pre-check) and `Wrap(Runner, agentID) Runner` (decorator)

2. Define `RunnerFactory` function type: `func(RunnerConfig, context.Context) (Runner, error)`

### Phase 2: ClaudeCLI Refactor

3. Refactor `ClaudeCLI` to implement `Runner`:
   - `Post(string)` — sends NDJSON user message over stdin (adapted from `InteractiveSession.Post` and the initial prompt flow)
   - `Receive() <-chan Event` — drains stdout through the stream parser, emits typed `Event` values
   - `ExtractPlan(ctx context.Context) (string, error)` — uses stored `sessionID` + `cwd` to locate JSONL and extract plan (replaces `ReadPlanFromRun`)
   - `SetEvents(chan<- Event)` — injects events channel; nil = no forwarding (nil-safe)
   - `SessionID() string` — returns stored session ID
   - `Cancel() error` — sends SIGKILL to process group

4. Migrate stream parsing:
   - The existing `parseStream()` function emits `StreamUpdate` via the events channel. Refactor to emit `Event` values instead.
   - `streamEventsFrom()` produces `[]Event` with `IsDelta` mapped from `streamDeltaText`.
   - `emitStreamEvents()` dispatches to the events channel — keep the same logic, change type.

### Phase 3: Sandbox Unification

5. Create unified `NewRunner(cfg RunnerConfig, ctx context.Context) (Runner, error)` constructor:
   - runner is ALWAYS launched with sandbox-exec
   - zero SandboxConfig meaning default sandbox configuration

6. Remove `SandboxCLIRunner` struct and all its methods:
   - `buildCommand()`, `run()`, `runParsed()`, `parseStreamLines()`
   - `extractJSONUsage()`, `extractStreamUsage()`, `extractStreamSessionID()`, `extractStreamResult()`
   - These are absorbed into the unified runner

### Phase 4: BudgetGuard

7. Implement `BudgetGuard` with pre-check:
   - `Check() error` — pre-check, returns `ErrBudgetExhausted` if over budget
   - `Wrap(Runner, agentID string) Runner` — decorator that wraps `Receive()` for post-check and per-agent usage recording

### Phase 5: Orchestrator Migration

8. Migrate `internal/orchestrator/engine.go`:
   - `Runners` struct fields: `harness.ContinuableRunner` -> `harness.Runner`
   - `WorktreeRunnerFactory` return type: `harness.ContinuableRunner` -> `harness.Runner`
   - `runRunnerStreaming` / `runRunnerContinue`: replace `RunStreaming`/`RunContinue` calls with `Post`/`Receive`, extract result from `EventSessionDone`
   - Stream conversion (engine.go:116-143): convert `Event` -> `StreamEntry` inside the runner's `Receive()` goroutine or at the orchestrator boundary
   - `runWithStreamConsumer`: change from `chan harness.StreamUpdate` to `chan Event`; `capture.OnUpdate` takes `Event`
   - `ReadPlanFromRun` calls (engine.go:759, 927, 1052): replace with `runner.ExtractPlan(ctx)`

9. Migrate `internal/orchestrator/budget.go`:
   - `budgetedRunner` -> `BudgetGuard` with pre-check
   - `WrapContinuable` -> `Wrap(Runner, agentID string) Runner`

10. Migrate `internal/orchestrator/stream_capture.go`:
    - `OnUpdate(ev harness.StreamUpdate)` -> `OnUpdate(ev Event)`
    - `ev.UsageValid` -> `ev.Kind == EventUsage`
    - `ev.Text` -> `ev.Text` (same)
    - `ev.Tool` -> `ev.Tool` (same)

### Phase 6: Agent Migration

11. Migrate `internal/agent/planner.go`:
    - `Planner.runner`: `harness.ContinuableRunner` -> `harness.Runner`
    - `Planner.Run(ctx, prompt string, events chan<- harness.StreamUpdate)`: replace with `runner.SetEvents(events)` before `runner.Post(prompt)`, then read from `runner.Receive()`
    - `Planner.Continue(ctx, sessionID, prompt, events)`: same pattern
    - `ReadPlanFromRun(result)`: replace with `runner.ExtractPlan(ctx)`

### Phase 7: TUI Migration

12. Migrate `cmd/harness/main.go`:
    - Remove `*harness.ClaudeCLI` type assertion
    - Use `Runner` directly with `SetEvents(streamUpdates)` before `Post()`
    - `sess.Kill()` -> `runner.Cancel()`
    - `sess.Updates()` -> `runner.Receive()`
    - `sess.SessionID()`, `sess.Usage()`, `sess.PlanPath()`, `sess.ResultError()` -> `runner.SessionID()`, read from `Receive()` events

13. Migrate `cmd/harness/tui.go`:
    - `Model.session`: `*harness.InteractiveSession` -> `harness.Runner`
    - `handleStreamUpdate`: takes `Event` instead of `harness.StreamUpdate`
    - `streamUpdateToBlock`: takes `Event` instead of `harness.StreamUpdate`
    - `TextBlock.IsDelta` check: use `Event.IsDelta` (already present in new Event struct)
    - `UsageBlock` detection: check `Event.Kind == EventUsage`

14. Migrate `internal/tui/screen_run_detail_log.go`:
    - `formatLogUpdates([]harness.StreamUpdate)` -> `formatLogUpdates([]Event)`
    - `update.Tool`, `update.Text` -> same fields on `Event`
    - `parseSessionLogFile` returns `[]Event`

15. Migrate `cmd/proto-askuser/main.go`:
    - `buildRunnerWithBridge`: returns `harness.Runner` instead of `harness.CLIRunner`
    - Use `harness.NewRunner(RunnerConfig, ctx)` instead of `harness.NewClaudeCLI(resolved, opts...)`
    - Replace `runner.RunStreaming(ctx, prompt, systemPrompt, updates)` with `runner.SetEvents(updates)` + `runner.Post()` + `runner.Receive()`

### Phase 8: Test Infrastructure

16. Migrate `internal/testutil/doubles.go`:
    - `FakeRunner` implements `Runner` interface
    - `FakeCall` fields: remove `PlanFilePath` (deprecated), keep `Output`, `SessionID`, `Usage`, `Err`
    - `RunPrint`, `RunStreaming`, `RunContinue` -> `Post`, `Receive`, `ExtractPlan`, `SetEvents`, `SessionID`, `Cancel`

17. Migrate `internal/harness/logparser.go`:
    - `ParseSessionLogStream` returns `[]Event` instead of `[]StreamUpdate`

### Phase 9: Cleanup

18. Remove obsolete interfaces and types:
    - `CLIRunner`, `ContinuableRunner`, `InteractiveRunner` interfaces
    - `RunResult` struct
    - `StreamUpdate` struct
    - `InteractiveSession` struct
    - `ClaudeCLIOption` pattern
    - `SandboxCLIRunner` struct and `SandboxCLIRunnerConfig`
    - `QueryRunner` (already unused)
    - `StatsTracker` (already unused)

19. Update all tests to use the new types.

## Blocker Resolution Summary

| # | Blocker | Resolution |
|---|---|---|
| 1 | Event missing `IsDelta` | Added `IsDelta bool` to `Event`. Stream parser maps `streamDeltaText` -> `Event{Kind: EventChunk, IsDelta: true}`. TUI checks `Event.IsDelta` for cursor indicator. |
| 2 | `ExtractPlan` cannot locate plan file | Runner stores `sessionID` internally after stream parsing begins. `ExtractPlan(ctx)` uses stored `sessionID` + `cwd` to call `ResolveSessionLogPath` + `ExtractPlanFilePath`. Signature returns `(string, error)` for proper error handling. |
| 3 | BudgetGuard pre-check removed | `BudgetGuard` has `Check() error` for pre-check and `Wrap(Runner, agentID) Runner` for post-check. |
| 4 | Planner needs events channel | `Runner.SetEvents(chan<- Event)` called before `Post()`. `Post()` ignores events if channel is nil (nil-safe). Planner calls `SetEvents(events)` then `Post(prompt)` then reads `Receive()`. |
| 5 | cmd/harness TUI features unmapped | `Runner` exposes `Cancel()` (replaces `Kill()`), `SessionID()` (replaces `sess.SessionID()`). `SetEvents` + `Receive()` replace `RunInteractive` + `Updates()`. `Post()` replaces `sess.Post()`. |
| 6 | `RunResult.PlanFilePath` lost | `PlanFilePath` removed from `Event` (user confirmed deprecated). Plan extraction uses `runner.ExtractPlan(ctx)` which internally uses stored `sessionID`. |
| 7 | SandboxCLIRunner two code paths | Unified `NewRunner(cfg, ctx)` factory. Sandbox layer is a decorator inside the factory, not a separate struct. Both paths share the same implementation. |
| 8 | streamCapture uses `StreamUpdate` | Explicit migration: `OnUpdate(Event)`, `Event.Kind == EventUsage` replaces `ev.UsageValid`. Stream conversion at orchestrator boundary. |
| 9 | `ContinuableRunner` references | Explicit table of all type changes. `Runners` fields, `WorktreeRunnerFactory`, `WrapContinuable` -> `Wrap`. |
| 10 | No `Kill()` / termination | `Cancel() error` on `Runner` interface. Sends SIGKILL to process group. Distinct from context cancellation (explicit user action). |
