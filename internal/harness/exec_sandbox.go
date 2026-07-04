package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	leash "github.com/aka-rider/leash"
)

// runningProcess abstracts the two ways Run obtains a live subprocess, so
// Run's downstream goroutine/parse/join logic is identical regardless of
// which path started it.
type runningProcess struct {
	stdout io.Reader
	stdin  io.WriteCloser // nil when spec has no input plane
	wait   func() error   // blocks until the process/leash call finishes

	// drainStdout, when non-nil, MUST be called after parseStream(stdout, ...)
	// returns and before wait() — see startSandboxed's doc comment for why
	// this exists. nil for startDirect, which needs no such hook.
	drainStdout func()
}

// errLeashDone is the cause startSandboxed's own goroutine sets on execCtx,
// exactly once, after leash.Execute has fully returned and execErr has been
// written. It is the ONLY intentional caller of the cancel func — mirroring
// this exact codebase's own established discipline for AgentSupervisor
// (internal/orchestrator/agent_supervisor.go's doc comment: "Cancellation
// discipline: the only stop signal is context.Context. stop() is the only
// intentional caller of cancelCause"), which is also where this ctx's own
// deadline is attached in the first place (context.WithTimeout(parent,
// spec.Timeout) wrapped in context.WithCancelCause — confirmed by reading
// AgentSupervisor.Run directly). startSandboxed's execCtx follows the same
// pattern one layer further in, rather than inventing a parallel one.
var errLeashDone = errors.New("harness: leash.Execute finished")

// startSandboxed runs spec's subprocess inside a leash (macOS Seatbelt)
// sandbox. leash.Execute is a single blocking call with no separate
// start/wait split and never exposes the underlying *exec.Cmd, so it runs in
// its own goroutine while the caller reads stdout as it streams through an
// io.Pipe — Go's os/exec already copies to a plain io.Writer as data arrives
// (not buffered-until-exit), so this is a faithful analogue of
// cmd.StdoutPipe().
//
// Termination signal — corrected after direct user feedback rejecting an
// earlier draft's plain `<-execErrCh` channel wait as an unprincipled "side
// channel" for completion detection, competing with ctx instead of being
// derived from it: wait() below blocks on EXACTLY ONE thing, execCtx.Done()
// — a context derived from ctx via context.WithCancelCause. execCtx.Done()
// fires in exactly two ways, and context.Cause(execCtx) tells wait() which
// one happened:
//
//	(a) the goroutine below calls cancelExec(errLeashDone) once
//	    leash.Execute has actually returned and execErr has been fully
//	    written — this is the ONLY place execErr is read, and it's
//	    provably race-free: context cancellation establishes a
//	    happens-before relationship between the code preceding cancelExec
//	    and any code that unblocks because of it (same guarantee a channel
//	    close provides), so execErr's write is guaranteed visible here.
//	(b) ctx itself is canceled or its deadline elapses (the role's real
//	    Timeout, attached upstream in AgentSupervisor.Run before this ctx
//	    ever reaches harness.Run) — context.WithCancelCause propagates a
//	    parent's cancellation down to execCtx automatically, entirely
//	    independent of whether the goroutine has finished writing execErr
//	    yet. wait() MUST NOT read execErr in this branch — the goroutine
//	    could still be concurrently writing it — so it returns ctx.Err()
//	    instead, which is also the value Run()'s own (unchanged)
//	    ctx.Err()-checked tail branch reports to its caller regardless of
//	    whatever wait() itself returns.
//
// io.Pipe deadlock, caught by critic review: io.Pipe is synchronous — a
// write blocks until a matching read consumes it. If parseStream(pr, ...)
// returns EARLY (a scanner error on an oversized/malformed line, which the
// harness explicitly defends against) while os/exec's internal copy
// goroutine still has buffered bytes to flush into pw, that goroutine blocks
// on pw.Write forever, because nothing calls pr.Read again. drainStdout
// closes the read side with an error, which unblocks any pending or future
// pw.Write with an error return instead of a block, DETERMINISTICALLY —
// Go's io.Pipe guarantees this synchronously, no timing dependency. Run's
// caller must invoke it after parseStream returns and before wait().
func startSandboxed(ctx context.Context, spec ProcessSpec, binary string, args []string, hasInputPlane bool, stderrBuf io.Writer) (*runningProcess, error) {
	mEnv, err := buildModelEnvFromSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox model env: %w", err)
	}

	l := leash.Leash{
		Program: binary,
		Args:    args,
		Dir:     spec.WorkDir,
		// Every role needs Claude/Anthropic API access — not a tunable.
		Network:      true,
		Reads:        sandboxReads(spec.Sandbox),
		Writes:       sandboxWrites(spec.Sandbox),
		Execs:        append([]string(nil), spec.Sandbox.Execs...),
		FutureWrites: append([]string(nil), spec.Sandbox.FutureWrites...),
		ExtraEnv:     mergeExtraEnv(mEnv, spec.Sandbox.Env, spec.Sandbox.ExtraEnv),
		ProxyEnv:     spec.Sandbox.ProxyEnv,
		Stderr:       stderrBuf,
		DenyTag:      "orqestra", // cosmetic parity with today's (deny default (with message "orqestra"))
	}

	pr, pw := io.Pipe()
	l.Stdout = pw

	var stdinWriter *io.PipeWriter
	if hasInputPlane {
		var stdinReader *io.PipeReader
		stdinReader, stdinWriter = io.Pipe()
		l.Stdin = stdinReader
	}

	execCtx, cancelExec := context.WithCancelCause(ctx)
	var execErr error
	go func() {
		execErr = leash.Execute(ctx, l)
		// By the time Execute returns, its internal cmd.Wait has already
		// confirmed every byte of stdout was copied (Go's exec package
		// waits for its own stdout-copy goroutine before Wait returns), so
		// closing here cannot truncate output — parseStream sees a clean EOF.
		_ = pw.Close()
		cancelExec(errLeashDone) // the only intentional cancelExec call
	}()

	return &runningProcess{
		stdout: pr,
		stdin:  stdinWriter,
		wait: func() error {
			<-execCtx.Done()
			if !errors.Is(context.Cause(execCtx), errLeashDone) {
				return ctx.Err()
			}
			return execErr
		},
		drainStdout: func() {
			_ = pr.CloseWithError(io.ErrClosedPipe)
		},
	}, nil
}

