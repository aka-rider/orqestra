# Orqestra — internal/agent/

Extends the root `CLAUDE.md` (Prime Directive and Go fundamentals bind here). This package owns
role prompts (researcher, architect + revision, worker, validation, commit, integrator,
continuation), plan extraction and health checks, parsing of worker/validator/integrator output,
and session-artifact helpers. Execution metadata (usage, session IDs, log paths) belongs on
`harness.RunResult` — keep it off the domain structs here (`ValidationReport`, `Issue`, …).

## Plan integrity

- ALWAYS read the plan from Claude's plan file via `ReadPlan`; treat stdout as advisory only.
- Source order is typed-first: the plan-file path reported by the run stream, then the session
  JSONL, and only after both fail a directory scan of `~/.claude/plans/` — and that fallback logs
  why the primary source failed.
- Fail closed on integrity: a missing session ID, a missing JSONL log, a plan path outside
  `~/.claude/plans/`, or an empty plan file RETURNS an error carrying the session ID and the path.

```go
// RIGHT — fail closed on an empty plan
if strings.TrimSpace(plan) == "" {
    return "", fmt.Errorf("read plan for session %s: empty plan file %s", sessionID, path)
}
// WRONG — reconstructing a plan from stream text
plan := lastAssistantMessage
```

## Parsed model output is advisory

- `CheckPlanHealth` warnings are advisory — show them; they do NOT prove a plan is correct.
- Worker self-validation text is advisory: preserve the raw output, parse marker lines
  defensively (`ParseValidationOutput`), and require command or artifact evidence before
  reporting work as passed — parser success proves only that parsing worked.
- The integrator gives up by default (`ParseIntegratorGiveUp`); safety is recoverability of the
  user's base, not a merge forced through.

## Test matrix (cover the invariant class)

Missing session ID, missing JSONL, invalid plan path, path outside `~/.claude/plans/`, empty
plan, truncated markdown, fallback logging.

## Pre-merge checklist

- [ ] Plans read via `ReadPlan`; stdout never used as a plan source.
- [ ] Integrity failures return errors carrying session ID + path.
- [ ] Raw model output preserved wherever parsing is advisory.
- [ ] Parser success never reported as proof that work passed.
