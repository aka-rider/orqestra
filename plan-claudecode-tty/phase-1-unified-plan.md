# Phase 1 & 2: Intake Agent Lifecycle & Orchestration

This is the actionable, chronological implementation plan for the Intake agent orchestration using existing PTY, sandbox, and TUI infrastructure. Targets local Qwen3.6 via config-resolved `ResolvedModel` routed through Docker outbound networking.

**Existing infrastructure (already implemented & tested — DO NOT recreate):**

- `sandbox.Sandbox` interface: `Provision`, `Exec`, `ExtractChanges`, `CopyOut`, `Destroy`
- `sandbox.DockerSandbox`: seed-and-commit provisioning, OverlayFS diff extraction
- `sandbox.PTYSession`: bidirectional TTY I/O via Docker exec attach (Read/Write/Resize/Close/ExitCode)
- `sandbox.StageInputs(ctx, Session, inputMD, systemPromptMD)`: stages files into `/workspace/.orqestra/<session>/`
- `sandbox.ExtractArtifact(ctx, Session)`: extracts `output.md` from session dir (10MB cap)
- `sandbox.CopyFileFromContainer`: single-file extraction with ZipSlip protection
- `sandbox.NewReaper(tracker, maxLifetime)`: container lifecycle management
- `harness.BuildModelEnv(resolved, small)`: dynamic env-var construction for any model/provider
- `tui.termView`: VT100 emulator with `PTYWriter` interface, `PTYOutputMsg`/`PTYDoneMsg`/`PTYNeedsInputMsg`
- `tui.keyToBytes()`: keystroke → ANSI serialization, direct `PTYWriter.Write()` on focus

---

## Step 1: Intake Runner (`internal/intent`)

**Goal:** Orchestrate a single intake agent session using existing sandbox + PTY primitives.

1. **Define `IntakeRunner` struct:**

   ```go
   type IntakeRunner struct {
       sandbox  *sandbox.DockerSandbox
       resolved config.ResolvedModel
       small    *config.ResolvedModel
   }
   ```

2. **`Execute` method:**

   ```go
   func (r *IntakeRunner) Execute(ctx context.Context, sess sandbox.Session, prompt string, send func(tea.Msg)) error
   ```

   - Calls `r.sandbox.StageInputs(ctx, sess, prompt, systemPrompt)` to stage the user prompt.
   - Constructs command: `[]string{"claude", "-p", "Process /workspace/.orqestra/<session>/input.md and write output to /workspace/.orqestra/<session>/output.md", "--output-format", "stream-json", "--verbose"}`.
   - Constructs env via `harness.BuildModelEnv(r.resolved, r.small)`.
   - Creates `sandbox.NewPTYSession(id, sess.Name, cli)` and calls `Start(ctx, containerID, cmd, env, 120, 40)`.
   - **Read goroutine:** Reads from `PTYSession`, sends `PTYOutputMsg{TabIndex, Data}` via `send()`. On EOF, sends `PTYDoneMsg`.
   - Blocks on `<-pty.eofCh` (via Read returning `io.EOF`) to detect completion.
   - Calls `r.sandbox.ExtractArtifact(ctx, sess)` to retrieve the bounded output.
3. **Termination Strategy:** System prompt instructs the agent to write its output and exit. `PTYSession.monitorExit()` detects exit via Docker exec inspect. No `/quit` command needed — natural process exit suffices.

## Step 2: Harness Launch Configuration (`internal/harness`)

**Goal:** Provide a reusable function to build the Claude Code CLI launch command for PTY-mode execution.

1. **Add `BuildPTYCommand(systemPrompt string, interactive bool) []string`:**
   - Non-interactive (intake/validation): `["claude", "-p", prompt, "--output-format", "stream-json", "--verbose"]`
   - Interactive (worker): `["claude", "--dangerously-skip-confirmation"]` (env var `CLAUDE_CODE_APPROVE_ALL=true` as fallback)
   - Verify exact flag name against Claude Code CLI `--help` at implementation time.
2. **Environment construction** uses existing `BuildModelEnv(resolved, small)` — no hard-coded IPs.
3. **Readiness detection:** For non-interactive mode, readiness = first bytes received on PTY stdout. For interactive mode, detect idle (no output for 2s after initial burst) — this matches the existing `PTYNeedsInputMsg` pattern already in `termView`.

## Step 3: Artifact Validation (`internal/plan`)

**Goal:** Add hash-chain validation for inter-agent artifact integrity.

1. **Add to existing `internal/plan` package:**

   ```go
   // ArtifactMeta is YAML frontmatter for inter-agent artifacts.
   type ArtifactMeta struct {
       Agent     string `yaml:"agent"`
       Session   string `yaml:"session"`
       InputHash string `yaml:"input_hash"` // SHA-256 of the input artifact
       CreatedAt string `yaml:"created_at"`
   }

   func ParseArtifact(raw []byte) (ArtifactMeta, []byte, error)
   func ValidateChain(current, parentContent []byte) error
   ```

2. **Parsing:** Split on `---` YAML boundaries. Validate `InputHash` matches SHA-256 of parent content.
3. **Atomic writes:** Use `os.CreateTemp` + `os.Rename` pattern with `0644` perms when persisting artifacts to host.

## Step 4: TUI Wiring (`internal/tui`)

**Goal:** Connect the intake runner to the existing TUI tab/term infrastructure.

**Already done (no changes needed):**

- `termView` renders PTY output via `PTYOutputMsg` and VT emulator
- `keyToBytes()` serializes keystrokes and writes directly to `PTYWriter`
- `PTYNeedsInputMsg` triggers "Waiting for input" status
- `PTYDoneMsg` renders exit status
- Bell stripping (`\a`) is handled by the VT emulator

**New wiring needed:**

1. **Tab creation (`model.go`):** When `IntakeRunner.Execute` starts, emit a message to create a new `termView` tab and attach the `PTYSession` via `AttachPTY()`.
2. **Focus management:** Ctrl+Space cycles to the first tab with `needsInput == true` (existing field on `termView`).
3. **Kill semantics:** Double Ctrl+C — first press sets `ShowKillWarning`, second press calls `PTYSession.Close()` which sends SIGINT and tears down the connection.

## Step 5: Integration Testing (`internal/intent/intake_e2e_test.go`)

**Goal:** Real end-to-end loop verification.

1. **Tagging:** `//go:build integration`
2. **Setup:**

   ```go
   reaper := sandbox.NewReaper(tracker, 10*time.Minute)
   defer reaper.CleanupAll(ctx)

   sb := sandbox.NewDockerSandbox(cfg, repoPath, env)
   defer sb.Destroy(ctx)
   err := sb.Provision(ctx)
   ```

3. **Test Flow:**
   - Create `sandbox.Session{Name: "test-intake", StartedAt: time.Now()}`.
   - Call `IntakeRunner.Execute(ctx, sess, "Build a REST API for...", collectMsgs)`.
   - Assert: `PTYOutputMsg` messages received (agent produced output).
   - Assert: `PTYDoneMsg` received with exit code 0.
   - Assert: `sb.ExtractArtifact(ctx, sess)` returns non-empty bytes.
   - If hash-chain is enabled: `plan.ValidateChain(output, input)` succeeds.
4. **Zombie prevention:** Reaper with 10-minute lifetime + `defer sb.Destroy(ctx)` ensures no leaked containers.
