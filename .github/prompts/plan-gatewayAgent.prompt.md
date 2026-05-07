# Plan: Rename intent→gateway + Evolve Gateway Agent (v2)

**TL;DR**: Mechanical rename of intent/intake to gateway, then evolve from accept/clarify/reject classifier into a skippable specification coach that never rejects, always produces a structured brief with assumptions, translates to LLM specification language, and frames planner input as a question. Meta-purpose: coach user into being engineering leader of the agent team.

## Phase 1: Mechanical Rename (intent/intake → gateway)

1. Rename `internal/agent/intent.go` → `gateway.go`, `intent_test.go` → `gateway_test.go`
2. Rename types: Intent→GatewayResult, IntentVerdict→GatewayVerdict, IntentVerdictAccept→GatewayVerdictAccept, IntentVerdictClarify→GatewayVerdictCoach, DELETE IntentVerdictReject, Recognizer→Gateway, NewRecognizer→NewGateway, Recognize→Evaluate
3. config.go: IntentConfig→GatewayConfig, Config.Intent→Config.Gateway
4. pipeline.yaml: intent: → gateway:
5. orqestra.yaml + variants: intent: → gateway:
6. orchestrator.go: PhaseIntent→PhaseGateway, Runners.Intent→Runners.Gateway
7. tui/messages.go: IntentResultMsg→GatewayResultMsg
8. tui/model.go: phase display, event routing
9. cmd/orqestra/main.go: intentRunner→gatewayRunner
10. Docs: .github/agent-instructions.md, CLAUDE.md, z_plans/plan-endgame.md

## Phase 2: Evolve Output Schema + Behavior

Two verdicts only: accept, coach. No reject.

GatewayResult: Verdict, Brief (always populated), Questions (coach only), Confidence, PlannerQuestion (always a question)
PromptBrief: Task (LLM spec language), EndState, Deliverables, Scope, NonScope, AcceptanceHints
Question: Text, Options, Default (pre-filled assumption)

Validation: accept requires Brief.Task + EndState + PlannerQuestion non-empty. Coach requires Questions non-empty + Brief partially populated.

## Phase 3: TUI — Stable Split Layout (no screen flashing)

Architecture: One stable frame with 3 zones. NO full-screen transitions during pipeline execution.

### Layout: 3-zone split

```
┌───────────────────────────────────────────┬──────────────┐
│                                           │  SIDEBAR     │
│  CONTENT (4/5 height, context-dependent)  │  mini-pipeline│
│                                           │  agent list  │
│  - gateway coaching + brief               │  with status │
│  - plan review                            │  tokens, time│
│  - agent detail stream                    │              │
│  - QA results                             │              │
│  - completion summary                     │              │
├───────────────────────────────────────────┴──────────────┤
│ INPUT (1/5 height, stable, never moves)                   │
│ > prompt / answers / slash commands                       │
│ [keys] context-sensitive legend                           │
└───────────────────────────────────────────────────────────┘
```

### Zones:

1. **Content** (top-left, ~75% width, 4/5 height): context-dependent. Flows in-place. No transitions.
2. **Sidebar** (top-right, ~25% width, 4/5 height): mini-pipeline status. ALWAYS visible. Shows agent names, states (run/done/wait/fail), elapsed, token count.
3. **Input** (bottom, full width, 1/5 height): user's stable domain. Prompt entry, question answers, slash commands. Status bar at very bottom with key legend.

### Content modes (not "screens" — no transitions):

- `ContentGatewayCoach`: shows brief + questions with defaults. Input zone has answer fields.
- `ContentPlanReview`: shows rendered spec. Input zone has [Y/N/E] keys.
- `ContentAgentDetail`: shows selected agent's stream buffer + tool calls. Input zone is status-only.
- `ContentQAResult`: shows validation report. Input zone has [F/A] keys.
- `ContentCompletion`: shows summary + files changed. Input zone has [Enter/Q] keys.

### Full-screen override:

- `[D]` from any state expands mini-pipeline sidebar into full dashboard (replaces content + sidebar). Press `[Esc]` to return to split view.
- Full dashboard is the detailed table with tokens, tok/s, context bars.

### Flow (no flashing):

1. User types prompt in input zone → Enter → content shows "evaluating..." briefly, sidebar shows `gateway ▶ run`
2. Gateway finishes with coach (2-3s later): content smoothly updates to show brief + questions. Input zone shows answer fields. Sidebar shows `gateway ● coach`.
3. User answers in input zone → Enter → sidebar shows `gateway ▶ run` again (re-eval)
4. Gateway accepts: content updates to show accepted brief summary, sidebar shows `gateway ✓`, then `planner ▶ run`
5. Planner running: content auto-follows planner stream (or stays on brief, user choice). Sidebar updates live.
6. Plan done: content shows plan review. Input zone shows [Y/N/E].
7. Workers: content auto-follows active worker (or user selects via sidebar). Sidebar shows parallel progress.

### Input zone behavior:

