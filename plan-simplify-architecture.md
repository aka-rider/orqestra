# Plan: simplify orqestra to a linear feature-factory pipeline

Status: **WP1 DONE** (uncommitted in working tree) · critic-vetted (orqestra-critic:
`blockers: 2, risks: 5`, all incorporated) · companion: `docs/bug-journal-2026-07-02.md` (J*
references below).

## Context

Orqestra is, in essence, a glorified bash script: launch Claude Code with the right arguments,
read/write stdio, end in a git commit. In practice "more fixes = more bugs" — the 2026-07-02 review
journaled 40+ defects, and the cause is architectural: the same fact travels 2–4 parallel channels
that must be manually reconciled; deliverables come back via side-channels needing correlation
machinery; two subprocess abstractions describe the same process; three generations of design
coexist (dead writers with live readers, half-removed features). This plan simplifies to
"one pipe out, one pipe in" for the linear pipeline
`prompt → plan → (optional human gate) → execute → validate → integrate/commit`, extensible to
future QA/PM roles by a config entry + one pipeline call site instead of ~15 edit sites.

## Owner constraints (binding)

1. Agents run `--dangerously-skip-permissions`; the seatbelt sandbox is the safety boundary.
2. Reporter agents (researcher/architect/critic) tend to jump into implementation instead of
   reporting; `permission-mode: plan` conflicts with researcher/critic roles; agents often print a
   perfect report to the console but "forget" to call SubmitReport. The tiered report recovery
   ("scavenging") is therefore intentional recall — keep SubmitReport + tiers + early-stop + drift
   nudges; unify and fix them, don't delete them.
3. **Fail-fast pre-flight, fail-forward once tokens are spent.** Cheap boundaries (config, spec
   build, worktree creation, branch detection) fail closed. After hours/millions of tokens, recover
   via tiers and preserved state, truthfully labeled; failing a still-recoverable pipeline at the
   final stage is itself a defect.
4. The pipeline stays a linear "bash script" shape.

## Root causes

- **RC1 — The same fact travels multiple channels, and the channels disagree.** Stream data:
  StreamRing + StreamHistoryStore + streamCh + notify (J14, J27). Terminal state: `Complete` +
  `Finished` (J3, J23). User intent: `Control.Submit` + `Engine.SendAnswer` + `ctrl.Input` +
  `cancelCause` — four back-channels, two with stale-buffer traps (J2, J17). The agent's
  deliverable: SubmitReport + plan file + JSONL probe, arbitrated by a heuristic, implemented three
  times.
- **RC2 — Observation is state-sync + diffing, not events.** The pipeline reduces its event stream
  into an `ObsStore` snapshot; the TUI DIFFS consecutive snapshots (`knownAgents`,
  `seenGateMarkdown`, `HasQuestion && !QuestionOpen()`) to reconstruct the very events the pipeline
  started with. Every derived transition is a hand-maintained bug site (J25, J26).
- **RC3 — Deliverables return via side-channels the harness must re-correlate.** Reports correlate
  through `reportKey(agentID, sessionID)` maps re-armed by `RegisterSession` from stream-observed
  session IDs carried on a dedicated channel through a fanout sink. Necessary feature, accidental
  implementation.
- **RC4 — Two parallel subprocess abstractions.** `ClaudeCLI`+options and `ProcessSpec` describe
  the same subprocess; args merged by two implementations (J21), env built by faking a `ClaudeCLI`
  (J22); adding a role touches ~15 sites across config/main/orchestrator/TUI.
- **RC5 — Three generations of design coexist.** Old phase-dir artifact schema (dead writer
  `artifacts.go`, live reader `run_history.go` — J11), the EventComplete era (dead
  `MergeConflictInfo`, phases, decisions — J10), the current RunPipeline. Restart (J11), headless
  (J18), AutoApprove (J26) are half-removed.
- **RC6 — Global mutable state.** `slog.SetDefault` swapped in three places (J4); Engine-owned
  QuestionBridge re-launched per run (J5, J41).

## Target architecture — "one pipe out, one pipe in"

