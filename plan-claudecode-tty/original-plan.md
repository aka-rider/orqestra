                                                                                
    go agent.sendInitialPrompt()

    return nil
}

// buildDockerExecArgs constructs: docker exec -it -e ... <containerID> claude ...
func (agent *RunningAgent) buildDockerExecArgs(cols, rows uint16) []string {
    args := []string{
        "exec",
        "-it",                   // interactive + allocate TTY
        "-u", "sandbox",         // non-root user
        "-e", fmt.Sprintf("COLUMNS=%d", cols),
        "-e", fmt.Sprintf("LINES=%d", rows),
        "-e", "TERM=xterm-256color",
        "-e", "LANG=en_US.UTF-8",
        "-e", "CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
        "-e", "LANG=en_US.UTF-8",
    }

    // Pass through environment (API keys, model config, etc.)
    for _, e := range agent.Config.Env {
        args = append(args, "-e", e)
    }

    args = append(args, agent.Sandbox.ID()) // container ID

    // Claude Code invocation — interactive mode, no --output-format
    binary := agent.Config.Binary
    if binary == "" {
        binary = "claude"
    }
    args = append(args,
        binary,
        "--dangerously-skip-permissions",  // sandbox IS the permission boundary
        "--system-prompt", fmt.Sprintf("$(cat /workspace/.orqestra/system-prompt.md)"),
    )

    if model := agent.Config.Env; model != nil {
        // Model is set via ANTHROPIC_MODEL env var, not --model flag
    }

    return args
}

// sendInitialPrompt waits for Claude Code to start, then sends the
// instruction to read input.md and produce output.md.
func (agent *RunningAgent) sendInitialPrompt() {
    // Brief delay for Claude Code startup (it prints banner first)
    time.Sleep(2 * time.Second)

    prompt := `Read /workspace/.orqestra/input.md carefully. ` +
        `Follow the instructions within. ` +
        `Write your complete output to /workspace/.orqestra/output.md before finishing.`

    agent.PTY.Write([]byte(prompt + "\n"))
}
```

### Phase 3: Pump Output — Goroutine Feeding the TUI

```go
// pumpOutput reads from the PTY in a loop and sends PTYOutputMsg to the TUI.
// Runs until the PTY closes (subprocess exits) or context is cancelled.
//
// This goroutine is the bridge between the Docker subprocess and bubbletea.
// It MUST NOT send mutable data — []byte is copied before sending.
//
// Buffer size: 4KB reads. Claude Code output is bursty (large tool results
// interspersed with idle periods), so small reads are fine and keep TUI latency low.
func (agent *RunningAgent) pumpOutput(send func(tea.Msg)) {
    buf := make([]byte, 4096)
    tabIndex := agent.tabIndex

    for {
        n, err := agent.PTY.Read(buf)
        if n > 0 {
            // Deep copy — buf is reused on next Read()
            data := make([]byte, n)
            copy(data, buf[:n])
            send(PTYOutputMsg{TabIndex: tabIndex, Data: data})
        }
        if err != nil {
            // EOF = subprocess exited; io error = PTY closed
            exitCode := agent.PTY.ExitCode()
            var exitErr error
            if exitCode == 137 {
                exitErr = fmt.Errorf("agent OOM killed (137)")
            } else if exitCode != 0 {
                exitErr = fmt.Errorf("agent exited with code %d", exitCode)
            }
            send(PTYDoneMsg{TabIndex: tabIndex, ExitCode: exitCode, Err: exitErr})
            return
        }
    }
}
```

### Phase 4: Collect — Extract Artifacts After Exit

```go
// Collect extracts the output artifact and file changes from the sandbox
// after the PTY session exits. This is called by the TUI when it receives
// PTYDoneMsg.
//
// Steps:
//   1. Read /workspace/.orqestra/output.md from the container
//   2. Validate artifact format (frontmatter + body)
//   3. Save to <sessionDir>/<stepName>/output.md on host
//   4. For worker agents: run ExtractChanges() (overlayfs snapshot diff or git diff)
//   5. Verify extracted files (path traversal, size, executables)
//   6. Stage verified files to <sessionDir>/<stepName>/changes/
//   7. Return artifact + changes
//
// If output.md is missing, returns an error with the last N lines of PTY
// output for debugging. The TUI displays this to the user with an option
// to re-run the agent.
func (r *SandboxedPTYRunner) Collect(ctx context.Context, agent RunningAgent) (CollectResult, error) {
    stepDir := filepath.Join(agent.SessionDir, agent.StepName)

    // Extract output artifact
    art, err := r.extractArtifact(ctx, agent.Sandbox)
    if err != nil {
        return CollectResult{}, fmt.Errorf("extracting output artifact: %w", err)
    }

    // Save to session trace
    if err := WriteArtifact(filepath.Join(stepDir, "output.md"), art); err != nil {
        return CollectResult{}, fmt.Errorf("saving output artifact: %w", err)
    }

    result := CollectResult{Artifact: art}

    // For workers: extract file changes
    if agent.Config.StepName[:2] == "05" { // workers are step 05
        changes, err := agent.Sandbox.ExtractChanges(ctx)
        if err != nil {
            return result, fmt.Errorf("extracting changes: %w", err)
        }

        // Verify
        vr := r.verifier.Verify(changes)
        if !vr.Passed {
            return result, fmt.Errorf("security verification failed: %d files rejected", len(vr.Rejected))
        }

        // Stage to session dir
        changesDir := filepath.Join(stepDir, "changes")
        for _, f := range changes {
            if f.Op == FileDeleted {
                continue
            }
            dst := filepath.Join(changesDir, f.Path)
            if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
                return result, fmt.Errorf("creating changes dir: %w", err)
            }
            if err := agent.Sandbox.CopyOut(ctx, f.Path, dst); err != nil {
                return result, fmt.Errorf("staging %s: %w", f.Path, err)
            }
        }

        result.Changes = changes
    }

    return result, nil
}

