# Orqestra QA Specification

> The cornerstone document. A test is valid **only** if it cites an invariant ID (`INV-*`) defined here.
> No invariant → no test. No test without a cited invariant. This file is the oracle; the suite is its executable shadow.

## 0. How to read this document

**Evidence tier** — every claim is tagged so its grounding is auditable. Nothing is asserted as fact above its tier:

| Tag | Meaning |
|----|---------|
| `[V file:line]` | Verified by reading that code this pass. |
| `[T test]` | Verified by an existing real test that exercises the behavior. |
| `[G]` | Signature/anchor confirmed by grep, body not fully read. |
| `[2]` | Second-hand (sub-agent report); **must be promoted to `[V]` before it backs a gate.** |
| `[OPEN]` | Unverified / unknown. Not admissible as fact. |

**Falsifiability rule** — every invariant carries a **Falsified by** line: the concrete change or input that turns the gate red. *An invariant with no falsifier is not an invariant — it is a virtue, and it does not belong here.* Two of the original "pillars" failed this test and were demoted (§2).

**Status** — `covered` (real gate exists) · `gap` (no real gate) · `red` (gate would fail on current code) · `defect` (a confirmed bug; see §4).

## 1. What Orqestra is (grounded)

Orqestra runs a coding task through a chain of LLM agents and is **designed to complete without a human**. `[V pipeline.yaml]` defines the roles; the Go engine drives them headless.

- Pipeline (Go engine, `internal/orchestrator/engine.go`): Researcher → Architect → Critic → *(optional)* plan gate → Worker → self-validation → worktree merge. `[V engine.go:1222-1513]`
- The worker receives the approved plan **verbatim** behind a fixed preamble. `[V spec.go:245-247]`
- `internal/config/pipeline.yaml` is the harness-agnostic core: abstract models `medium`/`large`, `permission_mode` per role (researcher/architect/critic = `plan`, worker = `full`), retry budgets. `[V pipeline.yaml:10-361]`
- Worker writes are meant to be isolated in a per-run git worktree and merged back; the seatbelt sandbox is the containment boundary. `[V engine.go:1236-1498]` `[G sandbox.go:38-223]`

Human approval gates are a **configurable overlay** (interactive adapter / non-`--auto-approve`), not a guarantee — `--auto-approve` runs the whole chain unattended. `[V engine.go:891,1480]` Therefore the safety net is not a human; it is the **truthfulness** and **hostile-input** properties below.

## 2. Pillars → falsifiable invariant families

The original list contained two virtues masquerading as pillars. Honest version:

| # | Pillar / family | Real? | Note |
|---|---|---|---|
| **P1** | **Provenance** — the approved plan is what executes | ✅ falsifiable | worker prompt = fixed preamble + plan verbatim `[V spec.go:245]` |
| **P2** | **Containment** — worker cannot escape sandbox/worktree | ✅ falsifiable | construction `[G sandbox.go]`, denial `[T sandbox_test.go]` |
| **P3** | **Truthfulness** — never report a step succeeded when it didn't | ⚠️ family, not one pillar | falsifiable **per member**; this is where the confirmed defects live (§4) |
| **P4** | **Hostile-input handling** — LLM/file/stream output is untrusted | ⚠️ narrow slice of "grounding" | the parser/path/boundary checks are gateable; the *anti-hallucination goal is **NOT gateable*** — see below |
| **P5** | **Provider-agnostic core** — any backend, fail-closed config | ✅ falsifiable | `[V config.go:323-385]`, openai env path `[V claude_cli.go:880-893]` |
| **P6** | **Autonomy via delegated validation** | ⚠️ corrected | **there is no Go retry loop** — see below |

**Demoted / corrected — stated plainly so no gate is built on sand:**

- **"Grounding defeats LLM unreliability" is NOT a deterministic gate.** It is probabilistic and model-dependent — exactly the *judgment* that Orqestra itself ranks below *discrete* checks. We never write a gate asserting "no hallucination reached execution." We gate only the falsifiable slice: output is parsed by typed parsers, paths are validated under allowed roots, raw text is preserved (P4). The aspiration is observed (not gated) at the live tiers (§5, L3/L4).
- **P6 has no orchestrator-level retry loop.** `retryBudget` is injected as *prompt text* ("Stop after %d fix attempts") `[V spec.go:250-268]`; validation is a **single** `runRunnerContinue` call `[V engine.go:1310-1313]`. The fix-retry happens *inside the model's one turn* — not gateable. The falsifiable Go-level invariants are narrow: validation runs in the worker's session, and the verdict→status mapping is correct (INV-O2, and DEFECT-02 where it is not).