```
main ── config.Load ──► roles: map[string]RoleConfig (one table, role CLASS: reporter/executor/utility)
                        harness.SpecForRole(cfg, role) (ONE spec builder; ClaudeCLI type deleted)

Engine.Start(ctx, input) ──► RunHandle{ Events <-chan RunEvent; Intents chan<- Intent }

pipeline goroutine (single writer):
    runPipeline(ctx, deps)         // literal control flow (kept — healthiest component)
      │ emits ──► emitter          // ONE ordered event channel; lifecycle events
      │                            // guaranteed, text deltas coalesced
      │ reads ◄── Intents          // gate decisions & question answers, ID-tagged
      │                            // (GateID/QuestionID) — stale submissions discarded
      ├─ agents: harness.Run(ctx, spec, stdin, sink)   // invocation mode from role class,
      │     never from policy presence; group-killed on cancel (WP1 ✓)
      ├─ reports: ONE ReportHarvester — SubmitReport → plan-file-if-changed → final message,
      │     provenance recorded in meta + events; nonce-correlated via --invocation-id
      └─ artifacts: rundir package — ONE schema, same accessors for write & read

TUI  = one waitForEvent adapter → tea.Msg per event; no snapshot diffing
headless = same Events consumed by a line printer; gates auto-approved/disabled
```

What stays deliberately: `RunPipeline` literal control flow; seatbelt sandbox; worktree isolation;
give-up-by-default integrator; qarun/replayclaude lanes; the MCP bridge for BOTH questions and
reports (per-run-owned, nonce-correlated, concurrent, readiness handshake); budget guard and ALL
policies including drift; the tiered report recovery as one implementation with provenance.

## Standing rules for every WP (falsifiability discipline)

- **RED-first gate proof.** Every new gate test MUST be demonstrated failing on the pre-change tree
  (write test → run → observe named failure → apply change → observe green). A gate that was never
  red is not falsifiable; the worker quotes both runs in its report.
- **Verdict discipline.** A WP is done only with a fresh `QA-ATTEST … SUITE-COMPLETE` from
  `make test` quoted; a hang is NO-VERDICT = failure (CLAUDE.md §8).
- **`-race` mandatory** for orchestrator/harness/mcp/tui WPs.
- **Grep gates** are exact commands whose output must be empty.
- One WP = one branch/commit unit; no WP starts until its dependencies are merged.

---

## Phase 1 — stop the bleeding

### WP1 — Kill the process group on cancel/timeout (J32/J15) — ✅ DONE 2026-07-02
`internal/harness/exec.go`: `cmd.Cancel` = negative-PID SIGKILL (ESRCH/ErrProcessDone = success,
other errors wrapped), `cmd.WaitDelay = 5s`, stdin-writer goroutine joined (J42). Fixture
`testdata/hold_stdout.sh` reproduces the wedge (grandchild ignoring SIGTERM holding stdout).
RED-first proven: `TestRunCancelKillsProcessGroup` failed at 10.22s bounded on pre-change tree,
passes at 0.01s after (5× stable). `QA-ATTEST commit=70cf4e1 dur=6s SUITE-COMPLETE` quoted.
`Sandbox.Run` left in place for WP6.

### WP2 — Single terminal write (J3/J23) — deps: none
- Delete `Complete` from `Observer` (observer.go:15) and `ObsStore` (obs_store.go:221-228); delete
  the `sc.Obs.Complete` calls (run_pipeline.go:142,182); `startNew`'s `obs.Finished`
  (engine_pipeline.go:135) is the only terminal writer.
- **QA gate:** subscribe to `NotifyCh`, run a plan-only pipeline (replayclaude fixture); at the
  FIRST observation of `Terminal.Done`, `Result.FinalPlan` is non-empty and `RunDir` set.
  RED-first. Grep gate: `grep -rn "func (s \*ObsStore) Complete" internal/` → empty.

### WP3 — Fail-closed isolation, truthful integrate (J8/J9/J10) — deps: none
- `step_execute.go:32-35`: `CurrentBranch` failure returns an error (pre-token-spend = fail-fast
  zone). `step_integrate.go:47-50`: worktree present + empty TargetBranch → error, not success.
  Add `ConflictFiles []string` to `Result` (engine_types.go:40) populated from `IntegrateOutput`
  (already set at step_integrate.go:145); delete `MergeConflictInfo` and repoint root CLAUDE.md
  §5.4 at the Result field. TUI done-screen renders conflict files when present.
- **QA gate:** three named tests (branch-detect error → ExecuteStep error; empty-target integrate →
  error; conflict give-up → `Result.ConflictFiles` non-empty and visible in TUI snapshot test).
  Each RED-first.

### WP4a — Stale gate decision + AutoApprove wiring (J2/J26) — deps: none
- `control.go:55-65`: drain any buffered decision at gate open before blocking.
  `run_pipeline.go:111-134`: `DecisionEdit` with `AutoApprove && Comment==""` is approval — adopt
  `EditedContent` as the plan and break the loop.
