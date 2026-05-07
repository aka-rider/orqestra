# Plan: Gateway Prompt Rewrite

## Problem Statement

The gateway is an x-small model that triages user prompts into structured briefs for the Opus-tier planner. Its output quality determines whether the planner gets a well-framed problem or noise. Currently it produces over-specified, lossy output that replaces user intent with fabricated implementation details nobody reads.

### Root Cause Analysis

1. **Dead fields create hallucination pressure.** `deliverables` and `acceptance_hints` are parsed, validated, and thrown away — never displayed in the TUI, never forwarded to the planner. Small models fill every schema field. These fields force the model to invent filenames, interfaces, and architecture decisions at Haiku quality. Pure waste: tokens + latency + false authority.

2. **`planner_question` is the only accept-path output that reaches the planner** (`plannerInput = gwResult.PlannerQuestion`), but the prompt encourages embedding implementation answers into it: "How should X be designed as [ANSWER]..." — pre-answering what Opus should discover.

3. **The prompt teaches lossy rewriting.** "Translate user prose into LLM specification language" destroys user framing. The planner operates on a distorted signal — gateway's interpretation of intent, not intent itself.

4. **Tool access is unrestricted and unmentioned.** The gateway runs as bare `ClaudeCLI` with full tool access (Read, Grep, Glob, Bash, all MCP servers), but the system prompt never references tools. The model may or may not use them — undefined behavior. When it does, it's x-small quality agentic reasoning that the planner will redo.

5. **Coach/accept threshold conflates WHAT and HOW.** The prompt has no distinction between ambiguous intent (coach) and ambiguous implementation (accept). "Like Claude Code does it" is ambiguous HOW but clear WHAT — should accept, currently undefined.

## Data Flow Evidence

```
Accept path:
  orchestrator.go:256 → plannerInput = gwResult.PlannerQuestion
  Planner sees: ONE string. No deliverables, no scope, no acceptance_hints.

Auto-approve coach fallback:
  buildPlannerInputFromBrief() → "How should %q be implemented such that the end state is: %s?"
  Planner sees: brief.Task + brief.EndState. Nothing else.

TUI coaching view:
  viewCoaching() renders: brief.Task, brief.EndState, questions.
  deliverables, scope, non_scope, acceptance_hints → never rendered.
```

## Phases

### Phase 1: Schema Surgery

Remove dead fields from `PromptBrief` and the system prompt JSON schema.

**Remove:**

- `Deliverables []string` from `PromptBrief` struct
- `AcceptanceHints []string` from `PromptBrief` struct
- `PlannerQuestion string` from `GatewayResult` struct
- All corresponding validation in `Evaluate()`
- All references in system prompt, examples, field guidance

**Keep:**

- `Task`, `EndState` — displayed in TUI, used in fallback planner input
- `Scope`, `NonScope` — cheap to fill (package names), useful for future TUI enhancement and planner context
- `Questions`, `Confidence`, `Verdict` — functional

**Files:**

- `internal/agent/gateway.go` — struct changes, remove `PlannerQuestion` validation
- `internal/agent/gateway_test.go` — update all test fixtures (remove `planner_question`, `deliverables`, `acceptance_hints` from JSON)
- `internal/config/pipeline.yaml` — gateway system prompt schema + examples
- `internal/tui/app_test.go` — update fixtures referencing `PromptBrief`

### Phase 2: Tool Access Restriction

Set gateway to `WithNoTools()`.

The gateway's irreducible function is interactive disambiguation — a pure language task. Tool access enables grounded observations but at x-small quality, those observations may mislead. The planner re-does all repo exploration at Opus quality. Removing tools makes the gateway faster, cheaper, and eliminates undefined behavior.

**Implementation:** Add tool restriction to `pipeline.yaml` gateway config:

```yaml
gateway:
  model_ref: x-small
  mcp_servers: [] # triggers WithNoTools() via toolOpts()
```

**Files:**

- `internal/config/pipeline.yaml` — add `mcp_servers: []` or `allowed_tools: []`

### Phase 3: Prompt Rewrite

Complete rewrite of the gateway system prompt around four principles:

1. **Preserve the ask** — the gateway's job is to faithfully relay user intent, not rewrite it into spec-speak
2. **Clear outcome / ambiguous outcome** — coach on ambiguous end result, accept ambiguous implementation
3. **Grounding discipline** — every concrete claim must trace to a source
4. **Mirror voice** — "Did you mean A or B?" never "Have you considered...?"

**New system prompt:**

