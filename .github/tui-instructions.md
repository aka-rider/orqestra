# Orqestra - TUI Instructions

<tui_architecture>

## Current TUI Architecture

- `internal/tui/` is a Bubble Tea MVU application.
- `model.go` owns root state, screen routing, global key handling, layout recalculation, and orchestration channel wiring.
- Screen structs such as `PipelineScreen`, `PromptScreen`, `RunsListScreen`, and `RunDetailScreen` are value-style sub-models. They return updated copies plus `tea.Cmd` values.
- `messages.go` owns typed cross-screen messages and intents. Intents flow up to the root model; orchestration events flow down through typed messages.
- Viewport and textarea dimensions are model state. Size, content, focus, scroll position, and bounds updates belong in `Update()` paths, not render paths.

</tui_architecture>

<known_pressure_points>

## Known Pressure Points

These are current or recurring weak spots. Do not spread them; fix in scope when touching nearby code.

- `Model.Update()` still performs some direct IO, including reading an edited plan after the editor returns and loading historical run details/logs. New IO must move into `tea.Cmd` messages; nearby work should migrate existing reads when practical.
- `PipelineScreen.SyncViewports()` is called frequently, including timer ticks. Any new `SetContent()` call must preserve scroll intentionally and should be guarded by data/size changes when the viewport is user-scrollable.
- `Model.View()` currently copies `ctrlCPending` into the pipeline screen before rendering. Do not add more render-time assignments. Prefer deriving render-only props locally or updating screen state in `Update()`.
- Run detail log loading parses files synchronously when step selection changes. Future work should use async commands and typed completion messages.
- Root-level `ctrl+c`, navigation, and gate decisions are delicate. Nested screens may emit intents, but the root model owns global exits and orchestration-side effects.

</known_pressure_points>

<core_rules>

## Core Rules

- `View()` is pure rendering: no IO, no channel reads, no subprocesses, no focus mutation, no viewport mutation, no `SetWidth`, `SetHeight`, `SetContent`, `GotoTop`, or `GotoBottom`.
- `Update()` may mutate model state and return commands, but must not block on disk, network, timers, subprocesses, or long parsing. Put that work in a `tea.Cmd` and return a typed completion message.
- Messages sent from goroutines must carry immutable values or copies. Never send mutable pointers, maps, slices, buffers, or shared model references unless ownership is explicit and race-tested.
- Sub-models handle local editing and selection. They do not directly call orchestrator decision channels, cancel contexts, or external processes; they emit intents for the root model.
- Global keys are rooted. `ctrl+c`, app-level `esc` behavior, merge decisions, run navigation, and pipeline cancellation belong in `Model` routing.
- Stale or transient errors must be stored in model state until rendered or intentionally cleared. Do not rely on TUI stderr for user-visible truth.

</core_rules>

<layout_and_rendering>

## Layout And Rendering

- Handle `tea.WindowSizeMsg` by calculating all layout dimensions in `Model.recalculateLayout()` or a screen-specific update method called from `Update()`.
- Use named chrome constants or measured component dimensions. Avoid unexplained arithmetic such as `height - 8` or duplicated line-count offsets.
- Clamp dimensions before assigning them to Bubble components. Zero or tiny terminals must render the small-screen message without panics.
- Treat `viewport.SetContent()` as scroll-destructive. Capture `AtBottom()` or the previous offset before setting content and restore only when that behavior is deliberate.
- Do not render just to measure inside `Update()`. If a chrome height is stable, name it as a constant; if it is dynamic, store the measured input state before rendering.
- Mouse events must be routed through tracked bounds (`image.Rect`) before forwarding to panes so background viewports cannot consume foreground interactions.

</layout_and_rendering>

<async_commands>

## Async Command Boundaries

- File reads for run lists, run details, session logs, and edited plan files should use `tea.Cmd` functions that return typed messages.
- Opening external editors or files should be represented by commands. Failures should return messages that update visible model error state unless the action is explicitly best effort.
- Long-running orchestration remains outside the TUI model. The TUI consumes orchestrator events through commands and channels; it never mutates orchestrator state directly.
- Timer ticks may refresh elapsed time and live stream output, but they must not reset user scroll or rebuild expensive content unless the underlying data changed.

</async_commands>

<testing_enforcement>

## Testing Enforcement

When changing TUI behavior, add focused tests for the invariant class, not a single happy path.

- Render purity: call `View()` repeatedly and assert viewport sizes, offsets, focus state, and content hashes do not change.
- Layout: test small terminals, resize transitions, content-mode transitions, input-height changes, and no overlap between tracked bounds.
- Scroll stability: test user-scrolled viewports across ticks, stream updates, dashboard/help toggles, and run detail step changes.
- Routing: test root handling for `ctrl+c`, `esc`, merge conflict decisions, plan approval/comment/edit intents, and mouse bounds.
- Async IO: test command messages for success and failure, including missing files and parse errors; do not use `time.Sleep` for synchronization.

</testing_enforcement>
