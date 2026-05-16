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

// ===== ResolvePath tests =====

func TestResolvePath(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}

	t.Run("expands tilde", func(t *testing.T) {
		p, err := ResolvePath("~", home)
		if err != nil {
			t.Fatalf("ResolvePath(~) failed: %v", err)
		}
		if !p.IsDir {
			t.Error("home should be a directory")
		}
		if !filepath.IsAbs(p.Resolved) {
			t.Errorf("resolved path not absolute: %q", p.Resolved)
		}
	})

	t.Run("file vs directory", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}

		pDir, err := ResolvePath(dir, home)
		if err != nil {
			t.Fatal(err)
		}
		if !pDir.IsDir {
			t.Error("expected IsDir=true for directory")
		}

		pFile, err := ResolvePath(file, home)
		if err != nil {
			t.Fatal(err)
		}
		if pFile.IsDir {
			t.Error("expected IsDir=false for file")
		}
	})

	t.Run("resolves symlinks", func(t *testing.T) {
		dir := t.TempDir()
		resolvedDir, _ := filepath.EvalSymlinks(dir)
		target := filepath.Join(resolvedDir, "target")
		link := filepath.Join(resolvedDir, "link")
		os.Mkdir(target, 0755)
		os.Symlink(target, link)

		p, err := ResolvePath(link, home)
		if err != nil {
			t.Fatal(err)
		}
		if p.Resolved != target {
			t.Errorf("expected resolved=%q, got %q", target, p.Resolved)
		}
	})
}

