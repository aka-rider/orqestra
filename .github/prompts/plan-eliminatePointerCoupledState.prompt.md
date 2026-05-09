# Plan: Eliminate Pointer-Coupled State — Go-Native Refactor

## Goal

Kill every gratuitous pointer in the codebase. Pointers that exist today create invisible coupling: shared mutable references, nil-guard ceremony, GC pressure. Replace with value semantics. Pointer stays ONLY when justified: sync primitives, genuine nil-semantics, interface satisfaction.

## Pointer Inventory (complete)

### KILL — gratuitous, no justification

| Location                           | What                       | Why it's wrong                          |
| ---------------------------------- | -------------------------- | --------------------------------------- |
| `harness.RunResult.Usage`          | `*TokenUsage`              | 24 bytes — zero value works as "absent" |
| `agent.RawPlan.Usage`              | `*harness.TokenUsage`      | Infrastructure on domain type           |
| `agent.PlanOutput.Usage`           | `*harness.TokenUsage`      | Infrastructure on domain type           |
| `agent.GatewayResult.Usage`        | `*harness.TokenUsage`      | Infrastructure on domain type           |
| `agent.ValidationReport.Usage`     | `*harness.TokenUsage`      | Infrastructure on domain type           |
| `agent.Gateway.cfg`                | `*config.GatewayConfig`    | Read-only, never mutated                |
| `agent.Planner.cfg`                | `*config.PlannerConfig`    | Read-only, never mutated                |
| `agent.Researcher.cfg`             | `*config.ResearcherConfig` | Read-only, never mutated                |
| `GitDiffSummary()` return          | `*WorkDiff`                | 3 strings + slice, copyable             |
| `GitDiffSummaryStaged()` return    | `*WorkDiff`                | Same                                    |
| `FormatValidationFeedback()` param | `*ValidationReport`        | Read-only                               |
| `extractJSONUsage()` return        | `*TokenUsage`              | 24 bytes                                |
| `extractStreamUsage()` return      | `*TokenUsage`              | 24 bytes                                |
| `ClaudeCLI.RunPrint` local         | `var usage *TokenUsage`    | Pointer local var for value type        |
| `ClaudeCLI.RunStreaming` local     | `var usage *TokenUsage`    | Same                                    |
| `ClaudeCLI.RunContinue` local      | `var usage *TokenUsage`    | Same                                    |
| `orchestrator.usageIn()`           | nil-guard helper           | Exists only because of `*TokenUsage`    |
| `orchestrator.usageOut()`          | nil-guard helper           | Same                                    |

### KEEP — justified

| Location                                               | Field/Sig                                                  | Justification                            |
| ------------------------------------------------------ | ---------------------------------------------------------- | ---------------------------------------- |
| `Specification.Scope *Scope`                           | nil = "no constraints" vs empty = "constrained to nothing" | genuine nil semantics (JSON `omitempty`) |
| `ClaudeCLI.small *config.ResolvedModel`                | nil = "no utility model configured"                        | genuine optional                         |
| `config.MCPServers *[]string`                          | nil = "all servers", `[]` = "no servers"                   | trinary                                  |
| `config.ResolveUtilityModel() *ResolvedModel`          | nil = "not defined"                                        | genuine optional                         |
| `*StreamBuffer`, `*StatsTracker`, `*Limiter`, `*Store` | contain `sync.Mutex`                                       | must not copy                            |
| `*Sandbox`, `*Scheduler`, `*ProfileBuilder`            | process/resource handles                                   | must not copy                            |
| `*ClaudeCLI`, `*SandboxCLIRunner`                      | hold interface fields                                      | copying would alias                      |
| `*LimitedRunner`                                       | wraps interface                                            | copying would alias                      |
| `New*()` → `*T` for service types                      | standard Go constructor pattern                            | methods need stable receiver             |

---

## Phase 1: Harness Boundary

Make `TokenUsage` a value on `RunResult`. Zero value = "not reported."

**Files:** `internal/harness/claude_cli.go`, `internal/harness/sandbox_cli_runner.go`, `internal/tokenlimit/runner.go`, `internal/harness/harness_e2e_test.go`, `internal/tokenlimit/runner_test.go`

**Changes:**

1. `RunResult.Usage`: `*TokenUsage` → `TokenUsage`
2. `extractJSONUsage`, `extractStreamUsage`: return `TokenUsage` not `*TokenUsage`; zero return instead of nil
3. All `RunResult` literal constructions: `Usage: &TokenUsage{...}` → `Usage: TokenUsage{...}`
4. All 3 local `var usage *TokenUsage` in `ClaudeCLI` methods → `var usage TokenUsage`; `usage = &TokenUsage{...}` → `usage = TokenUsage{...}`
5. `LimitedRunner`: `result.Usage != nil && result.Usage.TotalTokens > 0` → `result.Usage.TotalTokens > 0` (×3 methods)
6. E2E test: remove `resp.Usage == nil` nil-checks; positive assertions stay
7. Runner test: `Usage: &harness.TokenUsage{...}` → `Usage: harness.TokenUsage{...}`; `Usage: nil` → `Usage: harness.TokenUsage{}`

