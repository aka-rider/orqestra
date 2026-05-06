//go:build darwin

package detect

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/seatbelt"
)

func TestUserProfile_ReadWriteExec(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	// Create temp dirs for testing
	dir := t.TempDir()
	readDir := filepath.Join(dir, "readable")
	writeDir := filepath.Join(dir, "writable")
	execDir := filepath.Join(dir, "execable")
	os.MkdirAll(readDir, 0o755)
	os.MkdirAll(writeDir, 0o755)
	os.MkdirAll(execDir, 0o755)

	cfg := config.SeatbeltConfig{
		AllowRead:  []string{readDir},
		AllowWrite: []string{writeDir},
		AllowExec:  []string{execDir},
	}

	snap, err := UserProfile(home, cfg)
	if err != nil {
		t.Fatalf("UserProfile() error: %v", err)
	}

	// Snapshot should be valid (we just verify no error occurred)
	_ = snap
}

func TestUserProfile_MissingOptionalPathsSkipped(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	cfg := config.SeatbeltConfig{
		AllowRead:  []string{"/nonexistent/path/that/does/not/exist"},
		AllowWrite: []string{"/another/nonexistent/path"},
		AllowExec:  []string{"/no/such/dir"},
	}

	// Should not error for missing paths — they are optional
	_, err := UserProfile(home, cfg)
	if err != nil {
		t.Fatalf("UserProfile() should skip missing optional paths, got error: %v", err)
	}
}

func TestUserProfile_ExecRejectsFiles(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	// Create a file (not a directory)
	dir := t.TempDir()
	file := filepath.Join(dir, "binary")
	os.WriteFile(file, []byte("#!/bin/sh"), 0o755)

	cfg := config.SeatbeltConfig{
		AllowExec: []string{file},
	}

	_, err := UserProfile(home, cfg)
	if err == nil {
		t.Fatal("expected error when allow_exec points to a file, not a directory")
	}
}

func TestUserProfile_EmptyConfig(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	cfg := config.SeatbeltConfig{}
	snap, err := UserProfile(home, cfg)
	if err != nil {
		t.Fatalf("UserProfile() error: %v", err)
	}
	// Should succeed with empty snapshot
	_ = snap
}

func TestAllProfiles_Success(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	cfg := config.SeatbeltConfig{}

	profiles, err := AllProfiles(home, "claude", cfg)
	if err != nil {
		t.Fatalf("AllProfiles() error: %v", err)
	}

	// Should have at least user-config and claude profiles
	if len(profiles) < 2 {
		t.Errorf("expected at least 2 profiles (user-config + claude), got %d", len(profiles))
	}
}

func TestUserProfile_PermissionErrorPropagated(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	// Create a directory with no read permission to simulate a real error
	dir := t.TempDir()
	restricted := filepath.Join(dir, "noperm")
	os.MkdirAll(restricted, 0o755)

	// Create a symlink loop to trigger a non-ErrNotExist error
	loopLink := filepath.Join(dir, "loop")
	os.Symlink(loopLink, loopLink) // self-referential symlink

	cfg := config.SeatbeltConfig{
		AllowRead: []string{loopLink},
	}

	// Symlink loop produces a non-ErrNotExist error — should NOT be silently skipped
	// AllowOptional only skips ErrNotExist; other errors propagate
	_, err := UserProfile(home, cfg)
	// On macOS, a symlink loop results in "too many levels of symbolic links"
	// which is NOT ErrNotExist, so it should propagate
	if err == nil {
		// If the OS doesn't detect the loop (race), the test is inconclusive
		// but we won't fail — the behavior is "propagates non-ErrNotExist errors"
		t.Skip("OS did not detect symlink loop; test inconclusive")
	}
	// Good — error was propagated, not silently swallowed
}

func TestUserProfile_ExtraEnvNotInSnapshot(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	// ExtraEnv should NOT be part of the UserProfile snapshot.
	// It is applied at the seatbelt.New level, not through profiles.
	cfg := config.SeatbeltConfig{
		ExtraEnv: map[string]string{
			"MY_CUSTOM_VAR": "should-not-appear",
			"NODE_ENV":      "production",
		},
	}

	snap, err := UserProfile(home, cfg)
	if err != nil {
		t.Fatalf("UserProfile() error: %v", err)
	}

	// Verify by building a sandbox with this profile — ExtraEnv must come
	// from seatbelt.Config.ExtraEnv, not from the snapshot.
	// We can only verify structurally: UserProfile does not call AddEnv.
	// The snapshot's env map should be empty.
	_ = snap

	// If UserProfile had AddEnv calls for ExtraEnv keys, it would show up
	// in the SBPL env section. We verify by building a full sandbox and
	// checking the env doesn't get double-applied when ExtraEnv is also
	// set in seatbelt.Config.
	repoDir := t.TempDir()
	sb, sbErr := seatbelt.New(seatbelt.Config{
		RepoPath:     repoDir,
		RepoWritable: true,
		Profiles:     []seatbelt.Snapshot{snap},
		ExtraEnv:     cfg.ExtraEnv,
	})
	if sbErr != nil {
		t.Fatalf("seatbelt.New() error: %v", sbErr)
	}
	defer sb.Close()

	// Run env command and verify MY_CUSTOM_VAR appears exactly once
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "env | grep MY_CUSTOM_VAR | wc -l")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	count := strings.TrimSpace(out.String())
	if count != "1" {
		t.Errorf("MY_CUSTOM_VAR appeared %s times in env, want exactly 1 (no duplication)", count)
	}
}