## 3. Invariant catalog (the heart)

Keystone invariants only — one or two per pillar, not exhaustive. Format: statement · evidence · positive/negative · observable · falsifier · seam/layer · status.

### P1 — Provenance

**INV-P1-EXEC** The worker's execution prompt equals a fixed preamble concatenated with the approved plan markdown, byte-for-byte; nothing else is injected or transformed.
- Evidence: `[V spec.go:245-247]` (`"Execute the following plan…\n\n" + planMarkdown`), `[V engine.go:1253]`
- Positive: approved plan P → execPrompt == preamble+P.
- Negative: plan with code fences / unicode / 50KB → still verbatim, no truncation.
- Observable: string equality on the prompt handed to the worker runner.
- **Falsified by:** any summarize/wrap/re-render step between gate approval and worker `Post`.
- Seam/Layer: L2 (replay) or L1 (replay-Runner captures the posted prompt). Status: **gap**.

**INV-P1-PLANSRC** The plan comes from the real Claude plan file under `~/.claude/plans/`; a path outside that root, a missing session ID, or an empty file is an error, never a silent fallback.
- Evidence: `[V claude_cli.go:1041-1063]` (prefix gate + empty-file error), `[T plan_extract_test.go]` (security gate, empty, missing).
- Negative: planFilePath `/etc/passwd` → rejected; empty plan → error.
- **Falsified by:** removing the `HasPrefix(absPath, allowedPrefix)` check (claude_cli.go:1051).
- Seam/Layer: L0 + recorded-JSONL fixtures. Status: **covered** (extend hostile inputs).

### P2 — Containment

**INV-P2-WRITE** A worker process cannot write outside its worktree/session roots, nor read a secret outside the allowlist; a read-only-repo runner cannot write the repo.
- Evidence: construction `[G sandbox.go:38-157]` (EvalSymlinks, sandbox-exec required, SBPL chmod 0400, RepoWritable), denial `[T sandbox_test.go: TestDeny_WriteOutsideWorkspace / TestSeatbelt_ReadonlyRepoWriteDenied]` `[2]`.
- Observable: post-run, the forbidden file does not exist / secret not read.
- **Falsified by:** dropping `-f $SB` from the trampoline (sandbox.go:223) or flipping `RepoWritable`.
- Seam/Layer: L1, macOS `darwin` tier. Status: **covered** (real today; formalize + promote `[2]→[V]`).

### P3 — Truthfulness (family)

**INV-P3-VALID** A run is reported `StatusSuccess` only if validation actually ran **and** produced evidence of passing checks. Validation that errored, was skipped, or produced no recognized checks must **not** yield success.
- Evidence: current behavior **violates this** — `[V engine.go:1439-1442]` (`StatusSuccess` unless `VerdictFail`), `[V validation.go:80,48]` (`""`/unrecognized → `VerdictPass`), `[V engine.go:1330]` ("Non-fatal: proceed").
- Observable: feed empty or marker-less validation output → status must not be success.
- **Falsified by:** the code as it stands → see **DEFECT-02**.
- Seam/Layer: L0 (parser) + L1 (engine status mapping via replay-Runner). Status: **red / defect**.

**INV-P3-DEGRADE** When isolation, commit, or merge is degraded or fails, the run surfaces it via an **event** and does not report success.
- Evidence: merge failure/conflict → events + `StatusFailed` `[V engine.go:1456-1501]` (good); but worktree-create failure → only `slog.Warn`, **no event** `[V engine.go:1246]`; commit failure → merge skipped, status stays `StatusSuccess` `[V engine.go:1448-1455,1439]`.
- Observable: inject create/commit failure → an `Event` is emitted and status ≠ success.
- **Falsified by:** the code as it stands → see **DEFECT-03, DEFECT-04**.
- Seam/Layer: L1 (real temp git + replay-Runner). Status: **partial / defect**.

