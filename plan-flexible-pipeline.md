# Plan — Flexible Pipeline v3

> **Purpose:** Adapt the flexible-pipeline design to the current codebase reality.
> Every claim below is grounded in direct evidence from the running code.
>
> **This file was rewritten as the v5 critic-resolved revision.** Earlier drafts
> carried three contradictory layers (a v3 header that created `internal/pipeline/`,
> a v4 "Simplification Revision" that rejected it *and* split the gate, and Parts 0–8
> that used both). A Critic Report raised 10 blockers from those contradictions.
> The document now specifies **one** architecture end to end.

---

## Authoritative Revision (v5) — read this first

Two architectural decisions are **locked** and propagated through every part below.
One agrees with the prior v4 note; one overrides it.

1. **`internal/pipeline/` is eliminated.** All shared config/value types live in
   `internal/orchestrator/`. There is no new leaf package and no import cycle.
2. **`runGateLoop` is unified across all gate positions.** A single loop handles
   research / deliberation / execution / validation gates. There is **no**
   `planGate`/`pauseGate` split and **no** `revisionFn`/`chatFn` callback union.

### Why a unified `runGateLoop` is NOT the banned "flattened sum type"

The banned pattern (`<banned_patterns>` in CLAUDE.md) targets a **struct** that
holds the union of every variant's fields plus `hasX`/tag booleans (screen modes,
pipeline phases). The earlier unified loop smelled because it took two **nullable
function pointers** (`revisionFn==nil`/`chatFn==nil`) whose nilness encoded the
gate variant — that union is removed here. A *function* that dispatches on an enum
(`pos`) and on `Decision.Type` is ordinary Go control flow. The loop is
self-contained: it derives behavior from `pos` via a single `pos.IsPlanGate()`
predicate and selects its runner internally. `GateRequest` carries plan fields that
are simply empty for pause gates — exactly how the existing `Event` struct already
sets most fields per `EventType`. No new illegal state becomes representable.

### Blocker resolution index

| # | Blocker (from Critic Report) | Resolution | Part |
|---|------------------------------|------------|------|
| 1 | `internal/pipeline/` created vs rejected | Eliminate; types → `orchestrator/{setup,gate}.go`; break both couplings | 2, 3, 4 |
| 2 | Unified loop vs split gate | Keep unified; remove nil callbacks; dispatch on `pos` | 3 |
| 3 | `RestartPhase` typed-field vs plain-string return | `RunCompleteness.RestartPhase` is a plain `string` in `agent`; `orchestrator.RestartPhase` is a string-backed type used by `orchestrator`/`tui` only | 3, 4 |
| 4 | `revisionFn` return-type mismatch | Dissolved — loop calls `planner.Continue` directly, reads `PlanResult.{Plan,Chat,SessionID}` | 3 |
| 5 | `startQuestionBridge` defer placement | Helper starts bridge + goroutine, returns nothing; `defer Stop()` stays in `run()`, nil-guarded | 1 |
| 6 | Session continuation vs Risk #8 fresh sessions | Keep continuation; architect session threads deliberation → gate via `planSessionID`; delete fresh-session claim | 1, 3, Risks |
| 7 | `Event.Gate`→`Event.HumanGate` breaks TUI | Do not rename; reuse `EventGateRequest`/`GateRequest`/`Event.Gate`; add `Position`, drop git fields | 3, 6 |
| 8 | `screen_pipeline.go` churn | Reusing the event shrinks the blast radius; remaining edits enumerated | 6 |
| 9 | `RestartRunIntent.Loop` dropped but still present | Remove `Loop`; `RestartRunIntent{RunPath, Phase}` only; no mid-deliberation restart | 4, 6 |
| 10 | `run()` return type vs `Start()` goroutine | `run()` keeps its 6 params, returns `(Result, error)`; `Start()` captures it and emits terminal events | 1 |

---

## Part 0: Current Codebase State (Evidence)

Verified this session against the actual codebase.

### 0.1 Orchestrator (`internal/orchestrator/`)

| Item | Exists? | Evidence |
|------|---------|----------|
| `engine.go` | Yes | 1,638 lines (`wc -l`) |
| `run()` function | Yes | Lines 295–1513 (after the helper functions) |
| `runPlanner()` / `continuePlanner()` | Yes | Lines 229 / 235 — used by the gate revision path |
| `runRunnerStreaming()` / `runRunnerContinue()` | Yes | Lines 241 / 271 |
| `runDeliberation/runResearch/runExecution/runValidation` | **No** | All inline in `run()` — to be extracted |
| inline plan gate loop | Yes | Lines 886–1207 — becomes the unified `runGateLoop` |
| `Input` struct | Yes | Lines 30–36: `Prompt`, `AutoApprove`, `PlanFile`, `NoExecute`, `RestartFrom` |
| `Input.Setup` | **No** | To be added (`PipelineSetup`, **in `orchestrator`**) |
| `Input.AutoApprove` / `PlanFile` / `NoExecute` | Yes | To be removed (headless removal, Part 7) |
| `RestartInput` struct | Yes | Lines 24–27: `RunPath`, `FirstMissingAgent` — to become `{RunPath, Phase}` |
| `PipelineSetup` / `HumanGateSet` / `HumanGatePosition` / `RestartPhase` | **No** | To be added **in `orchestrator`** (NOT `internal/pipeline/`) |
| `EventGateRequest` / `GateRequest` / `Event.Gate` | Yes | **Reused** (add `Position`, drop git/`GateType`/`CriticReport`) |
| `EventHumanGate` / `HumanChatGateRequest` / `DecisionChat` | **No** | **Not added** — reuse the existing gate event |
| `EventAgentSkipped` / `PhaseDeliberating` | **No** | To be added |
| `planRepo *plan.GitRepo` | Yes | Line 449; **37** refs in `engine.go` — to be removed (Part 5) |
| `copyCompletedArtifacts()` | Yes | Line 1577 |
| `Runners` struct | Yes | Lines 66–71: `Researcher`, `Architect`, `Critic`, `Worker` all `harness.Runner` |
| `Engine` struct | Yes | Line 92: holds `Config *config.Config`, `Runners`, `QuestionBridge *mcp.QuestionBridge` |

### 0.2 Harness / Planner (`internal/harness/`, `internal/agent/`)

| Item | Exists? | Evidence |
|------|---------|----------|
| `harness.Runner` interface | Yes | `Post/Receive/ExtractPlan/SetEvents/SessionID/Cancel` (final form) |
| `agent.Planner` | Yes | `planner.go` — `NewPlanner(runner, system)`, `Run`, `Continue` |
| `Run(ctx, prompt, events) (PlanResult, error)` | Yes | `planner.go:48` |
| `Continue(ctx, sessionID, prompt, events) (PlanResult, error)` | Yes | `planner.go:99` |
| `PlanResult{Plan, Chat, Usage, SessionID, StreamFallback}` | Yes | `planner.go:12` |
| `DetectPlanRevision(planContent, baseline, baselineErr, currentPlan) *RawPlan` | Yes | `planner.go:140` — **output-based** revision/chat detection |
| `ContinuePrompt(currentPlan, comment)` / `CriticContinuePrompt(currentPlan, report)` | Yes | `prompts.go:20` / `prompts.go:58` |

