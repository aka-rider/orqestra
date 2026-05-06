# Plan: Orqestra End-to-End Interactive Pipeline (v2)

**TL;DR**: Wire 4 sequential interactive Claude Code agents (intake → planner → validator → worker) through the TUI, each in its own tab, with artifact passing via session directory files, system prompts templated with absolute paths, and all agents interactive.

---

## Current State (2026-05-06)

**Done:**

- `go build ./cmd/orqestra` ✓
- `go test ./internal/tui/...` ✓
- Junk files cleaned (test_keys.go, update_test.go, .orig, .rej)
- `cmd/sandbox` fixed: `Foreground=true` + `Ctty` enables interactive TTY in sandbox

**Proven working (cmd/sandbox):**

```bash
go run ./cmd/sandbox --workspace=$(pwd) \
  --anthropic-base-url=http://192.168.50.212:11434 \
  --anthropic-auth-token=dummy \
  --anthropic-model=qwen3.6 \
  --env=DISABLE_NON_ESSENTIAL_MODEL_CALLS=1 \
  --env=CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
  -- claude
```

Result: Claude Code → `❯` prompt immediately. Responds to "say hello". No screens.

**Remaining broken:**

1. `tryLaunch` uses `*Model` receiver (compiles via internal address-taking but is technically wrong)
2. Debug `/tmp/key.log` writes still in model.go and view_term.go
3. Single-agent dead end: `IntakeCompleteMsg` → `StateDone`, no next phase
4. `harness.BuildPTYCommandWithPromptFile` hardcodes `--dangerously-skip-permissions` (triggers acceptance dialog)
5. `harness.BuildModelEnv` uses `ANTHROPIC_API_KEY` (triggers "custom API key" screen)

---

## Phase 1: Fix Harness for Zero-Prompt Launch (PREREQUISITE)

Three changes to eliminate Claude Code startup screens:

### Step 1.1: Remove `--dangerously-skip-permissions` from interactive command

In `internal/harness/claude_cli.go` line 318, the interactive command is:

```go
cmd := []string{"claude", "--dangerously-skip-permissions"}
```

Change to:

```go
cmd := []string{"claude"}
```

**Rationale**: `--dangerously-skip-permissions` triggers a "WARNING: Bypass Permissions mode" dialog with default "No, exit". The seatbelt sandbox provides isolation — Claude Code doesn't need this flag.

### Step 1.2: Fix env in BuildModelEnv — no auth tokens

`BuildModelEnv` must NOT emit `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`. Claude Code uses existing OAuth/keychain auth (the sandbox allows `~/.claude` + XPC forwarding). Just set routing env:

- `ANTHROPIC_BASE_URL` — redirects API calls to Ollama
- `ANTHROPIC_MODEL` — model name at the endpoint
- `ANTHROPIC_SMALL_FAST_MODEL` — for fast completions
- `DISABLE_NON_ESSENTIAL_MODEL_CALLS=0`
- `CLAUDE_CODE_ATTRIBUTION_HEADER=0`

### Step 1.3: Add `DISABLE_NON_ESSENTIAL_MODEL_CALLS=0` and `CLAUDE_CODE_ATTRIBUTION_HEADER=0`

These suppress attribution headers and allow essential model calls only.

### Acceptance criteria

```bash
# Non-interactive (regression):
go run ./cmd/sandbox --workspace=$(pwd) \
  --anthropic-base-url=http://192.168.50.212:11434 \
  --anthropic-model=qwen36 \
  --env=ANTHROPIC_SMALL_FAST_MODEL=qwen36 \
  --env=DISABLE_NON_ESSENTIAL_MODEL_CALLS=0 \
  --env=CLAUDE_CODE_ATTRIBUTION_HEADER=0 \
  -- claude -p "say hello"
# Expected: prints response, exit 0

# Interactive:
go run ./cmd/sandbox --workspace=$(pwd) \
  --anthropic-base-url=http://192.168.50.212:11434 \
  --anthropic-model=qwen36 \
  --env=ANTHROPIC_SMALL_FAST_MODEL=qwen36 \
  --env=DISABLE_NON_ESSENTIAL_MODEL_CALLS=0 \
  --env=CLAUDE_CODE_ATTRIBUTION_HEADER=0 \
  -- claude
# Expected: Claude Code shows ❯ prompt within 3s. No dialog screens.
# Auth: "Claude Team" (existing OAuth via keychain). Ollama ignores the auth header.
```

