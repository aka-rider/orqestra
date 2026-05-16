# Orqestra - Copilot Instructions

<instruction_file_topology>

## Instruction File Topology

- Canonical instruction file: `.github/copilot-instructions.md`.
- Symlink topology: `.github/copilot-instructions.md` <-- symlink <-- `CLAUDE.md`.
- `CLAUDE.md` must remain a symlink to `.github/copilot-instructions.md`. Edit the canonical target, not a divergent copy. Verify with `readlink CLAUDE.md` after instruction-file changes.

</instruction_file_topology>

<system_router>

## Routing Rules

Before planning or editing code, decide whether the task touches one of these domains and load the matching file first.

1. Terminal UI and Bubble Tea architecture: any edit under `internal/tui/`
   - Read `.github/tui-instructions.md`.
2. Agent execution, harnesses, sandboxing, token limits, or plan persistence: edits under `internal/agent/`, `internal/harness/`, `internal/sandbox/`, `internal/tokenlimit/`, or `internal/plan/`
   - Read `.github/agent-instructions.md`.
3. Cross-domain changes
   - Read every matching instruction file before designing the change.

</system_router>

<commands>

## Commands

- `make build` — builds `./bin/orqestra` (CGO disabled, stripped).
- `make test` — unit tests with race detector and coverage. Fast, no external deps.
- `make test-integration` — adds `-tags integration`; needs `git` and `go build` in PATH.
- `make test-sandbox` — adds `-tags 'darwin integration'`; needs `sandbox-exec` (macOS only).
- `make test-e2e` — `-tags e2e` in `internal/harness/`; requires real `claude` CLI and API access.
- `make lint` — `go vet ./...`.
- Single package/test: `go test -race ./internal/agent/ -run TestArchitect_… -v`.
- Run TUI: `make run` (or `./bin/orqestra` after `make build`).
- Headless smoke: `./bin/orqestra --prompt "..." --auto-approve --config orqestra.yaml`.
- CLI subcommands: `plan`, `validate`, `exec`, `usage`, `reset-usage`, plus internal `mcp-bridge` (invoked by Claude CLI as an MCP server, not by users).

</commands>

<repo_truth>

## Hard Architecture Facts

- Orqestra is a macOS-first Go CLI/TUI that orchestrates Claude Code through harnesses, not direct model APIs.
- The active pipeline in `internal/orchestrator/` is: Researcher -> Architect -> Critic -> human plan gate -> sandboxed Worker -> worker self-validation -> optional worktree commit/merge.
- The current plan contract is `agent.RawPlan`: raw markdown read from Claude plan files by `agent.ReadPlanFromRun`. `agent.Specification`, `agent.PlanOutput`, `agent.ProjectPlan`, and `internal/scheduler/` are legacy or secondary paths unless the task explicitly targets them.
- `internal/harness/` owns CLI invocation, Claude stream parsing, session IDs, plan-file paths, and token usage. Agent domain structs must not import harness types only to carry execution metadata.
- Worker execution must go through the macOS seatbelt sandbox in `internal/sandbox/` via `harness.SandboxCLIRunner`; repo writes should be isolated in a per-run worktree when available.
- `internal/tui/` is Bubble Tea MVU. `Update()` mutates model state and returns commands; `View()` renders only.
- Session artifacts live under `.orqestra/sessions/<run>/`; Claude session logs are copied there for diagnostics when available.
- Configuration is YAML loaded through `internal/config/`, with embedded defaults from `internal/config/pipeline.yaml`. Explicit user-supplied paths, model refs, sandbox settings, and prompt files must fail clearly when invalid.

</repo_truth>

<architectural_fault_lines>

## Architectural Fault Lines To Hedge