> Runner/Planner consolidation is **out of scope**. This plan changes orchestrator
> structure, gates, config, and session layout only.

### 0.3 Agent Session (`internal/agent/`)

| Item | Exists? | Evidence |
|------|---------|----------|
| `SessionDir{Path string}` | Yes | `session.go:14` |
| `ArtifactPath` / `WriteArtifact` / `ReadArtifact` | Yes | `session.go:33/38/47` |
| `session.go` imports | **stdlib only** | `encoding/json fmt os path/filepath sort strings time` — the fact that makes the cycle avoidable |
| `SubDir` / `ResearchDir` / `DeliberationDir` / `LoopDir` / `ExecutionDir` / `ValidationDir` | **No** | To be added (gate-agnostic, no enum param) |
| `NewSessionDir` uses `os.MkdirAll` | Yes | `session.go:27` — change to `os.Mkdir` + EEXIST |
| `RunCompleteness.FirstMissingAgent` | Yes | `session.go:114` — replace with `RestartPhase string` |
| `AnalyzeRunCompleteness(runPath string, detail RunDetail) RunCompleteness` | Yes | `session.go:312` — drop `detail`; sole caller `screen_run_detail.go:55` |
| `KnownAgents` | Yes | `["researcher","architect","critic","worker"]` |
| `CopySessionLog` / `harness.ResolveSessionLogPath` | Yes | `session.go:235` / `harness/logpath.go:19` |

### 0.4 Events (`internal/orchestrator/events.go`)

| Item | Exists? | v5 action |
|------|---------|-----------|
| `EventPhaseChange/AgentStarted/AgentDone/AgentFailed/AgentCancelled/AgentOutput` | Yes | keep |
| `EventPlanReady` | Yes | **keep** (TUI handles it; emitted for plan gates) |
| `EventGateRequest` | Yes | **keep & reuse** |
| `EventChatResponse` + `Event.ChatText` | Yes | keep |
| `EventComplete/Error/RunDirReady/UserQuestion/MergeConflict/MergeError` | Yes | keep |
| `EventAgentSkipped` / `PhaseDeliberating` | **No** | add |
| `GateType` enum (`GatePlanApproval` only) | Yes | **remove** |
| `GateRequest` fields `PlanDiff/PlanHistoryDir/PlanHistoryHeadSHA` | Yes | **remove** (git micro-repo, Part 5) |
| `GateRequest.CriticReport` | Yes | **remove** (never read by TUI — verified) |
| `GateRequest.{FinalPlanMarkdown, PlanFilePath, PlanWarnings}` | Yes | keep |
| `GateRequest.Position HumanGatePosition` | **No** | add |
| `Decision{Type, EditedContent, Comment, AutoApprove}` | Yes | keep |
| `DecisionType` (`Approve/Edit/Skip/Cancel/Comment/MergeAbort`) | Yes | keep |
| `DecisionChat` | **No** | **not added** (revision/chat is output-based) |

### 0.5 TUI (`internal/tui/`)

| Item | Exists? | v5 action |
|------|---------|-----------|
| `ContentPlanReview` | Yes | reuse for rich gate; pause gate renders a minimal confirm |
| `ApplyEvent` `case EventGateRequest` switches `event.Gate.Type` | Yes (~397–433) | switch on `event.Gate.Position` |
| `event.Gate.PlanDiff/PlanHistoryDir/PlanHistoryHeadSHA` refs | Yes (~400–425) | remove |
| `AgentStateGate` | Yes (line 52) | keep; add `AgentStateSkipped` |
| Status-line state switch | Yes (~818–832) | add `case AgentStateSkipped` |
| `planHistoryScreen`, `StatePlanHistoryDetail`, `ContentPlanHistory` | Yes | **remove** (Part 5.5) |
| `plan_history_loader.go` / `screen_plan_history.go` / `screen_plan_history_test.go` / `plan_history_model_test.go` | Yes | **delete** |
| `OpenPlanHistoryIntent` / `ClosePlanHistoryIntent` / `RevertPlanIntent` | Yes | remove (`messages.go`) |
| `Ctrl+Y` handlers | Yes (`screen_pipeline.go:701`, `screen_run_detail_keys.go:20`) | remove |
| `RestartRunIntent.FirstMissingAgent` | Yes | replace with `Phase RestartPhase`; **no `Loop`** |
| `ApprovePlanIntent/CommentPlanIntent/ConfirmEditIntent/CancelPlanIntent` → `Decision` | Yes (`model.go:554–593`) | keep (no new intents) |
| `screen_pipeline.go` size | 1,530 lines, ~40 fields | stays large — only enumerated handlers edited |

### 0.6 CLI / Headless (`cmd/orqestra/main.go`, `internal/tui/tui.go`)

Headless mode is removed (Part 7): drop `--prompt/--auto-approve/--auto-reject/
--auto-init/--plan/--no-execute/--json` and `isHeadless`; delete `RunHeadless`,
`RunHeadlessPlanOnly`, `runPlanOnly`, `runValidateOnly`, `runExecOnly`. Keep
`--config`. `Input` loses `AutoApprove/PlanFile/NoExecute`.

### 0.7 Plan Package (`internal/plan/`) — git micro-repo to be removed (Part 5)

`gitrepo.go` (242 lines) and `gitrepo_history.go` (129 lines) deleted; replaced by
numbered `plan-v<N>.md` files and per-gate `dialog.md`. `spec.go` stays.

### 0.8 Config (`internal/config/`)

`PipelineConfig` (global: `TokenBudget/RunDir/WorkerConcurrency`) and
`ExecutionGraphConfig`/`BuildGraph()` (scheduler, separate/legacy) are **unrelated**
to the new `PipelineSetup`. `PipelineSetup` is a TUI-only concept, not wired from
YAML. Agent system prompts read from `e.Config.{Researcher,Architect,Critic}.SystemPrompt`.

> **There is no `internal/pipeline/` package, and none is created.** The earlier
> §0.9 (which introduced it to break a self-inflicted cycle) is deleted.

---

## Part 1: Engine Extraction — Coordinator Style

**Problem:** `engine.go` is 1,638 lines; `run()` is ~1,218 lines (295–1513) and owns
all control flow, goroutines, the `decisions` channel, and the big `select` loop.

**Goal:** `run()` becomes a thin coordinator returning `(Result, error)`; phases and
the gate loop move to sibling files (every file < 500 lines).

### 1.1 `run()` signature and the `Start()` goroutine (Blocker 10)

`run()` keeps **all six parameters** (so `emit`, `decisions`, `stream`, `streamOut`
stay in scope for the coordinator and phases) and changes only its return type:

```go
func (e *Engine) run(
    ctx context.Context, input Input,
    events chan<- Event, decisions <-chan Decision,
    stream *streamCapture, streamOut chan<- harness.Event,
) (Result, error)
```

`run()` **stops emitting** the terminal `EventComplete`/`EventError` (currently
`engine.go:1503–1512`) and returns `Result{Status, FinalPlan, WorkerValidation,
RunDir}`. The `Start()` goroutine (currently `engine.go:146–150`, which discards no
value because `run()` self-emits) captures the return and emits the terminal events:

```go
go func() {
    defer close(events)
    defer close(rawStream)
    result, err := e.run(ctx, input, events, decisions, capture, rawStream)
    if err != nil && !errors.Is(err, context.Canceled) {
        select { case events <- Event{Type: EventError, Err: fmt.Errorf("run: %w", err)}: case <-ctx.Done(): }
    }
    select {
    case events <- Event{Type: EventComplete,
        Status: result.Status, FinalPlan: result.FinalPlan,
        WorkerValidation: result.WorkerValidation, RunDir: result.RunDir}:
    case <-ctx.Done():
    }
}()
```

`Run()` (the legacy synchronous wrapper, `engine.go:156`) is unchanged — it already
reads `EventComplete`/`EventError` off the channel.

### 1.2 Coordinator `run()` structure

```go
emit := func(ev Event) { select { case events <- ev: case <-ctx.Done(): } }

setup := resolveSetup(input)                 // §2.3 defaults + validation
session, err := e.openSession(input)         // §4.5 os.Mkdir; copies completed artifacts on restart
if err != nil { return Result{}, err }
rl := &runLog{logger: slog.Default(), agentStart: map[string]time.Time{}, session: session} // AFTER session exists
emit(Event{Type: EventRunDirReady, RunDir: session.Path})
writeArtifactIn(session, "", "prompt.md", input.Prompt) // input.Prompt is the real prompt (TUI loads prompt.md on restart)
writeRunConfig(session, setup)               // §4.3 run_config.json

e.startQuestionBridge(ctx, emit)             // §1.6
if e.QuestionBridge != nil { defer e.QuestionBridge.Stop() }

rs, err := applyRestartSkip(session, input)  // §4 / Part-4 restart, phase-level
if err != nil { return Result{}, err }

// Research (once)
var draft, researchSID string
if setup.Research && !rs.SkipResearch {
    draft, researchSID, err = e.runResearch(ctx, setup, session, input, emit, stream, streamOut, rl)
    if err != nil { return Result{}, fmt.Errorf("research: %w", err) }
} else if !setup.Research {
    emit(Event{Type: EventAgentSkipped, AgentID: "researcher"})
    draft = rs.SeedDraft // "" when research is simply disabled → architect plans from input.Prompt alone (no double-feed)
} else { draft = rs.SeedDraft } // restart skipped research → seeded from research/researcher_draft.md

if setup.HumanGates.Active(GateAfterResearch) {
    _, draft, researchSID, err = e.runGateLoop(ctx, emit, decisions,
        GateAfterResearch, session, draft, "", nil, researchSID, stream, streamOut, rl)
    if err != nil { return cancelledOrErr(err, session) }
}

// Deliberation (1..N loops, one architect session)
var del deliberationResult
if !rs.SkipDeliberation {
    del, err = e.runDeliberation(ctx, setup, session, input, emit, stream, streamOut, rl, draft)
    if err != nil { return Result{}, fmt.Errorf("deliberation: %w", err) }
} else {
    del = deliberationResult{PlanMarkdown: rs.SeedPlanMarkdown} // restart: no live session
}
finalPlan, planSID := del.PlanMarkdown, del.PlanSessionID

if setup.HumanGates.Active(GateAfterDeliberation) {
    _, finalPlan, planSID, err = e.runGateLoop(ctx, emit, decisions,
        GateAfterDeliberation, session, finalPlan,
        findHighestPlan(session.DeliberationDir()), del.Warnings, planSID, stream, streamOut, rl)
    if err != nil { return cancelledOrErr(err, session) }
}
writeArtifactIn(session, "", "final_plan.md", finalPlan)

// Execution
var workerSID string
if setup.Execution && !rs.SkipExecution {
    _, workerSID, err = e.runExecution(ctx, setup, session, input, emit, stream, streamOut, rl, finalPlan)
    if err != nil { return Result{}, fmt.Errorf("execution: %w", err) }
} else if !setup.Execution {
    emit(Event{Type: EventAgentSkipped, AgentID: "worker"})
}
if setup.HumanGates.Active(GateAfterExecution) && setup.Execution {
    if _, _, _, err = e.runGateLoop(ctx, emit, decisions,
        GateAfterExecution, session, "", "", nil, "", stream, streamOut, rl); err != nil {
        return cancelledOrErr(err, session)
    }
}

// Validation (continues the worker session)
var validation string
if setup.Validation && setup.Execution {
    validation, _, err = e.runValidation(ctx, setup, session, input, emit, stream, streamOut, rl, workerSID)
    if err != nil { slog.Warn("validation failed, proceeding", "err", err) }
} else if !setup.Validation {
    emit(Event{Type: EventAgentSkipped, AgentID: "validator"})
}
if setup.HumanGates.Active(GateAfterValidation) && setup.Execution {
    if _, _, _, err = e.runGateLoop(ctx, emit, decisions,
        GateAfterValidation, session, "", "", nil, "", stream, streamOut, rl); err != nil {
        return cancelledOrErr(err, session)
    }
}

// Post-run worktree commit + merge (existing logic; moved to engine_phases.go).
// GUARDED on execution: no worker → no worktree → no merge.
status := StatusSuccess
if setup.Execution && !rs.SkipExecution {
    status = e.commitAndMerge(ctx, session, emit) // existing merge-outcome logic
}
emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
return Result{Status: status, FinalPlan: finalPlan, WorkerValidation: validation, RunDir: session.Path}, nil
```

`cancelledOrErr` maps the gate sentinel `errGateCancelled` to a clean cancelled
`Result` and any other error to `(Result{}, err)`. **Pause-gate cancel is special:**
when `errGateCancelled` comes from `GateAfterExecution`/`GateAfterValidation` the
worker has already produced a worktree with commits — the coordinator must **skip the
merge, preserve the worktree branch, and surface its name** (emit an
`EventMergeError`-style branch notice, mirroring `DecisionMergeAbort`) before returning
`Status = cancelled`. Cancel at a pre-execution gate has no worktree and just returns
cancelled.

### 1.3 `deliberationResult` (in `engine_deliberation.go`)

```go
type deliberationResult struct {
    PlanMarkdown      string
    CriticReport      string
    Warnings          []string
    PlanRevisionCount int
    PlanSessionID     string // architect session carried OUT (Blocker 6)
}
```

### 1.4 Phase functions (`engine_phases.go`, `engine_deliberation.go`)

Extract as `*Engine` methods taking `setup PipelineSetup` (an `orchestrator` type),
`session agent.SessionDir`, `emit`, `stream`, `streamOut`, `rl *runLog`. `ctx` stays
a parameter (banned-patterns: no `ctx` in structs).

```go
func (e *Engine) runResearch(ctx, setup, session, input, emit, stream, streamOut, rl) (draft, sessionID string, err error)
func (e *Engine) runDeliberation(ctx, setup, session, input, emit, stream, streamOut, rl, draft string) (deliberationResult, error)
func (e *Engine) runExecution(ctx, setup, session, input, emit, stream, streamOut, rl, plan string) (harness.RunResult, workerSessionID string, err error)
func (e *Engine) runValidation(ctx, setup, session, input, emit, stream, streamOut, rl, workerSessionID string) (output, lastSessionID string, err error)
```

