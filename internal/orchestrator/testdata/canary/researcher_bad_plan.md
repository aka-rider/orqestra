## Goal

Add a `DeliberationLoops` field to `PipelineSetup` (range 1..3, default 1) and expose it in the TUI setup panel so users can adjust it with `[←→]` like the existing bool/gate toggles.

## Codebase Facts

- `internal/orchestrator/setup.go` — `PipelineSetup` struct with `Research`, `Execution`, `Validation` bools + `HumanGates`. `DefaultPipelineSetup()` returns `{Research:true, Execution:true, Validation:true, GateAfterDeliberation}`. `Validate()` checks at least one phase enabled. `isZeroSetup()` checks if struct is zero value (no fields set by caller).
- `internal/tui/screen_setup.go` — `setupModel` owns the setup overlay. Items: 0=Research, 1=Execution, 2=Validation, 3=GateAfterDeliberation, 4=GateAfterResearch. `changeValue()` handles toggling by cursor position. `View()` renders bools via `renderBool()` and gates as checkboxes.
- Tests: `internal/orchestrator/setup_test.go` and `internal/tui/screen_setup_test.go`.

## Constraints Discovered

- Shell writes blocked by sandbox — only the Write tool can write files.
- Codebase follows strict value semantics: no silent fallbacks.
- `isZeroSetup()` checks `len(s.HumanGates) == 0` because HumanGateSet is a slice.
- `changeValue()` has a `default:` branch for gates — new cursor case must go *before* `default:`.

## Gotchas

- `isZeroSetup()` must include `s.DeliberationLoops == 0` to avoid treating partially-set setups as zero.
- Deliberation loops renderer needs its own format: `◁ N ▷` with clamping at 1 and 3.
- `numSetupItems` constant must be updated from 5 to 6.
- `changeValue()` switch needs a new case for the deliberation loops cursor index.
- `View()` needs a new renderer for the deliberation loops item between the bools and the "Human Review:" section.

## Plan

### File 1: `internal/orchestrator/setup.go`

1. Add `DeliberationLoops int` field to `PipelineSetup` struct.
2. Set `DeliberationLoops: 1` in `DefaultPipelineSetup()`.
3. Add validation in `Validate()`: `if p.DeliberationLoops < 1 || p.DeliberationLoops > 3`.
4. Update `isZeroSetup()` to include `s.DeliberationLoops == 0`.

### File 2: `internal/tui/screen_setup.go`

1. Add `setupItemDeliberation = 5` constant and increment `numSetupItems` to 6.
2. In `changeValue()`, add a case for `setupItemDeliberation` that increments/decrements `s.setup.DeliberationLoops` with clamping at 1..3.
3. In `View()`, add a new `renderInt` helper (or inline rendering) between validation and "Human Review:" section that shows `DeliberationLoops: ◁ N ▷`.
4. Update the hint text to mention `[←→]` (already present).

### File 3: Tests

- Update `internal/orchestrator/setup_test.go` — add test for `DeliberationLoops` validation.
- Update `internal/tui/screen_setup_test.go` — add test for deliberation loops increment/decrement.