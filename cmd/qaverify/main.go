// Command qaverify is the self-defense for the QA specification.
//
// It treats docs/qa/invariants.yaml as the source of truth and makes the spec
// unable to lie: stale anchors, untraceable status claims, ledger drift, and
// dead canaries all become a non-zero exit (a red build) rather than silent rot.
// The model that edits the spec cannot cheat — a false claim fails one of the
// four checks below.
//
//	make qa-verify          run all four checks
//	make qa-verify-write    regenerate the §9 ledger in qa-spec.md
//
// The four checks:
//  1. anchor-resolve   — every invariant anchor symbol exists in its file.
//  2. traceability     — status<->reality: covered needs a citing gate test,
//     defect needs a linked canary, no test cites an unknown invariant.
//  3. ledger-drift     — the generated §9 table equals what is committed.
//  4. canary           — every implemented canary_test currently PASSES, i.e.
//     the documented defect still reproduces.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	registryRel = "docs/qa/invariants.yaml"
	specRel     = "docs/qa/qa-spec.md"
	canaryTag   = "qacanary"
	ledgerBegin = "<!-- BEGIN GENERATED LEDGER (regenerate with: make qa-verify-write) -->"
	ledgerEnd   = "<!-- END GENERATED LEDGER -->"
)

var (
	invIDPattern   = regexp.MustCompile(`INV-[A-Z0-9]+(?:-[A-Z0-9]+)*`)
	testFnPattern  = regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\s*\(`)
	validStatuses  = map[string]bool{"gap": true, "covered": true, "defect": true}
	skipDirs       = map[string]bool{".git": true, "vendor": true, "bin": true, "node_modules": true}
)

type anchor struct {
	File   string `yaml:"file"`
	Symbol string `yaml:"symbol"`
}

type invariant struct {
	ID      string   `yaml:"id"`
	Pillar  string   `yaml:"pillar"`
	Layer   string   `yaml:"layer"`
	Status  string   `yaml:"status"`
	Anchors []anchor `yaml:"anchors"`
	Tests   []string `yaml:"tests"`
}

type canary struct {
	ID         string `yaml:"id"`
	Invariant  string `yaml:"invariant"`
	Summary    string `yaml:"summary"`
	State      string `yaml:"state"`
	CanaryTest string `yaml:"canary_test"`
	Package    string `yaml:"package"`
	Tags       string `yaml:"tags"`     // build tags for the canary test; default "qacanary"
	Platform   string `yaml:"platform"` // if set, only run when runtime.GOOS matches
	GateTest   string `yaml:"gate_test"`
}

type registry struct {
	Invariants []invariant `yaml:"invariants"`
	Canaries   []canary    `yaml:"canaries"`
}

// report accumulates hard failures (red build) and soft notes (informational).
type report struct {
	hard []string
	soft []string
}

func (r *report) fail(format string, a ...any) { r.hard = append(r.hard, fmt.Sprintf(format, a...)) }
func (r *report) note(format string, a ...any) { r.soft = append(r.soft, fmt.Sprintf(format, a...)) }

func main() {
	write := false
	for _, a := range os.Args[1:] {
		if a == "--write" {
			write = true
		}
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qaverify:", err)
		os.Exit(2)
	}

	reg, err := loadRegistry(filepath.Join(root, registryRel))
	if err != nil {
		fmt.Fprintln(os.Stderr, "qaverify:", err)
		os.Exit(2)
	}

	ids := map[string]bool{}
	for _, inv := range reg.Invariants {
		ids[inv.ID] = true
	}

	cited, testFns, err := scanTests(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qaverify:", err)
		os.Exit(2)
	}

	var rep report
	checkAnchors(root, reg, &rep)
	checkTraceability(reg, ids, cited, testFns, &rep)
	runCanaries(root, reg, ids, testFns, &rep)

	block := ledgerBlock(reg)
	specPath := filepath.Join(root, specRel)
	if write {
		if err := writeLedger(specPath, block); err != nil {
			rep.fail("ledger: %v", err)
		} else {
			fmt.Println("qaverify: wrote generated ledger to", specRel)
		}
	} else {
		checkLedger(specPath, block, &rep)
	}

	for _, s := range rep.soft {
		fmt.Println("  note:", s)
	}
	if len(rep.hard) == 0 {
		fmt.Printf("qaverify: OK — %d invariants, %d canaries, %d notes\n",
			len(reg.Invariants), len(reg.Canaries), len(rep.soft))
		return
	}
	fmt.Fprintln(os.Stderr, "\nqaverify: FAILED")
	for _, s := range rep.hard {
		fmt.Fprintln(os.Stderr, "  ✗", s)
	}
	os.Exit(1)
}

// repoRoot walks up from the cwd until it finds go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func loadRegistry(path string) (registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return registry{}, fmt.Errorf("read registry: %w", err)
	}
	var reg registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return registry{}, fmt.Errorf("parse registry %s: %w", path, err)
	}
	return reg, nil
}

// scanTests returns the set of INV IDs cited by *_test.go files (id -> citing
// files) and the set of test function names that exist.
func scanTests(root string) (map[string][]string, map[string]bool, error) {
	cited := map[string][]string{}
	testFns := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		text := string(data)
		rel, _ := filepath.Rel(root, path)
		for _, id := range invIDPattern.FindAllString(text, -1) {
			cited[id] = append(cited[id], rel)
		}
		for _, m := range testFnPattern.FindAllStringSubmatch(text, -1) {
			testFns[m[1]] = true
		}
		return nil
	})
	return cited, testFns, err
}

// checkAnchors verifies every anchor symbol resolves in its file.
func checkAnchors(root string, reg registry, rep *report) {
	cache := map[string]string{}
	for _, inv := range reg.Invariants {
		for _, a := range inv.Anchors {
			body, ok := cache[a.File]
			if !ok {
				data, err := os.ReadFile(filepath.Join(root, a.File))
				if err != nil {
					rep.fail("%s: anchor file %s unreadable: %v", inv.ID, a.File, err)
					cache[a.File] = ""
					continue
				}
				body = string(data)
				cache[a.File] = body
			}
			if body == "" || !strings.Contains(body, a.Symbol) {
				rep.fail("%s: anchor symbol %q not found in %s (renamed or removed?)", inv.ID, a.Symbol, a.File)
			}
		}
	}
}

// checkTraceability enforces status<->reality and rejects citations of unknown IDs.
func checkTraceability(reg registry, ids map[string]bool, cited map[string][]string, testFns map[string]bool, rep *report) {
	for id, files := range cited {
		if !ids[id] {
			rep.fail("test(s) %v cite unknown invariant %s (typo, or invariant deleted from registry)", uniq(files), id)
		}
	}
	canaryFor := map[string]int{}
	for _, c := range reg.Canaries {
		canaryFor[c.Invariant]++
	}
	for _, inv := range reg.Invariants {
		if !validStatuses[inv.Status] {
			rep.fail("%s: unknown status %q (want gap|covered|defect)", inv.ID, inv.Status)
			continue
		}
		switch inv.Status {
		case "covered":
			has := false
			for _, tn := range inv.Tests {
				if testFns[tn] {
					has = true
					break
				}
			}
			if !has {
				rep.fail("%s: status=covered but none of its tests %v exist", inv.ID, inv.Tests)
			}
			if len(cited[inv.ID]) == 0 {
				rep.fail("%s: status=covered but no test cites it (add a `// %s` comment to the gate test)", inv.ID, inv.ID)
			}
		case "defect":
			if canaryFor[inv.ID] == 0 {
				rep.fail("%s: status=defect but no canary references it", inv.ID)
			}
		case "gap":
			if len(cited[inv.ID]) > 0 {
				rep.note("%s: status=gap but cited by %v — promote to covered?", inv.ID, uniq(cited[inv.ID]))
			}
		}
	}
}

