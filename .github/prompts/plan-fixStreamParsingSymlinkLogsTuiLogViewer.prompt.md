# Plan: Fix Stream Parsing, Symlink Logs, TUI Log Viewer

Three-pronged fix: (1) fix the broken `stream-json` parser (root cause identified), (2) symlink Claude's JSONL session logs into the run dir for audit, (3) TUI log viewer in the dashboard with improved tool-use rendering.

---

## Root Cause Analysis

**Claude CLI `stream-json` format has changed.** The parser was written for the old Anthropic SSE format where `content_block_start` and `content_block_delta` were top-level event types. The current format is:

### Current format (without `--include-partial-messages`)

| Event type         | Contents                                                                                       |
| ------------------ | ---------------------------------------------------------------------------------------------- |
| `system`           | Init event with `cwd`, `session_id`, `tools[]`                                                 |
| `assistant`        | Full `message.content[]` array with `type:"text"`, `type:"tool_use"`, `type:"thinking"` blocks |
| `user`             | Tool results as `tool_result` content blocks                                                   |
| `rate_limit_event` | Rate limit status                                                                              |
| `result`           | Final result with `session_id`, `usage`, `result` text                                         |

**There are NO `content_block_start` or `content_block_delta` events at the top level.**

### Parser bugs (exact locations)

1. **`claude_cli.go:252-255`** — `case "content_block_start"`: **NEVER FIRES**. This event type no longer exists at the top level. Tool uses are inside `assistant` events as `message.content[].type == "tool_use"`.

2. **`claude_cli.go:245-248`** — `case "content_block_delta"`: **NEVER FIRES**. Text deltas no longer exist without `--include-partial-messages`.

3. **`claude_cli.go:241-244`** — `case "assistant"` → `extractAssistantText()`: Only extracts `type:"text"` blocks, **silently skips `type:"tool_use"` blocks**. This is why activities never appear.

4. **Text delay**: Without `--include-partial-messages`, text only arrives in complete `assistant` messages (one per turn), not incrementally. A multi-second LLM turn produces zero text until the full turn completes.

### Fix strategy: Add `--include-partial-messages`

With `--include-partial-messages`, Claude CLI emits `stream_event` objects wrapping the original Anthropic API events:

```
stream_event: event.type=content_block_start  cb.type=tool_use  cb.name=Read
stream_event: event.type=content_block_delta  delta.type=input_json_delta  pjson='{"file_path": "..."}'
stream_event: event.type=content_block_delta  delta.type=text_delta  text='...'
```

These nested events contain the `content_block_start`/`content_block_delta` the parser expects — just wrapped one level deeper. This gives us:

- Real-time tool-use detection (activity bar)
- Incremental text streaming (no delay)
- Thinking block visibility (optional, for future use)

---

## Phase 1: Fix `stream-json` Parsing

### Step 1: Add `--include-partial-messages` flag

In `claude_cli.go`, change the `RunStreaming` args from:

```go
args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
```

to:

```go
args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
```

Same for `RunContinue`.

### Step 2: Handle `stream_event` wrapper

Add a new case to the event loop in `RunStreaming()` and `RunContinue()`:

```go
case "stream_event":
    // Unwrap: stream_event wraps original Anthropic API events
    inner := event.extractInnerEvent()
    // Dispatch inner event by its type (content_block_start, content_block_delta, etc.)
```

Add to `streamEvent`:

```go
type streamEvent struct {
    // ... existing fields ...
    Event json.RawMessage `json:"event,omitempty"` // inner event for stream_event wrapper
}
```

### Step 3: Extract tool use from `assistant` events (fallback)

Update `extractAssistantText()` (or add `extractAssistantToolUse()`) to also extract `type:"tool_use"` blocks from `assistant` messages. This handles the case where `--include-partial-messages` is unavailable or a completed assistant message arrives:

```go
func (e *streamEvent) extractAssistantToolUse() (name string, args json.RawMessage) {
    // Parse message.content[], find type:"tool_use", return name + input
}
```

In the `"assistant"` case, call both `extractAssistantText()` and `extractAssistantToolUse()`.

### Step 4: Update tests

Add test cases in `output_test.go` / `claude_cli_test.go` with real captured events:

- `stream_event` wrapping `content_block_start` with `tool_use`
- `stream_event` wrapping `content_block_delta` with `text_delta`
- `assistant` with `tool_use` content blocks (non-partial fallback)

**Files:** `internal/harness/claude_cli.go`, `internal/harness/output.go`, `internal/harness/output_test.go`, `internal/harness/claude_cli_test.go`

---

## Phase 1b: Improved Tool-Use Rendering in TUI

Replace the single-line activity bar with a multi-line tool activity log. Each tool invocation renders on its own line with distinct styling for the tool name (dim/low-contrast) and the detail/filepath (accent color).

### Current rendering (single-line, cramped)

```
 ⚡ Read /path/file.go  ⚡ Bash ls -la  ⚡ Write main.go
```

### New rendering (multi-line, scannable)

```
 Read  internal/harness/claude_cli.go
 Bash  go test ./internal/harness/...
 Edit  internal/tui/model.go
 Read  go.mod
 Grep  StreamBuffer
```

### Design

- **Tool name**: dimmed foreground (`color 244`), regular weight — low visual priority since the user cares about _what_ was affected, not _which_ tool
- **Detail/filepath**: brighter color (`color 12` / blue) — high visual priority, the actionable information
- **No icon**: drop the `⚡` — it adds clutter at scale; each line being a tool call is already implied by position
- **Max 8 lines**: show last 8 tool invocations to balance context vs. space. Older entries scroll off naturally
- **File paths are OSC 8 hyperlinks** (keep existing `fileHyperlink()` for clickable paths in iTerm2/Kitty)

### Implementation

- Rename `renderActivityBar` → `renderActivityLog` in `model.go`
- Change from horizontal join to vertical line-per-activity
- Update `activityToolStyle` in `styles.go`: remove `.Bold(true)`, keep `.Faint(true)`, color `244` (dim gray)
- Add `activityPathStyle` in `styles.go`: color `12` (blue), no faint — for file path details
- Keep `activityDetailStyle` (color `244`, faint) for non-path details (Bash commands, Grep patterns)
- Adjust `viewStreaming` layout accounting: activity log height = `min(len(activities), 8)` lines instead of 1

**Files:** `internal/tui/model.go` (`renderActivityLog`, `viewStreaming`), `internal/tui/styles.go` (new `activityPathStyle`)

---

## Phase 2: Save Run Metadata and Files (No Symlinks)

Instead of symlinking the Claude session logs (which are prone to races), Orqestra will persist rich metadata around its runs.

4.  **Save Step Metadata**: Inside `.orqestra/sessions/<run>/`, persist metadata files for every step. E.g., `.orqestra/sessions/<run>/<step-id>.json`.
    - This file must include: `model_ref`, `start_time`, `end_time`, `claude_session_id`, `status` (success/error), and any `error` message.
5.  **Save Markdown Artifacts**:
    - Store the original `prompt.md` into the run directory.
    - Store the generated `plan.md` into the run directory.

_Implementation detail_: Add standard export logic per step to the `.orqestra/sessions/` directory.

**Files:** `internal/agent/session.go`, `internal/orchestrator/orchestrator.go`

---

## Phase 3: TUI Runs History List & Log Viewer

### Step 7: Runs List View

Add a new global `<runs>` history view to the TUI (accessible via a dedicated keystroke, e.g., `Ctrl+H` or a similar menu key).

- **The List**: It displays previous runs. Each run should be represented as a 1-3 line string featuring the original word-wrapped prompt.

### Step 8: Run Details (Two-Column View)

When the user opens a specific run from the list menu, navigate to a two-column view (similar to the main dashboard screen).

