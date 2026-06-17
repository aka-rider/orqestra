# Orqestra — Agent Instructions

`CLAUDE.md` and `.github/copilot-instructions.md` are one file (`CLAUDE.md` is a symlink). Edit
this target; never fork a divergent copy. Verify with `readlink CLAUDE.md`.

Orqestra is a macOS-first Go CLI/TUI that orchestrates Claude Code through a harness (subprocess
+ stream parsing), not direct model APIs.

## Errors — two legal fates, no third

Every error must end in exactly one of these. There is no silent third path.

1. **Propagate** with operation + resource context (verb, the resource, `%w`):
   ```go
   return fmt.Errorf("read plan %s: %w", path, err)
   ```
2. **Drop it on purpose**, marked at the call site with a truthful reason:
   ```go
   _ = os.Remove(sock) // fire-and-forget: socket may not exist yet
   ```

If you cannot write an honest `// fire-and-forget: <reason>`, you must propagate. These are
review blockers — never write them:
```go
data, _ := json.Marshal(v)        // dropped with no reason
if err != nil { return nil }       // swallowed
if err != nil { log.Printf(...) }  // logged, then continued past a real failure
if os.IsNotExist(err) { ... }      // OK only if absence is valid; otherwise propagate
```
A dropped or downgraded error must never leave the TUI, an artifact, or a return value claiming a
step succeeded. Real examples — wrap: `internal/config/config.go:284`,
`internal/harness/claude_cli.go:70`; fire-and-forget: `internal/sandbox/sandbox.go:261`,
`internal/mcp/bridge.go:50`.

## Routing — read the domain file before editing

- `internal/tui/**` → read `.github/tui-instructions.md`.
- `internal/{agent,harness,sandbox,orchestrator,plan}/**` → read `.github/agent-instructions.md`.
- Touching both → read both.

## Commands

- `make build` — build `./orqestra` (repo root, CGO off, stripped).
- `make test` — unit + race + coverage. Fast, no external deps.
- `make test-integration` — `-tags integration`; needs `git`, `go build`.
- `make test-sandbox` — `-tags 'darwin integration'`; needs `sandbox-exec` (macOS).
- `make test-e2e` — `-tags e2e` in `internal/harness/`; needs real `claude` CLI + API.
- `make lint` — `go vet ./...`.
- One test: `go test -race ./internal/agent/ -run TestX -v`.
- Run: `make run`. Headless: `./orqestra --prompt "…" --auto-approve --config orqestra.yaml`.

## Architecture facts

- Pipeline (`internal/orchestrator/run_pipeline.go`): Research → Deliberate (Architect+Critic) →
  human plan gate (with Revise) → sandboxed Worker → worker self-validation → optional worktree
  commit/merge.
- Plan contract is `agent.RawPlan`: raw markdown read from Claude plan files by
  `agent.ReadPlanFromRun`, under `~/.claude/plans/`. Never scrape plans from stdout.
- `internal/harness/` owns the Claude subprocess (`harness.ClaudeCLI`), stream parsing, session
  IDs, plan-file paths, token usage (`harness.RunResult`), and the MCP bridge.
- Worker writes go through the seatbelt sandbox (`sandbox.New`, `internal/sandbox/`), isolated in
  a per-run git worktree when available.
- Token budgets live in `internal/orchestrator/budget.go`; exhaustion is `harness.ErrBudgetExhausted`.
- Config is YAML via `internal/config/`, defaults embedded from `internal/config/pipeline.yaml`.
- `agent.Specification`, `agent.PlanOutput`, `agent.ProjectPlan`, `WorkPackage`, and
  `internal/scheduler/` are legacy/experimental — not on the active path. Don't extend them unless
  the task names them.

## Non-obvious rules

**Integrity vs best-effort.** Fail closed at integrity boundaries — missing/empty/corrupt plans,
unresolved models, invalid config, sandbox-setup failure, unsafe paths, broken worktree, merge
conflicts: stop, return, emit `EventError`, or ask the user. Best-effort diagnostics (session-log
copy, plan-history diff, commit-message gen) may continue only if they carry a fire-and-forget
marker AND user-visible state stays truthful.

**User intent never falls back silently.** User-supplied paths, model refs, prompt files, config
keys, and command targets must error when missing or invalid — never default. Genuinely optional
discovery may return empty; explicit intent must return an error. Don't use `os.MkdirAll` for
user-initiated creation (it hides `EEXIST`) — use `os.Mkdir`.

**LLM output is hostile input.** Parse typed formats with typed parsers; validate paths under
allowed roots; run commands only through known execution boundaries; preserve raw text when
parsing is advisory. Worker self-validation text never proves success without command/artifact
evidence.

**Domain vs infrastructure.** Token usage, session IDs, log paths, timings, and plan-file paths
live on `harness.RunResult` and orchestration boundaries — never on domain structs (`RawPlan`,
`ValidationReport`, `Issue`, …).

**Constructors that can fail return `(T, error)`.** Runners, sandboxes, stores, parsers,
resolvers, and orchestrators never return `nil, nil` to mean "disabled."

**Value semantics.** Use values unless nil has a defined meaning or the type owns a resource/sync
primitive. For optional data crossing goroutines, prefer `struct{ …; Valid bool }` over a pointer;
the zero value must be meaningful.

**Make illegal states unrepresentable.** Model mutually-exclusive modes (screen modes, phases,
variants) as one active sub-model — not one struct holding every variant's fields plus tags/`hasX`
bools.

**Concurrency & streaming.** `context.Context` is a function argument, never a struct field;
signal session end via context and return `ctx.Err()`. Messages crossing goroutines carry
copies/immutable values. Shared state needs ownership, channels, or mutexes. JSONL scanners need
an explicit buffer and a `scanner.Err()` check.

**Determinism.** Sort map keys before rendering, comparing, or persisting.

**File hygiene.** One primary entity per file; no `types`/`utils`/`helpers`/`misc` packages. A Go
file over 500 lines is a smell — split by entity; don't pile onto a known offender (e.g.
`internal/tui/screen_pipeline.go`).

**Tests.** Table-driven for matrices. Run the narrowest package; add `-race` when touching
concurrency, streaming, sandbox, harness, or orchestrator state. Never use `time.Sleep` to
synchronize tests — use channels, contexts, or fake clocks. The full per-domain invariant matrix
and the CLI-log debugging guide live in `.github/agent-instructions.md`.