### P4 — Hostile-input handling

**INV-P4-PARSE** Validation/commit/stream parsers accept arbitrary bytes without panic, always preserve raw text, and never let parser success imply work success.
- Evidence: raw preserved `[V validation.go:94]`; parser is best-effort `[V validation.go:40-48]`; commit-message empty → error `[V spec.go:288-292]`.
- Negative matrix: truncated UTF-8, 10MB line, injected `✕` inside prose, NUL bytes, marker-less essay.
- **Falsified by:** any parser that errors-to-panic or drops `Raw`.
- Seam/Layer: L0 (table-driven + fuzz). Status: **gap** (only happy markers tested today).

**INV-P4-STREAM** The stream scanner has an explicit buffer and handles `scanner.Err()`; oversized/non-JSON lines degrade gracefully.
- Evidence: `[V claude_cli.go:667-710]` (buffer set, non-JSON → Text event, returns `scanner.Err()`).
- **Falsified by:** removing `scanner.Buffer(...)` → long lines silently dropped.
- Seam/Layer: L0 with recorded stream-json fixtures. Status: **gap**.

### P5 — Provider-agnostic core

**INV-P5-FAILCLOSED** Config load fails on: empty role model, unresolvable model ref, unknown provider, missing/unknown provider type, native-with-base_url, non-native-without-base_url, malformed or conflicting token limits.
- Evidence: `[V config.go:323-385]` (each branch returns an error).
- Negative matrix: one case per branch; assert the error names the offending key.
- **Falsified by:** any branch downgraded to a warning or a default.
- Seam/Layer: L0 table-driven. Status: **partial** (extend to full matrix; pin wrong-case behavior — `lookupModel` is case-insensitive `[G config.go:96]`, and CLAUDE.md forbids *new* fallback, so a test must pin current behavior, not bless it).

**INV-P5-ROUTE** A non-native (`anthropic`/`openai`) provider routes the CLI to its `base_url` via env; `native` adds no override; an unknown type errors with no fallback.
- Evidence: `[V claude_cli.go:862-898]` (`BuildModelEnv` branches).
- **Falsified by:** a `default:` that returns nil env instead of an error (claude_cli.go:894).
- Seam/Layer: L0 (env) + **L4** end-to-end against llama-server (§5). Status: **gap**.

### P6 — Autonomy (narrow, corrected)

**INV-O1-FLOW** `--auto-approve` drives all phases in order to `EventComplete` without blocking on a decision channel; cancellation propagates and terminates the run.
- Evidence: `[V engine.go:891]` (gate only when `!AutoApprove`), `[V engine.go:1487,1504-1512]`.
- **Falsified by:** an unconditional `<-decisions` read on the auto path → INV-H1 hang is one cause.
- Seam/Layer: L2 (replay binary). Status: **gap** (and red until DEFECT-01 fixed).

**INV-O2-VERDICT** The run status is a pure, total function of the parsed verdict and merge outcome — and that function refuses to map "no evidence" to success (see INV-P3-VALID).
- Evidence: `[V engine.go:1438-1442,1499-1501]`.
- Seam/Layer: L1. Status: **red** (the mapping exists but admits the DEFECT-02 hole).

### Spine (cross-cutting — P1/P3)

**INV-H1-CLOSE** After the underlying Claude process exits, the channel from `Runner.Receive()` is closed, so a `for ev := range Receive()` consumer terminates; events reach exactly one consumer (no `SetEvents`/`Receive` split-brain); `SessionID()` is populated from the stream.
- Evidence: direct path closes `[V claude_cli.go:445]`; **sandbox path never closes** `[V runner.go:348-351]`; consumer depends on close `[V planner.go:57,110]`; fan-out vs Receive race `[V claude_cli.go:341-348,285-288]`.
- Observable: replay process emits N events then exits → consumer receives N then the loop returns within a timeout.
- **Falsified by:** the code as it stands → **DEFECT-01**.
- Seam/Layer: L1 via replay binary through the **real** `sandboxedRunner`. Status: **red / defect**. Pins DEFECT-01.

