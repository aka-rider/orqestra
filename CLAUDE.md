# Orqestra — Engineering Standard (root)

Orqestra is a macOS-first Go CLI/TUI that drives Claude Code through a harness (subprocess +
stream parse), not model APIs. This is the binding engineering standard; `MUST`/`NEVER` are law —
lead every rule by doing the right thing. Deeper TUI rules live in `internal/tui/CLAUDE.md`
(auto-loaded when you work there).

## 0. Prime Directive — report only what is true; keep the user's base recoverable

Rule 0: ALWAYS make user-visible state match reality. NEVER present an unverified or failed step
as success.

### 0.1 Harm ladder — rank every defect by its worst rung; always choose the lowest

1. **Catastrophic — a false claim of success.** A swallowed or downgraded error shown as done;
   `make test` called GREEN on a NO-VERDICT; a merge that corrupts the user's base irrecoverably.
2. **Severe — lost recoverability.** A broken worktree or half-finished merge with no clean return
   to the user's base.
3. **Tolerable — a surfaced failure.** A returned error, an `EventError`, a halted pipeline that
   loses nothing. ALWAYS prefer this over rungs 1–2.

A best-effort diagnostic (session-log copy, plan-history diff, commit-message generation) may
continue after a failure ONLY when it carries a `// fire-and-forget: <reason>` AND user-visible
state stays truthful.

## 1. Go fundamentals

### 1.1 Every returned error gets exactly one fate

ALWAYS do ONE of these with a returned `error`:

1. Return it wrapped — verb + resource + `%w`.
2. Drop it on purpose — `// fire-and-forget: <reason>`.

```go
// RIGHT — wrap (internal/harness/claude_cli.go:72)
return nil, fmt.Errorf("resolve model_ref %q: %w", modelRef, err)
// RIGHT — drop on purpose (internal/sandbox/sandbox.go:261)
_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup
// WRONG — these fail review:
data, _ := json.Marshal(v)         // dropped with no reason
if err != nil { return nil }       // swallowed
if err != nil { log.Printf(...) }  // logged, then continued past a real failure
```

ALWAYS use `%w` (never `%s`/`%v`) so `errors.Is`/`errors.As` keep working. Call `.Error()` only at
the display boundary. Handle `os.IsNotExist` ONLY when absence is valid; otherwise wrap and return.

### 1.2 Prefer values; make the zero value meaningful

ALWAYS pass values unless nil has a defined meaning or the type owns a resource / `sync` primitive.
For optional data crossing goroutines, use a `struct{ …; Valid bool }`, not `*T`.

```go
// RIGHT (internal/harness/exec.go:29) — zero value means "new session"
type SessionRef struct { ID string; Valid bool }
```

### 1.3 Context is an argument; shared state is owned

ALWAYS pass `context.Context` as the first argument; cancel it to stop. NEVER store it on a struct.
Service lifetime is `Run(ctx context.Context) error` — no `Start()/Stop()`, no `done` channel.
Every goroutine exits via `ctx.Done()` or is joined through its result channel after cancel. When
stop reasons differ, distinguish them with `context.WithCancelCause` (the cause is the signal; add
no `stopped bool`). Give every JSONL scanner a `Buffer(...)` and a `scanner.Err()` check. Messages
crossing goroutines carry copies or immutable values.

```go
// RIGHT (internal/orchestrator/engine.go:17)
func (e *Engine) Run(ctx context.Context, input Input) (Result, error)
// RIGHT (internal/harness/stream_event.go:246,283)
scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)
if err := scanner.Err(); err != nil { return rawBuf.String(), err }
// RIGHT — distinct stop reasons (internal/orchestrator/agent_supervisor.go:75)
ctx, cancelCause = context.WithCancelCause(parent)
// WRONG:
type Model struct { ctx context.Context }
```

### 1.4 Fallible constructors return (T, error)

ALWAYS return `(T, error)` from a constructor that can fail (runners, sandboxes, stores, parsers,
resolvers, orchestrators). NEVER return `nil, nil` to mean "disabled".

