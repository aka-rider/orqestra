# Plan — Flexible Pipeline v6 (TDD / fluent execution)

> **Purpose:** Adapt the flexible-pipeline design to the current codebase, executed
> as a sequence of small Canon-TDD iterations where **every iteration compiles and
> every test is green**. No "fixed in a later step", no build-red middle.
>
> **v6 supersedes v5.** v5's architecture is sound and stays; v6 changes two things:
>
> 1. **Unified session layout.** Drop the `gate_after_*/` and `deliberation/loop_NN/`
>    subdirectories. Each phase owns one directory `<phase>/` holding `plan-vN.md`
>    (flat, globally numbered within the phase) and one `dialog.md`. (Your point 3.)
> 2. **Fluent, choke-free execution via expand→migrate→contract.** v5's execution
>    order gutted `events.go` in step 2 and left the TUI broken until step 8 —
>    `go build ./...` was red for 6 of 9 steps. v6 reorders so nothing referenced is
>    ever deleted before its replacement is live and its callers migrated. The build
>    and the test suite stay green at the end of every iteration. (Your point 4.)

---

## Carried locks (from v5, still in force)

1. **No `internal/pipeline/` package.** Shared config/value types live in
   `internal/orchestrator/`. `agent` never imports orchestrator types — verified:
   `go list -deps ./internal/agent | grep orchestrator` is empty, and `session.go`
   is stdlib-only.
2. **One unified `runGateLoop`** across all gate positions — no `planGate`/`pauseGate`
   split, no nil-callback union. Variant is derived from `pos HumanGatePosition`. This
   is ordinary enum dispatch, not the banned flattened-sum-type.
3. **Continuation is the normal path.** One architect session threads through the
   deliberation loops and into the `GateAfterDeliberation` gate via a session id; the
   gate's cold-start path fires only on restart. The full plan is not re-sent per turn.
4. **Validation continues the worker session** to preserve tool state.

## New v6 locks

5. **Unified `<phase>/{plan-vN.md, dialog.md}` layout.** Gate revisions write into the
   *source phase's* directory; the gate→dir mapping is `phaseDir(pos)` in `orchestrator`,
   not a separate `gate_*` directory. No `loop_NN/` nesting.
6. **Green at every iteration.** Definition of done for an iteration: `go build ./...`
   succeeds **and** `go test ./...` passes. Deletions only happen in a *contract*
   iteration, after every consumer has migrated. CI command per iteration:
   `make build && make test` (add `make test-integration` when touching sandbox/harness).

---

## Part 0: Codebase evidence (verified this session)

| Claim | Status | Evidence |
|------|--------|----------|
| `engine.go` size | 1638 lines | `wc -l` |
| `run()` | `internal/orchestrator/engine.go:295`, currently returns **void** | grep |
| `Run()` already returns `(Result, error)` | Yes | `engine.go:156`; `engine_test.go:134` asserts it |
| `Start()` returns channels | Yes | `engine.go:113` |
| helpers `runPlanner`/`continuePlanner`/`runRunnerStreaming`/`runRunnerContinue` | Yes | `engine.go:229/235/241/271` |
| `copyCompletedArtifacts(src,dst)` copies a **flat** filename list | Yes | `engine.go:1577`, list at `:1584-1592` |
| artifacts are **flat** in session root today | Yes | `researcher_draft.md`, `critic_report.md`, `final_plan.md`, `worker_output.txt`, `worker_validation.txt` written at root |
| inline plan gate | `engine.go:888-1207` | `EventPlanReady` :888, `EventGateRequest` :911 |
| goto-based restart | `engine.go:470/498/501` (`goto planGate`/`skipPlanning`) | grep |
| `internal/pipeline/` | does **not** exist | `ls` |
| `agent` → `orchestrator` dependency | **none** | `go list -deps` |
| `session.go` imports | stdlib-only; uses `os.MkdirAll` at `:27` | read |
| `events.go`: `GateType`/`GatePlanApproval`/`GateRequest.{Type,PlanDiff,PlanHistoryDir,PlanHistoryHeadSHA,CriticReport,FinalPlanMarkdown,PlanFilePath,PlanWarnings}` | all present | `events.go:38-60` |
| `events.go`: `EventChatResponse` + `Event.ChatText` | present | `:20/:123` |
| `planner.go`: `NewPlanner`/`Run`/`Continue`/`PlanResult`/`DetectPlanRevision` | present | `:38/:52/:105/:13/:149` |
| prompts `ArchitectPrompt`/`ContinuePrompt`/`CriticContinuePrompt`/`CriticReviewPrompt` | present | `prompts.go:9/20/58/88` |
| `planRepo` refs | **37 in `engine.go`, 39 in `internal/orchestrator/`** | grep — *v5 undercounted: 2 live outside engine.go* |
| `AnalyzeRunCompleteness(runPath, detail RunDetail)` | `session.go:312`; sole caller `screen_run_detail.go:55` | grep |
| TUI plan-history subsystem | `plan_history_loader.go`, `screen_plan_history.go`, `plan_history_model_test.go`, `screen_plan_history_test.go` | `ls` |
| TUI gate handler | `screen_pipeline.go:398` switches `event.Gate.Type`; diff/history fields read at `:400-402,423-425,698`; fields declared `:65,176` | grep |
| `screen_pipeline.go` size | 1530 lines | `wc -l` |
| headless | `cmd/orqestra/main.go` `isHeadless` :100, `runPlanOnly/runValidateOnly/runExecOnly` :514/554/573; `RunHeadless*` in `tui.go` | grep |
| `AutoApprove`/`NoExecute`/`PlanFile` usages | 63 | grep |
| **black-box test seam** | `testutil.FakeRunner` (`var _ harness.Runner`); `engine_test.go` drives full pipeline via `engine.Run/Start` + scripted `FakeRunner` + `SetupPlanFile` | read |