## 4. Confirmed defects (the falsification targets)

These are personally verified and exist to prove the gates are real: each gate above must go **red** against current `flexible-pipeline` before any fix. Fixes are out of scope for this spec.

| ID | Pillar | Defect | Evidence |
|----|--------|--------|----------|
| **DEFECT-01** | P1/P3 | Sandboxed runner never closes its event channel (and `SetEvents`/`Receive` split-brain); since every runner is now sandboxed, the main flow **hangs** after Claude exits. | `[V runner.go:348-351]` `[V claude_cli.go:341-348,445]` `[V planner.go:57,110]` |
| **DEFECT-02** | P3 | Validation that errored or produced no `✕` marker parses to `VerdictPass` → run reported `StatusSuccess` with no evidence of a passing build. | `[V validation.go:80,48]` `[V engine.go:1330,1393,1439-1442]` |
| **DEFECT-03** | P2/P3 | `worktree.Create` failure (or missing factory/branch) silently falls back to a writable-repo worker, emitting only a `slog.Warn` (suppressed in TUI) — isolation degraded invisibly. | `[V engine.go:1242-1251]` (+ doc `engine.go:100-101`) |
| **DEFECT-04** | P3 | Worktree commit failure skips the merge but leaves `status = StatusSuccess`. | `[V engine.go:1448-1455,1439]` |
| **DEFECT-05** | P1 | `SessionID` is never assigned and `EventSessionStart` is never emitted, so `SessionID()` is always `""` and `planner.go`'s session-id branch never fires → plan extraction and validation continuation break. (INV-H2-SESSIONID) | `[V claude_cli.go:271/296/360]` `[V planner.go:67]` (no emit sites) |
| **DEFECT-06** | P5 | The sandboxed runner hardcoded `"claude"`, silently ignoring the documented `binary` config knob — the only runner path. **Fixed** in this increment (honors `r.cli.binary`, empty→`"claude"`) to enable the replay seam. | `[V runner.go sandboxedRunner.init]` |

**Why the suite stays green:** `harness/claude_cli_test.go` (22 funcs) tests only `buildEnv`/args; TUI tests wire `noopRunner` whose `Receive()` returns `nil` `[V app_test.go:24-26]` — so nothing drives `Post→Receive→close`, and no test maps a real validation/merge outcome to a run status.

## 5. Layered seam architecture

The seam decides whether a gate is real. **Only real production code is driven; the only permitted double is a verbatim recording of real output replayed — never hand-written behavior.** Injection uses the existing `binary` config knob `[V config.go:52,67,488]` `[G runner.go:63]` — *not* a test-only interface.

| Tier | Seam | Determinism | CI lane | Owns |
|---|---|---|---|---|
| **L0 Unit** | pure functions + recorded fixtures | deterministic | `make test` | INV-P1-PLANSRC, P4-*, P5-FAILCLOSED/ROUTE(env), parser/verdict/config matrices |
| **L1 Package** | real pkg API; replay-Runner at the real `harness.Runner` boundary; real temp git; real `sandbox-exec` | deterministic | `make test` + `darwin` tier | INV-H1, P2-WRITE, P3-VALID/DEGRADE, O2 |
| **L2 App (replay)** | `./orqestra --auto-approve` with model `binary` → replay executable emitting recorded stream-json | deterministic | `make test` | INV-P1-EXEC, O1-FLOW, full wiring without API — *catches DEFECT-01* |
| **L3 Live e2e** | real `claude` + real API + real git + real sandbox | non-deterministic → assert **side-effects**, never model text | local/manual `make test-e2e` | whole stack; **captures/refreshes fixtures + detects claude format-drift** |
| **L4 Provider gate** | real `claude` routed (`openai` type) to local **llama-server** | semi | pre-release, tagged | **INV-P5-ROUTE end-to-end** |

**What L4 (llama-server) tests, and what it does not:** it proves the **provider-independent mechanical spine** completes on a non-Anthropic backend — config resolves an `openai` provider, `BuildModelEnv` points `ANTHROPIC_BASE_URL` at llama-server `[V claude_cli.go:880]`, the stream parses, `SessionID` is captured, the channel closes (INV-H1), the plan extracts, the sandbox holds, the merge completes, the verdict surfaces. It deliberately does **not** judge plan or code quality (that is judgment, model-dependent, non-gateable). It is a *final* gate (needs a running server, slow) and cheap enough (local) to run pre-release without API spend.

