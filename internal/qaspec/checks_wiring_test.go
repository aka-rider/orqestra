package qaspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roleID builds a role invariant id from fragments so this test file contains no
// scannable invariant-id literal — the repo-wide citation scanner would otherwise
// read our synthetic test data as real citations of real invariants.
func roleID(role string) string { return "INV-" + "ROLE-" + strings.ToUpper(role) }

// fullRoleRegistry returns a registry where every canonical role owns a
// covered+real_wiring invariant — the green baseline for checkRoleCoverage.
func fullRoleRegistry() Registry {
	var invs []Invariant
	for _, role := range canonicalRoles {
		invs = append(invs, Invariant{
			ID:         roleID(role),
			Status:     "covered",
			Role:       role,
			RealWiring: true,
		})
	}
	return Registry{Invariants: invs}
}

func hardContains(rep Report, substr string) bool {
	for _, h := range rep.Hard {
		if strings.Contains(h, substr) {
			return true
		}
	}
	return false
}

// TestCheckRoleCoverage_MissingRole proves the gate: drop the worker's
// invariant and the build must go RED naming the uncovered role.
func TestCheckRoleCoverage_MissingRole(t *testing.T) {
	reg := fullRoleRegistry()
	var kept []Invariant
	for _, inv := range reg.Invariants {
		if inv.Role != "worker" {
			kept = append(kept, inv)
		}
	}
	reg.Invariants = kept

	var rep Report
	checkRoleCoverage(reg, &rep)
	if !hardContains(rep, `role "worker"`) {
		t.Fatalf("expected a hard failure naming the uncovered worker role, got: %v", rep.Hard)
	}
}

// TestCheckRoleCoverage_FakeDoesNotCount proves a role covered by a non-real
// (real_wiring=false) invariant is still treated as uncovered.
func TestCheckRoleCoverage_FakeDoesNotCount(t *testing.T) {
	reg := fullRoleRegistry()
	for i := range reg.Invariants {
		if reg.Invariants[i].Role == "critic" {
			reg.Invariants[i].RealWiring = false // covered, but by a fake
		}
	}
	var rep Report
	checkRoleCoverage(reg, &rep)
	if !hardContains(rep, `role "critic"`) {
		t.Fatalf("expected critic to be flagged uncovered when only fake-covered, got: %v", rep.Hard)
	}
}

func TestCheckRoleCoverage_AllCovered(t *testing.T) {
	var rep Report
	checkRoleCoverage(fullRoleRegistry(), &rep)
	if len(rep.Hard) != 0 {
		t.Fatalf("expected no failures when every role is covered, got: %v", rep.Hard)
	}
}

// writeTestFile drops a _test.go file under root and returns its repo-relative path.
func writeTestFile(t *testing.T, root, name, body string) string {
	t.Helper()
	abs := filepath.Join(root, name)
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	return rel
}

// realWiringCase runs checkRealWiring for a single worker invariant cited by one
// test file with the given body, and returns the report.
func realWiringCase(t *testing.T, body string) Report {
	t.Helper()
	root := t.TempDir()
	rel := writeTestFile(t, root, "sample_test.go", body)
	reg := Registry{Invariants: []Invariant{{
		ID: roleID("worker"), Status: "covered", Role: "worker", RealWiring: true,
	}}}
	cited := map[string][]string{roleID("worker"): {rel}}
	var rep Report
	checkRealWiring(root, reg, cited, &rep)
	return rep
}

// TestCheckRealWiring_FakeTaintedFails proves a real_wiring invariant cited only
// by a fake-tainted test (fakeStep/noopStepContext) is rejected — the
// INV-O1-FLOW-style lie.
func TestCheckRealWiring_FakeTaintedFails(t *testing.T) {
	rep := realWiringCase(t,
		"package x\nfunc TestX(t *testing.T){ _ = fakeStep{}; _ = noopStepContext() }\n")
	if !hardContains(rep, "ROLE-WORKER") {
		t.Fatalf("expected fake-tainted citation to fail real_wiring, got: %v", rep.Hard)
	}
}

// TestCheckRealWiring_RealPasses proves a citation touching a real seam and free
// of fakes satisfies the gate.
func TestCheckRealWiring_RealPasses(t *testing.T) {
	rep := realWiringCase(t,
		"package x\nfunc TestX(t *testing.T){ _ = sandbox.New(cfg); _ = harness.Run(ctx) }\n")
	if len(rep.Hard) != 0 {
		t.Fatalf("expected real-seam citation to pass, got: %v", rep.Hard)
	}
}

// TestCheckRealWiring_NoRealSeamFails proves a citation that is fake-free but
// touches no real seam (a pure stub) still fails — real_wiring demands evidence
// of real machinery, not merely the absence of known fakes.
func TestCheckRealWiring_NoRealSeamFails(t *testing.T) {
	rep := realWiringCase(t, "package x\nfunc TestX(t *testing.T){ _ = 1 + 1 }\n")
	if !hardContains(rep, "ROLE-WORKER") {
		t.Fatalf("expected a no-real-seam citation to fail, got: %v", rep.Hard)
	}
}