**Implication for TDD:** the engine is already exercised end-to-end through its public
API with one scripted fake at the `harness.Runner` boundary. That is exactly the
black-box style we want — extend it, do not replace it, and do not add expectation-
heavy mocks anywhere else.

---

## Part A: Target session layout (v6 unified)

```
.orqestra/sessions/<ts>-run/
  run_config.json            ← PipelineSetup
  prompt.md
  final_plan.md              ← copy of the highest deliberation plan-vN.md at run end
  research/
    plan-v1.md               ← researcher draft (rich gate treats it as editable plan)
    plan-v2.md ...           ← only if GateAfterResearch revises it
    dialog.md                ← gate turns (## Human / ## Agent); absent if no gate ran
    researcher_r01_meta.json  researcher_r01_session.jsonl
    gate_decision.json        ← written iff GateAfterResearch ran
  deliberation/
    plan-v1.md               ← architect initial pass
    plan-v2.md               ← after loop 1 (critic → architect revision)
    plan-vN.md               ← after loop N-1, then any gate revisions continue numbering
    dialog.md                ← ## Critic (round k) reports + ## Human/## Agent gate turns
    architect_r01_meta.json  critic_r01_meta.json  *_session.jsonl
    gate_decision.json        ← written iff GateAfterDeliberation ran
  execution/
    output.txt
    dialog.md                ← pause-gate turns (only if GateAfterExecution)
    worker_r01_meta.json  worker_r01_session.jsonl
    gate_decision.json
  validation/
    validation.txt
    dialog.md
    validator_r01_meta.json  validator_r01_session.jsonl
    gate_decision.json
```

Rules:
- **One directory per phase.** `plan-vN.md` is numbered flat **within the phase**,
  globally across that phase's automated loops *and* gate revisions; the highest N is
  current. Plan phases (research, deliberation) have `plan-vN.md`; pause phases
  (execution, validation) have `output.txt`/`validation.txt` instead.
- **`dialog.md` is the single human-readable transcript** of a phase: critic reports
  (`## Critic (round k)`) and gate turns (`## Human` / `## Agent`, `(chat only)` tag),
  `---` between turns. It replaces the per-round `critic_report.md` files, the git
  micro-repo dialog, and the per-gate dialog.
- **Diagnostics stay flat and round-suffixed** (`<agent>_rNN_meta.json`,
  `<agent>_rNN_session.jsonl`) — these are infrastructure, not domain artifacts, and
  never go on agent domain structs.
- `gate_decision.json` (`{Type, ApprovedPlan:"plan-vN.md", Timestamp}`) is written into
  the phase dir on gate exit (approve/cancel).

`phaseDir(pos)` mapping (in `orchestrator`):

| Position | Dir |
|----------|-----|
| `GateAfterResearch` | `research` |
| `GateAfterDeliberation` | `deliberation` |
| `GateAfterExecution` | `execution` |
| `GateAfterValidation` | `validation` |

This deletes v5's `gateDirName`, `gate_after_*` dirs, `LoopDir(n)`, and the separate
`critic_report.md` files.

---

## Part B: Types (additive leaves — no existing code references them)

### B.1 `internal/orchestrator/setup.go`

```go
package orchestrator

type PipelineSetup struct {
    Research          bool
    DeliberationLoops int          // 1..10; 0 → 1
    Execution         bool
    Validation        bool
    HumanGates        HumanGateSet
}

func DefaultPipelineSetup() PipelineSetup {
    return PipelineSetup{
        Research: true, DeliberationLoops: 1, Execution: true, Validation: true,
        HumanGates: HumanGateSet{GateAfterDeliberation},
    }
}

func (p PipelineSetup) Validate() error { /* loops 1..10; ≥1 of R/E/V enabled */ }

func resolveSetup(in Input) PipelineSetup { /* zero-value → default; loops 0 → 1 */ }
```

### B.2 `internal/orchestrator/gate.go`

```go
package orchestrator

type HumanGatePosition int
const (
    GateAfterResearch HumanGatePosition = iota
    GateAfterDeliberation
    GateAfterExecution
    GateAfterValidation
)
func (p HumanGatePosition) IsPlanGate() bool { return p == GateAfterResearch || p == GateAfterDeliberation }

type HumanGateSet []HumanGatePosition
func (h HumanGateSet) Active(pos HumanGatePosition) bool { /* linear scan */ }

func phaseDir(pos HumanGatePosition) string // "research"|"deliberation"|"execution"|"validation"

type RestartPhase string
const (
    RestartResearch     RestartPhase = "research"
    RestartDeliberation RestartPhase = "deliberation"
    RestartExecution    RestartPhase = "execution"
    RestartValidation   RestartPhase = "validation"
)
```

### B.3 `internal/agent/session.go` — gate-agnostic dir helpers (stays stdlib-only)

```go
func (s SessionDir) SubDir(name string) string { return filepath.Join(s.Path, name) }
func (s SessionDir) ResearchDir() string     { return s.SubDir("research") }
func (s SessionDir) DeliberationDir() string { return s.SubDir("deliberation") }
func (s SessionDir) ExecutionDir() string    { return s.SubDir("execution") }
func (s SessionDir) ValidationDir() string   { return s.SubDir("validation") }
```

The orchestrator owns gate→dir via `session.SubDir(phaseDir(pos))`. No `LoopDir`, no
enum parameter in `agent`.

### B.4 Artifact/dialog/version helpers (`internal/orchestrator`)

```go
func writeArtifactIn(s agent.SessionDir, subdir, name, content string) (path string)
func writeArtifactJSONIn(s agent.SessionDir, subdir, name string, v any) error
func appendDialog(dir, role, message string)          // ## <role>\n<msg>\n\n---\n
func highestPlanVersion(dir string) int               // max N in plan-v<N>.md (0 if none)
func findHighestPlan(dir string) string               // path of highest plan-v*.md ("" if none)
```

