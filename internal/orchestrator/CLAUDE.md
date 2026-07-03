# Orqestra — internal/orchestrator/

Extends the root `CLAUDE.md` (Prime Directive and Go fundamentals bind here). This package owns
the pipeline (phase order + gates), `Engine.Run`, agent supervision + cancel causes, budgets, and
the event stream the TUI consumes.

## Truthful events

- An integrity failure RETURNS, emits `EventError`, or gates the user — it is always visible.
- A retry emits accurate phase/agent state and preserves metadata for failed attempts.
- Plan-history failure may disable diffs, but the gate still shows the current plan and says
  diffing is unavailable.
- Worker self-validation failure is shown as exactly that: if execution continues, artifacts and
  events carry the validator status and the raw text.
- Merge conflicts surface through `MergeConflictInfo` — a completion event never stands in for
  one. The integrator gives up by default; safety is recoverability of the user's base, not a
  merge forced through.

## Budgets

Check budgets before each call and record usage after it; exhaustion is
`harness.ErrBudgetExhausted`. Report zero usage as zero — an absent number is not a spend of zero.

## Test matrix (cover the invariant class)

Phase order, retry exhaustion, gate re-entry, cancellation, question-bridge degradation,
artifact-status truth, validation verdict parsing, worktree fallback visibility, merge-conflict
surfacing. Budgets: pre-call exhaustion, post-call recording exhaustion, zero usage unreported,
status reporting, wrapped-runner continuation.

## Pre-merge checklist

- [ ] Every integrity failure returns, emits `EventError`, or gates.
- [ ] Merge conflicts surface via `MergeConflictInfo`; integrator give-up honored.
- [ ] Validation status shown truthfully even when execution continues.
- [ ] Distinct stop reasons carried via `context.WithCancelCause`.
- [ ] `-race` run for any supervision/event/budget change.
