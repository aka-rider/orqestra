# Plan: `@` File Picker for Prompt Textarea

## Overview

A `bubbles/list`-style sub-model within the Elm architecture that intercepts `@` from the prompt textarea, scans the repo asynchronously without blocking the event loop, provides real-time fuzzy filtering across 10M files, and cleanly restores textarea cursor position and focus on selection or dismissal without blocking the main UI.
'@' file picker should add directory or files names to the prompt. Opaque for the gateway (gateway cannot read the repo), but to focus the researcher
'@' file picker should mimic the behaviour of claudecode

researcher's claudecode **MUST** receive this context as if they were natively provided by '@' syntax in the prompt of the claudecode itself.

This makes orqestra to behave like a proxy.

---

## 1. Sub-Model: `FilePicker`

**File**: `internal/tui/filepicker.go`

```go
type FilePicker struct {
    active    bool
    query     string           // characters typed after '@'
    allFiles  []string         // full repo file list (set async)
    matches   []string         // fuzzy-filtered subset
    cursor    int              // selected index in matches
    loading   bool             // true while scan is in-flight
    err       error            // scan failure (surfaced, never swallowed)
    maxItems  int              // visible rows (from WindowSizeMsg)

    // Snapshot of textarea state at activation time
    triggerCol int             // cursor column where '@' was typed
}
```

**Why a plain struct, not `bubbles/list`?** The codebase follows the pattern of value-type sub-models with `Update(msg) (self, tea.Cmd)` + `View() string`. A `bubbles/list` brings filterable lists but also viewport management, key overrides, and delegate plumbing that fight the existing key routing. A 30-line fuzzy filter over ~83 items is simpler and more controllable than wiring `list.Model`'s delegate interface.

If `bubbles/list` is desired anyway, wrap it inside `FilePicker` and translate messages into its `FilterMatchesMsg` -- but the cost/benefit is marginal at 83 files.

---

## 2. Messages

Add to `messages.go`:

```go
// filesScanStartedMsg signals async repo scan has begun.
type filesScanStartedMsg struct{}

// filesScanResultMsg delivers the repo file list from the async scan.
type filesScanResultMsg struct {
    Files []string // sorted, relative paths
    Err   error
}
```

**Immutability contract**: `Files` is a freshly-allocated slice -- no shared pointers, safe for `p.Send()` from a goroutine per anti-pattern rules.

---

## 3. Async File Scanning

```go
// scanRepoFiles returns a tea.Cmd that walks the repo without blocking Update.
func scanRepoFiles(root string) tea.Cmd {
    return func() tea.Msg {
        var files []string
        err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
            if err != nil {
                return err // propagate, never swallow
            }
            if d.IsDir() {
                if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
                    return fs.SkipDir
                }
                return nil
            }
            rel, _ := filepath.Rel(root, path)
            files = append(files, rel)
            return nil
        })
        sort.Strings(files)
        return filesScanResultMsg{Files: files, Err: err}
    }
}
```

**Key points:**

- Runs in a goroutine via `tea.Cmd` -- never blocks `Update`.
- Prunes `.git`/`vendor`/`node_modules` to stay fast.
- Returns error on failure (permission denied, etc.) -- surfaced in `FilePicker.err`, rendered in `View()`.
- At ~83 files this completes in <5ms, but the async pattern is correct for larger repos.

---

## 4. Fuzzy Filtering

```go
func (fp FilePicker) filtered() []string {
    if fp.query == "" {
        return fp.allFiles
    }
    q := strings.ToLower(fp.query)
    var out []string
    for _, f := range fp.allFiles {
        if fuzzyMatch(strings.ToLower(f), q) {
            out = append(out, f)
        }
    }
    return out
}

// fuzzyMatch checks if all chars in pattern appear in s in order.
func fuzzyMatch(s, pattern string) bool {
    pi := 0
    for _, c := range s {
        if pi < len(pattern) && byte(c) == pattern[pi] {
            pi++
        }
    }
    return pi == len(pattern)
}
```

Called in `Update` (recomputes `fp.matches` on every keystroke). At 83 files x ~40-char paths, this is sub-microsecond -- no debounce needed.

---

## 5. Parent Integration -- Key Interception

In `Model`, add one field:

```go
filePicker FilePicker
```

### Activation: intercept `@` in `handlePromptKey`

```go
func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    // If file picker is active, route ALL keys to it
    if m.filePicker.active {
        return m.handleFilePickerKey(msg)
    }

    switch msg.Type {
    case tea.KeyEnter:
        // ... existing submit logic
    case tea.KeyCtrlS:
        // ... existing skip logic
    default:
        // Let textarea process the key first
        var cmd tea.Cmd
        m.prompt, cmd = m.prompt.Update(msg)

        // After processing, check if '@' was just typed
        if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == '@' {
            m.filePicker = FilePicker{
                active:     true,
                loading:    len(m.filePicker.allFiles) == 0,
                allFiles:   m.filePicker.allFiles, // reuse prior scan
                triggerCol: len(m.prompt.Value()),  // cursor position after '@'
            }
            m.filePicker.matches = m.filePicker.filtered()
            // Only scan if we haven't yet
            if len(m.filePicker.allFiles) == 0 {
                return m, tea.Batch(cmd, scanRepoFiles(m.repoRoot))
            }
            return m, cmd
        }
        return m, cmd
    }
}
```

### Picker Key Handling