**`runDeliberation` body (locked shape — one architect session via `Continue`):**

```go
res, _ := runPlanner(ctx, architectPlanner, agent.ArchitectPrompt(input.Prompt, draft), stream, streamOut)
planSID, planMarkdown, revN := res.SessionID, res.Plan, 1
writeArtifactIn(session, "deliberation", "plan-v1.md", planMarkdown) // initial pass → deliberation root
for round := 1; round <= setup.DeliberationLoops; round++ {
    crit := runPlanner(ctx, criticPlanner, agent.CriticReviewPrompt(input.Prompt, planMarkdown), …)
    writeArtifactIn(session, "deliberation/loop_"+R, "critic_report.md", crit.Plan)
    rev := continuePlanner(ctx, architectPlanner, planSID, agent.CriticContinuePrompt(planMarkdown, crit.Plan), …)
    planSID, revN = rev.SessionID, revN+1
    writeArtifactIn(session, "deliberation/loop_"+R, fmt.Sprintf("plan-v%d.md", revN), rev.Plan)
    planMarkdown = rev.Plan
}
return deliberationResult{PlanMarkdown: planMarkdown, Warnings: …, PlanSessionID: planSID, PlanRevisionCount: revN}
```

`DeliberationLoops = 1` ≡ today (one critic + one revision). `draft` may be empty when
Research is disabled — the architect then plans from `input.Prompt` alone. `plan-v<N>`
is numbered **globally**: `v1` = initial pass (deliberation root), `v2..v(N+1)` = each
round's revision (`loop_<R>/`).

> Bundling these into a `runState` struct is an acceptable later refinement; it is
> orthogonal to the two locked decisions and out of scope here. Pass explicit params.

### 1.5 Logging state + question bridge (Blocker 5)

Lift the four logging closures (`engine.go:303,356,384,395`) onto a `runLog` struct
in `run_log.go`; lift `copyLog` to package-level `copySessionLog(...)`:

```go
type runLog struct { logger *slog.Logger; agentStart map[string]time.Time; session agent.SessionDir }
func (l *runLog) agentEvent(event, agentID string, attempt int, usage harness.TokenUsage, err error)
func (l *runLog) claudeSession(agentID string, attempt int, sessionID, sessionLogCopy string)
func (l *runLog) claudeSessionPre(agentID string, attempt int, sessionID string)
func copySessionLog(s agent.SessionDir, repoPath, sessionID, destName string) (string, error) // wraps agent.CopySessionLog + harness.ResolveSessionLogPath
```

Extract the inline question-bridge goroutine (`engine.go:412–428`) to a helper that
**returns nothing** — the `defer Stop()` stays in `run()` (nil-guarded, §1.2):

```go
// startQuestionBridge starts the bridge and its EventUserQuestion forwarding
// goroutine. No-op when e.QuestionBridge == nil.
func (e *Engine) startQuestionBridge(ctx context.Context, emit func(Event))
```

### 1.6 Package-level helpers (`engine_phases.go`)

```go
func writeArtifactIn(session agent.SessionDir, subdir, name, content string) (path string)
func writeArtifactJSONIn(session agent.SessionDir, subdir, name string, v any) error
func appendDialog(dir, role, message string)                 // §5.3 dialog.md
func findHighestPlan(dir string) string                       // path of highest plan-v*.md (walks subdirs; deliberation final lives in the last loop_NN/)
func highestPlanVersion(dir string) int                       // max N in plan-v<N>.md (0 if none)
```

### Impact

| File | Before | After |
|------|--------|-------|
| `engine.go` | 1,638 | ~300 (coordinator) |
| `engine_deliberation.go` | — | ~300 |
| `engine_phases.go` | — | ~400 |
| `engine_restart.go` | — | ~150 |
| `run_log.go` | — | ~80 |
| `human_gate.go` | — | ~250 (unified loop) |
| `setup.go` / `gate.go` | — | ~80 / ~80 |

---

## Part 2: Configurable Pipeline Setup

**Problem:** Pipeline phases are hardcoded; no toggles, no configurable loop count.

### 2.1 `PipelineSetup` (in `internal/orchestrator/setup.go`)

Types live in **`orchestrator`**, not a new package. `agent` never imports them
(couplings broken in Part 4).

```go
package orchestrator

type PipelineSetup struct {
    Research          bool
    DeliberationLoops int          // 1..10; 0 → 1
    Execution         bool
    Validation        bool
    HumanGates        HumanGateSet // from gate.go
}

func DefaultPipelineSetup() PipelineSetup {
    return PipelineSetup{
        Research: true, DeliberationLoops: 1, Execution: true, Validation: true,
        HumanGates: HumanGateSet{GateAfterDeliberation},
    }
}

func (p PipelineSetup) Validate() error {
    if p.DeliberationLoops < 1 || p.DeliberationLoops > 10 {
        return fmt.Errorf("deliberation_loops must be 1..10, got %d", p.DeliberationLoops)
    }
    if !p.Research && !p.Execution && !p.Validation {
        return fmt.Errorf("at least one of Research, Execution, Validation must be enabled")
    }
    return nil
}
```

**Loop semantics:** research runs once. The architect makes **one** initial pass
(`architect.Run` → `plan-v1`), then the deliberation runs `DeliberationLoops` rounds,
each `critic.Run` → `architect.Continue` (revision) on the **same** architect session.
`DeliberationLoops = 1` is exactly today's behavior (one critic + one revision).
`plan-v<N>.md` is numbered **globally** across the deliberation (v1 = initial,
v2..v(N+1) = each revision); the highest is the final plan. Default is **1**.

### 2.2 `Input` struct (`engine.go`)

```go
type Input struct {
    Prompt      string
    Setup       PipelineSetup
    RestartFrom RestartInput
}
```

(`AutoApprove`, `PlanFile`, `NoExecute` removed — headless removal, Part 7.)

### 2.3 Defaults + validation in `run()`

```go
func resolveSetup(in Input) PipelineSetup {
    s := in.Setup
    if s == (PipelineSetup{}) { return DefaultPipelineSetup() }
    if s.DeliberationLoops == 0 { s.DeliberationLoops = 1 }
    return s
}
// run(): setup := resolveSetup(input); if err := setup.Validate(); err != nil { return Result{}, fmt.Errorf("pipeline setup: %w", err) }
```

### 2.4 Wiring

`PipelineSetup` is configured **only** through the TUI setup panel (Part 6.1), not
CLI flags (headless removed) and not YAML (`ExecutionGraphConfig` is scheduler/legacy).
`internal/tui/model.go` adds `setupOpen bool`, `currentSetup orchestrator.PipelineSetup`.

---

## Part 3: Human Gates — Unified `runGateLoop`

**Problem:** the plan gate is hardcoded and inline (886–1207); no configurable gate
positions.

### 3.1 Gate types (in `internal/orchestrator/gate.go`)

Gate positions use **"After"** semantics — each gate fires after its phase, reviewing
that phase's output.