**Acceptance Criteria:**

- `grep '\*TokenUsage' internal/harness/ internal/tokenlimit/` → **0 hits**
- `grep '\.Usage != nil' internal/tokenlimit/` → **0 hits**
- `go test ./internal/harness/... ./internal/tokenlimit/...` → green

> ⚠️ **Phase 1 does not compile standalone.** `agent/` files still hold `*harness.TokenUsage` fields until Phase 2 removes them. Phases 1 and 2 **must land in a single atomic commit**. Do not run `go build ./...` until Phase 2 is complete.

---

## Phase 2: Domain Purge (depends on Phase 1)

Rip `Usage` from every `agent.*` domain type. Agent methods return `(Result, harness.TokenUsage, error)`.

**Files:** `internal/agent/spec.go`, `internal/agent/gateway.go`, `internal/agent/validation.go`, `internal/agent/planner.go`, `internal/agent/researcher.go`, `internal/orchestrator/orchestrator.go`, all corresponding test files

**Changes:**

1. Delete `Usage` field from: `RawPlan`, `PlanOutput`, `GatewayResult`, `ValidationReport`
2. `Evaluate()` → `(GatewayResult, harness.TokenUsage, error)` — return `result.Usage` as 2nd value
3. `Research()`, `ResearchStreaming()` → `(RawPlan, harness.TokenUsage, error)` — return `result.Usage`
4. `Refine()`, `RefineStreaming()`, `RefineWithComments()`, `RefineWithCommentsStreaming()`, `parsePlanResult()` → 3-return
5. Orchestrator: `gwResult, gwUsage, err := gw.Evaluate(...)` → emit `gwUsage.InputTokens` directly. Same for researcher, planner.
6. Orchestrator worker/validator sites (direct `harness.RunResult`, **not** domain types): replace `usageIn(workResult.Usage)` → `workResult.Usage.InputTokens`, `usageOut(workResult.Usage)` → `workResult.Usage.OutputTokens` (×2 at line ~478); same for `valResult.Usage` (×4 at lines ~502/514). These 6 sites are in neither Phase 1 nor the domain purge above — they must be patched here before deleting `usageIn()`/`usageOut()`.
7. **Delete** `usageIn()` and `usageOut()` — dead after steps 5–6
8. Tests: `gwResult, err := ...` → `gwResult, _, err := ...` (agent tests). Mock `RunResult` value Usage (orchestrator tests).

**Acceptance Criteria:**

- `grep 'Usage.*harness' internal/agent/` → **0 hits**
- `grep 'usageIn\|usageOut' .` → **0 hits** (entire repo)
- No `agent.*` **struct field** references a `harness.*` type. Function signatures may retain `harness.TokenUsage` in their return types — this is expected and correct.
- `internal/agent/validation.go` specifically: no `harness` import remains (its only harness coupling was the `Usage` field; no runner, no function signature references it)
- `go test ./internal/agent/... ./internal/orchestrator/...` → green
- `go test ./...` → full suite green
- `go vet ./...` → clean

---

## Phase 3: Remaining Pointer Sweep (parallel with Phase 2)

**Files:** `internal/agent/work_audit.go`, `internal/agent/gateway.go`, `internal/agent/planner.go`, `internal/agent/researcher.go`, `internal/agent/validation.go`, `internal/orchestrator/orchestrator.go`, corresponding test files

**Changes:**

1. `GitDiffSummary()`, `GitDiffSummaryStaged()` → return `(WorkDiff, error)` not `(*WorkDiff, error)`. All `return nil, ...` error paths (×2 in `GitDiffSummary`, ×1 in `GitDiffSummaryStaged`) → `return WorkDiff{}, ...`. Struct literal constructions: `return &WorkDiff{...}` → `return WorkDiff{...}`.
2. `Gateway.cfg`, `Planner.cfg`, `Researcher.cfg` → value fields, constructors accept value
3. Orchestrator callers: `&e.Config.Gateway` → `e.Config.Gateway`
4. `FormatValidationFeedback(report *ValidationReport)` → `FormatValidationFeedback(report ValidationReport)`
5. All test files: drop `&` from cfg/report/workdiff constructions

**Acceptance Criteria:**

- `grep '\*WorkDiff' internal/agent/` → **0 hits**
- `grep 'cfg\s*\*config' internal/agent/` → **0 hits**
- `grep 'FormatValidationFeedback.*\*' internal/agent/` → **0 hits**
- `go test ./...` → green

---

## Phase 4: Codify (after implementation proven)