```go
// RIGHT (internal/sandbox/sandbox.go:38)
func New(cfg Config) (*Sandbox, error)
```

### 1.5 Make illegal states unrepresentable

ALWAYS model mutually-exclusive modes (screen modes, phases, variants) as ONE active sub-model.
NEVER hold every variant's fields side by side plus `hasX` bools.

### 1.6 Fail closed on user intent and integrity

ALWAYS return an error when a user-supplied path, model ref, prompt file, config key, or command
target is missing or invalid — NEVER default silently. For user-initiated creation use `os.Mkdir`
(it surfaces `EEXIST`), NEVER `os.MkdirAll`. Treat LLM output as hostile input: parse typed
formats with typed parsers, validate paths under allowed roots, run commands only through known
execution boundaries, and preserve raw text when parsing is advisory. Genuinely optional discovery
may return empty; explicit intent must error.

### 1.7 Determinism & file hygiene

ALWAYS sort map keys before you render, compare, or persist.

```go
// RIGHT (internal/config/config.go:120-125)
for k := range c.Models { names = append(names, k) }
sort.Strings(names)
```

Keep one primary entity per file. NEVER create `types`/`utils`/`helpers`/`misc` packages. A Go file
over 500 lines is a smell — split it (current offenders: `internal/harness/exec.go` 525,
`internal/config/config.go` 518; do not pile on).

### 1.8 Domain vs infrastructure

Keep execution metadata — token usage, session IDs, log paths, timings, plan-file paths — on
`harness.RunResult` and orchestration boundaries. NEVER add those fields to domain structs
(`agent.ValidationReport`, `agent.Issue`, …).

## 2. Architecture facts (current — `internal/`)

- **Pipeline** (`orchestrator/run_pipeline.go`): a typed `Step[I,O]` chain —
  `Deliberate → (human gate loop: Revise) → Execute → Validate? → Integrate?`. Phases are
  `PhasePlanning` (:102), `PhaseExecuting` (:147), `PhaseSelfValidating` (:159). Validate and
  Integrate are optional (nil = skip).
- **Plan contract**: Deliberate/Revise return `orchestrator.PlanOutput` (run_pipeline.go:21). The
  plan markdown ALWAYS comes from Claude's plan file via `agent.ReadPlan` (plan_extract.go:23) —
  primary: `PlanFilePath` from the run stream; fallbacks: session JSONL, then a scan of
  `~/.claude/plans/`. NEVER scrape a plan from stdout.
- **Harness** (`harness.ClaudeCLI`): owns the subprocess, stream-JSON parse, session IDs,
  `harness.RunResult` (`Usage`/`SessionID`/`PlanFilePath`, claude_cli.go:24), and the MCP bridge.
- **Sandbox**: worker writes go through the seatbelt sandbox (`sandbox.New`) inside a per-run git
  worktree (`internal/worktree/`).
- **Budgets**: `orchestrator/budget.go`; exhaustion is `harness.ErrBudgetExhausted` (runner.go:78).
- **Config**: YAML via `internal/config/`; defaults embedded from `internal/config/pipeline.yaml`.
- **Agents**: Researcher / Architect / Critic / Worker run through `harness.ClaudeCLI`; Execute
  returns `ExecuteOutput` (worker session ID + target branch).
- **rune dependency**: the TUI imports rune (`replace rune => <local checkout>`, go.mod:49). rune
  types stay confined to `runeui.go`, `promptinput.go`, `screen_prompt.go`.
- Legacy `agent.Specification`, `ProjectPlan`, `WorkPackage`, and `internal/scheduler` are GONE.
  Do not reintroduce them.

### 2.1 Package ownership (the real tree)

