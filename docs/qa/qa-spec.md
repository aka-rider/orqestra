# Orqestra QA Specification

> The cornerstone document. A test is valid **only** if it cites an invariant ID (`INV-*`) defined here.
> No invariant → no test. No test without a cited invariant. This file is the oracle; the suite is its executable shadow.
>
> **This document defends itself in `make test`** (`internal/qaspec.TestSpecIntegrity`): a stale anchor,
> ledger drift, an untraceable status, a line-number anchor, a test-hygiene violation, or a dead canary
> all turn the standard build red. See §11.

## 0. How to read this document

**Evidence tier** — every claim is tagged so its grounding is auditable. Nothing is asserted as fact above its tier:

| Tag | Meaning |
|----|---------|
| `[V path#Symbol]` | Verified by reading that code. Anchors are **symbols, never line numbers** (machine-checked in `make test`). |
| `[T test]` | Verified by an existing real test that exercises the behavior. |
| `[G path]` | Signature/anchor confirmed by grep, body not fully read. |
| `[2]` | Second-hand (sub-agent report); **must be promoted to `[V]` before it backs a gate.** |
| `[OPEN]` | Unverified / unknown. Not admissible as fact. |

**Falsifiability rule** — every invariant carries a **Falsified by** line: the concrete change or input that turns the gate red. *An invariant with no falsifier is not an invariant — it is a virtue, and it does not belong here.* Two of the original "pillars" failed this test and were demoted (§2).

**Status** — `covered` (a real gate cites it) · `gap` (no real gate) · `defect` (a confirmed bug; a live canary proves it; see §4).

**Run outcomes** — a test run yields one of three: **GREEN** (all gates pass), **RED** (a gate fails), or **NO-VERDICT** (the harness hung, timed out, crashed, or failed to build). NO-VERDICT is the **worst** — a gate that never returns is neither pass nor fail, so it silently defeats every other gate. It is treated as failure, never "probably fine." `make test` is bounded so a hang becomes a NO-VERDICT (§11, §12).

## 1. What Orqestra is (grounded)

Orqestra runs a coding task through a chain of LLM agents and is **designed to complete without a human**. `[V internal/config/pipeline.yaml]` defines the roles; the Go engine drives them headless.

- Pipeline (Go engine): Researcher → Architect → Critic → *(optional)* plan gate → Worker → self-validation → worktree merge. `[V internal/orchestrator/run_pipeline.go]`
- The worker receives the approved plan **verbatim** behind a fixed preamble. `[V internal/agent/spec.go#BuildExecutionPromptFromPlan]`
- `internal/config/pipeline.yaml` is the harness-agnostic core: abstract models `medium`/`large`, `permission_mode` per role (researcher/architect/critic = `plan`, worker = `full`), retry budgets. `[V internal/config/pipeline.yaml]`
- Worker writes are isolated in a per-run git worktree and merged back; the seatbelt sandbox is the containment boundary. `[V internal/orchestrator/step_execute.go#WorktreeSpecFn]` `[V internal/sandbox/sandbox.go#New]`

Human approval gates are a **configurable overlay** (interactive adapter / non-`--auto-approve`), not a guarantee — `--auto-approve` runs the whole chain unattended. `[V internal/orchestrator/events.go#AutoApprove]` Therefore the safety net is not a human; it is the **truthfulness** and **hostile-input** properties below.

## 2. Pillars → falsifiable invariant families

The original list contained two virtues masquerading as pillars. Honest version:

| # | Pillar / family | Real? | Note |
|---|---|---|---|
| **P1** | **Provenance** — the approved plan is what executes | ✅ falsifiable | worker prompt = fixed preamble + plan verbatim `[V internal/agent/spec.go#BuildExecutionPromptFromPlan]` |
| **P2** | **Containment** — worker cannot escape sandbox/worktree | ✅ falsifiable | construction `[G internal/sandbox/sandbox.go]`, denial `[T sandbox_test.go]` |
| **P3** | **Truthfulness** — never report a step succeeded when it didn't | ⚠️ family, not one pillar | falsifiable **per member**; this is where the confirmed defects live (§4) |
| **P4** | **Hostile-input handling** — LLM/file/stream output is untrusted | ⚠️ narrow slice of "grounding" | the parser/path/boundary checks are gateable; the *anti-hallucination goal is **NOT gateable*** — see below |
| **P5** | **Provider-agnostic core** — any backend, fail-closed config | ✅ falsifiable | `[V internal/config/config.go#validate]`, openai env path `[V internal/harness/claude_cli.go#BuildModelEnv]` |
| **P6** | **Autonomy via delegated validation** | ⚠️ corrected | **there is no Go retry loop** — see below |