(`findHighestPlan` is now a flat scan of one phase dir — no subtree walk, because there
are no `loop_NN/` subdirs.)

---

## Part C: Unified gate loop (`internal/orchestrator/human_gate.go`)

Same mechanics as the inline loop (`engine.go:888-1207`), generalized. Reuses
`EventGateRequest`/`GateRequest`/`Event.Gate` — **no rename**, no `DecisionChat`.

```go
func (e *Engine) runGateLoop(
    ctx context.Context, emit func(Event), decisions <-chan Decision,
    pos HumanGatePosition, session agent.SessionDir,
    planMarkdown, planFilePath string, planWarnings []string,
    planSessionID string,                 // session to CONTINUE; "" → cold start (restart)
    stream *streamCapture, streamOut chan<- harness.Event, rl *runLog,
) (decision Decision, finalPlan, finalSessionID string, err error)
```

Behavior (unchanged from v5 except dir = `phaseDir(pos)`):
- `isRich := pos.IsPlanGate()`. Rich gates emit `EventPlanReady`, accept
  edit/comment/approve/cancel; pause gates accept approve/cancel only (stray
  edit/comment are no-ops).
- Comment/edit+comment → `agent.NewPlanner(runner, system)` then `continuePlanner`
  (or cold-start `runPlanner` when `sessionID==""`); pre-turn `ExtractPlan` baseline;
  `agent.DetectPlanRevision` decides revision vs chat-only.
- Revision → new `plan-vN.md` in `session.SubDir(phaseDir(pos))`, `appendDialog(dir,
  "Agent", …)`, `planMarkdown` updated. Chat-only → `EventChatResponse` +
  `appendDialog(dir,"Agent", "…(chat only)")`, no new plan version.
- Planner error → `EventError`, `continue` (never returns an empty plan as approval).
- `sessionID` threads in from deliberation, updates each turn, returns as
  `finalSessionID` (continuation; no per-turn full-plan resend).
- On approve/cancel: write `gate_decision.json`; cancel returns sentinel
  `errGateCancelled`.

`gateRunner(pos)` returns `(e.Runners.Researcher, …)` / `(e.Runners.Architect, …)` for
plan gates, `(nil,"")` for pause gates.

---

## Part D: Engine coordinator (`run()` → `(Result, error)`)

`run()` becomes a thin coordinator (kept ~300 lines; phases/gate extracted to siblings,
each file < 500). Shape per v5 §1.2 with the unified dirs:

- `setup := resolveSetup(input)`; `setup.Validate()`.
- `runResearch` → `research/plan-v1.md`; optional `runGateLoop(GateAfterResearch)`.
- `runDeliberation` (one architect session via `Continue`): initial pass →
  `deliberation/plan-v1.md`; each round k → critic (`## Critic (round k)` in
  `deliberation/dialog.md`) → architect revision → `deliberation/plan-v(k+1).md`.
  Returns `deliberationResult{PlanMarkdown, Warnings, PlanSessionID, PlanRevisionCount}`.
- optional `runGateLoop(GateAfterDeliberation, …, planSessionID=del.PlanSessionID)`.
- `runExecution` (guarded on `setup.Execution`) → `execution/output.txt`; optional
  pause `runGateLoop(GateAfterExecution)`.
- `runValidation` (continues worker session) → `validation/validation.txt`; optional
  pause `runGateLoop(GateAfterValidation)`.
- post-run worktree commit/merge **guarded on `setup.Execution`**; pause-gate cancel
  preserves the worktree branch and skips merge (mirrors `DecisionMergeAbort`).
- returns `Result{Status, FinalPlan, WorkerValidation, RunDir}`; `Start()`'s goroutine
  emits terminal `EventComplete`/`EventError`.

Supporting extractions: `engine_phases.go`, `engine_deliberation.go`,
`engine_restart.go`, `run_log.go` (`runLog` struct + `copySessionLog`),
`startQuestionBridge` (returns nothing; `defer Stop()` stays nil-guarded in `run()`).

---

## Part E: Restart + completeness (unified-layout, plain-string restart)

`agent.AnalyzeRunCompleteness(runPath string) RunCompleteness` (drops `detail`); decode
`run_config.json` into an **agent-local** `runPhases` struct; return
`RestartPhase string` ("research"|"deliberation"|"execution"|"validation"|""). Phase
presence is decided by intended-phase flags cross-checked against the unified phase
dirs (`research/plan-v*.md`, `deliberation/plan-v*.md`, `execution/output.txt`,
`validation/validation.txt`).

`RestartInput{RunPath string; Phase RestartPhase}` (no `FirstMissingAgent`, no `Loop`).
`applyRestartSkip(session, in)` replaces the `goto` restart.

**Restart seed source — resolved (the v5 ambiguity):** on restart the engine
(1) creates the new session, (2) `copyCompletedArtifacts(source, newSession)` copies the
completed **phase directories** up to the restart point, then (3) seeds **from the new
session** (single source of truth): `findHighestPlan(newSession.DeliberationDir())` for
plan seed, `findHighestPlan(newSession.ResearchDir())` for draft seed. The resuming
architect uses a fresh session (`planSessionID==""`, cold-start) seeded by that highest
`plan-vN.md`. `copyCompletedArtifacts` is rewritten to copy phase dirs, not a flat list.

TUI builds `Input` from the source run's `run_config.json` (→ `PipelineSetup`) and
`prompt.md`; engine trusts `Input` and writes its own `run_config.json`.

---

## Part F: TUI (contained — event reused)

### F.1 Setup panel (overlay on `StatePrompt`)

The pipeline setup is an overlay panel rendered **above** the prompt textarea. It is:
- **Always visible** in `StatePrompt` — not a separate `AppState`.
- **Focusable** via `^P` — unfocused by default (prompt textarea is primary focus).
- **Resets to defaults** each launch — no cross-run persistence.
- **Becomes static** when the pipeline starts — scrolls up with the rest of output.

