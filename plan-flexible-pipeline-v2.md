# Plan — Flexible Pipeline with Deliberation Loops and Human Gates

## Context

Orqestra's current pipeline is hardcoded: research → architect → critic → architect-2nd-pass → human plan gate → sandboxed Worker → worker self-validation → merge. The user wants a configurable pipeline where:

1. Each phase (Research, Deliberation, Execution, Validation) can be toggled on/off.
2. The Architect↔Critic deliberation can repeat 1–10 times (stepper control).
3. Four human gate positions let the user pause and chat with the previously-active agent before the next phase runs.
4. The session directory structure and restart heuristics must reflect the new multi-loop reality.

The prior draft (`plan-semi-flexible-pipeline.md`) was rejected because it: silently skipped unknown actor names (safe default treated as OK), inverted boolean logic in `HumanControl()`, modelled a sum type as a product type (10-bool flat struct for selections), and changed `Event.Gate` to `any` losing compile-time safety.

---

## Runner Consolidation

**Decision:** `claude_cli.go` (766 lines) and `sandbox_cli_runner.go` (317 lines) are 90% copypasta. Consolidate into a single `Session` interface that both implement. `RunInteractive` is the **only** correct pattern — all runner calls go through it. Both `Planner` and `Worker` run in the sandbox.

**Rationale:**
- `ClaudeCLI` and `SandboxCLIRunner` share the same interface (`CLIRunner` + `ContinuableRunner`) and implement `RunPrint`, `RunStreaming`, `RunContinue` with nearly identical code — the only difference is subprocess execution (direct vs. `sandbox-exec`).
- `RunPrint` (one-shot) and `RunStreaming` (blocking) are outdated patterns. The bidirectional `RunInteractive` pattern is the only one that supports human gate chat, real-time TUI streaming, and revision loops.
- Every agent call should use a fresh session. No session continuation for planners — plan markdown is passed as prompt context.
- The worker's self-validation uses session continuation (`RunContinue`), but this is the only case. After consolidation, the sandbox wraps the `Session` interface uniformly.

### RC-1: New unified `Session` interface

**Replace** `CLIRunner`, `ContinuableRunner`, and `InteractiveRunner` with a single `Session` interface in `internal/harness/`:

```go
// Session represents a persistent Claude CLI session with bidirectional NDJSON communication.
// Both ClaudeCLI and SandboxCLIRunner implement this interface.
type Session interface {
    // Post sends a user message as NDJSON to the session stdin.
    // The message is formatted per the verified companion-repo wire format.
    // Returns an error if the write fails (process exited, stdin closed).
    Post(msg string) error

    // Done returns the done channel. It is closed when the Claude CLI process
    // exits (either naturally or via Kill). The error value is nil on clean
    // exit, non-nil on signal termination or context cancellation.
    Done() <-chan error

    // Updates returns the updates channel. It is closed when the Claude CLI
    // process exits and parseStream finishes draining stdout.
    Updates() <-chan StreamUpdate

    // Usage returns the final token usage from the result event.
    // Returns zero values if the result event has not yet been parsed.
    Usage() TokenUsage

    // ResultError reports whether the session ended with is_error:true
    // in the result event. Returns false if the result event has not yet
    // been parsed.
    ResultError() bool

    // SessionID returns the session ID extracted from the stream events.
    SessionID() string

    // PlanPath returns the plan file path extracted from the result event.
    // Empty if no plan file was produced.
    PlanPath() string

    // Kill terminates the Claude CLI process. The session's Done and Updates
    // channels will close once the process exits.
    Kill() error
}
```

**`Session` is created via a factory function** that returns `(Session, error)`:

```go
// RunSession starts a Claude CLI session with bidirectional NDJSON streaming.
// It is the only runner method — replaces RunPrint, RunStreaming, RunContinue.
func (c *ClaudeCLI) RunSession(ctx context.Context, prompt, systemPrompt string,
    streamUpdates chan<- StreamUpdate) (Session, error)
```

**`SandboxCLIRunner` implements the same interface:**

```go
func (r *SandboxCLIRunner) RunSession(ctx context.Context, prompt, systemPrompt string,
    streamUpdates chan<- StreamUpdate) (Session, error)
```

The sandbox runner wraps the same `claude` process with `sandbox-exec` before the bidirectional session starts. The `Session` interface is identical.

### RC-2: Remove old runner methods

**Delete from `ClaudeCLI`:**
- `RunPrint` — obsolete. All calls use `RunSession` (bidirectional).
- `RunStreaming` — obsolete. All calls use `RunSession` (bidirectional).
- `RunContinue` — obsolete. Session continuation is done by creating a new session with the prompt + previous plan as context.

**Delete from `SandboxCLIRunner`:**
- `RunPrint` — obsolete.
- `RunStreaming` — obsolete.
- `RunContinue` — obsolete.

**Delete `CLIRunner` interface** — replaced by `Session` interface.
**Delete `ContinuableRunner` interface** — replaced by `Session` interface.
**Delete `InteractiveRunner` interface** — replaced by `Session` interface.

### RC-3: `Planner` uses `Session` factory

**Update `agent.Planner`:**

```go
type Planner struct {
    runner   *ClaudeCLI      // or *SandboxCLIRunner — both have RunSession
    system   string
    workDir  string          // optional working directory for subprocess
}

func NewPlanner(runner *ClaudeCLI, system string, opts ...PlannerOption) *Planner {
    return &Planner{runner: runner, system: system}
}

func (p *Planner) Run(ctx context.Context, prompt string, events chan<- StreamUpdate) (PlanResult, error) {
    sess, err := p.runner.RunSession(ctx, prompt, p.system, events)
    if err != nil {
        return PlanResult{}, fmt.Errorf("planner run: %w", err)
    }
    defer sess.Kill()
    // ... drain updates, wait for Done, extract result
}
```

**`Planner.Continue` is removed.** Every agent call uses a fresh session. The plan markdown is passed as prompt context. For the worker's self-validation, the orchestrator creates a new session with the validation prompt + worker output as context.

### RC-4: `Runners` struct simplification

**Current state (engine.go:66-71):**

```go
type Runners struct {
    Researcher harness.ContinuableRunner
    Architect  harness.ContinuableRunner
    Critic     harness.ContinuableRunner
    Worker     harness.ContinuableRunner
}
```

**New state:**

```go
type Runners struct {
    Researcher *ClaudeCLI    // has RunSession
    Architect  *ClaudeCLI    // has RunSession
    Critic     *ClaudeCLI    // has RunSession
    Worker     *SandboxCLIRunner // runs in sandbox with worktree
}
```

**Every runner has `RunSession(ctx, prompt, systemPrompt, updates) (Session, error)`.** The orchestrator calls `RunSession` for every agent invocation. No type assertions, no interface switching.

### RC-5: Worker self-validation via fresh session

**Current state:** Worker self-validation uses `RunContinue` to resume the worker session.

**New state:** Validation uses a fresh `RunSession` call. The prompt includes the worker's output and a validation instruction. The orchestrator passes the worker's session ID as context in the prompt (not via session continuation).

```go
validationPrompt := agent.WorkerValidationPrompt(
    retryBudget,
    workResult.Output,
    workResult.SessionID, // passed as context, not session continuation
)
valSess, valErr := e.Runners.Worker.RunSession(ctx, validationPrompt, "", valUpdates)
```

### RC-6: `runPlanner` / `runRunnerStreaming` / `runRunnerContinue` helpers removed

**Delete from `engine.go`:**
- `runPlanner()` — replaced by `planner.Run()` which calls `RunSession`
- `runRunnerStreaming()` — replaced by `runner.RunSession()`
- `runRunnerContinue()` — replaced by `runner.RunSession()` (fresh session)

**The orchestrator's extracted functions call `RunSession` directly.** No indirection layer needed.

### RC-7: Copypasta elimination

**`parseStreamLines` → `parseStream`:** The sandbox runner's `parseStreamLines` (line 207) is functionally identical to `claude_cli.go`'s `parseStream` (line 388). Consolidate to a single package-level `parseStream` in `harness/`.

**`extractJSONUsage`, `extractStreamUsage`, `extractStreamSessionID`, `extractStreamResult`:** These sandbox-specific extraction functions are replaced by `Session` methods (`Usage()`, `SessionID()`, `PlanPath()`, `ResultError()`). Delete from `sandbox_cli_runner.go`.

**`run()` and `runParsed` in `SandboxCLIRunner`:** Consolidate process creation logic. Both `ClaudeCLI` and `SandboxCLIRunner` share a common `startSession()` helper that builds the command, pipes stdin/stdout, and starts the process. The sandbox runner wraps with `sandbox.New().Wrap()`.

**Result:** `claude_cli.go` shrinks from 766 to ~200 lines (interface, options, `RunSession`). `sandbox_cli_runner.go` shrinks from 317 to ~100 lines (sandbox-specific process wrapper). Shared code moves to `harness/session.go`.

### RC-8: `SandboxCLIRunner` wraps `Session` creation

The sandbox runner doesn't need its own `RunSession` — it wraps the process creation:

```go
func (r *SandboxCLIRunner) RunSession(ctx context.Context, prompt, systemPrompt string,
    updates chan<- StreamUpdate) (Session, error) {
    // 1. Build CLI args (same as ClaudeCLI.RunSession)
    // 2. Create sandbox: sb, err := sandbox.New(...)
    // 3. Create command: cmd := exec.CommandContext(ctx, "claude", args...)
    // 4. Wrap with sandbox: sb.Wrap(cmd)
    // 5. Pipe stdin/stdout
    // 6. Start process
    // 7. Return *sandboxSession{Session, sandbox: sb}
}
```

**`sandboxSession` embeds `*interactiveSession`** and adds sandbox cleanup on `Kill()`:

```go
type sandboxSession struct {
    *interactiveSession // the shared Session implementation
    sb                  *sandbox.Sandbox
}

func (s *sandboxSession) Kill() error {
    s.sb.Close()
    return s.interactiveSession.Kill()
}
```

### Summary: Runner consolidation impact

| Before | After |
|--------|-------|
| `CLIRunner` (RunPrint, RunStreaming) | `Session` (Post, Done, Updates, Kill) |
| `ContinuableRunner` (RunContinue) | `Session` (fresh session + prompt context) |
| `InteractiveRunner` (RunInteractive) | `Session` (same interface) |
| `ClaudeCLI.RunStreaming` (130 lines) | `ClaudeCLI.RunSession` → shared `startSession` |
| `SandboxCLIRunner.RunStreaming` (80 lines) | `SandboxCLIRunner.RunSession` → shared `startSession` + sandbox wrap |
| `agent.Planner` uses `ContinuableRunner` | `agent.Planner` uses `*ClaudeCLI` with `RunSession` |
| `Runners` struct: `ContinuableRunner` | `Runners` struct: concrete types with `RunSession` |
| `runPlanner`, `runRunnerStreaming`, `runRunnerContinue` helpers | Direct `RunSession` calls |
| `claude_cli.go`: 766 lines | `claude_cli.go`: ~200 lines |
| `sandbox_cli_runner.go`: 317 lines | `sandbox_cli_runner.go`: ~100 lines |
| New `harness/session.go`: shared session code | New file |

---

## TUI-Only: Remove Headless Mode and Non-TUI CLI Paths

**Decision:** Orqestra is an interactive TUI tool. Remove ALL headless mode, `--plan` mode, `--no-execute` mode, and non-TUI CLI paths. The app requires a terminal.

**Rationale:**
- Human gates require user interaction (chat, plan approval). Headless mode bypasses all gates, making the human gate feature meaningless.
- The `--prompt` + `--auto-approve` CLI path and `RunHeadless`/`RunHeadlessPlanOnly` TUI functions are a separate execution path that doesn't use the TUI model at all.
- The `--plan` subcommand and `runPlanOnly()` are non-TUI paths that duplicate the prompt→plan flow without the TUI.
- The `Input.AutoApprove` field served two purposes: (1) skip plan approval gate in headless mode, (2) control human gate degradation. This dual purpose is a design smell.
- Headless degradation logic in `runHumanGate` adds a third code path that must be tested and maintained.
- Orqestra is a macOS-first interactive TUI tool — non-interactive paths create complexity that conflicts with the new human gate system.

### HM-1: CLI flags (`cmd/orqestra/main.go`)

**Current state (lines 55-74):**

```go
var (
    configPath  string
    jsonOutput  bool
    noExecute   bool
    planPath    string
    promptFlag  string          // --prompt
    autoApprove bool            // --auto-approve
    autoReject  bool            // --auto-reject
    autoInit    bool            // --auto-init
)
```

**Changes:**

| Flag | Action |
|------|--------|
| `--prompt` | **Remove** — no replacement |
| `--auto-approve` | **Remove** — no replacement |
| `--auto-reject` | **Remove** — no replacement |
| `--auto-init` | **Remove** — no replacement |
| `--plan` | **Remove** — no replacement |
| `--no-execute` | **Remove** — no replacement |
| `--json` | **Remove** — no replacement |
| `--config` | **Keep** — still needed for TUI config loading |

**Removal scope in `main.go`:**

1. **Delete variables** (lines 60-63): `promptFlag`, `autoApprove`, `autoReject`, `autoInit`
2. **Delete variables**: `planPath`, `noExecute`, `jsonOutput`
3. **Delete flag definitions**: all flags listed above except `--config`
4. **Delete `isHeadless` determination** (line 100): `isHeadless := promptFlag != "" || planPath != ""`
5. **Delete headless branch in project root check** (lines 103-127): Remove the `if isHeadless` / `else` branch. All runs go through the TUI path (which requires a terminal). The `ensureProjectRoot` function no longer needs `isHeadless` parameter.
6. **Delete `RunHeadlessPlanOnly` call** (lines 155-177): No replacement.
7. **Delete `RunHeadless` call** (lines 179-196): No replacement.
8. **Delete `--prompt` validation error** (lines 197-200): No longer needed.
9. **Delete `--plan` mode** (lines 202-287): Entire block removed. No replacement.
10. **Delete `runPlanOnly()` function** (lines 494+): Entire function removed.
11. **Delete `runValidateOnly()` function** (lines 534+): Entire function removed.
12. **Delete `runExecOnly()` function** (lines 553+): Entire function removed.
13. **Keep `runInitCommand()`** — `orqestra init` subcommand stays (needed for project initialization).
14. **Update `ensureProjectRoot`** (lines 337-369): Remove `isHeadless` parameter. The function always prompts interactively (or errors if not a terminal, which is handled by the TUI check).

**Result:** `main.go` shrinks by approximately 150 lines. Only `--config` flag and `runInitCommand()` remain from the CLI entry point. All pipeline execution goes through the TUI.

### HM-2: TUI headless functions (`internal/tui/tui.go`)

**Current state (lines 38-77):**

```go
func RunHeadless(ctx context.Context, engine *orchestrator.Engine, prompt string) error {
    channels := engine.Start(ctx, orchestrator.Input{
        Prompt:      prompt,
        AutoApprove: true,
    })
    // ...
}

func RunHeadlessPlanOnly(ctx context.Context, engine *orchestrator.Engine, prompt string) (orchestrator.Result, error) {
    channels := engine.Start(ctx, orchestrator.Input{
        Prompt:      prompt,
        AutoApprove: true,
        NoExecute:   true,
    })
    // ...
}
```

**Changes:**

1. **Delete `RunHeadless`** (lines 38-51): Entire function removed.
2. **Delete `RunHeadlessPlanOnly`** (lines 53-77): Entire function removed.

### HM-3: Engine `Input` struct (`internal/orchestrator/engine.go`)

**Current state (lines 29-36):**

```go
type Input struct {
    Prompt      string
    AutoApprove bool      // <-- REMOVE
    PlanFile    string     // <-- REMOVE (--plan mode removed)
    NoExecute   bool       // <-- REMOVE (--no-execute removed)
    RestartFrom RestartInput
}
```

**Changes:**

1. **Delete `AutoApprove bool` field** from `Input` struct.
2. **Delete `PlanFile string` field** — no `--plan` mode.
3. **Delete `NoExecute bool` field** — no `--no-execute` flag.
4. **Delete `Input.Interactive bool` field** — no longer needed. All runs are interactive (TUI-only).

**Result:** `Input` struct shrinks to 2 fields:

```go
type Input struct {
    Prompt      string
    RestartFrom RestartInput
}
```

All references to `input.AutoApprove`, `input.PlanFile`, `input.NoExecute`, and `input.Interactive` are removed.

### HM-4: Engine `run()` logic (`internal/orchestrator/engine.go`)

**Current state:** The `run()` function has multiple `input.AutoApprove` checks:

| Line | Check | Action |
|------|-------|--------|
| 309 | `logger.Info(..., "auto_approve", input.AutoApprove, ...)` | Remove log field |
| 849 | `if !input.AutoApprove {` | **Delete the entire gate loop** (lines 850-1161). It is replaced by a `runHumanGate` call at `GateBeforeWorker` when `HumanGates.Active(GateBeforeWorker)`. |
| 1017-1027 | `if decision.Comment != "" || !decision.AutoApprove { continue }` | **Keep the logic** — `Decision.AutoApprove` is a TUI-specific field (set when user confirms edit without comment). This is not headless-specific. |

**Changes:**

1. **Line 309:** Remove `"auto_approve", input.AutoApprove` from the log call.
2. **Line 849:** Remove the `if !input.AutoApprove {` wrapper. The plan approval gate loop (lines 850-1161) is replaced entirely by a `runHumanGate` call at `GateBeforeWorker` when `HumanGates.Active(GateBeforeWorker)`. The gate runs unconditionally when the position is configured.
3. **Line 159:** `Run()` currently sets `AutoApprove: true` when wrapping `Start()`. Remove this field from the call.

### HM-5: Decision `AutoApprove` field (`internal/orchestrator/events.go`)

**Current state (lines 84-96):**

```go
type Decision struct {
    Type          DecisionType
    EditedContent string
    Comment       string
    AutoApprove   bool   // <-- KEEP — TUI-specific, not headless-specific
}
```

**Decision:** **Keep `Decision.AutoApprove`**. This field is set by the TUI (not headless mode) when the user confirms an edit without adding a comment. It controls whether the gate loop re-shows the plan after an edit. It's a TUI UX feature, not a headless feature.

