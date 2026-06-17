# Orqestra — Agent / Sandbox / Harness / Pipeline Instructions

Routed companion to `.github/copilot-instructions.md` — read that first; it owns the error
two-way-door, the integrity-vs-best-effort rule, and the universal Go rules. This file is the deep
dive for `internal/{agent,harness,sandbox,orchestrator,plan}/`. Build/test: see `../Makefile`.

## Architecture facts

- Active orchestration is `internal/orchestrator/`, not `internal/scheduler/`.
- Pipeline: Research → Deliberate (Architect ↔ Critic) → human plan gate (with Revise) → sandboxed
  Worker → worker self-validation → optional worktree commit/merge. Phase order lives in
  `internal/orchestrator/run_pipeline.go`.
- Researcher/Architect/Critic are in `internal/agent/` and run through the Claude harness
  (`harness.ClaudeCLI`).
- Plan contract is `agent.RawPlan`: raw markdown from Claude plan files via `agent.ReadPlanFromRun`.
- `agent.Specification`, `agent.PlanOutput`, `agent.ProjectPlan`, `WorkPackage`, and markdown
  frontmatter are legacy/secondary — touch only when the task targets legacy plan loading,
  `internal/plan/`, or `internal/scheduler/`.
- Token budgets: checks/recording in `internal/orchestrator/budget.go`; exhaustion is
  `harness.ErrBudgetExhausted`.

## Package ownership

- `internal/agent/`: raw plan handling, researcher/architect/critic wrappers, `CheckPlanHealth`,
  validation-output parsing, legacy work-package helpers, session-artifact helpers.
- `internal/harness/`: Claude CLI subprocess (`harness.ClaudeCLI`), stream JSON parsing, MCP
  AskUserQuestion bridge, model-environment construction, session-log parsing, `harness.RunResult`.
- `internal/orchestrator/`: phase ordering, retries, event emission, gates, budget checks, session
  artifact writes, question-bridge lifecycle, worker handoff, validation parsing, worktree
  commit/merge.
- `internal/sandbox/`: seatbelt config validation, SBPL profile building, env scrub, sandbox-exec
  wrapping, process-group cleanup.
- `internal/plan/`: markdown/spec persistence adapters, frontmatter utilities, plan-history git
  micro-repo.
- `internal/scheduler/`: experimental DAG support — separate from the active pipeline.

## Plan & artifact integrity

- Plans come from Claude plan files, not stdout. Use `ReadPlanFromRun`; preserve its boundary under
  `~/.claude/plans/`.
- Missing session IDs, missing JSONL logs, unsafe plan paths, unreadable or empty plan files are
  integrity failures — return errors with session ID and path context.
- Directory-scan fallback is allowed only after JSONL extraction fails, and must log enough context
  to debug why the primary source failed.
- `CheckPlanHealth` warnings are advisory: show them, but they don't prove a plan is correct.
- Validation text is advisory: preserve raw output, parse marker lines defensively, never convert
  parser success into proof that work passed without command/artifact evidence.
- Legacy `Specification`/frontmatter paths validate structured input with typed parsers and hash
  checks — don't string-slice a structured artifact when a parser exists.

## Sandbox & execution

- Worker execution, validation continuations, and merge-producing work go through the seatbelt
  sandbox (`internal/sandbox/`) or a test double — never a raw shell.
- `sandbox.New` failures are fatal for sandboxed execution: missing HOME, missing `sandbox-exec`,
  invalid repo/worktree/session paths, invalid proxy env, profile-build failures must not fall back
  silently.
- Worktree isolation is preferred for repo writes. If worktree creation or branch detection fails,
  the fallback to writable-repo execution must be explicit, tested, and user-visible.
- `RepoWritable` is high-risk: justify it by execution mode; test read-only repo + writable worktree
  when worktree mode is involved.
- Process cancellation must kill the process group — keep `Setpgid` + negative-PID kill covered by
  tests when touching sandbox/harness subprocess code.

## Harness & streaming