**Footer hint (unfocused):** `^P Pipeline Setup` in low-contrast; panel body shows
"Press ^P to change the pipeline".

**Key handling in `StatePrompt`:**

| Key | `setupOpen=false` | `setupOpen=true` |
|-----|-------------------|-----------------|
| `^P` | open panel, focus first row | close panel, return focus to textarea |
| `↑` / `↓` | no-op | move cursor through setup rows |
| `←` / `→` | no-op | toggle bool / adjust `DeliberationLoops` |
| `Space` | no-op | toggle a focused gate checkbox |
| `Enter` | submit prompt | close panel, apply settings |
| `Esc` | existing behavior | close panel |

**Panel content (when focused):**

```
  Research:              ◁ Enabled ▷
  Architect <-> Critic:  ◁ 4 ▷
  Execution:             ◁ Enabled ▷
  Validation:            ◁ Enabled ▷
  Human Review:          [ After Research, After Deliberation ]

  (Human Review sub-control, opened via Enter on the Human Review row)
  [ ] After Research
  [x] After Deliberation
  [ ] After Execution
  [ ] After Validation
  ↑↓ navigate | ←→/Space toggle | Esc back
```

**`Model` struct additions:**
```go
setupOpen    bool
currentSetup orchestrator.PipelineSetup
```

**New intent** (`messages.go`):
```go
type ConfirmSetupIntent struct {
    Setup orchestrator.PipelineSetup
}
```

`processIntent` saves `m.currentSetup = i.Setup`, closes panel, transitions to pipeline.
`recalculateLayout` in `StatePrompt` accounts for setup panel height.
`startPipeline` passes `Setup: m.currentSetup`; zero-value falls back to `DefaultPipelineSetup()`.

**No `StateSetup` needed.** `AppState` stays:
1. `StatePrompt` — prompt textarea + setup overlay
2. `StatePipeline` — pipeline running/done
3. `StateRunsList` — historical runs list
4. `StateRunDetail` — single run detail

---

### F.2 Gate split-view chat (`HumanChatMode`)

When a gate fires (`EventGateRequest` with `Position`), `PipelineScreen` switches to a
split-view layout:

```
┌─────────────────────────────────────────┐
│ Pipeline history (scrollable)           │
│   Research: ✓ done                      │
│   Architect↔Critic: 2 loops             │
│   Worker: ✓ done                        │
├─────────────────────────────────────────┤
│ Agent output (last agent's stream)      │
│   [rendered markdown plan if available] │
├─────────────────────────────────────────┤
│ Chat input (same textarea as prompt)    │
│   [user types here]                     │
├─────────────────────────────────────────┤
│ Footer: [Enter] send  [^E] editor       │
│         [^A] approve  [^C] abort        │
└─────────────────────────────────────────┘
```

The gate is modelled as a `HumanChatMode` typed interface sub-model
(`internal/tui/mode_human_chat.go`), following the tui-instructions "one active mode,
one owned value" rule:

```go
type HumanChatMode interface {
    Update(msg tea.Msg) (HumanChatMode, tea.Cmd)
    View(width int) string
    Footer() string
    Pending() tea.Msg  // non-nil = user acted; parent drains to decisions channel
}

// PlanChatMode — pos.IsPlanGate() == true (GateAfterResearch, GateAfterDeliberation)
type PlanChatMode struct {
    plan         string
    planFilePath string  // highest plan-v*.md on disk; used by ^E editor flow
    chatHistory  []ChatEntry
    input        textarea.Model
    pending      tea.Msg
}

// SimpleChatMode — pause gates (GateAfterExecution, GateAfterValidation)
type SimpleChatMode struct {
    chatHistory []ChatEntry
    input       textarea.Model
    pending     tea.Msg
}
```

Constructor: `newHumanChatMode(req GateRequest, width int) HumanChatMode` —
returns `PlanChatMode` when `req.Position.IsPlanGate()`, `SimpleChatMode` otherwise.

**`PipelineScreen` changes (Iter 9):**
- Add `ContentHumanGate ContentMode` to enum (replaces `ContentPlanReview` for new gates)
- Add **one field**: `activeChat HumanChatMode`
- `ApplyEvent` on `EventGateRequest`: `s.activeChat = newHumanChatMode(event.Gate, s.width); s.content = ContentHumanGate`
- `Update`: when `content == ContentHumanGate`, delegate to `s.activeChat.Update(msg)`, drain `Pending()` to `s.PendingIntent`
- `View`: render `s.activeChat.View(width)` when `content == ContentHumanGate`
- Add `case ContentHumanGate:` in `viewFooter()` → `s.activeChat.Footer()`; missing this falls to `default` and shows wrong hints
- Add `EventAgentSkipped` → append `AgentRow{State: AgentStateSkipped}`; add `AgentStateSkipped AgentState = "skipped"` in `model.go`

**Chat behavior:**
- Input mimics the prompt textarea — same widget, same key bindings.
- Chat expands upward, occupying **max 50% of remaining screen**.
- `^O` collapses chat (toggles): collapsed shows only pipeline history + agent output.

---

### F.3 Gate key bindings

| Key | `PlanChatMode` | `SimpleChatMode` |
|-----|----------------|-----------------|
| `Enter` | Send chat message to architect (streams response; `DetectPlanRevision` auto-detects plan change → new `plan-vN.md` if revised) | Send chat message to agent |
| `^E` | Open highest `plan-v*.md` in `$EDITOR`/`$VISUAL` (non-blocking `tea.Cmd`); after editor exits, reload plan content | — |
| `^A` | Approve — send `Decision{Type: DecisionApprove}` | Approve |
| `^C` | Abort — send `Decision{Type: DecisionCancel}` | Abort |
| `^O` | Collapse / expand chat | Collapse / expand chat |

