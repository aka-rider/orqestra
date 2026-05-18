# Plan: Token Ownership Refactor

## Thesis

Tokens are consumed by agents within a run. The **run** owns the accumulator.
A budget is a **policy** applied to that accumulator. These are two responsibilities.

The previous plan merged both into `RunBudget`. That's the same SRP violation as `tokenlimit.Store` — just without SQLite. The right decomposition:

| Concept       | Responsibility                       | Analogy                  |
| ------------- | ------------------------------------ | ------------------------ |
| `RunUsage`    | "What happened" — accumulate, report | Bank statement           |
| `BudgetGuard` | "Is this allowed" — enforce limit    | Spending cap on the card |

The statement records transactions. The cap decides whether to authorize the next one. The TUI reads the statement. It never sees the cap logic.

---

## Decomposition

**`TokenUsage`** (harness) — wire format. What a single call measured.

```
TokenUsage{Input, Output int64}
func (u TokenUsage) Total() int64
```

**`RunUsage`** (orchestrator) — golden source. What the run consumed so far.

```
RunUsage { mu sync.Mutex; input, output int64; byAgent map[…] }
  .Record(agentID, TokenUsage)    // write
  .Snapshot() UsageSnapshot       // read
```

**`BudgetGuard`** (orchestrator) — policy. May the next call proceed?

```
BudgetGuard { usage *RunUsage; limit int64 }
  .Check(agentID) error                        // reads RunUsage
  .WrapRunner(CLIRunner, agentID) CLIRunner    // creates decorator
```

The decorator does:

1. `guard.Check()` → reads `usage.Snapshot()`, compares to limit
2. `inner.Run*()` → `RunResult`
3. `usage.Record(agentID, result.Usage)` → writes golden source
4. `guard.Check()` → returns `ErrBudgetExhausted` if now over

---

## OLD Architecture: Where It Breaks

### Protocol Defect: Derived Field in Wire Type

```go
// harness/claude_cli.go:19
type TokenUsage struct {
    InputTokens  int64
    OutputTokens int64
    TotalTokens  int64     // ← DERIVED: always Input+Output
}
```

**Evidence it's redundant — consumers re-derive it anyway:**

- `session.go:252` — `totalTokens += meta.InputTokens + meta.OutputTokens`
- `screen_pipeline.go:1319` — `total := a.InputTokens + a.OutputTokens`
- `screen_run_detail.go:205` — `total := step.InputTokens + step.OutputTokens`

**The single consumer of `.TotalTokens` is `tokenlimit/runner.go:37-38`** — and that consumer is being deleted.

### Ownership Defect: Parallel Reality

| Who writes token state?      | Where does it live?                           |
| ---------------------------- | --------------------------------------------- |
| `parseStream()`              | `RunResult.Usage` (harness, ephemeral)        |
| `LimitedRunner.Record()`     | SQLite `token_usage` (tokenlimit, persistent) |
| `emit(Event{InputTokens:…})` | Event struct fields (orchestrator, ephemeral) |
| `ApplyEvent()`               | `AgentRow.Input/Output` (TUI, ephemeral)      |

Four writes. Four locations. Three packages. One concept: _how many tokens did this agent use?_

`tokenlimit` creates a parallel accounting ledger that:

- Persists across runs (wrong scope — user wants per-run)
- Routes by model string (wrong key — tokens belong to agents, not models)
- Requires 3 SQL queries per call (check + upsert + recheck)
- Pulls in 4 `modernc.org/*` dependencies for a counter
- Has no connection to the TUI display path

### Flow Defect: Worktree Gap

```go
// main.go:~390 — WorktreeRunnerFactory
worktreeRunnerFactory := func(worktreePath string) harness.ContinuableRunner {
    wtCfg := sandboxRunnerCfg
    wtCfg.WorktreePath = worktreePath
    return harness.NewSandboxCLIRunner(wtCfg)  // ← NO budget wrapper
}
```

Worker + validator in a worktree bypass budget enforcement entirely.

### OLD Data Flow