// CollectResult holds the output of a completed agent session.
type CollectResult struct {
    Artifact Artifact
    Changes  []ChangedFile // non-nil only for worker agents
}
```

### Phase 5: Destroy — Cleanup

```go
// Destroy tears down the sandbox. Called after Collect(), or on error/cancel.
// Always safe to call — idempotent, logs but doesn't fail.
func (r *SandboxedPTYRunner) Destroy(ctx context.Context, agent RunningAgent) {
    if agent.PTY != nil {
        agent.PTY.Close()
    }
    if agent.Sandbox != nil {
        destroyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
        defer cancel()
        if err := agent.Sandbox.Destroy(destroyCtx); err != nil {
            slog.Error("sandbox destroy failed", "id", agent.Sandbox.ID(), "err", err)
        }
        r.emitState(agent.Sandbox.ID(), StateDestroyed)
    }
}
```

### Full Lifecycle Sequence Diagram

```
TUI (bubbletea)             SandboxedPTYRunner          Docker           Claude Code
     │                            │                        │                  │
     │─── tea.Cmd: Prepare() ────▶│                        │                  │
     │                            │── docker create ──────▶│                  │
     │                            │── docker start ───────▶│                  │
     │                            │── stage input.md ─────▶│                  │
     │◀── RunningAgent ──────────│                        │                  │
     │                            │                        │                  │
     │─── tea.Cmd: Launch() ─────▶│                        │                  │
     │                            │── docker exec -it ────▶│── claude ───────▶│
     │                            │                        │                  │
     │                            │      ┌─── PTY fd ──────┤◀─── output ─────│
     │◀── PTYOutputMsg ──────────│◀─────┘ (read loop)     │                  │
     │◀── PTYOutputMsg ──────────│◀────────────────────────│                  │
     │                            │                        │                  │
     │    (user types in tab)     │                        │                  │
     │─── PTYSession.Write() ───▶│────── PTY fd ─────────▶│── stdin ────────▶│
     │                            │                        │                  │
     │◀── PTYNeedsInputMsg ──────│  (idle detector)       │                  │
     │    (tab header blinks ⚡)  │                        │                  │
     │                            │                        │                  │
     │◀── PTYDoneMsg ────────────│◀──── exit ─────────────│◀── exit ────────│
     │                            │                        │                  │
     │─── tea.Cmd: Collect() ────▶│                        │                  │
     │                            │── read output.md ─────▶│                  │
     │                            │── overlayfs snapshot diff or git diff ─────────▶│                  │
     │                            │── verify files ────────│                  │
     │◀── CollectResult ─────────│                        │                  │
     │                            │                        │                  │
     │─── tea.Cmd: Destroy() ────▶│── docker rm -f ───────▶│                  │
     │                            │── volume rm ──────────▶│                  │
     │◀── done ──────────────────│                        │                  │
```

### How the TUI Drives the Lifecycle

The TUI model orchestrates phases via `tea.Cmd` — no blocking in `Update()`:

```go
// In model.go Update():

