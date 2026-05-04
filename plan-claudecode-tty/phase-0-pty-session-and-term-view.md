# Work Package: pty-session + term-view

| Field | Value |
|-------|-------|
| **ID** | `pty-session-term-view` |
| **Wave** | 3–4 |
| **depends_on** | `docker-sdk-migration` |
| **Files** | `internal/sandbox/pty_session.go`, `internal/sandbox/pty_session_test.go`, `internal/tui/view_term.go`, `internal/tui/view_term_test.go`, `internal/tui/messages.go` |

## Goal

Implement the full PTY pipeline: `PTYSession` uses the Docker native Go SDK (`ContainerExecCreate` and `ContainerExecAttach`) for bidirectional TTY I/O with a container subprocess; `termView` renders the PTY output in a virtual terminal buffer inside the TUI.

The data flow is:

```
TUI (bubbletea)
  → termView.keyToBytes() → PTYSession.Write()
    → Docker SDK HijackedResponse.Conn
      → Docker Daemon API (exec stream)
        → Container TTY (allocated by ExecConfig.Tty)
          → Claude Code (subprocess inside container)
          ← stdout/stderr merged on TTY
        ← Container TTY
      ← Docker Daemon API (exec stream)
    ← Docker SDK HijackedResponse.Conn
  ← PTYSession.Read() → PTYOutputMsg → termView VT buffer → View()
```

The Docker SDK's HTTP hijack mechanism provides a direct TCP/Unix socket stream to the container session. Setting `Tty: true` in `ExecConfig` instructs Docker to allocate a real PTY for the container process (`isatty` returns true) and merge stdout/stderr into a single raw byte stream, replicating local PTY semantics without requiring the CLI.

---

## Part 1 — PTYSession (`internal/sandbox/pty_session.go`)

### Design

```go
type SessionState int

const (
    SessionPending SessionState = iota
    SessionRunning
    SessionDone
    SessionFailed
)

type PTYSession struct {
    ID    string
    Name  string
    State SessionState

    cli       *dockerclient.Client
    execID    string
    conn      types.HijackedResponse
    mu        sync.Mutex
    cancel    context.CancelFunc
    exitCode  int
    exited    bool
    cols, rows uint
    containerID string
}
```

### Methods

#### `Start(ctx context.Context, containerID string, command []string, env []string, cols, rows uint) error`

1. Create cancellable context: `ctx, ps.cancel = context.WithCancel(ctx)`.
2. Build exec config: `types.ExecConfig{Cmd: command, Env: env, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true}`.
3. Call `ps.cli.ContainerExecCreate(ctx, containerID, execConfig)`.
4. Store `ps.execID` from the response.
5. Allocate PTY via hijack: `ps.conn, err = ps.cli.ContainerExecAttach(ctx, ps.execID, types.ExecStartCheck{Tty: true})`.
6. Set `ps.State = SessionRunning`.
7. Launch exit monitor goroutine:

   ```go
   go func() {
       // Block until the hijacked network connection reaches EOF (process exit)
       io.Copy(io.Discard, ps.conn.Reader)

       // Explicitly inspect the exec process to retrieve the correct ExitCode
       inspect, err := ps.cli.ContainerExecInspect(context.Background(), ps.execID)

       ps.mu.Lock()
       ps.exited = true
       if err == nil {
           ps.exitCode = inspect.ExitCode
       }
       if err != nil || inspect.ExitCode != 0 {
           ps.State = SessionFailed
       } else {
           ps.State = SessionDone
       }
       ps.mu.Unlock()
   }()
   ```

#### `Write(p []byte) (int, error)`

Writes to `ps.conn.Conn`. No mutex — the hijacked `net.Conn` connection is safe for concurrent read/write.

#### `Read(p []byte) (int, error)`

Reads from `ps.conn.Reader`. Blocking. Returns `io.EOF` when process exits and the Docker stream closes.

#### `Resize(cols, rows uint) error`

Calls `ps.cli.ContainerExecResize(ctx, ps.execID, types.ResizeOptions{Height: rows, Width: cols})`. The Docker daemon passes SIGWINCH into the container.

