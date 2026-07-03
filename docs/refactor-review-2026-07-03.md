# Final review — simplification refactor (WP1–WP16, commits 620150b..8dc9201)

> **RESOLUTION (2026-07-03, commits d7b07dc..49aaa24):** every finding below was addressed by
> WP17 (cross-run lifecycle hardening: F1–F4, A2/A3/A7, fanoutSink session-event guarantee,
> bridge Release, sendIntent bound, quit-cancels-run) and WP18 (F5 zero-delta prose fallback,
> A1/A4/A5/A6/A8/A9 error-fate + size sweep, J12-residual, J13, J29, J31b/c, executor tool
> filters). All RED-first proven; final tree GREEN (`QA-ATTEST commit=1d1518a dur=16s
> SUITE-COMPLETE`, integration lane green). Documented residuals: executor filtering keys off
> `AllowedTools` only (blanket disallowed-tools default makes the other field non-discriminating
> — commented in spec_for_role.go); bridge retains `reports`/`agentNonce` entries by design
> (deleting them races the harvester's TakeReport — see bridge_report.go); §4's doc-drift list
> remains for the owner's in-flight CLAUDE.md restyle (incl. the G3 warning).

Three independent review passes (correctness / plan-gap audit / CLAUDE.md compliance) over the
cumulative diff (+10,662/−4,854, 140 files), findings verified first-hand before inclusion.
Verdict: **the refactor is sound and fully landed** — every wave closed green
(`QA-ATTEST commit=8dc9201 dur=15s SUITE-COMPLETE`, integration lane green incl. headless e2e),
all 16 packages verified in code against the plan, 31 journal defects fixed + 8 made moot.
The findings below are what three fresh sets of eyes caught AFTER that bar: five bugs (all in the
one seam no work package owned — cross-run lifecycle), nine engineering-standard violations
(mostly error-fate hygiene in moved code), and 28 stale statements in the CLAUDE.md family.

## 1. Bugs found (verified; none currently reachable in a green single-run flow)

| # | Severity | Defect | Where |
|---|---|---|---|
| F1 | rung 1 | Cross-run event bleed: `runEventMsg` carries no run identity; after ^N-cancel + quick restart, run 1's late `EventRunFinished` paints a false terminal state over live run 2, and the stale `waitForEvent` chain re-arms on run 2's channel → two concurrent consumers, event reordering | tui/model.go:293-313, model_intents.go:91-123 |
| F2 | rung 1/3 | Zombie `handleQuestion` goroutines (question pending when agent dies) block on the shared cap-1 `pendingAnswer` forever and later STEAL a valid answer (ID mismatch → dropped, not re-queued); compounds per abandoned question | mcp/bridge.go:170,221-233 |
| F3 | rung 3 | A stale question buffered across runs surfaces as a phantom on the next run's bus; `onQuestionAsked` drops any question while one is open, with no re-queue — a live agent's real question can be lost until its timeout | bridge.go:84, engine_pipeline.go:68-85, screen_pipeline_events.go:201-205 |
| F4 | rung 3 | Intents consumer's `select{case gateDecisions<-it: default:}` drops the NEWER gate decision when a stale one occupies the cap-1 buffer (comment claims supersede; code does drop-newest); TUI closes the gate optimistically → silent wedge until ^C | engine_pipeline.go:96-101 |
| F5 | rung 3 | Regression: non-delta assistant text is unconditionally dropped by `eventObserver.Stream`; a zero-delta stream (e.g. `testdata/worker_stream_sample.jsonl`) renders NO prose in the TUI and nothing under `--verbose-stream`; pre-refactor `EntryText` had an explicit fallback | observer_emitter.go:47-62 vs 620150b^ screen_pipeline.go |

Hardening notes below threshold: lossy `fanoutSink` (cap 64) could in principle drop the
`EventSessionDone` that triggers graceful stdin-close; bridge `reports`/`reportWaiters` entries
never deleted (memory only); post-run `sendIntent` can block a Cmd goroutine (unreachable via UI);
pre-existing ^C-from-runs-list quits without cancelling the run.

Clean areas (explicitly checked, no defect): emitter queue/coalescing/close-once, gate ID logic in
isolation, supervisor nonce copy semantics + stdin-close guards + capturedSID, harness exec
teardown ordering + group kill, SpecForRole/spec_args/env, headless signal + event loop,
run_pipeline/report_harvest/step files.

## 2. Plan-gap audit

**Real gaps (all documentation):**
- G1: `internal/orchestrator/CLAUDE.md` (created mid-range) mandates deleted `MergeConflictInfo`.
- G2: committed root CLAUDE.md §2.1 still cites deleted `Engine.Run`, `CommitAll`, `MergeInto`.
- G3 (**action needed by owner**): the UNCOMMITTED working-tree CLAUDE.md rewrite contains zero
  mentions of `ConflictFiles` — committing it as-is silently reverts WP3's mandated §5.4 repoint.
  The untracked `internal/agent/CLAUDE.md` also cites deleted `ValidationReport`/`Issue`.
- G4: REFUTED — the auditor claimed WP14's golden fixtures don't exist; verified first-hand they do
  (`cmd/orqestra/testdata/wp14_golden.{json,yaml}` in 8dc9201, corrupt-a-field RED demonstrated).

**Deviations adjudicated as acceptable** (all worker-documented): startup-only `slog.SetDefault`
in main.go (J4's defect was the per-run swap — gone; note the worker's cited rationale was wrong
even though the deviation is fine); executor class ignores configured tool filters (golden-faithful;
follow-up: validate-or-document); `RoleConfig.Attempts` informational; `make test-e2e` honestly
left as the live-API placeholder; J13 and J12's `revised==""` branch deliberately preserved.

**Journal:** 31 fixed, 8 moot (event-bus rewrite), 2 preserved by design, open: J29 (embedded
config still cannot boot standalone) and J31 b/c (setup-overlay key eating; case-folded aliases).
Full per-entry evidence table in the audit transcript; resolution table in the journal header.

## 3. Engineering-standard violations (CLAUDE.md §-refs; all verified)

- A1 §8: `engine_pipeline_wp4b_test.go:101` polls with `time.Sleep` and a stale "no readiness
  signal yet" comment — `bridge.Ready()` (WP12) is the channel it should use.
- A2 §1.3: `emitter.forward()` has no ctx exit and is never joined — blocks forever on an
  abandoned Events channel (the exact state F1/A3 produce).
- A3 §1.3/§0: no run identity on the TUI event chain (same root as F1).
- A4 §1.1: bare `if jsonErr != nil { return }` meta-write swallows in step_deliberate.go:238,
  step_execute.go:129, step_validate.go:73,94 (step_integrate.go:229 shows the compliant pattern).
- A5 §1.1: harvest tiers that ERROR (vs fail sanity) leave no trace in provenance or logs
  (report_harvest.go:212,229); `ParseCommitMessage` error discarded (step_integrate.go:128).
- A6 §1.1/§1.6: WP14-re-homed spec code keeps legacy swallows — `filterMCPConfig` warn-drops an
  explicitly configured MCP server; `mergeInlineMCP` marshal failure silently drops the entire
  inline-MCP set including the bridge (spec_for_role.go:320-333, spec_args.go:106-134).
- A7 §1.3: bridge per-connection goroutines unjoined; `readFrame` has no deadline — a hung peer
  leaks past Run's return (bridge.go:170, frame.go:29-42).
- A8 §1.7: config_test.go 544, bridge_test.go 511 (new >500); config.go 524 (+2 despite "do not
  pile on").
- A9 §0.1: run-log open failure continues without a `// fire-and-forget:` marker
  (engine_pipeline.go:147-153).

Pre-existing (unchanged lines, out of scope): exec.go's unmarked `_ = sb.Close()`s,
`formatYAMLError` %w severing + potential panic, timeline.go dropped Cmds, sandbox cleanup drops.

## 4. Documentation drift — 28 verified stale claims

Full table in the compliance transcript; the clusters, for the in-flight CLAUDE.md restyle:
1. **Deleted symbols still cited:** `ClaudeCLI` (§1.1, §2, §2.1), `Engine.Run` (§1.3, §2.1, and
   orchestrator/CLAUDE.md), `MergeConflictInfo` (§5.4, §11, orchestrator/CLAUDE.md),
   `agent.ReadPlan` + plans-dir scan tier (§2, §5.1, §9#12, §11, agent/CLAUDE.md),
   `ValidationReport`/`Issue`/`DeriveVerdict` (§1.8, §2.1, agent/CLAUDE.md),
   worktree `CommitAll`/`MergeInto`/`abortMerge` (§2.1), `Sandbox.Run` kill snippet + "sandbox owns
   process-group cleanup" (§1.1, §5.2, §2.1, sandbox/CLAUDE.md — kill now lives in harness.Run).
2. **New machinery undocumented:** `harness.SpecForRole` + `config.Roles()`, `RunEvent`/emitter/
   `Intent`/`RunHandle{Events,Intents}`, `ReportHarvester` + provenance, `rundir`,
   `ProcessSpec.InputPlane`, nonce-keyed bridge + `Ready()`, `Result.ConflictFiles`/
   `ValidationVerdict`, headless `--plan-only`/`--verbose-stream`.
3. **Line-number drift** throughout (§1.2/§1.3/§1.4/§2 citations) — supports the owner's new
   no-hard-cross-references house style.
4. rune-boundary claim stale in root + tui CLAUDE.md: `internal/tui/frame/plan.go` also imports rune.

## 5. Recommended follow-up packages (not executed)

- **WP17 — cross-run lifecycle hardening (F1–F4, A2, A3, A7):** run-scoped identity on
  events/intents (RunID on RunHandle + runEventMsg, TUI drops mismatched), teardown of
  bridge question goroutines on invocation end (per-conn deadline or invocation-scoped ctx),
  gate-decision supersede implemented as drain-then-send, emitter forwarder joined via runCtx.
  One coherent package; RED-first provable for each (F1/F4 already have concrete repro scenarios).
- **WP18 — output fidelity + error-fate sweep (F5, A1, A4–A6, A8, A9):** zero-delta prose
  fallback event (non-delta text forwarded when no deltas preceded it in the turn), meta-write and
  harvest-tier error fates (`// fire-and-forget:` or log-with-tier), filterMCPConfig fail-closed on
  named-but-missing servers, test-sync via Ready(), file-size splits.
- **Docs:** fold §4's cluster list into the in-flight CLAUDE.md restyle (G3 checked against WP3's
  repoint before committing).