- **Left Column (Big scrolling area)**: Displays a metadata header and a text view featuring the run log of the specific agent.
- **Right Column (Narrow list)**: Shows the list of agents/steps that executed in this run.

**Interactions within Details View**:

- `Up` / `Down` arrows map to switching agents within the right column.
- `PgUp` / `PgDown` and `Mouse Wheel` to scroll the log in the left column.
- `Ctrl+E` to open the log (the `.jsonl` from `~/.claude/projects/...`) in the system's default editor.

### Step 9: Parse the Claude JSONL for the Left Column

When displaying the agent log in the left column, we will use the `claude_session_id` saved in the run's metadata to locate the JSONL file directly within the `~/.claude/projects/` directory (resolving the workspace CWD name into the Anthropic dash-separated format: e.g., `~/.claude/projects/-Users-xiii-Developer-orqestra/<id>.jsonl` using `os.UserHomeDir()`).

**JSONL Schema Definition (Sampled from real logs)**:

For the parser, the `~/.claude` JSONL entries look like this. We will need Go structs representing these elements to filter and format the output:

```go
type ClaudeLogEntry struct {
    Type      string    `json:"type"`      // E.g., "user", "assistant"
    Timestamp time.Time `json:"timestamp"`
    Message   *struct {
        Role    string          `json:"role"`
        Content json.RawMessage `json:"content"`
    } `json:"message,omitempty"`
}

// Inside "content" when it's an array:
type ClaudeContentBlock struct {
    Type string `json:"type"`             // "text", "tool_use", "tool_result"
    Text string `json:"text,omitempty"`   // if type == "text"
    Name string `json:"name,omitempty"`   // if type == "tool_use"
    Input json.RawMessage `json:"input,omitempty"` // if type == "tool_use"
}
```

Rendering rules for the TUI:

- `type:"assistant"` with `tool_use` content → tool line: dim tool name + blue filepath
- `type:"assistant"` with `text` content → dim text prefixed with `╶`, truncated to 1 line per text block
- `type:"user"` with `tool_result` → skip (too verbose, tool results are internal)
- `type:"system"`, `type:"rate_limit_event"`, queue operations → skip

### Step 10: Handle Window Resize for Viewports

Ensure that `dashboardLogVP` and other newly introduced `viewport.Model` elements properly respond to `tea.WindowSizeMsg` in the main `Update()` function so their height/width scales accurately based on the terminal window.

**Files:** `internal/tui/model.go`, `internal/tui/messages.go`, `internal/tui/styles.go`, `internal/harness/logpath.go` (new helper to format the ~ path)

---

## Verification

1. `go test ./internal/harness/...` — parser fix tests, logpath tests
2. `go test ./internal/agent/...` — Output file write tests (prompt.md, metadata)
3. `go test ./internal/tui/...` — golden output for log viewer
4. Manual E2E:
   - Activity log shows tool uses in real-time with dim tool name + blue filepath
   - Text streams incrementally (no multi-second delay)
   - `.orqestra/sessions/<run>/` contains `.json` metadata and `.md` prompt/plans for each run step.
   - `Ctrl+H` opens runs list. Navigating to a run shows the two-column interface. Right column handles Up/Down to switch, Left column shows scrollable run history loaded straight from `~/.claude/projects/`.

## Decisions

- **Add `--include-partial-messages`** — restores incremental text + tool-use events via `stream_event` wrapper. Also keep `assistant` handler as fallback for the complete-message events
- **Multi-line tool log, not single-line bar** — each tool on its own line with dim name + colored filepath is more scannable than cramming 5 activities on one line
- **JSON Metadata Instead of Symlinks** — symlinks introduce race conditions as the session JSONL may not be flushed synchronously with Orqestra finishing the step.
- **Dedicated Runs View** — providing a standard list-to-detail interaction to explore all local pipeline runs, with a 2-column split.