| Package | Owns |
|---|---|
| `agent/` | plan extraction `ReadPlan` (plan_extract.go), `CheckPlanHealth` (plancheck.go), validation parsing — `ParseValidationOutput`/`ValidationReport`/`Issue`/`DeriveVerdict` (validation.go), integrator prompts + `ParseIntegratorGiveUp` (integrator.go), worker/validation/commit prompts (spec.go), session-artifact helpers (session.go) |
| `harness/` | `ClaudeCLI` subprocess, stream-JSON parse (stream_event.go), `Run(ctx)` (exec.go:111), MCP bridge wiring, model-env construction, `RunResult` |
| `orchestrator/` | phase order + gates (run_pipeline.go), `Engine.Run` (engine.go:17), agent supervision + cancel cause (agent_supervisor.go), budgets (budget.go), events + `Result.ConflictFiles` (events.go, engine_types.go) |
| `sandbox/` | seatbelt config validation, SBPL profile build, env scrub, sandbox-exec wrap, process-group cleanup |
| `mcp/` | `QuestionBridge` — AskUserQuestion over a unix socket (bridge.go) |
| `worktree/` | per-run git worktree: `Create`, `CommitAll`, `MergeInto`, conflict listing, `abortMerge` |
| `project/` | repo-root checks + init: `CheckGitRoot`, `IsInitialized`, `Init` (root.go) |
| `qarun/` | bounded test runner that prints `QA-ATTEST` |

There is no `internal/scheduler/` and no `internal/plan/` — do not reference them.

## 3. Where the TUI rules live

The TUI (Bubble Tea MVU) has its own rules in `internal/tui/CLAUDE.md`. It auto-loads when you work
under `internal/tui/` and is NOT re-injected after a context compaction — if you are editing the TUI
and its rules are not in context, read it now.

## 4. Commands

| Command | Does |
|---|---|
| `make build` | build `./orqestra` (CGO off, stripped) |
| `make test` | unit + race + coverage, run through `cmd/qarun` under a hard deadline |
| `make test-integration` | `-tags integration`; needs `git`, `go build` |
| `make test-sandbox` | `-tags 'darwin integration'`; needs `sandbox-exec` (macOS) |
| `make lint` | `go vet ./...` |
| one test | `go test -race ./internal/<pkg>/ -run TestX -v` |
| run | `make run`; headless: `./orqestra --prompt "…" --auto-approve --config orqestra.yaml` |

`make test-e2e` is a placeholder (no live e2e tests yet), not coverage.

## 5. Backend rules (agent / harness / sandbox / orchestrator)

### 5.1 Plan & artifact integrity

- ALWAYS read the plan from Claude's plan file via `agent.ReadPlan` (plan_extract.go:23); treat
  stdout as advisory only.
- Primary source is `PlanFilePath` from the run stream. A directory scan of `~/.claude/plans/` is a
  last-resort fallback used ONLY after the typed sources fail, and it MUST log why the primary
  source failed.
- A missing session ID, missing JSONL log, an unsafe plan path (outside `~/.claude/plans/`), or an
  empty plan file is an integrity failure: RETURN an error carrying the session ID and the path.
- `CheckPlanHealth` warnings are advisory — show them; they do NOT prove a plan is correct.
- Worker self-validation text is advisory: preserve the raw output, parse marker lines defensively
  (`ParseValidationOutput`), and NEVER turn parser success into proof that work passed without
  command or artifact evidence.

```go
// RIGHT — fail closed on an empty plan
if strings.TrimSpace(plan) == "" {
    return "", fmt.Errorf("read plan for session %s: empty plan file %s", sessionID, path)
}
// WRONG — never reconstruct a plan from stream text
plan := lastAssistantMessage
```

### 5.2 Sandbox & execution

- ALWAYS run worker execution, validation continuations, and merge-producing work through the
  seatbelt sandbox (`sandbox.New`) or a test double — NEVER a raw shell.
- `sandbox.New` failure is fatal for sandboxed execution: missing HOME, missing `sandbox-exec`,
  invalid repo/worktree/session paths, bad proxy env, or profile-build failure → RETURN, no silent
  fallback.
- Prefer worktree isolation for repo writes. If worktree creation or branch detection fails, an
  explicit, tested, user-visible fallback to writable-repo execution is required — never silent.
