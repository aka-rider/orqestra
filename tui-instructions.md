# Bubble Tea UI conventions

Instructions for writing TUI code in this package. Read before adding or modifying anything under `pkg/ui/`.

## 1. Principles

1. **Value receivers.** Every `Init`/`Update`/`View` method takes its model by value. `Update` returns a NEW model (a modified copy), never mutates the caller.
2. **`View` is read-only.** It reads receiver fields and returns a string. No assignments, no `tea.Cmd`s, no I/O, no time/random reads.
3. **Side effects only through `tea.Cmd` → `tea.Msg`.** I/O, network, clipboard, timers, errors — all run inside a `tea.Cmd`; results come back as typed messages handled in `Update`.
4. **Always forward, always batch.** A parent forwards every message to every child it owns and combines the returned `tea.Cmd`s with `tea.Batch`. `tea.Batch` tolerates `nil`; never drop a returned `Cmd`.
5. **Explicit dependencies.** Pass styles, keymap, dimensions, and any external collaborator in via constructor parameters. No god-struct embedded into every model. No `context.Context` stored on a model.
6. **No aliasing across model boundaries.** Models own child models by value. Don't store pointers to other models. (Stdlib/3rd-party types you don't own — `*log.Logger`, `*regexp.Regexp`, `*sync.Mutex` — are fine.)

## 2. Layout

```
pkg/ui/
  keymap/         — one Bindings struct + DefaultBindings()
  styles/         — lipgloss.Style values + Styles struct
  components/<x>/ — reusable widgets (tabs, viewport, footer, ...)
  pages/<x>/      — top-level screens
```

Rules:
- `components/` may import `keymap/`, `styles/`. It must not import `pages/` or any domain package.
- `pages/` may import `components/`, `keymap/`, `styles/`, and domain packages.
- One package per component or page. Co-locate the model, its message types, its `Bindings` (if any), and its `Styles` (if any).

## 3. Model contract

```go
// Every model implements this. Note: returns the CONCRETE model type,
// not tea.Model. Concrete returns avoid interface boxing and keep type
// information at the call site.
type Model interface {
    Init() tea.Cmd
    Update(tea.Msg) (Model, tea.Cmd)
    View() string
}
```

Concrete signature in a package named `tabs`:
```go
func (m Model) Init() tea.Cmd
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd)
func (m Model) View() string
```

A parent stores `tabs.Model` (value, not `*tabs.Model`) and writes back: `m.tabs, cmd = m.tabs.Update(msg)`.

## 4. DO / DON'T

### 4.1 Value receiver; return a NEW model

```go
// GOOD
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
    switch msg := msg.(type) {
    case LoadedMsg:
        m.items = msg.Items   // assignment is to the local copy
        m.loading = false
    }
    return m, nil
}
```

```go
// BAD: pointer receiver mutates the caller's struct;
// also returns tea.Model, which boxes the concrete type.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    m.items = msg.(LoadedMsg).Items
    return m, nil
}
```

### 4.2 `View` reads, never writes

```go
// GOOD
func (m Model) View() string {
    return lipgloss.JoinVertical(lipgloss.Left, m.header.View(), m.body.View())
}
```

```go
// BAD: mutation, I/O, and command emission inside View.
func (m *Model) View() string {
    m.lastRender = time.Now()           // mutation
    data, _ := os.ReadFile("conf.txt")  // I/O
    return string(data)
}
```

### 4.3 Side effects flow through `tea.Cmd` → typed `tea.Msg`

```go
// GOOD
type LoadedMsg struct{ Content string }
type ErrMsg    struct{ Err error }

func loadFile(path string) tea.Cmd {
    return func() tea.Msg {
        b, err := os.ReadFile(path)
        if err != nil {
            return ErrMsg{Err: err}
        }
        return LoadedMsg{Content: string(b)}
    }
}

// In Update:
case LoadedMsg: m.content = msg.Content
case ErrMsg:    m.err = msg.Err
```

```go
// BAD: blocking I/O inside Update freezes the event loop;
// the error is dropped silently.
case LoadFileMsg:
    b, _ := os.ReadFile(msg.Path)
    m.content = string(b)
    return m, nil
```

### 4.4 Forward every message; always `tea.Batch`

```go
// GOOD
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
    var cmds []tea.Cmd
    var cmd  tea.Cmd

    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.width, m.height = msg.Width, msg.Height
    case tea.KeyPressMsg:
        if key.Matches(msg, m.keys.Quit) {
            return m, tea.Quit
        }
    }

    // Forward to every child. tea.Batch handles nil Cmds.
    m.tabs, cmd = m.tabs.Update(msg); cmds = append(cmds, cmd)
    m.body, cmd = m.body.Update(msg); cmds = append(cmds, cmd)
    return m, tea.Batch(cmds...)
}
```

```go
// BAD: WindowSizeMsg never reaches children; bodyCmd is lost.
case tea.WindowSizeMsg:
    m.width, m.height = msg.Width, msg.Height
    return m, nil
m.tabs, headCmd := m.tabs.Update(msg)
m.body, _       := m.body.Update(msg)  // dropped
return m, headCmd
```

Resize is a message, not a method call. Don't call `child.SetSize(w, h)` from inside `View`; let `Update` handle `tea.WindowSizeMsg`.

### 4.5 Children owned by value; never mutate a child's fields directly

```go
// GOOD
type Page struct {
    tabs tabs.Model
    body body.Model
}

// Mutate via Update, which returns a new child:
m.tabs, cmd = m.tabs.Update(SetActiveMsg(2))
```

```go
// BAD: parent reaches into child's internals.
type Page struct {
    tabs *tabs.Model
}
m.tabs.active = 2          // breaks encapsulation; also aliasing
```