```go
package orchestrator

type HumanGatePosition int
const (
    GateAfterResearch HumanGatePosition = iota // researcher draft
    GateAfterDeliberation                       // plan (architect) — default gate
    GateAfterExecution                          // worker output (pause)
    GateAfterValidation                         // pipeline end (pause)
)
func (p HumanGatePosition) IsPlanGate() bool { return p == GateAfterResearch || p == GateAfterDeliberation }

type HumanGateSet []HumanGatePosition
func (h HumanGateSet) Active(pos HumanGatePosition) bool { for _, x := range h { if x == pos { return true } }; return false }

type RestartPhase string
const (
    RestartResearch     RestartPhase = "research"
    RestartDeliberation RestartPhase = "deliberation"
    RestartExecution    RestartPhase = "execution"
    RestartValidation   RestartPhase = "validation"
)

func gateDirName(pos HumanGatePosition) string {
    switch pos {
    case GateAfterResearch:     return "gate_after_research"
    case GateAfterDeliberation: return "gate_after_deliberation"
    case GateAfterExecution:    return "gate_after_execution"
    case GateAfterValidation:   return "gate_after_validation"
    }
    return "gate_unknown" // unreachable; defensive, not a panic
}
```

**Default set:** `{GateAfterDeliberation}` (plan approval). Others are opt-in.

### 3.2 Runner selection (no `HasPlanEditor` field)

```go
func (e *Engine) gateRunner(pos HumanGatePosition) (harness.Runner, string) {
    switch pos {
    case GateAfterResearch:     return e.Runners.Researcher, e.Config.Researcher.SystemPrompt
    case GateAfterDeliberation: return e.Runners.Architect,  e.Config.Architect.SystemPrompt
    }
    return nil, "" // pause gates
}
```

| Position | Runner | Output | Rich? |
|----------|--------|--------|-------|
| `GateAfterResearch` | Researcher | research draft | yes |
| `GateAfterDeliberation` | Architect | `plan-v<N>.md` | yes |
| `GateAfterExecution` | — | worker output | no (pause) |
| `GateAfterValidation` | — | validation text | no (pause) |

### 3.3 Events: minimal evolution (reuse, don't rename) — Blocker 7

In `events.go`: add `Position HumanGatePosition` to `GateRequest`; remove `Type GateType`
and the `GateType`/`GatePlanApproval` enum; remove `PlanDiff/PlanHistoryDir/
PlanHistoryHeadSHA` (Part 5) and `CriticReport` (unused). Keep `FinalPlanMarkdown/
PlanFilePath/PlanWarnings`. Add `EventAgentSkipped`, `PhaseDeliberating`. Keep
`EventPlanReady`, `EventGateRequest`, `EventChatResponse`/`Event.ChatText`. **Do not**
add `EventHumanGate`, `HumanChatGateRequest`, or `DecisionChat`.

```go
type GateRequest struct {
    Position          HumanGatePosition
    FinalPlanMarkdown string   // "" for pause gates
    PlanFilePath      string   // "" for pause gates
    PlanWarnings      []string
}
```

### 3.4 Unified `runGateLoop` (`internal/orchestrator/human_gate.go`)

This is the existing inline loop (886–1207) **extracted and generalized** — same
mechanics, no callbacks, no `DecisionChat`, no `runHumanGate`.

```go
func (e *Engine) runGateLoop(
    ctx context.Context, emit func(Event), decisions <-chan Decision,
    pos HumanGatePosition, session agent.SessionDir,
    planMarkdown, planFilePath string, planWarnings []string,
    planSessionID string,                 // session to CONTINUE; "" → cold start (restart)
    stream *streamCapture, streamOut chan<- harness.Event, rl *runLog,
) (decision Decision, finalPlan, finalSessionID string, err error)
```

Body:

```go
runner, system := e.gateRunner(pos)
isRich := pos.IsPlanGate()
sessionID := planSessionID
gateDir := gateDirName(pos)
revN := highestPlanVersion(session.SubDir(gateDir))

if isRich { emit(Event{Type: EventPlanReady, FinalPlan: planMarkdown}) }

for {
    emit(Event{Type: EventGateRequest, Gate: GateRequest{
        Position: pos, FinalPlanMarkdown: planMarkdown,
        PlanFilePath: planFilePath, PlanWarnings: planWarnings,
    }})

    var d Decision
    select {
    case d = <-decisions:
    case <-ctx.Done():
        return Decision{Type: DecisionCancel}, planMarkdown, sessionID, ctx.Err()
    }

    switch d.Type {
    case DecisionCancel:
        return d, planMarkdown, sessionID, errGateCancelled
    case DecisionApprove:
        return d, planMarkdown, sessionID, nil

    case DecisionEdit:
        if !isRich { continue }            // pause gate: stray edit ignored
        planMarkdown = d.EditedContent
        revN++
        planFilePath = writeArtifactIn(session, gateDir, fmt.Sprintf("plan-v%d.md", revN), planMarkdown)
        appendDialog(session.SubDir(gateDir), "Human", "(manual edit)")
        if d.Comment == "" {
            if d.AutoApprove { return Decision{Type: DecisionApprove}, planMarkdown, sessionID, nil }
            continue
        }
        fallthrough                        // edit + comment → run the architect revision step

    case DecisionComment:
        if !isRich { continue }            // pause gate: stray comment ignored
        planner := agent.NewPlanner(runner, system)
        baseline, baseErr := runner.ExtractPlan(ctx) // pre-turn plan (echo suppression)
        prompt := agent.ContinuePrompt(planMarkdown, d.Comment)
        var res agent.PlanResult
        if sessionID == "" {
            res, err = runPlanner(ctx, planner, prompt, stream, streamOut)       // cold start (restart)
        } else {
            res, err = continuePlanner(ctx, planner, sessionID, prompt, stream, streamOut)
        }
        if err != nil {
            emit(Event{Type: EventError, Err: fmt.Errorf("gate %s revision: %w", gateDir, err)})
            continue                       // never returns an empty plan as approval
        }
        sessionID = res.SessionID          // thread continuation forward
        if rev := agent.DetectPlanRevision(res.Plan, baseline, baseErr, planMarkdown); rev != nil {
            planMarkdown, planWarnings = rev.Markdown, rev.Warnings
            revN++
            planFilePath = writeArtifactIn(session, gateDir, fmt.Sprintf("plan-v%d.md", revN), planMarkdown)
            appendDialog(session.SubDir(gateDir), "Agent", "Re: "+truncateMsg(d.Comment, 50))
        } else if res.Chat != "" {
            emit(Event{Type: EventChatResponse, ChatText: res.Chat})
            appendDialog(session.SubDir(gateDir), "Agent", truncateMsg(res.Chat, 50)+" (chat only)")
        }
        continue
    }
}
```

`errGateCancelled` is a package sentinel; the coordinator maps it to a cancelled run
(preserving the worktree branch at pause gates, §1.2).

**On every return** (approve or cancel) the loop writes `<gateDir>/gate_decision.json`
(`{Decision.Type, ApprovedPlan: "plan-v<N>.md", Timestamp}`). **Edit+comment caveat:**
the `fallthrough` writes `plan-v<N>` for the manual edit *then* `plan-v<N+1>` for the
architect's reply — two versions for one action, **intentionally** (both are real plan
states); `dialog.md` records both turns.