### Step 1.4: Remove debug logging

Remove all `/tmp/key.log` writes from `model.go` and `view_term.go`. Remove `"os"` import if no longer needed.

### Step 1.5: Fix tryLaunch receiver (cleanup)

Convert `func (m *Model) tryLaunch() tea.Cmd` to value-return pattern: `func tryLaunch(m Model) (Model, tea.Cmd)`. Update callsites in `Update()` to use returned model.

### Acceptance criteria

```bash
go build ./cmd/orqestra && go test ./internal/tui/...
# Both must pass with zero errors
```

---

## Phase 2: Single Agent via TUI (orqestra launch → Claude Code ready)

**Goal**: `./orqestra --config orqestra.local.yaml` starts the TUI, spawns Claude Code inside seatbelt sandbox via PTY, and the user sees Claude Code's `❯` prompt rendered in the VT emulator tab — ready for input.

### Step 2.1: Apply harness changes and rebuild

After Phase 1 changes, the launch chain becomes:

1. `main()` → `config.Load("orqestra.local.yaml")`
2. `detect.AllProfiles(home, "claude", cfg.Seatbelt)` → discovers `claude` binary + dylibs
3. `runTUI()` → bubbletea starts → `WindowSizeMsg` + `setProgramMsg` → `tryLaunch` fires
4. `go startInteractiveAgent("")` → closure builds:

   ```
   AgentSpec.Command = ["claude", "--append-system-prompt-file", "<session>/intake/agent.md"]
   AgentSpec.Env includes: ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL,
                           DISABLE_NON_ESSENTIAL_MODEL_CALLS=1, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
   ```

5. `seatbeltRunner.RunInteractive()` → `seatbelt.New()` → SBPL → `StartNativePTY(cmd, cols, rows)`
6. PTY output flows: `readLoop` → `OnOutput` → `PTYOutputMsg` → `vt.Write(data)` → renders

### Step 2.2: Validate startup (no dialog screens)

**Test scenario**: Launch orqestra. Within 5 seconds, the "Claude" tab must show Claude Code's banner and `❯` prompt. No intermediate screens.

**What can go wrong and how to diagnose**:

| Symptom | Cause | Fix |
|---------|-------|-----|
| Tab shows "⟳ Provisioning sandbox..." forever | `seatbeltRunner.RunInteractive()` failed, no `attachPTYMsg` sent | Check `m.logPanel` for error. Run `seatbelt-trace.sh` to trace SBPL denials |
| Claude Code shows "Detected a custom API key" dialog | `ANTHROPIC_API_KEY` set in env | Verify `BuildModelEnv` emits `ANTHROPIC_AUTH_TOKEN` not `ANTHROPIC_API_KEY` |
| Claude Code shows "WARNING: Bypass Permissions" dialog | `--dangerously-skip-permissions` still in command | Verify `BuildPTYCommandWithPromptFile` doesn't include it |
| Claude Code shows "Do you trust this folder?" | New directory never trusted before | Run `claude` once manually in that dir to accept; OR use `--bare` |
| Tab shows garbled escape sequences | VT emulator doesn't support sequences Claude uses | Cosmetic only — not a blocker |
| No output at all, PTY attached but blank | Claude Code process stopped (SIGTTOU) | PTY mode doesn't have this issue (PTY IS its own terminal). Only cmd/sandbox with raw TTY needs `Foreground=true` |
| Claude exits immediately (PTYDoneMsg with exit code != 0) | SBPL denying access to needed paths | Run `scripts/seatbelt-trace.sh` → check sandbox violations |