#### `ExitCode() int`

Returns `ps.exitCode`. Valid after Read returns `io.EOF` and the exit monitor has inspected the container.

#### `Close() error`

1. Acquire `ps.mu`.
2. If already closed, return nil (idempotent).
3. Send `\x03` (SIGINT) to the TTY to request graceful termination (`ps.Write([]byte{3})`).
4. Close `ps.conn.Close()` — explicitly tears down the hijacked socket, unblocking pending Read/Write calls immediately.
5. Cancel context.
6. Release `ps.mu`.

Does NOT destroy the container (that's the runner's job).

### Dependencies

- `github.com/docker/docker/client` — Native Docker SDK (replaces UI CLI calls).

---

## Part 2 — termView (`internal/tui/view_term.go`)

### Messages (add to `internal/tui/messages.go`)

```go
// PTYOutputMsg delivers raw bytes from the PTY session to the terminal view.
type PTYOutputMsg struct {
    TabIndex int
    Data     []byte
}

// PTYNeedsInputMsg signals that the PTY session is waiting for user input.
type PTYNeedsInputMsg struct {
    TabIndex int
}

// PTYDoneMsg signals that the PTY session has exited.
type PTYDoneMsg struct {
    TabIndex int
    Err      error
    ExitCode int
}
```

### Design

```go
// PTYWriter is the interface termView uses to send input and resize the PTY.
// Decouples TUI from concrete sandbox.PTYSession to avoid circular imports.
type PTYWriter interface {
    Write([]byte) (int, error)
    Resize(cols, rows uint) error
}

type termView struct {
    tabIndex   int
    ptySession PTYWriter   // nil until attached
    vt         *vt.Terminal // charmbracelet/x/vt virtual terminal
    needsInput bool
    done       bool
    err        error
    startedAt  time.Time
    width      int
    height     int
    focused    bool
}
```

### Constructor

`newTermView(tabIndex int, cols, rows int) termView` — initializes VT with `cols × (rows-1)` to reserve the last row for the status bar.

### Update Handler

| Message | Behavior |
|---------|----------|
| `PTYOutputMsg` (matching tabIndex) | Feed `msg.Data` to VT buffer via `vt.Write(msg.Data)`. Clear `needsInput`. |
| `PTYNeedsInputMsg` (matching tabIndex) | Set `needsInput = true`. |
| `PTYDoneMsg` (matching tabIndex) | Set `done = true`, store `err` and exit code. |
| `tea.KeyMsg` (when focused and not done) | Convert via `keyToBytes()` → call `ptySession.Write()`. Return nil cmd. |
| `tea.WindowSizeMsg` | Update `width`/`height`, resize VT to `width × (height-1)`, call `ptySession.Resize(cols, rows)` if attached. |

### View

1. Render VT screen buffer as string (full `width × (height-1)` grid).
2. Status bar (last row):
   - Running: spinner + elapsed time.
   - Needs input: `⚡ Waiting for input` (highlighted).
   - Done: `✓ Exited (code 0)`.
   - Failed: `✗ Exited (code N)` + error string.

### `keyToBytes(msg tea.KeyMsg) []byte`

Converts bubbletea key messages to raw terminal byte sequences:

| Key | Bytes |
|-----|-------|
| Enter | `\r` |
| Backspace | `\x7f` |
| Tab | `\t` |
| Escape | `\x1b` |
| Arrow Up | `\x1b[A` |
| Arrow Down | `\x1b[B` |
| Arrow Right | `\x1b[C` |
| Arrow Left | `\x1b[D` |
| Ctrl+C | `\x03` |
| Ctrl+D | `\x04` |
| Ctrl+Z | `\x1a` |
| Printable rune | UTF-8 bytes of the rune |

### Dependencies

- `github.com/charmbracelet/x/vt` — add to `go.mod` (`go get github.com/charmbracelet/x/vt`).

---

## Part 3 — Integration Tests (`//go:build integration`)

All integration tests in this package run under:

```
go test ./internal/... -run TestDocker -tags integration
```

### File: `internal/sandbox/pty_session_test.go` (`//go:build integration`)

