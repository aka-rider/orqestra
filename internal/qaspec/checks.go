package qaspec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	invIDPattern  = regexp.MustCompile(`INV-[A-Z0-9]+(?:-[A-Z0-9]+)*`)
	testFnPattern = regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\s*\(`)
	vTagPattern   = regexp.MustCompile(`\[V ([^\]]+)\]`)
	// lineAnchorPattern matches a forbidden line-number anchor like foo.go:123
	// in any evidence tier — line numbers rot; anchors must be symbols.
	lineAnchorPattern = regexp.MustCompile(`[A-Za-z0-9_.-]+\.(?:go|ya?ml|md):[0-9]`)

	// greenClaimRe / claimMetaRe back the forbidden-claim tripwire: a prose
	// assertion that the suite passed is rot unless it quotes a real QA-ATTEST.
	// claimMetaRe excludes meta/rule lines (which discuss the rule, not assert green).
	greenClaimRe = regexp.MustCompile(`(?i)make test.{0,30}(✅|\bgreen\b|\bpass(?:es|ing|ed)\b)`)
	claimMetaRe  = regexp.MustCompile(`(?i)never|without|no green|no-verdict|qa-attest|must not|forbidden|do ?n.t|do not|\bclaim|hallucinat|attest|cannot|unfakeable|only valid|proof|bound`)

	validStatuses = map[string]bool{"gap": true, "covered": true, "defect": true}
	skipDirs      = map[string]bool{".git": true, "vendor": true, "bin": true, "node_modules": true, ".claude": true, ".orqestra": true}
)

// Report accumulates hard failures (a red build) and soft notes (informational).
type Report struct {
	Hard []string
	Soft []string
}

func (r *Report) failf(format string, a ...any) { r.Hard = append(r.Hard, fmt.Sprintf(format, a...)) }
func (r *Report) notef(format string, a ...any) { r.Soft = append(r.Soft, fmt.Sprintf(format, a...)) }

// Static runs every spec-integrity check that does not execute a subprocess.
// It is the shared core of TestSpecIntegrity (so `make test` enforces it) and
// the cmd/qaverify CLI. Hard problems mean the spec or tests have rotted.
func Static(root string) (Report, error) {
	reg, err := Load(root)
	if err != nil {
		return Report{}, err
	}
	ids := map[string]bool{}
	for _, inv := range reg.Invariants {
		ids[inv.ID] = true
	}
	cited, testFns, err := scanTests(root)
	if err != nil {
		return Report{}, err
	}

	var rep Report
	checkAnchors(root, reg, &rep)
	checkTraceability(reg, ids, cited, testFns, &rep)
	checkLedger(root, reg, &rep)
	checkProseAnchors(root, &rep)
	checkTestHygiene(root, &rep)
	checkHarnessVerdict(root, &rep)
	checkForbiddenClaims(root, &rep)
	checkRealWiring(root, reg, cited, &rep)
	checkRoleCoverage(reg, &rep)
	return rep, nil
}

// scanTests returns the INV IDs cited by *_test.go files (id -> citing files)
// and the set of test function names that exist. It reads raw text, so it sees
// build-tag-gated tests too.
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

// checkAnchors verifies every invariant anchor symbol resolves in its file.
func checkAnchors(root string, reg Registry, rep *Report) {
	cache := map[string]string{}
	for _, inv := range reg.Invariants {
		for _, a := range inv.Anchors {
			body, ok := cache[a.File]
			if !ok {
				data, err := os.ReadFile(filepath.Join(root, a.File))
				if err != nil {
					rep.failf("%s: anchor file %s unreadable: %v", inv.ID, a.File, err)
					cache[a.File] = ""
					continue
				}
				body = string(data)
				cache[a.File] = body
			}
			if body == "" || !strings.Contains(body, a.Symbol) {
				rep.failf("%s: anchor symbol %q not found in %s (renamed or removed?)", inv.ID, a.Symbol, a.File)
			}
		}
	}
}

// checkTraceability enforces status<->reality and rejects citations of unknown IDs.
func checkTraceability(reg Registry, ids map[string]bool, cited map[string][]string, testFns map[string]bool, rep *Report) {
	for id, files := range cited {
		if !ids[id] {
			rep.failf("test(s) %v cite unknown invariant %s (typo, or invariant deleted from registry)", uniq(files), id)
		}
	}
	canaryFor := map[string]int{}
	for _, c := range reg.Canaries {
		canaryFor[c.Invariant]++
	}
	for _, inv := range reg.Invariants {
		if !validStatuses[inv.Status] {
			rep.failf("%s: unknown status %q (want gap|covered|defect)", inv.ID, inv.Status)
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
				rep.failf("%s: status=covered but none of its tests %v exist", inv.ID, inv.Tests)
			}
			if len(cited[inv.ID]) == 0 {
				rep.failf("%s: status=covered but no test cites it (add a `// %s` comment to the gate test)", inv.ID, inv.ID)
			}
		case "defect":
			if canaryFor[inv.ID] == 0 {
				rep.failf("%s: status=defect but no canary references it", inv.ID)
			}
		case "gap":
			if len(cited[inv.ID]) > 0 {
				rep.notef("%s: status=gap but cited by %v — promote to covered?", inv.ID, uniq(cited[inv.ID]))
			}
		}
	}
	// A canary must reference a known invariant and name an existing test.
	for _, c := range reg.Canaries {
		if !ids[c.Invariant] {
			rep.failf("canary %s references unknown invariant %s", c.ID, c.Invariant)
		}
		if c.CanaryTest == "" {
			rep.notef("canary %s has no canary_test yet — add one to make %s self-verifying", c.ID, c.Invariant)
			continue
		}
		if !testFns[c.CanaryTest] {
			rep.failf("canary %s names canary_test %s which does not exist", c.ID, c.CanaryTest)
		}
	}
}