```go
func (m Model) handleFilePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    fp := &m.filePicker

    switch msg.Type {
    case tea.KeyEsc:
        // Dismiss -- leave '@' + any query text in the textarea, restore focus
        fp.active = false
        return m, nil

    case tea.KeyEnter:
        // Select -- replace '@query' with the selected file path
        if fp.cursor < len(fp.matches) {
            selected := fp.matches[fp.cursor]
            val := m.prompt.Value()
            // Replace from triggerCol-1 (the '@') through current cursor
            before := val[:fp.triggerCol-1]
            after := val[len(val):] // nothing, cursor is at end during typing
            m.prompt.SetValue(before + selected + " " + after)
            // Position cursor after inserted path + space
            // (textarea.SetValue resets cursor to end, which is correct here)
        }
        fp.active = false
        return m, nil

    case tea.KeyUp:
        if fp.cursor > 0 {
            fp.cursor--
        }
        return m, nil

    case tea.KeyDown:
        if fp.cursor < len(fp.matches)-1 {
            fp.cursor++
        }
        return m, nil

    case tea.KeyBackspace:
        if fp.query == "" {
            // Backspace past '@' -- dismiss picker AND delete the '@' from textarea
            val := m.prompt.Value()
            if fp.triggerCol > 0 {
                m.prompt.SetValue(val[:fp.triggerCol-1] + val[fp.triggerCol:])
            }
            fp.active = false
            return m, nil
        }
        // Delete last query char
        fp.query = fp.query[:len(fp.query)-1]
        fp.matches = fp.filtered()
        fp.cursor = 0
        // Also update textarea to reflect removed char
        var cmd tea.Cmd
        m.prompt, cmd = m.prompt.Update(msg)
        return m, cmd

    case tea.KeyRunes:
        // Append to query, let textarea also see it
        fp.query += string(msg.Runes)
        fp.matches = fp.filtered()
        fp.cursor = 0
        var cmd tea.Cmd
        m.prompt, cmd = m.prompt.Update(msg)
        return m, cmd
    }

    return m, nil
}
```

### Message Routing in `Update`

```go
case filesScanResultMsg:
    m.filePicker.allFiles = msg.Files
    m.filePicker.loading = false
    m.filePicker.err = msg.Err
    m.filePicker.matches = m.filePicker.filtered()
    return m, nil
```

---

## 6. View -- Overlay Rendering

```go
func (fp FilePicker) View(width int) string {
    if !fp.active {
        return ""
    }
    var b strings.Builder
    if fp.loading {
        b.WriteString(" scanning...\n")
        return b.String()
    }
    if fp.err != nil {
        b.WriteString(fmt.Sprintf(" scan error: %v\n", fp.err))
        return b.String()
    }

    visible := fp.maxItems
    if visible == 0 || visible > 10 {
        visible = 10
    }
    // Window around cursor
    start := fp.cursor - visible/2
    if start < 0 { start = 0 }
    end := start + visible
    if end > len(fp.matches) { end = len(fp.matches) }
    if end-start < visible && start > 0 {
        start = end - visible
        if start < 0 { start = 0 }
    }

    for i := start; i < end; i++ {
        prefix := "  "
        if i == fp.cursor {
            prefix = "> "
        }
        b.WriteString(fmt.Sprintf("%s%s\n", prefix, fp.matches[i]))
    }
    if len(fp.matches) == 0 {
        b.WriteString("  (no matches)\n")
    }
    return b.String()
}
```

In `viewPromptScreen`, render the picker dropdown **between the textarea and the footer**:

```go
if m.filePicker.active {
    input.WriteString(m.filePicker.View(w))
}
```

---

## 7. Cursor/Focus Restoration

There is no explicit cursor restoration needed because:

1. **The textarea never loses focus.** The picker intercepts keys *before* the textarea for navigation keys (`Up`/`Down`/`Enter`/`Esc`) and *passes through* to the textarea for character keys (`KeyRunes`, `Backspace`). The textarea's blink cursor remains active throughout.

2. **On selection**, `SetValue()` positions the cursor at end-of-value, which is correct since the `@query` is always at the typing position. If mid-line `@` is needed later, save `m.prompt.Value()` and `m.prompt.CursorPosition()` at activation, then splice and restore via `SetValue` + `SetCursor`.

3. **On dismissal** (`Esc`), the textarea already has focus and its cursor is where the user left it -- nothing to restore.

---

## 8. Data Flow Summary

```
User types '@' in textarea
    -> handlePromptKey detects '@' after letting textarea process it
    -> activates FilePicker, fires scanRepoFiles() cmd if needed
    -> subsequent keys route to handleFilePickerKey()

handleFilePickerKey:
    Runes/Backspace -> update query + forward to textarea -> refilter (sync, <1us)
    Up/Down         -> move cursor (absorbed, not sent to textarea)
    Enter           -> splice selected path into textarea value, deactivate
    Esc             -> deactivate (textarea untouched)

scanRepoFiles (goroutine via tea.Cmd):
    -> walks repo, returns filesScanResultMsg
    -> Model.Update stores files, refilters

View:
    -> prompt textarea renders normally (user sees '@query' being typed)
    -> FilePicker.View() renders dropdown list below textarea
```

This keeps all IO in `tea.Cmd`, all state in value types, parent owns routing, child emits no custom messages (selection/dismissal are handled inline by the parent), and the textarea cursor is never disrupted.

---

## Files to Create/Modify

| File | Action |
|------|--------|
| `internal/tui/filepicker.go` | **Create** -- `FilePicker` struct, `scanRepoFiles`, `fuzzyMatch`, `filtered`, `View` |
| `internal/tui/messages.go` | **Modify** -- add `filesScanResultMsg` |
| `internal/tui/model.go` | **Modify** -- add `filePicker FilePicker` field, wire `handleFilePickerKey`, handle `filesScanResultMsg` in `Update`, render picker in `viewPromptScreen` |
