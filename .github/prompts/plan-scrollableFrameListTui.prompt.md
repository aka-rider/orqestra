# Plan: Scrollable Frame-List TUI

**TL;DR**: Replace the single-viewport/mode-based pipeline screen with a scrollable list of bordered agent frames, each containing Claude Code-style tool sub-blocks and streaming text. Frames have init → in-progress → finished lifecycle. Shimmer animation moves into the active frame header. Built incrementally.

---

## Visual Model

```
───────────────────────────────────────────────────────────────────────────────────────────────────── Researcher ──(claude-sonnet-4-20250514) ──── ✓ 12s ─╮
      9 These are called by the framework or queue worker, never by in-process CALLS edges:
     … +58 lines (ctrl+o to expand)

  ⎿  Allowed by auto mode classifier

⏺ Now commit and push:

⏺ Bash(git -C /Users/xiii/Developer/brocc-ab/core-foundation-dev-environment add .claude/skills/monorepo-mcp/ && \
      git -C /Users/xiii/Developer/brocc-ab/core-foundatio…)
  ⎿  [add-mcp-server cd58491] Add monorepo-mcp skill with explicit blind spots
      6 files changed, 477 insertions(+)
      create mode 100644 .claude/skills/monorepo-mcp/SKILL.md
     … +9 lines (ctrl+o to expand)
  ⎿  (timeout 1m)
  ⎿  Allowed by auto mode classifier

⏺ Pushed. The skill is live and already showing up in the skill list (you can see monorepo-mcp in the system reminder).

  What was created:

  SKILL.md — the main entry point with:
  - Full tool reference table (all 22 tools)
  - 5 workflow categories with references to load
  - 9 explicitly documented blind spots in a dedicated section

  Reference files:

  ┌────────────────────┬─────────────────────────────────────────────────────────────────────────────────────────────┐
  │        File        │                                           Covers                                            │
  ├────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ call-graph.md      │ Callers/callees, impact analysis, edge kinds, framework entry-point false positives         │
  ├────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ db-access.md       │ find_table_access workflows, convention-name fallback, transitive DB audit                  │
  ├────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ dispatch-events.md │ DISPATCHES/FIRES chains, the Event::dispatch() caveat, what IS vs IS NOT captured           │
  ├────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ symbol-lookup.md   │ Find/search/browse, FQN format, ID stability, file path format                              │
  ├────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ dead-code.md       │ Four-signal pipeline, framework entry points that always look dead, dynamic coverage signal │
  └────────────────────┴─────────────────────────────────────────────────────────────────────────────────────────────┘

  The blind spots surface every time the skill is invoked, so answers automatically come qualified: "at least N methods write
  this table via static calls; instance-method writes are not tracked."

✻ Crunched for 3m 35s

※ recap: Added DB access edges and MCP tools to the monorepo symbol graph, then created a skill for using them. Branch
  add-mcp-server is pushed with everything committed including the LFS-tracked database. Ready for a PR. (disable recaps in
  /config)

────────────────────────────────────────────────────────────────────────────────────────────────────────────────── Architect ──
❯
───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
   Sonnet 4.6 |  — tok/s |  115.2k  299 |  58%                                    new task? /clear to save 115.5k tokens
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents

╭─ Architect (claude-sonnet-4-20250514) ── ·∘○∘· ────╮
│                                                     │
│ Based on the research, I'll design...               │
│                                                     │
│ ╭─ Write ───────────────────────────────────╮       │
│ │ internal/baz/qux.go                       │       │
│ ╰───────────────────────────────────────────╯       │
│                                                     │
│ ▎streaming text continues here...                   │
╰─────────────────────────────────────────────────────╯

╭─ Plan ──────────────────────────────────────────────╮
│ ## Implementation Plan                              │
│ 1. Create foo.go...                                 │
│ 2. Modify bar.go...                                 │
╰─────────────────────────────────────────────────────╯
  [^A Approve] [^E Comment] [^X Edit]
```

---

## Design Decisions (Confirmed)

- One bordered frame per agent execution
- Tool calls as bordered sub-blocks inside agent frames (Claude Code style)
- Finished frames stay full height (no collapsing)
- Plan gate renders inline as a Markdown frame in the list
- Auto-follow bottom unless user scrolls up manually
- Shimmer animation moves into the active frame's header
- Incremental migration strategy

---

## Architecture