```
Preserve the ask. You are the intake gate for Orqestra's planner. Your
job is to verify user intent is unambiguous, then hand it over faithfully.

You have no tools and no repository access. The planner has both.

GROUNDING RULES — every concrete claim must come from one of:
- user_text: explicitly stated by the user in their prompt.
- user_context: file or directory paths the user attached (you see
  names/paths, not content). Use these to confirm scope or resolve
  ambiguity, but do not analyze code.
- generic_engineering: common risk or pattern, broad wording only.

Only user_text and user_context may produce specific file paths, package
names, function names, library names, counts, or tests. If you cannot
trace a claim to one of these sources, do not emit it.

VERDICT RULES:
- clear_outcome → "accept". You can tell what done looks like, even if
  implementation details are open. Accept.
- ambiguous_outcome → "coach". Two or more materially different end
  results could satisfy the prompt. Ask which they mean.
- unbounded_scope → "coach" with scope-narrowing defaults.

ACCEPT BIAS:
- User names a file, package, command, error, or existing feature → accept.
- User references external behavior ("like X does it") and the referent
  identifies a specific feature → accept. The planner resolves scope.
- User asks to improve, fix, test, refactor, document, or review a named
  area → accept.
- Never ask HOW questions. Implementation approach, library choice,
  architecture pattern — all planner territory.

COACHING TRIGGERS:
- No identifiable target (no file, package, feature, or behavior named).
- Multiple different end states could satisfy the prompt.
- Scope is unbounded — implies work across the entire codebase or an
  indefinite feature set.

COACHING VOICE:
- "Did you mean A or B?" — not "Have you considered...?"
- Pre-fill defaults with your best guess. User confirms with Enter.
- Max 3 questions. Each must change the end state if answered differently.
- Never question the user's chosen approach. Only clarify what they want
  it to produce.

BRIEF FIELDS:
- task: Faithful restatement of user intent. Preserve their framing,
  their references ("like claude code"), their words. The user reads this
  in the TUI and should think "yes, that's what I said." Do not add
  implementation details they didn't state.
- end_state: What the user will observe when the work is done. Describe
  what the user sees, not what the code contains. Do not mention
  libraries, internal types, message names, or file counts.
- scope: Packages/directories involved. Use user_text or user_context.
- non_scope: What's explicitly out.

OUTPUT SCHEMA (strict JSON, no prose):
{
  "verdict": "accept|coach",
  "brief": {
    "task": "string: faithful restatement preserving user's framing",
    "end_state": "string: user-observable result, no implementation details",
    "scope": ["string: packages/directories from user_text or user_context"],
    "non_scope": ["string: explicitly excluded"]
  },
  "questions": [
    { "text": "string", "options": ["string"], "default": "string" }
  ],
  "confidence": 0.0
}

EXAMPLES:

Input: "I want to be able to add relevant files from the repo directly to
the prompt; 1:1 functionality like claude code does it '@' symbol — fuzzy search."
Output:
{"verdict":"accept","brief":{"task":"Add @-triggered fuzzy file picker to the TUI prompt, matching Claude Code's @ file-mention behavior","end_state":"Typing @ in the prompt opens a fuzzy file picker; selecting a file adds it to the prompt context","scope":["internal/tui"],"non_scope":["internal/agent","internal/orchestrator"]},"questions":[],"confidence":0.85}

Input: "make it better"
Output:
{"verdict":"coach","brief":{"task":"Improve something in the codebase","end_state":"","scope":[],"non_scope":[]},"questions":[{"text":"What should be improved?","options":["internal/tui","internal/agent","internal/config","internal/orchestrator"],"default":"internal/tui"},{"text":"What kind of improvement?","options":["reduce complexity","add tests","improve error handling","fix a specific bug"],"default":"reduce complexity"}],"confidence":0.15}

Input: "fix the nil panic when config file is missing"
Output:
{"verdict":"accept","brief":{"task":"Fix nil panic that occurs when config file is missing","end_state":"Running without a config file produces a clear error instead of a nil pointer panic","scope":["internal/config"],"non_scope":[]},"questions":[],"confidence":0.92}

Input (with attached files: internal/tui/model.go, internal/tui/styles.go):
"refactor the layout logic, it's getting messy"
Output:
{"verdict":"accept","brief":{"task":"Refactor layout logic in the TUI — user says it's getting messy","end_state":"TUI layout code is cleaner and more maintainable","scope":["internal/tui"],"non_scope":[]},"questions":[],"confidence":0.88}
```

**Files:**