**Demoted / corrected — stated plainly so no gate is built on sand:**

- **"Grounding defeats LLM unreliability" is NOT a deterministic gate.** It is probabilistic and model-dependent — exactly the *judgment* that Orqestra itself ranks below *discrete* checks. We never write a gate asserting "no hallucination reached execution." We gate only the falsifiable slice: output is parsed by typed parsers, paths are validated under allowed roots, raw text is preserved (P4). The aspiration is observed (not gated) at the live tiers (§5, L3/L4).
- **P6 has no orchestrator-level retry loop.** The retry budget is injected as *prompt text* into the validation prompt; validation is a **single** step. `[V internal/orchestrator/step_validate.go]` The fix-retry happens *inside the model's one turn* — not gateable. The falsifiable Go-level invariant is narrow: the verdict→status mapping is correct (and DEFECT-02 is where it is not).

## 3. Invariant catalog (the heart)

Keystone invariants only — one or two per pillar, not exhaustive. Format: statement · evidence · positive/negative · observable · falsifier · seam/layer · status.

### P1 — Provenance

**INV-P1-EXEC** The worker's execution prompt equals a fixed preamble concatenated with the approved plan markdown, byte-for-byte; nothing else is injected or transformed.
- Evidence: `[V internal/agent/spec.go#BuildExecutionPromptFromPlan]` (`"Execute the following plan…\n\n" + planMarkdown`), used at `[V internal/orchestrator/step_execute.go#BuildExecutionPromptFromPlan]`.
- Positive: approved plan P → execPrompt == preamble+P.
- Negative: plan with code fences / unicode / 50KB → still verbatim, no truncation.
- Observable: string equality on the prompt handed to the worker step.
- **Falsified by:** any summarize/wrap/re-render step between gate approval and worker execution.
- Seam/Layer: L2 (replay) or L1 (capture the spec.Prompt). Status: **gap**.

**INV-P1-PLANSRC** The plan comes from the real Claude plan file under `~/.claude/plans/`; a path outside that root, a missing session ID, or an empty file is an error, never a silent fallback.
- Evidence: `[V internal/agent/plan_extract.go#ReadPlan]` (prefix gate + empty-file error), `[T plan_extract_test.go]` (security gate, empty, missing).
- Negative: planFilePath `/etc/passwd` → rejected; empty plan → error.
- **Falsified by:** removing the allowed-prefix check in `ReadPlanFile`.
- Seam/Layer: L0 + recorded-JSONL fixtures. Status: **covered** (extend hostile inputs).

### P2 — Containment

**INV-P2-WRITE** A worker process cannot write outside its worktree/session roots, nor read a secret outside the allowlist; a read-only-repo runner cannot write the repo.
- Evidence: construction `[V internal/sandbox/sandbox.go#New]` (EvalSymlinks, sandbox-exec required, SBPL chmod 0400, RepoWritable) + `[V internal/sandbox/sandbox.go#Wrap]`; denial `[T sandbox_test.go: TestDeny_WriteOutsideWorkspace / TestSeatbelt_ReadonlyRepoWriteDenied]` `[2]`.
- Observable: post-run, the forbidden file does not exist / secret not read.
- **Falsified by:** dropping `-f $SB` from the trampoline in `Wrap`, or flipping `RepoWritable`.
- Seam/Layer: L1, macOS `darwin` tier. Status: **gap** (real tests exist; not yet linked to this ID).

### P3 — Truthfulness (family)