// checkLedger verifies the committed §9 ledger equals the generated one.
func checkLedger(root string, reg Registry, rep *Report) {
	specPath := filepath.Join(root, SpecRel)
	data, err := os.ReadFile(specPath)
	if err != nil {
		rep.failf("ledger: read spec: %v", err)
		return
	}
	start := strings.Index(string(data), LedgerBegin)
	endIdx := strings.Index(string(data), LedgerEnd)
	if start < 0 || endIdx < 0 {
		rep.failf("ledger: markers not found in %s (run `make qa-verify-write`)", SpecRel)
		return
	}
	current := string(data[start : endIdx+len(LedgerEnd)])
	if current != LedgerBlock(reg) {
		rep.failf("ledger: §9 is stale — run `make qa-verify-write` to regenerate it")
	}
}

// checkProseAnchors makes the spec's own evidence tags falsifiable: no
// line-number anchors anywhere (they rot), and every `[V <path>#Symbol]` must
// resolve. Non-path tag bodies (the §0 legend, `[T ...]`, `[2]`) are ignored.
func checkProseAnchors(root string, rep *Report) {
	specPath := filepath.Join(root, SpecRel)
	data, err := os.ReadFile(specPath)
	if err != nil {
		rep.failf("prose-anchor: read spec: %v", err)
		return
	}
	text := string(data)

	for _, m := range lineAnchorPattern.FindAllString(text, -1) {
		rep.failf("prose-anchor: line-number anchor %q in %s — use [V <path>#Symbol] (line numbers rot)", m, SpecRel)
	}

	cache := map[string]string{}
	for _, m := range vTagPattern.FindAllStringSubmatch(text, -1) {
		body := strings.TrimSpace(m[1])
		path, symbol, _ := strings.Cut(body, "#")
		if !isPathLike(path) {
			continue // legend / placeholder, not a real anchor
		}
		content, ok := cache[path]
		if !ok {
			b, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				rep.failf("prose-anchor: %s references missing file %q", SpecRel, path)
				cache[path] = ""
				continue
			}
			content = string(b)
			cache[path] = content
		}
		if content == "" {
			continue // already reported missing
		}
		if symbol != "" && !strings.Contains(content, symbol) {
			rep.failf("prose-anchor: symbol %q not found in %s", symbol, path)
		}
	}
}

// checkTestHygiene enforces the mechanically-decidable subset of the §6
// test-fiction ban list. Semantic fiction (tautology, mock-everything) is not
// decidable here and stays the critic's job.
func checkTestHygiene(root string, rep *Report) {
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
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(string(data), "time.Sleep(") {
			rep.failf("test-hygiene: %s uses time.Sleep — banned as test sync (use channels/contexts/fake clocks)", rel)
		}
		return nil
	})
	if err != nil {
		rep.failf("test-hygiene: walk: %v", err)
	}
}

// checkHarnessVerdict protects INV-HARNESS-VERDICT: `make test` must route
// through cmd/qarun (so a hang is a bounded NO-VERDICT, not an indefinite hang),
// and qarun must still emit the verdict/attestation tokens. Silently gutting the
// bound or the attestation turns this RED.
func checkHarnessVerdict(root string, rep *Report) {
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		rep.failf("harness-verdict: read Makefile: %v", err)
	} else if !testTargetRoutesThrough(string(mk), "qarun") {
		rep.failf("harness-verdict: the `make test` recipe no longer routes through cmd/qarun — a hang would be unbounded (NO-VERDICT silently defeats every gate)")
	}
	qr, err := os.ReadFile(filepath.Join(root, "cmd", "qarun", "main.go"))
	if err != nil {
		rep.failf("harness-verdict: read cmd/qarun/main.go: %v", err)
		return
	}
	for _, tok := range []string{"QA-ATTEST", "NO-VERDICT"} {
		if !strings.Contains(string(qr), tok) {
			rep.failf("harness-verdict: cmd/qarun no longer emits %q — the verdict/attestation mechanism is broken", tok)
		}
	}
}

