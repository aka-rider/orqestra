# Orqestra — TUI Instructions

<bubble_tea_architecture>

## Bubble Tea (TUI) Architecture

The TUI uses the **Elm architecture** (Model-View-Update) via `charmbracelet/bubbletea`.

- **Model**: Immutable state struct. `Update()` returns a new model + optional `tea.Cmd`.
- **View**: Pure function rendering model → string. No side effects.
- **Update**: Handles `tea.Msg`, transitions state, returns commands for async work.

### Package Layout (`internal/tui/`)

- `tui.go` — entry point (`Run`), program setup, panic recovery
- `model.go` — main model, state routing
- `messages.go` — custom message types (centralized data structures)
- `styles.go` — shared lipgloss styles
</bubble_tea_architecture>

<tui_core_rules>

## Core Rules & Anti-Patterns (BANNED)

- **CRITICAL: `View()` Must Be 100% Pure**: NEVER call `.SetWidth()`, `.SetHeight()`, `.SetContent()`, or alter any state (like viewports/textareas) inside `View()`. Performing layout math or state mutation inside `View()` causes race conditions and cursor bugs.
- **CRITICAL: Never Block in `Update()`**: Do not perform IO, sleeps, or network calls inside `Update()`. Always delegate to `tea.Cmd`.
- **No Pointers in IO Messages**: NEVER pass structs containing mutable pointers in `tea.Msg` (via `p.Send()`) when streaming from goroutines. BubbleTea requires deep immutability. Pass copies or values to prevent concurrent map panics.
- **No Direct IO in `Init()`**: `Init()` should only return a `tea.Cmd`.
- **No Goroutine Mutations**: Never mutate the model directly from an external goroutine. Always use `program.Send(msg)`.
- **Massive Switch Statements**: Split giant switch blocks into sub-model `Update` calls routed by state enum.
</tui_core_rules>

<tui_layout_and_rendering>

## Layout, State & Rendering

- **Calculate Layouts in `Update()`**: Handle `tea.WindowSizeMsg` explicitly. Calculate sizes, clamp bounds (e.g., `max(0, width)`), and apply `.SetWidth()` to sub-models **before** passing the message down (synchronous delegation).
- **Do NOT Render to Measure**: NEVER call view-rendering methods (e.g., `renderHeader()`) inside `Update()` just to calculate `lipgloss.Height()`. This causes severe CPU exhaustion. Use predefined constants (`headerHeight`) and component properties (`textarea.Height()`).
- **No Magic Numbers**: Avoid arithmetic layout guesses (e.g., `height - 8`). Measure available layout space explicitly (`contentHeight := height - usedHeight`).
- **Viewport Scroll Reset Bug**: `viewport.SetContent()` forcefully resets `YOffset` to 0. To prevent scroll snapping, capture `atBottom := vp.AtBottom()` before calling, and restore with `vp.GotoBottom()` afterwards. Only call `SetContent()` when data or window dimensions actually change.
- **Spatial Mouse Routing**: For multi-pane UIs, track component bounding boxes (`image.Rect`). Check `msg.X` and `msg.Y` against these bounds before forwarding `tea.MouseMsg` to sub-models, preventing background logs from scrolling when interacting with foreground inputs.
</tui_layout_and_rendering>

<tui_gotchas>

## Gotchas & Crash Prevention

- **`WindowSizeMsg` on Startup**: Bubble Tea sends this immediately upon program start. Sub-models must not crash if dimensions are zero prior to this message arriving.
- **Alt Screen Panic Recovery**: If using `tea.WithAltScreen()`, panics destroy the terminal state. ALWAYS implement a panic recovery block in `Run()`:

  ```go
  defer func() {
      if r := recover(); r != nil {
          p.Kill()
          fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
      }
  }()
  ```

- **Transient Errors**: Do not let temporal errors disappear. Store them in the model state to ensure user visibility.
- **`tea.Quit` Timing**: `tea.Quit` is a command, not an instant exit. The model will receive one final `Update` tick.
- **Command Concurrency**: Use `tea.Batch` for simultaneous async tasks, and `tea.Sequence` for ordered execution.
</tui_gotchas>
