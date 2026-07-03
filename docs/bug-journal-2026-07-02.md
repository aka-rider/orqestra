# Bug journal — architecture review 2026-07-02

Branch: `tui-rehaul`. Every entry cites file:line into the PRE-REFACTOR tree (`3ebaa5f` and
earlier) and states a concrete failure scenario. Companion doc: `plan-simplify-architecture.md`
(root causes, target architecture, work packages WP1–WP16 — ALL EXECUTED 2026-07-03, commits
620150b..8dc9201).

## Resolution table (post-execution, 2026-07-03)

| Status | Entries |
|---|---|
| Fixed by a WP | J2(WP4a) J3(WP2) J4(WP6) J5(WP4b) J6(WP13) J8(WP3) J9(WP3) J10(WP3) J11(WP15+WP10) J12(WP8, revErr branch) J15(WP1) J16(WP12) J17(WP5) J18(WP16) J19(WP6) J21(WP6+WP14 — one arg builder remains) J22(WP14, env.go) J23(WP2) J24(WP6) J25(WP5) J26(WP4a) J32(WP1) J33(WP8) J34(WP7) J35(WP11) J36(WP12+WP7) J37(WP7) J38(WP7 — divergence CONFIRMED, claude folds `.`/`_`) J40(WP10) J41(WP4b) J42(WP1+WP7) J43(WP15) |
| Moot — machinery deleted by the event-bus rewrite | J1 J7 J14 J27 J28 J30 J39 (ObsStore/Control/StreamRing/snapshot-diffing gone, WP10) J20 (ClaudeCLI deleted, WP14; args were already deterministic — critic-corrected) |
| Deliberately preserved (fail-forward / out of scope) | J13 (revise keeps pre-revision session ID — flagged by WP11, open) J12's `revised==""` defensive branch still writes "done" |
| Still open | J29 (embedded config cannot boot standalone — no WP claimed it), J31 minors (tick relayout, setup-overlay keys, case-folded aliases) |

## Critical / truth violations (harm-ladder rung 1–2)

### J32 — THE HANG: cancel kills only the group leader, then blocks forever joining the orphan **[FIXED WP1]**
Composite of `internal/harness/exec.go:120-121` (no `cmd.Cancel` override / no `WaitDelay` / no
negative-PID kill — `exec.CommandContext` kills only the leader) + `internal/orchestrator/agent_supervisor.go:194-196`
(on ctx.Done the supervisor blocks on `<-runDone`) + `engine_pipeline.go:84` (`Supervisor.Shutdown`
is deferred in the SAME goroutine). Scenario: worker times out → `sandbox-exec` leader killed →
claude/node grandchild survives holding the stdout pipe → `parseStream` never hits EOF →
`harness.Run` never returns → supervisor never returns → the deferred group-killing `Shutdown`
never runs. Pipeline wedged, orphan keeps billing tokens. The only working group kill lived in dead
code (`Sandbox.Run`, sandbox.go:245-269, zero callers).

### J9 — Pipeline reports SUCCESS with no commit when isolation was skipped
`internal/orchestrator/step_integrate.go:43-50`. `Worktree.Path == ""` or `TargetBranch == ""` →
integrate no-ops with `StatusSuccess`. Combined with J8: branch detection fails → worker writes the
live repo → integrate "succeeds" → run reports success, but nothing was committed and the repo has
uncommitted LLM edits. False claim of success (CLAUDE.md §0 rung 1).

### J8 — Silent worktree-isolation bypass on branch-detection failure
`internal/orchestrator/step_execute.go:32-35,43`. If `worktree.CurrentBranch` fails (detached HEAD,
odd repo state), the step logs a warning (invisible in TUI mode) and the `targetBranch != ""`
condition at :43 silently skips worktree creation — the worker then runs `s.Spec` against the LIVE
repo with no isolation, contradicting the fail-closed DEFECT-03 comment at :46-55 that only covers
`worktree.Create` failure. Violates CLAUDE.md §5.2.