### New Types (`internal/tui/frame.go`)

```
FrameKind: AgentFrame | PlanFrame | CompletionFrame | ErrorFrame
FrameState: FrameInit | FrameInProgress | FrameFinished

Frame struct:
  Kind        FrameKind
  State       FrameState
  AgentID     string
  AgentModel  string
  Elapsed     time.Duration
  StartedAt   time.Time
  Tokens      StreamStats
  TextLines   []string       // streaming text lines
  Partial     string         // current partial line
  Tools       []ToolBlock    // ordered tool sub-blocks
  Markdown    string         // for PlanFrame: rendered markdown
  ContentParts []ContentPart // interleaved text + tools in insertion order

ToolBlock struct:
  Icon   string
  Name   string
  Detail string

ContentPart struct:
  IsText bool
  Text   string     // when IsText
  Tool   ToolBlock  // when !IsText
```

### New Type (`internal/tui/framelist.go`)

```
FrameList struct:
  frames       []Frame
  width        int
  height       int        // viewport height
  scrollOffset int        // line offset from top
  userScrolled bool       // user manually scrolled up
  animFrame    int        // shimmer animation counter
```

Key methods:

- `AppendFrame(Frame)` — add new frame, auto-scroll if following
- `UpdateActive(fn func(*Frame))` — mutate the last in-progress frame
- `FinishActive(elapsed, tokens)` — mark current frame finished
- `Render() string` — concatenate all frame renders, apply scroll window
- `ScrollUp() / ScrollDown() / GotoBottom()` — manual scroll
- `IsFollowing() bool` — auto-follow state
- `SetSize(w, h)` — layout update

### Rendering Strategy

Use a **single viewport.Model** that holds the concatenated render of all frames. Reuses Bubble Tea's viewport scrolling + mouse wheel without reinventing scroll mechanics.

On each sync:

1. Render all frames → concatenated string
2. Set viewport content
3. If `userScrolled == false`, `GotoBottom()`
4. If user scrolls up (viewport offset changes without programmatic cause), set `userScrolled = true`
5. `GotoBottom` key (e.g. `G` or `End`) resets `userScrolled = false`

### Event → Frame Mapping

| Event                   | Frame Action                                                             |
| ----------------------- | ------------------------------------------------------------------------ |
| `EventAgentStarted`     | `AppendFrame(AgentFrame, InProgress, agentID, model)`                    |
| `EntryText` (stream)    | `UpdateActive` → append to TextLines/Partial/ContentParts                |
| `EntryToolUse` (stream) | `UpdateActive` → append ToolBlock to Tools/ContentParts                  |
| `EventAgentDone`        | `FinishActive(elapsed, tokens)`                                          |
| `EventGateRequest`      | `AppendFrame(PlanFrame, InProgress, markdown)` then immediately `Finish` |
| `EventChatResponse`     | Append text to PlanFrame or create new inline frame                      |
| `EventComplete`         | `AppendFrame(CompletionFrame, Finished, summary)`                        |
| `EventAgentFailed`      | `FinishActive` with error state                                          |
| `animTickMsg`           | increment `animFrame` on FrameList                                       |

---

## Steps

### Phase 1: Frame Infrastructure (no visible change yet)

1. Create `internal/tui/frame.go` — define `Frame`, `FrameKind`, `FrameState`, `ToolBlock`, `ContentPart` types
2. Create `internal/tui/framelist.go` — define `FrameList` with `AppendFrame`, `UpdateActive`, `FinishActive`, `Render`, `SetSize`, scroll methods
3. Implement `renderFrame(f Frame, width int) string` — bordered agent frame with header (name + model + state indicator + elapsed), interleaved content parts, tool sub-blocks
4. Implement `renderToolBlock(t ToolBlock, width int) string` — inner bordered block with icon + name + detail
5. Add frame rendering tests (golden file based): init state, in-progress with shimmer, finished with elapsed/tokens

### Phase 2: Wire Stream Data into FrameList

6. Add `FrameList` field to `PipelineScreen` (alongside existing `streamBuf`)
7. In `ApplyEvent()`: on `EventAgentStarted` → call `frameList.AppendFrame()`; on `EventAgentDone` → call `frameList.FinishActive()`
8. In `DrainStreamUpdates()`: after writing to `streamBuf`, also route `EntryText`/`EntryToolUse` into `frameList.UpdateActive()`
9. On `EventGateRequest`: append PlanFrame with rendered markdown content
10. On `EventComplete`: append CompletionFrame with summary