// checkForbiddenClaims is the LLM tripwire's machine half: prose must not assert
// the suite passed (a "green" claim) — green is proven only by a fresh QA-ATTEST
// from a completed run, never by a doc claim. Code fences and meta/rule lines
// (which discuss the rule) are exempt via claimMetaRe.
func checkForbiddenClaims(root string, rep *Report) {
	for _, rel := range []string{SpecRel, "README.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue // optional docs
		}
		inFence := false
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			if greenClaimRe.MatchString(line) && !claimMetaRe.MatchString(line) {
				rep.failf("forbidden-claim: %s:%d asserts test success in prose (%q) — green is proven only by a fresh QA-ATTEST, not a doc claim", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// canonicalRoles is the pipeline's agent set. checkRoleCoverage requires each
// to own ≥1 covered, real_wiring invariant — so every agent's real capability
// is gated by a non-fake test, not assumed.
var canonicalRoles = []string{"researcher", "architect", "critic", "worker", "validator", "integrator"}

// realWiringMarkers are substrings that prove a test exercises real production
// machinery: the seatbelt sandbox, real git/worktree, a real harness.Run
// subprocess driven through the replay stub, or a real parser on real input.
var realWiringMarkers = []string{
	"sandbox.New", "sandbox.Wrap", "NewToolProfile",
	"worktree.Create", "worktree.Worktree", "worktree.CurrentBranch",
	"worktree.MergeBaseIntoWorktree", "worktree.ResolutionClean",
	"harness.Run", "replayclaude", "ORQESTRA_REPLAY_FILE",
	"ReadPlanFile", "ReadPlan(", "ParseValidationOutput",
}

// fakeWiringMarkers are the banned fake/no-op seams. A file containing any of
// these is testing the router with stand-ins, NOT the real pipeline — it cannot
// satisfy a real_wiring invariant.
var fakeWiringMarkers = []string{"fakeStep", "noopStepContext", "Exec: nil", "Exec:nil"}

// checkRealWiring protects real_wiring invariants: a covered, real_wiring
// invariant must be cited by at least one test file that touches a real seam AND
// is free of the fake/no-op markers. This makes "covered by a fake" a red build
// — it is what catches the INV-O1-FLOW-style lie where pipeline coverage is
// claimed against engine_test.go's fakeStep/noopStepContext stand-ins.
func checkRealWiring(root string, reg Registry, cited map[string][]string, rep *Report) {
	cache := map[string]string{}
	read := func(rel string) string {
		if body, ok := cache[rel]; ok {
			return body
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			cache[rel] = ""
			return ""
		}
		cache[rel] = string(data)
		return string(data)
	}
	for _, inv := range reg.Invariants {
		if !inv.RealWiring || inv.Status != "covered" {
			continue
		}
		files := uniq(cited[inv.ID])
		if len(files) == 0 {
			rep.failf("%s: real_wiring+covered but no test cites it (add a `// %s` comment to the real-wiring gate)", inv.ID, inv.ID)
			continue
		}
		realFound := false
		for _, rel := range files {
			body := read(rel)
			if body == "" || containsAny(body, fakeWiringMarkers) {
				continue // fake-tainted or unreadable — cannot prove real wiring
			}
			if containsAny(body, realWiringMarkers) {
				realFound = true
				break
			}
		}
		if !realFound {
			rep.failf("%s: real_wiring+covered but no citing test exercises a real seam free of fakes "+
				"(every cite is fake-tainted or lacks a sandbox/git/worktree/harness.Run/parser marker) — "+
				"a fake cannot satisfy a wiring invariant", inv.ID)
		}
	}
}

// checkRoleCoverage enforces that every pipeline agent owns a validated, non-fake
// capability gate: each canonical role must have ≥1 invariant that is both
// status=covered and real_wiring. A role with no such invariant is an unguarded
// agent — RED, naming the role.
func checkRoleCoverage(reg Registry, rep *Report) {
	covered := map[string]bool{}
	for _, inv := range reg.Invariants {
		if inv.Role == "" {
			continue
		}
		if inv.Status == "covered" && inv.RealWiring {
			covered[inv.Role] = true
		}
	}
	for _, role := range canonicalRoles {
		if !covered[role] {
			rep.failf("role %q has no covered+real_wiring invariant — every agent must have a validated, non-fake capability gate (add an INV-ROLE-* entry)", role)
		}
	}
}

// containsAny reports whether s contains any of the needles.
func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// testTargetRoutesThrough reports whether the `test:` target's recipe (the
// TAB-indented lines, not comments elsewhere) contains needle.
func testTargetRoutesThrough(makefile, needle string) bool {
	inRecipe := false
	for _, ln := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(ln, "test:") {
			inRecipe = true
			continue
		}
		if inRecipe {
			if strings.HasPrefix(ln, "\t") {
				if strings.Contains(ln, needle) {
					return true
				}
				continue
			}
			inRecipe = false // a non-recipe line ends the target
		}
	}
	return false
}

func isPathLike(p string) bool {
	if strings.Contains(p, "/") {
		return true
	}
	return strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".yaml") ||
		strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".md")
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