### Step 2.3: Validate input forwarding

**Test scenario**: With Claude Code showing `❯`, type "say hello" and press Enter.

**Expected**: Claude Code processes the message, shows a response, returns to `❯`.

**What can go wrong**:

| Symptom | Cause | Fix |
|---------|-------|-----|
| Characters don't appear in Claude Code | `keyToBytes` not converting keys, or `ptySession.Write` failing | Add debug log before Write, check `keyToBytes` handles runes |
| Enter doesn't submit | `keyToBytes` returns `\n` instead of `\r` | Verify it returns `\r` for Enter |
| Claude Code receives input but no response | Ollama not running or model not loaded | `curl http://192.168.50.212:11434/v1/models` |
| Response renders garbled | VT emulator issue | Cosmetic, not blocking |

### Step 2.4: Validate exit and BEL

**Test scenario**: Type `/exit` in Claude Code prompt.

**Expected**:

1. Claude Code exits → PTY master gets EOF
2. `readLoop` in `seatbelt_runner.go` returns → `npty.Wait()` returns exit code → `close(done)`
3. `NativeLiveSession.Wait()` returns artifact (nil for intake) → goroutine sends `IntakeCompleteMsg`
4. Model transitions to `StateDone` → tab shows "✓ Exited (code 0)"

**BEL test**: During Claude Code's response (while it's "thinking"), if it emits `\x07`, the `scanForBEL` in `readLoop` fires `OnBEL` → `AttentionMsg` → tab shows ⚠. Verify by checking tab header during response generation.

### Acceptance criteria (all must pass)

1. `./orqestra --config orqestra.local.yaml` → TUI renders within 1s
2. "Claude" tab shows Claude Code banner + `❯` within 5s (no dialog screens)
3. Type "say hello" + Enter → response appears in VT emulator
4. Type `/exit` → tab shows "✓ Exited (code 0)" → TUI shows "Agent session complete"

---

## Phase 3: Multi-Agent Pipeline

### Step 3.1: Replace PipelineFuncs.LaunchInteractive with LaunchAgent

```go
// messages.go
type PipelineFuncs struct {
    LaunchAgent func(ctx context.Context, role string, inputFiles map[string][]byte, send func(tea.Msg), tabIndex int) (PTYWriter, WaitFunc, error)
    Send func(tea.Msg)
}
```

### Step 3.2: Add pipeline state to Model

```go
// model.go
type PipelinePhase int
const (
    PhaseIntake PipelinePhase = iota
    PhasePlanner
    PhaseValidator
    PhaseWorker
    PhaseDone
)

// Add to Model struct:
phase     PipelinePhase
artifacts map[string][]byte // keyed by role name
```

### Step 3.3: Add AgentCompleteMsg

```go
// messages.go
type AgentCompleteMsg struct {
    Role     string
    TabIndex int
    Artifact []byte
    Err      error
}
```

In `Update()`:

```go
case AgentCompleteMsg:
    if msg.Err != nil {
        m.err = msg.Err; m.state = StateDone; return m, nil
    }
    if m.artifacts == nil { m.artifacts = make(map[string][]byte) }
    m.artifacts[msg.Role] = msg.Artifact
    return m.advancePipeline()
```

### Step 3.4: Implement advancePipeline()