case PTYDoneMsg:
    // Agent finished — extract results
    return m, m.collectAgentCmd(msg.TabIndex)

case ArtifactReadyMsg:
    // Artifact extracted — show human gate or advance pipeline
    switch msg.Role {
    case "planner":
        m.state = StateConfirming
        m.spec = parseSpecFromArtifact(msg.Artifact)
        return m, nil
    case "worker":
        return m, m.destroyAgentCmd(msg.TabIndex)
    }
```

```go
func (m Model) collectAgentCmd(tabIndex int) tea.Cmd {
    return func() tea.Msg {
        result, err := m.runners[tabIndex].Collect(m.ctx, m.agents[tabIndex])
        if err != nil {
            return ErrorMsg{Err: err}
        }
        return ArtifactReadyMsg{
            TabIndex: tabIndex,
            Artifact: result.Artifact,
            Role:     m.agents[tabIndex].Config.StepName,
        }
    }
}
```

---

## PTY Library & Terminal Emulation

### PTY Library Choice

**`Docker SDK`** — battle-tested, used by Docker/Moby, supports macOS/Linux. No better alternative exists in Go for POSIX PTY allocation.

### Terminal State Parsing

For tracking cursor position, screen buffer, and detecting "waiting for input":

**`github.com/charmbracelet/x/vt`** — Charmbracelet's own virtual terminal parser. Stays in-ecosystem with bubbletea, handles ANSI state, gives us a screen buffer to render in a viewport.

If `charmbracelet/x/vt` is too experimental or insufficient, fallback to:
**`github.com/hinshun/vt10x`** — mature VT100 emulator with a state machine.

### `PTYSession` (internal/harness/pty_session.go)

```go
type PTYSession struct {
    ID        string
    Name      string
    State     SessionState

    cmd       *exec.Cmd
    ptyFd     *os.File      // creack/pty file descriptor — reads AND writes
    mu        sync.Mutex
    cancel    context.CancelFunc
    exitCode  int
    exited    bool

    cols, rows uint16       // current terminal dimensions
}

// Start spawns a process in a PTY with the given dimensions.
// The PTY file descriptor is used for both reading subprocess output
// and writing user input — it's a single bidirectional fd.
func (ps *PTYSession) Start(ctx context.Context, binary string, args []string, env []string, cols, rows uint16) error {
    ctx, ps.cancel = context.WithCancel(ctx)
    ps.cols = cols
    ps.rows = rows

    ps.cmd = exec.CommandContext(ctx, binary, args...)
    ps.cmd.Env = env

    var err error
    ps.ptyFd, err = pty.StartWithSize(ps.cmd, &pty.Winsize{
        Rows: rows,
        Cols: cols,
    })
    if err != nil {
        return fmt.Errorf("pty start: %w", err)
    }

    ps.State = SessionRunning

    // Monitor exit in background
    go func() {
        err := ps.cmd.Wait()
        ps.mu.Lock()
        ps.exited = true
        if err != nil {
            if exitErr, ok := err.(*exec.ExitError); ok {
                ps.exitCode = exitErr.ExitCode()
            }
            ps.State = SessionFailed
        } else {
            ps.State = SessionDone
        }
        ps.mu.Unlock()
    }()

    return nil
}

// Write sends user input bytes to the PTY (reaches subprocess stdin).
func (ps *PTYSession) Write(p []byte) (int, error) {
    return ps.ptyFd.Write(p)
}

// Read returns output bytes from the PTY (subprocess stdout+stderr).
// Blocking; caller runs in a goroutine.
func (ps *PTYSession) Read(p []byte) (int, error) {
    return ps.ptyFd.Read(p)
}

// Resize sends SIGWINCH to update terminal dimensions.
func (ps *PTYSession) Resize(cols, rows uint16) error {
    ps.mu.Lock()
    ps.cols = cols
    ps.rows = rows
    ps.mu.Unlock()
    return pty.Setsize(ps.ptyFd, &pty.Winsize{Rows: rows, Cols: cols})
}

// ExitCode returns the subprocess exit code. Only valid after Read() returns io.EOF.
func (ps *PTYSession) ExitCode() int {
    ps.mu.Lock()
    defer ps.mu.Unlock()
    return ps.exitCode
}

