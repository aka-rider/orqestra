// Command qaverify is a thin CLI over internal/qaspec. The same static checks
// run in `make test` via TestSpecIntegrity; this CLI adds --write (regenerate
// the §9 ledger) and a human-readable report for manual runs.
//
//	make qa-verify          run the static spec-integrity checks
//	make qa-verify-write    regenerate the §9 ledger in docs/qa/qa-spec.md
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/qaspec"
)

func main() {
	write := false
	for _, a := range os.Args[1:] {
		if a == "--write" {
			write = true
		}
	}

	root, err := qaspec.RepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qaverify:", err)
		os.Exit(2)
	}

	if write {
		reg, err := qaspec.Load(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "qaverify:", err)
			os.Exit(2)
		}
		if err := qaspec.WriteLedger(filepath.Join(root, qaspec.SpecRel), qaspec.LedgerBlock(reg)); err != nil {
			fmt.Fprintln(os.Stderr, "qaverify:", err)
			os.Exit(1)
		}
		fmt.Println("qaverify: wrote generated ledger to", qaspec.SpecRel)
		return
	}

	rep, err := qaspec.Static(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qaverify:", err)
		os.Exit(2)
	}
	for _, s := range rep.Soft {
		fmt.Println("  note:", s)
	}
	if len(rep.Hard) == 0 {
		fmt.Println("qaverify: OK")
		return
	}
	fmt.Fprintln(os.Stderr, "\nqaverify: FAILED")
	for _, s := range rep.Hard {
		fmt.Fprintln(os.Stderr, "  ✗", s)
	}
	os.Exit(1)
}
