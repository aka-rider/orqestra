# Plan

## Goal

Add a DeliberationLoops control to the setup TUI.

## Context

- `internal/orchestrator/setup.go` defines `PipelineSetup`; `Validate()` enforces invariants.

## Constraints

- Do not change existing gate semantics.

## Risks

- None found after checking: `setup.go` has no integer fields today.

## Work Packages

### 1. Add the field

**Steps:**
1. Add `DeliberationLoops int` to `PipelineSetup` in `internal/orchestrator/setup.go`.

**Done when:**
- `go build ./...` exits 0.

## Verification

Run `make test`.

## Assumptions

- Range is 1..3 with clamping.

## Gotchas

- `isZeroSetup()` must account for the new field.