- **QA gate:** (a) `Submit` before the gate opens does NOT satisfy the gate (RED today); (b)
  edit→confirm produces exactly ONE gate cycle and the approved plan equals the edited text (RED
  today: gate re-opens). Both `-race`.

### WP4b — Bridge/run lifecycle (J5/J41) — deps: WP4a
- `QuestionBridge.Run` starts ONCE for the engine lifetime, in `tui.Run`/main (and the future
  headless main), not in `startNew`.
- Per-run routing (spelled out): `startNew` derives `runCtx, runCancel := context.WithCancel(ctx)`
  and defers `runCancel()` after `RunPipeline` returns; the questions-forwarder goroutine selects
  on `bridge.Questions()` / `runCtx.Done()`, and `startNew`'s defer JOINS the forwarder before the
  run goroutine returns — at most one consumer of the shared `Questions()` channel at any time
  (runs are sequential), so a question can never land in a finished run's store.
- **QA gate:** two sequential `Start` calls on one Engine: (a) run-1's forwarder has exited before
  run-2 starts (instrumented done channel; RED today), (b) a question during run-2 lands in run-2's
  store, (c) exactly one bound socket listener across both runs (RED today).

### WP5 — Question lifecycle (J25/J17) — deps: WP4b
- Add `ID string` to `mcp.ToolCall`/`Answer` (generated per question in the bridge);
  `ObsStore.ClearQuestion` called on `SubmitQuestionAnswerIntent`; `handleQuestion` rejects an
  answer whose ID mismatches (drops stale buffered answers).
- **QA gate:** answer a question, emit a stream event, snapshot shows NO re-opened question (RED
  today per J25); stale pre-buffered answer does not satisfy a new question (RED today).

### WP6 — Tier-A dead-code sweep + fail-closed hygiene (inventory Tier A, J4, J24, J19) — deps: WP1, WP2
- Delete ONLY Tier A of the journal's dead-code inventory (critic-verified isolated). Tier B
  (stream_history/StreamRing chain, steering, restart plumbing, `EditPlanIntent`) is EXPLICITLY
  deferred to WP10 — it has live TUI references and deleting it here does not compile (critic B1).
- Logger: `slog.SetDefault` calls in `engine_pipeline.go:45-47` removed; per-run logger flows only
  through `StepContext.Log`. `resolveSetup` (J24): `Input.Setup` becomes `*PipelineSetup` (nil =
  defaults; non-nil used as-is, `Validate()` enforced). `main.go` conflict-spec build moves to
  startup, fatal on error (J19).
- **QA gate:** grep-gate list for Tier-A symbols, e.g.
  `grep -rn "writeArtifactIn\|scanPlansDirectory\|buildFinalArgs\|MergeInto\|slog.SetDefault" internal/ cmd/ | grep -v _test.go`
  → empty; `make build` + full `make test` QA-ATTEST; `make lint`; `make test-sandbox`.

### WP7 — Harness correctness bundle (J34/J37/J36-cap/J42-symlink/J38?) — deps: none
- `stream_event.go:220`: first-wins session ID (match comment at :216 and the supervisor).
  `logpath.go:132`: use `maxJSONLLineBytes` (one shared const). `mcp/frame.go:27` +
  `server.go:122`: raise cap to match. `plan_extract.go:137`: `filepath.EvalSymlinks` before the
  prefix check.
- `CwdToDash` (logpath.go:15): EMPIRICAL GATE FIRST — run `claude` from a dotted-path repo and read
  the actual `~/.claude/projects/` name. Fix only if divergence is confirmed; otherwise record and
  change nothing.
- NOTE (critic): the dead arg-builder duplicate is `buildFinalArgs` (WP6 deletes it);
  `mergeInlineMCP` (exec.go:421) is LIVE — do not touch. J20 sort item dropped: final args are
  already deterministic (sorted-map JSON collapse, exec.go:399,456) — no RED-able gate exists.
- **QA gate:** unit tests per item, RED-first (two-session-id stream → RunResult carries the FIRST;
  1–2 MB JSONL line → `ExtractPlanFilePath` succeeds; >1 MB report frame round-trips the bridge;
  symlinked plan path outside `~/.claude/plans/` rejected). Fuzz suites stay green.

