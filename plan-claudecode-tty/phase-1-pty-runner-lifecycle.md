# Work Package: pty-runner-lifecycle

| Field | Value |
|-------|-------|
| **ID** | `pty-runner-lifecycle` |
| **Wave** | 5 |
| **depends_on** | `session-naming`, `artifact-system`, `pty-session`, `overlayfs-sandbox`, `sandbox-file-transfer` |
| **Files** | `internal/harness/pty_runner.go`, `internal/harness/pty_runner_test.go`, `internal/harness/pty_runner_crash_test.go` |

## Goal

Implement `SandboxedPTYRunner` with the full agent lifecycle: Prepare → Launch → pumpOutput → Collect → Destroy. Uses Docker SDK exec-attach (via PTYSession), SDK-based file transfer (via StageInputs/ExtractArtifact), and OverlayFS-based change extraction. Covers spec steps 1.1 through 1.5.

## Steps

### 1.1 — SandboxedPTYRunner.Prepare()

1. Create `internal/harness/pty_runner.go` with:
   - `SandboxedPTYRunner` struct holding session config, sandbox builder, and verifier.
   - `RunningAgent` struct: `Config`, `Sandbox`, `PTY *PTYSession`, `SessionDir`, `StepName`, `tabIndex int`.
   - `Prepare(ctx context.Context, cfg AgentConfig) (RunningAgent, error)`:
     - Generate session name via `GenerateSessionName()`.
     - Create session directory on host: `<basePath>/<sessionName>/<stepName>/`.
     - Write `input.md` and `system-prompt.md` to step directory on host.
     - Provision Docker container (via `DockerSandbox.Provision()` — uses Docker SDK, OverlayFS).
     - Stage inputs via `sandbox.StageInputs(ctx, inputMD, systemPromptMD)` (Docker SDK CopyToContainer).
     - Return `RunningAgent` with container ID and session paths.

### 1.2 — RunningAgent.Launch()

1. Add to `pty_runner.go`:
   - `Launch(ctx context.Context, agent *RunningAgent, send func(tea.Msg)) error`:
     - Build Claude Code command: `[]string{"claude", "--dangerously-skip-permissions", "--system-prompt", "$(cat /workspace/.orqestra/system-prompt.md)"}`.
     - Start `PTYSession` via Docker SDK exec-attach (`pty.Start(ctx, cli, containerID, command, env, cols, rows)`).
     - Launch `pumpOutput` goroutine (reads from hijacked connection → sends `PTYOutputMsg`).
     - Launch `sendInitialPrompt` goroutine (waits 2s, writes instruction to hijacked connection).
   - `sendInitialPrompt()` — waits, then writes the "read input.md, write output.md" instruction.

### 1.3 — SandboxedPTYRunner.Collect()

1. Add to `pty_runner.go`:
   - `Collect(ctx context.Context, agent RunningAgent) (CollectResult, error)`:
     - Read output artifact via `sandbox.ExtractArtifact(ctx)` (Docker SDK CopyFromContainer).
     - Parse as Artifact via `ReadArtifact`.
     - Validate chain: artifact's `InputHash` matches hash of input.md.
     - Save artifact to `<sessionDir>/<stepName>/output.md` on host.
     - For worker agents (step prefix "05"): call `ExtractChanges()` (Docker SDK ContainerDiff), verify, stage to `changes/` subdirectory via `CopyFileFromContainer`.
     - Return `CollectResult{Artifact, Changes}`.
   - `CollectResult` struct: `Artifact`, `Changes []ChangedFile`.

### 1.4 — Two-way I/O (pumpOutput)

1. Add to `pty_runner.go`:
   - `pumpOutput(send func(tea.Msg))`:
     - 4KB read buffer, loop until EOF or context cancel.
     - Deep copy bytes before sending (`PTYOutputMsg{TabIndex, Data}`).
     - On EOF/error: send `PTYDoneMsg{TabIndex, Err, ExitCode}`.

### 1.5 — Destroy + Signal Handling

1. Add to `pty_runner.go`:
   - `Destroy(ctx context.Context, agent RunningAgent)`:
     - Close PTY if non-nil.
     - Destroy sandbox with 30s timeout context.
     - Log errors but don't fail (idempotent).
     - Emit `StateDestroyed` event.

2. Create `internal/harness/pty_runner_test.go` (build tag `//go:build integration`):
   - Test Prepare: container exists after call, input.md readable inside container.
   - Test Launch with `echo hello`: `PTYOutputMsg` received with "hello", `PTYDoneMsg` with exit 0.
   - Test Collect: pre-write output.md inside sandbox, call Collect, assert artifact on host.
   - Test two-way I/O: launch `cat` inside Docker PTY, Write "hello\n", assert "hello" in output.
   - All tests call Destroy in `t.Cleanup`, assert no orphaned containers via `docker ps -a --filter label=orqestra`.

3. Create `internal/harness/pty_runner_crash_test.go` (build tag `//go:build integration`):

   | Scenario | Action | Assert |
   |----------|--------|--------|
   | Container killed externally | `docker kill <id>` | PTY read returns EOF → `PTYDoneMsg` with error → Destroy succeeds |
   | Context cancelled | Cancel ctx | PTY session closed → container destroyed within 30s |
   | Claude Code exits non-zero | Agent writes bad output | Error surfaced in `PTYDoneMsg`, sandbox destroyed |
   | Docker exec dies, container lives | Kill exec PID | PTY EOF → Destroy kills container → no orphan |
   | Double Destroy | Call Destroy() twice | Second call is no-op, no error |

## Acceptance

- `go test ./internal/harness/ -run TestPTYRunner -tags integration` passes (requires Docker running).
- `go test ./internal/harness/ -run TestPTYRunnerCrash -tags integration` passes.
- `go vet ./internal/harness/` clean.
- After all tests: `docker ps -a --filter label=orqestra` returns empty.
- No zombie processes.
- Files touched: ONLY `internal/harness/pty_runner.go`, `internal/harness/pty_runner_test.go`, `internal/harness/pty_runner_crash_test.go`.