- Plan extraction depends on external Claude plan files and JSONL session logs. Missing session IDs, unreadable plan files, empty plans, and paths outside `~/.claude/plans/` are integrity failures, not soft warnings. Directory-scan fallback is allowed only behind the existing security gate and must be logged with enough context to debug the source of truth.
- The orchestrator is a long stateful pipeline with intentional best-effort diagnostics. Keep a hard boundary between integrity errors and optional observability: config/model resolution, sandbox setup, plan extraction, worker execution, validation verdict parsing, and merge conflict state must return, emit, or gate; session-log copy, plan-history diff, and commit-message generation may warn and continue if the user-visible state remains truthful.
- Worktree isolation currently falls back to writable-repo execution when branch detection or worktree creation fails. Changes in execution, merge, or sandbox code must either remove that fallback, gate it explicitly, or prove with tests and user-visible events that the fallback cannot hide unsafe writes.
- `internal/scheduler/` uses an opaque `spec any` payload and mutates per-agent status from goroutines. Treat it as legacy/experimental unless requested. Any new scheduler work needs typed payload boundaries or explicit adapters, race-safe status/event handling, unknown-dependency checks, cycle tests, and `go test -race` coverage.
- Config model lookup currently accepts case-insensitive aliases in some paths. Do not add new fallback resolution. Tightening this behavior requires migration-aware tests for exact refs, wrong-case refs, missing refs, and graph validator refs.
- The TUI is prone to layout desynchronization if viewport/text-area state changes during render. Viewport sizing, content sync, scroll restoration, focus changes, and input-height recalculation belong in `Update()` paths, not `View()`.
- LLM validation output is advisory. Preserve raw validation text for users, parse typed verdicts defensively, and never let parser success imply that the worker truly satisfied the plan without command or artifact evidence.

</architectural_fault_lines>

<core_principles>

## Core Principles

1. Contracts first: identify the existing package boundary and public behavior before editing. Add or update the smallest test that would fail for the behavior being changed.
2. Harness over direct API: route model work through `harness.CLIRunner` or `harness.ContinuableRunner` unless implementing the harness itself.
3. Fail closed at integrity boundaries: corrupt plans, unresolved models, invalid config, sandbox setup failures, unsafe paths, and broken worktree state must stop, return, emit an error, or ask the user.
4. Best-effort work must be named as best effort: if failure does not change correctness, log or surface the operation, resource, and error, then keep user-visible state truthful.
5. LLM output is hostile input: parse typed formats with typed parsers, validate paths under allowed roots, gate commands through known execution boundaries, and preserve raw text when parsing is advisory.
6. Domain state and execution metadata stay separate: domain types describe Orqestra concepts; token usage, timing, session IDs, plan-file paths, and logs are returned or persisted at orchestration boundaries.
7. Value semantics by default: use values unless nil has a distinct meaning, the type owns a process/resource/sync primitive, or mutation sharing is deliberate and documented by the API shape.
8. User-visible truth: the TUI and artifacts must never imply an agent, validation, merge, or sandbox step succeeded when it was skipped, degraded, or failed.
9. Small, idiomatic Go: prefer concrete structs and narrow consumer-owned interfaces; add abstraction only after two real call sites need it.

</core_principles>

<banned_patterns>

## Banned Patterns

These are review blockers. If existing code contains one near the task, do not spread it; either fix it in scope or call it out.