// sandboxReads/sandboxWrites translate SandboxConfig's RepoPath+Writable+
// WorktreePath discriminators into the flat Reads/Writes leash.Leash wants.
func sandboxReads(sb SandboxConfig) []string {
	reads := append([]string(nil), sb.Reads...)
	if !sb.Writable {
		reads = append(reads, sb.RepoPath)
	}
	return reads
}

func sandboxWrites(sb SandboxConfig) []string {
	writes := append([]string(nil), sb.Writes...)
	if sb.Writable {
		writes = append(writes, sb.RepoPath)
	}
	if sb.WorktreePath != "" {
		writes = append(writes, sb.WorktreePath)
	}
	return writes
}

// mergeExtraEnv folds the model-routing KEY=VALUE pairs (mEnv, from
// spec.Model), the existing Env []string channel (main.go's
// modelEnv/integratorEnv — kept as-is, see plan notes on why this isn't
// being removed), and the user's sandbox.extra_env YAML map into leash's
// map[string]string ExtraEnv shape. Later entries win on key collision,
// mirroring today's HarnessEnv: append(mEnv, spec.Sandbox.Env...) ordering,
// with the user's explicit extra_env applied last (highest precedence).
func mergeExtraEnv(mEnv, sandboxEnv []string, userExtraEnv map[string]string) map[string]string {
	out := make(map[string]string, len(mEnv)+len(sandboxEnv)+len(userExtraEnv))
	for _, kv := range mEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	for _, kv := range sandboxEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	for k, v := range userExtraEnv {
		out[k] = v
	}
	return out
}