**No `Ctrl+Enter` for revision:** v6's `runGateLoop` calls `continuePlanner` on every
`Enter`, then `DetectPlanRevision` decides automatically whether the response is a
revision (new `plan-vN.md`) or chat-only (`EventChatResponse`). The user always just
hits `Enter`.

**External editor flow (`^E`, plan gates only):**
1. TUI opens highest `plan-v*.md` in `$EDITOR` via a `tea.Cmd` (non-blocking).
2. User annotates plan with `<<-- [comment]` markers, saves and exits.
3. TUI reloads the plan file; display updates to show edits.
4. User types a follow-up message and hits `Enter` — the orchestrator's
   `continuePlanner` sends the chat along with the modified plan.
5. `DetectPlanRevision` decides if a new version is written.

**Intent** emitted by `HumanChatMode.Pending()` (drains to `m.decisions`):
```go
// decisions channel carries Decision values directly; no separate Intent type needed
// PlanChatMode.Pending() returns nil until user acts, then:
//   Decision{Type: DecisionComment, Comment: text}   — on Enter
//   Decision{Type: DecisionApprove}                  — on ^A
//   Decision{Type: DecisionCancel}                   — on ^C
```

---

### F.4 `AppState` and `model.go` changes (Iter 9 contract)

**Add to `model.go`:**
- `setupOpen bool`, `currentSetup orchestrator.PipelineSetup`
- `AgentStateSkipped AgentState = "skipped"`
- `ContentHumanGate ContentMode`

**Remove from `model.go` (contract — only after Iter 10 refs gone):**
- `StatePlanHistoryDetail` from `AppState` enum
- `ContentPlanHistory` from `ContentMode` enum
- `planHistoryScreen PlanHistoryScreen` field
- `planHistoryVisible()`, `handlePlanHistoryKey()` methods
- All `planHistoryScreen` update/view/layout calls

**Update `messages.go`:**
```go
// Add
type ConfirmSetupIntent struct {
    Setup orchestrator.PipelineSetup
}

// Update (Phase string, not Loop int — v6 uses string RestartPhase)
type RestartRunIntent struct {
    RunPath string
    Phase   orchestrator.RestartPhase
}

// Remove
type OpenPlanHistoryIntent  struct{ ... }
type ClosePlanHistoryIntent struct{ ... }
```

**Replace in `model.go`:**
- `lastRestartFirstMissingAgent string` → `lastRestartPhase orchestrator.RestartPhase`

**`screen_run_detail_keys.go`:** remove `Ctrl+Y` handler emitting `OpenPlanHistoryIntent`.

---

## Part G: Headless removal

Remove `--prompt/--auto-approve/--auto-reject/--auto-init/--plan/--no-execute/--json`,
`isHeadless`, `runPlanOnly/runValidateOnly/runExecOnly`, `RunHeadless/RunHeadlessPlanOnly`;
keep `--config`. `Input` loses `AutoApprove/PlanFile/NoExecute`. **Done last**, after
`GateAfterDeliberation` is the live approval path, so removing `AutoApprove` deletes dead
code. Test coverage is unaffected: `engine_test.go` drives `Start()/Run()` directly, not
the CLI headless path.

---

## The TDD iteration plan (Canon TDD, expand→migrate→contract)