File: `.github/copilot-instructions.md`

Add Core Principles and Banned Patterns using the exact numbered-list and inline-example style of the existing `<core_principles>` and `<banned_patterns>` blocks.

**Core Principle 13 — Value semantics by default:**

> Prefer value types over pointers. A pointer is justified only when: the type contains a sync primitive, nil has distinct meaning from zero value, or the type is a process/resource handle. 24-byte structs with a working zero value are not justified. Gratuitous pointers create invisible aliasing, nil-guard ceremony, and GC pressure.

**Core Principle 14 — Execution metadata is not domain state:**

> Token usage, timing, session IDs, and other infrastructure metadata must not live as fields on domain types (`RawPlan`, `GatewayResult`, `ValidationReport`, etc.). Return metadata as a separate value: `(Result, harness.TokenUsage, error)`. Domain types belong to the domain; the caller decides metadata lifecycle.

**Banned Pattern 9 — Gratuitous pointer fields when zero value works:**

> `Usage *TokenUsage` on any struct where `TokenUsage{}` (zero value) represents "not reported" — banned. Use `Usage TokenUsage`. A real LLM call never reports zero total tokens, so zero is unambiguous absence.

**Banned Pattern 10 — Infrastructure metadata on domain types:**

> `Usage *harness.TokenUsage \`json:"-"\``buried on`GatewayResult`, `RawPlan`, `ValidationReport`, or any other agent domain struct — banned. Infrastructure packages must not be imported by domain types to carry side-channel metadata. Surface it at the call boundary.

**Acceptance Criteria:**

- All four items present verbatim (or minimally edited for fit), matching the existing doc's numbered-list and `>` block style
- `go test ./...` → still green

---

## Decisions

- **No new interfaces.** Current concrete agents + inline construction is already minimal. Interfaces exist where needed (`CLIRunner`).
- **Zero `TokenUsage` = "not reported."** 24 bytes. A real LLM call never reports zero total tokens.
- **`Scope *Scope` stays.** Genuine nil-vs-empty distinction.
- **3-return `(Result, Usage, error)` not wrapper struct.** Explicit. Caller decides metadata lifecycle. No shared mutable state.
- **Event struct unchanged.** Already `int64` scalars — correct shape. Typed-event refactor is a separate TUI concern.

## Complete File Manifest

### Modified

- `internal/harness/claude_cli.go` — TokenUsage value, RunResult.Usage value, local var cleanup
- `internal/harness/sandbox_cli_runner.go` — extractJSONUsage/extractStreamUsage return value, RunResult construction
- `internal/harness/harness_e2e_test.go` — nil check removal
- `internal/tokenlimit/runner.go` — nil guard simplification
- `internal/tokenlimit/runner_test.go` — mock RunResult updates
- `internal/agent/spec.go` — delete Usage from RawPlan, PlanOutput
- `internal/agent/gateway.go` — delete Usage from GatewayResult, 3-return Evaluate, cfg by value
- `internal/agent/validation.go` — delete Usage from ValidationReport, FormatValidationFeedback by value
- `internal/agent/planner.go` — 3-return Refine\*, parsePlanResult, cfg by value
- `internal/agent/researcher.go` — 3-return Research\*, cfg by value
- `internal/agent/work_audit.go` — WorkDiff by value
- `internal/agent/gateway_test.go` — 3-return, cfg by value
- `internal/agent/planner_test.go` — 3-return, cfg by value
- `internal/agent/researcher_test.go` — 3-return, cfg by value
- `internal/agent/validation_test.go` — FormatValidationFeedback by value
- `internal/agent/work_audit_test.go` — WorkDiff by value
- `internal/orchestrator/orchestrator.go` — 3-return extraction, delete usageIn/usageOut, cfg by value callers
- `internal/orchestrator/orchestrator_test.go` — mock RunResult value Usage
- `internal/tui/app_test.go` — mock RunResult value Usage (if applicable)
- `.github/copilot-instructions.md` — principles 13-14, patterns 9-10

### Not Modified (confirmed safe)

- `internal/agent/spec_test.go`, `project_test.go`, `project.go`, `session.go`, `helpers.go`
- `internal/plan/spec.go`, `artifact.go`, `spec_test.go`, `artifact_test.go`
- `internal/config/config.go`, `config_test.go`, `graph.go`
- `internal/scheduler/scheduler.go`, `graph.go`, `event.go`, `scheduler_test.go`
- `internal/sandbox/*`
- `internal/tui/model.go`, `layout.go`, `tui.go`, `styles.go`, `messages.go`, `mascot.go`
- `internal/harness/output.go`, `query.go`, `ringbuf.go`, `stats.go`, `claude_cli_test.go`
- `internal/tokenlimit/limiter.go`, `store.go`, `store_test.go`
- `cmd/orqestra/main.go`