```go
func (m Model) advancePipeline() (tea.Model, tea.Cmd) {
    switch m.phase {
    case PhaseIntake:
        m.phase = PhasePlanner
        tabIdx := m.tabsView.AddTermTab("Planner")
        m.tabsView.active = tabIdx
        m.tabsView.termTabs[tabIdx].focused = true
        inputs := map[string][]byte{"01.intake.json": m.artifacts["intake"]}
        go m.startAgent("planner", tabIdx, inputs)
        return m, nil
    case PhasePlanner:
        m.phase = PhaseValidator
        tabIdx := m.tabsView.AddTermTab("Validator")
        m.tabsView.active = tabIdx
        m.tabsView.termTabs[tabIdx].focused = true
        inputs := map[string][]byte{
            "01.intake.json": m.artifacts["intake"],
            "02.plan.json":   m.artifacts["planner"],
        }
        go m.startAgent("plan-validator", tabIdx, inputs)
        return m, nil
    case PhaseValidator:
        var v struct{ Verdict string `json:"verdict"` }
        if json.Unmarshal(m.artifacts["plan-validator"], &v) == nil && v.Verdict != "approved" {
            m.err = fmt.Errorf("plan rejected"); m.state = StateDone; return m, nil
        }
        m.phase = PhaseWorker
        tabIdx := m.tabsView.AddTermTab("Worker")
        m.tabsView.active = tabIdx
        m.tabsView.termTabs[tabIdx].focused = true
        inputs := map[string][]byte{"02.plan.json": m.artifacts["planner"]}
        go m.startAgent("worker", tabIdx, inputs)
        return m, nil
    case PhaseWorker:
        m.phase = PhaseDone; m.state = StateDone; return m, nil
    }
    return m, nil
}
```

**Why `go m.startAgent(...)` is safe**: The goroutine captures `m.program` (pointer to live `tea.Program`), `m.ctx` (context with cancel), and `m.pipeline.LaunchAgent` (closure). All are stable references set at startup. The goroutine only READS these and calls `p.Send()`. It never writes to model fields.

### Step 3.5: Implement startAgent goroutine

```go
func (m Model) startAgent(role string, tabIdx int, inputFiles map[string][]byte) {
    p := m.program
    pty, wait, err := m.pipeline.LaunchAgent(m.ctx, role, inputFiles, p.Send, tabIdx)
    if err != nil {
        p.Send(AgentCompleteMsg{Role: role, TabIndex: tabIdx, Err: err})
        return
    }
    p.Send(attachPTYMsg{tabIndex: tabIdx, pty: pty})
    artifact, err := wait(m.ctx)
    p.Send(AgentCompleteMsg{Role: role, TabIndex: tabIdx, Artifact: artifact, Err: err})
}
```

### Step 3.6: Build the LaunchAgent closure in cmd/orqestra/main.go

This is the critical wiring. **The key problem**: system prompts from `pipeline.yaml` reference Docker paths (`/workspace/.orqestra/agent/input/01.intake.json`). For seatbelt mode, the agent's CWD is `repoPath` and input files are at absolute paths under the session dir. The closure MUST template the system prompt with correct paths.