### WP8 — Validation verdict surfaced (J33/J12) — deps: WP2
- Keep validation advisory (fail-forward zone) but truthful: put `Parsed.Verdict` + raw output into
  `Result`, emit an observer/agent event so the TUI done-screen shows PASS/FAIL/UNKNOWN;
  `step_deliberate.go:148-157` (J12): a fallback revision writes meta status `"fallback"` (not
  `"done"`). Optional config `pipeline.block_merge_on_validation_fail: bool` gates Integrate
  (default false = today's behavior, now honest).
- **QA gate:** replayclaude fixture where self-validation reports FAILED → `Result` carries the
  failed verdict, TUI shows it (snapshot test), and with the config flag set, Integrate is skipped
  with an explicit event. RED-first (today `.Parsed` is discarded — grep proves no reader).

## Phase 2 — one pipe out, one pipe in

### WP9 — Event bus behind the Observer (additive) — deps: WP2, WP6
- New `internal/orchestrator/event.go`: `RunEvent` union (PhaseStarted, AgentStarted, Delta,
  ToolCall, ToolResult, AgentDone{usage}, AgentFallback, GateOpened{GateID}, QuestionAsked{ID},
  ValidationVerdict, MergeConflict, RunFinished{Result, Err}) + `emitter` goroutine: lifecycle
  events guaranteed, deltas coalesced when the consumer lags. Pipeline/steps emit RunEvents; a
  compatibility adapter feeds the existing ObsStore so the TUI is untouched in this WP.
- **QA gate:** emitter unit tests — (a) lifecycle events NEVER dropped under a slow consumer;
  (b) delta coalescing preserves text concatenation; (c) AgentStarted precedes its first Delta.
  Existing TUI + orchestrator suites green through the adapter.

### WP10 — TUI consumes the bus; ObsStore/Control + Tier-B dead code deleted — deps: WP9, WP5
- One `waitForEvent` tea.Cmd; timeline/agent rows/gate/question driven by typed events (delete
  `ApplySnapshot` diffing, `knownAgents`, `seenGateMarkdown`, `streamCh` drains, tick-drain
  duality, `lastRev`). Intents: single `Intents chan Intent` on `RunHandle` with
  `GateID`/`QuestionID` correlation (replaces `Control` + `Engine.SendAnswer`); pipeline discards
  mismatched IDs. `RunHandle{Events, Intents}` is the whole TUI⇄engine surface.
- Tier-B deletions land HERE with their TUI call sites (critic B1/R1): `stream_history.go` +
  `StreamRing` + `PipelineScreen.streamBuf`/`AgentActivities` chain, `Control.Input`/
  `RegisterInput` + steering intent (model_intents.go:106), restart plumbing + run-detail restart
  UI, `EditPlanIntent`.
- **QA gate:** TUI tests rewritten event-driven; named regression tests for the bug behaviors: no
  question re-open (J25), one gate cycle per revision (J26), final delta rendered before
  RunFinished (J30), token totals accumulate across architect passes (J40). Grep gates incl. Tier
  B: `ApplySnapshot\|ObsStore\|NotifyCh\|StreamCh\|StreamHistoryStore\|RegisterInput\|RestartFrom\|AnalyzeRunCompleteness`
  → empty (non-test).

## Phase 3 — one report harvester

### WP11 — ReportHarvester with provenance (RC3, J35) — deps: WP9
- New `internal/orchestrator/report_harvest.go`: one component, tier order per role class
  (reporter: SubmitReport → plan-file-if-changed-this-invocation → final-message; executor:
  SubmitReport → raw output). Provenance (`tier`, source path/session) recorded in step meta JSON
  and emitted as a RunEvent — a tier-3 scavenge is fine, a SILENT tier-3 scavenge is not. Replaces
  `extractReport` (step.go:85) + inline copies (step_execute.go:85-90, step_revise.go:59-76).
  `looksLikeReport` stays as a per-tier advisory check whose rejection is logged with the tier.
  J35 fix: capture the plan file's pre-invocation mtime/hash; tier 2 fires only when it changed.
- **QA gate:** table-driven tests over replayclaude fixtures: report-only-in-console → tier 3 wins
  with provenance (RED today for the revise path — returns stale plan, J35); unchanged-plan-file
  revision returns the console report (RED today); provenance present in every `*_meta.json`.

### WP12 — Bridge transport: nonce correlation, concurrency, readiness (J36/J16/J34-reports) — deps: WP11
- Per-invocation `--invocation-id` injected into the inline MCP server args when a step copies its
  spec; envelope carries it; bridge stores reports by nonce (delete `RegisterSession`, `sessions`,
  `reportKey`, `fanoutSink.sessionC`). `handleConnection` per-goroutine. `Run` binds the listener
  synchronously and exposes `Ready() <-chan struct{}`; the engine waits for readiness before the
  first agent starts.
- **QA gate:** integration tests over the real unix socket: (a) SubmitReport delivered while a
  question is pending (RED today — serialized accept loop); (b) agent dialing immediately after
  engine start never sees ECONNREFUSED (gate = 100 iterations green); (c) two sequential
  invocations of the same agentID correlate to their own reports.

### WP13 — One invocation mode per role class (J6) — deps: WP11
- **WP13.0 spike (mandatory, before any code):** the stdin-close-ends-the-process assumption is
  UNDOCUMENTED claude-CLI behavior the team previously rejected (plan-flexible-pipeline.md:804-810;
  critic R2). Verify with a throwaway experiment: start
  `claude -p "" --input-format stream-json --output-format stream-json`, send one message, close
  stdin after the result event, observe whether/when the process exits. Record the finding here.
- If CONFIRMED: input plane becomes a role-class property (reporter/executor: always on; utility:
  one-shot `-p`), stdin closes on the result event, early-stop-on-report retained belt-and-braces.
- If REFUTED: keep early-stop-on-SubmitReport as the sole terminator (today's mechanism,
  step_execute.go:82-84); still make the input plane a role-class property so policy presence no
  longer flips invocation mode (the J6 fix stands either way); the supervisor additionally cancels
  after the result event.
- **QA gate:** args-matrix test: identical spec with SilenceGuard on vs off produces IDENTICAL
  subprocess args and mode (RED today — J6 flips `-p` ↔ `--input-format stream-json`). The
  exit-behavior gate follows the spike branch; either way bounded (≤10 s select) so a wrong
  assumption yields RED, not NO-VERDICT.

## Phase 4 — one spec system, one artifact schema, headless

### WP14 — Role table + single spec builder (RC4) — deps: WP13
- `config`: `Roles map[string]RoleConfig` (class, model ref, prompts, tools, guards, timeouts) with
  the current roles as embedded defaults; `harness.SpecForRole(cfg, roleName, sandboxTier)`
  replaces `ClaudeCLI` + options + `buildEngine`'s ~200 lines of per-role assembly. Adding a role =
  one YAML block + one pipeline call site.
- **QA gate:** golden test captured BEFORE the refactor: for each current spec, serialize
  args+env+knobs; after the refactor the builder reproduces them byte-identical. Second gate: a
  test config defining a novel `qa` role yields a complete runnable spec with zero Go changes.
  Grep gate: `type ClaudeCLI` → empty.

### WP15 — `rundir` package: one artifact schema (J11 root, J43) — deps: WP6
- `internal/rundir`: typed layout (Prompt, FinalPlan, WorkerOutput, Validation, StepMeta list,
  provenance) with Save/Load; steps write through it; `run_history.go` list/detail read through it
  (delete `readStringArtifact`/glob scraping). Restart stays deleted; this schema is its future
  foundation. START WRITING `StepMeta.ClaudeSessionLogPath` — the run-detail log viewer reads it
  today but no writer exists (J43).
- **QA gate:** round-trip test: full replayclaude pipeline → `ListRuns`/`LoadRunDetail` return
  status, duration, tokens, plan, verdict (RED today for status="" cases); grep gate:
  `filepath.Glob(filepath.Join(runPath` → empty.

### WP16 — Headless mode (J18) — deps: WP10, WP15
- `cmd/orqestra`: `--prompt <text> [--auto-approve] [--plan-only]` → `Engine.Start`, consume
  Events, print lifecycle lines to stdout, auto-approve gates (or fail if a gate fires without
  `--auto-approve`), exit code mapped from RunFinished (0/1/130 per main.go's existing table).
  Root CLAUDE.md §4 updated to match reality.
- **QA gate:** e2e (integration tag): `./orqestra --prompt … --auto-approve --config <replayclaude
  config>` exits 0, artifacts exist under `.orqestra/sessions/`, stdout contains phase lines;
  failure fixture exits 1. This lane replaces the `make test-e2e` placeholder.

## Plan-level verification

- Every WP gate is RED-first-proven; the worker's report quotes the red AND green runs.
- Critic pass DONE (orqestra-critic): B1 → inventory split Tier A/B, WP6 restricted, Tier B moved
  to WP10; B2 → J20 downgraded, WP7 determinism gate dropped; R2 → WP13.0 spike; R3 →
  `mergeInlineMCP` kept; R4 → WP4 split with routing spelled out; R5 → WP1 bounded-select RED.
- Phase boundaries are merge points: full `make test` + `make test-integration` QA-ATTEST quoted at
  each phase end; `make test-sandbox` for sandbox-touching WPs.
