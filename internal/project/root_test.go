package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckGitRoot(t *testing.T) {
	t.Parallel()

	t.Run("has .git", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := CheckGitRoot(dir); err != nil {
			t.Fatalf("CheckGitRoot: unexpected error: %v", err)
		}
	})

	t.Run("no .git", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		err := CheckGitRoot(dir)
		if err == nil {
			t.Fatal("CheckGitRoot: expected error, got nil")
		}
		if !errors.Is(err, ErrNoGitRepo) {
			t.Errorf("CheckGitRoot error = %v, want wrapped ErrNoGitRepo", err)
		}
	})

	t.Run(".git is a file not dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// git submodules use .git as a file
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ../foo"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := CheckGitRoot(dir)
		if err == nil {
			t.Fatal("CheckGitRoot: expected error for .git file, got nil")
		}
	})
}

func TestIsInitialized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{
			name: "has .orqestra",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, ".orqestra"), 0o755); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: true,
		},
		{
			name: "only .git",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: false,
		},
		{
			name: "empty dir",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			want: false,
		},
		{
			name: "non-existent path",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "no-such-dir")
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			got := IsInitialized(dir)
			if got != tt.want {
				t.Errorf("IsInitialized(%q) = %v, want %v", dir, got, tt.want)
			}
		})
	}
}

func TestInit(t *testing.T) {
	t.Parallel()

	t.Run("creates .orqestra/sessions", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := Init(tmp); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if !isDir(filepath.Join(tmp, ".orqestra", "sessions")) {
			t.Error(".orqestra/sessions not created")
		}
	})

	t.Run("idempotent fails", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		if err := Init(tmp); err != nil {
			t.Fatalf("first Init: %v", err)
		}
		if err := Init(tmp); err == nil {
			t.Fatal("second Init should fail, got nil")
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
		if content := string(data); content != "node_modules/\n*.log\n.orqestra/\n" {
			t.Errorf(".gitignore = %q, want %q", content, "node_modules/\n*.log\n.orqestra/\n")
		}
	})

	t.Run("does not duplicate .orqestra in .gitignore", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
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
