## User Task

Add a DeliberationLoops field to PipelineSetup and expose it in the TUI.

## Goal

Describe how the deliberation loop count is configured today.

## Codebase Facts

- `internal/orchestrator/setup.go` — `PipelineSetup` holds `Research`, `Execution`, `Validation` bools and `HumanGates`. `DefaultPipelineSetup()` returns the defaults; `Validate()` requires at least one phase enabled.
- `internal/tui/screen_setup.go` — `setupModel` owns the setup overlay; `changeValue()` toggles items by cursor index; `View()` renders each item.

## Constraints Discovered

- `numSetupItems` is a constant the renderer iterates over.
- `isZeroSetup()` checks `len(s.HumanGates) == 0` to decide whether to apply defaults.

## Gotchas

- A new int field needs its own renderer; the existing `renderBool()` only handles bools.

## User Questions

- Restate: what range should the loop count allow? Relevant facts: no int field exists on `PipelineSetup` today. Options seen: 1..3 with clamping, or unbounded. Not choosing.