### HM-6: Human gates headless logic

**Current state:** The `runHumanGate` function (in the plan's WP2 section) has headless-specific logic:

```go
func runHumanGate(
    ctx context.Context,
    emit func(Event),
    decisions <-chan Decision,
    interactive bool,     // <-- REMOVE
    autoApprove bool,     // <-- REMOVE
    req HumanChatGateRequest,
) (*Decision, error) {
    if !interactive && !autoApprove {
        // Error: headless without TUI
        return nil, fmt.Errorf(...)
    }
    if !interactive && autoApprove {
        // Skip: headless with auto-approve
        return &Decision{Type: DecisionApprove}, nil
    }
    // Normal: wait for user action
    // ...
}
```

**Changes:**

1. **Remove `interactive bool` parameter** from `runHumanGate`.
2. **Remove `autoApprove bool` parameter** from `runHumanGate`.
3. **Remove both headless degradation branches** (error on `!interactive && !autoApprove`, skip on `!interactive && autoApprove`).
4. **Simplify to:** always emit `EventHumanGate` and wait on `decisions` channel. The function signature becomes:

```go
func runHumanGate(
    ctx context.Context,
    emit func(Event),
    decisions <-chan Decision,
    req HumanChatGateRequest,
) (*Decision, error) {
    emit(Event{Type: EventHumanGate, HumanGate: req})
    select {
    case d := <-decisions:
        return &d, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

5. **Remove `input.Interactive` from all `runHumanGate` call sites** in `run()`. The calls become:

```go
d, err := runHumanGate(ctx, emit, decisions, HumanChatGateRequest{...})
```

### HM-7: README (`README.md`)

**Current state (lines 133, 319, 323-325):**

```markdown
Headless run for scripting or E2E testing:
./bin/orqestra --prompt "..." --auto-approve --auto-init
```

**Changes:**

1. **Remove the "Headless run" section** from README (around line 133).
2. **Remove `--auto-approve` flag** from the CLI reference table.
3. **Remove `--auto-reject` flag** from the CLI reference table.
4. **Remove `--auto-init` flag** from the CLI reference table.
5. **Remove `--prompt` flag** from the CLI reference table.
6. **Remove `--plan` flag** from the CLI reference table.
7. **Remove `--no-execute` flag** from the CLI reference table.
8. **Remove `--json` flag** from the CLI reference table.
9. **Keep `--config`** in the CLI reference table.
10. **Keep `orqestra init`** subcommand documentation.
11. **Remove `orqestra plan`, `orqestra validate`, `orqestra exec`** subcommand documentation.

### HM-8: Copilot instructions (`.github/copilot-instructions.md`)

**Current state (lines 40, 175, 181):**

```markdown
- Headless smoke: `./bin/orqestra --prompt "..." --auto-approve --config orqestra.yaml`.
- Claude CLI logs under `~/.claude/` are often the ground truth when headless or TUI runs look silent.
## Debugging E2E And Headless Runs Via Claude CLI Logs
```

**Changes:**

1. **Remove headless smoke test** from commands section (line 40). Replace with:
   ```markdown
   - TUI smoke: `make run` (interactive terminal required).
   ```
2. **Remove "headless or"** from line 175: change to "when TUI runs look silent" (remove "headless or").
3. **Rename section** from "Debugging E2E And Headless Runs Via Claude CLI Logs" to "Debugging Runs Via Claude CLI Logs" (remove "E2E And Headless").

### HM-9: Benchmark script (`scripts/benchmark-prompts.sh`)

**Current state (lines 5, 149-151):**

```bash
# Runs each prompt headless with --auto-reject (plan-only, no worker execution)
# ...
PROMPTS+=("Implement a --verbose flag that causes the CLI to print the researcher facts, architect plan, and critic report to stderr when running in headless mode (--prompt + --auto-approve).")
```

**Changes:**

1. **Update comment** (line 5): Change from "headless with --auto-reject" to "TUI runs via `make run`".
2. **Remove the --verbose headless test case** (lines 149-151): This test case describes a feature that requires headless mode. Remove it from the benchmark.

### HM-10: Test files

**Current state:** Tests that exercise headless mode:

| File | Test | Action |
|------|------|--------|
| `internal/orchestrator/engine_test.go:339-361` | `TestEngine_HeadlessAutoApprove` | **Delete** — no longer applicable |
| `internal/orchestrator/engine_test.go:1197-1250` | `TestGate_DecisionEditAutoApprove_ProceedsToWorker` | **Keep** — tests `Decision.AutoApprove` which is TUI-specific |
| `internal/tui/screen_pipeline_test.go:168-170` | `TestEditConfirm_YesWithComment` | **Keep** — tests TUI edit confirm |
| `internal/tui/screen_pipeline_test.go:192-194` | `TestEditConfirm_YesNoComment` | **Keep** — tests TUI edit confirm |
| `plan-flexible-pipeline-v2.md` (WP6) | `TestEngine_Phase_HeadlessNoAutoApprove` | **Delete** — no longer applicable |
| `plan-flexible-pipeline-v2.md` (WP6) | `TestEngine_Phase_HeadlessAutoApprove` | **Delete** — no longer applicable |

### HM-11: Impact on existing plan sections

The headless mode and non-TUI path removal affects several sections of the existing plan:

1. **WP0 — `Input` struct:** Remove `AutoApprove bool`, `PlanFile string`, `NoExecute bool`, and `Interactive bool` fields. The `Input` struct becomes:
   ```go
   type Input struct {
       Prompt      string
       RestartFrom RestartInput
   }
   ```

2. **WP0 — `run()` setup validation:** The `Interactive` field was used to detect headless mode. Without headless mode, `Interactive` is removed and the `runHumanGate` function no longer receives it.

3. **WP2 — `runHumanGate`:** Simplified as described in HM-6. The function signature loses 2 parameters and 2 code branches.

4. **WP2 — Human gate degradation matrix:** Remove the headless degradation rows. The matrix becomes:
   - TUI mode: wait for user action on `decisions` channel.
   - Context cancellation: return `ctx.Err()`.

5. **Known Risks:** Remove the headless-related risk entry about `--prompt --auto-approve` headless runs.

### Summary of TUI-only removal

| Area | Lines removed | Lines added | Net change |
|------|--------------|-------------|------------|
| `cmd/orqestra/main.go` | ~150 | 0 | -150 |
| `internal/tui/tui.go` | ~40 | 0 | -40 |
| `internal/orchestrator/engine.go` | ~5 | 0 | -5 |
| `internal/orchestrator/events.go` | 0 | 0 | 0 |
| `README.md` | ~15 | 0 | -15 |
| `.github/copilot-instructions.md` | ~3 | ~3 | 0 |
| `scripts/benchmark-prompts.sh` | ~3 | ~1 | -2 |
| Tests | ~60 | 0 | -60 |
| **Total** | **~276** | **~4** | **-272** |

The removal simplifies the codebase by eliminating all parallel non-TUI execution paths that conflict with the human gate system. All runs are interactive TUI runs, and the `Input` struct carries only `Prompt` and `RestartFrom`.

---

## Git Micro-Repo Removal

**Decision:** Remove the `plan-history/` git micro-repo entirely. Replace it with numbered plan files (`plan-v1.md`, `plan-v2.md`, ...) and a multi-section Markdown dialog log per gate directory.

**Rationale:**
- Plan revisions during deliberation and gate interactions are tracked by numbered files — the highest-numbered file is the current plan.
- The multi-section Markdown dialog captures human-agent interactions (gate edits, comments, chat) with plan file references. It replaces `dialog.md`.
- The TUI plan-history viewer (Ctrl+Y) is removed. Plan history is visible as files on disk.
- Removing git eliminates complexity: no `git init`, no `git add`/`commit`, no `Diff()`, `DiffPlain()`, `HeadCommitHash()`, `Revisions()`, `ContentAt()`, `DiffBetween()`.
- The Markdown dialog interleaves conversation with plan snapshots — human-readable, machine-parseable, and self-contained per gate directory.

### GR-1: Delete `internal/plan/gitrepo.go` and `internal/plan/gitrepo_history.go`

**Current state:** `gitrepo.go` (243 lines) implements `NewGitRepo()`, `Commit()`, `CommitPlan()`, `CommitDialog()`, `CommitPlanAndDialog()`, `Diff()`, `DiffPlain()`, `HasHistory()`, `Log()`, `Head()`, `HeadCommitHash()`. `gitrepo_history.go` (130 lines) implements `OpenGitRepo()`, `Revisions()`, `ContentAt()`, `DiffBetween()`, `parseRevLine()`.

**Action:** Delete both files. Remove the `plan.DialogEntry` and `plan.Revision` types (they were only used by the git repo).

### GR-2: Remove `planRepo` from `engine.go` (~30 call sites)

**Current state:** `engine.go` declares `var planRepo *plan.GitRepo` at line 407. It is created at line 410 (`plan.NewGitRepo(session.Path)`) and used in ~30 locations for:
- Initial plan commit (line 422): `planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{...})`
- Architect pass commit (line 632): `planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{...})`
- Critic dialog commit (line 744): `planRepo.CommitDialog(plan.DialogEntry{...})`
- Critic revision commit (line 821): `planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{...})`
- Critic dialog commit (line 834): `planRepo.CommitDialog(plan.DialogEntry{...})`
- Gate diff (line 853): `planRepo.Diff()`
- Gate history (line 862): `planRepo.Dir()`, `planRepo.HeadCommitHash()`
- User edit commit (line 902): `planRepo.CommitPlan(edited, "user: manual edit")`
- Gate diff after edit (line 912): `planRepo.DiffPlain(lastArchitectHash)`
- Gate dialog commits (lines 915, 1006, 1039, 1141): `planRepo.CommitDialog(...)`
- Gate revision commits (lines 994, 1128): `planRepo.CommitPlanAndDialog(...)`
- Gate revision hash (lines 1000, 1134): `planRepo.HeadCommitHash()`

**Action:** Remove all `planRepo` declarations, creations, and call sites. Replace plan commits with numbered file writes (`plan-v1.md`, `plan-v2.md`, ...). Remove `PlanDiff`, `PlanHistoryDir`, `PlanHistoryHeadSHA` from gate request data. Add `PlanFilePath` (path to highest-numbered `plan-v*.md` on disk) to gate request data for external editor support.

### GR-3: Plan revision files — numbered naming

**Decision:** Each plan revision writes a new file: `plan-v1.md` (initial), `plan-v2.md` (first revision), etc. The highest-numbered file is the current plan.

**Tracking:** A `planRevisionCounter` integer tracks the current revision number. Each architect pass (initial or revision) increments the counter and writes `plan-v<N>.md`.

**Location:** Plan files are written to the deliberation loop directory (`deliberation/loop_NN/plan-v<N>.md`) and to the gate directory (`gate_before_worker/plan-v<N>.md`).

### GR-4: Multi-section Markdown dialog format

**Decision:** Replace `dialog.md` with a multi-section Markdown dialog file (`dialog.md`) in each gate directory. The Markdown file captures human-agent interactions for that gate, interleaving conversation with plan snapshots.

**Format:**

```markdown
## Human
Edit plan to add X section

## Architect (plan-v2.md, 1234 tokens)
Updated plan with X. See plan-v2.md.

---
### plan-v2.md excerpt
[short excerpt or diff of the plan change]
```

**Structure:**
- `## Human` — human messages (edits, comments, approve/cancel)
- `## Architect` — architect responses, referencing plan file and token count
- `---` — separator between turns
- `### plan-v<N>.md excerpt` — short excerpt or diff of the plan change after each architect turn

**Plan changes:** When the architect revises the plan in response to a human comment, the dialog entry references the new plan file and includes a short excerpt or diff. The full plan content is read from `plan-v<N>.md` on disk.

**Location:** Each gate directory gets its own `dialog.md`:
- `gate_before_deliberation/dialog.md`
- `gate_before_worker/dialog.md`
- `gate_before_validator/dialog.md`
- `gate_end_of_pipeline/dialog.md`

### GR-5: Remove TUI plan history viewer

**Decision:** Remove the entire plan-history viewer from the TUI. Plan revisions are visible as numbered files on disk; dialog history is in Markdown files.

**Files to delete:**
- `internal/tui/plan_history_loader.go`
- `internal/tui/screen_plan_history.go`
- `internal/tui/screen_plan_history_test.go`
- `internal/tui/plan_history_model_test.go`

**Types to remove from `model.go`:**
- `StatePlanHistoryDetail` from `AppState` enum
- `ContentPlanHistory` from `ContentMode` enum
- `planHistoryScreen PlanHistoryScreen` field from `Model` struct
- `planHistoryVisible()` method
- `handlePlanHistoryKey()` method
- All `planHistoryScreen` update/view/layout calls

**Types to remove from `messages.go`:**
- `OpenPlanHistoryIntent`
- `ClosePlanHistoryIntent`
- `RevertPlanIntent`

**Changes to `screen_run_detail_keys.go`:**
- Remove Ctrl+Y handler that emits `OpenPlanHistoryIntent{HistoryDir: filepath.Join(s.detail.Path, "plan-history")}`

**Changes to `engine.go` GateRequest → HumanChatGateRequest:**
- Remove `PlanDiff`, `PlanHistoryDir`, `PlanHistoryHeadSHA` fields
- Remove `PlanDiff` from gate emission

### GR-6: Remove backward compatibility for old-format runs

**Decision:** Remove the old-layout fallback from `AnalyzeRunCompleteness` and `copyCompletedArtifacts`. Old-format runs (without `run_config.json`) are no longer restartable.

**Changes to `AnalyzeRunCompleteness` (session.go):**
- Remove the `else` branch that falls back to flat-layout analysis
- Remove `FirstMissingAgent` field from `RunCompleteness`
- Always use new-layout logic (read `run_config.json`, check subdirectories)

**Changes to `copyCompletedArtifacts` (engine.go):**
- Remove the `else` branch that falls back to flat-layout copy
- Always use new-layout logic (read `run_config.json`, copy from subdirectories)

**Impact:** `FirstMissingAgent` is removed from `RunCompleteness`. Old runs show as incomplete in the TUI (no restart button action).

---

## Critique Resolution: High-Severity Blockers

All 4 high-severity blockers from the code review are resolved below with concrete, code-grounded fixes.

### BR-1: run() extraction scope — explicit function signatures and data-passing strategy

**Evidence verified:** `engine.go` is exactly 1581 lines. The `run()` function at lines 253–1455 is 1203 lines. Variables shared across phase blocks:

| Variable | Declared at | Used by | Updated by |
|----------|-------------|---------|------------|
| `finalPlanMarkdown` | line 393 | planning, critic, gate loop, worker | lines 420, 630, 818, 897, 991, 1125 |
| `workResult` | — | worker, validation, merge | line 1212 |
| `wt` | line 1195 | worker execution, merge | lines 1200–1209, 1390–1440 |
| `researchSessionID` | line 485 | researcher log copy | line 493 |
| `draftMarkdownForPlanning` | line 396 | architect prompt | lines 540, 544 |
| `architectAttempt` | line 313 | architect logging | lines 552, 753, 921, 1029 |
| `criticReportMarkdown` | line 395 | gate emission, plan revision | lines 733, 875 |
| `finalPlanWarnings` | line 394 | gate emission, revisions | lines 629, 819, 898, 992, 1126 |

Note: `planSessionID` is removed — both architect and critic always run in fresh sessions. The plan markdown is passed as prompt context.

**Fix — explicit signatures and result structs:**

The plan extracts `run()` into 4 files. Each extracted function has an explicit signature:

```go
// engine_deliberation.go

// deliberationResult carries output from runDeliberation back to run().
type deliberationResult struct {
    planMarkdown        string
    criticReport        string
    warnings            []string
    planRevisionCount   int  // number of plan-v?.md files written during deliberation
}

// runDeliberation executes the architect↔critic deliberation loop.
// It does NOT include the plan approval gate (that is a configurable HumanGate at GateBeforeWorker, handled in run()).
//
// Session management:
//   - Each architect pass (initial + revisions) uses a fresh Planner session.
//   - Each critic pass uses a fresh Planner session.
//   - Plan markdown is passed as prompt context to every agent call.
//   - No session continuation (no planSessionID).
//
// Artifact paths: all writes go to session.LoopDir(loop)/
//   - architect_initial_meta.json (loop 1 only)
//   - architect_initial_session.jsonl (loop 1 only)
//   - critic_meta.json, critic_report.md, critic_session.jsonl (every loop)
//   - architect_revision_meta.json, architect_revision_session.jsonl (every loop after critic)
//   - plan-v<N>.md (every loop, the latest plan file after this loop)
func (e *Engine) runDeliberation(
    ctx context.Context,
    setup PipelineSetup,
    session SessionDir,
    input Input,
    emit func(Event),
    decisions <-chan Decision,
    stream *streamCapture,
    streamOut chan<- harness.StreamUpdate,
    draftMarkdown string, // input: researcher output or prompt
) (deliberationResult, error)
```

```go
// engine_phases.go

// runResearch executes the researcher phase.
// Returns (draftMarkdown, researchSessionID, err).
// If setup.Research is false, emits EventAgentSkipped and returns ("", "", nil).
func (e *Engine) runResearch(
    ctx context.Context,
    setup PipelineSetup,
    session SessionDir,
    input Input,
    emit func(Event),
    stream *streamCapture,
    streamOut chan<- harness.StreamUpdate,
) (draftMarkdown string, researchSessionID string, err error)

// runExecution executes the worker phase.
// Returns (workResult, workerSessionID, err).
// If setup.Execution is false, emits EventAgentSkipped and returns (nil, "", nil).
func (e *Engine) runExecution(
    ctx context.Context,
    setup PipelineSetup,
    session SessionDir,
    input Input,
    emit func(Event),
    stream *streamCapture,
    streamOut chan<- harness.StreamUpdate,
) (workResult harness.RunResult, workerSessionID string, err error)

// runValidation executes the worker self-validation phase.
// Returns (validationOutput, lastSessionID, err).
// If setup.Validation is false or setup.Execution is false, emits EventAgentSkipped and returns ("", "", nil).
func (e *Engine) runValidation(
    ctx context.Context,
    setup PipelineSetup,
    session SessionDir,
    input Input,
    emit func(Event),
    stream *streamCapture,
    streamOut chan<- harness.StreamUpdate,
    workerSessionID string, // from runExecution
) (validationOutput string, lastSessionID string, err error)
```