**INV-P3-VALID** A run is reported `StatusSuccess` only if validation actually ran **and** produced evidence of passing checks. Validation that errored, was skipped, or produced no recognized checks must **not** yield success.
- Evidence: current behavior **violates this** — `[V internal/agent/validation.go#ParseValidationOutput]` (`""`/unrecognized → `VerdictPass`); the run status maps from that verdict in `[V internal/orchestrator/step_validate.go]`.
- Observable: feed empty or marker-less validation output → status must not be success.
- **Falsified by:** the code as it stands → see **DEFECT-02** (canary `TestCanary_DEFECT02_EmptyValidationParsesPass`).
- Seam/Layer: L0 (parser) + L1 (status mapping). Status: **defect**.

**INV-P3-DEGRADE** When isolation, commit, or merge is degraded or fails, the run surfaces it via an **event** and does not report success.
- Evidence: commit/merge failure now → `StatusFailed` `[V internal/orchestrator/step_merge.go]` (DEFECT-04 fixed); but worktree-create failure still silently falls back to the direct repo with only a `slog.Warn`, no event `[V internal/orchestrator/step_execute.go#WorktreeSpecFn]`.
- Observable: inject worktree-create failure → an `Event` is emitted and isolation loss is visible.
- **Falsified by:** the create-failure path as it stands → see **DEFECT-03**.
- Seam/Layer: L1 (real temp git). Status: **defect**.

### P4 — Hostile-input handling

**INV-P4-PARSE** Validation/commit/stream parsers accept arbitrary bytes without panic, always preserve raw text, and never let parser success imply work success.
- Evidence: best-effort parser + raw preserved `[V internal/agent/validation.go#ParseValidationOutput]`; commit-message empty → error `[V internal/agent/spec.go#ParseCommitMessage]`.
- Negative matrix: truncated UTF-8, 10MB line, injected `✕` inside prose, NUL bytes, marker-less essay.
- **Falsified by:** any parser that panics on bad input or drops `Raw`.
- Seam/Layer: L0 (table-driven + fuzz). Status: **gap** (only happy markers tested today).

**INV-P4-STREAM** The stream scanner has an explicit buffer and handles `scanner.Err()`; oversized/non-JSON lines degrade gracefully.
- Evidence: `[V internal/harness/stream_event.go#parseStreamLines]` (buffer set, non-JSON → Text event, returns `scanner.Err()`).
- **Falsified by:** removing the `scanner.Buffer(...)` call → long lines silently dropped.
- Seam/Layer: L0 with recorded stream-json fixtures. Status: **gap**.

### P5 — Provider-agnostic core

**INV-P5-FAILCLOSED** Config load fails on: empty role model, unresolvable model ref, unknown provider, missing/unknown provider type, native-with-base_url, non-native-without-base_url, malformed or conflicting token limits.
- Evidence: `[V internal/config/config.go#validate]` (each branch returns an error).
- Negative matrix: one case per branch; assert the error names the offending key.
- **Falsified by:** any branch downgraded to a warning or a default.
- Seam/Layer: L0 table-driven. Status: **gap** (config_test covers some branches; not yet linked + extend; pin the case-insensitive `lookupModel` behavior rather than bless it).

**INV-P5-ROUTE** A non-native (`anthropic`/`openai`) provider routes the CLI to its `base_url` via env; `native` adds no override; an unknown type errors with no fallback.
- Evidence: `[V internal/harness/claude_cli.go#BuildModelEnv]` (per-type branches; default → error).
- **Falsified by:** a `default:` that returns nil env instead of an error.
- Seam/Layer: L0 (env) + **L4** end-to-end against llama-server (§5). Status: **gap**.

### P6 — Autonomy (narrow, corrected)

**INV-O1-FLOW** `--auto-approve` drives all phases to completion without blocking on a decision channel; cancellation propagates and terminates the run.
- Evidence: gate is taken only when not auto-approving `[V internal/orchestrator/events.go#AutoApprove]`.
- **Falsified by:** an unconditional decision-channel read on the auto path.
- Seam/Layer: L2 (replay binary). Status: **gap**.