```
                STREAMING (during call)
               ┌────────────────────────────────────────────┐
               │ claude CLI stdout (NDJSON)                  │
               │      │                                      │
               │      ▼                                      │
               │ parseStream()             [claude_cli:341]  │
               │ ├─ text → io.Writer → StreamBuffer          │
               │ ├─ tools → ActivitySink → StreamBuffer      │
               │ └─ "result" →                               │
               │    TokenUsage{In, Out, Total=In+Out}        │
               │           │                                 │
               │           ▼                                 │
               │    RunResult{Usage, SessionID, ...}         │
               └───────────┬────────────────────────────────┘
                           │
                CALL-BASED (after call returns)
               ┌───────────┼────────────────────────────────┐
               │           ▼                                 │
               │  LimitedRunner             [runner.go]      │
               │  ├─ PRE:  SELECT SUM FROM token_usage       │
               │  ├─ inner.Run*() → RunResult                │
               │  └─ POST: INSERT ON CONFLICT + SELECT SUM   │
               │                                             │
               │  3 SQL queries per call                     │
               │  Cross-run. Keyed by (model, agent_id).     │
               └───────────┬────────────────────────────────┘
                           │
                EVENT-BASED (orch → TUI)
               ┌───────────┼────────────────────────────────┐
               │           ▼                                 │
               │  emit(Event{                                │
               │    InputTokens: usage.InputTokens,    ×12   │
               │    OutputTokens: usage.OutputTokens})       │
               │           │                                 │
               │           ▼                                 │
               │  AgentRow.InputTokens = event.Input   ×12   │
               │  viewSidebar reads AgentRow  ← 4th copy    │
               │  Only updates on EventAgentDone             │
               │  NO budget visibility                       │
               └─────────────────────────────────────────────┘

   4 token stores. 3 SQL queries/call. 5 hand-offs.
   TUI blind between calls. Worktree unmetered.
```

---

## NEW Data Flow

```
                STREAMING (during call — unchanged)
               ┌────────────────────────────────────────────┐
               │ claude CLI stdout (NDJSON)                  │
               │      │                                      │
               │      ▼                                      │
               │ parseStream()                               │
               │ ├─ text → io.Writer → StreamBuffer          │
               │ ├─ tools → ActivitySink → StreamBuffer      │
               │ └─ "result" → TokenUsage{Input, Output}    │
               │           │                                 │
               │           ▼                                 │
               │    RunResult{Usage, SessionID, ...}         │
               └───────────┬────────────────────────────────┘
                           │
                CALL-BASED (decorator writes RunUsage,
                            BudgetGuard reads RunUsage)
               ┌───────────┼────────────────────────────────┐
               │           ▼                                 │
               │  budgetedRunner                             │
               │  ├─ PRE: guard.Check(agentID)               │
               │  │        guard reads usage.Snapshot()      │
               │  │        compares total to limit           │
               │  │                                          │
               │  ├─ inner.Run*() → RunResult                │
               │  │                                          │
               │  ├─ usage.Record(agentID, result.Usage)     │
               │  │  └─ mutex write to golden source         │
               │  │                                          │
               │  └─ POST: guard.Check(agentID)              │
               │           └─ ErrBudgetExhausted if over     │
               │                                             │
               │  RunUsage: state (accumulates)              │
               │  BudgetGuard: policy (reads, decides)       │
               └───────────┬────────────────────────────────┘
                           │
                POLL-BASED (TUI reads RunUsage directly)
               ┌───────────┼────────────────────────────────┐
               │                                             │
               │  RunChannels{                               │
               │    Events    <-chan Event  (transitions)     │
               │    Decisions chan<- Decision                 │
               │    Stream    *StreamBuffer (text+tools)     │
               │    Usage     *RunUsage     (token state)    │
               │    Budget    int64         (limit, display) │
               │  }                                          │
               │                                             │
               │  TUI polls Usage.Snapshot() on tickMsg      │
               │  TUI never sees BudgetGuard                 │
               │  TUI renders Budget int64 for display only  │
               │                                             │
               │    "✓ researcher    12s   203k"             │
               │    "▶ worker        34s   412k"             │
               │    "─────────────────────────"              │
               │    " Tokens: 1.2M / 2.0M (60%)"            │
               └─────────────────────────────────────────────┘
```

### Responsibility Map

```
Component        Responsibility        Reads       Writes
────────────────────────────────────────────────────────────
TokenUsage       wire format            —          —
RunUsage         accumulation + read    —          itself
BudgetGuard      enforcement policy     RunUsage   —
budgetedRunner   call interception      —          RunUsage
TUI              display                RunUsage   —
Event            state transitions      —          —
StepMeta/logs    diagnostics            RunResult  disk
```

No component does two things. No data is stored in two places.

### Side-by-Side