// Close kills the subprocess and closes the PTY fd.
func (ps *PTYSession) Close() error {
    ps.cancel()
    return ps.ptyFd.Close()
}
```

---

## TUI Integration

### `termView` (internal/tui/view_term.go)

Replaces `streamView`. Maintains a virtual terminal buffer and renders it.

```go
type termView struct {
    tabIndex    int
    ptySession  *harness.PTYSession  // nil until Launch()
    vt          *vt.Terminal          // virtual terminal state machine

    needsInput  bool
    done        bool
    err         error
    startedAt   time.Time

    width, height int
    focused       bool
}

func newTermView(tabIndex int, cols, rows int) termView {
    return termView{
        tabIndex: tabIndex,
        vt:       vt.New(cols, rows),
    }
}

func (tv termView) Update(msg tea.Msg) (termView, tea.Cmd) {
    switch msg := msg.(type) {
    case PTYOutputMsg:
        if msg.TabIndex == tv.tabIndex {
            if tv.startedAt.IsZero() {
                tv.startedAt = time.Now()
            }
            tv.vt.Write(msg.Data) // feed bytes to virtual terminal
            tv.needsInput = false // got output, so not waiting
        }

    case PTYNeedsInputMsg:
        if msg.TabIndex == tv.tabIndex {
            tv.needsInput = true
        }

    case PTYDoneMsg:
        if msg.TabIndex == tv.tabIndex {
            tv.done = true
            tv.err = msg.Err
        }

    case tea.KeyMsg:
        if tv.focused && tv.ptySession != nil && !tv.done {
            // Forward keystroke to PTY
            tv.ptySession.Write(keyToBytes(msg))
            return tv, nil
        }

    case tea.WindowSizeMsg:
        tv.width = msg.Width
        tv.height = msg.Height
        tv.vt.Resize(msg.Width, msg.Height)
        if tv.ptySession != nil {
            tv.ptySession.Resize(uint16(msg.Width), uint16(msg.Height))
        }
    }
    return tv, nil
}

func (tv termView) View() string {
    // Render the virtual terminal's screen buffer as a string
    screen := tv.vt.String() // rows of cells → string with ANSI

    var status string
    if tv.done {
        if tv.err != nil {
            status = errorStyle.Render("✗ Failed: " + tv.err.Error())
        } else {
            status = goalStyle.Render("✓ Complete")
        }
    } else if tv.needsInput {
        status = warningStyle.Render("⚡ Waiting for input")
    } else {
        elapsed := ""
        if !tv.startedAt.IsZero() {
            d := time.Since(tv.startedAt).Truncate(time.Second)
            elapsed = fmt.Sprintf(" (%s)", d)
        }
        status = "● Running..." + elapsed
    }

    return screen + "\n" + statusStyle.Render(status)
}
```

### Tab Header States

| State | Header Display |
|-------|---------------|
| Provisioning | `◌ planner` (dimmed) |
| Running (no input needed) | `✦ planner` (existing pulse) |
| Needs input | `⚡ planner` (rapid blink, yellow/orange) |
| Done | `✓ planner` (green) |
| Failed | `✗ planner` (red) |

### Quick-Switch: `ctrl+space`

- Jumps to the *most recently signaled* "needs input" tab
- If no tab needs input, cycles to next running tab
- Status bar shows: `⚡ Tab 3 needs input — Ctrl+Space to switch`

### Focus Model

When `FocusTabs` is active:

- All `tea.KeyMsg` except global hotkeys (`ctrl+c`, `alt+1..9`, `ctrl+space`, `esc`) forward to the focused `termView`
- `esc` releases focus back to `FocusPrompt` (command bar)
- Clicking or pressing `Enter` on the tab area grants `FocusTabs`

### Input Detection (internal/harness/input_detector.go)

Heuristic-based detection that the subprocess is waiting for user input:

1. **Output idle timeout** — (REMOVED: Do not use time-based heuristics as Claude delays often. Rely purely on deterministic VT text matching and cursor positions).
2. **Pattern matching** — scan recent VT buffer for Claude Code prompts:
   - `"Do you want to proceed?"`, `"(y/n)"`, `"Allow"`, `"[Y/n]"`
   - Prompt-like suffixes: lines ending in `?`, `>`, `:`
3. **Cursor position** — VT cursor at end of a line with a prompt-like pattern.

Emits `PTYNeedsInputMsg{TabIndex}` to the TUI when detected.

---

## Sandbox Configurations Per Role

### Planner — Full Research Access

```yaml
sandbox:
  image: orqestra-sandbox:latest
  network: bridge              # can curl docs, APIs, registries
  memory: 4g
  cpus: 2
  max_lifetime: 15m
  allowed_executables: []      # planner doesn't produce files
  read_only_mounts:
    - host: /workspace
      container: /workspace    # full repo access (read-only)
