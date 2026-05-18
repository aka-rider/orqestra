# Plan: Token Ownership & Data Model Refactor + TUI Dashboard Enrichment

## TL;DR

Unify the four parallel token accounting paths into a single `RunUsage` golden source that serves both live TUI polling and historical run views. Merge budget enforcement into a `BudgetGuard` decorator. Delete `tokenlimit/` (SQLite). Design `RunSnapshot` as the universal data shape consumed by both the live dashboard and historical run detail screens — same types, same rendering code, different data sources (mutex poll vs. disk load).

## Scope

This plan covers THREE interrelated concerns:

1. **StreamBuffer → StreamRing + channel** — unified typed entry stream replacing parallel lines/activities (Phase 0)
2. **Token ownership refactor** — RunUsage/BudgetGuard replacing tokenlimit (Phases 1-3)
3. **TUI dashboard enrichment** — new content modes consuming the unified data model (Phases 4-6, separate plan file)

### Key Protocol Fact: Usage in Claude CLI Stream

Claude CLI's `streamEvent` struct has `Usage *streamUsage` on ALL events (not just "result"). The code currently only checks it on `event.Type == "result"`, but Anthropic's streaming API emits usage on `message_start` (input tokens) and `message_delta` (output tokens incrementally). If Claude CLI surfaces these, mid-call stats are available — the harness just needs `if event.Usage != nil` outside the result block.

### Data Source Separation

- **StreamRing** = TUI's SOLE data source. Contains text, tools, AND stats as unified entries. TUI polls this only.
- **RunUsage** = budget enforcement only. BudgetGuard reads it. budgetedRunner writes post-call. TUI NEVER reads it.

Both fed from the same harness stream. Ring for presentation, RunUsage for accounting/kill-switch.

StreamEntry kinds: `EntryText`, `EntryToolUse`, `EntryStats` (carries latest known in/out tokens).
StreamEntry is a pure value type (string/int64 only) — no memory aliasing risk on Snapshot copy.

### Zero-Value Safety Pattern for StreamEntry

```
type StreamEntry struct {
    Kind    StreamEntryKind
    Text    string       // EntryText: completed line
    Tool    string       // EntryToolUse: tool name
    Detail  string       // EntryToolUse: human-readable detail
    Stats   EntryStats   // value type, zero = not populated
}

type EntryStats struct {
    InputTokens  int
    OutputTokens int
    Valid  bool   // false by default — consumer checks: if entry.Stats.Valid { ... }
}
```

NO pointers in StreamEntry. Zero value of EntryStats is safe to read (InputTokens=0, OutputTokens=0, Valid=false). Consumer pattern: `if entry.Stats.Valid { render(entry.Stats) }`. This eliminates nil checks, aliasing across goroutines, and shared heap objects in the ring buffer.

