package plan

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRevLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantErr bool
		check   func(t *testing.T, r Revision)
	}{
		{
			name: "well-formed",
			line: "abc123def\tabc123d\t2026-05-16T01:02:03Z\tAlice\tinitial plan",
			check: func(t *testing.T, r Revision) {
				if r.SHA != "abc123def" || r.ShortSHA != "abc123d" {
					t.Errorf("sha mismatch: %+v", r)
				}
				if r.Author != "Alice" || r.Subject != "initial plan" {
					t.Errorf("author/subject mismatch: %+v", r)
				}
				if r.Time.Year() != 2026 {
					t.Errorf("time year mismatch: %v", r.Time)
				}
			},
		},
		{
			name:    "missing field",
			line:    "abc\tdef\t2026-05-16T01:02:03Z\tAlice",
			wantErr: true,
		},
		{
			name:    "bad date",
			line:    "abc\tdef\tnot-a-date\tAlice\tmsg",
			wantErr: true,
		},
		{
			name: "empty subject",
			line: "abc\tdef\t2026-05-16T01:02:03Z\tAlice\t",
			check: func(t *testing.T, r Revision) {
				if r.Subject != "" {
					t.Errorf("expected empty subject, got %q", r.Subject)
				}
			},
		},
		{
			name: "extra tab in subject",
			line: "abc\tdef\t2026-05-16T01:02:03Z\tAlice\tsubject\twith\ttabs",
			check: func(t *testing.T, r Revision) {
				if r.Subject != "subject\twith\ttabs" {
					t.Errorf("expected subject to preserve tabs, got %q", r.Subject)
				}
			},
		},
		{
			name:    "empty sha",
			line:    "\tdef\t2026-05-16T01:02:03Z\tAlice\tmsg",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseRevLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil; rev=%+v", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

func TestOpenGitRepo_MissingDirErrors(t *testing.T) {
	_, err := OpenGitRepo(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestOpenGitRepo_EmptyDirErrors(t *testing.T) {
	if _, err := OpenGitRepo(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestRevisions_OrderingAndCount(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if err := repo.CommitPlan("v1", "user: first"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v2", "user: second"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v3", "user: third"); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatalf("OpenGitRepo: %v", err)
	}
	revs, err := ro.Revisions()
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("expected 3 revisions, got %d", len(revs))
	}
	// newest first
	if !strings.Contains(revs[0].Subject, "third") {
		t.Errorf("expected newest first, got %q", revs[0].Subject)
	}
	if !strings.Contains(revs[2].Subject, "first") {
		t.Errorf("expected oldest last, got %q", revs[2].Subject)
	}
	for _, r := range revs {
		if len(r.SHA) < 7 {
			t.Errorf("SHA looks short: %q", r.SHA)
		}
		if r.ShortSHA == "" {
			t.Errorf("ShortSHA empty for %q", r.SHA)
		}
	}
}

func TestContentAt_HistoricalContent(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if err := repo.CommitPlan("v1-content", "user: first"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v2-content", "user: second"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatal(err)
	}
	revs, err := ro.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	oldest := revs[len(revs)-1]
	got, err := ro.ContentAt(oldest.SHA)
	if err != nil {
		t.Fatalf("ContentAt: %v", err)
	}
	if got != "v1-content" {
		t.Errorf("expected v1-content, got %q", got)
	}
}

func TestContentAt_UnknownShaErrorsWithSha(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v1", "user: first"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ro.ContentAt("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("expected error for unknown sha")
	}
	if !strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("error should name the sha; got: %v", err)
	}
}

func TestDiffBetween_IdenticalShaEmpty(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v1", "user: first"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatal(err)
	}
	revs, err := ro.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ro.DiffBetween(revs[0].SHA, revs[0].SHA)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty diff for identical sha, got %q", out)
	}
}

func TestDiffBetween_ConsecutiveMatchesGitDiff(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("alpha\n", "user: first"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("beta\n", "user: second"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatal(err)
	}
	revs, err := ro.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revs, got %d", len(revs))
	}
	older, newer := revs[1].SHA, revs[0].SHA
	out, err := ro.DiffBetween(older, newer)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}
	if !strings.Contains(out, "-alpha") || !strings.Contains(out, "+beta") {
		t.Errorf("expected unified diff with -alpha/+beta; got:\n%s", out)
	}
}

func TestDiffBetween_EmptyShaErrors(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v1", "user: first"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenGitRepo(repo.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.DiffBetween("", "HEAD"); err == nil {
		t.Error("expected error for empty base")
	}
	if _, err := ro.DiffBetween("HEAD", ""); err == nil {
		t.Error("expected error for empty target")
	}
}