## 6. Test-fiction ban list (the immune system)

Review blockers on any PR touching `*_test.go` — enforced by the `orqestra-critic` agent:

- **Tautology** — asserting a field equals the value the test just set.
- **Test-only hook/method** — install `m.cancel = func(){…}` then assert it was called; setters that bypass the real path.
- **Hand-written fake for a real dependency** — `FakeClaude`/fake git/fake sandbox; an interface that exists *only* for test injection. (Permitted: a **replay** double fed recorded real output.)
- **No-crux** — interesting setup, but the one invariant that matters is never asserted.
- **Mock-everything** — the real code path under test never runs (`noopRunner.Receive()==nil`).
- **Golden-without-oracle** — capturing current output as "correct" with no independent expectation.
- **`_ = err`** without a `// fire-and-forget:` reason · **`time.Sleep`** as sync · **map-order-dependent** assertions.

## 7. Fixtures & replay seam

- **Two schemas, two sources** (a real distinction `[V]`):
  - **stdout `stream-json`** — carries `result` with `session_id`/`usage`/`planFilePath`; drives `parseStream` `[V claude_cli.go:620-661]`. Needs one captured run; seed exists at `internal/harness/testdata/worker_stream_sample.jsonl` `[V]`.
  - **project JSONL conversation log** + `~/.claude/plans/*.md` — carries the `plan_mode` attachment (`attachment.planFilePath`) that drives plan extraction `[2]`; seed directly from existing logs, no live run.
- **Replay executable** — `cmd/replayclaude` (**delivered**): a recording *player* (writes a committed real recording to stdout, ignores argv), driven through the real `sandboxedRunner`/`ClaudeCLI` via the `binary` knob (DEFECT-06 fix). First consumer: the H1 gate (§8) replays `worker_stream_sample.jsonl` through real seatbelt. Generalizes to the L2 app-level replay.
- Real **failure** fixtures (validator error, marker-less output, merge conflict, oversized line) are first-class — they are how INV-P3/P4 go red. Secrets redacted; committed under `testdata/transcripts/`.

## 8. Proposed red gates (app + package — not exhaustive)

App-level (L2/L3): **A1** INV-P1-EXEC (prompt == plan) · **A2** INV-O1-FLOW (auto-approve reaches EventComplete) · **A3** INV-P3-VALID/DEGRADE (injected FAIL/conflict ⇒ non-success exit + artifacts).

Package-level keystones: **H1** INV-H1-CLOSE *(**landed** — `TestHarnessRunner_ReceiveClosesOnExit`, red-first demonstrated; canary `TestCanary_DEFECT01_*` live in qaverify)* · **V1** INV-P3-VALID *(red — DEFECT-02)* · **W1** INV-P3-DEGRADE *(red — DEFECT-03/04)* · **S1** INV-P2-WRITE *(covered; formalize)* · **C1** INV-P5-FAILCLOSED *(partial; extend)* · **PX1** INV-P4-PARSE/STREAM *(gap)* · **PL1** INV-P1-PLANSRC *(covered; extend)*.

## 9. Coverage ledger (by invariant, never by line %)

The status column is **generated** from `docs/qa/invariants.yaml` by `make qa-verify-write` and checked by `make qa-verify` — it cannot be hand-edited to lie (§11). `covered` = a gate test cites the invariant; `defect` = a live canary proves the bug; `gap` = no gate yet.

<!-- BEGIN GENERATED LEDGER (regenerate with: make qa-verify-write) -->

| Invariant | Pillar | Layer | Status |
|-----------|--------|-------|--------|
| INV-H1-CLOSE | P1 | L1 | defect |
| INV-H2-SESSIONID | P1 | L1 | defect |
| INV-O1-FLOW | P6 | L2 | gap |
| INV-P1-EXEC | P1 | L2 | gap |
| INV-P1-PLANSRC | P1 | L0 | covered |
| INV-P2-WRITE | P2 | L1 | gap |
| INV-P3-DEGRADE | P3 | L1 | defect |
| INV-P3-VALID | P3 | L1 | defect |
| INV-P4-PARSE | P4 | L0 | gap |
| INV-P4-STREAM | P4 | L0 | gap |
| INV-P5-FAILCLOSED | P5 | L0 | gap |
| INV-P5-ROUTE | P5 | L0 | gap |