```go
// engine_restart.go

// restartState carries state loaded from a previous run during restart.
type restartState struct {
    isRestart                bool
    restartSrc               string
    finalPlanMarkdown        string
    criticReportMarkdown     string
    draftMarkdownForPlanning string
    skipResearch             bool
    skipPlanning             bool // skip to plan gate
}

// applyRestartSkip loads artifacts from a previous run and returns restart state.
// The caller uses restartState to decide which phases to skip.
func applyRestartSkip(
    ctx context.Context,
    session SessionDir,
    input Input,
    restartSrc string,
    emit func(Event),
) (restartState, error)
```

**Execution order for extraction (single commit):**
1. Move `copyLog`, `logAgentEvent`, `logClaudeSession`, `logClaudeSessionPre` from `run()` closures
   (engine.go:261-353) to package-level functions in `engine.go`. These are called by every
   extracted function and must not be local to `run()`.
2. Add result structs and helper types (`deliberationResult`, `restartState`)
3. Add `SessionDir` sub-dir helpers (WP1)
4. Add `writeArtifactIn` / `writeArtifactJSONIn` overloads (WP1)
5. Extract `runDeliberation` to `engine_deliberation.go`
6. Extract `runResearch`, `runExecution`, `runValidation` to `engine_phases.go`
7. Extract `applyRestartSkip` to `engine_restart.go`
8. Rewrite `run()` to call extracted functions (removes goto)
9. Run `go test -race ./internal/orchestrator/` before committing

**File size estimate:**
- `engine.go`: ~400 lines (Start, Run, run entry point, helpers, copyCompletedArtifacts)
- `engine_deliberation.go`: ~350 lines (runDeliberation + deliberation loop logic)
- `engine_phases.go`: ~350 lines (runResearch, runExecution, runValidation)
- `engine_restart.go`: ~150 lines (applyRestartSkip + restart helpers)

All files are well under the 500-line threshold.

### BR-2: Session directory restructuring — artifact path audit

**Evidence verified:** Current code has 14 artifact write calls across engine.go (lines 290, 524, 525, 592, 615, 719, 720, 778, 802, 859, 951, 975, 1085, 1109, 1165, 1215, 1236, 1237, 1278, 1298, 1317, 1335, 1352). These go through `writeArtifact()` and `writeArtifactJSON()` which call `session.WriteArtifact()` directly.

**Fix — two-phase approach:**

**Phase A: Add SessionDir sub-dir helpers and overload helpers**

Add these methods to `SessionDir` in `internal/agent/session.go`:

```go
func (s SessionDir) ResearchDir() string { return filepath.Join(s.Path, "research") }
func (s SessionDir) DeliberationDir() string { return filepath.Join(s.Path, "deliberation") }
func (s SessionDir) LoopDir(n int) string {
    return filepath.Join(s.Path, "deliberation", fmt.Sprintf("loop_%02d", n))
}
func (s SessionDir) ExecutionDir() string { return filepath.Join(s.Path, "execution") }
func (s SessionDir) ValidationDir() string { return filepath.Join(s.Path, "validation") }
func (s SessionDir) GateDir(pos orchestrator.HumanGatePosition) string {
    switch pos {
    case orchestrator.GateBeforeDeliberation: return filepath.Join(s.Path, "gate_before_deliberation")
    case orchestrator.GateBeforeWorker: return filepath.Join(s.Path, "gate_before_worker")
    case orchestrator.GateBeforeValidator: return filepath.Join(s.Path, "gate_before_validator")
    case orchestrator.GateEndOfPipeline: return filepath.Join(s.Path, "gate_end_of_pipeline")
    }
    panic(fmt.Sprintf("unknown gate position: %d", pos))
}
```

Add `writeArtifactIn` and `writeArtifactJSONIn` helpers in `engine_phases.go` (or as methods on a session writer struct):

```go
// writeArtifactIn writes an artifact to a named subdirectory of the session.
func writeArtifactIn(session SessionDir, subdir string, name string, content string) {
    if session.Path == "" {
        return
    }
    dir := filepath.Join(session.Path, subdir)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        slog.Warn("create artifact subdir", "dir", dir, "err", err)
        return
    }
    path := filepath.Join(dir, name)
    if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
        slog.Error("write artifact", "path", path, "err", err)
    }
}

func writeArtifactJSONIn(session SessionDir, subdir string, name string, v any) {
    if session.Path == "" {
        return
    }
    dir := filepath.Join(session.Path, subdir)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        slog.Warn("create artifact subdir", "dir", dir, "err", err)
        return
    }
    data, err := json.MarshalIndent(v, "", "  ")
    if err != nil {
        slog.Error("marshal artifact", "name", name, "err", err)
        return
    }
    path := filepath.Join(dir, name)
    if err := os.WriteFile(path, data, 0o644); err != nil {
        slog.Error("write artifact", "path", path, "err", err)
    }
}

// writeDialogEntryMarkdown appends a turn to a gate directory's dialog.md file.
// It uses the multi-section Markdown format:
//
//	## Human
//	<message>
//	---
func writeDialogEntryMarkdown(path string, role string, message string) {
    if path == "" {
        return
    }
    f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
    if err != nil {
        slog.Error("open dialog.md", "path", path, "err", err)
        return
    }
    defer f.Close()
    f.WriteString("## " + role + "\n")
    f.WriteString(message + "\n")
    f.WriteString("---\n")
}

// findHighestPlan returns the path to the highest-numbered plan-v*.md in the given
// directory. It globs for plan-v*.md, sorts the matches, and returns the last entry.
// If no matches exist, it returns ("", nil).
func findHighestPlan(sessionPath, subdir string) string {
    dir := filepath.Join(sessionPath, subdir)
    matches, _ := filepath.Glob(filepath.Join(dir, "plan-v*.md"))
    if len(matches) == 0 {
        return ""
    }
    sort.Strings(matches)
    return matches[len(matches)-1]
}

// readDialogMarkdown reads the full contents of a dialog.md file.
// Returns empty string if the file does not exist.
func readDialogMarkdown(path string) string {
    data, err := os.ReadFile(path)
    if err != nil {
        return ""
    }
    return string(data)
}
```

**Phase B: Complete artifact path audit**

Every artifact write call must be mapped to its target subdirectory. Here is the complete audit:

| Current call (line) | Current path | New path | Subdir |
|---------------------|-------------|----------|--------|
| `writeArtifact(session, "prompt.md", ...)` (290) | `prompt.md` | `prompt.md` | _(root, unchanged)_ |
| `writeArtifact(session, "researcher_draft.md", ...)` (524) | `researcher_draft.md` | `researcher_draft.md` | `research/` |
| `writeArtifactJSON(session, "researcher_meta.json", ...)` (525) | `researcher_meta.json` | `researcher_meta.json` | `research/` |
| `writeArtifactJSON(session, "architect_meta.json", ...)` (592, 615) | `architect_meta.json` | `architect_initial_meta.json` | `deliberation/loop_NN/` |
| `writeArtifact(session, "critic_report.md", ...)` (719) | `critic_report.md` | `critic_report.md` | `deliberation/loop_NN/` |
| `writeArtifactJSON(session, "critic_meta.json", ...)` (720) | `critic_meta.json` | `critic_meta.json` | `deliberation/loop_NN/` |
| `writeArtifactJSON(session, "architect_critic_revision_%d_meta.json", ...)` (778, 802) | `architect_critic_revision_N_meta.json` | `architect_revision_meta.json` | `deliberation/loop_NN/` |
| `writeArtifact(session, "final_plan.md", ...)` (859, 1165) | `final_plan.md` | `final_plan.md` | _(root, after gate)_ |
| `writeArtifactJSON(session, "architect_revision_%d_meta.json", ...)` (951, 975, 1085, 1109) | `architect_revision_N_meta.json` | `architect_revision_meta.json` | `deliberation/loop_NN/` |
| `writeArtifactJSON(session, "worker_meta.json", ...)` (1215, 1237) | `worker_meta.json` | `worker_meta.json` | `execution/` |
| `writeArtifact(session, "worker_output.txt", ...)` (1236) | `worker_output.txt` | `worker_output.txt` | `execution/` |
| `writeArtifactJSON(session, "validator_meta.json", ...)` (1278, 1298, 1317, 1335) | `validator_meta.json` | `validator_meta.json` | `validation/` |
| `writeArtifact(session, "worker_validation.txt", ...)` (1352) | `worker_validation.txt` | `worker_validation.txt` | `validation/` |

**Session log copy paths** (via `copyLog` helper):

| Current call | Current path | New path | Subdir |
|-------------|-------------|----------|--------|
| researcher_session.jsonl (502, 520) | `researcher_session.jsonl` | `researcher_session.jsonl` | `research/` |
| architect_session.jsonl (584, 607) | `architect_session.jsonl` | `architect_initial_session.jsonl` | `deliberation/loop_NN/` |
| critic_session.jsonl (689, 707) | `critic_session.jsonl` | `critic_session.jsonl` | `deliberation/loop_NN/` |
| architect_critic_revision_session.jsonl (770, 794) | `architect_critic_revision_session.jsonl` | `architect_revision_session.jsonl` | `deliberation/loop_NN/` |
| worker_session.jsonl (1232) | `worker_session.jsonl` | `worker_session.jsonl` | `execution/` |
| validator_session.jsonl (1274, 1294, 1331) | `validator_session.jsonl` | `validator_session.jsonl` | `validation/` |

**Root-level artifacts (unchanged):**
- `prompt.md` — user prompt
- `final_plan.md` — approved plan (after gate)
- `run_config.json` — PipelineSetup JSON (layout detection)
- `run.log` — run log

**Gate decision artifacts:**
- `gate_decision.json` in `gate_before_deliberation/`, `gate_before_worker/`, `gate_before_validator/`, `gate_end_of_pipeline/`

**Gate dialog artifacts (new):**
- `dialog.md` in each gate directory — human-agent conversation log (multi-section Markdown)

### BR-3: AnalyzeRunCompleteness — no old-format support

**Evidence verified:** `session.go:312-399` — `AnalyzeRunCompleteness` takes `(runPath string, detail RunDetail)` and returns `RunCompleteness` with `FirstMissingAgent string`. `RunDetail` has `Steps []StepMeta` populated by `LoadRunDetail` which scans `*_meta.json` via glob. `KnownAgents = ["researcher", "architect", "critic", "worker"]` (no "validator").

**Fix — RunCompleteness uses new layout only:**

The `RunCompleteness` struct gets new layout fields only:

```go
type RunCompleteness struct {
    Complete         bool
    MissingAgents    []string
    FailedAgents     []string
    MissingArtifacts []ArtifactRequirement
    Reason           string
    // New layout fields (only supported layout)
    RestartPhase     RestartPhase
    DeliberationLoop int         // 1-based; 0 if not in deliberation
}
```

`AnalyzeRunCompleteness` always uses new-layout logic:

```go
func AnalyzeRunCompleteness(runPath string, detail RunDetail) RunCompleteness {
    var c RunCompleteness

    // Always new layout: read run_config.json to determine intended phases
    var setup PipelineSetup
    data, readErr := os.ReadFile(filepath.Join(runPath, "run_config.json"))
    if readErr != nil {
        // No run_config.json — run is incomplete/unrestartable
        c.Reason = "missing run_config.json"
        return c
    }
    json.Unmarshal(data, &setup)

    // Check research/
    if setup.Research {
        if !fileExists(filepath.Join(runPath, "research", "researcher_meta.json")) {
            c.MissingAgents = append(c.MissingAgents, "researcher")
        }
    }

    // Check deliberation/loop_NN/ for each loop
    for loop := 1; loop <= setup.DeliberationLoops; loop++ {
        loopDir := filepath.Join(runPath, "deliberation", fmt.Sprintf("loop_%02d", loop))
        hasLoopMeta := fileExists(filepath.Join(loopDir, "architect_initial_meta.json")) ||
            fileExists(filepath.Join(loopDir, "architect_revision_meta.json"))
        if !hasLoopMeta && loop == 1 {
            c.MissingAgents = append(c.MissingAgents, "architect")
        }
        if fileExists(filepath.Join(loopDir, "critic_meta.json")) {
            // critic ran in this loop
        } else if loop <= setup.DeliberationLoops {
            // Check if critic ran in any previous loop
            found := false
            for prev := 1; prev < loop; prev++ {
                if fileExists(filepath.Join(runPath, "deliberation", fmt.Sprintf("loop_%02d", prev), "critic_meta.json")) {
                    found = true
                    break
                }
            }
            if !found {
                c.MissingAgents = append(c.MissingAgents, "critic")
            }
        }
    }

    // Check execution/
    if setup.Execution {
        if !fileExists(filepath.Join(runPath, "execution", "worker_meta.json")) {
            c.MissingAgents = append(c.MissingAgents, "worker")
        }
    }

    // Check validation/
    if setup.Validation && setup.Execution {
        if !fileExists(filepath.Join(runPath, "validation", "validator_meta.json")) {
            c.MissingAgents = append(c.MissingAgents, "validator")
        }
    }

    // Determine RestartPhase
    if contains(c.MissingAgents, "researcher") || contains(c.FailedAgents, "researcher") {
        c.RestartPhase = RestartFromResearch
    } else if contains(c.MissingAgents, "architect") || contains(c.MissingAgents, "critic") ||
        contains(c.FailedAgents, "architect") || contains(c.FailedAgents, "critic") {
        // Find the first incomplete loop
        for loop := 1; loop <= setup.DeliberationLoops; loop++ {
            loopDir := filepath.Join(runPath, "deliberation", fmt.Sprintf("loop_%02d", loop))
            hasArchitectMeta := fileExists(filepath.Join(loopDir, "architect_initial_meta.json")) ||
                fileExists(filepath.Join(loopDir, "architect_revision_meta.json"))
            if !hasArchitectMeta {
                c.RestartPhase = RestartFromDeliberation
                c.DeliberationLoop = loop
                break
            }
        }
        if c.RestartPhase == 0 {
            c.RestartPhase = RestartFromDeliberation // last loop, retry from beginning
        }
    } else if contains(c.MissingAgents, "worker") || contains(c.FailedAgents, "worker") {
        c.RestartPhase = RestartFromExecution
    } else if contains(c.MissingAgents, "validator") || contains(c.FailedAgents, "validator") {
        c.RestartPhase = RestartFromValidation
    }

    // If no missing agents and all checks passed, the run is complete
    if len(c.MissingAgents) == 0 && len(c.FailedAgents) == 0 {
        c.Complete = true
    }

    return c
}
```

**TUI impact:** `RunDetailScreen` calls `AnalyzeRunCompleteness` and reads `RestartPhase` (new field) instead of `FirstMissingAgent`. The `RestartRunIntent` struct uses the new `Phase` + `Loop` fields. Runs without `run_config.json` show as incomplete/unrestartable.

### BR-4: runDeliberation underspecified — explicit session management

**Evidence verified:** `engine.go:549-842` — the current deliberation block has:
1. First architect pass (RunPlanner, lines 549-641)
2. Critic review (RunPlanner, lines 651-750)
3. Architect second pass (RunContinue with planSessionID, lines 752-842)

In the new loop version, steps 1-3 repeat N times. The plan approval gate (now a configurable `HumanGate` at `GateBeforeWorker`) is separate and stays in `run()`.

**Fix — explicit loop semantics, fresh sessions for every agent call:**

```go
func (e *Engine) runDeliberation(
    ctx context.Context,
    setup PipelineSetup,
    session SessionDir,
    input Input,
    emit func(Event),
    decisions <-chan Decision,
    stream *streamCapture,
    streamOut chan<- harness.StreamUpdate,
    draftMarkdown string, // input from research or prompt
) (deliberationResult, error) {
    var result deliberationResult
    result.planMarkdown = draftMarkdown
    result.planRevisionCount = 0
    var criticReportMarkdown string
    var warnings []string
    architectAttempt := 0

    for loop := 1; loop <= setup.DeliberationLoops; loop++ {
        loopDir := session.LoopDir(loop)

        // --- Architect initial pass (loop 1) or revision (loop N>1) ---
        architectAttempt++
        emit(Event{Type: EventPhaseChange, Phase: PhasePlanning, Loop: loop})
        emit(Event{Type: EventAgentStarted, AgentID: "architect"})
        stream.SetAgent("architect")

        var planResult agent.PlanResult
        var planErr error

        if loop == 1 {
            // First loop: fresh planner call with initial plan
            // Every architect pass uses a fresh Planner instance + RunSession.
            architectPrompt := guardPrompt(agent.ArchitectPrompt(input.Prompt, draftMarkdown), input.Prompt, "architect")
            planner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
            planResult, planErr = planner.Run(ctx, architectPrompt, streamOut)
        } else {
            // Subsequent loops: fresh planner with previous plan as context
            architectPrompt := guardPrompt(agent.ContinuePrompt(result.planMarkdown, "continue deliberation loop "+strconv.Itoa(loop)), input.Prompt, "architect")
            planner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
            planResult, planErr = planner.Run(ctx, architectPrompt, streamOut)
        }

        if planErr != nil {
            // Error handling: write meta to loopDir, return error
            return result, fmt.Errorf("architect pass %d (loop %d): %w", architectAttempt, loop, planErr)
        }

        // Write architect meta
        metaName := "architect_initial_meta.json"
        if loop > 1 {
            metaName = "architect_revision_meta.json"
        }
        writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), metaName, /* meta */)
        result.planMarkdown = planResult.Plan
        result.warnings = agent.CheckPlanHealth(planResult.Plan)

        // Write plan-v<N>.md to loopDir
        result.planRevisionCount++
        planFileName := fmt.Sprintf("plan-v%d.md", result.planRevisionCount)
        writeArtifactIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), planFileName, result.planMarkdown)

        // --- Critic review ---
        emit(Event{Type: EventPhaseChange, Phase: PhaseCritiquing, Loop: loop})
        emit(Event{Type: EventAgentStarted, AgentID: "critic"})
        stream.SetAgent("critic")

        criticPlanner := agent.NewPlanner(e.Runners.Critic, e.Config.Critic.SystemPrompt)
        // Every critic pass uses a fresh Planner instance + RunSession.
        criticResult, criticErr := criticPlanner.Run(ctx,
            agent.CriticReviewPrompt(input.Prompt, result.planMarkdown), streamOut)

        if criticErr != nil {
            // Error handling: write critic meta to loopDir, return error
            return result, fmt.Errorf("critic review (loop %d): %w", loop, criticErr)
        }

        // Write critic artifacts to loopDir
        writeArtifactIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "critic_report.md", criticResult.Plan)
        writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "critic_meta.json", /* meta */)
        criticReportMarkdown = criticResult.Plan

        // --- Architect revision (critic feedback) ---
        architectAttempt++
        emit(Event{Type: EventAgentStarted, AgentID: "architect"})
        stream.SetAgent("architect")

        // Fresh planner with critic feedback as context
        // Every architect pass, including critic revisions, uses a fresh
        // Planner instance + RunSession (see BR-4 key session management rule #1).
        revPlanner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
        revResult, revErr := revPlanner.Run(ctx,
            agent.CriticContinuePrompt(result.planMarkdown, criticReportMarkdown), streamOut)

        if revErr != nil {
            // Error handling: write revision meta to loopDir, return error
            return result, fmt.Errorf("architect revision (loop %d): %w", loop, revErr)
        }

        revisedPlan := agent.DetectPlanRevision(revResult.Plan, result.planMarkdown, nil, result.planMarkdown)
        if revisedPlan != nil {
            result.planMarkdown = revisedPlan.Markdown
            result.warnings = revisedPlan.Warnings
        }

        // Write revision meta to loopDir
        writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "architect_revision_meta.json", /* meta */)

        // Write plan-v<N>.md to loopDir
        result.planRevisionCount++
        planFileName = fmt.Sprintf("plan-v%d.md", result.planRevisionCount)
        writeArtifactIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), planFileName, result.planMarkdown)
    }

    result.criticReport = criticReportMarkdown
    return result, nil
}
```

