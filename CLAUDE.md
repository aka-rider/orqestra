# Orqestra — Engineering Standard (root)

Orqestra is a macOS-first Go CLI/TUI that drives Claude Code through a harness (subprocess +
stream parse), not model APIs. This file is the binding cross-cutting standard; `MUST`/`NEVER`
are law — lead every rule by doing the right thing. Packages with extra rules carry their own
`CLAUDE.md` (see "Package rules" below).

## 0. Prime Directive — report only what is true; keep the user's base recoverable

ALWAYS make user-visible state match reality: a step is shown as done only after it verifiably
succeeded. When something fails, ALWAYS prefer a surfaced failure — a returned error, an
`EventError`, a halted pipeline that loses nothing — over the two worse outcomes:

1. **A false claim of success** (worst): a swallowed or downgraded error shown as done; `make
   test` called GREEN on a NO-VERDICT; a merge that corrupts the user's base irrecoverably.
2. **Lost recoverability**: a broken worktree or half-finished merge with no clean return to the
   user's base.

A best-effort diagnostic (session-log copy, plan-history diff, commit-message generation) may
continue after a failure ONLY when it carries a `// fire-and-forget: <reason>` AND user-visible
state stays truthful.

## 1. Go fundamentals

### 1.1 Every returned error gets exactly one fate

ALWAYS do ONE of these with a returned `error`:

1. Return it wrapped — verb + resource + `%w`.
2. Drop it on purpose — `// fire-and-forget: <reason>`.

```go
// RIGHT — wrap
return nil, fmt.Errorf("resolve model_ref %q: %w", modelRef, err)
// RIGHT — drop on purpose
_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup
// WRONG — these fail review:
data, _ := json.Marshal(v)         // dropped with no reason
if err != nil { return nil }       // swallowed
if err != nil { log.Printf(...) }  // logged, then continued past a real failure
```

ALWAYS wrap with `%w` (not `%s`/`%v`) so `errors.Is`/`errors.As` keep working; call `.Error()`
only at the display boundary. Handle `os.IsNotExist` ONLY when absence is valid; otherwise wrap
and return.

### 1.2 Prefer values; make the zero value meaningful

ALWAYS pass values unless nil has a defined meaning or the type owns a resource / `sync`
primitive. For optional data crossing goroutines, use a `struct{ …; Valid bool }`, not `*T`.

```go
// RIGHT — zero value means "new session"
type SessionRef struct { ID string; Valid bool }
```

### 1.3 Context is an argument; shared state is owned

ALWAYS pass `context.Context` as the first argument; cancel it to stop. Service lifetime is
`Run(ctx context.Context) error` — no `Start()/Stop()`, no `done` channel. Every goroutine exits
via `ctx.Done()` or is joined through its result channel after cancel. When stop reasons differ,
distinguish them with `context.WithCancelCause` (the cause is the signal; add no `stopped bool`).
Messages crossing goroutines carry copies or immutable values.

```go
// RIGHT
func (e *Engine) Run(ctx context.Context, input Input) (Result, error)
// RIGHT — distinct stop reasons
ctx, cancelCause = context.WithCancelCause(parent)
// WRONG — a context stored on a struct
type Model struct { ctx context.Context }
```

### 1.4 Fallible constructors return (T, error)

ALWAYS return `(T, error)` from a constructor that can fail (runners, sandboxes, stores, parsers,
resolvers, orchestrators). "Disabled" or "misconfigured" is an error, not a `nil, nil`.

```go
// RIGHT
func Create(root, slug string) (Dir, error)
```

### 1.5 Make illegal states unrepresentable

ALWAYS model mutually-exclusive modes (screen modes, phases, variants) as ONE active sub-model —
one field holding the current mode's model, not every variant's fields side by side plus `hasX`
bools.

### 1.6 Fail closed on user intent and integrity

ALWAYS return an error when a user-supplied path, model ref, prompt file, config key, or command
target is missing or invalid — a silent default hides the user's mistake. For user-initiated
creation use `os.Mkdir` (it surfaces `EEXIST`). Treat LLM output as hostile input: parse typed
formats with typed parsers, validate paths under allowed roots, run commands only through known
execution boundaries, and preserve raw text when parsing is advisory. Genuinely optional
discovery may return empty; explicit intent must error.

### 1.7 Determinism & file hygiene

ALWAYS sort map keys before you render, compare, or persist.

```go
// RIGHT
for k := range c.Models { names = append(names, k) }
sort.Strings(names)
```

Keep one primary entity per file; split any Go file that grows past ~500 lines. Name packages
after what they own (`agent`, `sandbox`, `worktree`) — a grab-bag name (`types`, `utils`,
`helpers`, `misc`) means the code belongs in a package that already exists.

### 1.8 Domain vs infrastructure

Keep execution metadata — token usage, session IDs, log paths, timings, plan-file paths — on
`harness.RunResult` and orchestration boundaries. Domain structs (`agent.ValidationReport`,
`agent.Issue`, …) stay free of it.

## 2. Architecture map (`internal/`)

- **Pipeline** (`orchestrator/`): a typed `Step[I,O]` chain —
  `Deliberate → (human gate loop: Revise) → Execute → Validate? → Integrate?`. Validate and
  Integrate are optional (nil = skip).
- **Plan contract**: the plan markdown ALWAYS comes from Claude's plan file via `agent.ReadPlan`;
  stdout is advisory only. Integrity rules live in `internal/agent/CLAUDE.md`.
