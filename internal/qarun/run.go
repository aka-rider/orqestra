//go:build unix

// Package qarun runs a command (the test suite) under a hard wall-clock
// deadline, so a hung suite becomes a bounded, falsifiable NO-VERDICT instead of
// an indefinite hang. A hang is the worst outcome: a gate that never returns is
// neither pass nor fail, so it silently defeats every other gate.
package qarun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Outcome is the three-valued result of a bounded run.
type Outcome int

const (
	Green     Outcome = iota // command exited 0 — a real GREEN verdict
	Red                      // command exited non-zero — a real RED verdict
	NoVerdict                // deadline exceeded; child killed; NO verdict produced
)

func (o Outcome) String() string {
	switch o {
	case Green:
		return "GREEN"
	case Red:
		return "RED"
	default:
		return "NO-VERDICT"
	}
}

// RunSpec describes one bounded invocation. The command is a field so tests can
// inject a fake (e.g. `sleep`) — the deadline logic is exercised without running
// the real suite.
type RunSpec struct {
	Name     string        // executable, e.g. "go"
	Args     []string      // arguments, e.g. ["test","-race","-timeout","120s","./..."]
	Dir      string        // working directory
	Deadline time.Duration // hard outer wall-clock; <=0 means no outer bound
	Stdout   io.Writer
	Stderr   io.Writer
}

// Result is the outcome of Run.
type Result struct {
	Outcome  Outcome
	ExitCode int
	Duration time.Duration
}

// Run executes spec under an outer deadline. On deadline it SIGKILLs the whole
// process GROUP (Setpgid is always set) and returns NoVerdict. It never blocks
// longer than Deadline plus a brief reap. ctx is an argument, never stored.
func Run(ctx context.Context, spec RunSpec) (Result, error) {
	runCtx := ctx
	if spec.Deadline > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.Deadline)
		defer cancel()
	}

	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group → killable as a unit

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{Outcome: Red, ExitCode: -1}, fmt.Errorf("qarun: start %s: %w", spec.Name, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		dur := time.Since(start)
		if err == nil {
			return Result{Outcome: Green, ExitCode: 0, Duration: dur}, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Result{Outcome: Red, ExitCode: ee.ExitCode(), Duration: dur}, nil
		}
		return Result{Outcome: Red, ExitCode: -1, Duration: dur}, err
	case <-runCtx.Done():
		// Deadline (or parent cancel): kill the whole process group and reap.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort group kill on deadline
		}
		<-done // wait for the killed child to be reaped
		return Result{Outcome: NoVerdict, ExitCode: -1, Duration: time.Since(start)}, nil
	}
}