- Silent fallback after explicit user intent: user-supplied config paths, plan paths, model refs, prompt files, sandbox paths, and command targets must not fall back to defaults when missing or invalid.
- Log-and-continue across integrity boundaries: after an error that affects correctness, model selection, sandboxing, plan source, worker execution, merge state, or user-visible output, return or emit a failure instead of continuing.
- Swallowed file-system errors: do not treat all `os.Stat` or read errors as absence. Propagate permission, symlink, parse, and IO errors with operation and path context.
- Bare ignored errors: `_ = err` is allowed only with an adjacent `// fire-and-forget: <reason>` comment or in tests where the assertion already covers the failure path.
- Nil interface construction: factories for runners, sandboxes, stores, parsers, resolvers, and orchestrators return `(T, error)` and never use `nil, nil` to mean disabled or misconfigured.
- Direct worker execution outside the sandbox/worktree boundary: worker shells, validation continuations, and merge-producing execution must use the seatbelt runner or an explicit test double.
- Raw LLM command execution: never execute commands, file paths, JSON, YAML, or markdown emitted by an LLM without parsing and boundary validation.
- Stdout plan scraping: plans come from Claude plan files through `ReadPlanFromRun`, not from free-form stdout, except in tests explicitly covering legacy behavior.
- Infrastructure metadata on agent domain structs: no `harness.TokenUsage`, session IDs, log paths, timings, or plan file paths inside `RawPlan`, `Specification`, `PlanOutput`, `ProjectPlan`, `ValidationReport`, or `Issue`.
- TUI mutation in render paths: no `SetWidth`, `SetHeight`, `SetContent`, `GotoBottom`, focus mutation, channel reads, IO, or layout recalculation from `View()`.
- Blocking work in Bubble Tea `Update()` or `Init()`: IO, sleeps, network calls, subprocesses, and long parsing run in `tea.Cmd` or outside the TUI model.
- Mutable pointers in messages crossing goroutine boundaries: Bubble Tea messages sent from goroutines must carry copies or immutable values.
- Unsynchronized shared state in goroutines: status slices, maps, buffers, callbacks, and event collectors need ownership, channels, mutexes, or deterministic test hooks.
- Unbounded or unchecked streaming scanners: set explicit scanner buffers for Claude/MCP JSONL streams and handle `scanner.Err()`.
- Map-order-dependent output: sort keys before rendering, comparing, persisting golden output, or generating deterministic artifacts.
- `time.Sleep` as test synchronization: tests must coordinate with channels, contexts, fake clocks, hooks, or explicit process signals.
- Catch-all packages: do not create `types`, `utils`, `helpers`, or `misc`. Put concepts in the package that owns the behavior.
- Magic layout arithmetic: layout dimensions must come from measured components or named chrome constants, not unexplained offsets.

</banned_patterns>

<go_engineering>

## Go Engineering Rules

- Start from repo evidence: inspect relevant files, callers, tests, config, and build tags before choosing an approach.
- Use table-driven tests for validation matrices, config loading, parser behavior, state transitions, and dependency graphs.
- Run the narrowest relevant `go test` package after a change; broaden to `go test -race ./...` when touching concurrency, streaming, orchestration, sandboxing, or shared state.
- Wrap errors with operation and resource context: include the model ref, file path, session ID, agent ID, command, or config key that failed.
- Keep package ownership clear:
  - `internal/agent/`: agent-facing contracts, raw plan handling, plan health checks, validation parsing, work package legacy helpers, session artifact helpers.
  - `internal/harness/`: Claude CLI execution, stream parsing, MCP bridge, model environment, runner interfaces.
  - `internal/orchestrator/`: pipeline state, gates, event emission, session artifact writing, worktree handoff.
  - `internal/config/`: YAML schema, defaults, config validation, model and graph resolution.
  - `internal/sandbox/`: seatbelt profile generation, environment scrub, command wrapping, process-group cleanup.
  - `internal/tokenlimit/`: token budget storage, accounting, and runner decoration.
  - `internal/tui/`: pure rendering, MVU state transitions, screen models, user interaction.
  - `internal/plan/`: plan-history and markdown persistence adapters.
  - `internal/scheduler/`: DAG execution support; treat as separate from the active orchestrator pipeline unless explicitly wired.
- Constructors and loaders that can fail return errors. Do not panic except for impossible embedded build-time invariants.
- Prefer concrete types internally. Introduce interfaces at the consumer boundary when tests or alternate implementations need them now.
- Use `context.Context` for subprocesses, LLM calls, validators, sandboxes, and long-running orchestration steps.
- If a change degrades capability intentionally, persist or emit that fact so the TUI, run artifacts, and logs agree.
- Do not fix unrelated defects. If an adjacent defect affects the task, either cover it with the focused change or report it with evidence.

