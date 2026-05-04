# Plan: Intake Receptionist + Unified Human Gate

## Problem

Two problems converge:

### 1. Intake acts as a gatekeeper instead of a receptionist

The current `Recognizer` receives the raw user prompt, sends it to an LLM, and returns a `verdict` (accept/clarify/reject). This is backwards:

- The LLM's `reject` verdict blocks the user from proceeding — but the user is the authority, not the LLM. From a UI perspective, showing "✗ Not planner-ready" and disabling Accept makes zero sense: who is the LLM to refuse service?
- The LLM judges the prompt without any repo context, so its "understanding" is an ungrounded guess.
- On acceptance, the raw prompt passes through to the planner unchanged — no context enrichment, no optimization.
- The planner then hallucinates file paths and APIs because it never received repo context.

Intake should be a **triage receptionist**: analyze the raw prompt, perform fast keyword-targeted code lookups to disambiguate fuzzy references, clean up the grammar, establish a Definition of Done, and surface precise questions if context is heavily lacking.

### 2. Two gate UIs with inconsistent interaction

- **Intent gate** (`StateIntentConfirm`): plain text in tab area, `[A]ccept`/`[R]efine`, no edit option.
- **Plan gate** (`StateConfirming`): two-pane scrollable viewport, `[A]pprove`/`[R]eject`/`[E]dit`.

The edit/save flow writes bare markdown (no metadata), and `--plan` restore has no awareness of which phase produced the file.

## Requirements

1. Intake triages the prompt, cleaning up grammar and expanding implicit context by actively reading target files, culminating in a strict Definition of Done.
2. Intake never rejects. It always produces its best understanding. If uncertain, it surfaces questions — but never blocks.
3. On user confirmation, intake hands off a clean, declarative work order to the planner (Opus). It does NOT spoon-feed generic repository context (file trees, git logs) to the planner, preserving Opus's context window.
4. Intake and Plan confirmation share the same UI widget with phase-appropriate content and framing.
5. Both gates offer: Accept, Refine/Reject, Edit (save to file).
6. Saved files include YAML frontmatter with phase and the enriched triage prompt for session restoration.
7. `orqestra --plan <file>` parses frontmatter independently and restores to the correct phase.

## Phase 1: Targeted Context Gathering (Intake Only)

Intake needs specific code context to clean the prompt (e.g., mapping "dashboard alignment" to actual files), but dumping massive file trees and git histories burns context windows. This phase adds fast, targeted local context gathering strictly for the small model's use.

### Files

- `internal/intent/context.go` (new)
- `internal/intent/context_test.go` (new)
- `internal/intent/intent.go` (modify)
- `internal/intent/intent_test.go` (modify)

### Changes — `internal/intent/context.go`

```go
package intent

import "context"

// CodeSnippet represents a targeted file extraction for the Intake model.
type CodeSnippet struct {
    Path    string
    Content string // Truncated to a reasonable bound if too large
}

// RepoContext holds targeted code snippets relevant to resolving fuzzy nouns
// in the user's prompt. It is passed strictly to the Intake model, not the Planner.
type RepoContext struct {
    RootDir  string
    Snippets []CodeSnippet
}

// GatherContext performs fast keyword matching against the codebase to pull
// targeted snippets for the small Intake model to read (e.g. reading `cmd/ui/dashboard.go`).
// Does not dump generic file trees or git histories to preserve context windows.
// Respects context cancellation.
func GatherContext(ctx context.Context, rootDir, prompt string) (RepoContext, error)
```

Implementation:

- **Keyword Extraction**: Fast local tokenization of the user prompt to find likely file paths or component names (e.g. "auth", "dashboard", "stream").
- **Targeted Reading**: `filepath.WalkDir` with `bytes.Contains(fileName/path)` to locate exact matches, explicitly skipping `.git`, `vendor`, `node_modules`.
- **Content Bounding**: Extract the file content, capped at ~1,000 characters per file (up to max 5 files) to keep the Intake context window hyper-efficient.
- **Graceful Degradation**: Silent logs on read errors. If no files match the fuzzy prompt strings, `Snippets` returns empty and the Intake LLM relies purely on its generic prompt context structure.

### Changes — `internal/intent/intent.go`

**Remove `VerdictReject`.** The receptionist doesn't reject. If the LLM returns `reject`, coerce to `clarify` with the reason surfaced as a question.

```go
type Verdict string

const (
    VerdictAccept  Verdict = "accept"   // prompt is clear enough to plan
    VerdictClarify Verdict = "clarify"  // understood, but has questions
)
```

**Update `Recognizer.Recognize` signature** to accept repo context:

```go
func (r *Recognizer) Recognize(ctx context.Context, rawPrompt string, repoCtx RepoContext) (Intent, error)
```

The LLM now receives both the raw prompt and a summary of repo context, so its rephrasing is grounded in actual file paths and package names — not guesses.

Validation changes:

- Remove the `VerdictReject` case. If the LLM returns `"reject"`, map it to `VerdictClarify` and promote `Reason` to a question: `"The request may need clarification: {reason}"`.
- Remove the `intentVerdict == "reject"` UI gating (see Phase 4).

### Tests

- `GatherContext` on a test directory with known structure → verify fields populated.
- `GatherContext` on empty directory → zero-value fields, no error.
- `Recognize` with repo context → rephrased intent references actual package names.
- `Recognize` with former "reject" LLM response → coerced to `clarify`, not error.

## Phase 2: Planner Brief Composer

Intake's value-add is not just understanding the user — it's triaging the prompt, aggressively cleaning up grammar, and codifying exactly what success looks like. **Intake must not pass repo context (file trees, commits) to the planner.** The Opus-level planner has its own intelligent tools to explore the workspace. Spoon-feeding it truncated context limits its agency and wastes context window.

### Files

- `internal/intent/brief.go` (new)
- `internal/intent/brief_test.go` (new)

### Changes

```go
package intent

// PlannerBrief is the clean, declarative work order that intake
// hands to the planner. It replaces the raw user prompt.
type PlannerBrief struct {
    Rephrased   string      // refined intent statement, cleaned up and expanded
    EndState    string      // concrete definition of done
    UserPrompt  string      // original for reference
}

// ComposeBrief formats the brief as a strict, goal-oriented prompt string.
func ComposeBrief(b PlannerBrief) string
```

Template (lean and declarative):

```markdown
## Task
{rephrased intent}

## Definition of Done (End State)
{end_state}

## Original Request
{raw prompt, for reference}
```

Note: There is no `Repository Context` section here. The fast local context gathered in Phase 1 is consumed entirely by the small Intake LLM to inform its cleanup and clarification questions. It is purposefully hidden from the Planner so the Planner can use its own tool-calling loops organically.

### Tests

- `ComposeBrief` → verify template populated with clean markdown.

## Phase 3: Document Persistence with Context Fields

Frontmatter extraction from `plan/spec.go` into a new `internal/document/` package — extended with context and enriched-prompt fields so the receptionist's work survives save/restore.

### Files

- `internal/document/document.go` (new)
- `internal/document/document_test.go` (new)
- `internal/plan/spec.go` (modify)
- `internal/plan/testdata/golden.md` (update)

### Changes

```go
package document

type Phase string

// UnmarshalYAML guarantees type safety for strictly allowed phase values.
func (p *Phase) UnmarshalYAML(unmarshal func(interface{}) error) error { ... }

const (
    PhaseIntake Phase = "intake"
    PhasePlan   Phase = "plan"
)

type Frontmatter struct {
    SchemaVersion   string              `yaml:"schema_version"`
    Phase           Phase               `yaml:"phase"`            // strictly "intake" | "plan"
    Prompt          string              `yaml:"prompt"`            // original user prompt
    Timestamp       string              `yaml:"timestamp"`         // ISO 8601
    IntentRephrased string              `yaml:"intent_rephrased,omitempty"`
    IntentEndState  string              `yaml:"intent_end_state,omitempty"`
    IntentQuestions []string            `yaml:"intent_questions,omitempty"`
    EnrichedPrompt  string              `yaml:"enriched_prompt,omitempty"`   // the composed planner brief
}

type File struct {
    Frontmatter Frontmatter
    Body        string
}
```

Note: `IntentVerdict` is deliberately absent. The receptionist doesn't store verdicts — it stores understanding.

Parsing primitives:

- Use `github.com/adrg/frontmatter` (the industry standard for Go) to manage the split between YAML frontmatter and markdown body reliably.
- `LoadFromFile(path string) (File, error)` — Uses `frontmatter.Parse` to populate the `Frontmatter` struct. Missing frontmatter logic is handled cleanly by the library (it leaves the struct zero-valued, which acts as backward compat handling for `Phase: plan`), and returning the rest of the string as `Body`.
- `SaveToFile(path string, fm Frontmatter, body string) error` — marshals to file. If `fm.Phase == PhaseIntake`, inject: `<!-- Orqestra: edit the YAML above to adjust; body below is for reference only. -->`
- `ValidateFrontmatter(fm Frontmatter) error`:
  - `phase` must be `"intake"` or `"plan"`.
  - `prompt` must be non-empty.
  - `schema_version` must be `"1"`.