|                | OLD                                                 | NEW                                          |
| -------------- | --------------------------------------------------- | -------------------------------------------- |
| Token state    | 4 copies                                            | 1 (`RunUsage`)                               |
| Policy         | mixed into `tokenlimit.Store` (state + policy + IO) | `BudgetGuard` reads `RunUsage` (policy only) |
| Protocol       | `TotalTokens` stored field                          | `Total()` method                             |
| TUI sees       | `AgentRow` copy from `Event` copy from `RunResult`  | `RunUsage.Snapshot()` (golden source)        |
| Budget display | none                                                | `int64` limit passed through `RunChannels`   |
| SRP            | Store = state + policy + SQL                        | RunUsage = state. BudgetGuard = policy.      |
| Hand-offs      | 5                                                   | 2                                            |
| Ops per call   | 3 SQL queries                                       | 2 mutex ops                                  |
| Dependencies   | 4 `modernc.org/*`                                   | 0                                            |
| Scope          | cross-run (needs `reset-usage`)                     | per-run (auto)                               |
| TUI update     | on `EventAgentDone` only                            | every tick (1s)                              |
| Worktree       | unmetered                                           | wrapped                                      |

---

## Steps (HOW)

### Phase 1: Fix the Wire Protocol (harness + agent)

**Step 1** — Clean `TokenUsage` in `harness/claude_cli.go:19`:

- `TokenUsage{Input, Output int64}` + `func (u TokenUsage) Total() int64`
- Drop `TotalTokens` field. Rename `InputTokens` → `Input`, `OutputTokens` → `Output`.
- Update 6 construction sites in harness: `parseStream` [L365], `RunPrint` envelope [L243], `extractStreamUsage` [sandbox_cli_runner.go:270], `extractJSONUsage` [L245], `query.go` Result [L204].
- Update `StepMeta` in `agent/session.go:73`: field renames, `RunSummary.TotalTokens` → `Tokens`.
- Update 7 agent return signatures (pass-through renames).

### Phase 2: Build RunUsage + BudgetGuard, Delete tokenlimit (_depends on 1_)

**Step 2** — Create `internal/orchestrator/usage.go` (~60 LoC):

- `RunUsage` struct: `mu sync.Mutex`, `input/output int64`, `byAgent map[string]struct{Input, Output int64}`
- `NewRunUsage() *RunUsage`
- `Record(agentID string, usage harness.TokenUsage)` — mutex write, accumulate by agent
- `Snapshot() UsageSnapshot` — mutex read, return immutable value type

  `UsageSnapshot` is a pure value:

  ```go
  type UsageSnapshot struct {
      Input   int64
      Output  int64
      ByAgent map[string]struct{ Input, Output int64 }
  }
  func (s UsageSnapshot) Total() int64
  ```

  `RunUsage` has ONE job: accumulate and report. No budget logic. No policy.

**Step 3** — Create `internal/orchestrator/budget.go` (~90 LoC):

- `BudgetGuard` struct: `usage *RunUsage`, `limit int64`
- `NewBudgetGuard(usage *RunUsage, limit int64) *BudgetGuard`
- `Check(agentID string) error` — reads `usage.Snapshot().Total()`, compares to `limit`. Returns `ErrBudgetExhausted` if over. Returns nil if `limit <= 0` (unlimited).
- `WrapRunner(inner CLIRunner, agentID string) CLIRunner`
- `WrapContinuable(inner ContinuableRunner, agentID string) ContinuableRunner`
- `ErrBudgetExhausted{AgentID string, Used int64, Budget int64}`
- `IsBudgetExhausted(err error) bool`

  Unexported `budgetedRunner` inside same file:
  1. `guard.Check(agentID)` — pre-call
  2. `inner.Run*()` → `RunResult`
  3. `guard.usage.Record(agentID, result.Usage)` — write golden source
  4. `guard.Check(agentID)` — post-call (may return ErrBudgetExhausted)

  `BudgetGuard` has ONE job: decide whether a call is allowed. It delegates recording to `RunUsage`.

**Step 4** — Delete `internal/tokenlimit/` entirely (~1100 LoC across 5 files).

**Step 5** — Create `internal/orchestrator/usage_test.go` + `budget_test.go` (~140 LoC total):

- `usage_test.go`: Record accumulates correctly per agent. Snapshot returns consistent copy. Concurrent Record/Snapshot race test.
- `budget_test.go`: Check under/at/over. Unlimited (limit=0). budgetedRunner blocks on pre-check. budgetedRunner records + signals exhaustion post-call. Runner passes through inner errors.

### Phase 3: Rewire Config, Orchestrator, main.go (_depends on 2_)

**Step 6** — Config: `PipelineConfig.TokenBudget` → `RunTokenBudget string` in `config.go:201`. Default `"2M"` in `pipeline.yaml`.

**Step 7** — Orchestrator:

- Add `Usage *RunUsage` and `Guard *BudgetGuard` fields to `Engine`.
- Add `Usage *RunUsage` and `Budget int64` to `RunChannels`.
- Remove `InputTokens`/`OutputTokens` from `Event` struct and ~12 `emit()` calls.
- `Engine.Start()` passes `e.Usage` and configured limit to `RunChannels`.