The harness-internal `*streamUsage` stays for JSON unmarshaling (doesn't escape parseStream), but the StreamEntry constructed from it is always a value copy:

```
if event.Usage != nil {
    entry.Stats = EntryStats{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens, Valid: true}
}
```

TUI live metrics = latest EntryStats in the ring (updated mid-call if Claude CLI emits, or end-of-call from result event). Per-agent totals from agent snapshots.

The plans are co-designed because the data types must serve both.

## Critical Review of Existing Plan

### What's Right

- **Thesis is sound**: RunUsage (state) + BudgetGuard (policy) is the correct SRP decomposition
- **Wire protocol fix**: Dropping `TotalTokens` stored field in favor of `Total()` method — correct, all consumers re-derive it
- **Ownership defect analysis**: The four-write/four-location problem is accurately diagnosed
- **Worktree gap**: Real bug — worktree runners bypass budget entirely
- **LoC impact**: ~-910 net is realistic

### What's Wrong or Missing

1. **Plan assumes old agent structs exist** — `Researcher`, `Architect`, `Critic` structs are already deleted. The unified `Planner` entity from `plan-unify-plan-mode-agents` is implemented. Runners struct already uses `ContinuableRunner` for all fields. Phase 1 Step 1 (field renames in agent return signatures) is simpler now.

2. **No disk format for RunSnapshot** — The plan creates `RunUsage` for live polling but never persists it. Historical run detail still loads from individual `*_meta.json` files and re-derives totals. This means the live dashboard shows rich data (model, context %, tok/s) but historical views can't.

3. **AgentMeta disconnected from RunUsage** — The TUI dashboard plan puts `AgentMeta` on `Event` and `AgentRow`. The token refactor puts tokens in `RunUsage`. This creates two polling sources for what should be one entity: "everything about this agent's execution". `RunUsage` should own per-agent metadata too.

4. **No streaming token integration** — The plan only records tokens on call completion (from `RunResult.Usage`). The TUI dashboard plan adds `UsageSink` for mid-call streaming deltas. These need to be reconciled: `RunUsage` should be the accumulation target for both.

5. **`StepMeta` schema gap** — `StepMeta` (disk artifact) lacks `ModelDisplay`, `Provider`, `ContextWindow`. The TUI dashboard plan adds these, but the token refactor plan doesn't mention it.

6. **Event struct still carries token fields in transition** — Plan removes `InputTokens`/`OutputTokens` from events but doesn't address backward compat for headless mode consumers that read events.

7. **`usage` and `reset-usage` CLI subcommands** — Plan deletes `tokenlimit` but doesn't address what replaces `orqestra usage` and `orqestra reset-usage`. These become no-ops or need new semantics (per-run usage is auto-scoped, no reset needed).

## Revised Design: Unified Data Model

### Core Types

**Package `internal/orchestrator/`**

```
AgentMeta {
    ModelRef      string    // config key, e.g., "opus-4"
    ModelDisplay  string    // underlying model, e.g., "claude-opus-4-20250514"
    Provider      string    // e.g., "anthropic"
    ContextWindow int64     // tokens
}

AgentSnapshot {
    AgentID   string
    Meta      AgentMeta
    Input     int64
    Output    int64
    CallCount int           // how many LLM calls (workers may have 2+: exec + validation)
    StartTime time.Time
    EndTime   time.Time     // zero = still running
    Status    string        // "running", "done", "failed", "cancelled"
}

RunSnapshot {
    Input   int64           // aggregate
    Output  int64           // aggregate
    Limit   int64           // budget cap, 0 = unlimited
    Agents  []AgentSnapshot // ordered by start time
}
    .Total() int64
    .AgentByID(id string) (AgentSnapshot, bool)
    .ContextPercent(agentID string) float64
    .TokPerSec(agentID string) float64
    .BudgetPercent() float64

RunUsage {                  // golden source, concurrent-safe
    mu      sync.Mutex
    limit   int64
    agents  map[string]*agentAccum  // internal mutable state
    order   []string                // insertion order for stable snapshot
}
    .Record(agentID string, usage harness.TokenUsage)
    .StartAgent(agentID string, meta AgentMeta)
    .EndAgent(agentID string, status string)
    .Snapshot() RunSnapshot // immutable copy
```

**Package `internal/harness/`**

```
TokenUsage {Input, Output int64}
    .Total() int64
```

Drop `TotalTokens` field, rename `InputTokens`→`Input`, `OutputTokens`→`Output`.

**Package `internal/orchestrator/`**

```
BudgetGuard {usage *RunUsage}
    .Check(agentID string) error
    .WrapRunner(inner CLIRunner, agentID string) CLIRunner
    .WrapContinuable(inner ContinuableRunner, agentID string) ContinuableRunner
```

BudgetGuard reads limit from RunUsage (set at construction). budgetedRunner decorator:

1. guard.Check() → pre-call
2. inner.Run\*() → RunResult
3. usage.Record(agentID, result.Usage) → write golden source
4. guard.Check() → post-call, may return ErrBudgetExhausted

### RunChannels Extension

```
RunChannels {
    Events    <-chan Event
    Decisions chan<- Decision
    Stream    *StreamRing       // unified typed entry ring (was *StreamBuffer)
}
```

Note: `RunUsage` is NOT in RunChannels. TUI reads stats from StreamRing entries only.

### Event Struct Simplification

Remove from `Event`: `InputTokens`, `OutputTokens` (TUI gets these from StreamRing).

Keep events for state transitions only: `EventAgentStarted`, `EventAgentDone`, `EventAgentFailed`.

### Session Artifact: `run_snapshot.json`

Written at `EventComplete` time. Contains `RunSnapshot` serialized as JSON. Historical `RunDetailScreen` loads this file. Shared rendering code with live dashboard.

Individual `*_meta.json` files still written (they carry Claude session paths, plan file paths — execution metadata that RunSnapshot doesn't own).

### StepMeta Extension

Add to `agent.StepMeta`:

- `ModelDisplay string` (json:"model_display,omitempty")
- `Provider string` (json:"provider,omitempty")
- `ContextWindow int64` (json:"context_window,omitempty")
- `CallCount int` (json:"call_count,omitempty")

---

## Implementation Steps

### Phase 0: StreamBuffer → StreamRing (_no dependencies_)

**Step 0a** — Replace `StreamBuffer` with `StreamRing` in `internal/orchestrator/stream_ring.go`:

- Unified `[]StreamEntry` ring buffer (replaces parallel `lines[]` + `activities[]`)
- `StreamEntryKind` enum: `EntryText`, `EntryToolUse`, `EntryStats`
- `StreamEntry` pure value type (zero-value safe, no pointers)
- `EntryStats{Input, Output int64; Valid bool}` — consumer checks `if entry.Stats.Valid`
- `Append(StreamEntry)` — single append method for all kinds
- `SetAgent(id)` — snapshots all entries for previous agent (not just activities)
- `Snapshot() (agentID string, entries []StreamEntry)` — unified return
- `AgentEntries(id string) []StreamEntry` — replaces `AgentActivities`
- Partial-line text accumulation stays internal (same logic, `EntryText` emitted on newline)

**Step 0b** — Replace `streamWriter` with channel adapter:

- `channelWriter` implements `io.Writer` + `harness.ActivitySink`
- Pushes `StreamEntry` onto a buffered `chan StreamEntry`
- Partial-line accumulation in Write() (same as current), emits `EntryText` on newline
- `OnToolUse(name, detail)` → pushes `EntryToolUse`
- Channel closed when parseStream returns

**Step 0c** — Fan-out goroutine in engine:

- Created by `Engine.Start()`, reads from channel
- Pushes each entry to `StreamRing.Append(entry)`
- If `entry.Stats.Valid` → also calls `RunUsage.Record()` (once RunUsage exists; skip in Phase 0)
- Exits when channel closes

**Step 0d** — Update TUI consumers:

- `viewStreaming()`: filter `EntryText` entries for stream preview, `EntryToolUse` for activity log
- `viewDashboard()`: filter `EntryToolUse` for agent cards
- `AgentActivities()` → `AgentEntries()` with kind filter
- Live stats: scan for latest `EntryStats` in snapshot

**Step 0e** — Delete old `stream_buffer.go`, create `stream_ring.go` + `stream_ring_test.go`

**Step 0f** — Harness: in `dispatchStreamEvent`, when `event.Usage != nil`, push `StreamEntry{Kind: EntryStats, Stats: EntryStats{Input: ..., Output: ..., Valid: true}}` through the writer/channel. This captures mid-call usage if Claude CLI emits it.

### Phase 1: Wire Protocol Fix (harness + agent)

**Step 1** — Clean `TokenUsage` in `internal/harness/claude_cli.go`:

- `TokenUsage{Input, Output int64}` + `func (u TokenUsage) Total() int64`
- Drop `TotalTokens`, rename fields
- Update construction sites: `parseStream`, `RunPrint` envelope, `extractStreamUsage` (sandbox_cli_runner.go), `extractJSONUsage`, `query.go` Result
- Update `agent.StepMeta` field renames + add new fields (ModelDisplay, Provider, ContextWindow, CallCount)
- Update `agent.PlanResult.Usage` (pass-through renames)

**Files**: `internal/harness/claude_cli.go`, `internal/harness/sandbox_cli_runner.go`, `internal/harness/query.go`, `internal/agent/session.go`, `internal/agent/planner.go`

### Phase 2: RunUsage + BudgetGuard + RunSnapshot (_depends on 1_)

**Step 2** — Create `internal/orchestrator/usage.go` (~100 LoC):

- `AgentMeta`, `AgentSnapshot`, `RunSnapshot` types with JSON tags
- `RunUsage` struct with `Record`, `StartAgent`, `EndAgent`, `Snapshot`
- `RunSnapshot` helper methods: `Total()`, `AgentByID()`, `ContextPercent()`, `TokPerSec()`, `BudgetPercent()`

**Step 3** — Create `internal/orchestrator/budget.go` (~90 LoC):

- `BudgetGuard`, `ErrBudgetExhausted`, `budgetedRunner`
- `WrapRunner`, `WrapContinuable` methods

**Step 4** — Tests: `usage_test.go`, `budget_test.go` (~150 LoC):

- Record accumulates per agent correctly
- StartAgent/EndAgent lifecycle
- Snapshot returns consistent immutable copy
- Concurrent Record+Snapshot race test
- BudgetGuard: under/at/over limit, unlimited (limit=0)
- budgetedRunner: pre-check, record, post-check, inner error passthrough

**Files**: `internal/orchestrator/usage.go`, `internal/orchestrator/budget.go`, `internal/orchestrator/usage_test.go`, `internal/orchestrator/budget_test.go`

### Phase 3: Delete tokenlimit, Rewire Engine + main.go (_depends on 2_)

**Step 5** — Delete `internal/tokenlimit/` entirely (~1100 LoC, 5 files)

**Step 6** — Config: Replace per-model `token_limit` with `PipelineConfig.RunTokenBudget string` in `config.go`. Default `"2M"` in `pipeline.yaml`. Keep `ParseTokenLimit()` for parsing. Remove `ResolvedTokenLimits()`.

**Step 7** — Engine changes in `internal/orchestrator/engine.go`:

- Add `Usage *RunUsage` field to `Engine`
- Remove `InputTokens`/`OutputTokens` from `Event` struct (events.go) and ~20 emit calls
- Add `resolveAgentMeta(cfg, modelRef)` helper
- Before each agent: `e.Usage.StartAgent(agentID, resolveAgentMeta(cfg, model))`
- After each agent: `e.Usage.EndAgent(agentID, status)`
- At `EventComplete`: write `run_snapshot.json` artifact from `e.Usage.Snapshot()`
- Wire fan-out goroutine: on `entry.Stats.Valid`, call `e.Usage.Record(currentAgentID, ...)`

**Step 8** — main.go rewrite:

- Delete `openStore`, `openLimiter`, `resolveModelString`, old `wrapRunner`, `runUsage`, `runResetUsage`
- Delete `usage` and `reset-usage` subcommands (per-run scoping makes them obsolete)
- `buildEngine()`:
  ```
  usage := orchestrator.NewRunUsage(parseRunBudget(cfg))
  guard := orchestrator.NewBudgetGuard(usage)
  // Wrap ALL runners including worktree factory:
  Runners{
      Researcher: guard.WrapContinuable(researcherRunner, "researcher"),
      Architect:  guard.WrapContinuable(architectRunner, "architect"),
      Critic:     guard.WrapContinuable(criticRunner, "critic"),
      Worker:     guard.WrapContinuable(sandboxRunner, "worker"),
  }
  WorktreeRunnerFactory: func(path) {
      return guard.WrapContinuable(newSandboxRunner(path), "worker")
  }
  Engine{..., Usage: usage}
  ```

**Files**: `internal/tokenlimit/` (DELETE), `internal/config/config.go`, `internal/config/pipeline.yaml`, `internal/orchestrator/engine.go`, `internal/orchestrator/events.go`, `cmd/orqestra/main.go`

### Phase 4: TUI Reads StreamRing for Everything (_depends on 0, 3_)

**Step 9** — `internal/tui/model.go`:

- On `tickMsg`: poll `streamRing.Snapshot()` → store entries in `pipelineScreen`
- Extract latest `EntryStats` from entries for live metrics display
- Remove `AgentRow.InputTokens`/`OutputTokens`, `reviewTokensIn/Out`
- Remove token accumulation from `ApplyEvent`

**Step 10** — `internal/tui/screen_pipeline.go`:

- `viewSidebar()` renders from latest `EntryStats` per agent (from agent snapshots)
- `viewDashboard()` renders per-agent cards from agent entries (stats + tools)
- Delete `constDefaultContextWindow` from layout.go (context window comes from config via AgentMeta in RunSnapshot loaded from run_snapshot.json for historical, or from StreamRing stats for live)

**Files**: `internal/tui/model.go`, `internal/tui/screen_pipeline.go`, `internal/tui/layout.go`

### Phase 5: Historical Run Views Use RunSnapshot (_depends on 3_)

**Step 11** — `internal/tui/screen_run_detail.go`:

- Load `run_snapshot.json` via `orchestrator.LoadRunSnapshot(runPath)` (no import cycle — TUI imports orchestrator)
- Render step list from snapshot agents (includes model info, context %, tok/s)
- Fallback: if `run_snapshot.json` missing (old runs), derive minimal snapshot from `agent.RunDetail.Steps`

**Step 12** — Extract shared rendering helpers:

- Agent card rendering (context bar, tok/s, model name) used by both live dashboard and historical detail

**Files**: `internal/tui/screen_run_detail.go`, `internal/orchestrator/usage.go` (add `LoadRunSnapshot`)

### Phase 6: Cleanup (_depends on all_)

**Step 13** — `go mod tidy`: remove `modernc.org/sqlite` + 3 transitives
**Step 14** — Update orchestrator tests: `ErrBudgetExhausted` moves to orchestrator package
**Step 15** — Update `engine_test.go`: remove token fields from event assertions

**Files**: `go.mod`, `go.sum`, `internal/orchestrator/engine_test.go`

---

## Relevant Files

**New:**

- `internal/orchestrator/stream_ring.go` — StreamRing, StreamEntry, EntryStats (~150 LoC)
- `internal/orchestrator/stream_ring_test.go` — ring buffer tests (~100 LoC)
- `internal/orchestrator/usage.go` — RunUsage, RunSnapshot, AgentMeta, AgentSnapshot (~120 LoC)
- `internal/orchestrator/budget.go` — BudgetGuard, ErrBudgetExhausted, budgetedRunner (~90 LoC)
- `internal/orchestrator/usage_test.go` — RunUsage tests (~70 LoC)
- `internal/orchestrator/budget_test.go` — BudgetGuard tests (~80 LoC)

**Deleted:**

- `internal/orchestrator/stream_buffer.go` — replaced by stream_ring.go
- `internal/orchestrator/stream_buffer_test.go` — replaced by stream_ring_test.go
- `internal/tokenlimit/runner.go` — LimitedRunner (~100 LoC)
- `internal/tokenlimit/store.go` — SQLite store (~120 LoC)
- `internal/tokenlimit/limiter.go` — Limiter + ErrBudgetExhausted (~100 LoC)
- `internal/tokenlimit/runner_test.go` + `store_test.go` (~780 LoC)

**Modified:**

- `internal/harness/claude_cli.go` — TokenUsage field rename, drop TotalTokens, add Total(), push EntryStats on Usage != nil
- `internal/harness/sandbox_cli_runner.go` — field renames in extraction
- `internal/harness/query.go` — field renames
- `internal/orchestrator/engine.go` — Usage field, StartAgent/EndAgent, fan-out goroutine, remove event token fields, write run_snapshot.json
- `internal/orchestrator/events.go` — remove InputTokens/OutputTokens from Event
- `internal/config/config.go` — RunTokenBudget, remove ResolvedTokenLimits
- `internal/config/pipeline.yaml` — run_token_budget default
- `cmd/orqestra/main.go` — delete 6 functions, rewrite buildEngine, delete usage/reset-usage subcommands
- `internal/agent/session.go` — StepMeta new fields
- `internal/agent/planner.go` — TokenUsage field renames (pass-through)
- `internal/tui/model.go` — poll StreamRing on tick, remove AgentRow token fields
- `internal/tui/screen_pipeline.go` — render from StreamRing entries, remove token accumulation from ApplyEvent
- `internal/tui/screen_run_detail.go` — load + render from RunSnapshot
- `internal/tui/layout.go` — remove constDefaultContextWindow
- `go.mod` — remove modernc.org/sqlite + transitives

## Verification

1. `go build ./...`
2. `go test -race ./internal/orchestrator/` — stream_ring, usage, budget, engine
3. `go test -race ./internal/harness/` — TokenUsage changes, EntryStats dispatch
4. `go test -race ./internal/tui/` — render from ring entries, no token fields on events
5. `go test -race ./...` — full suite
6. `make test`
7. `grep -r 'tokenlimit\|TotalTokens\|modernc.org' internal/ cmd/ go.mod` — zero
8. Smoke: `./bin/orqestra --prompt "test" --auto-approve --config orqestra.yaml`
9. Historical: complete a run, navigate to Ctrl+R → run detail → verify model info renders from run_snapshot.json

## Decisions

- **StreamRing is TUI's sole data source** — text, tools, AND stats in one unified ring. No separate polling of RunUsage. No split data sources.
- **RunUsage is for budget enforcement only** — BudgetGuard reads it for kill-switch. TUI never touches it.
- **Zero-value safety** — `EntryStats{Valid bool}` pattern instead of `*streamUsage` pointers. No aliasing across goroutines.
- **Channel as internal transport** — `channelWriter` (io.Writer + ActivitySink) → buffered chan → fan-out goroutine → ring + RunUsage. CLIRunner interface unchanged.
- **RunSnapshot is the universal historical format** — same type for live (ring snapshot → derive totals) and historical (JSON load). Shared rendering code.
- **run_snapshot.json is the new artifact** — written at completion, loaded by historical views. Individual \*\_meta.json still written for Claude session references.
- **Per-model token_limit → per-run RunTokenBudget** — budget scoped to run, not model. Simpler config, no cross-run persistence.
- **usage/reset-usage subcommands deleted** — per-run scoping makes them obsolete.
- **Backward compat for old runs** — TUI derives minimal snapshot from StepMeta when run_snapshot.json missing.

## Import Cycle Resolution

`orchestrator → agent` (engine.go imports agent). `agent` does NOT import `orchestrator`.

`RunSnapshot`/`AgentSnapshot`/`AgentMeta` live in `orchestrator`. `agent.LoadRunDetail` cannot return `RunSnapshot` without creating a cycle.

**Resolution**: TUI loads `run_snapshot.json` directly via `orchestrator.LoadRunSnapshot(path)`. `agent.LoadRunDetail` continues to load `StepMeta` (for Claude session references). TUI combines both: `RunSnapshot` for metrics/rendering, `RunDetail.Steps` for session log links.

---

## LoC Impact: ~-850 net

| Action                                          | LoC       |
| ----------------------------------------------- | --------- |
| Delete internal/tokenlimit/                     | -1100     |
| Create stream_ring.go + tests                   | +250      |
| Delete stream_buffer.go + tests                 | -200      |
| Create usage.go + budget.go + tests             | +340      |
| Remove Event token fields + emit calls          | -40       |
| Delete 6 functions + 2 subcommands from main.go | -100      |
| Remove TUI token accumulation                   | -15       |
| Harness/agent renames                           | +35       |
| RunSnapshot persistence + loading               | +30       |
| Channel adapter + fan-out                       | +50       |
| **Net**                                         | **~-750** |

Plus removal of 4 go.mod dependencies (modernc.org/sqlite + 3 indirect).

---

## Part 2: TUI Dashboard Enrichment (additions to plan-tuiStatusDashboard.prompt.md)

The existing TUI dashboard plan covers: agent model metadata, single-line status bar with animation, dashboard card rewrite, session artifact persistence.

### Missing Features To Add

**1. Streaming Logs Pane (ContentStreamingLogs)**

- New `ContentMode` value: `ContentStreamingLogs`
- Full-screen scrollable view of the StreamRing content (currently only 5 preview lines in `viewStreaming`)
- Shows all entries interleaved (text + tools + stats) — unified stream view
- Toggle: Ctrl+L or expand from streaming view
- Auto-follows bottom when not user-scrolled
- Works for current agent; Alt+1-9 switches agent → shows snapshot from `StreamRing.AgentEntries()`

**2. Artifact Viewer (ContentArtifactView)**

- New `ContentMode` value: `ContentArtifactView`
- Shows input prompt (markdown rendered) and output markdown for each agent
- Data sources:
  - Input: the prompt assembled by the orchestrator (store as artifact: `researcher_prompt.md`, `architect_prompt.md`, etc.)
  - Output: plan markdown from `ReadPlanFromRun` (already stored as `final_plan.md`, `researcher_draft.md`)
- Navigation: Tab to switch between input/output panes
- Toggle: Ctrl+I from streaming or dashboard view
- For historical views: same artifacts loaded from session directory

**3. Dialog + Plan Progression View (enrich existing ContentPlanReview)**

- Current `viewPlanReview` shows flat chat history + final plan
- Enrich to show:
  - Revisions list sidebar (from plan-history git micro-repo, already wired via Ctrl+Y)
  - Inline diff between any two revisions (not just last)
  - Human comments contextually paired with the revision they triggered
  - Token cost per revision (from RunSnapshot agent calls with CallCount)
- Data: `plan.GitRepo` already tracks revisions. Need to pair `ChatEntry` with revision SHAs (store SHA in ChatEntry when a plan revision occurs)

**4. Worker Git Diff View (ContentWorkerDiff)**

- New `ContentMode` value: `ContentWorkerDiff`
- Shows `git diff` of worker changes (worktree diff or commit diff)
- Data source: after worker completes, run `git diff` in the worktree or against the merge commit
- Persist as `worker_diff.txt` artifact for historical views
- Toggle: Ctrl+G during/after worker phase
- For merge conflicts: overlay on the existing `ContentMergeConflict` view

**5. Historical Run Dashboard Parity**

- `RunDetailScreen` currently shows: prompt, plan, step list (agent/status/duration/tokens), step log
- Enrich to match live dashboard: model info, context %, tok/s per agent (data from `run_snapshot.json`)
- Add artifact viewer for historical runs: load `researcher_prompt.md`, `researcher_draft.md`, `final_plan.md`, `worker_output.txt` from session directory
- Add worker diff for historical runs: load `worker_diff.txt`

### New Artifacts to Persist

| Artifact            | Written By | When                   | Contents                                               |
| ------------------- | ---------- | ---------------------- | ------------------------------------------------------ |
| `run_snapshot.json` | Engine     | EventComplete          | Full RunSnapshot (tokens, model info, per-agent stats) |
| `{agent}_prompt.md` | Engine     | Before each agent call | Assembled prompt sent to Claude                        |
| `worker_diff.txt`   | Engine     | After worker completes | `git diff` of worker changes                           |
| `dialog.json`       | Engine     | At each gate iteration | Chat entries paired with revision SHAs                 |

### Data Model Additions for TUI

`ChatEntry` gains:

- `RevisionSHA string` — set when the entry triggered a plan revision
- `TokenCost harness.TokenUsage` — tokens consumed for this architect call

`PipelineScreen` gains:

- Content mode state for new views (streaming logs VP, artifact VPs, worker diff VP)
- Latest stats derived from StreamRing entries (not from RunUsage)

### New Keybindings

| Key    | Action                                | Availability               |
| ------ | ------------------------------------- | -------------------------- |
| Ctrl+L | Toggle full streaming logs            | During pipeline execution  |
| Ctrl+I | Toggle artifact viewer (input/output) | Any agent selected         |
| Ctrl+G | Toggle worker git diff                | Worker phase or completion |

### Phase Dependencies

All TUI enrichment phases depend on Phase 0 (StreamRing exists) and Phase 3 (RunSnapshot types exist for historical views).

The artifact persistence (prompt.md, worker_diff.txt, dialog.json) can be implemented in parallel with the existing TUI dashboard plan phases.

### Verification (additions)

1. Ctrl+L: full stream log scrollable, auto-follow, agent switch
2. Ctrl+I: shows assembled prompt and output for each agent
3. Ctrl+G: shows worker diff during execution and after completion
4. Historical run: model info, context %, tok/s visible in step list
5. Historical run: artifact viewer loads from session directory
6. Plan progression: revisions paired with comments, diff between any two