```go
func runTUI(_ context.Context, cfg *config.Config, seatbeltRunner *agent.SeatbeltRunner, noExecute, jsonOutput bool) {
    repoPath, _ := os.Getwd()

    // Shared session dir for all phases
    sessDir, err := agent.NewSessionDir(repoPath, "pipeline")
    if err != nil { /* fatal */ }

    pipeline := tui.PipelineFuncs{
        LaunchAgent: func(ctx context.Context, role string, inputFiles map[string][]byte, send func(tea.Msg), tabIndex int) (tui.PTYWriter, tui.WaitFunc, error) {

            // 1. Resolve model for this role
            var modelRef, basePrompt, outputFile string
            switch agent.Role(role) {
            case agent.RoleIntake:
                modelRef = cfg.Intent.ModelRef
                basePrompt = cfg.Intent.SystemPrompt
                outputFile = "01.intake.json"
            case agent.RolePlanner:
                modelRef = cfg.Planner.ModelRef
                basePrompt = cfg.Planner.SystemPrompt
                outputFile = "02.plan.json"
            case agent.RolePlanValidator:
                modelRef = cfg.Validator.ModelRef
                basePrompt = cfg.Validator.SystemPrompt
                outputFile = "03.validation.json"
            case agent.RoleWorker:
                modelRef = cfg.Worker.ModelRef
                basePrompt = cfg.Worker.SystemPrompt
                outputFile = "" // worker modifies repo, no artifact
            }

            resolved, err := cfg.ResolveModel(modelRef)
            if err != nil { return nil, nil, err }

            // 2. Template the system prompt with actual paths
            roleDir := filepath.Join(sessDir.Path, role)
            inputDir := filepath.Join(roleDir, "input")
            outputDir := filepath.Join(roleDir, "output")
            systemPrompt := basePrompt + fmt.Sprintf(
                "\n\n---\nRuntime paths:\n- Input directory: %s\n- Output directory: %s\n- Output file: %s/%s\n- Working directory (repo): %s\n",
                inputDir, outputDir, outputDir, outputFile, repoPath,
            )

            // 3. Build AgentSpec
            promptFile := filepath.Join(roleDir, "agent.md")
            spec := agent.AgentSpec{
                Role:         agent.Role(role),
                ModelRef:     modelRef,
                SystemPrompt: systemPrompt,
                InputFiles:   inputFiles,
                OutputFile:   outputFile,
                Command:      harness.BuildPTYCommandWithPromptFile("", promptFile, true),
                Env:          harness.BuildModelEnv(resolved, cfg.ResolveSmallModel()),
                Interactive:  true,
            }

            // 4. Launch via seatbelt runner
            liveSession, err := seatbeltRunner.RunInteractive(ctx, agent.RunConfig{
                Spec:     spec,
                Session:  sessDir,
                RepoPath: repoPath,
                Callbacks: agent.RunCallbacks{
                    OnOutput: func(data []byte) { send(tui.PTYOutputMsg{TabIndex: tabIndex, Data: data}) },
                    OnBEL:    func() { send(tui.AttentionMsg{TabIndex: tabIndex}) },
                    OnDone:   func(exitCode int, exitErr error) {
                        send(tui.PTYDoneMsg{TabIndex: tabIndex, ExitCode: exitCode, Err: exitErr})
                    },
                },
            })
            if err != nil { return nil, nil, err }

            return liveSession, func(waitCtx context.Context) ([]byte, error) {
                return liveSession.Wait(waitCtx)
            }, nil
        },
    }

    tuiErr := tui.Run(pipeline)
    ...
}
```

**Critical detail on OnDone ordering**: The `OnDone` callback fires INSIDE `NativeLiveSession.Wait()` (seatbelt_runner.go:72-78) BEFORE `Wait()` returns the artifact. So `PTYDoneMsg` arrives FIRST (updates tab status to "✓ Exited"), then `AgentCompleteMsg` arrives (advances pipeline). Correct ordering.

**Critical detail on session dir reuse**: `sharedSessionDir` is created once. Each role gets subdirectory `<session>/<role>/` with `input/` and `output/`. `SeatbeltRunner.start()` creates these dirs from `cfg.Session.Path + "/" + spec.Role`. Artifacts accumulate across phases.

---

## Phase 4: End-to-End Validation

### Step 4.1: Run

```bash
make build && ./orqestra --config orqestra.local.yaml
```

### Step 4.2: Intake

In the "Intake" tab, Claude Code starts. The intent system prompt classifies the user's message. Type: "Create a CONTRIBUTING.md with build instructions from the Makefile". Agent classifies as ACCEPT, writes structured JSON to `<session>/intake/output/01.intake.json`, exits.

### Step 4.3: Planner

"Planner" tab appears. Reads `01.intake.json`, generates engineering spec (goal, steps, acceptance), writes to `<session>/planner/output/02.plan.json`, exits.

### Step 4.4: Validator