```

**Claude Code tools**: `Read, Bash` (for curl, grep, find, git log)
**No write to repo**: planner writes ONLY to `/workspace/.orqestra/output.md`

### Plan Validator — Air-Gapped

```yaml
sandbox:
  image: orqestra-sandbox:latest
  network: none                # no network — judges spec in isolation
  memory: 2g
  cpus: 1
  max_lifetime: 5m
```

### Project Manager — Air-Gapped

```yaml
sandbox:
  image: orqestra-sandbox:latest
  network: none
  memory: 2g
  cpus: 1
  max_lifetime: 10m
```

### Worker — Full Access

```yaml
sandbox:
  image: orqestra-sandbox:latest
  network: bridge              # can install deps, fetch packages
  memory: 4g
  cpus: 2
  max_lifetime: 50m
  allowed_executables:
    - "*.sh"
    - "node_modules/.bin/*"
```

**Claude Code tools**: `Read, Bash, Write` (full implementation access)

### Work Validator — Air-Gapped

```yaml
sandbox:
  image: orqestra-sandbox:latest
  network: none
  memory: 2g
  cpus: 1
  max_lifetime: 10m
```

---

## Artifact System (internal/sandbox/artifact.go)

```go
// Artifact represents a structured markdown file exchanged between agents.
type Artifact struct {
    Schema     string            // e.g. "orqestra/specification@1"
    Session    string            // session name
    ProducedBy string            // agent role
    Step       string            // e.g. "02-planner"
    Timestamp  time.Time
    InputHash  string            // SHA-256 of the input artifact (chain verification)
    Body       string            // markdown body after frontmatter
    Metadata   interface{} // parsed via yaml.v3 into a strict schema struct
}

// WriteArtifact serializes an artifact to a file with YAML frontmatter.
func WriteArtifact(path string, art Artifact) error

// ReadArtifact parses a markdown file with YAML frontmatter into an Artifact.
func ReadArtifact(path string) (Artifact, error)

// HashArtifact returns the SHA-256 hex digest of an artifact file.
func HashArtifact(path string) (string, error)

// ValidateChain checks that an artifact's InputHash matches the hash of
// the referenced input file. Returns an error if the chain is broken.
func ValidateChain(artifactPath, inputPath string) error
```

---

## Dependencies to Add

```
github.com/docker/docker/client v27.1.1 (Docker Engine Go SDK)
github.com/charmbracelet/x/vt v0.x.x   (or github.com/hinshun/vt10x as fallback)
```

---

## Pipeline Flow (End-to-End Example)

```
User types: "Add retry logic to the HTTP client with exponential backoff"

1. Orchestrator generates session name: "2026-05-04T16-42-07-amber-serpent"
   Creates /workspace/.orqestra/sessions/2026-05-04T16-42-07-amber-serpent/

2. INTAKE (optional, if enabled)
   - Writes user prompt to 01-intake/input.md
   - Runs intake agent → produces cleaned prompt in 01-intake/output.md
   - [Gate: user reviews cleaned prompt]

3. PLANNER (sandboxed PTY, network=bridge)
   - TUI tab: "⚡ planner" → user sees Claude Code exploring the repo
   - Claude Code inside sandbox runs:
       grep -r "HTTPClient\|http.Client" /workspace/internal/
       curl https://pkg.go.dev/time#Duration | head -50
       cat /workspace/internal/harness/client.go
   - Writes specification to /workspace/.orqestra/output.md
   - Orchestrator extracts → saves to 02-planner/output.md
   - [Gate: user reviews specification in TUI viewport]

4. PLAN VALIDATOR (sandboxed PTY, network=none)
   - Input: specification from step 3
   - Tab: "✦ validator" → user can watch validation reasoning
   - Extracts → saves to 03-plan-validator/output.md

5. PROJECT MANAGER (sandboxed PTY, network=none)
   - Input: specification
   - Decomposes into work packages
   - Extracts → saves to 04-project-manager/output.md
   - [Gate: user reviews work packages]

6. WORKERS (parallel sandboxed PTYs, network=bridge)
   - Tabs: "✦ worker-0", "✦ worker-1"
   - Each implements their work package
   - If worker-1 needs permission: "⚡ worker-1" header blinks
   - User presses ctrl+space → switches to worker-1 tab → types "y"
   - Extracts → saves to 05-worker-N/output.md + 05-worker-N/changes/