- Always at same terminal rows (bottom 1/5)
- Split into two sub-rows:
  1. **Input line**: prompt field / answer fields / status text (context-sensitive)
  2. **Footer**: persistent key legend, always visible

### Footer design:

- Single line at very bottom of terminal, dimmed/muted style
- Always shows: `[?] help | [D] dashboard | [1-9] agent | [N] new run | [Ctrl+C Ctrl+C] quit`
- Context-sensitive additions appear LEFT of the static keys:
  - During gateway coaching: `[Enter] confirm | [Tab] skip`
  - During plan review: `[Y] approve | [N] reject | [E] edit`
  - During QA: `[F] fix | [A] accept`
  - During agent stream: `[F] follow | [Esc] unfocus`
- `[?]` opens a full-screen help overlay (dismisses with Esc or ?)

### Agent navigation:

- `[1-9]` number keys focus content zone on that agent (by pipeline order: 1=gateway, 2=planner, 3=validator, 4+=workers)
- Focused agent: content shows that agent's stream/output history (read-only)
- Unfocused (default): content shows the current active phase (gateway coaching, plan review, etc.)
- `[Esc]` from a focused agent returns content to the current active phase

### Viewing past phases (read-only):

- User can press `[1]` to view gateway's conversation while planner is running
- This is READ-ONLY. The gateway output is frozen, just scrollable history.
- Content zone shows the gateway's brief/questions/answers as rendered at time of completion
- Sidebar still shows live pipeline progress (planner running, etc.)
- To CHANGE gateway output: press `[N]` to start a new run from scratch

### New run semantics:

- `[N]` from any state during or after a pipeline run → starts fresh
- Transitions back to the initial ScreenPrompt (full-screen, pre-pipeline)
- Optionally pre-fills the prompt with the current run's original input (user can edit or clear)
- Current run is NOT cancelled automatically — user gets confirmation if agents are still running: "Abort current run and start new? [Y/N]"
- Run artifacts remain on disk in their run directory regardless

### Runs are immutable:

- A run only moves forward: gateway → planner → validator → workers → QA → done/failed
- No step travels backward. No "undo planner, redo gateway."
- If user wants different gateway output: new run. If user rejects plan: new run (with feedback pre-filled).
- Failed/aborted runs remain in run directory for inspection.

### Slash commands (available in input zone):

- `/skip` — skip current gateway coaching (same as Tab)
- `/abort` — abort current run
- `/new` — same as [N], start new run
- `/focus <agent>` — same as number key, focus content on agent

### Sidebar behavior:

- Always visible unless full-dashboard override
- Compact: agent name (truncated), state icon, elapsed time
- Shows total token count and elapsed at bottom of sidebar
- Selectable with number keys to focus content on that agent

### State model change:

- Remove discrete Screen enum transitions for the running pipeline
- Replace with: `ContentMode` (what's in the content zone) + `InputMode` (what the input zone expects)
- ContentMode changes do NOT flash — lipgloss just re-renders the content zone
- Only ScreenPrompt (before pipeline starts) and full-dashboard override are "full screen"

### Messages:

- SubmitClarificationMsg → SubmitGatewayAnswersMsg
- Add SkipGatewayMsg (Tab from input zone at any point during gateway)
- Add FocusAgentMsg{AgentID} (select which agent stream shows in content)
- Add ToggleFullDashboardMsg (D key)
- Add NewRunMsg (N key, with confirmation if running)
- ContentMode transitions are internal (driven by orchestrator events), not user messages

## Phase 4: System Prompt Rewrite

Specification coach: translate to LLM spec language, assume deliverables/acceptance, ask "is this correct?", frame planner question, coach toward engineering-leader prompts, never reject, max 3 questions with options+defaults, 3 round loop cap.

## Phase 5: Orchestrator + Planner Contract

Pass gwResult.PlannerQuestion to planner. Skip-gateway sends raw prompt. Handle gateway→user→gateway re-eval loop.

## Files to modify

- internal/agent/intent.go → gateway.go
- internal/agent/intent_test.go → gateway_test.go
- internal/config/config.go
- internal/config/config_test.go
- internal/config/pipeline.yaml
- orqestra.yaml, orqestra.flash.yaml, orqestra.local.yaml, orqestra.anthropic.yaml
- internal/orchestrator/orchestrator.go
- internal/tui/messages.go
- internal/tui/model.go
- cmd/orqestra/main.go
- .github/agent-instructions.md
- CLAUDE.md
- z_plans/plan-endgame.md

## Verification

1. go test ./internal/agent -run TestGateway
2. go test ./internal/config
3. go test ./internal/orchestrator
4. go test ./internal/tui
5. go build ./cmd/orqestra
6. grep -rn "IntentConfig|IntentVerdict|Recognizer|NewRecognizer|PhaseIntent|IntentResultMsg" --include='\*.go' . → 0 matches
7. grep -rn "VerdictReject" --include='\*.go' . → 0 matches
8. go test ./...