</go_engineering>

<enforcement>

## Enforcement Checklist

For every non-trivial change, enforce the relevant invariant class rather than adding one cherry-picked example.

- Config invariants: test missing, malformed, wrong-case, unknown provider/model, and conflicting values at load or resolution boundaries.
- Plan invariants: test missing session ID, missing JSONL, invalid plan path, empty plan file, truncated/bad-shaped markdown, and fallback logging behavior.
- Sandbox invariants: test missing HOME, missing sandbox-exec, invalid repo/worktree/session paths, environment scrub, proxy env validation, process-group cleanup, and writable-root policy.
- Orchestrator invariants: test phase/event ordering, gate re-entry, cancellation, retry exhaustion, artifact status, degraded diagnostics, validation verdict handling, and merge conflict surfacing.
- TUI invariants: test window resize, content-mode transitions, viewport scroll retention, key routing, mouse routing, and render height stability without mutating in `View()`.
- Concurrency invariants: run race tests for shared buffers, scheduler waves, bridge communication, stream parsing, and any goroutine-updated state.
- Parser invariants: use malformed input, extra fields, missing fields, hostile paths, large lines, and advisory raw-output preservation.
- When a broad invariant is too expensive to test fully, add the smallest deterministic boundary test and state the residual risk in the final response.

</enforcement>

<common_gotchas>

## Common Gotchas

- Nil has meaning only when the API defines it. A `*RawPlan` continuation result may mean no plan revision; a nullable runner from a factory is a bug.
- Zero `harness.TokenUsage` means the harness did not report usage. Do not use a pointer solely to represent absence.
- `bufio.Scanner` defaults are too small for streamed LLM JSON lines. Set buffers and handle scan errors.
- Loop variables must be copied before goroutines or closures that outlive the iteration.
- Optional discovery can return empty results; explicit user intent must return errors.
- Golden tests and rendered output must not depend on map order or terminal-specific width guesses.
- TUI stderr is suppressed in alt-screen mode; if users need to know, emit an event or write a session artifact.
- Claude CLI logs under `~/.claude/` are often the ground truth when headless or TUI runs look silent.

</common_gotchas>

<e2e_debugging>

## Debugging E2E And Headless Runs Via Claude CLI Logs

When an orchestrator run hangs, produces no output, or errors opaquely, inspect Claude CLI logs on disk. TUI mode intentionally silences ordinary stderr.

### Log Locations

| Path                                                   | Contents                                                                                                                                                        |
| ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `~/.claude/sessions/`                                  | Active process metadata: PID, session ID, cwd, CLI version. One JSON file per running `claude` process.                                                         |
| `~/.claude/projects/-Users-<user>-Developer-orqestra/` | Per-session JSONL conversation logs. Each line is a typed event such as `queue-operation`, `user`, `assistant`, or `last-prompt`. Filename is the session UUID. |
| `~/.claude/debug/latest`                               | Symlink to the most recent debug trace, when debug mode was enabled.                                                                                            |

### How To Read Them

1. Find recent session logs:

   ```sh
   ls -lt ~/.claude/projects/-Users-*-Developer-orqestra/*.jsonl | head -5
   ```

2. Inspect the JSONL events. Important fields:
   - `"type":"user"`: prompt sent by Orqestra's harness.
   - `"type":"assistant"`: model response; check `message.content[].text`.
   - `"isApiErrorMessage": true`: the model did not run; inspect the text for provider or connection errors.
   - `"error":"unknown"`: transport failure, not a model refusal.

3. Classify connection errors (`ConnectionRefused`, timeout, rate limit, auth) as infrastructure until code evidence says otherwise.

4. Cross-reference the session ID in the JSONL filename with `~/.claude/sessions/<pid>.json` to identify the running process.

</e2e_debugging>

<mcp_servers>

## Available MCP Servers

Use active MCP tools when they are available instead of inventing raw integrations.

- `MCP_DOCKER`

</mcp_servers>