// runCanaries executes each implemented canary_test and requires it to pass
// (the defect still reproduces). A failing canary means the bug was likely
// fixed: retire the canary and add the real gate.
func runCanaries(root string, reg registry, ids map[string]bool, testFns map[string]bool, rep *report) {
	for _, c := range reg.Canaries {
		if !ids[c.Invariant] {
			rep.fail("canary %s references unknown invariant %s", c.ID, c.Invariant)
		}
		if c.CanaryTest == "" {
			rep.note("canary %s has no canary_test yet — implement one to make %s self-verifying", c.ID, c.Invariant)
			continue
		}
		if !testFns[c.CanaryTest] {
			rep.fail("canary %s names canary_test %s which does not exist", c.ID, c.CanaryTest)
			continue
		}
		if c.Platform != "" && c.Platform != runtime.GOOS {
			rep.note("canary %s is %s-only — run on that platform's tier to verify %s", c.ID, c.Platform, c.Invariant)
			continue
		}
		tags := c.Tags
		if tags == "" {
			tags = canaryTag
		}
		pkg := c.Package
		if pkg == "" {
			pkg = "./..."
		}
		cmd := exec.Command("go", "test", "-tags", tags, "-run", "^"+c.CanaryTest+"$", pkg)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			rep.fail("canary %s: %s did not pass — the defect may be fixed. Retire the canary, add the gate, flip %s to covered.\n--- go test output ---\n%s",
				c.ID, c.CanaryTest, c.Invariant, strings.TrimSpace(string(out)))
			continue
		}
		rep.note("canary %s reproduces (defect live, as documented)", c.ID)
	}
}

// ledgerBlock renders the generated §9 ledger, sorted by invariant ID.
func ledgerBlock(reg registry) string {
	invs := append([]invariant(nil), reg.Invariants...)
	sort.Slice(invs, func(i, j int) bool { return invs[i].ID < invs[j].ID })
	var b strings.Builder
	b.WriteString(ledgerBegin)
	b.WriteString("\n\n| Invariant | Pillar | Layer | Status |\n")
	b.WriteString("|-----------|--------|-------|--------|\n")
	for _, inv := range invs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", inv.ID, inv.Pillar, inv.Layer, inv.Status)
	}
	b.WriteString("\n")
	b.WriteString(ledgerEnd)
	return b.String()
}

func writeLedger(specPath, block string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	start := strings.Index(string(data), ledgerBegin)
	endIdx := strings.Index(string(data), ledgerEnd)
	if start < 0 || endIdx < 0 {
		return fmt.Errorf("ledger markers not found in %s — add the BEGIN/END markers to §9", specRel)
	}
	end := endIdx + len(ledgerEnd)
	out := string(data[:start]) + block + string(data[end:])
	return os.WriteFile(specPath, []byte(out), 0o644)
}

func checkLedger(specPath, block string, rep *report) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		rep.fail("ledger: read spec: %v", err)
		return
	}
	start := strings.Index(string(data), ledgerBegin)
	endIdx := strings.Index(string(data), ledgerEnd)
	if start < 0 || endIdx < 0 {
		rep.fail("ledger: markers not found in %s (run `make qa-verify-write`)", specRel)
		return
	}
	current := string(data[start : endIdx+len(ledgerEnd)])
	if current != block {
		rep.fail("ledger: §9 is stale — run `make qa-verify-write` to regenerate it")
	}
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