- `RepoWritable` is high-risk: justify it by execution mode and test read-only repo + writable
  worktree.
- ALWAYS kill the process group on cancel: `Setpgid` (sandbox.go:207) + negative-PID SIGKILL
  (sandbox.go:261). Keep both covered by tests when touching subprocess code.

### 5.3 Harness & streaming

- ALWAYS give the Claude stream scanner a bounded `Buffer(...)` and check `scanner.Err()`
  (stream_event.go:246,283).
- Result, session ID, usage, and plan-file path come from typed parsed events. Non-JSON lines may
  be logged for diagnostics, but never drive control flow.
- `harness.RunResult` carries execution metadata (`Usage`/`SessionID`/`PlanFilePath`). Domain
  structs MUST NOT grow those fields (§1.8).
- Runner factories that can fail return `(runner, error)` (§1.4) — never a nil runner to mean
  "disabled/misconfigured".
- MCP bridge failures are classified: a startup failure degrades question support; a malformed
  model tool-call returns an MCP error; a bridge IO error includes socket + operation context.

### 5.4 Orchestrator boundaries

- An integrity failure RETURNS, emits `EventError`, or gates the user.
- A retry emits accurate phase/agent state and preserves metadata for failed attempts.
- Plan-history failure may disable diffs, but the gate still shows the current plan and says
  diffing is unavailable.
- Worker self-validation failure is NEVER shown as success: if execution continues, artifacts and
  events show the validator status and the raw text.
- Merge conflicts surface through `orchestrator.Result.ConflictFiles` (engine_types.go:40) — never
  hidden behind a completion event. The integrator gives up by default (`ParseIntegratorGiveUp`);
  safety is recoverability of the user's base, not a merge forced through (§0).

## 6. Debugging headless runs via Claude CLI logs

TUI mode silences ordinary stderr; Claude's on-disk logs are the ground truth when a run hangs or
errors opaquely.

| Path | Contents |
|---|---|
| `~/.claude/sessions/` | Active process metadata: PID, session ID, cwd, CLI version (one JSON per running `claude`) |
| `~/.claude/projects/-Users-<user>-Developer-orqestra/` | Per-session JSONL conversation logs; filename is the session UUID |
| `~/.claude/debug/latest` | Symlink to the most recent debug trace (when debug mode was on) |

- Find recent sessions: `ls -lt ~/.claude/projects/-Users-*-Developer-orqestra/*.jsonl | head -5`.
- JSONL fields: `"type":"user"` (harness prompt), `"type":"assistant"` (check
  `message.content[].text`), `"isApiErrorMessage":true` (model did not run — read the text),
  `"error":"unknown"` (transport failure, not a refusal).
- Classify `ConnectionRefused` / timeout / rate-limit / auth as infrastructure until code evidence
  says otherwise. Cross-reference the UUID with `~/.claude/sessions/<pid>.json`.

## 8. Testing & verdicts

A run is GREEN, RED, or **NO-VERDICT** (hang / timeout / crash / build-fail). NO-VERDICT is a
failure — never "probably fine". `make test` runs through `cmd/qarun` under a deadline, so a hang
becomes a bounded NO-VERDICT; a real pass prints `QA-ATTEST … SUITE-COMPLETE`
(cmd/qarun/main.go:53). ALWAYS quote a fresh `QA-ATTEST` line before you call `make test` green;
per-package `ok` lines are not completion. Use table-driven tests for matrices (e.g. cmd/orqestra/main_test.go:60);
add `-race` when touching concurrency, streaming, sandbox, harness, or orchestrator state. NEVER
use `time.Sleep` to synchronize a test — use channels, contexts, or fake clocks.

Run the narrowest package after a change; cover the invariant class, not one happy path:

| Area | Cover |
|---|---|
| Plan extraction | missing session ID, missing JSONL, invalid plan path, path outside `~/.claude/plans/`, empty plan, truncated markdown, fallback logging |
| Harness streaming | malformed JSONL, large lines, scanner errors, result error events, usage + session ID + plan-path extraction (initial + continuation) |
| Sandbox | missing HOME, missing sandbox-exec, invalid symlinks, env scrub, proxy-env validation, process-group cleanup, read-only repo + writable worktree |
| Orchestrator | phase order, retry exhaustion, gate re-entry, cancellation, question-bridge degradation, artifact-status truth, validation verdict parsing, worktree fallback visibility, merge-conflict surfacing |
| Budgets | pre-call exhaustion, post-call recording exhaustion, zero usage unreported, status reporting, wrapped-runner continuation |

## 9. LLM pitfalls — each is a hard violation

| # | Anti-pattern | Rule |
|---|---|---|
| 1 | `data, _ := …` or `if err != nil { return nil }` — swallowed error | §1.1 |
| 2 | storing/returning an `error` as a `string` (severs the `%w` chain) | §1.1 |
| 3 | `context.Context` stored as a struct field | §1.3 |
| 4 | `done chan struct{}` or `Start()/Stop()` instead of `Run(ctx)` | §1.3 |
| 5 | a fallible constructor returning `nil, nil` for "disabled" | §1.4 |
| 6 | a struct holding every mode's fields + `hasX` bools | §1.5 |
| 7 | `os.MkdirAll` / a silent default for user-supplied intent | §1.6 |
| 8 | rendering or persisting an unsorted map | §1.7 |
| 9 | a `types`/`utils`/`helpers` package, or a file > 500 LoC | §1.7 |
| 10 | execution metadata (usage, session ID, paths) on a domain struct | §1.8 |
| 11 | calling `make test` green on a NO-VERDICT / with no `QA-ATTEST` quoted | §0, §8 |
| 12 | reconstructing a plan from stdout instead of `agent.ReadPlan` | §5.1 |
| 13 | treating an empty/missing plan as usable instead of erroring | §5.1 |
| 14 | reporting parser success as proof work passed (no command/artifact evidence) | §5.1 |
| 15 | running worker/merge work through a raw shell, not the sandbox | §5.2 |
| 16 | a `sandbox.New` failure silently falling back | §5.2 |
| 17 | a cancel that does not kill the process group | §5.2 |
| 18 | a scanner with no bounded buffer or no `scanner.Err()` check | §5.3 |
| 19 | a merge conflict hidden behind a completion event | §5.4 |
| 20 | "merge at any cost" instead of give-up-by-default | §5.4, §0 |

## 11. Pre-merge checklist

- [ ] Every returned error is wrapped with `%w` or dropped with `// fire-and-forget: <reason>`.
- [ ] No `error` downgraded to `string`; `.Error()` only at the display boundary.
- [ ] No `context.Context` on a struct; every goroutine exits via `ctx.Done()` or a joined channel.
- [ ] Fallible constructors return `(T, error)`; none return `nil, nil`.
- [ ] Mutually-exclusive modes are one active sub-model, not parallel fields + bools.
- [ ] User intent (paths, model refs, config keys) errors when missing — no silent default.
- [ ] Map keys are sorted before render/compare/persist.
- [ ] One entity per file; no new file over 500 LoC; no `types`/`utils`/`helpers` package.
- [ ] Plans read via `agent.ReadPlan`; stdout never used as a plan source.
- [ ] Integrity failures (missing session/JSONL, unsafe/empty plan) return errors with context.
- [ ] Validation parser success is never reported as proof of passing work.
- [ ] Sandboxed execution path used; `sandbox.New` failure returns, with no silent fallback.
- [ ] Stream scanner has a bounded buffer and a `scanner.Err()` check.
- [ ] Merge conflicts surface via `Result.ConflictFiles`; the integrator gives up by default.
- [ ] Cancellation kills the process group (`Setpgid` + negative-PID kill), covered by tests.
- [ ] `-race` run for any concurrency / streaming / sandbox / harness / orchestrator change.
- [ ] `make test` reported green only with a fresh `QA-ATTEST … SUITE-COMPLETE` quoted.
```