Use a `*tabs.Model` field only when the child is genuinely large AND has a single owner. Document the exception in a comment.

### 4.6 Keymap lives in one package, injected via constructor

```go
// GOOD — keymap/keymap.go
type Bindings struct {
    Up, Down, Select, Back, Quit key.Binding
}
func Default() Bindings { /* key.NewBinding(...) for each */ }

// In a component:
func New(keys keymap.Bindings, ...) Model { ... }
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
    if k, ok := msg.(tea.KeyPressMsg); ok && key.Matches(k, m.keys.Up) {
        // ...
    }
    return m, nil
}
```

```go
// BAD: bindings declared inline — allocated on every keypress,
// invisible to help.Model, can't be customized by callers.
if msg.String() == "k" || msg.String() == "up" { ... }
```

Per-component keymaps (e.g. `tabs.Bindings`) are fine when the binding is component-specific; the rule is "declared once, injected", not "all in one struct".

### 4.7 No god-struct; pass explicit dependencies

```go
// BAD: every component embeds this. Hidden mutable state
// via ctx.SetValue; impossible to test in isolation;
// styles/keymap shared by pointer means mutation anywhere
// leaks everywhere.
type Common struct {
    ctx    context.Context // mutated via SetValue
    Width  int
    Height int
    Styles *Styles
    Keys   *Bindings
    Logger *log.Logger
}

type Tabs struct {
    common Common
    items  []string
}
func (t *Tabs) View() string {
    return t.common.Styles.Tab.Render(t.items[t.common.Ctx().Value("active").(int)])
}
```

```go
// GOOD: only what this component needs, by value.
type Model struct {
    items  []string
    active int
    styles tabs.Styles    // value
    keys   tabs.Bindings  // value
    width  int
}

func New(items []string, keys tabs.Bindings, styles tabs.Styles) Model {
    return Model{items: items, keys: keys, styles: styles}
}
```

Never store `context.Context` on a model. If a `Cmd` needs a context for cancellation, capture it in the closure that creates the `Cmd`, not on the model.

### 4.8 Messages: typed structs, owned by the producer's package

```go
// GOOD — defined in package tabs, exported only if other
// packages must react to it.
package tabs

type ActiveMsg   int   // emitted when active tab changes
type SetActiveMsg int  // sent by parent to switch tabs

// A Cmd that emits the message. Closure captures BY VALUE.
func setActiveCmd(i int) tea.Cmd {
    return func() tea.Msg { return SetActiveMsg(i) }
}
```

```go
// BAD: parent invents an untyped or string-keyed message
// and the child has to switch on strings.
return m, func() tea.Msg {
    return map[string]any{"event": "tab", "i": i} // !
}
```

Closures inside `Cmd`s must close over **copies**, not over the model's fields. Take the value you need as a function argument and let the closure capture that argument.

```go
// BAD: closure captures m, races with the next Update.
return m, func() tea.Msg { return ItemMsg{ID: m.cursor} }
```
```go
// GOOD: capture the value, not the model.
id := m.cursor
return m, func() tea.Msg { return ItemMsg{ID: id} }
```

## 5. Canonical page skeleton

```go
package mypage

import (
    "charm.land/bubbles/v2/key"
    tea "charm.land/bubbletea/v2"
    "charm.land/lipgloss/v2"

    "example.com/app/pkg/ui/components/body"
    "example.com/app/pkg/ui/components/tabs"
    "example.com/app/pkg/ui/keymap"
    "example.com/app/pkg/ui/styles"
)

type Model struct {
    width, height int
    keys          keymap.Bindings
    styles        styles.Styles
    tabs          tabs.Model
    body          body.Model
    err           error
}

func New(keys keymap.Bindings, st styles.Styles) Model {
    return Model{
        keys:   keys,
        styles: st,
        tabs:   tabs.New([]string{"a", "b", "c"}, tabs.BindingsFrom(keys), st.Tabs),
        body:   body.New(st.Body),
    }
}

func (m Model) Init() tea.Cmd {
    return tea.Batch(m.tabs.Init(), m.body.Init())
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
    var cmds []tea.Cmd
    var cmd  tea.Cmd

    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.width, m.height = msg.Width, msg.Height
    case tea.KeyPressMsg:
        if key.Matches(msg, m.keys.Quit) {
            return m, tea.Quit
        }
    case ErrMsg:
        m.err = msg.Err
    }

    m.tabs, cmd = m.tabs.Update(msg); cmds = append(cmds, cmd)
    m.body, cmd = m.body.Update(msg); cmds = append(cmds, cmd)
    return m, tea.Batch(cmds...)
}

func (m Model) View() string {
    if m.err != nil {
        return m.styles.Err.Render(m.err.Error())
    }
    return lipgloss.JoinVertical(lipgloss.Left, m.tabs.View(), m.body.View())
}

type ErrMsg struct{ Err error }
```

## 6. Checklist before merging UI code

- [ ] No `Update`, `View`, or `Init` method uses a pointer receiver.
- [ ] No model struct has a `context.Context` field, an embedded `Common`-style struct, or a `*Logger`/`*Backend` field reaching app-wide services.
- [ ] Every `Update` that forwards to children combines their `tea.Cmd`s with `tea.Batch`; no `Cmd` is discarded.
- [ ] `tea.WindowSizeMsg` is forwarded to every child before the parent returns.
- [ ] Every key binding used inside `Update` came from a `keymap` package via the constructor; none built inline with `key.NewBinding` per event.
- [ ] No `View` method assigns to a receiver field or returns a `tea.Cmd`.
- [ ] All message types are typed structs (or named integer/string types), defined in the package that emits them.
- [ ] `Cmd` closures capture local values, not model fields.