### J33 — Worker self-validation verdict is computed and thrown away
`internal/agent/validation.go` fail-closes to `VerdictFail`, `ValidateStep` returns
`ValidateOutput{Parsed: …}` (step_validate.go:57), but `run_pipeline.go:160-164` reads only
`.Output` and no code anywhere consumes `.Parsed`. A worker that self-reports FAILED still proceeds
to unconditional commit + merge. Validation is decorative. (`DeriveVerdict`,
`FormatValidationFeedback`, `ValidationReport`, `Issue` are all dead too.)

### J10 — Merge conflicts never reach the user; `MergeConflictInfo` is dead code
`internal/orchestrator/run_pipeline.go:170-180` keeps only `intResult.Status`, dropping
`ConflictFiles`. `orchestrator.MergeConflictInfo` (events.go:37) — the type CLAUDE.md §5.4 mandates
for surfacing conflicts — has zero constructors/consumers. On conflict give-up the user sees bare
`StatusFailed`; conflict files exist only in run.log and integrator_meta.json.

### J12 — Failed architect revision recorded as "done"
`internal/orchestrator/step_deliberate.go:148-157`. When revision extraction fails (non-ctx error),
the code falls back to the previous plan but writes meta status "done"/no error and emits
`AgentDone` — the TUI and run history show a successful revision that never happened; only a Debug
log records the truth.

## Races / state divergence

### J3 — Dual terminal writers: `Complete` vs `Finished` race
`internal/orchestrator/run_pipeline.go:142,182` calls `sc.Obs.Complete(status)` → sets
`Terminal.Done=true` with a partial Result. `engine_pipeline.go:135` later calls
`obs.Finished(result, err)` with the full Result. `Engine.Run` (engine.go:20-22) returns as soon as
`Terminal.Done` — it can observe the partial write and return a Result with empty FinalPlan/RunDir.
Two writers for the same fact.

### J23 — TUI stops observing at `Terminal.Done`; late `Finished` never rendered
`internal/tui/model.go:285-288` — the notify loop re-arms only while `!snap.Terminal.Done`. With
J3's dual write, the TUI can consume the partial terminal state, stop listening, and never render
the real result or error.

### J2 — Stale gate decision auto-consumed by next gate
`internal/orchestrator/control.go:79-84` + `:55-65`. `Submit` is non-blocking into a buffered-1
channel that is never drained when no gate is waiting. A decision submitted when no gate is open
(double keypress, submit racing gate close) sits in the buffer and the NEXT `Gate` call returns
instantly with the stale decision — e.g. auto-approving a revised plan the user never saw.

### J17 — Stale answer auto-fires on the next question
`internal/mcp/bridge.go:271-276` + `:153-157`. `SendAnswer` is non-blocking into a cap-1 buffer
never drained when no question is pending. A double-submit or an answer racing a dropped connection
leaves a buffered `Answer` that instantly (mis)answers the NEXT question. Same class as J2.

### J25 — Answered question re-opens forever; `ClearQuestion` has zero callers
`internal/orchestrator/obs_store.go:113-121` (`ClearQuestion` — dead) +
`internal/tui/screen_pipeline_snapshot.go:128-130`. After the user answers, `obs.hasQuestion` stays
true; the next snapshot (`snap.HasQuestion && !chat.QuestionOpen()`) re-opens the identical
question. The duplicate answer is then silently dropped by `SendAnswer`'s non-blocking send.

### J26 — `Decision.AutoApprove` is written by the TUI, never read by the orchestrator
`internal/orchestrator/events.go:49-52`, `run_pipeline.go:121-133`,
`internal/tui/model_intents.go:36-45`. The gate loop breaks only on `DecisionApprove`; a confirmed
edit (`DecisionEdit` + AutoApprove) routes through Revise, which returns the edit unchanged, and the
gate re-opens demanding a second approval — meanwhile the TUI already flipped to streaming state and
appends a duplicate plan frame.

