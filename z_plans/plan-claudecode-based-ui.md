# Plan: Claude Code as Universal Agent UI

## Vision

Orqestra is a powered tmux that juggles multiple Claude Code sandbox sessions. Every agent (intake, planner, validator, worker) is the **same primitive**: a sandboxed Claude Code instance with a role-specific system prompt, input files, and an expected output artifact. The user sees familiar Claude Code UI rendered in TUI tabs and can interact with any agent that requests attention.

**Core insight:** Claude Code IS the interactive UI. Orqestra orchestrates the pipeline, manages sandboxes, stages artifacts between phases, detects attention requests, and renders everything in tabs.

## Principles

1. **All agents are identical machinery** — different system prompt, different I/O files, same lifecycle
2. **Always sandboxed** — no agent bypasses the sandbox, regardless of role
3. **BEL = attention** — detect `\x07` in PTY byte stream → yellow tab marker
4. **Linear state machine** — init → stage → exec → extract → destroy. No loops, no repair cycles
5. **Reject = stop** — pipeline halts, session artifacts persist on host, user decides next step
6. **No CLAUDE.md reliance** — that file belongs to the user's target project, not Orqestra's internals
7. **JSON protocol** — structured artifacts between phases; broken JSON = broken output (canary)

## The Universal Agent Primitive

```go
// AgentSpec defines what to run in a sandbox.
type AgentSpec struct {
    Role         string            // "intake", "planner", "plan-validator", "worker-N"
    ModelRef     string            // model tier from config
    SystemPrompt string            // from pipeline.yaml, staged as file
    InputFiles   map[string][]byte // staged into sandbox before exec
    OutputFile   string            // expected artifact path (relative to /workspace)
    Command      []string          // claude CLI invocation
}
```

Every agent's lifecycle:

```
1. Provision fresh sandbox (copy workspace into container)
   - MUST run under non-root user copying the host's machine `uid`/`gid`.
2. Stage files:
   - /workspace/.orqestra/agent/system-prompt.md  ← role instructions
   - /workspace/.orqestra/agent/input/*           ← artifacts from prior phases
   - /root/.claude/settings.json                  ← NOT touched (belongs to user)
3. Launch PTY:
   claude --dangerously-skip-permissions \
     --append-system-prompt-file /workspace/.orqestra/agent/system-prompt.md
4. Stream PTY output → TUI tab (termView)
5. Detect BEL (\x07) in byte stream → mark tab yellow "⚠ Attention"
6. Forward user keystrokes when tab is focused
7. Wait for process exit (PTY EOF) and capture exit code
   - If non-zero exit code: immediately halt pipeline, explicitly mark tab as failed, refuse artifact extraction.
8. ExtractChanges() → CopyOut() expected artifact to host session directory
   - MUST explicitly verify output exists and is >0 bytes.
   - If JSON Unmarshal fails: explicitly mark tab erroneous, fail the pipeline, halt, and KEEP ALL produced artifacts directly in `.orqestra/sessions/<timestamp>-<slug>/`.
9. Destroy sandbox
```

## Attention Detection

Claude Code emits **terminal bell (`\x07`)** when it needs user attention:

- Finished thinking, waiting for next user prompt
- Asking a clarifying question
- Permission prompt (though we skip permissions)

**Detection:** Scan every `PTYOutputMsg.Data` byte slice for `\x07`. On detection:

- Set tab state to `attention: true`
- Render tab header with yellow marker: `⚠ Intake`
- Global shortcut (e.g., `Alt+!`) jumps to first yellow tab

**Clearing:** When user focuses the tab and sends input (any keystroke forwarded to PTY), clear the attention marker. Or: clear on next non-BEL output from the agent (agent resumed working).

**No `~/.claude/settings.json` manipulation.** We don't touch user config. BEL detection is passive observation of the byte stream we already have.

### PoC: Validate BEL Hypothesis

Before building on this, run a live test:

1. Spin up sandbox with local model (Qwen 3.6 via `orqestra.local.yaml`)
2. Launch `claude --dangerously-skip-permissions` interactively via `PTYSession`
3. Send a prompt, capture raw bytes, verify `\x07` appears when Claude waits for input
4. Inspect `/proc/<pid>/status` via Docker exec — confirm process is in `read()` (sleeping on stdin)