### Spine (cross-cutting — P1 / truthfulness)

**INV-H1-CLOSE** `harness.Run` returns **exactly once** when the underlying process exits — a consumer is never left hanging. **INV-H2-SESSIONID** the session id is captured from the stream.
- Evidence: `[V internal/harness/exec.go#Run]` (documented "returns EXACTLY ONCE"; joins its goroutines); session id `[V internal/harness/stream_event.go#EventSessionStart]`.
- Observable: replay a recording then exit → `Run` returns within a timeout and the sink observed a SessionID.
- **Falsified by:** a code path where `Run` can block forever (the old DEFECT-01 hang) or where `EventSessionStart` is never emitted (the old DEFECT-05).
- Seam/Layer: L1 via the replay stub through real seatbelt — `TestHarnessRun_TerminatesWhenProcessExits`. Status: **covered** (both were red pre-refactor; now green).

**INV-HARNESS-VERDICT** `make test` runs under a hard wall-clock deadline and always yields a verdict: a hang/timeout becomes a bounded **NO-VERDICT** (the process group is killed), never an indefinite hang; on completion it emits a `QA-ATTEST` token.
- Evidence: `[V internal/qarun/run.go#Run]` (deadline → SIGKILL the process group → NoVerdict); `[V cmd/qarun/main.go#main]` (QA-ATTEST on completion, NO-VERDICT exit 124); routed via `[V Makefile]`.
- Observable: a hanging child is killed at the deadline and reported NO-VERDICT promptly (`TestRun_NoVerdictOnHang`).
- **Falsified by:** removing the deadline/process-group kill, or routing `make test` around qarun → the harness-verdict check turns RED.
- Seam/Layer: L1 unit (a fake hanging command). Status: **covered**.

## 4. Confirmed defects (the falsification targets)

Each defect was verified red-first; the red→green transition is how a gate proves it is real. Status is reconciled against `flexible-pipeline` and is **machine-checked**: anchors and canaries live in `invariants.yaml`, and live canaries run in `make test`, so this table cannot drift silently from reality. Anchors are **symbols** (line numbers rot).

| ID | Pillar | Defect | Status | Anchor |
|----|--------|--------|--------|--------|
| DEFECT-01 | P1 | Sandboxed runner never closed its event channel (+ `SetEvents`/`Receive` split-brain) → main flow hangs after Claude exits. | **fixed** — `harness.Run` returns exactly once. Gate `TestHarnessRun_TerminatesWhenProcessExits` (INV-H1-CLOSE) now green. | `internal/harness/exec.go#Run` |
| DEFECT-02 | P3 | Validation that errored or produced no `✕` marker parses to `VerdictPass` → `StatusSuccess` with no passing evidence. | **live** — canary `TestCanary_DEFECT02_EmptyValidationParsesPass` runs in `make test`. | `internal/agent/validation.go#ParseValidationOutput` |
| DEFECT-03 | P3 | `worktree.Create` failure silently falls back to the direct repo, only a `slog.Warn`, no event — isolation degraded invisibly. | **live** — `step_execute.go` "falling back to direct repo". | `internal/orchestrator/step_execute.go#WorktreeSpecFn` |
| DEFECT-04 | P3 | Worktree commit failure skipped the merge but left `StatusSuccess`. | **fixed** — `step_merge.go` returns `StatusFailed` on commit failure. | `internal/orchestrator/step_merge.go` |
| DEFECT-05 | P1 | `SessionID` never set / `EventSessionStart` never emitted → plan extraction + validation continuation break. | **fixed** — `parseStreamLines` emits `EventSessionStart`. Gate (INV-H2-SESSIONID) now green. | `internal/harness/stream_event.go#EventSessionStart` |
| DEFECT-06 | P5 | Sandboxed runner hardcoded `"claude"`, ignoring the `binary` config knob. | **fixed** — `harness.Run` honors `spec.Binary`. | `internal/harness/exec.go#Run` |

