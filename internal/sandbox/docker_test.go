package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// --- buildCreateArgs ---

func TestBuildCreateArgs_RequiredFlags(t *testing.T) {
	repoDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
	}, repoDir, nil)
	args := d.buildCreateArgs()

	mustContain(t, args, "create")
	mustContain(t, args, "--init")
	mustContain(t, args, "--privileged")
	mustContain(t, args, "orqestra-sandbox:test")
}

func TestBuildCreateArgs_ResourceLimits(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image:     "orqestra-sandbox:test",
		Memory:    "2g",
		CPUs:      1.5,
		PidsLimit: 128,
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	mustContain(t, args, "--memory")
	mustContain(t, args, "2g")
	mustContain(t, args, "--cpus")
	mustContain(t, args, "1.5")
	mustContain(t, args, "--pids-limit")
	mustContain(t, args, "128")
}

func TestBuildCreateArgs_NoResourceLimitsOmitted(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		// Memory, CPUs, PidsLimit deliberately zero.
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	if containsStr(args, "--memory") {
		t.Error("--memory should be omitted when Memory is empty")
	}
	if containsStr(args, "--cpus") {
		t.Error("--cpus should be omitted when CPUs is 0")
	}
	if containsStr(args, "--pids-limit") {
		t.Error("--pids-limit should be omitted when PidsLimit is 0")
	}
}

func TestBuildCreateArgs_NetworkMode(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:test",
		Network: "host",
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	mustContain(t, args, "--network")
	mustContain(t, args, "host")
}

func TestBuildCreateArgs_NoNetworkFlagWhenEmpty(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	if containsStr(args, "--network") {
		t.Error("--network should be omitted when Network is empty")
	}
}

func TestBuildCreateArgs_ReadOnlyMounts(t *testing.T) {
	mountDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		ReadOnlyMounts: []MountConfig{
			{HostPath: mountDir, ContainerPath: "/deps/node_modules"},
		},
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	// Check for the readonly bind mount string.
	wantFragment := fmt.Sprintf("source=%s,target=/deps/node_modules,readonly", mountDir)
	if !containsSubstring(args, wantFragment) {
		t.Errorf("args missing read-only mount fragment %q", wantFragment)
	}
}

func TestBuildCreateArgs_BindMounts(t *testing.T) {
	mountDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		BindMounts: []MountConfig{
			{HostPath: mountDir, ContainerPath: "/credentials"},
		},
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	wantFragment := fmt.Sprintf("source=%s,target=/credentials", mountDir)
	if !containsSubstring(args, wantFragment) {
		t.Errorf("args missing bind mount fragment %q", wantFragment)
	}
}

func TestBuildCreateArgs_MCPSocketPresent(t *testing.T) {
	// Create a real socket-like file so os.Stat succeeds.
	sockFile := filepath.Join(t.TempDir(), "mcp.sock")
	if err := os.WriteFile(sockFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		MCP:   MCPConfig{SocketPath: sockFile},
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	wantFragment := fmt.Sprintf("source=%s,target=/run/mcp.sock,readonly", sockFile)
	if !containsSubstring(args, wantFragment) {
		t.Errorf("args missing MCP socket mount fragment %q", wantFragment)
	}
}

func TestBuildCreateArgs_MCPSocketMissing_Skipped(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		MCP:   MCPConfig{SocketPath: "/nonexistent/mcp.sock"},
	}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	if containsSubstring(args, "/run/mcp.sock") {
		t.Error("missing MCP socket should be silently skipped from the mount args")
	}
}

func TestBuildCreateArgs_Labels(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), nil)
	args := d.buildCreateArgs()

	mustContain(t, args, "--label")
	mustContain(t, args, LabelOwner+"="+LabelOwnerValue)
}

func TestBuildCreateArgs_EnvironmentVars(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), []string{
		"CLAUDE_API_KEY=secret",
		"ORQESTRA_SESSION=abc123",
	})
	args := d.buildCreateArgs()

	mustContain(t, args, "-e")
	mustContain(t, args, "CLAUDE_API_KEY=secret")
	mustContain(t, args, "ORQESTRA_SESSION=abc123")
}