**Properties (resolve Blockers 2/4/6):**
- Variant comes from `pos`/`isRich` — **no nil-callback union** (Blocker 2).
- Loop calls `runPlanner`/`continuePlanner` directly and reads
  `PlanResult.{Plan,Chat,SessionID}`, like the current code at `engine.go:979,1099`
  — **no `revisionFn`/`string` leak** (Blocker 4).
- `planSessionID` flows in from deliberation, updates each turn, returns out as
  `finalSessionID`; cold-start `runPlanner` fires only on restart (`sessionID==""`)
  — **continuation preserved, no per-turn full-plan re-send** (Blocker 6).
- Revision vs chat is **output-based** via `agent.DetectPlanRevision` — no
  `DecisionChat` needed.

### 3.5 Gate ordering

```
Research → [GateAfterResearch] → Deliberation → [GateAfterDeliberation] → Execution → [GateAfterExecution] → Validation → [GateAfterValidation]
```

`GateAfterDeliberation` continues the architect session produced by the deliberation
loop (Design via `deliberationResult.PlanSessionID`). `GateAfterResearch` continues
the researcher session. Pause gates pass `""` (approve/cancel only).

---

## Part 4: Session Directory + Completeness (break the cycle)

The two couplings that the earlier draft used to justify `internal/pipeline/` are
removed so `agent` never imports an orchestrator gate type.

### 4.1 `SessionDir` stays gate-agnostic (`internal/agent/session.go`)

No `GateDir(pos)` taking an enum. `session.go` stays **stdlib-only**.

```go
func (s SessionDir) SubDir(name string) string { return filepath.Join(s.Path, name) }
func (s SessionDir) ResearchDir() string       { return s.SubDir("research") }
func (s SessionDir) DeliberationDir() string   { return s.SubDir("deliberation") }
func (s SessionDir) LoopDir(n int) string      { return filepath.Join(s.Path, "deliberation", fmt.Sprintf("loop_%02d", n)) }
func (s SessionDir) ExecutionDir() string      { return s.SubDir("execution") }
func (s SessionDir) ValidationDir() string     { return s.SubDir("validation") }
```

The orchestrator owns the gate→dir mapping: `session.SubDir(gateDirName(pos))`.

### 4.2 Artifact writes

Replace each `writeArtifact(session, "X", content)` in extracted code with
`writeArtifactIn(session, "<subdir>", "X", content)`.

### 4.3 `run_config.json`

At session start, `writeArtifactJSONIn(session, "", "run_config.json", setup)`
(`json.Marshal(orchestrator.PipelineSetup)`).

### 4.4 `AnalyzeRunCompleteness` (Blocker 3) — local struct, plain-string restart

`agent` must not consume `orchestrator.PipelineSetup`. Decode `run_config.json` into
an **agent-local** struct; return the restart point as a **plain `string`**:

```go
type runPhases struct {
    Research, Execution, Validation bool
    DeliberationLoops               int
}

type RunCompleteness struct {
    Complete         bool
    MissingAgents    []string
    FailedAgents     []string
    MissingArtifacts []ArtifactRequirement
    Reason           string
    RestartPhase     string // "research"|"deliberation"|"execution"|"validation"; "" if complete
}

func AnalyzeRunCompleteness(runPath string) RunCompleteness // drops `detail`
```

`runPhases` mirrors `PipelineSetup`'s exported field names; the string values equal
`orchestrator.RestartPhase`'s consts. Update the sole caller `screen_run_detail.go:55`
to `agent.AnalyzeRunCompleteness(detail.Path)`. Read everything from disk under
`runPath` (`run_config.json`, subdir artifacts) — re-add nothing as a parameter.

**Phase presence, not `KnownAgents`:** each phase's completeness is decided by the
intended-phase flags in `run_config.json` (`Research`/`Execution`/`Validation`,
`DeliberationLoops`) cross-checked against subdir artifacts (`research/`,
`deliberation/loop_NN/plan-v*.md`, `execution/`, `validation/`). The validation phase
is the **worker** continuing its session; `"validator"` stays the event/artifact label
(`validator_meta.json`), and `KnownAgents` (no `"validator"`) is irrelevant here.

### 4.5 `NewSessionDir` uses `os.Mkdir`

```go
if err := os.Mkdir(dir, 0o755); err != nil {
    if !os.IsExist(err) { return SessionDir{}, fmt.Errorf("creating session dir %s: %w", dir, err) }
}
return SessionDir{Path: dir}, nil
```

### 4.6 Restart is phase-level (Blockers 3 & 9) — `engine_restart.go`

```go
type RestartInput struct { RunPath string; Phase RestartPhase } // no FirstMissingAgent

type restartState struct {
    SkipResearch, SkipDeliberation, SkipExecution bool
    SeedPlanMarkdown string // highest plan-v*.md / final_plan.md when restarting past deliberation
    SeedDraft        string // researcher_draft.md when restarting past research
}
func applyRestartSkip(session agent.SessionDir, in Input) (restartState, error)
```

Replaces the current `goto skipPlanning`/`goto planGate` (`engine.go:473–505`):

| `Phase` | Skip | Resume from | Seed |
|---------|------|-------------|------|
| `research` / "" | nothing | research | — |
| `deliberation` | research | deliberation | `research/researcher_draft.md` |
| `execution` | research + deliberation | execution | highest `plan-v*.md` / `final_plan.md` |
| `validation` | research + deliberation + execution | validation | worker artifacts |

On restart there is **no live session**; the resuming architect uses a **fresh
session seeded by the highest `plan-v<N>.md`** (the gate's `sessionID==""` cold-start
path). This is the only place fresh sessions are correct — not a normal-flow
regression (Risks).

**Input source (TUI-side).** The restart trigger in the TUI loads the source run's
`run_config.json` → `orchestrator.PipelineSetup` and `prompt.md`, and passes them as
`Input.Setup`/`Input.Prompt` (with `RestartFrom{RunPath, Phase}`); the engine **trusts
`Input`** and does no restart-load itself. The new session writes its own
`run_config.json` from the prefilled `Setup`.

**`copyCompletedArtifacts` rewrite.** It currently copies a flat filename list; it must
copy the new **subdir layout** (`research/`, `deliberation/…` incl. `loop_NN/`,
`final_plan.md`) from the source run into the new session, so the resumed run is a
complete record.

---

## Part 5: Plan Versioning (Git Micro-Repo Removal)

**Decision:** remove `internal/plan/gitrepo.go` (242) and `gitrepo_history.go` (129),
and all **37** `planRepo` refs in `engine.go`. Replace with numbered plan files and a
per-gate Markdown dialog.

### 5.1 Numbered plan files