### Phase 3: Switch Pipeline Rendering to FrameList

11. Add new `ContentMode`: `ContentFrameList` (or repurpose `ContentStreaming`)
12. In `viewContent()`: when mode is streaming, render `frameList.Render()` instead of old `viewStreaming()`
13. Move shimmer animation from status bar into `FrameList.Render()` (active frame header)
14. Implement auto-follow logic: track `viewport.YOffset` changes, detect manual scroll, `GotoBottom` on new content when following
15. Wire `WindowSizeMsg` → `frameList.SetSize()`
16. Handle plan gate interaction: plan frame shows controls hint, key handlers still route to existing intent system

### Phase 4: Cleanup & Polish

17. Remove old `viewStreaming()`, `renderActivityLog()`, collapsed stream block code
18. Update `viewStatusLine()` — simplify since shimmer moved into frames; keep agent chain as compact reference
19. Adapt `ContentPlanReview` → plan content already inline; remove mode or alias to frame list
20. Adapt `ContentCompletion` → completion already inline; remove mode or alias
21. Adapt `ContentAgentHistory` (Alt+N) — scroll to the specific agent frame instead of rendering separately
22. Update layout constants: remove `streamPreviewLines`, adjust input height if needed
23. Add/update tests: scroll stability across ticks, resize, auto-follow toggle, plan gate interaction within frame list

---

## Relevant Files

- `internal/tui/frame.go` (NEW) — Frame, FrameKind, FrameState, ToolBlock, ContentPart types + renderFrame()
- `internal/tui/framelist.go` (NEW) — FrameList model with scroll, append, render
- `internal/tui/screen_pipeline.go` — Major: replace viewStreaming/viewContent dispatch, wire events to FrameList, adapt SyncViewports
- `internal/tui/model.go` — Minor: ContentMode enum changes, recalculateLayout adjustments, animation tick routing
- `internal/tui/layout.go` — Minor: remove/adjust constants (streamPreviewLines gone)
- `internal/tui/styles.go` — Add frame border styles, tool sub-block styles, shimmer-in-header style
- `internal/tui/messages.go` — Possibly add scroll intent messages
- `internal/orchestrator/stream_ring.go` — No changes needed (SnapshotText already provides what we need)

---

## Verification

1. `go test -race ./internal/tui/ -run TestFrame` — golden-file tests for frame rendering (init/in-progress/finished states)
2. `go test -race ./internal/tui/ -run TestFrameList` — scroll behavior, auto-follow, manual scroll detection
3. `go test -race ./internal/tui/ -run TestPipeline` — existing pipeline screen tests still pass (incremental: old modes work until migrated)
4. `make test` — full test suite passes
5. Manual: `make run` with a real orchestration run, verify:
   - Agent frames appear bordered with headers
   - Tool calls render as sub-blocks inside agent frames
   - Text streams live with shimmer in active header
   - Scrolling up pauses auto-follow; End/G resumes
   - Plan gate appears inline, ^A/^E/^X still work
   - Completion summary appears as final frame
   - Resize works cleanly at all stages

---

## Scope Boundaries

**Included:**

- Frame infrastructure (types, rendering, scroll)
- Stream → frame wiring
- Replace streaming/plan/completion content modes with frame-list rendering
- Shimmer in active frame header
- Auto-follow with manual scroll override

**Excluded:**

- Dashboard overlay (^D) — stays as-is, rendered on top
- UserQuestion mode — stays modal for now (could be inline later)
- MergeConflict mode — stays modal
- Run history screens — untouched
- Stream data pipeline (harness → orchestrator) — no changes needed
- Prompt screen — untouched

---

## Further Considerations

1. **Frame height explosion** — With "stay full height" and verbose agents, the concatenated render could be thousands of lines. The viewport handles this via its internal line buffer, but we may want to lazy-render only visible frames for perf. Start simple (render all), optimize if profiling shows lag.

2. **PlanDiff (^P)** — Currently uses a separate diff viewport. Could render as an inline frame or keep as a modal toggle. Recommend: keep as modal overlay for now, migrate in a follow-up.

3. **Goal display** — Currently at the top of viewStreaming(). In the new model, could be a static header above the frame list, or the first frame. Recommend: keep as a 1-line static header above the scrollable area (not a frame).