7. WORK VALIDATOR (sandboxed PTY, network=none)
   - Input: specification + all work results
   - Validates implementation against spec
   - Extracts → saves to 06-work-validator/output.md

8. APPLY
   - Verified file changes from 05-worker-N/changes/ applied to host repo
   - Session directory preserved as complete audit trail

Post-run: ls /workspace/.orqestra/sessions/2026-05-04T16-42-07-amber-serpent/
  → every agent's input, output, system prompt, and file changes
```

---

## Risk & Mitigations

| Risk | Mitigation |
|------|-----------|
| `charmbracelet/x/vt` too experimental | Fall back to `hinshun/vt10x` |
| Input detection false positives | Conservative patterns; user can always type regardless |
| PTY resize race conditions | Debounce `WindowSizeMsg` → `Resize()` (100ms) |
| Claude Code encoding issues | `LANG=en_US.UTF-8` in container env |
| Key conflicts with bubbletea | Global hotkey table never forwarded to PTY |
| Planner abuses network access | Bridge network + `max_lifetime: 15m` hard kill |
| Agent doesn't write output.md | Timeout + check; surface last 50 PTY lines + re-run option |
| Artifact format invalid | Schema validation on extraction; reject + surface error |
| Docker not available | Fail fast at startup: "Docker required for sandboxed agents" |
| Session directory fills disk | Configurable retention policy; `orqestra sessions prune --older-than 7d` |
| Parallel worker session name collision | Workers use numbered step dirs (05-worker-0, 05-worker-1) — no collision |

---

## Implementation Plan

TDD, small changes, one agent at a time. Intake first — simplest agent (short-lived, no file changes to extract, no parallel workers, clear input/output contract). Iron out every bug there, then generalize.

### Phase 0: Foundation (no agent, no Docker)

Each piece is independently testable and mergeable. Zero integration risk.

#### 0.1 Session Naming

`internal/sandbox/session_name.go` — `GenerateSessionName() string`

- Two word lists (~100 adjectives, ~100 nouns), timestamp prefix
- Format: `2026-05-04T16-42-07-amber-serpent`
- Table-driven tests: format validation, no collisions in 1000 sequential calls
- `CreateSessionDir(basePath string) (string, error)` — creates the directory tree

**Tests**: `session_name_test.go` — format regex, uniqueness, directory creation + cleanup.

#### 0.2 Artifact System

`internal/sandbox/artifact.go` — read/write/hash/validate markdown artifacts with YAML frontmatter.

- `WriteArtifact(path string, art Artifact) error`
- `ReadArtifact(path string) (Artifact, error)`
- `HashArtifact(path string) (string, error)` — SHA-256
- `ValidateChain(artifactPath, inputPath string) error`
- Round-trip test: write → read → assert equal
- Golden file tests for known artifact formats
- Chain validation: write two artifacts, verify hash chain, corrupt one, assert error

**Tests**: `artifact_test.go` — round-trip, golden files, chain validation, malformed frontmatter rejection.

#### 0.3 PTYSession

`internal/harness/pty_session.go` — `Start`, `Read`, `Write`, `Resize`, `Close`, `ExitCode`.

Test against simple programs, not Claude Code:

| Test | Program | Assert |
|------|---------|--------|
| Basic I/O | `echo hello` | Read returns "hello\r\n", ExitCode 0 |
| Bidirectional | `cat` | Write "foo\n" → Read returns "foo" |
| Interactive | `sh -c "read x; echo got $x"` | Write "bar\n" → Read returns "got bar" |
| Exit code | `sh -c "exit 42"` | ExitCode 42 |
| Context cancel | `sleep 60` | Cancel ctx → Close succeeds, no zombie, no hanging fd |
| SIGTERM propagation | `sleep 60` | Close → process receives signal, exits within 5s |
| Resize | `tput cols` after Resize(120, 40) | Read returns "120" |
| Double close | any | Second Close() returns nil (idempotent) |

**Tests**: `pty_session_test.go` — all above. Verify no zombie processes with `ps` after each test.

#### 0.4 termView

`internal/tui/view_term.go` — virtual terminal buffer + rendering.

- Feed canned ANSI byte sequences into `vt.Terminal`
- Assert rendered text output
- Verify: terminal bell (`\a`) consumed (not rendered as `^G`)
- Verify: cursor movement sequences render correctly
- Verify: resize updates VT buffer dimensions
- Verify: keystroke forwarding calls `Write()` on a mock PTY

**Tests**: `view_term_test.go` — canned input → expected output, bell handling, resize.

---

### Phase 1: Intake Agent End-to-End

The goal is one agent — Intake — running Claude Code in a real Docker sandbox with a real PTY, visible in a real TUI tab, with correct lifecycle management. Every bug surfaces here.

#### 1.1 SandboxedPTYRunner.Prepare()

Wire Prepare with a real Docker sandbox:

- Create session directory on host
- Write input.md + system-prompt.md to step directory
- Provision Docker container
- Stage input.md inside container at `/workspace/.orqestra/input.md`
- Assert: container exists, file is readable inside container
- `t.Cleanup`: destroy sandbox

**Tests**: `pty_runner_test.go` — requires Docker (build tag `integration`).

#### 1.2 RunningAgent.Launch() — trivial command

Start a trivial command (`echo hello`) in the PTY inside Docker, not Claude Code yet:

- Assert: `PTYOutputMsg` received with "hello"
- Assert: `PTYDoneMsg` received with exit code 0
- Assert: container cleaned up after Destroy()
- Assert: no orphaned containers (`docker ps -a --filter label=orqestra`)

**Tests**: `pty_runner_test.go` — integration test.

#### 1.3 SandboxedPTYRunner.Collect()

Pre-write `output.md` inside a sandbox manually, then call Collect:

- Assert: artifact extracted to host session directory
- Assert: valid frontmatter parsed
- Assert: `input_hash` validation passes against known input

**Tests**: `pty_runner_test.go` — integration test.

#### 1.4 Two-way I/O through Docker PTY

Launch `cat` inside the Docker sandbox PTY:

- Write "hello\n" through `PTYSession.Write()`
- Assert: "hello" appears in PTY output
- This proves the bidirectional channel works through `docker exec -it`

**Tests**: `pty_runner_test.go` — integration test.

#### 1.5 Signal handling & crash resilience

| Scenario | Action | Assert |
|----------|--------|--------|
| Container killed externally | `docker kill <id>` | PTY read returns EOF → `PTYDoneMsg` with error → sandbox Destroy() succeeds |
| Context cancelled | Cancel ctx | PTY session closed → container destroyed within 30s |
| Claude Code exits non-zero | Agent writes bad output | Error surfaced in `PTYDoneMsg`, sandbox destroyed |
| Docker exec dies, container lives | Kill exec PID | PTY EOF → Destroy kills container → no orphan |
| Double Destroy | Call Destroy() twice | Second call is no-op, no error |
| Reaper catches orphan | Crash orchestrator mid-run | Reaper (existing) kills container by label after max_lifetime |

**Tests**: `pty_runner_crash_test.go` — integration tests with build tag.

#### 1.6 TUI tab rendering

Wire `termView` into `tabsView` for the Intake session:

- Replace Intake's current text-in-tab-area display with a `termView` tab
- Verify ANSI escape sequences render correctly (Claude Code uses colors, spinners)
- Verify terminal bell (`\a`) triggers tab notification, not `^G` in output
- Verify window resize propagates: `WindowSizeMsg` → `PTYSession.Resize()` → VT buffer resize → re-render
- Verify tab header states: provisioning `◌` → running `✦` → done `✓` / failed `✗`

**Tests**: `view_term_test.go` (unit, canned input), manual verification with real Claude Code.

#### 1.7 Input detection + quick-switch

- Idle timeout: no PTY output for 3s while running → `PTYNeedsInputMsg`
- Pattern matching: scan VT buffer for `"(y/n)"`, `"? "`, `"> "`, `": "`
- Tab header blinks `⚡` (yellow) when needs input
- `ctrl+space` switches to the needing-input tab
- Status bar: `⚡ Tab 1 needs input — Ctrl+Space`

**Tests**: `input_detector_test.go` — feed known prompt patterns, assert detection fires. `view_tabs_test.go` — assert header rendering per state.

#### 1.8 Focus routing

- `FocusTabs` active → all keystrokes except global hotkeys forward to `termView`
- Global hotkeys: `alt+1..9`, `ctrl+space`, `esc`.
- `ctrl+c` within tab: Single press displays "Press Ctrl+C again to force kill the session". Double press sends kill signal to Docker API to terminate the PTY container completely (do not proxy to Claude Code).
- `esc` releases focus to command bar
- `Enter` / clicking tab area grants `FocusTabs`

**Tests**: `focus_test.go` — assert key routing per focus state.

#### 1.9 E2E: Real Claude Code in Real Docker

Single integration test — the graduation test for Phase 1:

```go
//go:build e2e

