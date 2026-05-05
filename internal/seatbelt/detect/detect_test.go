//go:build darwin

package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectClaude_BinaryNotFound(t *testing.T) {
	home := t.TempDir()
	_, err := DetectClaude(home, "nonexistent-binary-xyz")
	if err == nil {
		t.Fatal("expected error when claude binary not found")
	}
}

func TestDetectClaude_WithRealBinary(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}

	snap, err := DetectClaude(home, "claude")
	if err != nil {
		t.Fatalf("DetectClaude failed: %v", err)
	}
	// Snapshot is opaque; just verify we got here without error
	_ = snap
}

func TestDetectClaude_OptionalPathsMissing(t *testing.T) {
	// Use a temp dir as home — none of the optional paths will exist
	home := t.TempDir()

	// Create a fake claude binary
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	snap, err := DetectClaude(home, fakeBin)
	if err != nil {
		t.Fatalf("DetectClaude with missing optionals should succeed: %v", err)
	}
	// Should have at least the binary dir entry
	_ = snap
}

func TestDetectHomebrew(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	snap, err := DetectHomebrew(home)
	if err != nil {
		t.Fatalf("DetectHomebrew failed: %v", err)
	}
	if snap == nil {
		t.Skip("homebrew not installed")
	}
}

func TestDetectDocker(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	snap, err := DetectDocker(home)
	if err != nil {
		t.Fatalf("DetectDocker failed: %v", err)
	}
	if snap == nil {
		t.Skip("docker not installed")
	}
}

func TestDetectGit(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	snap, err := DetectGit(home)
	if err != nil {
		t.Fatalf("DetectGit failed: %v", err)
	}
	if snap == nil {
		t.Skip("git config not found")
	}
}

func TestDetectNPM(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	snap, err := DetectNPM(home)
	if err != nil {
		t.Fatalf("DetectNPM failed: %v", err)
	}
	if snap == nil {
		t.Skip("npm/nvm not found")
	}
}

func TestDetectDocker_NotInstalled(t *testing.T) {
	// Use temp dir as home — no Docker paths in HOME
	home := t.TempDir()
	snap, err := DetectDocker(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// May be non-nil if /Applications/Docker.app exists on this machine
	_ = snap
}

func TestDetectGit_NotInstalled(t *testing.T) {
	home := t.TempDir()
	snap, err := DetectGit(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// May be non-nil if /Library/Developer/CommandLineTools exists
	_ = snap
}

func TestDetectNPM_NotInstalled(t *testing.T) {
	home := t.TempDir()
	snap, err := DetectNPM(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap != nil {
		t.Error("expected nil when npm not installed in temp home")
	}
}
