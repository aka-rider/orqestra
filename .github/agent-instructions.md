# Orqestra - Agent, Sandbox, Harness, And Pipeline Instructions

[How to build/test/run](../Makefile)

<current_architecture>

## Current Architecture Facts

- The active orchestration path lives in `internal/orchestrator/`, not `internal/scheduler/`.
- The active pipeline is: Researcher -> Architect <-> Critic <-> human plan gate -> sandboxed Worker -> worker self-validation -> optional worktree commit/merge.
- Researcher, Architect, and Critic are in `internal/agent/` and run through `harness.CLIRunner` or `harness.ContinuableRunner`.
- The active plan contract is `agent.RawPlan`: raw markdown read from Claude plan files using `agent.ReadPlanFromRun`.
- `agent.Specification`, `agent.PlanOutput`, `agent.ProjectPlan`, `WorkPackage`, and markdown artifact frontmatter are legacy or secondary paths unless the task explicitly targets legacy plan loading, `internal/plan/`, or `internal/scheduler/`.
- `internal/harness/` owns Claude CLI invocation, stream parsing, MCP bridge wiring, session IDs, plan-file paths, and token usage.
- `internal/sandbox/` owns macOS seatbelt profile generation, environment scrubbing, path validation, command wrapping, and process-group cleanup.
- `internal/tokenlimit/` owns budget storage and runner decoration. Budget exhaustion is returned as `tokenlimit.ErrBudgetExhausted`.

</current_architecture>

<routing_and_ownership>

## Package Ownership

- `internal/agent/`: raw plan handling, researcher/architect/critic wrappers, plan health checks, validation-output parsing, legacy work-package helpers, session artifact helpers.
- `internal/harness/`: CLI runner interfaces, Claude CLI subprocesses, stream JSON parsing, MCP AskUserQuestion bridge, model environment construction, session-log parsing.
- `internal/orchestrator/`: phase ordering, retries, event emission, gates, session artifact writes, question bridge lifecycle, worker handoff, validation parsing, worktree commit/merge flow.
- `internal/sandbox/`: seatbelt config validation, SBPL profile building, env policy, sandbox-exec wrapping, cancellation cleanup.
- `internal/plan/`: markdown/spec persistence adapters, artifact frontmatter utilities, and the plan-history git micro-repo.
- `internal/tokenlimit/`: model budget checks, usage recording, status reporting, and `LimitedRunner` wrappers.
- `internal/scheduler/`: experimental DAG support. Treat as separate from the active pipeline unless explicitly wiring it.

</routing_and_ownership>

<plan_integrity>

## Plan And Artifact Integrity

- Plans come from Claude plan files, not stdout. Use `ReadPlanFromRun` and preserve its security boundary under `~/.claude/plans/`.
- Missing session IDs, missing JSONL logs, unsafe plan paths, unreadable plan files, and empty plan files are integrity failures. Return errors with session ID and path context.
- Directory-scan fallback is allowed only as an explicit fallback after JSONL plan-file extraction fails, and it must log enough context to debug why the primary source failed.
- `CheckPlanHealth` warnings are advisory. They may be shown to the user, but they do not prove a plan is correct.
- LLM validation text is advisory. Preserve raw output, parse marker lines defensively, and do not convert parser success into proof that work passed without command or artifact evidence.
- Legacy `Specification`/frontmatter paths must validate structured input with typed parsers and hash checks. Do not parse structured artifacts with string slicing when a parser exists.

</plan_integrity>

<sandbox_and_execution>

## Sandbox And Execution Rules

- Worker execution, worker validation continuations, and merge-producing work must go through `harness.SandboxCLIRunner` or a test double.
- `sandbox.New` failures are fatal for sandboxed execution: missing HOME, missing `sandbox-exec`, invalid repo/worktree/session paths, invalid proxy env, and profile build failures must not fall back silently.
- Worktree isolation is preferred for repo writes. If worktree creation or branch detection fails, the fallback to writable repo execution must be explicit, tested, and user-visible.
- `RepoWritable` is a high-risk switch. It must be justified by the execution mode, and tests must cover read-only repo plus writable worktree behavior when worktree mode is involved.
- Do not execute commands, paths, JSON, YAML, or markdown emitted by an LLM without parsing and boundary validation.
- Process cancellation must kill the process group. Keep `Setpgid` and negative-PID kill behavior covered by tests when touching sandbox or harness subprocess code.

</sandbox_and_execution>

<harness_streaming>

## Harness And Streaming Rules

- Claude stream parsing must use bounded scanner buffers large enough for JSONL events and must check `scanner.Err()`.
- Non-JSON stream lines may be displayed and logged for diagnostics, but result, session ID, usage, and plan-file path must come from typed parsed events.
- `harness.RunResult` may carry execution metadata (`Usage`, `SessionID`, `PlanFilePath`). Agent domain structs must not grow those fields.
- CLI runner factories that can fail return `(runner, error)`. Never use a nil runner to mean disabled or misconfigured.
- MCP bridge failures must be classified: startup failure affects question support; malformed model tool calls return MCP errors; bridge IO errors should include socket and operation context.

</harness_streaming>

<orchestrator_boundaries>

## Orchestrator Boundaries

- Integrity failures return, emit `EventError`, or gate the user. Best-effort diagnostics may warn and continue only when user-visible state remains truthful.
- Retries must emit accurate phase/agent state and preserve useful metadata for failed attempts.
- Plan-history failures may disable diffs, but the gate must still show the current plan and state that diffing is unavailable if that matters to the user.
- Worker self-validation failure cannot be presented as success. If execution continues, artifacts and events must show the validator status and raw validation text.
- Merge conflict state must be surfaced through `EventMergeConflict`; do not hide conflicts behind a successful completion event.

</orchestrator_boundaries>

<legacy_and_scheduler>

## Legacy And Scheduler Rules

- `internal/scheduler/` currently passes `spec any` and mutates status from goroutines. Any new scheduler work needs typed payload boundaries or adapters, race-safe status/event handling, unknown-dependency checks, cycle tests, and `go test -race` coverage.
- Legacy project-plan dependency helpers must reject empty packages, duplicate IDs, unknown dependencies, self-dependencies, and cycles as a class.
- Do not resurrect removed concepts such as `PlanValidator`, `agent.Gate`, `GatewayResult`, `internal/agent/pm.go`, or `stripCodeFences` unless the task explicitly reintroduces them with tests and migration notes.

</legacy_and_scheduler>

<testing_enforcement>

## Testing Enforcement

Run the narrowest relevant package test after changes; broaden to race tests when touching streaming, goroutines, scheduler, sandbox, harness, or orchestrator state.

- Plan extraction: missing session ID, missing JSONL, invalid plan path, path outside `~/.claude/plans/`, empty plan, large/truncated markdown, and fallback logging.
- Harness streaming: malformed JSONL, large lines, scanner errors, result error events, usage extraction, session ID extraction, and plan-file path extraction for both initial runs and continuations.
- Sandbox: missing HOME, missing sandbox-exec, invalid symlinks, env scrub, proxy env validation, process-group cleanup, repo read-only mode, and writable worktree mode.
- Orchestrator: phase ordering, retry exhaustion, gate re-entry, cancellation, question bridge degradation, artifact status truth, validation verdict parsing, worktree fallback visibility, and merge conflict surfacing.
- Token limits: pre-call budget exhaustion, post-call recording exhaustion, zero usage meaning unreported, status reporting, and wrapped runner behavior for continuations.
- Scheduler or dependency graphs: unknown dependencies, duplicate roles/IDs, cycles, parallel wave ordering, cancellation, and race safety.

</testing_enforcement>