**Key session management rules documented:**

1. Every architect pass (initial + revisions) uses a **fresh** `Planner` instance — no session continuation.
2. Every critic pass uses a **fresh** `Planner` instance.
3. Plan markdown is passed as prompt context to every agent call.
4. No `planSessionID` variable — removed entirely.
5. Each loop writes to its own `deliberation/loop_NN/` directory — no cross-loop artifact collision.
6. `DeliberationLoops=0` is handled by the caller (`run()`) which applies `DefaultPipelineSetup().DeliberationLoops` (1) before calling `runDeliberation`.
7. `runDeliberation` receives `setup` with `DeliberationLoops >= 1` (guaranteed by the validation in `run()`).
8. Plan files are numbered: `plan-v1.md`, `plan-v2.md`, ... — the highest-numbered file is the current plan.
9. `guardPrompt` is already package-level (engine.go:38-44) and accessible from all extracted functions — no changes needed.
10. Every agent call uses `planner.Run()` which calls `RunSession` (fresh session). No `RunContinue` anywhere — plan markdown is passed as prompt context. This is a **deliberate behavioral change** from current code, which uses `RunContinue` for the architect's second pass.

---

## Medium-Severity Critique Resolutions

The following 6 medium-severity and 1 low-severity findings from the critique are addressed below. Each is already resolved by existing plan sections, but the resolution is not immediately obvious to a worker reading the plan. The items below make the resolution explicit and add any missing code examples.

### MS-1: `runHumanGate` loop structure is explicit at the caller (Critique #5)

**Status: Already resolved in WP2.** The plan approval gate code in WP2 shows the full `for` loop:

```go
if setup.HumanGates.Active(GateBeforeWorker) {
    for {
        d, err := runHumanGate(ctx, emit, decisions, HumanChatGateRequest{...})
        if err != nil { return }
        switch d.Type {
        case DecisionEdit:
            // ... edit handling ...
            if decision.Comment != "" || !decision.AutoApprove { continue }
        case DecisionComment:
            // ... comment handling ...
            continue
        case DecisionApprove:
            break
        case DecisionCancel:
            emit(Event{Type: EventComplete, Phase: PhaseDone})
            return
        }
    }
    break
}
```

**Explicit rule:** `runHumanGate` is a **one-shot** function — it emits `EventHumanGate` and waits for exactly one decision. The **caller** (`run()`) wraps it in a `for { ... continue/break }` loop. The loop is NOT inside `runHumanGate`. This is already shown in the WP2 code block above; the worker must not move the loop inside `runHumanGate`.

### MS-2: Helper function placement committed to `engine_phases.go` (Critique #6)

**Status: Already resolved in BR-2/BR-4.** The plan defines these helpers in the BR-2 Phase A code block:

```go
func writeArtifactIn(session SessionDir, subdir string, name string, content string) { ... }
func writeArtifactJSONIn(session SessionDir, subdir string, name string, v any) { ... }
func writeDialogEntryMarkdown(path string, role string, message string) { ... }
func findHighestPlan(sessionPath, subdir string) string { ... }
func readDialogMarkdown(path string) string { ... }
```

**Committed placement:** All five helpers go in **`engine_phases.go`** (the new file that owns `runResearch`, `runExecution`, `runValidation`). They are package-level functions in the `orchestrator` package, accessible from all extracted files (`engine.go`, `engine_deliberation.go`, `engine_restart.go`). The plan's "Execution order for extraction" in BR-1 step 4 says "Add `writeArtifactIn` / `writeArtifactJSONIn` overloads" — this refers to these same functions.

### MS-3: `writeArtifactIn` call-site migration table (Critique #7)

**Status: BR-2 Phase B has the artifact path audit. The following table maps each existing `writeArtifact` call to its new target.** The worker must update every `writeArtifact(session, "X", content)` call in the extracted functions to use `writeArtifactIn(session, "subdir", "X", content)` where `subdir` is the new directory.

| Extracted function | Current call (approx. line in engine.go) | New call |
|---|---|---|
| `runResearch` | `writeArtifact(session, "researcher_draft.md", draft.Markdown)` (line 524) | `writeArtifactIn(session, "research", "researcher_draft.md", draft.Markdown)` |
| `runResearch` | `writeArtifactJSON(session, "researcher_meta.json", meta)` (line 525) | `writeArtifactJSONIn(session, "research", "researcher_meta.json", meta)` |
| `runResearch` | `copyLog(..., "researcher_session.jsonl")` (line 520) | `copyLog` writes to `filepath.Join(session.ResearchDir(), "researcher_session.jsonl")` |
| `runDeliberation` | `writeArtifactJSON(session, "architect_initial_meta.json", meta)` (line 592) | `writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "architect_initial_meta.json", meta)` |
| `runDeliberation` | `writeArtifactJSON(session, "architect_critic_revision_%d_meta.json", meta)` (lines 778, 802) | `writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "architect_revision_meta.json", meta)` |
| `runDeliberation` | `writeArtifact(session, "critic_report.md", criticResult.Plan)` (line 719) | `writeArtifactIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "critic_report.md", criticResult.Plan)` |
| `runDeliberation` | `writeArtifactJSON(session, "critic_meta.json", meta)` (line 720) | `writeArtifactJSONIn(session, "deliberation/loop_"+fmt.Sprintf("%02d", loop), "critic_meta.json", meta)` |
| `runDeliberation` | `writeArtifact(session, "final_plan.md", ...)` (lines 859, 1165) | **Root-level artifact** — stays as `writeArtifact(session, "final_plan.md", ...)` (no subdir) |
| `runExecution` | `writeArtifactJSON(session, "worker_meta.json", meta)` (line 1215) | `writeArtifactJSONIn(session, "execution", "worker_meta.json", meta)` |
| `runExecution` | `writeArtifact(session, "worker_output.txt", ...)` (line 1236) | `writeArtifactIn(session, "execution", "worker_output.txt", ...)` |
| `runExecution` | `copyLog(..., "worker_session.jsonl")` (line 1232) | `copyLog` writes to `filepath.Join(session.ExecutionDir(), "worker_session.jsonl")` |
| `runValidation` | `writeArtifactJSON(session, "validator_meta.json", meta)` (lines 1278, 1298) | `writeArtifactJSONIn(session, "validation", "validator_meta.json", meta)` |
| `runValidation` | `writeArtifact(session, "worker_validation.txt", ...)` (line 1352) | `writeArtifactIn(session, "validation", "worker_validation.txt", ...)` |

**Note:** `writeArtifact` (root-level) is kept for `prompt.md`, `final_plan.md`, and `run_config.json` which stay at the session root. All other artifacts go into subdirectories.

### MS-4: `EventHumanGate` TUI access pattern (Critique #8)

**Status: WP5 shows `ApplyEvent` handling for `EventHumanGate`. The following shows the complete handler with `HasPlanEditor` branching.**

The current `ApplyEvent` switch handles `EventGateRequest` (line 397-433 of screen_pipeline.go) which reads `event.Gate.Type`, `event.Gate.FinalPlanMarkdown`, `event.Gate.PlanDiff`, etc. This entire block is **replaced** by:

```go
case orchestrator.EventHumanGate:
    // Branch on HasPlanEditor to create the correct sub-model
    if event.HumanGate.HasPlanEditor {
        s.activeChat = newPlanChatMode(event.HumanGate, s.width)
    } else {
        s.activeChat = newSimpleChatMode(event.HumanGate, s.width)
    }
    s.content = ContentHumanGate
    // Set fields that both modes may need
    s.startTime = time.Time{} // reset for gate timing
```

**Field access pattern:** The new `HumanChatGateRequest` fields are accessed as `event.HumanGate.Position`, `event.HumanGate.AgentSessionID`, `event.HumanGate.AgentLabel`, `event.HumanGate.HasPlanEditor`, `event.HumanGate.PlanMarkdown`, `event.HumanGate.PlanFilePath`, `event.HumanGate.PlanWarnings`, `event.HumanGate.CriticReport`.

**`EventGateRequest` and `EventPlanReady` cases are deleted.** Both are replaced by the single `EventHumanGate` handler. The `Event.Gate GateRequest` field is deleted from the `Event` struct and replaced by `HumanGate HumanChatGateRequest`.

### MS-5: `ContentHumanGate` case in `Update()` switch (Critique #9)

**Status: WP5 shows `ContentHumanGate` in `viewFooter()`. The `Update()` switch case must also be added.** The plan's WP5 section says "Add `case ContentHumanGate:` to the `Update()` switch" but doesn't show the code. Here is the complete case:

```go
case ContentHumanGate:
    if s.activeChat != nil {
        var cmd tea.Cmd
        s.activeChat, cmd = s.activeChat.Update(msg)
        if s.activeChat.Pending() != nil {
            s.PendingIntent = s.activeChat.Pending()
            s.activeChat = nil
        }
        return s, cmd
    }
    return s, nil
```

**Additionally, `ContentPlanReview` case in the `Update()` switch (line 586-587) is removed** and replaced by the `ContentHumanGate` case above. The `handlePlanReviewKey` function is kept for backward compatibility during the transition but is eventually removed once the gate migration is complete.

**`screen_pipeline_keys.go` impact:** The `ContentPlanReview` check in `handleStreamingKey` (line 42 of screen_pipeline_keys.go: `if s.content != ContentPlanReview && s.content != ContentUserQuestion`) must be updated to also exclude `ContentHumanGate`:

```go
case "ctrl+d":
    if s.content != ContentPlanReview && s.content != ContentUserQuestion && s.content != ContentHumanGate {
        s.showDashboard = !s.showDashboard
        s.SyncViewports()
        return s, nil
    }
```

### MS-6: `EventAgentSkipped` `Loop` field is zero value (Critique #10)

**Status: Already correct in the plan.** `EventAgentSkipped` does **not** set `Loop` — it relies on the zero value (`Loop: 0`). This is the correct behavior because skipped phases have no loop association. `EventPhaseChange` with `PhaseDeliberating` sets `Loop` to the actual loop number. The TUI `ApplyEvent` for `EventAgentSkipped` (WP5) does not read `event.Loop`.

### MS-7: `gateDirName` helper for `copyCompletedArtifacts` (Critique #2 continuation)

**Status: WP2 `copyCompletedArtifacts` code calls `gateDirName(pos)`. This function must be added.** The plan defines `SessionDir.GateDir(pos)` which returns the full path. `copyCompletedArtifacts` needs a name-only mapping. Here is the function to add to `human_gate.go`:

```go
// gateDirName returns the directory name for a gate position.
// Used by copyCompletedArtifacts to map gate positions to subdirectory names.
func gateDirName(pos HumanGatePosition) string {
    switch pos {
    case GateBeforeDeliberation:
        return "gate_before_deliberation"
    case GateBeforeWorker:
        return "gate_before_worker"
    case GateBeforeValidator:
        return "gate_before_validator"
    case GateEndOfPipeline:
        return "gate_end_of_pipeline"
    }
    panic(fmt.Sprintf("unknown gate position: %d", pos))
}
```

This is a **package-level function** in `internal/orchestrator/` (in `human_gate.go` alongside `HumanGateSet` and `HumanChatGateRequest`). It is separate from `SessionDir.GateDir()` which returns the full path — this function returns only the directory name string for use in `filepath.Join()` calls within `copyCompletedArtifacts`.

---

## Key Design Decisions

### D1: Pipeline phases as linked-list nodes

The control surface is four phases (Research, Deliberation, Execution, Validation) plus human gate positions — not a per-actor checkbox list. This matches the UX goal document exactly.

**Execution model:** Each pipeline phase is a node with `prev`/`next` pointers and `LaunchNext()` control. `LaunchNext()` is gated by `maxInFlight` — no goroutine pool to manage, no need to kill excess. This design is forward-compatible: if a DAG-based executor is added later, nodes already have the traversal primitives.

**HumanGate is a first-class node.** It uses the same `prev`/`next`/`LaunchNext()` pattern as researcher, architect, critic, worker, and validator — not a special-case helper function. This eliminates NIH by reusing the node interface for all pipeline steps, including human gates.

Added small End Pipeline phase: git worktree merge (not rebase — fewer commits, fewer conflict surfaces). Agent resolves trivial conflicts automatically; on smallest suspicion of data corruption, fail-fast (inform user, keep worktree intact); **on success** delete worktree.

```
Research:              ◁ Enabled ▷
Architect <-> Critic:  ◁ 4 ▷        ← stepper 1..10
Execution:             ◁ Enabled ▷
Validation:            ◁ Enabled ▷
Human Review: [ Before Worker, End of Pipeline ]
```

**Scope boundary — no YAML pipeline config.** Pipeline topology is defined by `PipelineSetup` in Go and the `RawPlan` from Claude — no separate YAML format. YAGNI: the CLI config has ~10 values; a YAML file adds indirection without value.

**Scope boundary — `RawPlan` is the sole plan contract.** The orchestrator reads plans from Claude via `agent.ReadPlanFromRun`. No `PlanExtractor` layer, no secondary plan format.

**Scope boundary — harness types are not duplicated.** Worker execution uses `Session` interface (via `RunSession`) directly. No `Worker` struct wrapping the harness. All runners (`ClaudeCLI`, `SandboxCLIRunner`) implement `RunSession`.

### D2: HumanGateSet as a set of positions — no boolean inversion bugs

Human gate positions are an enum; `HumanGateSet` is a slice membership check, not booleans.

```go
type HumanGatePosition int
const (
    GateBeforeDeliberation HumanGatePosition = iota // chat with Researcher
    GateBeforeWorker                                  // plan approval gate (chat/review/approve/abort with architect re-engagement)
    GateBeforeValidator                               // chat with Worker, no plan editor
    GateEndOfPipeline                                 // chat with Validator or Worker
)

type HumanGateSet []HumanGatePosition
func (h HumanGateSet) Active(pos HumanGatePosition) bool { ... }  // set membership
```

### D3: Typed gate events — single `HumanGate` field

`Event.Gate GateRequest` is deleted. `Event.HumanGate HumanChatGateRequest` is the sole gate field (zero value = not set). Each event type populates exactly one. This preserves compile-time safety and eliminates the dual-gate code path.

```go
type HumanChatGateRequest struct {
    Position       HumanGatePosition
    AgentSessionID string    // session to continue for chat/re-engagement
    AgentLabel     string    // "Researcher", "Architect", etc.
    HasPlanEditor  bool      // true for GateBeforeDeliberation and GateBeforeWorker
    PlanMarkdown   string    // current plan (when HasPlanEditor)
    PlanFilePath   string    // path to highest plan-v*.md on disk (when HasPlanEditor, for $EDITOR)
    PlanWarnings   []string  // plan health warnings (when HasPlanEditor)
    CriticReport   string    // critic's review report (when HasPlanEditor)
}
```

**Removed fields from `HumanChatGateRequest`:**
- `PlanDiff` — no longer computed from a git micro-repo; diffs are visible between numbered plan files on disk
- `PlanHistoryDir` — no plan-history/ git micro-repo
- `PlanHistoryHeadSHA` — no plan-history/ git micro-repo

**New fields in `HumanChatGateRequest`:**
- `PlanFilePath string` — path to the highest-numbered `plan-v*.md` on disk, used by `Ctrl+E`
  to open the plan in the user's `$EDITOR`. Set when `HasPlanEditor=true`. Also added to
  `PlanChatMode` as `planFilePath` so the TUI can resolve the path without relying on
  `PipelineScreen.planFilePath`.

### D4: Session directory restructured for loops

```
.orqestra/sessions/<ts>-run/
  run_config.json            ← PipelineSetup persisted
  prompt.md
  run.log
  research/
    researcher_meta.json
    researcher_draft.md
    researcher_session.jsonl
  deliberation/
    loop_01/
      architect_initial_meta.json
      architect_initial_session.jsonl
      critic_meta.json
      critic_report.md
      critic_session.jsonl
      architect_revision_meta.json
      architect_revision_session.jsonl
      plan-v1.md               ← plan after initial pass
      plan-v2.md               ← plan after critic revision
    loop_02/ ...
  final_plan.md
  gate_before_deliberation/  ← when gate ran
    gate_decision.json
    dialog.md                ← human-agent conversation log (Markdown)
  gate_before_worker/
    gate_decision.json
    dialog.md                ← human-agent conversation log (Markdown)
  execution/
    worker_meta.json
    worker_output.txt
    worker_session.jsonl
  gate_before_validator/
    gate_decision.json
    dialog.md                ← human-agent conversation log (Markdown)
  validation/
    validator_meta.json
    validator_session.jsonl
    worker_validation.txt
  gate_end_of_pipeline/
    gate_decision.json
    dialog.md                ← human-agent conversation log (Markdown)
```

