# Orqestra — TUI Instructions

<bubble_tea_architecture>
## Bubble Tea (TUI) Architecture

The TUI uses the **Elm architecture** (Model-View-Update) via `charmbracelet/bubbletea`.

- **Model**: Immutable state struct. `Update()` returns a new model + optional `tea.Cmd`.
- **View**: Pure function rendering model → string. No side effects.
- **Update**: Handles `tea.Msg`, transitions state, returns commands for async work.

### Package Layout (`internal/tui/`)

- `tui.go` — entry point (`Run`), program setup, panic recovery, `programWriter`
- `model.go` — main model, state routing
- `messages.go` — all custom message types (one file, easy to find)
- `styles.go` — shared lipgloss styles (tab, border, color definitions)
</bubble_tea_architecture>

<tui_patterns>
## Patterns & Best Practices

- **Composing models**: Sub-views are plain structs with `Update(msg) (self, tea.Cmd)` and `View() string`. Parent owns routing.
- **Parent-child communication**: Children emit custom messages (e.g., `ConfirmMsg`); parent handles them in its `Update`.
- **Async work**: Return a `tea.Cmd` (a `func() tea.Msg`). Never block in `Update`.
- **Streaming from goroutines**: Use `p.Send(msg)` from external goroutines (e.g., standard out logging loops).
- **Tab navigation**: `tab`/`shift+tab` to cycle, number keys for direct access.
- **State enum routing**: Main model uses strongly typed `State` enum; `Update` and `View` switch on it.
</tui_patterns>

<tui_anti_patterns>
## TUI Anti-Patterns (BANNED)

- **Blocking in Update**: Never do IO, sleep, or network calls inside `Update`. Use `tea.Cmd`.
- **Mutating model from goroutines**: Never. Use `p.Send()` to deliver messages.
- **Passing Pointers in Messages**: Never pass structs containing mutable pointers in `tea.Msg` (via `p.Send()`) when streaming from goroutines. BubbleTea requires deep immutability to avoid concurrent map read/write panics. Pass copies or values.
- **Massive switch statements**: Split into sub-model `Update` calls routed by state.
- **Direct IO in Init**: `Init` should only return a `tea.Cmd`, not perform IO directly.
- **Ignoring WindowSizeMsg**: Always handle it — viewport and layout depend on terminal size. Recalculate viewports on every resize msg, clamping sizes before applying.
</tui_anti_patterns>

<tui_gotchas>
## Gotchas & Crash Prevention

- **`tea.Quit` timing**: `tea.Quit` is a command, not immediate. The model will receive one more `Update` after sending it.
- **`WindowSizeMsg` on startup**: Sent automatically by bubbletea on program start. Sub-models must be ready for it before other messages. Never rely on zero-dimensions.
- **Alt screen buffer panic recovery**: `tea.WithAltScreen()` uses the alternate screen. If the program panics, the terminal stays in alt screen — **always use panic recovery with `p.Kill()` in `Run()`**.
```go
defer func() {
    if r := recover(); r != nil {
        p.Kill()
        fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
    }
}()
```
- **`Batch` vs `Sequence`**: `tea.Batch` runs commands concurrently; `tea.Sequence` runs them in order. Use Batch for independent work.
- **Transient errors**: Do not let transient errors disappear from the UI. Store the error in model state and keep it visible.
</tui_gotchas>