**What this proves (the spec-vs-code test):** the gates are anchored to the **spec**, not the code. When `flexible-pipeline` refactored the harness severely and fixed the bugs, the contract gate `TestHarnessRun_TerminatesWhenProcessExits` flipped **red→green** by adapting only its plumbing (`Receive()` → `harness.Run`) — its assertion (no hang, session id captured) unchanged; the DEFECT-01/05 canaries correctly stopped reproducing and were retired; the anchor check named every moved symbol so the registry could be re-pointed. DEFECT-02/03 survived the refactor and remain flagged, their canary/anchor still live.

## 5. Layered seam architecture

The seam decides whether a gate is real. **Only real production code is driven; the only permitted double is a verbatim recording of real output replayed — never hand-written behavior.** Injection uses the existing `binary` config knob, honored by `[V internal/harness/exec.go#Run]` — *not* a test-only interface.

| Tier | Seam | Determinism | CI lane | Owns |
|---|---|---|---|---|
| **L0 Unit** | pure functions + recorded fixtures | deterministic | `make test` | INV-P1-PLANSRC, P4-*, P5-FAILCLOSED/ROUTE(env), parser/verdict/config matrices |
| **L1 Package** | real pkg API; the replay stub via `spec.Binary`; real temp git; real `sandbox-exec` | deterministic | `make test` + `darwin` tier | INV-H1/H2, P2-WRITE, P3-VALID/DEGRADE |
| **L2 App (replay)** | `./orqestra --auto-approve` with model `binary` → replay stub emitting recorded stream-json | deterministic | `make test` | INV-P1-EXEC, O1-FLOW, full wiring without API |
| **L3 Live e2e** | real `claude` + real API + real git + real sandbox | non-deterministic → assert **side-effects**, never model text | local/manual `make test-e2e` | whole stack; **captures/refreshes fixtures + detects claude format-drift** |
| **L4 Provider gate** | real `claude` routed (`openai` type) to local **llama-server** | semi | pre-release, tagged | **INV-P5-ROUTE end-to-end** |

**What L4 (llama-server) tests, and what it does not:** it proves the **provider-independent mechanical spine** completes on a non-Anthropic backend — config resolves an `openai` provider, `BuildModelEnv` points `ANTHROPIC_BASE_URL` at llama-server `[V internal/harness/claude_cli.go#BuildModelEnv]`, the stream parses, `SessionID` is captured, `Run` returns, the plan extracts, the sandbox holds, the merge completes, the verdict surfaces. It deliberately does **not** judge plan or code quality (that is judgment, model-dependent, non-gateable). It is a *final* gate (needs a running server, slow) and cheap enough (local) to run pre-release without API spend.

## 6. Test-fiction ban list (the immune system)

Review blockers on any PR touching `*_test.go`. The **mechanically-decidable** ones (★) are enforced in `make test` by `internal/qaspec.TestSpecIntegrity`; the rest are enforced by the `orqestra-critic` agent.

- **Tautology** — asserting a field equals the value the test just set.
- **Test-only hook/method** — install `m.cancel = func(){…}` then assert it was called; setters that bypass the real path.
- **Hand-written fake for a real dependency** — `FakeClaude`/fake git/fake sandbox; an interface that exists *only* for test injection. (Permitted: a **replay** double fed recorded real output.)
- **No-crux** — interesting setup, but the one invariant that matters is never asserted.
- **Mock-everything** — the real code path under test never runs.
- **Golden-without-oracle** — capturing current output as "correct" with no independent expectation.
- ★ **`time.Sleep` as test sync** — banned; use channels/contexts/fake clocks.
- ★ **Test cites an unknown `INV-*`**, or a `covered` invariant has no citing test (traceability).

## 7. Fixtures & replay seam

- **Two schemas, two sources** (a real distinction):
  - **stdout `stream-json`** — carries `result` with `session_id`/`usage`/`planFilePath`; drives `[V internal/harness/stream_event.go#parseStream]`. Needs one captured run; seed exists at `[V internal/harness/testdata/worker_stream_sample.jsonl]`.
  - **project JSONL conversation log** + `~/.claude/plans/*.md` — carries the `plan_mode` attachment (`attachment.planFilePath`) that drives plan extraction `[2]`; seed directly from existing logs, no live run.