**Markdown dialog files:** Each gate directory contains a `dialog.md` file that captures human-agent interactions in a multi-section Markdown format. Each `## Human` and `## Architect` section interleaves conversation with plan snapshots. The human gate agent is the data (format) owner of its dialog.

**No backward compatibility:** `AnalyzeRunCompleteness` and `copyCompletedArtifacts` always use new-layout logic. Runs without `run_config.json` are treated as incomplete/unrestartable.

### D5: Restart heuristics use typed RestartPhase

**Explicitly replaces the old `RestartInput` struct (engine.go:23-27).** The old struct with `FirstMissingAgent string` is deleted and replaced by the new typed version. All dependent types are updated:
- `RestartRunIntent` in `messages.go:167-170` — changes from `FirstMissingAgent string` to `Phase RestartPhase` + `Loop int`
- `Model.lastRestartFirstMissingAgent` in `model.go:98` — changes to `lastRestartPhase RestartPhase` + `lastRestartLoop int`
- `Model.startPipelineRestart()` in `model.go:700` — signature changes from `(prompt, runPath, firstMissingAgent string)` to `(prompt, runPath string, phase RestartPhase, loop int)`

```go
type RestartPhase int
const (
    RestartFromResearch      RestartPhase = iota
    RestartFromDeliberation               // + DeliberationLoop int (which loop)
    RestartFromExecution
    RestartFromValidation
)

type RestartInput struct {
    RunPath          string
    Phase            RestartPhase
    DeliberationLoop int  // 1-based; 0 if not in deliberation
}
```

`AnalyzeRunCompleteness` reads `run_config.json` to know intended loop count, then checks presence+status of artifacts in the new directory structure.

### D6: Pipeline setup panel is an overlay on the prompt screen — not a separate state

The pipeline setup is an overlay/panel above the prompt textarea. It is:
- **Always visible** in the prompt entry screen (not a separate `AppState`).
- **Focusable** via `^P` key binding — when focused, the user can navigate and toggle settings.
- **Unfocused by default** — the prompt textarea is the primary control, always in focus.
- **Resets to defaults** on each launch — no persistence between runs.
- **Becomes static** when the pipeline starts — the setup panel content scrolls up like the rest of the output.

**Footer hint:** When the setup panel is visible but unfocused, the footer shows `^P Pipeline Setup` and the panel displays low-contrast text: "Press ^P to change the pipeline".

**Key handling:**
- `^P` — toggle setup panel focus (open/close)
- When panel is focused: `↑/↓` navigate, `←/→` toggle values, `Enter` confirm, `Esc` close
- When panel is closed: `Enter` submits prompt, `Esc` closes prompt input
- `Enter` with panel open — closes panel, applies settings, returns focus to prompt

**No `StateSetup` needed.** The app flow is:
1. `StatePrompt` — prompt textarea + setup overlay panel (always visible, toggleable)
2. `StatePipeline` — 3-zone split layout (pipeline running/done)
3. `StateRunsList` — historical runs list
4. `StateRunDetail` — detail view for a single historical run

### D7: HumanChatMode as a typed interface sub-model with split view

Following tui-instructions.md ("one active mode, one owned value"):

```go
type HumanChatMode interface {
    Update(msg tea.Msg) (HumanChatMode, tea.Cmd)
    View(width int) string
    Footer() string
    Pending() tea.Msg  // non-nil = user acted, parent drains
}

// PlanChatMode — for GateBeforeDeliberation, GateBeforeWorker (has plan editor)
type PlanChatMode struct { ... }

// SimpleChatMode — for GateBeforeValidator, GateEndOfPipeline (chat only)
type SimpleChatMode struct { ... }
```

**Split view layout when a human gate fires:**

```
┌─────────────────────────────────────────┐
│ Pipeline history (scrollable)           │
│   Research: ✓ done                      │
│   Architect↔Critic: 2 loops             │
│   Worker: ✓ done                        │
├─────────────────────────────────────────┤
│ Agent output (last agent's output)      │
│   [rendered markdown plan if available] │
├─────────────────────────────────────────┤
│ Chat input (same control as prompt)     │
│   [textarea for chat message]           │
├─────────────────────────────────────────┤
│ Footer: [^O] collapse chat              │
└─────────────────────────────────────────┘
```

**Chat behavior:**
- The chat input **mimics the initial prompt control** — same textarea widget, same rendering, same key bindings.
- Chat expands upward, occupying **max 50% of the remaining screen** (below pipeline history and agent output).
- The chat is **collapsible** via `^O` — when collapsed, only the pipeline history and agent output remain visible.
- The pipeline history (top portion) is always visible and scrollable.
- The agent output (middle portion) shows the last agent's output — if it produced a plan, the plan is rendered as markdown.

**Field ownership:** `PlanChatMode` shares fields with `PipelineScreen` (planComment, chatHistory, planDiff, reviewTokensIn, reviewTokensOut, awaitingPlanDecision) as a **known limitation documented for follow-up decomposition**. The full decomposition moves these fields into `PlanChatMode` and removes them from `PipelineScreen`. For this change, we accept the sharing and document it.

PipelineScreen gets ONE new field (`activeChat HumanChatMode`) and ONE new `ContentMode` value (`ContentHumanGate`). `ContentPlanReview` is removed — the plan approval gate now uses `ContentHumanGate` with `PlanChatMode` (same UI, different event path). All gates flow through `EventHumanGate` → `ContentHumanGate` → `s.activeChat`.

### D8: Deliberation loop in orchestrator — no goto labels

Replace the current `goto skipPlanning / planGate` with explicit conditional blocks.

**Refactor is phased to reduce risk** (addresses goto scope underestimation):
1. **Phase 1:** Extract the deliberation loop into `func (e *Engine) runDeliberation(...)` in `engine_deliberation.go`
2. **Phase 2:** Extract phase-specific blocks (research, execution, validation) into `engine_phases.go`
3. **Phase 3:** Extract restart logic into `engine_restart.go`
4. **Phase 4:** Restructure the main `run()` to use these functions without goto

```go
// Deliberation loop
for loop := 1; loop <= setup.DeliberationLoops; loop++ {
    loopDir := session.LoopDir(loop)
    // architect (fresh planner each time, plan markdown as context)
    // critic (fresh planner each time, plan markdown as context)
    // architect revision (fresh planner each time, critic feedback as context)
    // persist loop artifacts under loopDir
}
```

Every agent call uses a fresh session. Plan markdown is passed as prompt context.

**Plan revision files:** Each architect pass writes a new `plan-v<N>.md` file. The revision counter increments with each write. The highest-numbered file is the current plan. No git commits — plan history is visible as numbered files on disk.

### D9: Human gate chat loop — three decision paths, revision as loop

**Decision:** The human gate is a chat loop with three decision paths. The interaction model is chat-first: the user reads the split view and types messages.

**Key bindings at the human gate:**

| Key | Action |
|-----|--------|
| `Enter` | Send chat message to agent (immediate streaming). Agent responds in real-time. Gate re-shown with response. |
| `Ctrl+Enter` | Request revision (only active if `dialog.md` or the highest-numbered `plan-v*.md` has changed since last revision). Sends chat message + inline comments to architect. Architect produces revised plan. Gate re-shown with new plan. |
| `Ctrl+E` | Open the highest-numbered `plan-v*.md` file in the external editor. After editing, the user sends a chat message referencing the edits (e.g., "I made comments directly in the plan, look for `<<-- [comment]`"). The revision request picks up inline comments from the edited plan. |
| `Ctrl+A` | Approve — proceed to next phase. |
| `Ctrl+C` | Abort — terminate the run. |

**Revision tracking:** The `Ctrl+Enter` key is only active (enabled) when the gate has detected changes — either `dialog.md` has a new human entry since the last revision, or the highest-numbered `plan-v*.md` has been modified. The gate tracks the SHA256 of `dialog.md` and the highest-numbered `plan-v*.md` (found by glob, sorted, last entry) to determine this.

**External editor flow:**
1. User presses `Ctrl+E` — opens the highest-numbered `plan-v*.md` in `$EDITOR` (or `$VISUAL`).
2. User edits the plan, adding inline comments like `<<-- [comment] I think section 2 should...`.
3. User saves and exits the editor. The plan file is updated on disk.
4. User types a chat message referencing the edits: "I made comments directly in the plan, look for `<<-- [comment]`".
5. This is a normal chat message (Enter), NOT a revision request yet.
6. The architect reads the plan file, sees the inline comments, and responds.
7. User sends `Ctrl+Enter` to request a revision. The revision prompt includes:
   - The latest plan markdown (highest-numbered `plan-v*.md`, read from disk)
   - The chat history (all messages since the gate opened)
   - The inline comments detected in the plan file

**Revision prompt template:**
```
The current implementation plan is below. The human reviewer has sent messages and made edits.

<current_plan>
%s
</current_plan>

<chat_history>
Human: ...
Architect: ...
Human: ...
</chat_history>

<inline_comments>
<<-- [comment] I think section 2 should...
</inline_comments>

If the reviewer asks a question, answer it using your knowledge of the codebase.
If the reviewer requests changes, revise the plan.
Output the COMPLETE updated plan starting with "# Plan".
```

**Chat message prompt template (non-revision):**
```
The current implementation plan is below. The human reviewer sent a message.

<current_plan>
%s
</current_plan>

<reviewer_message>
%s
</reviewer_message>

If the reviewer asks a question, answer it using your knowledge of the codebase from this session.
If the reviewer requests changes, revise the plan.
```

**Gate decision outcomes:**
1. **Approve (`Ctrl+A`):** Gate loop breaks, execution proceeds to Worker phase.
2. **Request Refinement (`Ctrl+Enter`):** Architect is re-engaged with a fresh session. The revision prompt includes the latest plan (highest-numbered `plan-v*.md`) + chat history + inline comments. The gate re-shows with the revised plan. This is the loop — user can iterate indefinitely.
3. **Abort (`Ctrl+C`):** Run terminates.

**Why this works:** The chat-first model means the user doesn't need to decide upfront whether to approve, edit, or comment. They just chat. The architect handles questions, answers, and plan revisions naturally. `Ctrl+Enter` is the explicit "I'm done chatting, give me a revised plan" signal. `Ctrl+E` is the "I want to edit the plan directly" signal. The external editor flow integrates with the chat — edits become chat messages that reference the edits.

### D10: Session — bidirectional streaming via `RunSession`

**Status:** `RunInteractive` implemented and proven working in `internal/harness/interactive_cli.go`. Consolidated into `Session` interface (see Runner Consolidation section).

**Decision:** The orchestrator uses the `Session` interface. Every agent call — deliberation, human gate chat, worker execution, validation — goes through `RunSession`. The method starts a Claude CLI session with `--input-format stream-json` and returns a bidirectional `Session` handle.

**`RunSession` is the only runner method.** There is no `RunPrint`, `RunStreaming`, `RunContinue`, or `RunInteractive`. Every agent call is a fresh session. Plan markdown, chat history, and validation context are passed as prompt text.