- Claude stream parsing uses bounded scanner buffers large enough for JSONL events and checks
  `scanner.Err()`.
- Non-JSON stream lines may be displayed/logged for diagnostics, but result, session ID, usage, and
  plan-file path come from typed parsed events.
- `harness.RunResult` carries execution metadata (`Usage`, `SessionID`, `PlanFilePath`); agent
  domain structs must not grow those fields.
- Runner factories that can fail return `(runner, error)` — never a nil runner to mean
  disabled/misconfigured.
- MCP bridge failures are classified: startup failure degrades question support; malformed model
  tool calls return MCP errors; bridge IO errors include socket + operation context.

## Orchestrator boundaries

- Integrity failures return, emit `EventError`, or gate the user. Best-effort diagnostics may warn
  and continue only when user-visible state stays truthful (the error two-way-door applies here).
- Retries emit accurate phase/agent state and preserve metadata for failed attempts.
- Plan-history failures may disable diffs, but the gate must still show the current plan and say
  diffing is unavailable when that matters.
- Worker self-validation failure cannot be presented as success: if execution continues, artifacts
  and events must show validator status and raw text.
- Merge conflicts surface through `EventMergeConflict` — never hidden behind a completion event.

## Legacy & scheduler

- `internal/scheduler/` passes `spec any` and mutates status from goroutines. New scheduler work
  needs typed payload boundaries or adapters, race-safe status/events, unknown-dependency checks,
  cycle tests, and `go test -race` coverage.
- Legacy project-plan dependency helpers reject empty packages, duplicate IDs, unknown deps,
  self-deps, and cycles as a class.

## Testing enforcement

Run the narrowest package after changes; add `-race` when touching streaming, goroutines,
scheduler, sandbox, harness, or orchestrator state.

- Plan extraction: missing session ID, missing JSONL, invalid plan path, path outside
  `~/.claude/plans/`, empty plan, large/truncated markdown, fallback logging.
- Harness streaming: malformed JSONL, large lines, scanner errors, result error events, usage +
  session ID + plan-file path extraction for initial runs and continuations.
- Sandbox: missing HOME, missing sandbox-exec, invalid symlinks, env scrub, proxy env validation,
  process-group cleanup, repo read-only mode, writable worktree mode.
- Orchestrator: phase ordering, retry exhaustion, gate re-entry, cancellation, question-bridge
  degradation, artifact-status truth, validation verdict parsing, worktree fallback visibility,
  merge-conflict surfacing.
- Budgets: pre-call exhaustion, post-call recording exhaustion, zero usage meaning unreported,
  status reporting, wrapped-runner behavior for continuations.
- Scheduler / dependency graphs: unknown deps, duplicate roles/IDs, cycles, parallel wave ordering,
  cancellation, race safety.

## Debugging headless / TUI runs via Claude CLI logs

TUI mode silences ordinary stderr; Claude's on-disk logs are often the ground truth when a run
hangs or errors opaquely.

| Path | Contents |
| --- | --- |
| `~/.claude/sessions/` | Active process metadata: PID, session ID, cwd, CLI version (one JSON per running `claude`). |
| `~/.claude/projects/-Users-<user>-Developer-orqestra/` | Per-session JSONL conversation logs; filename is the session UUID. |
| `~/.claude/debug/latest` | Symlink to the most recent debug trace, when debug mode was on. |

- `ls -lt ~/.claude/projects/-Users-*-Developer-orqestra/*.jsonl | head -5` finds recent sessions.
- JSONL event fields: `"type":"user"` (prompt sent by the harness), `"type":"assistant"` (response;
  check `message.content[].text`), `"isApiErrorMessage":true` (model didn't run — read the text for
  provider/connection errors), `"error":"unknown"` (transport failure, not a refusal).
- Classify `ConnectionRefused` / timeout / rate-limit / auth as infrastructure until code evidence
  says otherwise. Cross-reference the session UUID with `~/.claude/sessions/<pid>.json`.