"Validator" tab appears. Reads both artifacts, evaluates plan quality, presents to user. User interacts (approve/reject). Writes verdict JSON, exits.

### Step 4.5: Worker

"Worker" tab appears. Reads plan, executes (creates CONTRIBUTING.md in repo), exits. No artifact — the work IS the repo changes.

### Step 4.6: Verify

```bash
git diff  # Shows CONTRIBUTING.md created by worker
```

### Verification gate

`git diff` shows file changes made by the worker agent.

---

## Files Modified

| File                         | What changes                                                                                                                                                                                                             |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `cmd/sandbox/main.go`        | Add `Foreground=true` + `Ctty` for interactive TTY. Use `Wrap` + manual Start/Wait instead of `sb.Run()`. ✅ DONE                                                                                                         |
| `internal/harness/claude_cli.go` | Remove `--dangerously-skip-permissions` from interactive command. Remove `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` from `BuildModelEnv` — no auth env needed. Add `DISABLE_NON_ESSENTIAL_MODEL_CALLS=0` + `CLAUDE_CODE_ATTRIBUTION_HEADER=0`. |
| `internal/tui/model.go`      | Fix `tryLaunch` → free function returning (Model, tea.Cmd). Add `PipelinePhase`, `artifacts`. Replace `IntakeCompleteMsg` handler with `AgentCompleteMsg` → `advancePipeline()`. Add `startAgent`. Remove debug logging. |
| `internal/tui/messages.go`   | Replace `LaunchInteractive` with `LaunchAgent` in `PipelineFuncs`. Add `AgentCompleteMsg`. Remove `IntakeCompleteMsg` (replaced).                                                                                        |
| `internal/tui/view_term.go`  | Remove `/tmp/key.log` debug writes.                                                                                                                                                                                      |
| `internal/tui/model_test.go` | Update to `LaunchAgent` signature. Fix type assertions if needed.                                                                                                                                                        |
| `cmd/orqestra/main.go`       | Rewrite `runTUI()`: shared session dir, role-dispatching `LaunchAgent` closure with path templating.                                                                                                                     |

---

## Risks

1. **System prompt path injection**: The appended "Runtime paths" section tells the agent where to read/write. If the agent ignores this (LLM doesn't follow), artifacts won't be produced. **Mitigation**: Make the path instructions very explicit and test with intake first.

2. **OutputFile empty for worker**: `NativeLiveSession.Wait()` returns `nil` when `spec.OutputFile == ""`. `AgentCompleteMsg{Artifact: nil}` triggers `advancePipeline()` → `PhaseDone`. Correct.

3. **Validator doesn't produce valid JSON**: `json.Unmarshal` fails → plan treated as rejected → pipeline halts. Acceptable for v1.

4. **tabsView.AddTermTab called from Update is safe**: Synchronous call inside `advancePipeline()` which runs inside `Update()`. No concurrency issue.

---

## Decisions

- **SeatbeltRunner only** — `internal/agent/pipeline.go` (Docker runner) is NOT used. All agents go through `SeatbeltRunner.RunInteractive`.
- **Sequential execution** — agents run one at a time. No parallelism.
- **Shared session dir** — one `SessionDir` per orqestra run, all roles write into it.
- **All agents are interactive** — even planner/worker get full PTY. User CAN interact with any.
- **No `--dangerously-skip-permissions`** — triggers acceptance dialog. Seatbelt IS the security boundary.
- **No auth env vars** — Claude Code uses existing OAuth/keychain. Sandbox allows `~/.claude` + XPC for keychain IPC. Ollama ignores the auth header sent.
- **Agent CWD = repoPath** — confirmed via `sandbox.Wrap()` setting `cmd.Dir = s.repoPath`.
- **PTY mode doesn't need `Foreground=true`** — only `cmd/sandbox` (direct TTY sharing) needed that fix. `SeatbeltRunner.RunInteractive` uses `StartNativePTY` which creates its own pseudo-terminal.