**`--input-format stream-json` is implemented and verified.** The NDJSON stdin format was discovered by reverse-engineering against [The-Vibe-Company/companion](https://github.com/The-Vibe-Company/companion) (`web/server/protocol/claude-upstream/claude-adapter.ts`) and validated with live Claude CLI testing. The format is confirmed working.

**Critical implementation detail — `--print` flag:** The `--print` flag forces the Claude CLI into one-shot mode — it processes the initial prompt and exits immediately, killing the bidirectional session before the TUI can receive any updates. **`--print` must NOT be passed** when using `RunSession`. Without `--print`, the CLI stays alive: it processes the initial NDJSON prompt written to stdin, streams the response, then waits for follow-up NDJSON messages via stdin (written by `Post()`).

**Session interface (consolidated from `InteractiveSession`):**

```go
type Session interface {
    Post(msg string) error
    Done() <-chan error
    Updates() <-chan StreamUpdate
    Usage() TokenUsage
    ResultError() bool
    SessionID() string
    PlanPath() string
    Kill() error
}
```

**`RunSession` behavior (verified implementation):**
1. Starts `claude -p <prompt> --output-format stream-json --input-format stream-json --verbose --include-partial-messages`
2. The initial prompt is sent as NDJSON via stdin, then stdin is closed immediately to signal EOF so the CLI processes the prompt and produces output
3. Harness manages the `parseStream()` goroutine internally — it reads stdout, parses NDJSON, writes to `sess.updates`
4. Harness owns the stdin pipe — `Post()` writes NDJSON via `sess.stdin`
5. `done` channel is closed when the process exits (success, error, or SIGTERM)
6. `sessionID`, `planPath`, `usage`, and `resultError` are extracted from the stream events by `parseStream()`

**Harness owns the capture goroutine:** The `parseStream()` goroutine, NDJSON parsing, and process lifecycle are all managed by the harness. The orchestrator only calls `sess.Post(msg)`, `sess.Done()`, `sess.Updates()`, `sess.Usage()`, `sess.Kill()`. This keeps the harness as the sole owner of the session's I/O.

**Gate loop with `RunSession`:**

```go
// All runners have RunSession — no type assertions needed.
sess, err := e.Runners.Architect.RunSession(ctx, architectPrompt, streamOut)
if err != nil { return }
defer sess.Kill()

for {
    select {
    case <-sess.Done():
        break
    case update := range sess.Updates():
        streamOut <- update
    case decision := <-decisions:
        switch decision.Type {
        case DecisionComment, DecisionEdit:
            sess.Post(decision.Comment) // harness formats NDJSON
        case DecisionApprove, DecisionCancel:
            sess.Kill()
        }
    }
}
```

**Stdin writes via `Session.Post`:** The orchestrator's `run()` receives decisions from the TUI via the `decisions` channel and calls `sess.Post()` to write chat messages. The harness owns the NDJSON formatting and stdin pipe. Single writer (orchestrator goroutine) avoids races.

**NDJSON input format (verified against companion repo):**

```json
{"type":"user","message":{"role":"user","content":"Hello architect"},"parent_tool_use_id":null,"session_id":"<session_id>"}
```

**Initial prompt:** Sent as NDJSON via stdin immediately after `cmd.Start()`, then stdin is closed. The `-p` flag is still passed but the Claude CLI in stream-json mode expects the prompt via stdin NDJSON.

**Scope:** `RunSession` is used for ALL agent calls — deliberation (architect/critic), human gate chat, worker execution, and validation. Every call is a fresh session. No session continuation.

**Error handling:** If `RunSession` fails (process can't start, stdin pipe breaks, etc.), the orchestrator returns an error and the TUI shows it. The gate is not skipped.

**Process cleanup:** On `DecisionApprove` or `DecisionAbort`, the orchestrator calls `sess.Kill()`. The `done` channel fires, and the orchestrator's `run()` function detects the exit and proceeds.

---

## Work Packages

### WP0: New types in `internal/orchestrator/`

> **CRITIQUE #5 FIX — one-entity-per-file:** The types are split across 3 files to comply with
> CLAUDE.md "One entity per file" rule. Each file owns a single primary type plus its methods.

**File 1 — `pipeline_setup.go`** (primary type: `PipelineSetup`):

```go
type PipelineSetup struct {
    Research          bool
    DeliberationLoops int          // 1..10; 0 is invalid
    Execution         bool
    Validation         bool
    HumanGates        HumanGateSet  // type from human_gate.go
}

func DefaultPipelineSetup() PipelineSetup {
    return PipelineSetup{
        Research: true, DeliberationLoops: 1,
        Execution: true, Validation: true,
    }
}

func (p PipelineSetup) Validate() error {
    if p.DeliberationLoops < 1 || p.DeliberationLoops > 10 {
        return fmt.Errorf("deliberation_loops must be between 1 and 10, got %d", p.DeliberationLoops)
    }
    if !p.Research && !p.Execution && !p.Validation {
        return fmt.Errorf("at least one of Research, Execution, or Validation must be enabled")
    }
    return nil
}
```

**File 2 — `human_gate.go`** (primary type: `HumanGateSet`):

```go
type HumanGatePosition int
const (
    GateBeforeDeliberation HumanGatePosition = iota
    GateBeforeWorker
    GateBeforeValidator
    GateEndOfPipeline
)

type HumanGateSet []HumanGatePosition
func (h HumanGateSet) Active(pos HumanGatePosition) bool {
    for _, p := range h {
        if p == pos { return true }
    }
    return false
}

type HumanChatGateRequest struct {
    Position       HumanGatePosition
    AgentSessionID string
    AgentLabel     string
    HasPlanEditor  bool
    PlanMarkdown   string
    PlanWarnings   []string
    CriticReport   string
}
```

**File 3 — `restart.go`** (primary type: `RestartInput`):

```go
type RestartPhase int
const (
    RestartFromResearch      RestartPhase = iota
    RestartFromDeliberation               // + DeliberationLoop int (which loop)
    RestartFromExecution
    RestartFromValidation
)

type RestartInput struct {
    RunPath          string
    Phase            RestartPhase
    DeliberationLoop int  // 1-based; 0 if not in deliberation
}
```

**`events.go`** changes:

**Delete:**
- `GateType` enum and `GatePlanApproval` constant — the plan approval gate is now a configurable human gate at `GateBeforeWorker`
- `GateRequest` struct — replaced by `HumanChatGateRequest`
- `EventPlanReady` and `EventGateRequest` from `EventType` — both replaced by `EventHumanGate`
- `Event.Gate GateRequest` field — replaced by `Event.HumanGate HumanChatGateRequest`

**Add:**
- `EventHumanGate EventType` (after existing events)
- `EventAgentSkipped EventType` (for disabled phases)
- `HumanGate HumanChatGateRequest` field to `Event` struct (zero value = not set)
- `Loop int` field to `Event` struct (set on `EventPhaseChange` with `PhaseDeliberating`)

**`Phase` enum:**
- Add `PhaseDeliberating Phase = "deliberating"` — **comment added noting that this phase must be handled by any new switch statements on Phase** (no existing switch on Phase was found in the TUI)

**`engine.go`** `Input` struct changes:
- **Delete** the old `RestartInput` struct (engine.go:23-27) — replaced by the new typed version in `pipeline_setup.go`
- Replace `RestartFrom RestartInput` (old, with `FirstMissingAgent string`) with new typed `RestartFrom RestartInput` (with `Phase RestartPhase` + `DeliberationLoop int`)
- Add `Setup PipelineSetup` field
- **Delete** `AutoApprove bool`, `PlanFile string`, `NoExecute bool`, `Interactive bool` fields — all removed (see TUI-Only section). The `Input` struct shrinks to:
  ```go
  type Input struct {
      Prompt      string
      RestartFrom RestartInput
  }
  ```

**Zero-value safety for `Setup`:** In `run()`, apply a full zero-value fallback and always call `Validate()`:

```go
setup := input.Setup
if setup == (PipelineSetup{}) {
    setup = DefaultPipelineSetup() // all fields default: Research=true, DeliberationLoops=1, Execution=true, Validation=true
} else if setup.DeliberationLoops == 0 {
    setup.DeliberationLoops = DefaultPipelineSetup().DeliberationLoops // 1 — partial default for explicit-but-incomplete setup
}
if err := setup.Validate(); err != nil {
    emit(Event{Type: EventError, Err: fmt.Errorf("pipeline setup validation: %w", err)})
    return
}
```

> **CRITIQUE #4 FIX:** A fresh user who launches the app and presses Enter without opening the
> setup panel (^P) gets `Input{Prompt: "..."}` with zero-value `PipelineSetup{}`. The old code
> only defaulted `DeliberationLoops` to 1, leaving `Research`, `Execution`, `Validation` as false,
> which caused `Validate()` to fail. The fix checks if the **entire** `PipelineSetup` is zero-value
> and applies `DefaultPipelineSetup()` entirely. If `DeliberationLoops` is 0 but other fields are
> set (partial default), only `DeliberationLoops` is defaulted. `Validate()` is always called
> afterward.

> **Note:** `DefaultPipelineSetup()` returns `PipelineSetup{Research: true, DeliberationLoops: 1,
> Execution: true, Validation: true}`. This is the pipeline the user expects when they don't
> customize anything.

**Done when:** `go vet ./internal/orchestrator/` passes; `Validate()` errors on loops<1 or >10; `HumanGateSet.Active()` works correctly; `go test -race ./internal/orchestrator/` passes.

---

### WP1: Session directory helpers in `internal/agent/`

**`session.go`** additions — new methods on `SessionDir`:

```go
func (s SessionDir) ResearchDir() string { return filepath.Join(s.Path, "research") }
func (s SessionDir) DeliberationDir() string { return filepath.Join(s.Path, "deliberation") }
func (s SessionDir) LoopDir(n int) string {
    return filepath.Join(s.Path, "deliberation", fmt.Sprintf("loop_%02d", n))
}
func (s SessionDir) ExecutionDir() string { return filepath.Join(s.Path, "execution") }
func (s SessionDir) ValidationDir() string { return filepath.Join(s.Path, "validation") }
func (s SessionDir) GateDir(pos orchestrator.HumanGatePosition) string {
    switch pos {
    case orchestrator.GateBeforeDeliberation: return filepath.Join(s.Path, "gate_before_deliberation")
    case orchestrator.GateBeforeWorker: return filepath.Join(s.Path, "gate_before_worker")
    case orchestrator.GateBeforeValidator: return filepath.Join(s.Path, "gate_before_validator")
    case orchestrator.GateEndOfPipeline: return filepath.Join(s.Path, "gate_end_of_pipeline")
    }
    // Unreachable: HumanGatePosition switch is exhaustive (4 values).
    // panic is correct here — hitting this is a programming error.
    panic(fmt.Sprintf("unknown gate position: %d", pos))
}
```

**Fix for critique point #1:** The `default` case was changed from `return fmt.Errorf(...)` (compile error — string return type cannot return error) to `panic(...)`. The switch is exhaustive (all 4 `HumanGatePosition` values are covered), so the panic is provably unreachable at runtime. This is the idiomatic Go pattern for exhaustive switches on small enums.

Each sub-dir is created on first write (`os.MkdirAll` for subdirs — safe because subdirs are lazily created and may already exist).

> **CRITIQUE #6 FIX — `NewSessionDir` uses `os.Mkdir`, not `os.MkdirAll`:**
> The current code at `session.go:27` uses `os.MkdirAll(dir, 0o755)`. The plan changes this to
> `os.Mkdir` with explicit EEXIST handling:
>
> ```go
> func NewSessionDir(repoPath, slug string) (SessionDir, error) {
>     ts := time.Now().Format("2006-01-02-150405")
>     name := ts
>     if slug != "" {
>         name = ts + "-" + slug
>     }
>     dir := filepath.Join(repoPath, ".orqestra", "sessions", name)
>     if err := os.Mkdir(dir, 0o755); err != nil {
>         if !os.IsExist(err) {
>             return SessionDir{}, fmt.Errorf("creating session dir %s: %w", dir, err)
>         }
>         // Directory already exists — return it (idempotent)
>         return SessionDir{Path: dir}, nil
>     }
>     return SessionDir{Path: dir}, nil
> }
> ```
>
> This gives correct EEXIST semantics: if the dir exists, return it; if it doesn't exist, create it;
> if we can't create it (permission, etc.), return an error. The previous plan's claim about
> "preserving the existing EEXIST policy" was incorrect — `os.MkdirAll` has no EEXIST policy.

`writeArtifact` / `writeArtifactJSON` get a new overload accepting a dir path override, or the engine passes the correct sub-path directly.

**`run_config.json`** written at run start — `json.Marshal(setup)` → `session.ArtifactPath("run_config.json")`.

**Updated `AnalyzeRunCompleteness` (session.go):**

```go
func AnalyzeRunCompleteness(runPath string, detail RunDetail) RunCompleteness {
    var c RunCompleteness

    // Always new layout: read run_config.json to determine intended phases
    var setup PipelineSetup
    data, readErr := os.ReadFile(filepath.Join(runPath, "run_config.json"))
    if readErr != nil {
        c.Reason = "missing run_config.json"
        return c
    }
    json.Unmarshal(data, &setup)

    // Check research/researcher_meta.json (if Research enabled)
    // Check deliberation/loop_NN/architect_revision_meta.json for each loop
    // Check execution/worker_meta.json (if Execution enabled)
    // Check validation/validator_meta.json (if Validation enabled)
    // Return RestartPhase + DeliberationLoop
    // ... new-layout logic only, no old-format fallback ...
    return c
}
```

**`copyCompletedArtifacts`** updated for new directory layout — located in `internal/orchestrator/engine.go` (not `agent/session.go` as the original plan incorrectly stated).

> **Fix for critique point #6:** The function `copyCompletedArtifacts` is defined in `internal/orchestrator/engine.go:1519-1547`. The update goes there. After extraction, it moves to `engine_phases.go`.

**Done when:** `go test -race ./internal/agent/` passes; `AnalyzeRunCompleteness` returns correct `RestartPhase` for each failure scenario; runs without `run_config.json` are treated as incomplete.

---

### WP2: Orchestrator `engine.go` refactor

**engine.go actual line count: 1581 lines** (not ~1450 as previously estimated).

**Refactoring plan: 4 extracted files** (not 3) to keep each under 500 lines:
- `engine_deliberation.go` — deliberation loop logic (`runDeliberation`)
- `engine_phases.go` — research/execution/validation phase funcs
- `engine_restart.go` — restart logic (new file)
- Root `engine.go` — owns `Engine` struct, `Start`, `Run`, `run` entry point and channel wiring

**Structural changes to `run()`:**

1. Validate `input.Setup` at start of `run()` — fall back to `DefaultPipelineSetup()` if `DeliberationLoops == 0`, return `EventError` if validation fails.
2. Write `run_config.json` immediately after creating session dir.
3. **Phase the goto removal** (addresses critique point #7):
   - Phase 1: Extract deliberation loop into `runDeliberation()` — this isolates the architect↔critic cycle
   - Phase 2: Extract research/execution/validation blocks into `runResearch()`, `runExecution()`, `runValidation()`
   - Phase 3: Extract restart skip logic into `applyRestartSkip()`
   - Phase 4: Remove `goto skipPlanning` and `goto planGate`; replace with explicit conditional blocks calling the extracted functions
4. Each phase is a `Node` with `prev`/`next` pointers and `LaunchNext()` control — no separate DAGExecutor struct.

**Research phase:**
```go
if setup.Research {
    // ... existing researcher block, artifacts → session.ResearchDir()
} else {
    draftMarkdownForPlanning = input.Prompt
    emit(Event{Type: EventAgentSkipped, AgentID: "researcher"})
}
```

**Human gate: Before Deliberation** (after research, before first architect loop):
```go
if setup.HumanGates.Active(GateBeforeDeliberation) {
    d, err := runHumanGate(ctx, emit, decisions, HumanChatGateRequest{
        Position:       GateBeforeDeliberation,
        AgentSessionID: researchSessionID,
        AgentLabel:     "Researcher",
        HasPlanEditor:  true,
        PlanMarkdown:   draftMarkdownForPlanning,
        PlanFilePath:   "", // no plan-v*.md exists yet at deliberation start
    })
    if err != nil { return }
    if d.Type == DecisionCancel { emit(EventComplete cancelled); return }
    if d.EditedContent != "" { draftMarkdownForPlanning = d.EditedContent }
}
```

**Deliberation loop** (extracted into `engine_deliberation.go`):
```go
result, err := e.runDeliberation(ctx, setup, session, input, emit, decisions, stream, streamOut, draftMarkdownForPlanning)
if err != nil { return }
finalPlanMarkdown = result.planMarkdown
criticReportMarkdown = result.criticReport
finalPlanWarnings = result.warnings
planRevisionCount = result.planRevisionCount

// --- Plan approval gate (GateBeforeWorker) ---
if setup.HumanGates.Active(GateBeforeWorker) {
    // Emit gate with plan markdown, warnings, critic report
    // No PlanDiff, PlanHistoryDir, PlanHistoryHeadSHA (no git micro-repo)
    emit(Event{Type: EventHumanGate, HumanGate: HumanChatGateRequest{
        Position:       GateBeforeWorker,
        AgentSessionID: "",  // no session continuation — fresh sessions only
        AgentLabel:     "Architect",
        HasPlanEditor:  true,
        PlanMarkdown:   finalPlanMarkdown,
        PlanFilePath:   findHighestPlan(session.Path, ""), // highest plan-v*.md in session root
        PlanWarnings:   finalPlanWarnings,
        CriticReport:   criticReportMarkdown,
    }})

    select {
    case decision := <-decisions:
        switch decision.Type {
        case DecisionCancel:
            emit(Event{Type: EventComplete, Phase: PhaseDone})
            return
        case DecisionEdit:
            edited := strings.TrimSpace(decision.EditedContent)
            finalPlanMarkdown = edited
            finalPlanWarnings = agent.CheckPlanHealth(edited)
            // Write plan-v<N>.md to gate directory
            planRevisionCount++
            writeArtifactIn(session, "gate_before_worker",
                fmt.Sprintf("plan-v%d.md", planRevisionCount), edited)
            // Write dialog entry to gate_before_worker/dialog.md
            writeDialogEntryMarkdown(
                filepath.Join(session.Path, "gate_before_worker", "dialog.md"),
                "Human",
                decision.EditedContent,
            )
            if decision.Comment != "" {
                // Re-engage architect: fresh session via planner.Run().
                // Context = plan-v<max>.md (the edited plan just written)
                //        + dialog.md (full conversation since gate opened).
                // See runDeliberation for the identical revision block.
                maxPlan := findHighestPlan(session.Path, "gate_before_worker")
                dialogPath := filepath.Join(session.Path, "gate_before_worker", "dialog.md")
                dialogContext := readDialogMarkdown(dialogPath)
                gatePrompt := agent.ContinuePrompt(maxPlan, dialogContext+"\n\n"+decision.Comment)
                gatePrompt = guardPrompt(gatePrompt, input.Prompt, "architect")
                gatePlanner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
                gateResult, gateErr := gatePlanner.Run(ctx, gatePrompt, streamOut)
                if gateErr != nil {
                    return result, fmt.Errorf("architect gate revision: %w", gateErr)
                }
                if gateResult.Plan != "" {
                    finalPlanMarkdown = gateResult.Plan
                    finalPlanWarnings = agent.CheckPlanHealth(gateResult.Plan)
                    planRevisionCount++
                    writeArtifactIn(session, "gate_before_worker",
                        fmt.Sprintf("plan-v%d.md", planRevisionCount), finalPlanMarkdown)
                    writeDialogEntryMarkdown(dialogPath, "Architect",
                        fmt.Sprintf("plan-v%d.md (%d tokens)", planRevisionCount, gateResult.Usage.InputTokens+gateResult.Usage.OutputTokens))
                }
                if decision.Comment != "" || !decision.AutoApprove {
                    continue
                }
            }
        case DecisionComment:
            // Re-engage architect: fresh session via planner.Run().
            // Context = highest plan-v*.md + full dialog.md.
            // Same revision logic as DecisionEdit branch above.
            maxPlan := findHighestPlan(session.Path, "gate_before_worker")
            dialogPath := filepath.Join(session.Path, "gate_before_worker", "dialog.md")
            dialogContext := readDialogMarkdown(dialogPath)
            gatePrompt := agent.ContinuePrompt(maxPlan, dialogContext+"\n\n"+decision.Comment)
            gatePrompt = guardPrompt(gatePrompt, input.Prompt, "architect")
            gatePlanner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
            gateResult, gateErr := gatePlanner.Run(ctx, gatePrompt, streamOut)
            if gateErr != nil {
                return result, fmt.Errorf("architect gate revision: %w", gateErr)
            }
            if gateResult.Plan != "" {
                finalPlanMarkdown = gateResult.Plan
                finalPlanWarnings = agent.CheckPlanHealth(gateResult.Plan)
                planRevisionCount++
                writeArtifactIn(session, "gate_before_worker",
                    fmt.Sprintf("plan-v%d.md", planRevisionCount), finalPlanMarkdown)
                writeDialogEntryMarkdown(dialogPath, "Architect",
                    fmt.Sprintf("plan-v%d.md (%d tokens)", planRevisionCount, gateResult.Usage.InputTokens+gateResult.Usage.OutputTokens))
            }
            writeDialogEntryMarkdown(dialogPath, "Architect", decision.Comment)
            continue
        case DecisionApprove:
            // proceed to execution
        }
    case <-ctx.Done():
        return
    }
    break
}
```

**Human gate: Before Worker — plan approval gate** (addresses critique point #3):

`GateBeforeWorker` **is** the plan approval gate. There is no separate hardcoded gate. When `HumanGates.Active(GateBeforeWorker)`, a single `runHumanGate` call with `HasPlanEditor=true` handles the full plan approval flow:

1. Emit `EventHumanGate` with `HumanChatGateRequest` containing `PlanMarkdown`, `PlanWarnings`, `CriticReport`.
2. Wait on `decisions` channel for `DecisionApprove`, `DecisionEdit`, `DecisionComment`, or `DecisionCancel`.
3. On `DecisionEdit`: update `finalPlanMarkdown`, write `plan-v<N>.md` to gate directory, write dialog entry to `dialog.md`, re-engage architect (if comment provided with fresh session), re-show gate.
4. On `DecisionComment`: write dialog entry to `dialog.md`, re-engage architect (fresh session), re-show gate.
5. On `DecisionApprove`: break the gate loop, proceed to execution.
6. On `DecisionCancel`: emit cancelled event, return.

The gate loop semantics are identical to the current hardcoded `planGate` loop (lines 850-1161 of engine.go), but it runs only when `HumanGates.Active(GateBeforeWorker)`. When `GateBeforeWorker` is not configured, the gate is skipped entirely and the pipeline proceeds directly to execution.

**Execution phase:**
```go
if setup.Execution {
    // ... existing worker block, artifacts → session.ExecutionDir()
} else {
    emit(Event{Type: EventAgentSkipped, AgentID: "worker"})
}
```

**Human gate: Before Validator:**
```go
if setup.HumanGates.Active(GateBeforeValidator) && setup.Execution {
    d, err := runHumanGate(ctx, emit, decisions, HumanChatGateRequest{
        Position:       GateBeforeValidator,
        AgentSessionID: workResult.SessionID,
        AgentLabel:     "Worker",
        HasPlanEditor:  false,
    })
    if err != nil { return }
    if d.Type == DecisionCancel { ... return }
}
```

**Validation phase:**
> **CRITIQUE #1 FIX:** Validation does NOT introduce a new agent runner. It reuses the existing
> `PhaseSelfValidating` block from engine.go:1254–1382 (worker self-validation via session
> continuation). The `Validation` toggle on `PipelineSetup` controls whether that block runs.
> No new `Validator` runner field is added to `Engine.Runners`.

```go
if setup.Validation && setup.Execution {
    // Validation via fresh session (no session continuation):
    //   emit(Event{Type: EventPhaseChange, Phase: PhaseSelfValidating})
    //   valPrompt := agent.WorkerValidationPrompt(retryBudget, workResult.Output, workResult.SessionID)
    //   valSess, valErr := e.Runners.Worker.RunSession(ctx, valPrompt, "", valUpdates)
    // Write artifacts → session.ValidationDir()
    //   validator_meta.json, validator_session.jsonl, worker_validation.txt
} else {
    emit(Event{Type: EventAgentSkipped, AgentID: "validator"})
}
```

> **Note:** `KnownAgents` in `session.go:100` must NOT include "validator". The "validator" agent
> ID is an internal phase name used by the existing self-validation block; it runs via
> `RunSession` (fresh session), not via session continuation. The `Runners` struct uses concrete
> types with `RunSession` (Researcher, Architect, Critic = `*ClaudeCLI`; Worker = `*SandboxCLIRunner`).

**Human gate: End of Pipeline:**
```go
if setup.HumanGates.Active(GateEndOfPipeline) {
    gateSessionID := lastSessionID // validator or worker
    gateLabel := "Validator"
    if !setup.Validation { gateLabel = "Worker" }
    d, err := runHumanGate(ctx, emit, decisions, HumanChatGateRequest{
        Position:       GateEndOfPipeline,
        AgentSessionID: gateSessionID,
        AgentLabel:     gateLabel,
        HasPlanEditor:  false,
    })
    ...
}
```

**Restart logic** (extracted into `engine_restart.go`):
```go
if isRestart {
    switch input.RestartFrom.Phase {
    case RestartFromResearch:
        // start from beginning
    case RestartFromDeliberation:
        // load research artifacts, skip to loop input.RestartFrom.DeliberationLoop
        // load plan-v<N>.md from deliberation/loop_N-1/ as the starting plan for loop N
        // architect runs with fresh session, plan markdown as context
        //
        // Glob+sort to find the highest-numbered plan file:
        //   matches, err := filepath.Glob(filepath.Join(runPath, "deliberation",
        //       fmt.Sprintf("loop_%02d", input.RestartFrom.DeliberationLoop-1), "plan-v*.md"))
        //   if err == nil && len(matches) > 0 {
        //       sort.Strings(matches)
        //       startingPlan, _ = os.ReadFile(matches[len(matches)-1])
        //   }
        // The highest-numbered file is the correct restart point.
        // If no plan files found, restart from the beginning of loop N (safe default).
    case RestartFromExecution:
        // load research + all deliberation artifacts, skip to worker
    case RestartFromValidation:
        // load everything, skip to validator
    default:
        return fmt.Errorf("unknown restart phase: %d", input.RestartFrom.Phase)  // fail closed
    }
}
```

> **CRITIQUE #8 FIX — Artifact manifest for each directory:**
> The restart code must know the exact file names and paths. Here is the complete artifact manifest:
>
> **`research/`:**
> - `researcher_meta.json` — StepMeta for researcher
> - `researcher_draft.md` — researcher's output plan draft
> - `researcher_session.jsonl` — Claude CLI session log
>
> **`deliberation/loop_NN/` (one per loop iteration):**
> - `architect_initial_meta.json` — StepMeta for architect's initial pass (loop 1 only)
> - `architect_initial_session.jsonl` — Claude CLI session log for initial pass
> - `critic_meta.json` — StepMeta for critic
> - `critic_report.md` — critic's review report
> - `critic_session.jsonl` — Claude CLI session log
> - `architect_revision_meta.json` — StepMeta for architect's revision
> - `architect_revision_session.jsonl` — Claude CLI session log for revision
> - `plan-v<N>.md` — numbered plan files, highest-numbered is current
>
> **`execution/`:**
> - `worker_meta.json` — StepMeta for worker
> - `worker_output.txt` — worker's output
> - `worker_session.jsonl` — Claude CLI session log
>
> **`validation/`:**
> - `validator_meta.json` — StepMeta for self-validation
> - `validator_session.jsonl` — Claude CLI session log
> - `worker_validation.txt` — parsed validation output
>
> **`gate_*/`:**
> - `gate_decision.json` — user's decision at the gate
> - `dialog.md` — human-agent conversation log (multi-section Markdown)
>
> **`run_config.json`** (in session root): PipelineSetup JSON for layout detection and phase status
>
> **RestartFromDeliberation specifics:**
> - Load the highest-numbered `plan-v<N>.md` from `deliberation/loop_N-1/` as the starting plan for loop N
> - If no plan files found, restart from the beginning of loop N (safe default)
> - Architect runs with fresh session, plan markdown as context (no session continuation needed)

**`copyCompletedArtifacts` new layout only** (BR-3 FIX):

The function is defined in `internal/orchestrator/engine.go:1519-1547`. It always uses new-layout logic:

```go
func copyCompletedArtifacts(src, dst string) error {
    if src == "" || dst == "" {
        return nil
    }

    // Always new layout: read run_config.json to determine which phases ran
    var setup PipelineSetup
    data, _ := os.ReadFile(filepath.Join(src, "run_config.json"))
    json.Unmarshal(data, &setup)

    // Copy from subdirectories based on enabled phases
    if setup.Research {
        copyDirContents(src, dst, "research")
    }
    if setup.DeliberationLoops > 0 {
        for loop := 1; loop <= setup.DeliberationLoops; loop++ {
            loopSrc := filepath.Join(src, "deliberation", fmt.Sprintf("loop_%02d", loop))
            loopDst := filepath.Join(dst, "deliberation", fmt.Sprintf("loop_%02d", loop))
            if fileExists(loopSrc) {
                copyDirContents(loopSrc, loopDst, ".")
            }
        }
    }
    if setup.Execution {
        copyDirContents(src, dst, "execution")
    }
    if setup.Validation {
        copyDirContents(src, dst, "validation")
    }
    // Copy gate directories for any active gates
    for _, pos := range setup.HumanGates {
        gateSrc := filepath.Join(src, gateDirName(pos))
        gateDst := filepath.Join(dst, gateDirName(pos))
        if fileExists(gateSrc) {
            copyDirContents(gateSrc, gateDst, ".")
        }
    }
    return nil
}

// copyDirContents copies all files from src/subdir to dst/subdir.
func copyDirContents(src, dst, subdir string) {
    srcDir := filepath.Join(src, subdir)
    dstDir := filepath.Join(dst, subdir)
    entries, _ := os.ReadDir(srcDir)
    for _, e := range entries {
        if e.IsDir() {
            continue
        }
        srcPath := filepath.Join(srcDir, e.Name())
        dstPath := filepath.Join(dstDir, e.Name())
        copyFile(srcPath, dstPath)
    }
}
```

> **Key design:** `run_config.json` is the source of truth for layout detection and phase status.
> Runs without `run_config.json` are treated as incomplete — `copyCompletedArtifacts` returns nil
> (no copies made). The `copyDirContents` helper handles the per-subdirectory copy without
> file-by-file hardcoding.

```go
func runHumanGate(
    ctx context.Context,
    emit func(Event),
    decisions <-chan Decision,
    req HumanChatGateRequest,
) (*Decision, error) {
    emit(Event{Type: EventHumanGate, HumanGate: req})
    select {
    case d := <-decisions:
        return &d, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

**Headless mode removal (see TUI-Only section):** The `runHumanGate` function no longer takes `interactive` or `autoApprove` parameters. All runs are interactive TUI runs — the human gate always blocks for user input. Context cancellation is the only non-interactive exit path.

**Degradation matrix (no NIH — reuse existing decision channel):**
- TUI mode: wait for user action on `decisions` channel.
- Context cancellation: return `ctx.Err()`.

**Done when:** `go test -race ./internal/orchestrator/` passes; deliberation loop runs N times; restart from each phase works; 1581-line `run()` is refactored into 4 files each under 500 lines.

---

### WP3: Pipeline setup panel as overlay on prompt screen

**No new file needed.** The setup panel lives within the existing prompt screen (no `StateSetup`, no `StateSetup` in `AppState`).

**`Model` struct changes:**
- Add `setupOpen bool` — tracks whether the setup panel is open (focused).
- Add `currentSetup PipelineSetup` — stores the user's pipeline configuration.

**`AppState` enum:**
```go
const (
    StatePrompt     AppState = iota  // prompt entry + setup overlay panel
    StatePipeline                    // 3-zone split layout (pipeline running/done)
    StateRunsList                    // historical runs list
    StateRunDetail                   // detail view for a single historical run
)
```

**Setup panel rendering:**
- The setup panel is rendered above the prompt textarea in `StatePrompt`.
- When `setupOpen` is false (default), the panel shows in low-contrast text with `^P Pipeline Setup` hint.
- When `setupOpen` is true, the panel is fully visible with editable controls.

**Key handling in `StatePrompt`:**
```
^P        → toggle setupOpen (open/close panel)
↑ / ↓     → when setupOpen: move cursor through setup rows; otherwise: no-op (prompt textarea handles it)
← / →     → when setupOpen: toggle bool / adjust DeliberationLoops; otherwise: no-op
Enter     → when setupOpen: confirm and close panel; otherwise: submit prompt
Esc       → when setupOpen: close panel; otherwise: (existing behavior)
```

**Setup panel content:**
```
  Research:              ◁ Enabled ▷
  Architect <-> Critic:  ◁ 4 ▷
  Execution:             ◁ Enabled ▷
  Validation:            ◁ Enabled ▷
  Human Review:          [ Before Worker, End of Pipeline ]

  (when setupOpen, sub-control replaces footer area)
  [ ] Before Architect ↔ Critic
  [x] Before Worker
  [ ] Before Validator
  [x] End of Pipeline
  ↑↓ navigate | ←→/Space toggle | Esc back
```

**New intent** in `messages.go`:
```go
type ConfirmSetupIntent struct {
    Setup orchestrator.PipelineSetup
}
func (ConfirmSetupIntent) isIntent() {}
```

**`processIntent` handles `ConfirmSetupIntent`:** saves `m.currentSetup = i.Setup`, closes panel, transitions to pipeline.

**`recalculateLayout` in `StatePrompt`:** accounts for the setup panel height when calculating prompt textarea dimensions.

**`startPipeline`:** passes `Setup: m.currentSetup` into `orchestrator.Input`. When `m.currentSetup` is not yet set (first run without setup), uses `Setup: orchestrator.DefaultPipelineSetup()` as the fallback.

**`startPipelineRestart`:** updated signature:
```go
func (m *Model) startPipelineRestart(prompt, runPath string, phase orchestrator.RestartPhase, loop int) tea.Cmd
```
Uses `RestartFrom: orchestrator.RestartInput{RunPath: runPath, Phase: phase, DeliberationLoop: loop}`.

**Done when:** app shows prompt screen with setup overlay; setup flows to pipeline; pipeline runs with configured setup; restart from each phase works.

---

### WP4: AppState and model wiring

**`model.go`** changes:

1. **Remove** `StatePlanHistoryDetail` from `AppState` enum (plan history viewer removed).
2. **Remove** `StateSetup` — setup is an overlay on `StatePrompt`, not a separate state.
3. **Add** `setupOpen bool` to `Model` struct — tracks whether the setup panel is focused.
4. **Add** `currentSetup PipelineSetup` to `Model` struct — stores the user's pipeline configuration.
5. **Remove** `StatePlanHistoryDetail` from `AppState` enum.
6. **Remove** `ContentPlanHistory` from `ContentMode` enum.
7. **Remove** `planHistoryScreen PlanHistoryScreen` field from `Model` struct.
8. **Remove** `planHistoryVisible()` method.
9. **Remove** `handlePlanHistoryKey()` method.
10. **Remove** all `planHistoryScreen` update/view/layout calls.

**`RestartRunIntent`** in `messages.go`:
```go
type RestartRunIntent struct {
    RunPath  string
    Phase    orchestrator.RestartPhase
    Loop     int  // 1-based deliberation loop; 0 if not in deliberation
}
func (RestartRunIntent) isIntent() {}
```

**`Model` struct changes:**
- Replace `lastRestartFirstMissingAgent string` with `lastRestartPhase orchestrator.RestartPhase` and `lastRestartLoop int`.

**`RunDetailScreen` restart button:** Calls `AnalyzeRunCompleteness` → reads new `RunCompleteness` → populates `RestartRunIntent{Phase: completeness.RestartPhase, Loop: completeness.DeliberationLoop}`.

**Done when:** app shows prompt screen with setup overlay; setup flows to pipeline; pipeline runs with configured setup; restart from each phase works.

---

### WP5: HumanChatMode sub-models for human gates

**New file `internal/tui/mode_human_chat.go`:**

```go
type HumanChatMode interface {
    Update(msg tea.Msg) (HumanChatMode, tea.Cmd)
    View(width int) string
    Footer() string
    Pending() tea.Msg
}

// PlanChatMode — HasPlanEditor=true gates (GateBeforeDeliberation, GateBeforeWorker)
type PlanChatMode struct {
    agentLabel   string
    plan         string
    planFilePath string    // path to highest plan-v*.md on disk (for $EDITOR via Ctrl+E)
    chatHistory  []ChatEntry
    planComment  textarea.Model
    // ... existing plan review fields migrated here
    pending      tea.Msg
}

// SimpleChatMode — HasPlanEditor=false gates (GateBeforeValidator, GateEndOfPipeline)
type SimpleChatMode struct {
    agentLabel  string
    chatHistory []ChatEntry
    input       textarea.Model
    pending     tea.Msg
}
```

Constructor: `newHumanChatMode(req orchestrator.HumanChatGateRequest, width int) HumanChatMode`:
- Returns `PlanChatMode` when `req.HasPlanEditor`
- Returns `SimpleChatMode` otherwise
- `PlanChatMode.planFilePath` is set from `req.PlanFilePath` (path to highest `plan-v*.md` on disk)

**PipelineScreen** changes:
- Add `ContentHumanGate ContentMode` value to enum
- Add one field: `activeChat HumanChatMode` (nil when not in chat mode)
- **Fix for critique point #6 (dual-gate event handling):** `ApplyEvent` handles `EventHumanGate`:
  ```go
  case orchestrator.EventHumanGate:
      s.activeChat = newHumanChatMode(event.HumanGate, s.width)
      s.content = ContentHumanGate
  ```
  `EventGateRequest` and `EventPlanReady` are removed. All gates now use `EventHumanGate`. The `GateBeforeWorker` position carries the plan approval semantics (edit/comment/approve/cancel with architect re-engagement).
- `Update(msg tea.Msg)` (signature already accepts `tea.Msg`): when `content == ContentHumanGate`, delegate to `s.activeChat.Update(msg)`, check `Pending()`, drain to `s.PendingIntent`
- `View`: renders `s.activeChat.View(width)` when `content == ContentHumanGate`

> **CRITIQUE #4 FIX — `viewFooter()` wiring:**
> Add a `case ContentHumanGate:` in `viewFooter()` (screen_pipeline_keys.go:103-165):
> ```go
> case ContentHumanGate:
>     return s.activeChat.Footer() + "  [^H] help  " + ctrlCHint
> ```
> Without this, `ContentHumanGate` falls to `default` which shows navigation hints instead of
> gate-specific hints. Also add `case ContentHumanGate:` to the `Update()` switch on `s.content`
> (screen_pipeline.go:571-598) — currently unhandled.

**Intents** from chat modes:
```go
type HumanGateChatIntent struct {
    Position      orchestrator.HumanGatePosition
    EditedPlan    string  // non-empty if plan was edited
    Comment       string  // message to send to agent
    Cancelled     bool
}
func (HumanGateChatIntent) isIntent() {}
```

`processIntent` in `model.go` handles `HumanGateChatIntent`:
- If `Cancelled`: send `Decision{Type: DecisionCancel}` on `m.decisions`
- If `EditedPlan != ""`: send `Decision{Type: DecisionEdit, EditedContent: ...}`
- If `Comment != ""`: send `Decision{Type: DecisionComment, Comment: ...}`
- Resume waiting for events: `waitForEvent(m.events)`

**Chat input behavior:**
- The chat textarea uses the **same control** as the initial prompt textarea — same widget, same rendering, same key bindings.
- `Enter` submits the message to the agent.
- `^O` collapses the chat (toggles `^O` collapsed/expanded state).
- When collapsed, the chat textarea disappears; only pipeline history and agent output remain visible.

**Done when:** `go test -race ./internal/tui/` passes; simple chat and plan chat modes render and route intents correctly; PipelineScreen field count does not grow by more than 2.

> **CRITIQUE #7 FIX — `EventAgentSkipped` handled in TUI `ApplyEvent`:**
> Add a `case orchestrator.EventAgentSkipped:` in `ApplyEvent` (screen_pipeline.go:326-494):
>
> ```go
> case orchestrator.EventAgentSkipped:
>     s.agents = append(s.agents, AgentRow{
>         ID:            event.AgentID,
>         State:         AgentStateSkipped,
>         StartedAt:     time.Now(),
>     })
>     s.frameList.AppendFrame(Frame{
>         Kind:    AgentFrame,
>         State:   FrameFinished, // reuse FrameFinished — skipped frames have no content to render
>         AgentID: event.AgentID,
>     })
> ```
>
> **New types needed (add to model.go and frame.go respectively):**
> - `model.go`: Add `AgentStateSkipped AgentState = "skipped"` to the `AgentState` const block (after `AgentStateGate`).
> - `frame.go`: **Do NOT add a new `FrameSkipped` value.** Reuse `FrameFinished` for skipped frames. Skipped frames have no content to render, no elapsed time, and no token data — `FrameFinished` semantically matches (a frame that is done with no further activity). Adding a separate `FrameSkipped` would require a new `case` in every `FrameState` switch throughout the TUI, increasing maintenance risk for no visual benefit.
>
> Without this, skipped phases (e.g., Research disabled) are silently ignored — the agent won't
> appear in the sidebar, making it unclear which phases were intentionally skipped.

---

### WP6: Tests

**`internal/orchestrator/pipeline_setup_test.go`:**
- Table-driven `Validate()` tests: loops=0→error, loops=11→error, loops=1→ok, all disabled→error
- `HumanGateSet.Active()`: position present→true, absent→false, empty set→false
- `DefaultPipelineSetup()`: Research=true, DeliberationLoops=1, Execution=true, Validation=true
- `RestartInput` replacement: verify old `FirstMissingAgent` field no longer compiles

**`internal/orchestrator/engine_deliberation_test.go`:**
- `TestEngine_Deliberation_OneLoop`: existing behavior preserved
- `TestEngine_Deliberation_ThreeLoops`: architect runs 3 times, critic 3 times
- `TestEngine_Deliberation_SkipResearch`: prompt fed directly to architect
- `TestEngine_Deliberation_SkipExecution`: plan gate reached, worker skipped
- `TestEngine_Deliberation_HumanGateBeforeWorker`: gate event emitted, decision unblocks pipeline
- `TestEngine_Deliberation_Cancellation`: cancelling context stops all goroutines and returns error
- `TestEngine_Deliberation_HumanGateCancel`: cancelling during human gate returns error, does not leak goroutines

**`internal/orchestrator/engine_phases_test.go`:**
- `TestEngine_Phase_SetupZeroValueFallback`: `Input{Prompt: "..."}` without `Setup` uses `DefaultPipelineSetup()`

**`internal/orchestrator/engine_restart_test.go`:**
- `TestEngine_Restart_FromDeliberation`: loads research artifacts, skips to specified loop
- `TestEngine_Restart_FromExecution`: loads research + deliberation, skips to worker
- `TestEngine_Restart_FromValidation`: loads everything, skips to validator

**`internal/agent/session_test.go`:**
- `TestAnalyzeRunCompleteness_NewLayout`: new artifact paths recognized
- `TestAnalyzeRunCompleteness_PartialDeliberation`: loop 2 of 3 failed → RestartFromDeliberation, Loop=2
- `TestAnalyzeRunCompleteness_NoRunConfig`: missing `run_config.json` → incomplete, no restart
- `TestNewSessionDir_Exists`: calling NewSessionDir twice with the same slug returns the same dir (idempotent)
- `TestNewSessionDir_PermissionDenied`: if parent dir is unwritable, returns error (not silent success)

**`internal/orchestrator/human_gate_test.go`:**
- `TestHumanGateSet_Active`: position present→true, absent→false, empty set→false

**`internal/orchestrator/restart_test.go`:**
- `TestRestartPhase_String`: each RestartPhase has a readable string

**`internal/harness/session_test.go`:**
- `TestSession_interface`: verifies `*ClaudeCLI` and `*SandboxCLIRunner` implement `Session`
- `TestSession_zero_values`: verifies zero-value safety of all `Session` methods
- `TestParseStream_basic`: verifies NDJSON stream parsing produces correct `Session` metadata
- `TestNDJSON_marshal_format`: verifies `Post()` NDJSON marshal format matches companion repo wire format
- `TestSession_Post_Then_Kill`: verifies `Post()` + `Kill()` lifecycle

**`internal/tui/screen_setup_test.go`:**
- Navigation: cursor moves up/down within bounds
- Toggle: left/right toggles Research/Execution/Validation
- Stepper: left/right on Deliberation row clamps at 1 and 10
- Sub-control: enter opens Human Review, Esc closes preserving state
- Confirm: Enter emits `ConfirmSetupIntent` with correct `PipelineSetup`
- Validation error: invalid setup shows error, does not emit intent

---

### WP7: Session — bidirectional streaming (production-ready)

**Status:** `RunInteractive` implemented and proven working in `internal/harness/interactive_cli.go`. Consolidated into `Session` interface (see Runner Consolidation section).

**Implementation:**

1. **`Session` interface** in `internal/harness/session.go` (new file, shared code):

```go
type Session interface {
    Post(msg string) error
    Done() <-chan error
    Updates() <-chan StreamUpdate
    Usage() TokenUsage
    ResultError() bool
    SessionID() string
    PlanPath() string
    Kill() error
}
```

2. **`*ClaudeCLI.RunSession`** in `internal/harness/claude_cli.go`:

```go
func (c *ClaudeCLI) RunSession(ctx context.Context, prompt, systemPrompt string,
    updates chan<- StreamUpdate) (Session, error)
```

   Returns `*interactiveSession` implementing `Session`.

3. **`*SandboxCLIRunner.RunSession`** in `internal/harness/sandbox_cli_runner.go`:

```go
func (r *SandboxCLIRunner) RunSession(ctx context.Context, prompt, systemPrompt string,
    updates chan<- StreamUpdate) (Session, error)
```

   Returns `*sandboxSession` (wraps `*interactiveSession` + sandbox cleanup) implementing `Session`.

4. **Shared `parseStream()`** in `internal/harness/session.go`: single source of truth for NDJSON stream parsing. Used by both `ClaudeCLI` and `SandboxCLIRunner`.

5. **Start command:**
```sh
claude -p "<prompt>" --output-format stream-json --input-format stream-json --verbose --include-partial-messages
```

6. **After `cmd.Start()`:**
   - Capture `cmd.StdoutPipe()` — parse NDJSON output via `parseStream()`
   - Capture `cmd.StdinPipe()` — used by `Post()` for NDJSON writes
   - Create `updates` channel (buffered, 512) — `parseStream()` writes to it
   - Create `done` channel — closed when `cmd.Wait()` returns
   - Extract `sessionID`, `planPath`, `usage`, `resultError` from stream events
   - **Send initial prompt as NDJSON via stdin, then close stdin** to signal EOF
   - Return `Session`

7. **NDJSON input format (verified against companion repo):**
```json
{"type":"user","message":{"role":"user","content":"<msg>"},"parent_tool_use_id":null,"session_id":"<session_id>"}
```

8. **Critical: `--print` flag must NOT be passed.** The `--print` flag forces one-shot mode.

**Unit tests (in `interactive_cli_test.go`):**
- `TestSession_interface` — verifies `*ClaudeCLI` and `*SandboxCLIRunner` implement `Session`
- `TestSession_zero_values` — verifies zero-value safety of all `Session` methods
- `TestTokenUsage_Total` — verifies `TokenUsage.Total()` computation
- `TestNDJSON_marshal_format` — verifies `Post()` NDJSON marshal format

**Integration test:**
```go
func TestRunSession_Bidirectional(t *testing.T) {
    if !isIntegrationTest() {
        t.Skip("requires -tags integration")
    }

    runner := harness.NewClaudeCLI( /* ... */ )
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    sess, err := runner.RunSession(ctx, "You are a test assistant. Reply with exactly 'ack.'", "", nil)
    require.NoError(t, err)
    defer sess.Kill()

    select {
    case <-sess.Done():
        t.Fatal("process exited early")
    case update := <-sess.Updates():
        assert.NotEmpty(t, update.Text, "expected initial response")
    case <-ctx.Done():
        t.Fatal("timeout")
    }

    err = sess.Post("Hello")
    require.NoError(t, err)

    select {
    case <-sess.Done():
        t.Fatal("process exited after follow-up")
    case update := <-sess.Updates():
        assert.Contains(t, update.Text, "ack")
    case <-ctx.Done():
        t.Fatal("timeout")
    }

    assert.NotEmpty(t, sess.SessionID())
    assert.NotZero(t, sess.Usage().Total())
}
```

**Acceptance criteria:**
- `RunSession` starts the Claude CLI with `--input-format stream-json` (verified: implemented)
- `Session.Post(msg)` writes NDJSON to stdin (verified: implemented)
- Initial prompt produces a response (verified: `parseStream()` drains stdout)
- Follow-up via `sess.Post("Hello")` produces a response (verified: live testing)
- `sess.Kill()` terminates process (verified: implemented)
- `sess.Done()` closes on exit (verified: implemented)
- `sess.Updates()` returns `StreamUpdate` values (verified: implemented)
- `sess.Usage()`, `sess.SessionID()`, `sess.PlanPath()`, `sess.ResultError()` correct (verified: implemented)
- Unit tests pass: `go test -race ./internal/harness/ -run TestSession|TestTokenUsage|TestNDJSON -v`
- Integration test passes: `make test-integration -run TestRunSession_Bidirectional`

**Done when:** All unit tests pass; integration test passes with `-tags integration`.

---

## Files Modified

| File | Change |
|---|---|
| `internal/orchestrator/pipeline_setup.go` | **NEW** — `PipelineSetup`, `DefaultPipelineSetup()`, `Validate()` |
| `internal/orchestrator/human_gate.go` | **NEW** — `HumanGatePosition`, `HumanGateSet`, `HumanChatGateRequest` (includes `PlanFilePath`), `gateDirName()` helper |
| `internal/orchestrator/restart.go` | **NEW** — `RestartPhase`, `RestartInput` (replaces old engine.go:23-27) |
| `internal/orchestrator/events.go` | Add `EventHumanGate`, `EventAgentSkipped`, `PhaseDeliberating`; add `HumanGate HumanChatGateRequest`, `Loop int` to `Event`; **delete `GateType`, `GatePlanApproval`, `GateRequest`, `EventPlanReady`, `EventGateRequest`, `Event.Gate`** |
| `internal/orchestrator/engine.go` | `Input` shrinks to `Prompt` + `RestartFrom`; `Setup PipelineSetup` field added; delete `AutoApprove`, `PlanFile`, `NoExecute`, `Interactive` fields; delete old `RestartInput` struct (line 23-27); delete all `planRepo` references; delete `PlanDiff`, `PlanHistoryDir`, `PlanHistoryHeadSHA` from gate emission; add `PlanFilePath` to `HumanChatGateRequest` emissions; move `copyLog`, `logAgentEvent`, `logClaudeSession`, `logClaudeSessionPre` from `run()` closures to package-level; add `findHighestPlan` and `readDialogMarkdown` helpers; delete `runPlanner`, `runRunnerStreaming`, `runRunnerContinue` helpers; `Runners` struct uses concrete types (`*ClaudeCLI`, `*SandboxCLIRunner`) with `RunSession`; all agent calls use `planner.Run()` or `runner.RunSession()` directly |
| `internal/orchestrator/engine_deliberation.go` | **NEW** — deliberation loop logic (`runDeliberation`) extracted |
| `internal/orchestrator/engine_phases.go` | **NEW** — `runResearch`, `runExecution`, `runValidation` phase funcs extracted; package-level helpers: `writeArtifactIn`, `writeArtifactJSONIn`, `writeDialogEntryMarkdown`, `findHighestPlan`, `readDialogMarkdown` |
| `internal/orchestrator/engine_restart.go` | **NEW** — restart skip logic extracted |
| `internal/plan/gitrepo.go` | **DELETE** — git micro-repo removed |
| `internal/plan/gitrepo_history.go` | **DELETE** — git micro-repo history reader removed |
| `internal/plan/spec.go` | No changes (markdown serialisation still valid) |
| `internal/agent/session.go` | Add sub-dir helpers (including fixed `GateDir` with panic); update `AnalyzeRunCompleteness` for new-layout-only; remove `FirstMissingAgent` from `RunCompleteness` |
| `internal/harness/session.go` | **NEW** — `Session` interface, shared `parseStream()`, `TokenUsage` methods, `interactiveSession` struct |
| `internal/harness/claude_cli.go` | **REDUCED** — `RunSession` replaces `RunPrint`/`RunStreaming`/`RunContinue`; `RunSession` returns `Session`; deletes obsolete methods; keeps options, `buildFinalArgs`, `buildEnv` |
| `internal/harness/sandbox_cli_runner.go` | **REDUCED** — `RunSession` replaces `RunPrint`/`RunStreaming`/`RunContinue`; returns `*sandboxSession` wrapping `Session`; deletes `extractJSONUsage`/`extractStreamUsage`/`extractStreamSessionID`/`extractStreamResult` (replaced by `Session` methods); keeps sandbox-specific process wrapper |
| `internal/harness/interactive_cli.go` | **MERGED** — `InteractiveSession` → `interactiveSession` in `session.go`; `RunInteractive` → `RunSession`; `InteractiveRunner` interface deleted |
| `internal/agent/planner.go` | `Planner` uses `*ClaudeCLI` with `RunSession`; `Continue` method deleted (fresh session + prompt context); `Run` calls `planner.runner.RunSession()` |
| `internal/plan/gitrepo.go` | **DELETE** — git micro-repo removed |
| `internal/tui/plan_history_loader.go` | **DELETE** — plan history viewer removed |
| `internal/tui/screen_plan_history.go` | **DELETE** — plan history viewer removed |
| `internal/tui/screen_plan_history_test.go` | **DELETE** — plan history tests removed |
| `internal/tui/plan_history_model_test.go` | **DELETE** — plan history model tests removed |
| `internal/tui/screen_run_detail_keys.go` | Remove Ctrl+Y `OpenPlanHistoryIntent` handler |
| `internal/tui/model.go` | Add `setupOpen bool`, `currentSetup PipelineSetup`; remove `StatePlanHistoryDetail`, `ContentPlanHistory`, `planHistoryScreen`; wire `ConfirmSetupIntent`, `RestartRunIntent` changes; update `recalculateLayout` for setup panel; add `AgentStateSkipped`; add `ContentHumanGate` to ContentMode enum |
| `internal/tui/messages.go` | Add `ConfirmSetupIntent`, `HumanGateChatIntent`; remove `OpenPlanHistoryIntent`, `ClosePlanHistoryIntent`; update `RestartRunIntent` fields |
| `internal/tui/screen_pipeline.go` | Add `ContentHumanGate`, `activeChat HumanChatMode`; handle `EventHumanGate` and `EventAgentSkipped` in `ApplyEvent`; add `ContentHumanGate` case in `Update()` switch |
| `internal/tui/screen_pipeline_keys.go` | Add `ContentHumanGate` case in `viewFooter()`; update `ContentPlanReview` exclusion in `handleStreamingKey` to also exclude `ContentHumanGate` |
| `internal/tui/frame.go` | Do NOT add `FrameSkipped` — reuse `FrameFinished` for skipped frames (avoids new case in every FrameState switch) |
| `internal/tui/mode_human_chat.go` | **NEW** — `HumanChatMode` interface, `PlanChatMode` (includes `planFilePath`), `SimpleChatMode` |
| `cmd/orqestra/main.go` | Remove `--prompt`, `--auto-approve`, `--auto-reject`, `--auto-init`, `--plan`, `--no-execute`, `--json` flags; remove `isHeadless` logic; remove `RunHeadless`/`RunHeadlessPlanOnly` calls; remove `runPlanOnly`, `runValidateOnly`, `runExecOnly`; keep `--config` and `runInitCommand` |
| `internal/tui/tui.go` | Remove `RunHeadless` and `RunHeadlessPlanOnly` functions |
| `README.md` | Remove headless run section and `--prompt`/`--auto-approve`/`--auto-reject`/`--auto-init`/`--plan`/`--no-execute`/`--json` CLI flags |
| `.github/copilot-instructions.md` | Remove headless smoke test; rename "Debugging E2E And Headless Runs" section; update `internal/plan/` description |

---

## Verification

```sh
make build
make test                  # unit tests with race detector
make test-integration      # needs git + go in PATH
make lint
```

Manual:
1. `make run` — prompt screen appears with setup overlay panel
2. Press `^P` to open setup panel, adjust toggles and loop count, confirm → prompt
3. Enter prompt → pipeline runs with configured phases
4. With `DeliberationLoops=3`: sidebar shows architect/critic cycling 3 times
5. Enable "Before Worker" gate: plan gate appears with split view (pipeline history + agent output + chat)
6. Enable "Before Validator" gate: simple chat appears after worker
7. Disable Research: architect receives raw prompt
8. Disable Execution: pipeline stops after plan gate
9. `^O` on gate: collapses chat, shows only pipeline history + agent output
10. Ctrl+R in runs list: restarting a partially-complete multi-loop run resumes from correct loop

Integration test verification:
11. `make test-integration -run TestRunSession_Bidirectional` — verifies bidirectional streaming via `--input-format stream-json`

---

## Known Risks

- `engine.go` is 1581 lines. This plan extracts into 4 files; keep each under 500 lines.
- `PipelineScreen` has 54+ fields and is flagged as the "canonical mode-state-flattening offender" in tui-instructions. This plan adds 1 field (`activeChat HumanChatMode`). The full decomposition (moving plan review fields into `PlanChatMode`) is follow-up work; for this change, `PlanChatMode` shares fields with `PipelineScreen` as a documented known limitation.
- The plan approval gate is now a configurable human gate at `GateBeforeWorker`. There is no separate hardcoded `GatePlanApproval`. When `HumanGates` includes `GateBeforeWorker`, the plan approval gate runs with full edit/comment/approve/cancel semantics. When it is not configured, the gate is skipped entirely. `GateBeforeDeliberation` is a separate human gate position (chat with researcher before deliberation starts).
- Restart from mid-deliberation requires reading the previous loop's highest-numbered `plan-v<N>.md` for the starting plan. No `planSessionID` needed — architect runs with fresh session, plan markdown as context.
- Old-format runs (without `run_config.json`) are no longer restartable. They appear as incomplete in the TUI.
- **Plan revision files:** Each architect pass writes a new `plan-v<N>.md` file. The highest-numbered file is the current plan. Plan history is visible as numbered files on disk; no git micro-repo.
- **Markdown dialog:** Each gate directory contains a `dialog.md` file capturing human-agent interactions in multi-section Markdown format. The human gate agent owns its dialog data.
- **Every agent uses a fresh session:** Both architect and critic run in fresh sessions on every call. Plan markdown is passed as prompt context. This eliminates session continuation complexity but means each agent call is a full invocation (no session reuse).
- **`Session` NDJSON format is verified:** The `--input-format stream-json` stdin format is implemented and tested in `internal/harness/interactive_cli.go` (now `internal/harness/session.go`). The NDJSON input format was reverse-engineered against [The-Vibe-Company/companion](https://github.com/The-Vibe-Company/companion) and validated with live Claude CLI testing. The format is confirmed working. A remaining risk is that future Claude CLI versions might change the NDJSON wire format without notice — guard against this with integration tests and version pinning if needed.
- **External editor integration:** The `Ctrl+E` flow opens the highest-numbered `plan-v*.md` in the user's `$EDITOR`/`$VISUAL`. After the user saves and exits, the chat message must read the plan file to detect inline comments (`<<-- [comment]`). This requires file I/O in the TUI — must be non-blocking (run in a `tea.Cmd`).
- **Revision request activation:** `Ctrl+Enter` is only active when `dialog.md` or the highest-numbered `plan-v*.md` (found by glob, sorted, last entry) has changed. The gate tracks SHA256 hashes to determine this. If the gate is shown for the first time (no prior changes), `Ctrl+Enter` should be disabled to prevent confusion.
- **`Session` unifies all runner patterns:** `RunSession` replaces `RunPrint`, `RunStreaming`, `RunContinue`, and `RunInteractive`. Both `ClaudeCLI` and `SandboxCLIRunner` implement `Session`. No type assertions needed — every runner has `RunSession(ctx, prompt, systemPrompt, updates) (Session, error)`. The orchestrator calls `planner.Run()` for planners and `runner.RunSession()` for workers.
