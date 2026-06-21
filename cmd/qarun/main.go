//go:build unix

// Command qarun runs `make test`'s suite under a hard wall-clock deadline and
// emits a completion attestation. It exists so the build always yields a
// VERDICT (GREEN/RED/NO-VERDICT) and so "green" requires proof of completion:
//
//   - GREEN      → prints  QA-ATTEST commit=<sha> dur=<s>s SUITE-COMPLETE   exit 0
//   - RED        → prints  RED: test suite failed (exit=<n>)                exit 1
//   - NO-VERDICT → prints  NO-VERDICT: make test exceeded <deadline> ...    exit 124
//
// The QA-ATTEST line is the only valid evidence that the suite passed: it can be
// produced only by a run that actually completed. A hang yields NO-VERDICT, never
// a silent or assumed pass. Tunable via env QA_TEST_TIMEOUT (per-package) and
// QA_DEADLINE (outer wall-clock).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/qarun"
)

func main() {
	perPkg := envDuration("QA_TEST_TIMEOUT", 120*time.Second)
	outer := envDuration("QA_DEADLINE", 360*time.Second)

	args := []string{
		"test", "-race",
		"-timeout", perPkg.String(),
		"-coverprofile=coverage.out", "-covermode=atomic",
		"./...",
	}

	res, err := qarun.Run(context.Background(), qarun.RunSpec{
		Name:     "go",
		Args:     args,
		Dir:      ".",
		Deadline: outer,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	})
	if err != nil && res.Outcome != qarun.Red {
		fmt.Fprintln(os.Stderr, "qarun:", err)
	}

	switch res.Outcome {
	case qarun.Green:
		fmt.Printf("QA-ATTEST commit=%s dur=%.0fs SUITE-COMPLETE\n", gitHead(), res.Duration.Seconds())
		os.Exit(0)
	case qarun.Red:
		fmt.Fprintf(os.Stderr, "RED: test suite failed (exit=%d)\n", res.ExitCode)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "NO-VERDICT: make test exceeded %s — hang/timeout, treat as failure (not 'probably fine')\n", outer)
		os.Exit(124)
	}
}

// envDuration reads a Go duration from env, falling back to def.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// gitHead returns the short HEAD SHA, or "unknown" (best-effort — the
// attestation is still valid evidence of completion without it).
func gitHead() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