### J34 — Session ID: last-wins in the stream, first-wins in the supervisor
`internal/harness/stream_event.go:220` overwrites `sessionID` on every event (comment at :216
claims "first"); `agent_supervisor.go:334-338` keeps the first. When the architect spawns the
orqestra-researcher subagent and any event carries the subagent's session id,
`RunResult.SessionID` (drives `--resume`, plan resolution) and `capturedSID` (drives report
correlation) diverge.

### J35 — Revision rounds can silently return the PRE-revision plan
`internal/orchestrator/step.go:106-112`: extraction tier 2 reads the plan file whenever
`spec.PlanMode` — revision specs copy `ArchSpec` (step_deliberate.go:126) and `--resume` the same
session, so if the revision turn didn't rewrite the plan file, tier 2 returns the old file content
and the critic feedback is silently dropped while everything reports success.

### J13 — Revise keeps the pre-revision session ID
`internal/orchestrator/step_revise.go:83-87` returns `SessionID: in.Plan.SessionID` even after a
successful revision run (`res.SessionID` discarded), while `step_deliberate.go:161-163` advances
the session ID on revision. Second gate-loop comment resumes the pre-revision session — architect
memory diverges from the plan text shown to the user.

### J40 — Display token ledger disagrees with the budget ledger
`internal/orchestrator/obs_store.go:133-158`: `AgentStarted` resets the snapshot and `AgentDone`
writes absolute per-call usage, while `RunUsage.Record` accumulates. A multi-pass architect (init +
revisions) shows only the last pass's tokens in the UI while the budget counts all passes.

### J28 — New-run navigation leaves the old run's notify goroutine dangling
`internal/tui/model_intents.go:66-88,126`. `NavigateToPrompt`/`ConfirmNewRun` don't clear
`m.obs/m.ctrl/m.cancelCause`; the in-flight `notifyCmd` still blocks on the OLD run's `NotifyCh()`
and can deliver a wrong-store `obsNotifyMsg` after `m.obs` is reassigned to the new run.

### J1 — Data race on `controlImpl.inputs` map (theoretical — see J39)
`internal/orchestrator/control.go:67-77`. `RegisterInput` and `Input` access the plain map
`c.inputs` with no mutex from different goroutines. Softened by J39: the map is always empty today.

## Lossy / duplicated delivery paths

### J14 — Same fact, three delivery paths (stream data)
`internal/orchestrator/obs_store.go:59,65,195-200` + `stream_ring.go` + `stream_history.go`. Every
stream event is (1) appended to a `StreamRing` (with embedded `StreamHistoryStore`), (2) pushed
best-effort into `streamCh` (cap 512, silently dropped when full), and (3) signaled via `notify`.
The TUI reads BOTH the ring snapshot and drains `streamCh`; drops on one path but not the other
guarantee the two views disagree under load.

### J27 — Timeline fed only from the lossy channel; drops corrupt tool status
`internal/orchestrator/obs_store.go:196-199` (cap-512 drop) +
`internal/tui/screen_pipeline.go:193-205,221-272`. The transcript has no snapshot fallback; a burst
>512 entries permanently loses frames. Tool results resolve against `pendingTools` by position — a
dropped `EntryToolUse` whose `EntryToolResult` survives mis-marks an unrelated tool as done/failed.

### J30 — Terminal truncates the final streamed sentence
`internal/tui/model.go:285-288` + `screen_pipeline_snapshot.go:134-144` + `timeline.go:86-102`. On
terminal the notify loop stops and `timeline.Stop()` drops the live tail; entries still buffered in
`streamCh` arrive on the next tick but hit a nil tail — the final assistant text can be cut off.

### J7 — `Observer.Stream` misclassification order
`internal/orchestrator/obs_store.go:181-193`. Case order `IsDelta → Text → Tool → ToolResult`
drops/misclassifies events carrying more than one field (e.g. a tool event that also has Text
becomes EntryText; an event with none is silently dropped).

## Lifecycle / resource management