// containsSubstring returns true if any element contains substr.
func containsSubstring(args []string, substr string) bool {
	for _, a := range args {
		if len(a) >= len(substr) {
			for i := 0; i <= len(a)-len(substr); i++ {
				if a[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

// --- reaper.Run ---

// funcTracker implements ContainerTracker with function fields for flexible reaper testing.
type funcTracker struct {
	listFn func(ctx context.Context) ([]TrackedContainer, error)
	killFn func(ctx context.Context, id string) error
}

func (m *funcTracker) ListOrqestraContainers(ctx context.Context) ([]TrackedContainer, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

func (m *funcTracker) KillAndRemove(ctx context.Context, id string) error {
	if m.killFn != nil {
		return m.killFn(ctx, id)
	}
	return nil
}

func TestReaper_Run_StopsOnContextCancel(t *testing.T) {
	sweepCount := 0
	tracker := &funcTracker{
		listFn: func(_ context.Context) ([]TrackedContainer, error) {
			sweepCount++
			return nil, nil
		},
	}
	r := NewReaper(tracker, 10*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, 5*time.Millisecond)
	}()

	// Let it tick a couple of times.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Expected: loop exits after cancel.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation within 2s")
	}
	if sweepCount == 0 {
		t.Error("expected at least one sweep before cancellation")
	}
}

func TestReaper_Run_FinalCleanupOnCancel(t *testing.T) {
	// Verify that CleanupAll is called when the context is cancelled.
	cleanupCalled := false
	tracker := &funcTracker{
		listFn: func(_ context.Context) ([]TrackedContainer, error) {
			// Return a container only after cancel so Sweep does nothing,
			// but CleanupAll finds it.
			return []TrackedContainer{{ID: "orphan-1", CreatedAt: time.Now()}}, nil
		},
		killFn: func(_ context.Context, id string) error {
			if id == "orphan-1" {
				cleanupCalled = true
			}
			return nil
		},
	}
	r := NewReaper(tracker, 10*time.Minute) // max lifetime long enough to not expire
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, time.Hour) // long interval so no sweeps trigger
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}

	if !cleanupCalled {
		t.Error("CleanupAll must be called on context cancellation to remove orphan containers")
	}
}

// --- containsTraversal edge cases ---

func TestContainsTraversal_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// filepath.Clean resolves these before checking — only paths that
		// truly escape the workspace root (result starts with ".." or is absolute) are rejected.
		{"clean relative", "src/main.go", false},
		{"dot only", ".", false},
		{"double dot", "..", true},
		// src/../etc/passwd → filepath.Clean → etc/passwd (within workspace) → safe
		{"embedded traversal stays in workspace", "src/../etc/passwd", false},
		// src/../../etc/passwd → filepath.Clean → ../etc/passwd (escapes) → rejected
		{"embedded traversal escapes workspace", "src/../../etc/passwd", true},
		{"absolute path", "/etc/passwd", true},
		{"absolute path cleaned", "/workspace/../host", true},
		{"deep nested escapes", "a/b/c/../../d/../../../etc", true},
		{"Windows-style backslash not traversal", `src\main.go`, false},
		{"dotdot in filename not traversal", "src/..hidden/file.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsTraversal(tt.path)
			if got != tt.want {
				t.Errorf("containsTraversal(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// --- parseDiffOutput alias ---

func TestParseDiffOutput_DelegatesToParseBtrfsDump(t *testing.T) {
	lines := []string{
		"mkfile ./foo.go",
		"unlink ./bar.go",
	}
	alias := parseDiffOutput(lines)
	direct := parseBtrfsDump(lines)

	if len(alias) != len(direct) {
		t.Fatalf("parseDiffOutput len=%d, parseBtrfsDump len=%d", len(alias), len(direct))
	}

	sortChangedFiles := func(cf []ChangedFile) {
		sort.Slice(cf, func(i, j int) bool {
			return cf[i].Path < cf[j].Path
		})
	}
	sortChangedFiles(alias)
	sortChangedFiles(direct)

	for i := range alias {
		if alias[i] != direct[i] {
			t.Errorf("result[%d]: alias=%+v direct=%+v", i, alias[i], direct[i])
		}
	}
}

// --- stageCopy ---

func TestStageCopy_CreatesParentsAndWritesContent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "subdir", "file.go")
	content := []byte("package main\n")

	if err := stageCopy(content, dest); err != nil {
		t.Fatalf("stageCopy() error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestStageCopy_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "file.go")
	os.WriteFile(dest, []byte("old"), 0o644)

	newContent := []byte("new content")
	if err := stageCopy(newContent, dest); err != nil {
		t.Fatalf("stageCopy() error: %v", err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != string(newContent) {
		t.Errorf("got %q, want %q", got, newContent)
	}
}