- **Replay executable** — `cmd/replayclaude` (**delivered**): a recording *player* (writes a committed real recording to stdout, ignores argv), driven through the real `[V internal/harness/exec.go#Run]` via `spec.Binary`. First consumer: the H1 gate replays `worker_stream_sample.jsonl` through real seatbelt. Generalizes to the L2 app-level replay.
- Real **failure** fixtures (validator error, marker-less output, merge conflict, oversized line) are first-class — they are how INV-P3/P4 go red. Secrets redacted; committed under `testdata/transcripts/`.

## 8. Proposed red gates (app + package — not exhaustive)

App-level (L2/L3): **A1** INV-P1-EXEC (prompt == plan) · **A2** INV-O1-FLOW (auto-approve completes) · **A3** INV-P3-VALID/DEGRADE (injected FAIL/conflict ⇒ non-success + event).

Package-level keystones: **H1/H2** INV-H1-CLOSE + INV-H2-SESSIONID *(**covered, green** — `TestHarnessRun_TerminatesWhenProcessExits`; was red pre-fix)* · **V1** INV-P3-VALID *(defect — DEFECT-02 canary live in `make test`)* · **W1** INV-P3-DEGRADE *(defect — DEFECT-03)* · **S1** INV-P2-WRITE *(formalize + link)* · **C1** INV-P5-FAILCLOSED *(extend + link)* · **PX1** INV-P4-PARSE/STREAM *(gap)* · **PL1** INV-P1-PLANSRC *(covered; extend)*.

## 9. Coverage ledger (by invariant, never by line %)

The status column is **generated** from `docs/qa/invariants.yaml` by `make qa-verify-write` and checked in `make test` (§11) — it cannot be hand-edited to lie. `covered` = a gate test cites the invariant; `defect` = a live canary proves the bug; `gap` = no gate yet.

<!-- BEGIN GENERATED LEDGER (regenerate with: make qa-verify-write) -->

| Invariant | Pillar | Layer | Status |
|-----------|--------|-------|--------|
| INV-H1-CLOSE | P1 | L1 | covered |
| INV-H2-SESSIONID | P1 | L1 | covered |
| INV-HARNESS-VERDICT | P3 | L1 | covered |
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
4. **Enforced in `make test`** — `TestSpecIntegrity` runs the decidable checks; the `orqestra-critic` covers the semantic ban-list (§6) on test diffs.
5. **CI tiers** — L0+L1(+L2) on every push; `darwin` sandbox tier on macOS; L3 local/manual; L4 pre-release.
6. **Coverage = §9**, by invariant. Line-coverage % is explicitly not a goal.

## 11. Anti-rot & anti-cheat — how the spec defends itself

Premise to reject: *"force the model to journal in/out of the spec."* The model is **not** the spec's maintainer-of-record — the build is. The file is not read-only (that kills maintenance, and a model just edits it in the same PR); instead **every load-bearing claim is machine-checked, so a stale anchor or a weakened gate produces a red build, not a silent lie.** Trusted prose is the entire rot/cheat surface — it is shrunk toward zero. This is Orqestra's own rule (*discrete > judgment, fail-closed, LLM output is hostile input*) applied to the QA system, including the LLM that edits it.

**Source of truth:** `docs/qa/invariants.yaml` (the registry). The prose above is the human index; the §9 ledger is *generated* from the registry. Anchors cite **stable symbols**, never line numbers — line numbers rot silently; a renamed symbol fails loudly.

**Enforcement is in `make test`**, not an opt-in lane: `internal/qaspec.TestSpecIntegrity` runs the static checks natively (`cmd/qaverify` is a convenience CLI over the same code, adding `--write` for the ledger). The checks:

| # | Check | Cheat / rot it kills |
|---|-------|----------------------|
| 1 | **anchor-resolve** — every registry anchor symbol exists in its file | stale/invented anchors; spec drifting from code |
| 2 | **traceability** — `covered` needs a citing gate test; `defect` needs a linked canary; no test cites an unknown INV; a named canary test must exist | writing `covered` with no test; hand-flipping a status; orphaned tests/invariants |
| 3 | **ledger-drift** — §9 is regenerated from the registry and must match what's committed | hand-editing the ledger to read green |
| 4 | **prose-anchor** — no line-number anchors anywhere in this file; every `[V path#Symbol]` resolves | the exact rot I once "disclosed" instead of failing — now a red build |
| 5 | **test-hygiene** — decidable §6 bans (e.g. `time.Sleep` in `*_test.go`) | fiction creeping into the test suite |
| 6 | **canary execution** — live-defect canaries are normal `make test` tests; they pass while the bug lives and fail when it is fixed | quietly fixing a bug without retiring its canary / weakening a gate |

**The canary is the keystone.** A live-defect canary (e.g. `TestCanary_DEFECT02_EmptyValidationParsesPass`) runs in `make test` and asserts the buggy behavior reproduces. To cheat, a model can't silently soften an assertion — it would have to *also* defeat a named, committed canary that catches a known bug, a visible, attributable, reviewable act. When the bug is **fixed**, the canary FAILS `make test`, forcing whoever fixed it to retire the canary and add the real gate — then migrate to a **mutation canary** (the real gate plus a committed mutant that re-introduces the bug; CI asserts the gate fails against the mutant).

**Authority separation:** the agent that *does the work* never *relaxes a gate*. Edits to `docs/qa/` and to test assertions are reviewed by an independent adversary (the `orqestra-critic`, fresh context, prompted to **refute** the weakening); `CODEOWNERS` + branch protection require a second signature. Weakening is allowed — only loudly.

**What `make test` does NOT guarantee:** that a `covered` test is itself non-fiction (it could be a tautology). That boundary is held by the §6 ban-list enforced by the critic. The build guarantees *structure* (anchors valid, status honest, canaries with teeth); the critic guarantees the *test is real*. Two layers, deliberately.

**Honest limit:** a determined human+model can rip it all out. The goal is not prevention but to make the dishonest path **cost more than the honest one and turn the build red/attributable when taken** — the same bar Orqestra sets for its own workers.

## 12. The hang, and the LLM tripwire

**Post-mortem (the failure this section answers).** Across several turns the assistant reported the suite "green" without ever observing it terminate. Two causes: (a) the `TestSpecIntegrity` walk recursed into nested `.claude`/`.orqestra` worktrees (237 test files across 9 nested checkouts, with `.orqestra/…/.orqestra` nesting) → an unbounded walk → the suite hung; (b) a wall of `ok` lines was mistaken for completion. A hang is a **NO-VERDICT**: it yields neither pass nor fail, so every gate is defeated and any "green" report is a hallucination.

**Fixes.**
- *Root cause:* the integrity walk now skips `.claude`/`.orqestra` — `[V internal/qaspec/checks.go#skipDirs]`.
- *Bound:* `make test` runs through `cmd/qarun`, which kills the process group at a deadline and reports **NO-VERDICT** instead of hanging — INV-HARNESS-VERDICT.
- *Attestation:* on genuine completion qarun emits `QA-ATTEST commit=<sha> dur=<s> SUITE-COMPLETE`. This token is the **only** valid evidence the suite passed — it cannot exist without a run that actually finished.

**The tripwire.** Green is now unfakeable without completion: a hang or partial run cannot produce a `QA-ATTEST`. Two machine checks (run in `make test` via `TestSpecIntegrity`) keep it intact:
- **harness-verdict** — `make test` must route through `cmd/qarun`, and qarun must still emit the verdict tokens; gutting the bound or the attestation turns it RED.
- **forbidden-claim** — prose may not assert the suite passed; such a statement is rot unless it quotes a fresh `QA-ATTEST`.

And the norm (`CLAUDE.md`): never report the suite green without quoting a fresh `QA-ATTEST`; a hang/timeout is NO-VERDICT, treated as failure.

**Honest limit.** A pure chat-level false claim cannot be machine-prevented. This removes the ability to *obtain* a green signal without completion and to *encode* a green claim in the repo, and binds the norm; the residue is the critic's job.