### J5 — QuestionBridge re-launched per run on a shared Engine field
`internal/orchestrator/engine_pipeline.go:61-80`. Every `startNew` spawns `QuestionBridge.Run(ctx)`
+ a forwarding goroutine on the same Engine-owned bridge. A second run starts a second `Run` on the
same bridge/socket; questions from run N can land in run N+1's ObsStore.

### J41 — Bridge goroutines and listener FD leak per completed run
Extends J5: the per-run ctx is never cancelled after a NATURAL completion (`model_intents.go`
replaces `cancelCause` on the next run without calling the old one), so each finished run leaves
its accept-loop + question-forwarder goroutines alive; the next run's `os.Remove(socket)` +
re-listen orphans the previous listener.

### J16 — Question bridge serializes connections: report blocked behind pending question
`internal/mcp/bridge.go:100-111` — `handleConnection` runs inline in the accept loop, and
`handleQuestion` (:140-168) blocks until the human answers. Any other bridge message (a
`SubmitReport`, another agent's question) queues in the socket backlog until then; the supervisor's
report-arrival stop and drift policy stall behind human latency.

### J36 — Bridge has no readiness handshake and a 1 MB frame cap
`cmd/orqestra/main.go:152-166` bakes the socket path into agent args, but the listener binds later
in a goroutine (`engine_pipeline.go:62`) — an early `AskUserQuestion`/`SubmitReport` gets
ECONNREFUSED and is lost. `internal/mcp/frame.go:27` + `server.go:122` cap frames at 1 MB — a large
report fails delivery and falls to the scraping tiers.

### J4 — Global logger swapped per run, discarded after
`internal/orchestrator/engine_pipeline.go:45-47`. The run goroutine calls `slog.SetDefault(logger)`
(process-global) and the deferred reset sets the default to `io.Discard` — after the first run
completes, ALL process logging is silently discarded. Races if two runs overlap. (main.go swaps the
global twice more: :32, :111.)

### J42 — Minor integrity items **[stdin-writer join FIXED WP1]**
`internal/agent/plan_extract.go:137-143` — "secure" plan-path guard is lexical (`filepath.Abs` +
`HasPrefix`), not symlink-resolved. `internal/harness/exec.go:191-218` — stdin-writer goroutine was
signaled but never joined (contract at :109 says joined). `internal/orchestrator/step_meta.go:26` —
`KnownAgents` is dead AND stale (lists "researcher", omits validator/integrator).

## Fail-closed violations / silent defaults

### J19 — Conflict-resolution spec build failure degrades to a zero ProcessSpec
`cmd/orqestra/main.go:360-364`. `integratorConflictSpecFn` returns `harness.ProcessSpec{}` on error
— at merge-conflict time the pipeline would exec a bare unconfigured `claude` (no sandbox, no model
routing, empty AgentID) to edit conflicted files. Violates §1.4/§1.6.

### J24 — Silent-default trap in `resolveSetup`
`internal/orchestrator/setup.go:38-51`. A caller that legitimately wants "everything off"
(plan-only, no gates) and leaves `DeliberationRounds` at 0 passes `isZeroSetup` and silently
receives `DefaultPipelineSetup()` — including `Execution: true`. A worker could run and modify a
repo the user asked only to plan for.

### J6 — Nudge policy config silently changes subprocess invocation mode
`internal/orchestrator/agent_supervisor.go:101-118`. `needsInputPlane` flips to true when any
policy (silence guard etc.) exists; then `spec.Prompt` is moved into the stdin channel and cleared,
switching claude from `-p` one-shot mode to interactive stream mode. Configuring a nudge changes
how the prompt is delivered — action at a distance.

### J29 — Embedded default config cannot run standalone
`cmd/orqestra/main.go:409-446` + `internal/config/config.go:346,356-359`. The embedded
`pipeline.yaml` ships no `providers`/`models`, and `validate` demands them (including a hardcoded
`"small"` model ref) — with no on-disk `orqestra.yaml` the binary exits `exitInvalidInput`.
"Defaults" that cannot boot.

## Half-removed features / schema drift

### J11 — Restart feature dead end-to-end: writer/reader schema drift
Three-part failure. (a) `internal/orchestrator/artifacts.go` (192 lines: `writeArtifactIn`,
`writeArtifactJSONIn`, `appendDialog`, `highestPlanVersion`, `findHighestPlan`) has NO callers — it
is the orphaned writer of the old per-phase session schema. (b) `run_history.go:139-183`
`AnalyzeRunCompleteness` still READS that old schema (`run_config.json`, `deliberation/plan-v*.md`,
`execution/output.txt`, `validation/validation.txt`) which no current code writes → always returns
"no run_config.json (old format run)"; every historical run shows "⚠ INCOMPLETE". (c) The TUI sets
`Input.RestartFrom` (model_intents.go:142) but `startNew` never reads it — a restart silently runs
a fresh full pipeline with a synthesized prompt string.

### J18 — Documented headless mode does not exist; `Engine.Run` is dead API
Root `CLAUDE.md` §4 documents `./orqestra --prompt "…" --auto-approve --config …`;
`cmd/orqestra/main.go:49-56` defines only `--config` (plus `init` / `mcp-bridge`). The synchronous
`Engine.Run` (engine.go:17) — the natural headless entry — has no callers, and would deadlock on
any gate (polls `Snapshot()` while never servicing `Control`).

### J15 — `TrackProc` is dead code (superseded by J32/WP1)
`internal/orchestrator/supervisor.go:26` has zero callers; the Supervisor's process-group tracking
never ran.

## Fragility / duplication

### J21 — Duplicated MCP-merge logic in two arg builders
`internal/harness/exec.go:421-469` (`mergeInlineMCP`, LIVE) and `claude_cli.go:432-490`
(`buildFinalArgs`, dead — reachable only via test helper `BuildTestArgs`) implement the same
--mcp-config merge twice; tests validate the copy that production doesn't run.

### J22 — Fake decoupling: harness Run fabricates a `ClaudeCLI` to borrow `buildEnv`
`internal/harness/exec.go:514-517`. `ModelSpec` exists "to decouple config from harness"
(runner.go:8-9), yet `buildEnvFromSpec` converts ModelSpec back into `config.ResolvedModel` and
instantiates a throwaway `&ClaudeCLI{}` to call its private method. Two spec systems for one
subprocess.

### J20 — Nondeterministic intermediate slice order from map iteration (cosmetic — critic-downgraded)
`internal/harness/claude_cli.go:103-111` (`toSpec` iterates `inlineMCPServers`/`inlineAgents` maps
into slices). The final CLI args are NOT affected: both slices collapse back into single
`--mcp-config`/`--agents` JSON blobs whose map keys marshal sorted (exec.go:399,456). Only
`ProcessSpec` value equality between two builds is broken.

### J37 — Inconsistent scanner ceilings: 1 MB vs 2 MB on the same JSONL
`internal/harness/logpath.go:132` (`ExtractPlanFilePath`, 1 MB) vs `logpath.go:77` and
`stream_event.go:246` (2 MB). One JSONL line in the 1–2 MB range makes plan-path extraction fail
with "no plan_mode attachment" while other readers handle the same file fine.

### J38 — `CwdToDash` may diverge from Claude's project-dir encoding (UNVERIFIED)
`internal/harness/logpath.go:15` replaces only `/`; if Claude also folds `.` (e.g.
`/Users/x/my.app` → `-Users-x-my-app`), every JSONL-based path fails for such repos. Verify
empirically before fixing (critic: a wrong "fix" corrupts correct paths).

### J39 — The live "post message to agent" plane is inert
`internal/orchestrator/control.go:75-77`: `RegisterInput` has zero callers, so `inputs` is always
empty, `Input(id)` always returns nil, and the TUI steering feature (`model_intents.go:101-111`)
silently sends into a nil channel.

### J31 — Minor TUI/config items
`internal/tui/model.go:246` (unconditional `recalculateLayout()` every tick);
`model_keys.go:152-163` (setup overlay eats ^R/^N/^Q with no hint);
`internal/config/config.go:100-105` (case-folded alias lookup masks typos).

### J43 — Log viewer reads a StepMeta field no writer sets (critic-found)
`internal/tui/screen_run_detail_log.go:29-31,119-121` reads `StepMeta.ClaudeSessionLogPath`, which
no `writeMeta` in the orchestrator ever populates — the viewer's primary path can never work.
Resolve by writing the field (WP15), not deleting it.

## Dead-code inventory (two tiers; critic-verified)

**Tier A — isolated, zero live callers (safe to delete first):**
- `internal/orchestrator/artifacts.go` — entire file (old phase-dir artifact writer).
- `internal/orchestrator/usage.go` — `StartAgent`/`EndAgent`/`Snapshot`/`RunSnapshot`/
  `WriteRunSnapshot`/`LoadRunSnapshot` (only `Record`/`Limit`/`TotalUsed` live).
- `internal/orchestrator`: `Engine.Run`, `MergeConflictInfo`, `DecisionSkip`, `DecisionMergeAbort`,
  `GateRequest.PlanFilePath`, `PhaseResearching/Deliberating/Done`, `KnownAgents`,
  `StepMeta.ClaudeProjectPath/PlanSource`, AgentSnapshot status "cancelled", `TrackProc`.
  (`ClearQuestion` and `Decision.AutoApprove` are NOT dead-to-delete — they get WIRED by WP5/WP4a.)
- `internal/harness`: `ClaudeCLI.buildFinalArgs` + `BuildTestArgs` (retarget tests to
  `buildSpecArgs`).
- `internal/agent`: `ReadPlan` + `scanPlansDirectory` (production uses only strict `ReadPlanFile`),
  `CommitMessagePrompt`, `DeriveVerdict`, `FormatValidationFeedback`, `ValidationReport`, `Issue`.
- `internal/sandbox`: `Sandbox.Run` (its group-kill logic was ported to harness.Run in WP1),
  `Sandbox.Workspace`, `Config.SessionPath/ProxyEnv/ExtraEnv` (+ unreachable
  `ProfileBuilder.SessionPath` branch and `config.SandboxConfig.ProxyEnv/ExtraEnv`).
- `internal/worktree`: `MergeInto`, `CommitAll`, `RemoveDir` (superseded merge strategy).
- `internal/tui`: `PipelineScreen.phase` (written, never rendered).
- Researcher-as-stage remnants: `RetryConfig.ResearcherAttempts`, `preTimeoutNudgeFor("researcher")` arm.
- `cmd/orqestra/main.go:1` stale comment; root CLAUDE.md documents nonexistent headless flags (J18)
  and cites dead `MergeConflictInfo` as the conflict surface (J10).

**Tier B — dead-in-effect but with live TUI references; deletable only with their TUI call sites:**
- `internal/orchestrator/stream_history.go` — `StreamHistoryStore` is a live compile-time
  dependency of `StreamRing` (stream_ring.go:68,83,106,146,169); the TUI instantiates its OWN ring
  (model_intents.go:129,150 → `PipelineScreen.streamBuf`) and renders via `AgentActivities`
  (screen_pipeline_view.go:135).
- `Control.Input`/`RegisterInput` — inert but CALLED at model_intents.go:106.
- `RestartInput`/`RestartPhase`/`Input.RestartFrom`/`AnalyzeRunCompleteness` — engine-side no-ops
  but typed into 6+ TUI files (screen_run_detail.go:58, screen_run_detail_keys.go:25,
  model_keys.go:178-184, model_intents.go:93/136/144, messages.go:134, model.go:99).
- `EditPlanIntent` — no producer, but a consumer case exists (model_intents.go:46).
- `StepMeta.ClaudeSessionLogPath` — never written, read live (J43); fix by writing it, not deleting.