Test setup helper: inject a native `*dockerclient.Client`, provision a temporary `alpine:latest` container via SDK (`ContainerCreate`, `ContainerStart`), tear down in `t.Cleanup`. Alpine includes a full `/bin/sh`, `cat`, `tput`, and coreutils.

| Test Name | Command | Assert |
|-----------|---------|--------|
| `TestDockerPTYSession_BasicIO` | `echo hello` | Read output contains `"hello"`, ExitCode 0 via `ContainerExecInspect` |
| `TestDockerPTYSession_Bidirectional` | `cat` | Write `"foo\n"` → Read contains `"foo"` |
| `TestDockerPTYSession_Interactive` | `sh -c "read x; echo got $x"` | Write `"bar\n"` → Read contains `"got bar"` |
| `TestDockerPTYSession_ExitCode` | `sh -c "exit 42"` | ExitCode == 42 |
| `TestDockerPTYSession_SigintClose` | `sleep 60` | Write `\x03` + Close() succeeds, inspect confirms process exited within 5s |
| `TestDockerPTYSession_Resize` | Start at 80×24, Resize(120, 40), then run `tput cols` | Read contains `"120"` |
| `TestDockerPTYSession_DoubleClose` | `echo x` | First Close() nil, second Close() nil |

### File: `internal/tui/view_term_test.go` (unit tests, no build tag)

| Test Name | Scenario |
|-----------|----------|
| `TestTermView_ANSIRendering` | Feed canned ANSI sequences via `PTYOutputMsg` → `View()` contains expected text |
| `TestTermView_BellConsumed` | Feed `\a` → not rendered as `^G` |
| `TestTermView_CursorMovement` | Feed `\x1b[2;5Hhello` → text positioned correctly in buffer |
| `TestTermView_Resize` | Send `tea.WindowSizeMsg{Width: 120, Height: 40}` → VT buffer width changes |
| `TestTermView_KeystrokeForwarding` | Mock PTYWriter, send `tea.KeyMsg` → assert `Write()` called with correct bytes |
| `TestTermView_StatusLine` | Assert status text for each state: running, needsInput, done, failed |

### File: `internal/sandbox/pty_session_e2e_test.go` (`//go:build integration`)

End-to-end tests proving TTY signal propagation through the full Docker stack:

| Test Name | Scenario |
|-----------|----------|
| `TestDockerPTY_SIGWINCH_Propagation` | Start `sh`, Resize to 132×50, run `stty size` inside → output contains `"50 132"` |
| `TestDockerPTY_SIGINT_Kills_Process` | Start `sh -c "trap 'echo caught' INT; sleep 60"`, Write `\x03` → Read contains `"caught"`, process exits |
| `TestDockerPTY_RawMode_Passthrough` | Start `cat`, write binary bytes `\x00\x01\x02` → read echoes them back (TTY raw mode) |
| `TestDockerPTY_LongOutput_NoTruncation` | Start `seq 1 5000` → read all 5000 lines without data loss |

---

## Acceptance Criteria

1. `go test ./internal/... -run TestDocker -tags integration` passes — all PTY session and E2E Docker tests green.
2. `go test ./internal/tui/ -run TestTermView` passes — all unit tests green (no Docker required).
3. `go vet ./internal/sandbox/ ./internal/tui/` clean.
4. No OS-level CLI calls or local PTY allocations (uses native Docker SDK `ContainerExecAttach`).
5. `charmbracelet/x/vt` in `go.mod` — virtual terminal for TUI rendering.
6. `termView` does NOT import `internal/sandbox` directly — uses `PTYWriter` interface.
7. TTY signal propagation verified end-to-end: SIGWINCH (resize), SIGINT (Ctrl+C), raw byte passthrough all work through the Docker API → container chain.
8. Files touched:
   - `internal/sandbox/pty_session.go`
   - `internal/sandbox/pty_session_test.go`
   - `internal/sandbox/pty_session_e2e_test.go`
   - `internal/tui/view_term.go`
   - `internal/tui/view_term_test.go`
   - `internal/tui/messages.go`
   - `go.mod`, `go.sum`