## Pipeline Flow

```
./orqestra
  │
  ├── Generate session: .orqestra/sessions/<timestamp>-<slug>/
  │
  ▼
┌──────────────────────────────────────────────────────────────┐
│ [Tab: Intake]  — interactive: proactive                       │
│                                                              │
│ System prompt: "You are the intake agent. Converse with the  │
│   user to understand their request. Ask clarifying questions. │
│   When satisfied, write structured JSON to                   │
│   /workspace/.orqestra/agent/output/01.intake.json           │
│   then exit with /exit."                                     │
│                                                              │
│ Input files: (none — user IS the input)                      │
│ Output: 01.intake.json                                       │
│ Model: intent model_ref (e.g., x-small or small)            │
└──────────────────────────────────────────────────────────────┘
  │ ExtractChanges → CopyOut 01.intake.json → session dir
  │ Destroy intake sandbox
  ▼
┌──────────────────────────────────────────────────────────────┐
│ [Tab: Planner]  — interactive: escalate                       │
│                                                              │
│ System prompt: "You are the planner. Read the intake artifact.│
│   Produce an engineering specification. Work autonomously.    │
│   You MUST enter interactive mode and ask the user for       │
│   clarification if you cannot proceed with high confidence." │
│                                                              │
│ Input files: 01.intake.json                                  │
│ Output: 02.plan.json                                         │
│ Model: x-large                                               │
└──────────────────────────────────────────────────────────────┘
  │ ExtractChanges → CopyOut 02.plan.json → session dir
  │ Destroy planner sandbox
  ▼
┌──────────────────────────────────────────────────────────────┐
│ [Tab: Validator]  — interactive: gate                         │
│                                                              │
│ System prompt: "You are the plan validator. Read the intake   │
│   and plan. Evaluate against the rubric. Then present the    │
│   plan summary to the user and ask them to:                  │
│   [A]pprove — proceed to execution                           │
│   [E]dit — save and stop so user can edit manually           │
│   [S]top — halt the pipeline                                 │
│   Write your decision to 03.validation.json and exit."       │
│                                                              │
│ Input files: 01.intake.json + 02.plan.json                   │
│ Output: 03.validation.json                                   │
│ Model: small                                                 │
│                                                              │
│ THIS IS THE HUMAN GATE                                       │
└──────────────────────────────────────────────────────────────┘
  │ ExtractChanges → CopyOut 03.validation.json → session dir
  │ Destroy validator sandbox
  │
  │ Read 03.validation.json → check verdict
  │   "approved" → continue to workers
  │   "rejected" / "stopped" → HALT pipeline. Artifacts persist.
  ▼
┌──────────────────────────────────────────────────────────────┐
│ [Tab: Worker-1..N]  — interactive: escalate                   │
│                                                              │
│ System prompt: "You are a worker. Execute the plan. Work     │
│   autonomously. Escalate to user only if stuck."             │
│                                                              │
│ Input files: 02.plan.json (+ work package if PM decomposed)  │
│ Output: changed files (ExtractChanges applied to host repo)  │
│ Model: large                                                 │
└──────────────────────────────────────────────────────────────┘
  │ ExtractChanges → CopyOut changed files → host repo
  │ Destroy worker sandbox(es)
  ▼
  Done (or optional work validation cycle)
```

## Session Directory

One host-side session per `./orqestra` run. Artifacts accumulate across phases:

```
.orqestra/sessions/2026-05-04-refactor-auth/
  ├── 01.intake.json          ← structured user intent
  ├── 02.plan.json            ← engineering specification
  ├── 03.validation.json      ← validator report + human decision
  └── (pipeline halts or workers produce repo changes)
```

On reject/stop, the session directory contains everything needed to understand what happened and resume manually.

## System Prompt Delivery

System prompts live in `pipeline.yaml` (already loaded into config structs). They are staged into the sandbox filesystem before launching Claude Code:

```
/workspace/.orqestra/agent/system-prompt.md  ← written by Orqestra before exec
```

Launch command:

```bash
claude --dangerously-skip-permissions \
  --append-system-prompt-file /workspace/.orqestra/agent/system-prompt.md
```

This preserves all native Claude Code capabilities while injecting role behavior. The project's own `CLAUDE.md` (if any) is naturally present in the workspace copy — Claude Code picks it up for project context. Orqestra doesn't write or depend on it.

## Sandbox Interface Addition

The `Sandbox` interface needs a `StageFiles` method to inject agent configuration before exec:

```go
// StageFiles copies content into the sandbox at specified container paths.
// Must be called after Provision and before Exec.
// Keys are absolute paths inside the container.
StageFiles(ctx context.Context, files map[string][]byte) error
```

This stages:

- System prompt file
- Input artifacts from prior phases
- Any agent-specific config (but NOT `~/.claude/settings.json`)

## TUI Changes

### Tab Lifecycle (replaces current state machine)

```
StateIdle → agent launches → tab created → StateRunning
         → BEL detected → tab marked yellow
         → user focuses tab → attention cleared
         → PTY exits → extract & validate → next agent or halt
```

The TUI MUST maintain a definitive understanding of the current pipeline state (Intake, Planner, Validator, Worker). It manages:

- **Tabs** (each rendering a Claude Code session sequentially)
- **Attention markers** (yellow tabs needing user input, handled one at a time on the main thread; events MUST be serialized to avoid simultaneous multi-tab collisions)
- **A command bar** (for `/abort`, `/status`, etc. — NOT for prompt entry)
- **Pipeline progress & State** (which phase is active, explicitly rendered)
- **Error Visibility**: Background goroutines MUST propagate all errors to the main thread via standard `tea.Msg` (e.g. `ErrMsg`). No silent swallowing. The TUI MUST present the precise error state, even for orchestration-level failures.

### Attention Shortcut

- `Alt+!` or dedicated key → jump to first tab with attention marker
- Visual: `⚠ Intake` (yellow) vs `● Planner` (normal) vs `✓ Worker-1` (done)

### Command Bar Role

- NOT the entry point for user prompts anymore
- Purpose: `/abort` (kill current agent), `/status`, `/logs`, `/quit`
- Placeholder: `"/command… (press Alt+! for attention)"`

## Implementation Phases

### Phase 0: PoC — BEL Detection (branch: `poc/bel-detection`)

1. Use existing `PTYSession` + `termView` infrastructure
2. Launch `claude --dangerously-skip-permissions` in sandbox
3. Observe raw byte stream for `\x07`
4. Validate detection works with local model (Qwen 3.6)
5. Measure timing: how quickly after Claude finishes does BEL appear?

### Phase 1: `Sandbox.StageFiles` Method

Add to `Sandbox` interface and implement in `DockerSandbox`:

```go
StageFiles(ctx context.Context, files map[string][]byte) error
```

Uses `docker cp` or tar-over-stdin to inject files into the running container.

### Phase 2: Universal Agent Runner (`internal/agent/runner.go`)

New package `internal/agent` with:

```go
type Runner struct { ... }

type RunConfig struct {
    Spec       AgentSpec
    Session    SessionDir   // host-side session directory
    TabIndex   int
    Send       func(tea.Msg)
}

// Run provisions sandbox, stages files, launches PTY, streams to TUI,
// waits for exit, extracts artifact, destroys sandbox.
func (r *Runner) Run(ctx context.Context, cfg RunConfig) ([]byte, error)
```

### Phase 3: Pipeline Orchestrator

Drives the sequence: intake → planner → validator → workers. Each step:

1. Builds `AgentSpec` from config + prior artifacts
2. Calls `agent.Runner.Run()`
3. On success, stores artifact in session directory
4. On failure or rejection, halts

### Phase 4: TUI Simplification

- Remove `StateIntentConfirm`, `StateConfirming`, `confirmView`, `savedView`
- All UI is tabs + command bar + status bar
- Add attention detection in PTY output handling
- Add attention marker rendering in tab headers
- Add `Alt+!` shortcut

### Phase 5: Wire Everything in `cmd/orqestra/main.go`

Replace the current `PipelineFuncs` wiring with the new universal agent approach.

## Artifact Format (JSON)

### 01.intake.json

