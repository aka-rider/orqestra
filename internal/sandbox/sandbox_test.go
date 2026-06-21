//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===== INV-P4-PARSE: hostile-input path validation =====

func TestPath_EmptyString(t *testing.T) {
	// INV-P4-PARSE: empty path must be rejected
	_, err := ResolvePath("", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestPath_NullByteInjection(t *testing.T) {
	// INV-P4-PARSE: null bytes in path must be rejected
	_, err := ResolvePath("/opt/dir\x00/hidden", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for null byte in path")
	}
	// Go's syscall layer rejects null bytes with EINVAL — no custom check needed.
}

func TestPath_RelativePath(t *testing.T) {
	// INV-P4-PARSE: relative paths must be rejected
	_, err := ResolvePath("some/relative/file", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestPath_RecursiveSymlink(t *testing.T) {
	// INV-P4-PARSE: recursive symlinks must be rejected
	dir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	a := filepath.Join(resolvedDir, "a")
	b := filepath.Join(resolvedDir, "b")
	os.Symlink(b, a)
	os.Symlink(a, b)

	_, err := ResolvePath(a, os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for recursive symlink")
	}
}

func TestPath_BrokenSymlink(t *testing.T) {
	// INV-P4-PARSE: broken symlinks must be rejected
	dir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	link := filepath.Join(resolvedDir, "broken")
	os.Symlink("/nonexistent/target/xyz", link)

	_, err := ResolvePath(link, os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for broken symlink")
	}
}

func TestPath_DirectoryTraversal(t *testing.T) {
	// INV-P4-PARSE: directory traversal must be canonicalized (no ".." in resolved path)
	home := os.Getenv("HOME")
	// ~/../../../../etc should resolve to the canonical path, not contain ..
	p, err := ResolvePath("~/../../../../etc", home)
	if err != nil {
		t.Skipf("traversal path doesn't exist (fine): %v", err)
	}
	if strings.Contains(p.Resolved, "..") {
		t.Errorf("resolved path contains ..: %q", p.Resolved)
	}
}

func TestPath_MaxLengthExceeded(t *testing.T) {
	// INV-P4-PARSE: path exceeding PATH_MAX (1024 on macOS) must be rejected
	longName := strings.Repeat("a", 1100)
	_, err := ResolvePath("/"+longName, os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for path exceeding PATH_MAX")
	}
}

// ===== INV-P2-WRITE: sandbox construction validation =====

func TestSandbox_NewValidation(t *testing.T) {
	// INV-P2-WRITE: New() must fail closed on invalid workspace inputs
	t.Run("empty workspace", func(t *testing.T) {
		_, err := New(Config{RepoPath: ""})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("nonexistent workspace", func(t *testing.T) {
		_, err := New(Config{RepoPath: "/nonexistent/dir/xyz"})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("file as workspace", func(t *testing.T) {
		f, _ := os.CreateTemp("", "ws")
		f.Close()
		defer os.Remove(f.Name())
		_, err := New(Config{RepoPath: f.Name()})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

// ===== INV-P2-WRITE: secret non-leakage via env =====

func TestBaseEnv_NoLeak(t *testing.T) {
	// INV-P2-WRITE: sandbox base env must not include host secrets
	sensitiveVars := []string{
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
	}
	for _, v := range sensitiveVars {
		t.Setenv(v, "LEAKED_"+v)
	}

	env := BaseEnv("/Users/test", "/private/tmp", "/tmp/ws")
	envStr := strings.Join(env, "\n")

	for _, v := range sensitiveVars {
		if strings.Contains(envStr, "LEAKED_"+v) {
			t.Errorf("BaseEnv leaked sensitive variable: %s", v)
		}
	}
}

// ===== INV-P2-WRITE: security property tests (require sandbox-exec) =====

func TestDeny_ReadFileOutsideAllowlist(t *testing.T) {
	// INV-P2-WRITE: worker cannot read files outside the allowed set
	workspace := t.TempDir()

	home := os.Getenv("HOME")
	secretDir := filepath.Join(home, ".seatbelt-test-deny-read")
	os.MkdirAll(secretDir, 0755)
	defer os.RemoveAll(secretDir)
	secretFile := filepath.Join(secretDir, "secret.txt")
	os.WriteFile(secretFile, []byte("TOP_SECRET_DATA"), 0644)

	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/cat", secretFile)
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	sb.Run(ctx, cmd) // may error — that's expected

	if strings.Contains(stdout.String(), "TOP_SECRET_DATA") {
		t.Fatal("SECURITY FAILURE: sandbox leaked secret file content")
	}
}

func TestDeny_WriteOutsideWorkspace(t *testing.T) {
	// INV-P2-WRITE: worker cannot write files outside the workspace
	workspace := t.TempDir()

	home := os.Getenv("HOME")
	targetDir := filepath.Join(home, ".seatbelt-test-deny-write")
	os.MkdirAll(targetDir, 0755)
	defer os.RemoveAll(targetDir)
	targetFile := filepath.Join(targetDir, "written.txt")

	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo BREACH > '"+targetFile+"'")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	sb.Run(ctx, cmd) // expected to fail

	if _, err := os.Stat(targetFile); err == nil {
		content, _ := os.ReadFile(targetFile)
		os.Remove(targetFile)
		t.Fatalf("SECURITY FAILURE: wrote outside workspace: %s", string(content))
	}
}

func TestAllow_ReadFromProfile(t *testing.T) {
	// INV-P2-WRITE: explicitly allowed paths are readable
	workspace := t.TempDir()

	allowDir := t.TempDir()
	resolvedAllowDir, _ := filepath.EvalSymlinks(allowDir)
	allowFile := filepath.Join(resolvedAllowDir, "readable.txt")
	os.WriteFile(allowFile, []byte("ALLOWED_CONTENT"), 0644)

	home := os.Getenv("HOME")
	p := NewToolProfile("test", home)
	p.Allow(resolvedAllowDir, Read)

	sb, err := New(Config{
		RepoPath: workspace, RepoWritable: true,
		Profiles: []Snapshot{p.Snapshot()},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/cat", allowFile)
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "ALLOWED_CONTENT") {
		t.Errorf("expected to read allowed file, got: %q", stdout.String())
	}
}

func TestAllow_WriteInWorkspace(t *testing.T) {
	// INV-P2-WRITE: worker can write within the workspace
	workspace := t.TempDir()

	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	targetFile := filepath.Join(workspace, "output.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo WORKSPACE_WRITE > "+targetFile)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("file not created in workspace: %v", err)
	}
	if !strings.Contains(string(content), "WORKSPACE_WRITE") {
		t.Errorf("wrong content: %q", string(content))
	}
}

func TestDeny_ExecArbitraryBinary(t *testing.T) {
	// INV-P2-WRITE: worker cannot exec binaries outside allowed exec profile
	workspace := t.TempDir()

	home := os.Getenv("HOME")
	binDir := filepath.Join(home, ".seatbelt-test-deny-exec")
	os.MkdirAll(binDir, 0755)
	defer os.RemoveAll(binDir)

	src, err := os.ReadFile("/bin/echo")
	if err != nil {
		t.Fatalf("read /bin/echo: %v", err)
	}
	binFile := filepath.Join(binDir, "echo")
	os.WriteFile(binFile, src, 0755)

	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binFile, "EXEC_LEAKED")
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	sb.Run(ctx, cmd) // expected to fail

	if strings.Contains(stdout.String(), "EXEC_LEAKED") {
		t.Fatal("SECURITY FAILURE: sandbox allowed exec of arbitrary binary outside profile")
	}
}

func TestAllow_ExecFromProfile(t *testing.T) {
	// INV-P2-WRITE: scripts inside the workspace are executable
	workspace := t.TempDir()

	execDir := filepath.Join(workspace, "bin")
	os.MkdirAll(execDir, 0755)
	scriptFile := filepath.Join(execDir, "myecho.sh")
	os.WriteFile(scriptFile, []byte("#!/bin/sh\necho EXEC_OK\n"), 0755)

	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptFile)
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "EXEC_OK") {
		t.Errorf("expected to exec script from workspace, got: %q", stdout.String())
	}
}

// ===== INV-P4-PARSE: Wrap hostile-input rejection =====

func TestWrap_EmptyCommand(t *testing.T) {
	// INV-P4-PARSE: Wrap must reject an empty command
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	cmd := &exec.Cmd{}
	if err := sb.Wrap(cmd); err == nil {
		t.Fatal("expected error wrapping empty command")
	}
}

// ===== INV-P2-WRITE: dual-root access model =====

func TestSeatbelt_ReadonlyRepoWriteDenied(t *testing.T) {
	// INV-P2-WRITE: with RepoWritable=false, writes to repo root are denied
	home := os.Getenv("HOME")
	repoDir := filepath.Join(home, ".seatbelt-test-readonly-repo")
	os.MkdirAll(repoDir, 0755)
	defer os.RemoveAll(repoDir)

	sessionDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "existing.txt"), []byte("readonly"), 0644)

	sb, err := New(Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: false,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	targetFile := filepath.Join(repoDir, "breach.txt")
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo BREACH > '"+targetFile+"'")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	sb.Run(ctx, cmd)

	if _, err := os.Stat(targetFile); err == nil {
		os.Remove(targetFile)
		t.Fatal("SECURITY FAILURE: readonly sandbox wrote to repo root")
	}
}

func TestSeatbelt_ReadonlySessionWriteAllowed(t *testing.T) {
	// INV-P2-WRITE: with RepoWritable=false, writes to the session dir are permitted
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	sb, err := New(Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: false,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	targetFile := filepath.Join(sessionDir, "artifact.json")
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo '{\"ok\":true}' > '"+targetFile+"'")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("session write failed — file not created: %v", err)
	}
	if !strings.Contains(string(data), "ok") {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestSeatbelt_WorkerRepoWriteAllowed(t *testing.T) {
	// INV-P2-WRITE: with RepoWritable=true, the worker can write code files to the repo
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	sb, err := New(Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: true,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	targetFile := filepath.Join(repoDir, "new-file.go")
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo 'package main' > '"+targetFile+"'")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("worker repo write failed: %v", err)
	}
	if !strings.Contains(string(data), "package main") {
		t.Errorf("unexpected content: %s", data)
	}
}