<!-- END GENERATED LEDGER -->

## 10. The process that makes this last

1. **Spec is the oracle** — every test cites an `INV-*`; this file is reviewed like code.
2. **Red-first acceptance** — a gate is merged only with evidence it was red on a known-broken state (the §4 defects, or a deliberately corrupted fixture). A gate green on broken code is rejected as fiction.
3. **Evidence promotion** — `[2]`/`[G]` claims are promoted to `[V]` (or to `[OPEN]`) before they back a merged gate.
4. **Ban-list enforcement** — `orqestra-critic` runs §6 against every test diff.
5. **CI tiers** — L0+L1(+L2) on every push; `darwin` sandbox tier on macOS; L3 local/manual; L4 pre-release.
6. **Coverage = §9**, by invariant. Line-coverage % is explicitly not a goal.

## 11. Anti-rot & anti-cheat — how the spec defends itself

Premise to reject: *"force the model to journal in/out of the spec."* The model is **not** the spec's maintainer-of-record — the build is. The file is not read-only (that kills maintenance, and a model just edits it in the same PR); instead **every load-bearing claim is machine-checked, so a stale anchor or a weakened gate produces a red build, not a silent lie.** Trusted prose is the entire rot/cheat surface — it is shrunk toward zero. This is Orqestra's own rule (*discrete > judgment, fail-closed, LLM output is hostile input*) applied to the QA system, including the LLM that edits it.

**Source of truth:** `docs/qa/invariants.yaml` (the registry). The prose above is the human index; the §9 ledger is *generated* from the registry. Anchors cite **stable symbols** (func/type names), never line numbers — line numbers rot silently; a renamed symbol fails loudly.

**Four checks — `make qa-verify` (`cmd/qaverify`), wired into CI:**

| # | Check | Cheat it kills |
|---|-------|----------------|
| 1 | **anchor-resolve** — every anchor symbol exists in its file | stale/invented anchors; spec drifting from code |
| 2 | **traceability** — `covered` needs a citing gate test, `defect` needs a linked canary, no test may cite an unknown INV | writing `covered` with no test; hand-flipping a status; orphaned tests/invariants |
| 3 | **ledger-drift** — §9 is regenerated from the registry and must match what's committed | hand-editing the ledger to read green |
| 4 | **canary** — each `qacanary`-tagged defect test must currently PASS (the bug still reproduces) | quietly weakening a gate — weakening stops it catching its canary → red |

**The canary is the keystone.** While a bug is live, its canary asserts the buggy behavior (`TestCanary_DEFECT02_*` is implemented and proves DEFECT-02 today). To cheat, a model can't silently soften an assertion — it would have to *also* defeat a named, committed canary that catches a known bug, which is a visible, attributable, reviewable act, not silent prose-editing. When the bug is **fixed**, the canary FAILS, forcing you to retire it and add the real gate — then migrate to a **mutation canary**: the real gate plus a committed mutant that re-introduces the bug, with CI asserting the gate fails against the mutant. Same teeth, post-fix.

**Authority separation:** the agent that *does the work* never *relaxes a gate*. Edits to `docs/qa/` and to test assertions are reviewed by an independent adversary (the `orqestra-critic`, fresh context, prompted to **refute** the weakening); `CODEOWNERS` + branch protection require a second signature. Weakening is allowed — only loudly.

**What `qaverify` does NOT guarantee:** that a `covered` test is itself non-fiction (it could be a tautology). That boundary is held by the §6 ban-list enforced by the critic. The verifier guarantees *structure* (anchors valid, status honest, canaries with teeth); the critic guarantees the *test is real*. Two layers, deliberately.

**Honest limit:** a determined human+model can rip it all out. The goal is not prevention but to make the dishonest path **cost more than the honest one and turn the build red/attributable when taken** — the same bar Orqestra sets for its own workers.