Update `internal/plan/spec.go` and `internal/plan/spec_test.go`:

- Remove `SaveToFile` and `LoadFromFile` (delegate file ops to `document`).
- Update `UnmarshalMarkdown` to tolerate missing `Goal` and `SchemaVersion` headers **if they are backfilled via Frontmatter** (via `LoadFromFile`). `MarshalMarkdown` should omit `## SchemaVersion` and `## Goal` if `document.SaveToFile` writes them to the YAML frontmatter, thereby avoiding duplication.

### Tests

- Round-trip: marshal with all fields (including `EnrichedPrompt`) → unmarshal → verify.
- Backward compat: legacy markdown without frontmatter → zero `Frontmatter`, no error.
- Corruption: malformed YAML → explicit parse error.
- Validation: missing phase → error, missing prompt → error, invalid phase → error.

## Phase 4: Unified Confirmation UI

### Files

- `internal/tui/view_confirm.go` (modify)
- `internal/tui/view_intent.go` (modify)
- `internal/tui/model.go` (modify)
- `internal/tui/messages.go` (modify)

### Changes to `confirmView`

Add `phase` field (`document.Phase`).

`SetPhase(phase document.Phase)` sets the phase and updates header + footer hints:

- `PhaseIntake` → title `"INTAKE REVIEW"`, footer: `[A]ccept  [R]efine  [E]dit`
- `PhasePlan` → title `"EXECUTION PLAN"`, footer: `[A]pprove  [R]eject  [E]dit`

No `SetAllowApprove`. The user always has authority. The LLM never gates the Accept action.

`View()` renders the phase-appropriate title in the viewport header.

### Changes to `view_intent.go`

Rename `renderIntent` → `renderIntakeReview`. New signature and content:

```go
func renderIntakeReview(rephrased, endState string, questions []string, briefPreview string) string
```

New content structure (rendered using `lipgloss.NewStyle().Border(...)` to respect `tea.WindowSizeMsg` resizing and correct terminal rendering natively instead of hardcoded ASCII):

```
┌ What I Understand ─────────────────────────┐
│ {rephrased intent}                         │
│                                            │
│ End State: {end_state}                     │
└────────────────────────────────────────────┘

┌ What I'll Tell the Planner ────────────────┐
│ Task: {rephrased intent}                   │
│ Definition of Done: {end_state}            │
└────────────────────────────────────────────┘

Questions (if any):
  • Which module should this target?
```

No verdict display. No "✗ Not planner-ready" messaging. No LLM-driven blocking. The receptionist always serves.

Note: We explicitly DO NOT blast the `RepoContext` visually onto the user's screen anymore, as the context is exclusively used to make the "What I Understand" box smarter.

### Caution: Bubbletea Memory Model

When dispatching the newly gathered `RepoContext` into an async `tea.Msg` to enter `StateIntentConfirm`, ensure its slices (e.g. `Packages`, `RelevantFiles`) are rigorously **deep-copied**. Storing pointer-to-slice directly in `p.Send(Msg)` causes critical concurrent map/slice read-write panics.

### Changes to `messages.go`

Remove `IntentConfirmMsg` and `IntentRejectMsg`.

Both gates use `ConfirmMsg` with `ConfirmChoice`: `ConfirmAccept`, `ConfirmReject`, `ConfirmEdit`.

### Changes to `model.go`

**New Model fields:**

```go
plannerBrief string              // composed enriched prompt for planner
// Note: no persistent repoContext on model anymore, context is hyper-transient
```

**Remove** `intentVerdict` — no longer relevant. The receptionist doesn't judge.

**Remove** `intentContent` — replaced by `renderIntakeReview` content in `confirmView`.

**`StateIntentConfirm` handling:**

When entering `StateIntentConfirm`:

- `confirmView.SetPhase(document.PhaseIntake)` — always, unconditionally.
- `confirmView.SetPlanText(renderIntakeReview(rephrased, endState, questions, briefPreview))` (Note: RepoContext is not drawn to screen).
- Call `m.syncConfirmViewport()`.
- Focus `confirmView`.

When `ConfirmMsg` arrives in `StateIntentConfirm`:

- `ConfirmAccept` → compose `plannerBrief` via `intent.ComposeBrief(...)` → set `m.prompt = m.plannerBrief` → `StatePlanning`. **Always allowed. The user is the authority.**
- `ConfirmReject` (labeled "Refine" in intake phase) → `StateIdle` (passing `m.prompt` explicitly back to the internal `commandBar` state so the user's raw prompt is intentionally *preserved and editable*, rather than starting over).
- `ConfirmEdit` → save with `PhaseIntake` frontmatter (including `EnrichedPrompt`) → `StateSaved`.

**`StateConfirming` handling** (unchanged from current behavior):

- `confirmView.SetPhase(document.PhasePlan)`.
- `ConfirmAccept` → `StateExecuting`.
- `ConfirmReject` → `StateDone` → `CycleBackToIdleMsg` → `StateIdle`.
- `ConfirmEdit` → save with `PhasePlan` frontmatter → `StateSaved`.

**Key routing change in `handleKey`:**

`StateIntentConfirm` now forwards keys to `confirmView` instead of handling `a/r` directly. The old direct key handling assumed the LLM verdict controlled what keys were active — that's gone.

**`handleConfirmEdit` refactored:**

```go
func (m Model) handleConfirmEdit() (tea.Model, tea.Cmd) {
    fm := document.Frontmatter{
        SchemaVersion: "1",
        Prompt:        m.prompt,
        Timestamp:     time.Now().UTC().Format(time.RFC3339),
    }

    prefix := "plan"
    if m.state == StateIntentConfirm {
        fm.Phase = document.PhaseIntake
        prefix = "intake"
        fm.IntentRephrased = m.intentRephrased
        fm.IntentEndState  = m.intentEndState
        fm.IntentQuestions = m.intentQuestions
        fm.EnrichedPrompt  = m.plannerBrief
    } else {
        fm.Phase = document.PhasePlan
    }

    filename := fmt.Sprintf("%s-%s.md", prefix, fm.Timestamp)
    // ... call save cmd with filename → StateSaved
}
```

### State Machine After

```
StateIdle → user submits prompt
  → `startIntentRecognition` starts (Bubbletea cmd, non-blocking)
  → `GatherContext` executes synchronously *inside* the background goroutine
  → `Recognize` (LLM, with repo context) finishes
  → Sends `IntentResultMsg` to update loop
  → Enters `StateIntentConfirm`

StateIntentConfirm
  → [A] Accept → ComposeBrief → m.prompt = brief → StatePlanning
  → [R] Refine → StateIdle (prompt preserved in command bar)
  → [E] Edit   → StateSaved (phase:intake) → Quit

StatePlanning → planner receives enriched brief, not raw prompt
  → ... existing flow ...

StateConfirming
  → [A] Approve → StateExecuting
  → [R] Reject  → StateDone → CycleBackToIdleMsg → StateIdle
  → [E] Edit    → StateSaved (phase:plan) → Quit
```

*Note: when `CycleBackToIdleMsg` occurs, `model.prompt` retains the restored file's data for the subsequent loop so context is not lost.*

## Phase 5: Frontmatter-based Restore via `--plan`

### Files

- `cmd/orqestra/main.go`
- `internal/tui/model.go`

### Changes to `main.go`

`--plan` path now:

1. `doc, err := document.LoadFromFile(path)`.
2. If `doc.Frontmatter.Phase != ""`, `document.ValidateFrontmatter(doc.Frontmatter)` → fail clearly on invalid.
3. Route by `doc.Frontmatter.Phase`:
   - `PhaseIntake` with `EnrichedPrompt` set → skip context gathering, pass `InitialFrontmatter` to TUI, start at `StatePlanning` using the enriched prompt (user already confirmed their intent before saving).
   - `PhaseIntake` without `EnrichedPrompt` → user saved before confirming; restore to `StateIntentConfirm` skipping internal fast context extraction dynamically (we just care about the rephrased output).
   - `PhasePlan` (or implicit missing frontmatter mapping to PhasePlan): Use `plan.UnmarshalMarkdown([]byte(doc.Body))` to load the `Spec`. Note that `UnmarshalMarkdown` should succeed even if missing `## SchemaVersion` and `## Goal` as long as `doc.Frontmatter` backfills them. Pass `InitialFrontmatter` and `InitialSpec` to TUI.

### Changes to `PipelineFuncs`

Add field:

```go
InitialFrontmatter *document.Frontmatter
```

### Changes to `NewModel` / `Init()`

If `InitialFrontmatter` is provided:

- Override `m.prompt = InitialFrontmatter.Prompt`.
- Restore `m.intentRephrased`, `m.intentEndState`, `m.intentQuestions` from frontmatter.
- If `EnrichedPrompt` is present, set `m.plannerBrief` and `m.prompt = EnrichedPrompt`.

If `InitialFrontmatter.Phase == document.PhaseIntake`:

- Start at `StateIntentConfirm` (user reviews again) or `StatePlanning` (if enriched prompt was already composed).

### `view_saved.go` Update

Update message to:

```
File saved to <path>
Edit it, then restore:  orqestra --plan <path>
(press any key to exit)
```

## Phase 6: Planner Integration

The planner already receives a string prompt. The change is *what* that string contains.

### Files

- `internal/tui/model.go` (modify — planning transition)

### Changes

When transitioning from `StateIntentConfirm` → `StatePlanning`:

```go
// Compose the enriched brief
brief := intent.ComposeBrief(intent.PlannerBrief{
    Rephrased:   m.intentRephrased,
    EndState:    m.intentEndState,
    UserPrompt:  m.prompt,
})
m.plannerBrief = brief
m.prompt = brief  // planner sees the clean triage brief, not raw input
```

The planner's `Plan(ctx, prompt, stdout)` call receives the enriched brief. No signature change needed — `brief` is a string.

The planner's system prompt in `pipeline.yaml` already says: *"Use only the user's prompt and any context included with it."* The enriched brief satisfies this contract — the planner receives grounded context (real file paths, real packages) instead of guessing.

## Phase 7: Cleanup & Migration

### Remove

- `IntentConfirmMsg`, `IntentRejectMsg` types from `messages.go`.
- `VerdictReject` explicitly deleted from `intent.go` (and all dependent tests) to trigger compiler errors globally, ensuring we find any stray LLM or UI reject bindings.
- Direct `a/A`, `r/R` handling in `handleKey` for `StateIntentConfirm`.
- `m.intentContent` field (replaced by `confirmView` content).
- `m.intentVerdict` field (receptionist doesn't judge).
- `SetAllowApprove` method from `confirmView` (user always has authority).
- `plan.SaveToFile` and `plan.LoadFromFile` (migrated to `document`).

### Keep

- `renderIntakeReview()` as the content formatter for intake phase.
- `IntentResultMsg` (carries LLM response into the TUI).
- `Recognizer` struct (repurposed from gatekeeper to receptionist brain).

### Intent System Prompt Update

Update the intent recognizer's system prompt to reflect the new role:

> You are a triage receptionist for a highly capable software engineering agent. Your job is to transform raw, messy user prompts into crisp, grammatically correct, declarative work orders. You receive the user's prompt and a fast local summary of their repo. Use the repo summary ONLY to understand the user's context (e.g., mapping "the dashboard" to `cmd/ui/dashboard.go`).
>
> - Rephrase their intent clearly, fixing typos and expanding implicit references.
> - Define a strict "Definition of Done" (Expected End State) to measure success against.
> - If critical nouns or targets are entirely missing and cannot be confidently deduced from the local context, proactively populate the `clarify` JSON properties with targeted, helpful questions to prompt the user.
> - Never refuse or reject. Always do your best to understand.

Explicitly rewrite the system prompt JSON-mode schema instructions to exclude `"verdict": "reject"`, preventing blank/invalid payload crashes.

## File Change Summary

| File | Action |
|---|---|
| `internal/intent/context.go` (new) | Repo context gathering |
| `internal/intent/context_test.go` (new) | Context gathering tests |
| `internal/intent/brief.go` (new) | PlannerBrief composer |
| `internal/intent/brief_test.go` (new) | Brief composer tests |
| `internal/intent/intent.go` | Remove VerdictReject, accept RepoContext param |
| `internal/intent/intent_test.go` | Update for new signature, remove reject tests |
| `internal/document/` (new) | Unified save/load, frontmatter with context fields |
| `internal/plan/spec.go` | Remove load/save logic |
| `internal/plan/spec_test.go` | Update for new signatures |
| `internal/plan/testdata/golden.md` | Add frontmatter header |
| `internal/tui/messages.go` | Remove `IntentConfirmMsg`, `IntentRejectMsg` |
| `internal/tui/model.go` | Receptionist flow, remove verdict gating, compose brief |
| `internal/tui/view_confirm.go` | Phase awareness, remove `SetAllowApprove` |
| `internal/tui/view_intent.go` | Replace `renderIntent` with `renderIntakeReview` |
| `internal/tui/view_saved.go` | Update restore message |
| `cmd/orqestra/main.go` | Frontmatter-aware restore, phase routing |

## Implementation Order

1. Phase 1 — Context gathering (new capability, no existing code breaks)
2. Phase 2 — Brief composer (new capability, depends on Phase 1 types)
3. Phase 3 — Document package (frontmatter extraction + new fields)
4. Phase 4 — Unified UI (depends on Phase 1-3 types)
5. Phase 5 — Restore via `--plan` (depends on Phase 3-4)
6. Phase 6 — Planner integration (depends on Phase 2, 4)
7. Phase 7 — Cleanup dead code (last, after everything works)
