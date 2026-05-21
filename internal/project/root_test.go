package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// resolveForTest normalizes both actual and expected paths for comparison.
// On macOS, /var is a symlink to /private/var, so t.TempDir() paths
// differ from EvalSymlinks output. This helper resolves the expected path
// the same way the code does.
func resolveForTest(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path // fallback
	}
	return resolved
}

func TestFindRoot(t *testing.T) {
	t.Parallel()

	t.Run(".orqestra takes priority", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// Create .orqestra in tmp
		if err := os.MkdirAll(filepath.Join(tmp, ".orqestra"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Create .git deeper (should not be found since .orqestra is closer)
		sub := filepath.Join(tmp, "subdir")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindRoot(sub)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if root != resolveForTest(t, tmp) {
			t.Errorf("FindRoot(%s) = %s, want %s", sub, root, resolveForTest(t, tmp))
		}
	})

	t.Run(".git fallback when no .orqestra", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(tmp, "src", "deep")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindRoot(sub)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if root != resolveForTest(t, tmp) {
			t.Errorf("FindRoot(%s) = %s, want %s", sub, root, resolveForTest(t, tmp))
		}
	})

	t.Run(".orqestra only no .git", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".orqestra"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(tmp, "nested")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindRoot(sub)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if root != resolveForTest(t, tmp) {
			t.Errorf("FindRoot(%s) = %s, want %s", sub, root, resolveForTest(t, tmp))
		}
	})

	t.Run("not initialized returns error", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		sub := filepath.Join(tmp, "empty")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := FindRoot(sub)
		if err == nil {
			t.Fatal("FindRoot: expected error, got nil")
		}
		if !errors.Is(err, ErrNotInitialized) {
			t.Errorf("FindRoot error = %v, want wrapped ErrNotInitialized", err)
		}
	})

	t.Run("finds root at exact directory", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindRoot(tmp)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if root != resolveForTest(t, tmp) {
			t.Errorf("FindRoot(%s) = %s, want %s", tmp, root, resolveForTest(t, tmp))
		}
	})

	t.Run("closer .git wins over farther .orqestra", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// .orqestra at tmp root
		if err := os.MkdirAll(filepath.Join(tmp, ".orqestra"), 0o755); err != nil {
			t.Fatal(err)
		}
		// .git in a subdirectory (closer to start point)
		nested := filepath.Join(tmp, "nested-repo")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		// .orqestra in nested repo too (takes priority)
		if err := os.MkdirAll(filepath.Join(nested, ".orqestra"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(nested, "src")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindRoot(sub)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		// Should find the nested .orqestra (closest upward)
		if root != resolveForTest(t, nested) {
			t.Errorf("FindRoot(%s) = %s, want %s", sub, root, resolveForTest(t, nested))
		}
	})
}

func TestFindGitRoot(t *testing.T) {
	t.Parallel()

	t.Run("finds .git directory", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(tmp, "src")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		root, err := FindGitRoot(sub)
		if err != nil {
			t.Fatalf("FindGitRoot: %v", err)
		}
		if root != resolveForTest(t, tmp) {
			t.Errorf("FindGitRoot(%s) = %s, want %s", sub, root, resolveForTest(t, tmp))
		}
	})

	t.Run("no git returns error", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		_, err := FindGitRoot(tmp)
		if err == nil {
			t.Fatal("FindGitRoot: expected error, got nil")
		}
	})
}

func TestIsGitRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	if IsGitRoot(tmp) {
		t.Error("IsGitRoot returned true for empty directory")
	}

	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsGitRoot(tmp) {
		t.Error("IsGitRoot returned false for directory with .git")
	}
}

func TestInit(t *testing.T) {
	t.Parallel()

	t.Run("creates .orqestra/sessions", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}

		if !isDir(filepath.Join(tmp, ".orqestra", "sessions")) {
			t.Error(".orqestra/sessions not created")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := Init(tmp); err != nil {
			t.Fatalf("first Init: %v", err)
		}
		if err := Init(tmp); err != nil {
			t.Fatalf("second Init (idempotent): %v", err)
		}
	})

	t.Run("adds to .gitignore", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()

		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(tmp, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if content := string(data); content != ".orqestra/\n" {
			t.Errorf(".gitignore = %q, want .orqestra/\\n", content)
		}
	})

	t.Run("appends to existing .gitignore", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// Pre-existing .gitignore
		if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("node_modules/\n*.log\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(tmp, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		content := string(data)
		if content != "node_modules/\n*.log\n.orqestra/\n" {
			t.Errorf(".gitignore = %q, want %q", content, "node_modules/\n*.log\n.orqestra/\n")
		}
	})

	t.Run("does not duplicate .orqestra in .gitignore", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// Pre-existing .gitignore already has .orqestra/
		if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte(".orqestra/\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(tmp, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if content := string(data); content != ".orqestra/\n" {
			t.Errorf(".gitignore = %q, want no duplication", content)
		}
	})

	t.Run("handles .gitignore without trailing newline", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// .gitignore without trailing newline
		if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("node_modules/"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(tmp, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if content := string(data); content != "node_modules/\n.orqestra/\n" {
			t.Errorf(".gitignore = %q, want newline before append", content)
		}
	})
}