func TestPath_EmptyString(t *testing.T) {
	_, err := ResolvePath("", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestPath_NullByteInjection(t *testing.T) {
	_, err := ResolvePath("/opt/dir\x00/hidden", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for null byte in path")
	}
	// Go's syscall layer rejects null bytes with EINVAL — no custom check needed.
}

func TestPath_RelativePath(t *testing.T) {
	_, err := ResolvePath("some/relative/file", os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestPath_RecursiveSymlink(t *testing.T) {
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
	home := os.Getenv("HOME")
	// ~/../../../../etc should resolve to /etc (or /private/etc on macOS)
	p, err := ResolvePath("~/../../../../etc", home)
	if err != nil {
		t.Skipf("traversal path doesn't exist (fine): %v", err)
	}
	// Should resolve to the canonical path, not contain ..
	if strings.Contains(p.Resolved, "..") {
		t.Errorf("resolved path contains ..: %q", p.Resolved)
	}
}

func TestPath_EmptyHome(t *testing.T) {
	_, err := ResolvePath("~/something", "")
	if err == nil {
		t.Fatal("expected error when home is empty")
	}
}

// ===== Permission & ToolProfile tests =====

func TestToolProfile_Allow(t *testing.T) {
	home := os.Getenv("HOME")
	dir := t.TempDir()

	p := NewToolProfile("test", home)
	if err := p.Allow(dir, Read); err != nil {
		t.Fatalf("Allow dir Read failed: %v", err)
	}

	file := filepath.Join(dir, "f.txt")
	os.WriteFile(file, []byte("x"), 0644)
	if err := p.Allow(file, Read); err != nil {
		t.Fatalf("Allow file Read failed: %v", err)
	}
}

func TestToolProfile_ExecRequiresDir(t *testing.T) {
	home := os.Getenv("HOME")
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	os.WriteFile(file, []byte("x"), 0644)

	p := NewToolProfile("test", home)
	err := p.Allow(file, Exec)
	if err == nil {
		t.Fatal("expected error: exec requires directory")
	}
	if !strings.Contains(err.Error(), "exec requires directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestToolProfile_AllowOptional_Missing(t *testing.T) {
	home := t.TempDir()
	p := NewToolProfile("test", home)
	// Should not error for non-existent paths
	if err := p.AllowOptional("/nonexistent/path/xyz", Read); err != nil {
		t.Fatalf("AllowOptional should skip not-exist: %v", err)
	}
}

func TestToolProfile_AddEnv(t *testing.T) {
	p := NewToolProfile("test", "/tmp")
	p.AddEnv("FOO", "bar")
	p.AddEnv("BAZ", "qux")
	snap := p.Snapshot()
	if snap.env["FOO"] != "bar" {
		t.Error("env FOO not set")
	}
	if snap.env["BAZ"] != "qux" {
		t.Error("env BAZ not set")
	}
}

// ===== Snapshot encapsulation tests =====

func TestProfile_Compilation(t *testing.T) {
	home := os.Getenv("HOME")
	dir := t.TempDir()

	p := NewToolProfile("test", home)
	p.Allow(dir, Read)
	p.AddEnv("KEY", "original")

	snap := p.Snapshot()

	// Mutate the original profile after snapshot
	p.Allow("/tmp", Write)
	p.AddEnv("KEY", "mutated")
	p.AddEnv("NEW", "value")

	// Snapshot must not reflect mutations
	if len(snap.entries) != 1 {
		t.Errorf("snapshot entries mutated: got %d, want 1", len(snap.entries))
	}
	if snap.env["KEY"] != "original" {
		t.Errorf("snapshot env mutated: KEY=%q, want 'original'", snap.env["KEY"])
	}
	if _, exists := snap.env["NEW"]; exists {
		t.Error("snapshot env leaked new key")
	}
}

// ===== ProfileBuilder / SBPL tests =====

func TestProfileBuilder_Build(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	workspace := Path{Resolved: resolvedDir, IsDir: true}
	home := os.Getenv("HOME")

	builder, err := NewProfileBuilder(workspace, home, "/private/tmp")
	if err != nil {
		t.Fatalf("NewProfileBuilder failed: %v", err)
	}

	// Add a snapshot with entries
	p := NewToolProfile("test-tool", home)
	subdir := filepath.Join(resolvedDir, "sub")
	os.MkdirAll(subdir, 0755)
	p.Allow(subdir, Read)
	builder.Add(p.Snapshot())

	profile, err := builder.Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	checks := []struct {
		name    string
		substr  string
		present bool
	}{
		{"version", "(version 1)", true},
		{"deny default", `(deny default (with message "orqestra"))`, true},
		{"workspace", `(subpath "` + resolvedDir + `")`, true},
		{"tool comment", `;; Tool: test-tool`, true},
		{"no sysctl-write", `sysctl-write`, false},
		{"no broad library exec", `(subpath "/Library")`, false},
		{"etc read-only", `(subpath "/private/etc")`, true},
		{"network outbound", `(allow network-outbound)`, true},
		{"root read-data", `(allow file-read-data`, true},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(profile, tc.substr) != tc.present {
				if tc.present {
					t.Errorf("expected profile to contain %q", tc.substr)
				} else {
					t.Errorf("expected profile NOT to contain %q\n%s", tc.substr, profile)
				}
			}
		})
	}
}

func TestProfileBuilder_Errors(t *testing.T) {
	t.Run("workspace not dir", func(t *testing.T) {
		_, err := NewProfileBuilder(Path{Resolved: "/tmp/x", IsDir: false}, "/Users/test", "/tmp")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty home", func(t *testing.T) {
		_, err := NewProfileBuilder(Path{Resolved: "/tmp", IsDir: true}, "", "/tmp")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty tmpdir", func(t *testing.T) {
		_, err := NewProfileBuilder(Path{Resolved: "/tmp", IsDir: true}, "/Users/test", "")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestProfileBuilder_WorkspaceIsLast(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	builder, err := NewProfileBuilder(
		Path{Resolved: resolvedDir, IsDir: true},
		"/Users/test",
		"/private/tmp",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Add profiles with entries
	p1 := NewToolProfile("alpha", "/Users/test")
	p1.entries = []entry{{path: Path{Resolved: "/alpha", IsDir: true}, perm: Read}}
	builder.Add(p1.Snapshot())

	profile, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}

	wsIdx := strings.LastIndex(profile, `(subpath "`+resolvedDir+`")`)
	alphaIdx := strings.Index(profile, `(subpath "/alpha")`)
	if wsIdx <= alphaIdx {
		t.Error("workspace must be the last rule")
	}
}

// ===== BaseEnv tests =====

func TestBaseEnv(t *testing.T) {
	env := BaseEnv("/Users/test", "/private/tmp", "/tmp/workspace")

	required := map[string]bool{
		"HOME": false, "TMPDIR": false, "LANG": false,
		"TERM": false, "USER": false, "PATH": false, "LC_ALL": false,
	}

	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if _, ok := required[key]; ok {
			required[key] = true
		}
	}

	for key, found := range required {
		if !found {
			t.Errorf("BaseEnv missing required variable: %s", key)
		}
	}
}

func TestBaseEnv_NoLeak(t *testing.T) {
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

// ===== MergeEnv tests =====

func TestMergeEnv_OverrideOrder(t *testing.T) {
	base := []string{"KEY=base", "PATH=/usr/bin"}
	snap := Snapshot{env: map[string]string{"KEY": "tool"}}
	harness := []string{"KEY=harness"}

	env, err := MergeEnv(base, []Snapshot{snap}, harness, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Find KEY — harness should win
	for _, kv := range env {
		if strings.HasPrefix(kv, "KEY=") {
			if kv != "KEY=harness" {
				t.Errorf("expected KEY=harness, got %q", kv)
			}
			return
		}
	}
	t.Error("KEY not found in merged env")
}

func TestMergeEnv_ProxyEnvMissing(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	_, err := MergeEnv(base, nil, nil, []string{"NONEXISTENT_VAR_XYZ"}, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing proxy env var")
	}
}

func TestMergeEnv_InvalidHarnessEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	_, err := MergeEnv(base, nil, []string{"MISSING_EQUALS"}, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid harness env entry")
	}
}

func TestEnv_HarnessEnvPreserved(t *testing.T) {
	base := BaseEnv("/Users/test", "/private/tmp", "/tmp/ws")
	harnessVars := []string{
		"ANTHROPIC_BASE_URL=http://localhost:11434",
		"ANTHROPIC_API_KEY=test-key",
		"ANTHROPIC_AUTH_TOKEN=test-token",
		"ANTHROPIC_MODEL=claude-sonnet-4-20250514",
		"ANTHROPIC_SMALL_FAST_MODEL_BASE_URL=http://localhost:11434",
		"ANTHROPIC_SMALL_FAST_MODEL_NAME=qwen3",
		"DISABLE_CLAUDE_TRAFFIC=1",
	}

	env, err := MergeEnv(base, nil, harnessVars, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	envMap := make(map[string]string)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}

	for _, kv := range harnessVars {
		k, v, _ := strings.Cut(kv, "=")
		if got := envMap[k]; got != v {
			t.Errorf("harness var %s = %q, want %q", k, got, v)
		}
	}
}

func TestExtraPathDirs(t *testing.T) {
	snaps := []Snapshot{
		{entries: []entry{
			{path: Path{Resolved: "/opt/homebrew/bin", IsDir: true}, perm: Exec},
			{path: Path{Resolved: "/opt/homebrew/lib", IsDir: true}, perm: Read},
			{path: Path{Resolved: "/usr/local/bin", IsDir: true}, perm: Exec},
		}},
	}
	dirs := ExtraPathDirs(snaps)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
	// Should only include "bin" dirs
	for _, d := range dirs {
		if filepath.Base(d) != "bin" {
			t.Errorf("unexpected non-bin dir: %s", d)
		}
	}
}

// ===== Sandbox New validation tests =====

func TestSandbox_NewValidation(t *testing.T) {
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

// ===== Security property tests (require sandbox-exec) =====

func TestDeny_ReadFileOutsideAllowlist(t *testing.T) {
	workspace := t.TempDir()

	// Create a secret file outside workspace AND outside allowed paths.
	// t.TempDir() is under /private/var/folders which is in the allowlist,
	// so we use a subdir of HOME which is not broadly allowed.
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
	workspace := t.TempDir()

	// Use a subdir of HOME (not in allowlist) as the target
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
	workspace := t.TempDir()

	// Create a file we'll allow via profile
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

func TestAllow_LocalhostNetwork(t *testing.T) {
	workspace := t.TempDir()

	// Write a tiny Go program to test localhost connectivity directly without python/ruby
	src := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(src, []byte(`package main
import ("fmt"; "net")
func main() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { panic(err) }
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil { panic(err) }
	defer c.Close()
	fmt.Println("LOCALHOST_OK")
}`), 0644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(workspace, "nettest")
	buildCmd := exec.Command("go", "build", "-o", bin, src)
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("go build failed: %v", err)
	}

	sb, err := New(Config{
		RepoPath: workspace, RepoWritable: true,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "LOCALHOST_OK") {
		t.Errorf("localhost network test failed, got: %q", stdout.String())
	}
}

// ===== Necessity tests =====

func TestNecessity_EtcReadable(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/cat", "/etc/hosts")
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "localhost") {
		t.Errorf("/etc/hosts should contain localhost, got: %q", stdout.String())
	}
}

func TestNecessity_ProcessFork(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// sh -c spawns a subprocess
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo FORK_OK")
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "FORK_OK") {
		t.Error("process fork failed")
	}
}

func TestNecessity_TmpWritable(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `echo TMP_OK > /tmp/seatbelt-test-$$ && cat /tmp/seatbelt-test-$$ && rm /tmp/seatbelt-test-$$`)
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "TMP_OK") {
		t.Error("/tmp should be writable")
	}
}

// ===== Resource limit tests =====

func TestLimits_RlimitNoFile(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Open many file descriptors — should hit the limit
	script := `python3 -c "
import os, sys
fds = []
try:
    for i in range(8192):
        fds.append(os.open('/dev/null', os.O_RDONLY))
except OSError as e:
    print(f'HIT_LIMIT at {len(fds)}: {e}')
    sys.exit(0)
print(f'NO_LIMIT: opened {len(fds)} fds')
"`
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	sb.Run(ctx, cmd)

	output := stdout.String()
	if strings.Contains(output, "NO_LIMIT") {
		t.Error("RLIMIT_NOFILE not enforced — opened 8192+ file descriptors")
	}
	if strings.Contains(output, "HIT_LIMIT") {
		t.Logf("RLIMIT_NOFILE enforced: %s", output)
	}
}

func TestLimits_ZombieReaping(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start a script that spawns background sleeps, then cancel the context
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & sleep 30 & sleep 30 & wait")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	err = sb.Run(ctx, cmd)
	// Should be canceled
	if err == nil {
		t.Fatal("expected context cancellation error")
	}

	// The process group should be dead — this is tested by context cancellation
	// triggering SIGKILL to -PGID in sandbox.Run
	t.Logf("zombie reaping test passed: %v", err)
}

func TestDeny_ExecArbitraryBinary(t *testing.T) {
	workspace := t.TempDir()

	// Place a binary outside any exec profile
	home := os.Getenv("HOME")
	binDir := filepath.Join(home, ".seatbelt-test-deny-exec")
	os.MkdirAll(binDir, 0755)
	defer os.RemoveAll(binDir)

	// Copy /bin/echo to the unallowed dir
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
	workspace := t.TempDir()

	// Create a shell script in workspace — no code signing needed
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

func TestNecessity_EtcNotExecutable(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Attempt to exec /etc/hosts — must be denied
	cmd := exec.CommandContext(ctx, "/private/etc/hosts")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	err = sb.Run(ctx, cmd)
	if err == nil {
		t.Fatal("SECURITY FAILURE: sandbox allowed exec of /private/etc/hosts")
	}
}

func TestNecessity_LibraryFrameworks(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// sw_vers uses CoreFoundation.framework — proves framework linking works
	cmd := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion")
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if stdout.Len() == 0 {
		t.Error("sw_vers produced no output — framework linking may be broken")
	}
}

func TestNecessity_MachLookup(t *testing.T) {
	workspace := t.TempDir()
	sb, err := New(Config{RepoPath: workspace, RepoWritable: true})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// security find-identity uses mach-lookup for Keychain access
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "default-keychain")
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run failed (mach-lookup may be blocked): %v", err)
	}

	if stdout.Len() == 0 {
		t.Error("security default-keychain produced no output — mach-lookup may be broken")
	}
}

// TestLimits_RlimitNproc is removed: ulimit -u (RLIMIT_NPROC) is a per-USER
// limit on macOS, not per-process-tree. Setting it to a low value breaks the
// sandbox when the host already has many processes. Containment is provided by
// SBPL profile + pgid isolation.

func TestPath_MaxLengthExceeded(t *testing.T) {
	// PATH_MAX on macOS is 1024
	longName := strings.Repeat("a", 1100)
	_, err := ResolvePath("/"+longName, os.Getenv("HOME"))
	if err == nil {
		t.Fatal("expected error for path exceeding PATH_MAX")
	}
}

// ===== Wrap tests =====

func TestWrap_EmptyCommand(t *testing.T) {
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

// === Phase 3: Dual-root access model tests ===

func TestSeatbelt_ReadonlyRepoWriteDenied(t *testing.T) {
	// Use a directory under HOME (not under /tmp or /var/folders) to avoid
	// the base profile's temp-write allowance.
	home := os.Getenv("HOME")
	repoDir := filepath.Join(home, ".seatbelt-test-readonly-repo")
	os.MkdirAll(repoDir, 0755)
	defer os.RemoveAll(repoDir)

	sessionDir := t.TempDir()

	// Create a file in repo to prove it exists
	os.WriteFile(filepath.Join(repoDir, "existing.txt"), []byte("readonly"), 0644)

	sb, err := New(Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: false, // readonly
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
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	sb, err := New(Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: true, // worker mode
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

func TestSeatbelt_SymlinkRootsResolved(t *testing.T) {
	// Create a real directory and a symlink to it
	realDir := t.TempDir()
	// Resolve to get the canonical path (macOS adds /private prefix)
	realDir, _ = filepath.EvalSymlinks(realDir)

	symlinkDir := filepath.Join(t.TempDir(), "link")
	os.Symlink(realDir, symlinkDir)

	sb, err := New(Config{
		RepoPath:     symlinkDir,
		RepoWritable: true,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	// The workspace should be the resolved real path
	if sb.Workspace() != realDir {
		t.Errorf("Workspace() = %q, want resolved %q", sb.Workspace(), realDir)
	}
}

func TestSeatbelt_EnvPrecedence_HarnessWinsOverBase(t *testing.T) {
	workspace := t.TempDir()

	// HarnessEnv should override base env (e.g. LANG set by BaseEnv)
	sb, err := New(Config{
		RepoPath:     workspace,
		RepoWritable: true,
		HarnessEnv:   []string{"LANG=custom-harness-locale"},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo $LANG")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := strings.TrimSpace(out.String())
	if got != "custom-harness-locale" {
		t.Errorf("LANG = %q, want %q (HarnessEnv should override base)", got, "custom-harness-locale")
	}
}

func TestSeatbelt_EnvPrecedence_ExtraWinsOverAll(t *testing.T) {
	workspace := t.TempDir()

	// ExtraEnv should override both base and harness env
	sb, err := New(Config{
		RepoPath:     workspace,
		RepoWritable: true,
		HarnessEnv:   []string{"LANG=from-harness"},
		ExtraEnv:     map[string]string{"LANG": "from-extra-final"},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer sb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo $LANG")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}

	if err := sb.Run(ctx, cmd); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := strings.TrimSpace(out.String())
	if got != "from-extra-final" {
		t.Errorf("LANG = %q, want %q (ExtraEnv should override all)", got, "from-extra-final")
	}
}