```json
{
  "schema_version": "1",
  "goal": "Implement JWT authentication for API endpoints",
  "context": "User wants token-based auth with refresh capability",
  "constraints": ["No breaking changes", "Must support refresh tokens"],
  "acceptance_criteria": ["Protected endpoints return 401 without token"],
  "confidence": 0.95
}
```

### 02.plan.json

Same schema as current `types.Specification`:

```json
{
  "schema_version": "1",
  "goal": "...",
  "steps": ["..."],
  "constraints": ["..."],
  "acceptance": ["..."],
  "validation_commands": [...],
  "expected_artifacts": [...]
}
```

### 03.validation.json

```json
{
  "schema_version": "1",
  "verdict": "approved",
  "issues": [],
  "summary": "Plan is well-formed and executable",
  "user_decision": "approve"
}
```

On rejection:

```json
{
  "schema_version": "1",
  "verdict": "rejected",
  "issues": [...],
  "summary": "User wants postgres instead of sqlite",
  "user_decision": "stop",
  "user_feedback": "Change DB to postgres, keep everything else"
}
```

## Files to Create

| File | Purpose |
|------|---------|
| `internal/agent/runner.go` | Universal agent runner |
| `internal/agent/runner_test.go` | Unit tests |
| `internal/agent/spec.go` | AgentSpec type + builder |
| `internal/agent/session.go` | Host-side session directory management |

## Files to Modify

| File | Change |
|------|---------|
| `internal/sandbox/sandbox.go` | Add `StageFiles` to interface |
| `internal/sandbox/docker.go` | Implement `StageFiles` |
| `internal/tui/model.go` | Simplify state machine, add attention detection |
| `internal/tui/messages.go` | Add `AttentionMsg`, remove obsolete messages |
| `internal/tui/view_tabs.go` | Attention marker rendering |
| `internal/tui/view_term.go` | BEL detection in PTY output handling |
| `internal/harness/claude_cli.go` | Update `BuildPTYCommand` for system-prompt-file |
| `cmd/orqestra/main.go` | Wire new agent runner |
| `internal/config/pipeline.yaml` | Update system prompts for new contract |

## Files to Eventually Remove (follow-up)

| File | Reason |
|------|--------|
| `internal/tui/view_confirm.go` | Validator-as-gate replaces it |
| `internal/tui/view_intent.go` | Intake tab replaces it |
| `internal/tui/view_saved.go` | Session directory replaces it |
| `internal/intent/intent.go` | Old non-PTY intent recognition |
| `internal/planner/planner.go` | Replaced by universal agent runner |

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| BEL detection unreliable | PoC validates first. Fallback: idle timeout heuristic |
| Agent never exits | System explicitly marks tab as erroneous and fails pipeline. All artifacts stay in `.orqestra/sessions/<timestamp>-<slug>/` for inspection. |
| Agent writes malformed JSON | Broken JSON = broken output. Explicitly mark tab erroneous, fail the pipeline, halt, and KEEP ALL produced artifacts directly in `.orqestra/sessions/<timestamp>-<slug>/`. |
| Sandbox cold-start latency (~2-5s) | Accept for now. Future: pre-warm next sandbox during current phase |
| User ctrl+c's Claude Code | DO NOT hijack Ctrl+C. Let the agent PTY handle it natively so users can cancel prompt generations interactively. |
| Sandbox file permissions | Sandbox MUST run as a non-root user mapping host machine's uid/gid. |
| System prompt too long for CLI arg | `--append-system-prompt-file` reads from staged file, no limit |

## Test Strategy

1. **PoC branch** — live BEL detection with real Claude Code in sandbox
2. **`agent/runner_test.go`** — test with `echo`/`cat` as subprocess mock
3. **`sandbox_test.go`** — test `StageFiles` implementation
4. **`tui/model_test.go`** — test attention detection, tab lifecycle
5. **Integration** — full pipeline with local model

## Non-Goals (this plan)

- Windows support (PTY is Unix-only for now)
- Multiple concurrent workers (PM decomposition) — follow-up
- Work validation cycle — follow-up
- Resume from session directory (`--resume-session`) — follow-up
- Pre-warming next sandbox — optimization for later