- **Harness** (`harness.ClaudeCLI`): owns the subprocess, stream-JSON parse, session IDs,
  `harness.RunResult`, and the MCP bridge.
- **Sandbox**: worker writes go through a macOS Seatbelt sandbox built on the external `leash`
  library (`leash.Execute`, wired in `internal/harness`) inside a per-run git worktree
  (`internal/worktree/`).
- **Budgets**: owned by `orchestrator/`; exhaustion is `harness.ErrBudgetExhausted`.
- **Config**: YAML via `internal/config/`; defaults embedded from its `pipeline.yaml`.
- **Agents**: Researcher / Architect / Critic / Worker run through `harness.ClaudeCLI`; Execute
  returns `ExecuteOutput` (worker session ID + target branch).
- **rune dependency**: the TUI imports rune from a local checkout (`replace` in go.mod); rune
  types stay confined to the rune-boundary files named in `internal/tui/CLAUDE.md`.
- Legacy `agent.Specification`, `ProjectPlan`, `WorkPackage`, and `internal/scheduler` are GONE.
  Do not reintroduce them.

### 2.1 Package ownership

| Package | Owns |
|---|---|
| `agent/` | role prompts (researcher/architect/worker/validation/commit/integrator), plan extraction + health checks, parsing of worker/validator/integrator output, session-artifact helpers |
| `config/` | YAML config load/validation; embedded `pipeline.yaml` defaults |
| `harness/` | `ClaudeCLI` subprocess, stream-JSON parse, `RunResult`, model-env construction, leash-backed seatbelt sandboxing, MCP bridge wiring |
| `mcp/` | `QuestionBridge` — AskUserQuestion over a unix socket |
| `orchestrator/` | phase order + gates, `Engine.Run`, agent supervision + cancel causes, budgets, events |
| `project/` | repo-root checks + init (`CheckGitRoot`, `IsInitialized`, `Init`) |
| `qarun/` | bounded test runner that prints `QA-ATTEST` |
| `testutil/` | shared test fixtures and doubles (repo root, temp HOME, plan files, transcripts) |
| `tui/` | Bubble Tea MVU front end |
| `worktree/` | per-run git worktree: `Create`, `CommitAll`, `MergeInto`, conflict listing, merge abort |

## 3. Package rules (auto-loaded sub-files)

`internal/agent/`, `internal/harness/`, `internal/orchestrator/`, and
`internal/tui/` each carry a `CLAUDE.md` with that package's binding rules, test matrix, and
pre-merge checklist. They auto-load when you edit there but are NOT re-injected after a context
compaction — if you work in one of these packages and its rules are not in context, read the file
first.

## 4. Commands

| Command | Does |
|---|---|
| `make build` | build `./bin/orqestra` (CGO off, stripped) |
| `make test` | unit + race + coverage, run through `cmd/qarun` under a hard deadline |
| `make test-integration` | `-tags integration`; needs `git`, `go build` |
| `make test-sandbox` | `-tags 'darwin integration'` on `internal/harness`; needs `sandbox-exec` (macOS) |
| `make test-fuzzy` | fuzz the stream/TUI/validation parsers in parallel (`T=<duration>`, default 3m) |
| `make test-all` | test + integration + sandbox + e2e + lint |
| `make lint` | `go vet ./...` |
| one test | `go test -race ./internal/<pkg>/ -run TestX -v` |
| run | `make run`; headless: `./bin/orqestra --prompt "…" --auto-approve --config orqestra.yaml` |

`make test-e2e` is a placeholder (no live e2e tests yet), not coverage.

## 5. Debugging headless runs via Claude CLI logs

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

## 6. Testing & verdicts

A run is GREEN, RED, or **NO-VERDICT** (hang / timeout / crash / build-fail). NO-VERDICT is a
failure — never "probably fine". `make test` runs through `cmd/qarun` under a deadline, so a hang
becomes a bounded NO-VERDICT; a real pass prints `QA-ATTEST … SUITE-COMPLETE`. ALWAYS quote a
fresh `QA-ATTEST` line before you call `make test` green; per-package `ok` lines are not
completion.

Run the narrowest package after a change; cover the invariant class, not one happy path — each
package `CLAUDE.md` carries its test matrix. Use table-driven tests for matrices; add `-race`
when touching concurrency, streaming, sandbox, harness, or orchestrator state. Synchronize tests
with channels, contexts, or fake clocks — a `time.Sleep` race is flaky by construction.

## 7. Pre-merge checklist

- [ ] Every returned error is wrapped with `%w` or dropped with `// fire-and-forget: <reason>`;
      `.Error()` only at the display boundary.
- [ ] `context.Context` is an argument; every goroutine exits via `ctx.Done()` or a joined channel.
- [ ] Fallible constructors return `(T, error)`.
- [ ] Mutually-exclusive modes are one active sub-model.
- [ ] User intent (paths, model refs, config keys) errors when missing or invalid.
- [ ] Map keys are sorted before render/compare/persist.
- [ ] One primary entity per file; files stay under ~500 lines; packages named for what they own.
- [ ] Execution metadata stays on `harness.RunResult` / orchestration boundaries, off domain structs.
- [ ] The `CLAUDE.md` checklist of every touched package consulted (agent / harness / orchestrator /
      tui).
- [ ] `-race` run for any concurrency / streaming / sandbox / harness / orchestrator change.
- [ ] `make test` reported green only with a fresh `QA-ATTEST … SUITE-COMPLETE` quoted.