**Step 8** — main.go: delete `openStore`, `openLimiter`, `resolveModelString`, old `wrapRunner`, `runUsage`, `runResetUsage`. Rewrite `buildEngine()`:

```go
usage := orchestrator.NewRunUsage()
limit := parseRunBudget(cfg)
guard := orchestrator.NewBudgetGuard(usage, limit)

Runners{
    Researcher: guard.WrapRunner(researcherRunner, "researcher"),
    Architect:  guard.WrapRunner(architectRunner, "architect"),
    Critic:     guard.WrapRunner(criticRunner, "critic"),
    Worker:     guard.WrapContinuable(sandboxRunner, "worker"),
}
WorktreeRunnerFactory: func(path) {
    return guard.WrapContinuable(newSandboxRunner(path), "worker")
}
```

### Phase 4: TUI reads RunUsage on tick (_depends on 3_)

**Step 9** — `Model` stores `usage *RunUsage` and `budgetLimit int64` from `RunChannels`. On `tickMsg`, calls `usage.Snapshot()`.

**Step 10** — `viewSidebar()` renders from snapshot + budget limit. Remove `AgentRow.InputTokens/OutputTokens`, `reviewTokensIn/Out`, token accumulation from `ApplyEvent`.

### Phase 5: Cleanup (_depends on all_)

**Step 11** — `go mod tidy`: removes `modernc.org/sqlite` + 3 transitives.
**Step 12** — Update `orchestrator_test.go:390`: `orchestrator.ErrBudgetExhausted`.

---

## File Map

| File                                         | Action     | Key Changes                                                        |
| -------------------------------------------- | ---------- | ------------------------------------------------------------------ |
| `internal/harness/claude_cli.go`             | MODIFY     | `TokenUsage{Input, Output}`, drop TotalTokens, add Total()         |
| `internal/harness/sandbox_cli_runner.go`     | MODIFY     | Field renames in extraction functions                              |
| `internal/harness/query.go`                  | MODIFY     | Field renames in Result construction                               |
| `internal/tokenlimit/`                       | DELETE ALL | 5 files, ~1100 LoC                                                 |
| `internal/orchestrator/usage.go`             | CREATE     | `RunUsage`, `UsageSnapshot` (~60 LoC)                              |
| `internal/orchestrator/budget.go`            | CREATE     | `BudgetGuard`, `ErrBudgetExhausted`, `budgetedRunner` (~90 LoC)    |
| `internal/orchestrator/usage_test.go`        | CREATE     | RunUsage tests (~60 LoC)                                           |
| `internal/orchestrator/budget_test.go`       | CREATE     | BudgetGuard + runner tests (~80 LoC)                               |
| `internal/orchestrator/orchestrator.go`      | MODIFY     | Remove Event token fields, add Usage/Budget to RunChannels+Engine  |
| `internal/orchestrator/orchestrator_test.go` | MODIFY     | ErrBudgetExhausted now in orchestrator pkg                         |
| `internal/config/config.go`                  | MODIFY     | RunTokenBudget, validation                                         |
| `internal/config/pipeline.yaml`              | MODIFY     | `run_token_budget: "2M"`                                           |
| `cmd/orqestra/main.go`                       | MODIFY     | Delete 6 functions, rewrite buildEngine                            |
| `internal/tui/model.go`                      | MODIFY     | Store RunUsage + limit, poll on tick, remove AgentRow token fields |
| `internal/tui/screen_pipeline.go`            | MODIFY     | Render from UsageSnapshot + limit                                  |
| `internal/agent/session.go`                  | MODIFY     | StepMeta/RunSummary field renames                                  |
| `internal/agent/*.go`                        | MODIFY     | Return type field renames (pass-through)                           |
| `go.mod`                                     | MODIFY     | Remove modernc.org/sqlite + 3 indirect                             |

## Verification

1. `go build ./...`
2. `go test -race ./internal/orchestrator/` — usage, budget, orchestrator
3. `go test -race ./...` — full suite
4. `grep -r 'tokenlimit\|TotalTokens\|modernc.org' internal/ cmd/ go.mod` — zero

## LoC Impact: ~-910 net

| Action                                 | LoC       |
| -------------------------------------- | --------- |
| Delete `internal/tokenlimit/`          | -1100     |
| Create usage.go + budget.go + tests    | +290      |
| Remove Event token fields + emit calls | -40       |
| Delete 6 functions from main.go        | -80       |
| Remove TUI token accumulation          | -15       |
| Harness/agent renames                  | ~+35      |
| **Net**                                | **~-910** |

Plus removal of 4 go.mod dependencies.
