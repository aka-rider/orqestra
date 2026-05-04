# Work Package: pty-session

| Field | Value |
|-------|-------|
| **ID** | `pty-session` |
| **Wave** | 3 |
| **depends_on** | `docker-sdk-migration` |
| **Files** | `internal/harness/pty_session.go`, `internal/harness/pty_session_test.go` |

## Goal

Implement the PTYSession type that wraps Docker SDK's exec-attach with TTY allocation for bidirectional subprocess I/O. Provides Start, Read, Write, Resize, Close, ExitCode. Uses `ContainerExecCreate` + `ContainerExecAttach` with `Tty: true` — not the local `creack/pty` library (we're running inside Docker, not locally).

## Steps

1. Create `internal/harness/pty_session.go` with:
   - `SessionState` type (iota: `SessionPending`, `SessionRunning`, `SessionDone`, `SessionFailed`).
   - `PTYSession` struct: `ID`, `Name`, `State`, internal fields (`conn types.HijackedResponse` from Docker SDK, `execID string`, `cli *client.Client`, `mu sync.Mutex`, `cancel context.CancelFunc`, `exitCode int`, `exited bool`, `cols/rows uint16`, `containerID string`).
   - `Start(ctx context.Context, cli *client.Client, containerID string, command []string, env []string, cols, rows uint16) error`:
     - `ContainerExecCreate(ctx, containerID, types.ExecConfig{Cmd, Env, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true, ConsoleSize: [2]uint{rows, cols}})`
     - `ContainerExecAttach(ctx, execID, types.ExecStartCheck{Tty: true, ConsoleSize: [2]uint{rows, cols}})` — returns hijacked connection.
     - The hijacked connection's `Conn` implements `io.ReadWriteCloser` — this IS the TTY stream.
     - Start background goroutine monitoring exec exit via `ContainerExecInspect` polling.
   - `Write(p []byte) (int, error)` — writes to hijacked connection (reaches subprocess stdin).
   - `Read(p []byte) (int, error)` — reads from hijacked connection (subprocess stdout+stderr, already merged in TTY mode). Blocking.
   - `Resize(cols, rows uint16) error` — calls `cli.ContainerExecResize(ctx, execID, container.ResizeOptions{Height: rows, Width: cols})`.
   - `ExitCode() int` — returns subprocess exit code (valid after Read returns io.EOF). Obtained from `ContainerExecInspect`.
   - `Close() error` — closes hijacked connection, cancels context. Idempotent (second call returns nil). Does NOT destroy the container (that's the runner's job).

   Note: No `github.com/creack/pty` dependency. Docker SDK handles TTY allocation server-side. The hijacked connection is a raw TCP/Unix socket stream — no local PTY needed.

2. Create `internal/harness/pty_session_test.go` (build tag `//go:build integration`) — requires Docker running with a container to exec into:

   | Test | Command inside container | Assert |
   |------|---------|--------|
   | Basic I/O | `echo hello` | Read returns "hello\r\n", ExitCode 0 |
   | Bidirectional | `cat` | Write "foo\n" → Read returns "foo" |
   | Interactive | `sh -c "read x; echo got $x"` | Write "bar\n" → Read returns "got bar" |
   | Exit code | `sh -c "exit 42"` | ExitCode 42 |
   | Context cancel | `sleep 60` | Cancel ctx → Close succeeds, exec process exits within 5s |
   | Resize | `sh -c "sleep 0.1; tput cols"` after Resize(120, 40) | Read returns "120" |
   | Double close | any | Second Close() returns nil (idempotent) |

   Test setup: spin up a temporary container (`docker run -d orqestra-sandbox:latest sleep infinity`), exec PTYSession commands inside it, tear down container in `t.Cleanup`.

## Acceptance

- `go test ./internal/harness/ -run TestPTYSession -tags integration` passes.
- `go vet ./internal/harness/` clean.
- No `github.com/creack/pty` dependency — uses Docker SDK exec-attach only.
- Exec sessions inside Docker containers correctly allocate a TTY (verified by `tput cols` returning the configured width).
- Files touched: ONLY `internal/harness/pty_session.go`, `internal/harness/pty_session_test.go`.
