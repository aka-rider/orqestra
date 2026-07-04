# Orqestra — internal/harness/

Extends the root `CLAUDE.md` (Prime Directive and Go fundamentals bind here). This package owns
the `ClaudeCLI` subprocess, the stream-JSON (NDJSON) parse, session IDs, `RunResult`, model-env
construction, leash-backed seatbelt sandboxing, and MCP-bridge wiring.

## Streaming

ALWAYS give a stream scanner a bounded buffer and check its error:

```go
scanner.Buffer(make([]byte, initialScanBufferBytes), maxJSONLLineBytes)
// … scan loop …
if err := scanner.Err(); err != nil { return rawBuf.String(), err }
```

Result, session ID, usage, and plan-file path come from typed parsed events. Log non-JSON lines
for diagnostics; only typed events drive control flow.

## Execution goes through the sandbox

- ALWAYS run worker execution, validation continuations, and merge-producing work through `Run`
  with `spec.Sandbox.RepoPath` set (or a test double). A raw shell is not an execution boundary.
- A sandboxed setup failure is fatal: a bad grant, missing `sandbox-exec`, or a `leash.Execute`
  setup error → RETURN the wrapped error. There is no fallback to unsandboxed execution for a spec
  that requested sandboxing.
- Prefer worktree isolation for repo writes. When worktree creation or branch detection fails,
  fall back to writable-repo execution only through an explicit, tested, user-visible path.
- `SandboxConfig.Writable` is high-risk: justify it by execution mode and test read-only repo +
  writable worktree in the same sandbox instance (the real shape production constructs).
- `Network: true` is hardcoded on every sandboxed `leash.Leash{}` built by `startSandboxed` — every
  role needs Claude/Anthropic API access, so this is never a tunable a caller can override.

## Cancellation kills the process group

Both execution paths guarantee a group-kill on cancel/timeout, but by different mechanisms —
document which one a change actually touches:

- **Direct execution** (`startDirect`, `SandboxConfig{}`'s zero value): owns this itself.
  `Setpgid` starts the child in its own process group; `cmd.Cancel` SIGKILLs the negative-PID group
  on `ctx.Done()`; `cmd.WaitDelay` bounds `cmd.Wait()` so a still-dying orphan can't wedge it.

  ```go
  cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
  // on cancel/timeout:
  _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup
  ```

- **Sandboxed execution** (`startSandboxed`): the process-group kill is entirely
  `leash.Execute`'s internal responsibility — do NOT re-implement it, and do NOT layer a hand-rolled
  timer or a side-channel completion signal on top of it either. Two earlier drafts of this
  package's leash integration tried exactly that (a `time.After`-based grace period, then a plain
  indefinite channel receive competing with `ctx`) and both were rejected: neither is derived from
  `ctx`, so neither can prove it doesn't race a concurrent write to the goroutine's own result
  variable. The actual design: `startSandboxed`'s `wait()` follows this exact codebase's own
  documented discipline for `AgentSupervisor` ("the only stop signal is context.Context" —
  `internal/orchestrator/agent_supervisor.go`) one layer further in — `execCtx, cancelExec :=
  context.WithCancelCause(ctx)`, with the goroutine running `leash.Execute` calling
  `cancelExec(errLeashDone)` exactly once, only after its result has been fully written. `wait()`
  blocks on `execCtx.Done()` alone and uses `context.Cause(execCtx)` to distinguish "the goroutine
  finished, safe to read its result" from "`ctx` itself was canceled/timed out and propagated down
  automatically, the result may still be concurrently written" — returning `ctx.Err()` in the
  latter case instead of racily reading the result. `leash.Execute`'s own `sandbox.Run` already
  SIGKILLs the whole process group on `ctx.Done()`, which is what actually resolves a
  lingering-grandchild-holds-stdout-open scenario, bounded by the role's real `Timeout` (attached
  upstream in `AgentSupervisor.Run`) — not an invented constant.

## Boundaries

- `RunResult` carries execution metadata (`Usage`/`SessionID`/`PlanFilePath`); domain structs in
  `internal/agent/` stay free of it.
- Runner factories that can fail return `(runner, error)` — a disabled or misconfigured runner is
  an error, not a nil runner.
- Budget exhaustion is `ErrBudgetExhausted`; callers detect it with `errors.Is`.
- Classify MCP-bridge failures: a startup failure degrades question support; a malformed model
  tool-call returns an MCP error; a bridge IO error includes socket + operation context.

## Test matrix (cover the invariant class)

Malformed JSONL, oversized lines, scanner errors, result error events, usage + session ID +
plan-path extraction (initial and continuation runs). Sandboxed execution: missing `HOME`,
read-only-repo + writable-worktree in one sandbox instance, a sandboxed setup failure propagating
as a wrapped (never silently swallowed) error, process-group cleanup for BOTH the direct and
sandboxed branches, a `-race`-covered test proving `wait()` never reads the sandboxed goroutine's
result when `execCtx` was canceled by `ctx` propagation rather than by the goroutine's own
`cancelExec` call, and the ported INV-ROLE-WORKER capability/containment test (real subprocess, no
fakes). Path resolution, env-scrub layering, and SBPL profile construction are leash's own concern
now — not re-tested here; leash's own test suite covers them.

## Pre-merge checklist

- [ ] Every scanner has a bounded `Buffer(...)` and a `scanner.Err()` check.
- [ ] Control flow driven by typed events only; non-JSON lines are diagnostics.
- [ ] Fallible runner factories return `(runner, error)`.
- [ ] Worker/merge-producing writes go through a sandboxed spec (`spec.Sandbox.RepoPath` set); a
      leash setup failure returns and never silently falls back to unsandboxed execution.
- [ ] `startDirect`'s `Setpgid`/`Cancel`/`WaitDelay` are untouched by any sandboxed-path change.
- [ ] No timer-based or side-channel logic is added to the sandboxed path's wait/cancellation
      handling; `cancelExec` has exactly one intentional caller, matching `AgentSupervisor`'s own
      documented `cancelCause` discipline.
- [ ] `-race` run — this package is concurrency + streaming throughout.
