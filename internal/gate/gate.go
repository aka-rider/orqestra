package gate

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/xiii/orqestra/internal/types"
)

// HumanGate displays the plan and waits for user confirmation.
type HumanGate struct {
	in  io.Reader
	out io.Writer
}

func New(in io.Reader, out io.Writer) *HumanGate {
	return &HumanGate{in: in, out: out}
}

// Confirm displays the specification and asks the user to approve or reject.
// Returns true if approved, false if rejected.
func (g *HumanGate) Confirm(spec types.Specification) (bool, error) {
	fmt.Fprintf(g.out, "\n╔══════════════════════════════════════════════╗\n")
	fmt.Fprintf(g.out, "║             EXECUTION PLAN                   ║\n")
	fmt.Fprintf(g.out, "╚══════════════════════════════════════════════╝\n\n")

	fmt.Fprintf(g.out, "Goal: %s\n\n", spec.Goal)

	fmt.Fprintf(g.out, "Steps:\n")
	for i, step := range spec.Steps {
		fmt.Fprintf(g.out, "  %d. %s\n", i+1, step)
	}

	fmt.Fprintf(g.out, "\nAcceptance Criteria:\n")
	for i, criterion := range spec.Acceptance {
		fmt.Fprintf(g.out, "  ✓ %d. %s\n", i+1, criterion)
	}

	fmt.Fprintf(g.out, "\n─────────────────────────────────────────────────\n")
	fmt.Fprintf(g.out, "Approve this plan? [y/N]: ")

	scanner := bufio.NewScanner(g.in)
	if !scanner.Scan() {
		return false, fmt.Errorf("reading input: %w", scanner.Err())
	}

	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}