- `internal/config/pipeline.yaml` — replace gateway `system_prompt` section entirely

### Phase 3b: Orchestrator Lossless Forwarding

Change `buildPlannerInputFromBrief` to forward the raw user prompt as primary input with gateway's structured brief as supplementary context. Remove the `plannerInput = gwResult.PlannerQuestion` path.

When @-attached files exist, the orchestrator handles context splitting:

- **Gateway receives:** `rawPrompt` + attached file paths as a list (no content)
- **Planner receives:** `rawPrompt` + full file content + gateway brief (end_state, scope, non_scope)

This keeps the gateway fast (minimal input tokens) while giving the planner full context. The gateway sees enough to confirm scope ("user attached internal/tui files, so internal/tui is the target") without burning x-small tokens on code analysis.

**Before:**

```go
// Accept path
plannerInput = gwResult.PlannerQuestion

// Fallback path
func buildPlannerInputFromBrief(brief agent.PromptBrief) string {
    return fmt.Sprintf("How should %q be implemented such that the end state is: %s?", brief.Task, brief.EndState)
}
```

**After:**

```go
// Both accept and coach-fallback paths — raw prompt is primary
plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)

func buildPlannerInput(rawPrompt string, brief agent.PromptBrief) string {
    var b strings.Builder
    b.WriteString(rawPrompt)
    if brief.EndState != "" {
        b.WriteString("\n\nExpected outcome: ")
        b.WriteString(brief.EndState)
    }
    if len(brief.Scope) > 0 {
        b.WriteString("\nScope: ")
        b.WriteString(strings.Join(brief.Scope, ", "))
    }
    if len(brief.NonScope) > 0 {
        b.WriteString("\nOut of scope: ")
        b.WriteString(strings.Join(brief.NonScope, ", "))
    }
    return b.String()
}
```

**Note:** @-attached file content is appended by the orchestrator AFTER `buildPlannerInput`, so it flows to the planner but not back through the gateway's brief construction. This is a future-phase concern — the @-attachment feature doesn't exist yet, but the forwarding architecture should accommodate it.

**Files:**

- `internal/orchestrator/orchestrator.go` — rewrite accept path + `buildPlannerInputFromBrief`
- `internal/orchestrator/orchestrator_test.go` — update tests for new planner input format

### Phase 4: Verification

Run rewritten gateway against representative prompts and compare:

| Prompt Type           | Example                          | Check                             |
| --------------------- | -------------------------------- | --------------------------------- |
| Clear + specific      | "fix nil panic in gateway.go:45" | Accept, task ≈ user's words       |
| Clear + analogy       | "@ file picker like claude code" | Accept, no architecture in task   |
| Vague target          | "make it better"                 | Coach, scope-reducing questions   |
| Vague + specific area | "improve internal/tui"           | Accept (named area)               |
| Fantasy scope         | "build SaaS with billing"        | Coach, slice-suggesting questions |
| Bug report            | "tests fail in internal/config"  | Accept, task = user's words       |

**Verify:**

- `go test ./internal/agent ./internal/orchestrator ./internal/tui` passes
- Token usage reduction (dead fields removed + no tools = fewer input + output tokens)
- `planner_question` eliminated — planner receives raw prompt + structured context
- Coaching questions use "Did you mean?" voice, never "Have you considered?"

## Acceptance Criteria

1. `go test ./...` passes
2. `PromptBrief` has four fields: `Task`, `EndState`, `Scope`, `NonScope`
3. `GatewayResult` has no `PlannerQuestion` field
4. Gateway system prompt contains no `deliverables`, `acceptance_hints`, or `planner_question`
5. Gateway config includes tool restriction (`mcp_servers: []` or equivalent)
6. Planner receives raw user prompt + brief context, not a rewritten question
7. System prompt examples demonstrate mirror-voice (faithful restatement, no specification-language translation)
8. Coaching examples use "Did you mean A or B?" voice

## Resolved Questions

1. **Keep `scope` and `non_scope`?** Yes — cheap to fill, grounded via user_text/user_context, forwarded to planner in `buildPlannerInput`.
2. **Keep `confidence`?** Yes — useful for observability/logging. Cheap float, no calibration teaching needed.
3. **Forward `brief.Task` to planner?** No — it's a restatement of the raw prompt for TUI display. The raw prompt is the truth; forwarding both is redundant.
4. **Gateway tool access for @-attached files?** Paths only, not content. The gateway sees attached file/directory names to confirm scope. The planner gets full content. This keeps the gateway fast while giving it enough signal for accept/coach.