Each architect pass writes a new `plan-v<N>.md`, numbered **globally** across the
deliberation; the highest N is current. The initial pass writes `deliberation/plan-v1.md`
(deliberation root); each round R writes its revision to `deliberation/loop_<R>/plan-v<N>.md`
(alongside that round's `critic_report.md`). On gate revision, new versions go to the
gate dir (`gate_after_*/plan-v<N>.md`). `highestPlanVersion(dir)` / `findHighestPlan(dir)`
track the counter from disk; `findHighestPlan(session.DeliberationDir())` walks the
deliberation tree (root + `loop_NN/`) for the final.

### 5.2 `dialog.md` (replaces git dialog)

Each gate directory has its own `dialog.md`, appended via `appendDialog(dir, role, msg)`:

```markdown
## Human
Edit plan to add X section

## Agent
Re: Edit plan to add X section

---
```

`## Human` / `## Agent` sections; `---` between turns; chat-only turns are tagged
`(chat only)`.

### 5.3 Remove TUI plan-history viewer

Delete `plan_history_loader.go`, `screen_plan_history.go`, `screen_plan_history_test.go`,
`plan_history_model_test.go`. Remove `StatePlanHistoryDetail`, `ContentPlanHistory`,
`planHistoryScreen` (`model.go`); `OpenPlanHistoryIntent`, `ClosePlanHistoryIntent`,
`RevertPlanIntent` (`messages.go`); the `Ctrl+Y` handlers (`screen_pipeline.go:701`,
`screen_run_detail_keys.go:20`); and the `model.go` routing/mouse/layout/view wiring.
The subsystem is self-contained (verified).

### 5.4 Session directory layout

```
.orqestra/sessions/<ts>-run/
  run_config.json            ← PipelineSetup
  prompt.md
  final_plan.md
  research/
    researcher_meta.json  researcher_draft.md  researcher_session.jsonl
  deliberation/
    plan-v1.md                       ← architect initial pass (deliberation root)
    architect_initial_meta.json  architect_session.jsonl
    loop_01/                         ← round 1: critic + architect revision
      critic_meta.json  critic_report.md  critic_session.jsonl
      architect_revision_meta.json  architect_revision_session.jsonl
      plan-v2.md                     ← revision after round 1
    loop_02/                         ← round 2 (when DeliberationLoops ≥ 2)
      critic_report.md  plan-v3.md  ...
  gate_after_research/        ← when gate ran
    gate_decision.json  dialog.md
  gate_after_deliberation/
    gate_decision.json  dialog.md  plan-v<N>.md
  execution/
    worker_meta.json  worker_output.txt  worker_session.jsonl
  gate_after_execution/      gate_decision.json  dialog.md
  validation/
    validator_meta.json  validator_session.jsonl  worker_validation.txt
  gate_after_validation/     gate_decision.json  dialog.md
```

### 5.5 No backward compatibility

`AnalyzeRunCompleteness` and `copyCompletedArtifacts` use new-layout logic only. Runs
without `run_config.json` are incomplete/unrestartable.

---

## Part 6: TUI Changes (contained — Blockers 7 & 8)

Because `EventGateRequest`/`Event.Gate` is **reused**, the blast radius is small and
fully enumerable. No `HumanChatMode`/`PlanChatMode`/`SimpleChatMode` split; the
existing `ContentPlanReview` serves the rich gate.

### 6.1 Pipeline setup panel (`model.go`)

`setupOpen bool`, `currentSetup orchestrator.PipelineSetup`; overlay on the prompt
screen. Keys: `^P` toggle, `↑/↓` navigate, `←/→` change values (space toggles a focused
gate). `Architect ↔ Critic` is the `DeliberationLoops` stepper (default **1**). Human
Review is a **per-gate toggle list** — all 4 positions, default `{After Deliberation}`.

```
  Research:              ◁ Enabled ▷
  Architect ↔ Critic:    ◁ 1 ▷          (DeliberationLoops, default 1)
  Execution:             ◁ Enabled ▷
  Validation:            ◁ Enabled ▷
  Human Review:
    [x] After Deliberation              (default on)
    [ ] After Research
    [ ] After Execution
    [ ] After Validation
```

### 6.2 `screen_pipeline.go` edits (enumerated)

| Location | Change |
|----------|--------|
| `ApplyEvent` `case EventGateRequest` (~397–433) | Replace `switch event.Gate.Type` with a branch on `event.Gate.Position`. `IsPlanGate()` → existing `ContentPlanReview` setup. Execution/Validation → minimal pause-confirm. |
| same handler (~400–425) | Delete `event.Gate.PlanDiff` / inline-diff render / `planDiffLineOffset`. |
| `handlePlanReviewKey` Ctrl+D (~696) | Delete diff-scroll (no diff). |
| `handlePlanReviewKey` Ctrl+Y (~701–709) | Delete `OpenPlanHistoryIntent` (Part 5.3). |
| status-line switch (~818–832) | Add `case AgentStateSkipped`. |
| `ApplyEvent` | Add `case EventAgentSkipped:` → **append** a new `AgentRow{State: AgentStateSkipped}` in pipeline order (skipped agents never emit `EventAgentStarted`, so there is no row to find-and-update — mirror the append at `screen_pipeline.go:339`). |
| struct fields (~42–135) | Remove `planDiff`, `planHistoryDir`, `planHistoryHeadSHA`, `planDiffLineOffset`. |

### 6.3 Pause-confirm rendering (Execution/Validation)

Opt-in/default-off. Reuse `ContentPlanReview` with `finalPlan==""`, no comment
textarea, only `Ctrl+A` (→`DecisionApprove`) and `Ctrl+C`/cancel (→`DecisionCancel`).
Keep minimal; deletable with the pause positions if no consumer materializes.

### 6.4 Decision send-path (unchanged; no `DecisionChat`)

Existing intents map cleanly (`model.go:554–593`):
`ApprovePlanIntent→DecisionApprove`, `CommentPlanIntent→DecisionComment`,
`ConfirmEditIntent→DecisionEdit`, `CancelPlanIntent→DecisionCancel`. No
`HumanGateChatIntent`, no `DecisionChat`. A "chat" turn is a `DecisionComment` whose
agent reply is chat-only — decided by `DetectPlanRevision` in the loop.

### 6.5 `messages.go` — restart intent (Blocker 9)

```go
type ConfirmSetupIntent struct { Setup orchestrator.PipelineSetup }
func (ConfirmSetupIntent) isIntent() {}

type RestartRunIntent struct { RunPath string; Phase orchestrator.RestartPhase } // no Loop
func (RestartRunIntent) isIntent() {}
```

`messages.go` already imports `orchestrator`; no new import. The restart trigger in
`screen_run_detail_keys.go` builds
`RestartRunIntent{RunPath: s.detail.Path, Phase: orchestrator.RestartPhase(s.completeness.RestartPhase)}`
(cast the plain `agent` string to the typed `orchestrator.RestartPhase`). `model.go`
restart wiring drops `lastRestartFirstMissingAgent`, carries `Phase`.

### 6.6 `AppState` / `AgentState`

Remove `StatePlanHistoryDetail`, `ContentPlanHistory`. Setup is an overlay on
`StatePrompt`. Add `AgentStateSkipped AgentState = "skipped"`.

---

## Part 7: Headless Mode Removal

Orqestra is TUI-only. Remove all headless / `--plan` / `--no-execute` paths.

- **`cmd/orqestra/main.go`:** remove `--prompt/--auto-approve/--auto-reject/
  --auto-init/--plan/--no-execute/--json`, `isHeadless`, `runPlanOnly`,
  `runValidateOnly`, `runExecOnly`, and the `RunHeadless`/`RunHeadlessPlanOnly` calls.
  Keep `--config`.
- **`internal/tui/tui.go`:** delete `RunHeadless`, `RunHeadlessPlanOnly`.
- **`Input`:** already reduced to `{Prompt, Setup, RestartFrom}` (§2.2). Remove all
  `input.AutoApprove` checks in `run()` — plan approval is the configurable
  `GateAfterDeliberation` gate.
- Update README and copilot instructions to TUI-only.

---

## Part 8: Tests

### 8.1 Setup / gate types (`internal/orchestrator/setup_test.go`, `gate_test.go`)

Table-driven `PipelineSetup.Validate()`, `DefaultPipelineSetup()`,
`HumanGateSet.Active()`, `HumanGatePosition.IsPlanGate()`, `gateDirName`. (No
`internal/pipeline/` package and no cycle-guard test — there is no such package.)

### 8.2 Unified gate (`internal/orchestrator/human_gate_test.go`)

- `TestRunGateLoop_Comment_Revision` — `DecisionComment` → `Continue` → new
  `plan-v<N>.md` and updated `finalPlan`.
- `TestRunGateLoop_Comment_ChatOnly` — reply with no plan change emits
  `EventChatResponse`, writes no new `plan-v<N>.md`, plan unchanged.
- `TestRunGateLoop_RevisionError` — planner error surfaces `EventError`, never
  returns an empty plan as approval (Blocker 4).
- `TestRunGateLoop_PauseApprove` / `TestRunGateLoop_PauseStrayCommentNoop` —
  Execution/Validation: approve/cancel only; comment/edit are no-ops.
- `TestRunGateLoop_Cancel` → `errGateCancelled`.
- `TestRunGateLoop_Continuation` — assert one `claude_session_id` across deliberation
  + gate turns (no token regression, Blocker 6).

### 8.3 Engine (`engine_deliberation_test.go`, `engine_phases_test.go`, `engine_restart_test.go`)

- `TestEngine_Deliberation_{OneLoop,ThreeLoops}`, `_SkipResearch`, `_SkipExecution`,
  `_HumanGateAfterDeliberation`.
- `TestEngine_Phase_SetupZeroValueFallback`.
- `TestEngine_Restart_From{Deliberation,Execution,Validation}` via `RestartInput.Phase`.

### 8.4 Session (`internal/agent/session_test.go`)

- `TestAnalyzeRunCompleteness_{NewLayout,PartialDeliberation,NoRunConfig}` (local
  `runPhases`, plain-string `RestartPhase`).
- `TestNewSessionDir_{Exists,PermissionDenied}`.

### 8.5 TUI (`internal/tui/screen_setup_test.go`)

Navigation, toggle, stepper, confirm, validation-error tests; gate handler renders
rich vs pause by `Position`; window-resize stability with no mutation in `View()`.

---

## Execution Order (build-green at every step; `go build ./...` after each)

1. **Types in `orchestrator`** — `setup.go` + `gate.go` (`PipelineSetup`,
   `DefaultPipelineSetup`, `Validate`, `HumanGatePosition`+`IsPlanGate`,
   `HumanGateSet`, `RestartPhase`, `gateDirName`). Builds standalone.
2. **`events.go` deltas** — add `GateRequest.Position`; remove `GateType`/`Type`/git
   fields/`CriticReport`; add `EventAgentSkipped`, `PhaseDeliberating`. (TUI
   temporarily references removed fields — fixed in step 8; build the orchestrator
   package alone here.)
3. **Part 5** — delete `gitrepo*.go`; remove the 37 `planRepo` refs; add `appendDialog`
   + numbered `plan-v<N>.md` writes.
4. **Part 4** — `SubDir`+named dirs; `runPhases`; `AnalyzeRunCompleteness(runPath)` +
   caller; `os.Mkdir`.
5. **`human_gate.go`** — `runGateLoop`, `gateRunner`, `errGateCancelled`,
   `writeArtifactIn`, `appendDialog`, `highestPlanVersion`.
6. **Part 1** — coordinator `run()` `(Result,error)` + `Start()` terminal events;
   `runLog`; `startQuestionBridge`; phase funcs; `engine_restart.go`; wire gate calls.
7. **Part 2 + deliberation continuation** — `Input.Setup`, defaults/validation,
   `run_config.json`; one architect session across loops; `PlanSessionID` out.
8. **Part 7** then **Part 6** — headless removal; setup panel; gate handler on
   `Position`; `AgentStateSkipped`; plan-history deletion; restart-phase wiring.
   `go build ./...` green end to end.
9. **Part 8** — tests.

---

## Verification

- **No package, no cycle:** `grep -rn "internal/pipeline" .` empty;
  `go list -deps ./internal/agent | grep orchestrator` empty.
- **Build green** after each step.
- **Unified gate** + **no token regression:** §8.2 tests; one `claude_session_id`
  across deliberation + gate turns in `*_meta.json` and `~/.claude/projects/.../*.jsonl`.
- **Completeness / restart / setup:** §8.1, §8.3, §8.4.
- **Smoke:** `make build && ./bin/orqestra` — run the default pipeline; revise at the
  deliberation gate (confirm continuation), approve; verify session layout
  (`run_config.json`, `deliberation/loop_01/plan-v*.md`,
  `gate_after_deliberation/dialog.md`).

---

## Known Risks (corrected)

1. `engine.go` must end < 500 lines after extraction; split per Part 1.
2. `screen_pipeline.go` stays large (~1,530 lines, ~40 fields) — full decomposition is
   follow-up; this plan edits only the enumerated handlers.
3. Plan approval is now `GateAfterDeliberation` (default-on) with full
   edit/comment/approve/cancel semantics; other gates are opt-in and skipped when not
   configured.
4. **Restart uses a fresh session by necessity** (original process is gone): the
   resuming architect seeds from the highest `plan-v<N>.md`. Not the normal-flow
   regression — normal flow keeps continuation (Risk 7).
5. Old-format runs (no `run_config.json`) are not restartable; shown as incomplete.
6. Plan history is numbered `plan-v<N>.md` + per-gate `dialog.md`; no git micro-repo,
   no in-TUI viewer.
7. **Continuation is the normal path.** The architect session continues across
   deliberation loops (`continuePlanner`) and into the `GateAfterDeliberation` gate via
   `planSessionID`; the gate's cold-start `runPlanner` fires only when
   `planSessionID == ""` (restart). The full plan is **not** re-sent per turn.
   *(Replaces the earlier incorrect "every agent uses a fresh session" claim.)*
8. Validation continues the worker session (`runRunnerContinue`) to preserve tool
   state — unchanged.
9. Pause gates (Execution/Validation) are wired but **default-off**. Cancel at a
   post-execution pause gate **preserves the worktree branch** and skips the merge
   (mirrors `DecisionMergeAbort`), `Status = cancelled`; it never discards worker output.
10. The post-run merge is **guarded on `setup.Execution`** (+ worktree created): when
    execution is skipped there is no worktree and no merge.
11. `EventAgentSkipped` **appends** a skipped agent row (skipped agents never emit
    `EventAgentStarted`); the handler must not assume an existing row.