func TestE2E_IntakePTY(t *testing.T) {
    // 1. Generate session: 2026-05-04T...-<adj>-<noun>
    // 2. Write user prompt as input artifact
    // 3. Prepare sandbox (Docker, network=bridge)
    // 4. Launch Claude Code PTY inside sandbox
    // 5. Wait for PTYDoneMsg (timeout: 3 min)
    // 6. Collect output artifact
    //
    // Assert:
    //   - output.md exists in session dir with valid frontmatter
    //   - output body is non-empty markdown
    //   - input_hash in output matches hash of input.md
    //   - system-prompt.md saved in session dir
    //   - container destroyed (docker ps shows nothing)
    //   - volumes removed (docker volume ls shows nothing)
    //   - no zombie processes
    //   - session dir contains: input.md, output.md, system-prompt.md
}
```

**This test runs real Claude Code. It costs tokens. It's slow (~1-2 min). It's the proof that everything works.**

---

### Phase 2: Generalize (after Intake is rock-solid)

Only start this when Phase 1 has zero flakes, zero orphans, correct rendering, correct signal handling.

#### 2.1 Planner

- Same `SandboxedPTYRunner` path as Intake
- Sandbox config: `network=bridge`, read-only repo mount at `/workspace`
- Planner system prompt instructs: read input.md, research the codebase, write specification to output.md
- Human gate reads the extracted `output.md` artifact and displays in confirm viewport
- Parse specification from markdown artifact body (replaces JSON parsing)

#### 2.2 Plan Validator + PM

- Validator: `network=none` (air-gapped), reads specification artifact, writes validation report
- PM: `network=none`, reads specification, writes project plan with work packages
- Human gate on decomposed work packages

#### 2.3 Workers

- Parallel PTY tabs (`05-worker-0`, `05-worker-1`, ...)
- Each in its own sandbox with `network=bridge`
- File change extraction via overlayfs snapshot diff or git diff (existing `ExtractChanges`)
- Changes staged to `<session>/<step>/changes/`
- Verify + apply to host repo

#### 2.4 Work Validator

- Air-gapped sandbox, reads specification + all work results
- Writes validation report

#### 2.5 Remove Old Paths

- Delete `streamView` — fully replaced by `termView`
- Remove `RunPrint`/`RunStreaming` from agent execution paths (keep for llama-server API calls)
- Remove `planner.PlanStreaming()`, `pm.DecomposeStreaming()`
- Remove JSON specification as inter-agent protocol
- Remove `--output-format stream-json` usage
- Remove `SandboxedCLIRunner.RunPrint()` / `RunStreaming()`

#### 2.6 Session Management CLI

- `orqestra sessions list` — list session directories with metadata
- `orqestra sessions inspect <name>` — show pipeline trace
- `orqestra sessions prune --older-than 7d` — retention policy

---

### Key Watchpoints

- **`docker exec -it` requires a TTY on the calling side** — `creack/pty` allocates one, but verify `isatty` returns true inside the container
- **`--dangerously-skip-permissions` + sandbox = correct boundary** — the PTY must NOT leak the host terminal's raw mode to Docker
- **VT parser choice** — try `charmbracelet/x/vt` first; if it chokes on Claude Code's xterm-256color output, switch to `vt10x` before going further
- **Bell handling** — Claude Code rings `\a` on completion; VT parser must swallow it and optionally trigger a TUI notification
- **Orphan containers on crash** — the existing Reaper handles this by label + max_lifetime, but verify it catches PTY-spawned containers too

---

## What Gets Removed (Phase 2.5)

- `planner.PlanStreaming()` — replaced by sandbox PTY + artifact extraction
- `pm.DecomposeStreaming()` — same
- `harness.CLIRunner` for agent execution — retained only for cheap llama-server API calls
- `streamView` — fully replaced by `termView`
- JSON specification as inter-agent protocol — replaced by markdown artifacts
- `--output-format stream-json` flag usage — agents run in native interactive mode
- `SandboxedCLIRunner.RunPrint()` / `RunStreaming()` — replaced by `SandboxedPTYRunner`

## Out of Scope

- Mouse passthrough to PTY (future)
- Scrollback search within terminal buffer (future)
- Copy/paste from terminal view (future — selection model)
- Windows ConPTY support (macOS/Linux only)
- Agent-to-agent direct communication (always through orchestrator + filesystem)
- MCP server access from within sandbox (future — socket forwarding)
- Session diffing (comparing two session runs — future)