**Method (Kent Beck's Canon TDD), every iteration:**
1. Write/extend the **test list** (scenarios) for the iteration.
2. Turn **one** item into a concrete failing test (RED).
3. Make it pass — and keep all prior tests passing (GREEN). Add discovered scenarios
   to the list as you go.
4. Refactor with tests green.
5. Repeat until the list is empty. **End state: `make build && make test` green.**

**Test style:** black-box through package public APIs; the *only* test double is
`testutil.FakeRunner` at the `harness.Runner` boundary (Claude can't run in unit
tests). Real `SessionDir` on `t.TempDir()`, real file IO, real value types. No
expectation-heavy mocks; assert on returned values, emitted `Event`s, and on-disk
artifacts — not on call sequences. Table-driven for validation/parse/mapping matrices.

**Choke-free ordering rule:** a symbol is deleted **only** in a *contract* iteration,
after its replacement is live and every caller migrated. New fields/types/funcs are
**added alongside** the old ones first (*expand*), callers moved (*migrate*), old ones
removed last (*contract*). Each iteration is independently green.

---

### Iteration 0 — confirm the seam (no production change)
- **List:** `FakeRunner` satisfies the final `harness.Runner`; can script
  (output, sessionID, err) and thread a session id across calls; `SetupPlanFile`
  produces plan content `ExtractPlan` can read.
- Add a tiny `FakeCall` field only if a gate test needs to distinguish a
  *revision* reply from a *chat-only* reply (e.g. returning identical vs changed plan
  text). Keep it minimal.
- **Green:** `make test`. *(Expand — test infra only.)*

### Iteration 1 — `PipelineSetup` (`setup.go`)
- **List:** zero-value → `DefaultPipelineSetup`; `DeliberationLoops 0 → 1`; `<1`/`>10`
  → error; all of R/E/V off → error; valid passes; `DefaultPipelineSetup` shape;
  `resolveSetup` zero-value & loop-fallback.
- RED → GREEN, table-driven. Nothing references it. *(Expand — additive leaf.)*

### Iteration 2 — gate types (`gate.go`)
- **List:** `IsPlanGate()` per position; `HumanGateSet.Active`; `phaseDir(pos)` for all
  four + defensive default; `RestartPhase` const values equal the strings
  `AnalyzeRunCompleteness` will return.
- RED → GREEN, table-driven. *(Expand — additive leaf.)*

### Iteration 3 — session dir helpers + `os.Mkdir` (`agent/session.go`)
- **List:** `SubDir`/`ResearchDir`/`DeliberationDir`/`ExecutionDir`/`ValidationDir`
  return expected joins; a path-taking mkdir helper creates a dir, returns EEXIST-wrapped
  error on a pre-existing dir, wraps permission errors. (Keep public `NewSessionDir`;
  factor the mkdir into a path-taking helper so "exists"/"denied" are deterministic
  without `time.Now()`.)
- RED → GREEN. Switch `MkdirAll`→`Mkdir` via the helper; existing callers unaffected
  (run dir is freshly timestamped). *(Expand + tiny behavior swap.)*

### Iteration 4 — artifact/dialog/version helpers (`orchestrator`)
- **List:** `writeArtifactIn` writes nested file & returns path; `writeArtifactJSONIn`
  round-trips; `appendDialog` appends `## Role`/body/`---`, creates the file, preserves
  prior turns; `highestPlanVersion` 0 on empty, N over `plan-v{1..N}.md`, ignores
  `plan-vX.bak`/non-matching; `findHighestPlan` returns the highest path / "" on empty.
- RED → GREEN on `t.TempDir()`. *(Expand — additive funcs.)*

### Iteration 5 — `AnalyzeRunCompleteness(runPath)` + restart phase (`agent`)
- **List:** new-layout complete run → `Complete=true, RestartPhase=""`; deliberation
  started but no `final_plan.md` → `RestartPhase="deliberation"`; missing
  `run_config.json` → incomplete & unrestartable; execution intended+present,
  validation missing → `RestartPhase="validation"`; research-only setup complete.
- RED → GREEN: add agent-local `runPhases` decode + unified-dir checks; change signature
  to `(runPath)` and update the **single** caller `screen_run_detail.go:55` in the same
  iteration; replace `RunCompleteness.FirstMissingAgent` with `RestartPhase string` and
  update its TUI reads in the same iteration. *(Migrate — atomic, single caller → green.)*

### Iteration 6 — `events.go` EXPAND (add, remove nothing)
- Add `GateRequest.Position HumanGatePosition`; add `EventAgentSkipped`,
  `PhaseDeliberating`. Leave `Type`/`PlanDiff`/`PlanHistory*`/`CriticReport` in place.
- **List:** constructing a `GateRequest{Position:…}` and an `EventAgentSkipped` event
  compiles and round-trips through `ApplyEvent` (TUI still on old fields — untouched).
- GREEN: TUI compiles unchanged. *(Expand.)*

### Iteration 7 — `runGateLoop` built & tested in isolation (`human_gate.go`)
- **List (black-box, `FakeRunner` + tmp `SessionDir`):**
  `Comment_Revision` → new `plan-vN.md` in phase dir + updated finalPlan + `## Agent`
  dialog turn; `Comment_ChatOnly` → `EventChatResponse`, no new plan version, plan
  unchanged, `(chat only)` dialog; `RevisionError` → `EventError`, never returns empty
  plan as approval; `Approve` → returns plan, writes `gate_decision.json`; `Cancel` →
  `errGateCancelled`; `PauseApprove` / `PauseStrayCommentNoop` (pos=execution);
  `Continuation` → one session id across turns (assert threaded `SessionID`).
- RED → GREEN per scenario. Function exists and is fully tested but the **inline gate
  in `run()` still serves production** — both compile. *(Expand — parallel implementation.)*

### Iteration 8 — engine extraction + go-live (behavior-preserving, sub-steps each green)
Each sub-step ends with `make build && make test` green (existing `engine_test.go` is
the regression net):
- **8a** Extract `runResearch`/`runExecution`/`runValidation`/`runDeliberation` from
  inline `run()` into `engine_phases.go`/`engine_deliberation.go`; `run()` calls them.
  Behavior identical; artifacts still flat for now. *(Refactor move.)*
- **8b** `run()` → `(Result, error)`; move terminal events to `Start()`’s goroutine.
  `Run()` wrapper unchanged (reads channel). *(Refactor.)*
- **8c** Switch artifact writes in the extracted phases to the unified `<phase>/`
  layout (`writeArtifactIn`, numbered `plan-vN.md`, `## Critic (round k)` dialog);
  update `engine_test.go` assertions to the new paths in the same sub-step. *(Migrate.)*
- **8d** Replace the inline plan gate (888-1207) with `runGateLoop(GateAfterDeliberation,
  …, del.PlanSessionID)`; remove the inline gate code. Gate tests + engine tests green.
  *(Migrate — new gate now in production.)*
- **8e** Add `Input.Setup`, `resolveSetup`+`Validate`, the other gate-position calls,
  `EventAgentSkipped` emits, `runLog`, `startQuestionBridge`. *(Expand on engine.)*
- **8f** `applyRestartSkip` + `RestartInput{RunPath, Phase}` replacing the `goto`s;
  rewrite `copyCompletedArtifacts` to copy phase dirs; seed from the new session.
  `engine_restart_test.go` green. *(Migrate.)*

> `planRepo`/git writes still run during 8 (harmless duplicate record). They are
> removed in Iteration 10, keeping every sub-step green.

### Iteration 9 — TUI migrate to `Position` + new layout (old event fields still present)
- **List (TUI):** gate handler renders rich vs pause by `Position` (window-resize
  stable, no mutation in `View()`); `EventAgentSkipped` appends a skipped row;
  setup-panel nav/toggle/stepper/confirm + validation error; restart intent carries
  `Phase`.
- RED → GREEN. Stop reading `PlanDiff`/`PlanHistory*` (still declared, now unused).
  *(Migrate.)*

### Iteration 10 — CONTRACT: delete the now-dead code (build stays green — no refs remain)
- `events.go`: remove `Type`/`GateType`/`GatePlanApproval`/`PlanDiff`/`PlanHistoryDir`/
  `PlanHistoryHeadSHA`/`CriticReport`.
- TUI: remove `planDiff`/`planHistoryDir`/`planHistoryHeadSHA`/`planDiffLineOffset`;
  delete plan-history viewer files + their tests; remove `Open/Close/RevertPlanIntent`,
  `Ctrl+Y` handlers, `StatePlanHistoryDetail`/`ContentPlanHistory`.
- `internal/plan`: delete `gitrepo.go`/`gitrepo_history.go` + their tests; remove **all
  39** `planRepo` refs across `internal/orchestrator/` (not just the 37 in `engine.go`);
  update any `engine_test.go` expectations that asserted git artifacts.
- **Verify:** `grep -rn "planRepo\|PlanDiff\|GateType\|internal/pipeline" .` empty;
  `make build && make test` green. *(Contract.)*

### Iteration 11 — CONTRACT: headless removal (Part G)
- Remove `Input.{AutoApprove,PlanFile,NoExecute}` (now dead after 8d), the CLI flags,
  `runPlanOnly/runValidateOnly/runExecOnly`, `RunHeadless/RunHeadlessPlanOnly`,
  `isHeadless`; update `testutil`/tests that set those fields.
- **Verify:** `grep -rn "AutoApprove\|NoExecute\|RunHeadless" internal/ cmd/` empty;
  green. *(Contract.)*

### Iteration 12 — docs
- README + `.github/copilot-instructions.md` (and the routed `agent`/`tui` files) to
  TUI-only; document the unified `<phase>/{plan-vN.md, dialog.md}` layout. *(No code.)*

---

## Files Modified

| File | Change |
|------|--------|
| `internal/orchestrator/setup.go` | **NEW** — `PipelineSetup`, `DefaultPipelineSetup()`, `Validate()`, `resolveSetup()` |
| `internal/orchestrator/gate.go` | **NEW** — `HumanGatePosition`, `HumanGateSet`, `phaseDir()`, `RestartPhase`, `RestartInput` |
| `internal/orchestrator/human_gate.go` | **NEW** — `runGateLoop`, `gateRunner`, `errGateCancelled` |
| `internal/orchestrator/engine_deliberation.go` | **NEW** — `runDeliberation`, `deliberationResult` |
| `internal/orchestrator/engine_phases.go` | **NEW** — `runResearch`, `runExecution`, `runValidation` |
| `internal/orchestrator/engine_restart.go` | **NEW** — `applyRestartSkip`, rewritten `copyCompletedArtifacts` |
| `internal/orchestrator/run_log.go` | **NEW** — `runLog` struct, `copySessionLog` |
| `internal/orchestrator/artifacts.go` | **NEW** — `writeArtifactIn`, `writeArtifactJSONIn`, `appendDialog`, `highestPlanVersion`, `findHighestPlan` |
| `internal/orchestrator/events.go` | Add `GateRequest.Position HumanGatePosition`, `EventAgentSkipped`, `PhaseDeliberating`; contract (Iter 10): remove `GateType`, `GatePlanApproval`, `PlanDiff`, `PlanHistoryDir`, `PlanHistoryHeadSHA`, `CriticReport` |
| `internal/orchestrator/engine.go` | `run()` → `(Result, error)`; add `Input.Setup PipelineSetup`; shrinks to <500 lines after extractions |
| `internal/agent/session.go` | Add `SubDir`, `ResearchDir`, `DeliberationDir`, `ExecutionDir`, `ValidationDir`; rewrite `AnalyzeRunCompleteness(runPath)` (drop `detail`); replace `RunCompleteness.FirstMissingAgent` with `RestartPhase string` |
| `internal/tui/mode_human_chat.go` | **NEW** — `HumanChatMode` interface, `PlanChatMode`, `SimpleChatMode`, `newHumanChatMode` |
| `internal/tui/model.go` | Add `setupOpen`, `currentSetup`, `AgentStateSkipped`, `ContentHumanGate`; contract (Iter 10): remove `StatePlanHistoryDetail`, `ContentPlanHistory`, `planHistoryScreen`; update `RestartRunIntent` |
| `internal/tui/messages.go` | Add `ConfirmSetupIntent`; update `RestartRunIntent`; contract (Iter 10): remove `OpenPlanHistoryIntent`, `ClosePlanHistoryIntent` |
| `internal/tui/screen_pipeline.go` | Iter 9: add `activeChat HumanChatMode`, handle `EventGateRequest.Position`, `EventAgentSkipped`; add `ContentHumanGate` delegation |
| `internal/tui/screen_pipeline_keys.go` | Add `case ContentHumanGate:` in `viewFooter()` |
| `internal/tui/screen_run_detail.go` | Update `AnalyzeRunCompleteness` call (drop `detail` arg), read `RestartPhase` instead of `FirstMissingAgent` |
| `internal/tui/screen_run_detail_keys.go` | Contract (Iter 10): remove `Ctrl+Y` / `OpenPlanHistoryIntent` handler |
| `internal/tui/plan_history_loader.go` | **DELETE** (Iter 10) |
| `internal/tui/screen_plan_history.go` | **DELETE** (Iter 10) |
| `internal/tui/screen_plan_history_test.go` | **DELETE** (Iter 10) |
| `internal/tui/plan_history_model_test.go` | **DELETE** (Iter 10) |
| `internal/plan/gitrepo.go` | **DELETE** (Iter 10) |
| `internal/plan/gitrepo_history.go` | **DELETE** (Iter 10) |
| `cmd/orqestra/main.go` | Iter 11: remove `--prompt`, `--auto-approve`, `--auto-reject`, `--auto-init`, `--plan`, `--no-execute`, `--json`; remove `isHeadless`, `runPlanOnly/runValidateOnly/runExecOnly`; keep `--config` |
| `internal/tui/tui.go` | Iter 11: remove `RunHeadless`, `RunHeadlessPlanOnly` |
| `README.md` / `.github/copilot-instructions.md` | Iter 12: TUI-only; unified layout docs |

---

## Why this is choke-free (the dependency argument)

The only deletions that previously broke the build are `events.go` field removals
(TUI-dependent), plan-history viewer + `planRepo` (git-dependent), and headless flags
(`run()`-dependent). v6 gates each behind a migration:

- `events.go` removal (Iter 10) is **after** the TUI reads `Position` (Iter 9).
- `planRepo`/viewer removal (Iter 10) is **after** the gate stops needing the micro-repo
  (Iter 8d) and the TUI stops rendering history (Iter 9).
- headless removal (Iter 11) is **after** `GateAfterDeliberation` replaces `AutoApprove`
  (Iter 8d).

Everything before Iter 8 is purely additive (new files/funcs/fields). So
`go build ./...` and `go test ./...` are green at the end of all 13 iterations.

---

## Verification

### Automated (per iteration)

- **Per iteration:** `make build && make test` (add `make test-integration` for
  iterations touching sandbox/harness; `make test-sandbox` is macOS-only).
- **No package / no cycle:** `grep -rn "internal/pipeline" .` empty;
  `go list -deps ./internal/agent | grep orchestrator` empty.
- **No dead subsystem leftovers (after Iter 11):**
  `grep -rn "planRepo\|PlanDiff\|GateType\|RunHeadless\|AutoApprove" internal/ cmd/`
  empty.
- **Continuation / no token regression:** `human_gate_test.go` `Continuation` asserts
  one `claude_session_id` across deliberation + gate turns; cross-check
  `deliberation/*_session.jsonl` and `~/.claude/projects/.../*.jsonl`.
- **Layout:** a default run produces `deliberation/plan-v1.md` (+`plan-v2.md` after the
  loop), `deliberation/dialog.md`, `final_plan.md`, `run_config.json`.

### Manual smoke (handoff — interactive TUI + real `claude` required)

`make build && ./bin/orqestra` for each scenario:

1. **Default run:** prompt screen appears with setup overlay panel above textarea;
   footer shows `^P Pipeline Setup` in low-contrast.
2. **Setup panel:** press `^P` → panel focuses; `↑/↓` moves cursor; `←/→` on
   "Architect <-> Critic" steps 1→10 clamped; `←/→` on Research toggles Enabled/Disabled;
   Enter confirms and closes; prompt submit starts pipeline with configured phases.
3. **DeliberationLoops=3:** sidebar shows architect/critic cycling 3 times;
   `deliberation/plan-v1.md` through `deliberation/plan-v3.md` appear on disk.
4. **Gate after deliberation (default):** split-view appears (pipeline history top,
   plan markdown middle, textarea bottom); footer shows `Enter send  ^E editor  ^A approve  ^C abort`.
5. **Chat in gate:** type a message, hit `Enter` → architect streams response;
   if plan changed, `deliberation/plan-v4.md` appears; if chat-only, no new version.
6. **`^E` in gate:** editor opens `deliberation/plan-v3.md`; add `<<-- [comment] test`;
   save and exit; type follow-up message; hit `Enter` → architect responds to the edits.
7. **Approve gate (`^A`):** pipeline continues to execution; `gate_decision.json` written
   in `deliberation/`.
8. **Abort gate (`^C`):** run terminates; `gate_decision.json` records cancel.
9. **`^O` collapse chat:** chat textarea disappears; only pipeline history + plan visible;
   `^O` again restores.
10. **Disable Research:** architect receives raw prompt; `research/` dir absent.
11. **Disable Execution:** pipeline stops after gate; `execution/` dir absent.
12. **Gate after execution (pause gate):** `SimpleChatMode` — no plan viewer; just chat
    input + approve/abort.
13. **Skipped agents:** disabled phases show a skipped row in the sidebar (not silent).
14. **Restart:** complete 2-loop run; restart from `StateRunDetail` → new session picks
    up `deliberation/plan-v2.md` as seed; fresh architect session (cold-start).

---

## Known risks

1. **Manual/e2e verification is a handoff.** Unit + integration TDD (through
   `engine.Run/Start` + `FakeRunner`) covers logic, layout, gate flow, restart, and
   completeness without Claude. The interactive deliberation-gate smoke and
   `make test-e2e` (real `claude` + API; sandbox needs `sandbox-exec`) remain yours to
   run. v6 maximizes what the automated suite proves so the handoff is small.
2. `engine.go` must end < 500 lines; lines 1-294 (Engine/Input/Runners + `run*`
   helpers) mostly stay, so the coordinator must move enough into siblings — watch this
   during Iter 8 and split further if needed.
3. `screen_pipeline.go` stays ~1530 lines; v6 edits only enumerated handlers (full
   decomposition is follow-up). Adding `activeChat HumanChatMode` is +1 field; the
   full move of plan-review fields into `PlanChatMode` is follow-up decomposition.
4. Restart uses a fresh architect session by necessity (original process is gone),
   seeded from the highest `plan-vN.md` — not a normal-flow regression (normal flow keeps
   continuation via `planSessionID`).
5. Old-format runs (no `run_config.json`, flat artifacts) are shown incomplete and are
   not restartable. No backward-compat shim.
6. During Iter 8 the git micro-repo and numbered plans both write (duplicate record);
   the micro-repo is removed in Iter 10. Intentional, keeps every sub-step green.
7. Pause gates (Execution/Validation) are wired but default-off; post-execution cancel
   preserves the worktree branch and skips merge.
8. The post-run merge is guarded on `setup.Execution` + worktree created.
9. **Gate chat uses session continuation, not `RunInteractive`.** An earlier design
   (v2 D10/WP7) proposed a `RunInteractive` harness method using Claude CLI's
   `--input-format stream-json` for bidirectional streaming — the architect process
   would stay alive while the user chatted. v6 instead uses `continuePlanner`
   (session ID continuation): each user turn spawns a new Claude invocation that
   continues the session. This avoids the undocumented `--input-format stream-json`
   protocol but means each gate turn pays a cold-start cost. If latency becomes
   unacceptable, revisit `RunInteractive` as an upgrade path; the gate interface
   (`runGateLoop`) is designed to hide this choice from callers.
