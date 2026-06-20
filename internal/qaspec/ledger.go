package qaspec

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Markers delimiting the generated §9 ledger inside qa-spec.md.
const (
	LedgerBegin = "<!-- BEGIN GENERATED LEDGER (regenerate with: make qa-verify-write) -->"
	LedgerEnd   = "<!-- END GENERATED LEDGER -->"
)

// LedgerBlock renders the generated §9 ledger (sorted by invariant ID),
// including the BEGIN/END markers. It is the single source the ledger-drift
// check compares against and that --write installs.
func LedgerBlock(reg Registry) string {
	invs := append([]Invariant(nil), reg.Invariants...)
	sort.Slice(invs, func(i, j int) bool { return invs[i].ID < invs[j].ID })
	var b strings.Builder
	b.WriteString(LedgerBegin)
	b.WriteString("\n\n| Invariant | Pillar | Layer | Status |\n")
	b.WriteString("|-----------|--------|-------|--------|\n")
	for _, inv := range invs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", inv.ID, inv.Pillar, inv.Layer, inv.Status)
	}
	b.WriteString("\n")
	b.WriteString(LedgerEnd)
	return b.String()
}

// WriteLedger replaces the marked ledger region in specPath with block.
func WriteLedger(specPath, block string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	start := strings.Index(string(data), LedgerBegin)
	endIdx := strings.Index(string(data), LedgerEnd)
	if start < 0 || endIdx < 0 {
		return fmt.Errorf("ledger markers not found in %s", SpecRel)
	}
	end := endIdx + len(LedgerEnd)
	out := string(data[:start]) + block + string(data[end:])
	return os.WriteFile(specPath, []byte(out), 0o644)
}
