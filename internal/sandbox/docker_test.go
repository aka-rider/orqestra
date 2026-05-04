package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/mount"
)

// --- buildContainerConfig ---

func TestBuildContainerConfig_RequiredFields(t *testing.T) {
	repoDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
	}, repoDir, nil)
	containerCfg, hostCfg := d.buildContainerConfig()

	if containerCfg.Image != d.ephemeralImage {
		t.Errorf("Image = %q, want %q", containerCfg.Image, d.ephemeralImage)
	}
	if !containerCfg.Tty {
		t.Error("Tty should be true")
	}
	if !containerCfg.OpenStdin {
		t.Error("OpenStdin should be true")
	}
	if hostCfg.Init == nil || !*hostCfg.Init {
		t.Error("Init should be true (--init)")
	}
	if hostCfg.Privileged {
		t.Error("Privileged should be false (no btrfs needed)")
	}
}

func TestBuildContainerConfig_ResourceLimits(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image:     "orqestra-sandbox:test",
		Memory:    "2g",
		CPUs:      1.5,
		PidsLimit: 128,
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	wantMem := int64(2 * 1024 * 1024 * 1024)
	if hostCfg.Resources.Memory != wantMem {
		t.Errorf("Memory = %d, want %d", hostCfg.Resources.Memory, wantMem)
	}
	wantCPU := int64(1.5 * 1e9)
	if hostCfg.Resources.NanoCPUs != wantCPU {
		t.Errorf("NanoCPUs = %d, want %d", hostCfg.Resources.NanoCPUs, wantCPU)
	}
	if hostCfg.Resources.PidsLimit == nil || *hostCfg.Resources.PidsLimit != 128 {
		t.Errorf("PidsLimit = %v, want 128", hostCfg.Resources.PidsLimit)
	}
}

func TestBuildContainerConfig_NoResourceLimitsOmitted(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	if hostCfg.Resources.Memory != 0 {
		t.Errorf("Memory should be 0 when not configured, got %d", hostCfg.Resources.Memory)
	}
	if hostCfg.Resources.NanoCPUs != 0 {
		t.Errorf("NanoCPUs should be 0 when not configured, got %d", hostCfg.Resources.NanoCPUs)
	}
	if hostCfg.Resources.PidsLimit != nil {
		t.Errorf("PidsLimit should be nil when not configured, got %v", hostCfg.Resources.PidsLimit)
	}
}

func TestBuildContainerConfig_NetworkMode(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:test",
		Network: "host",
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	if string(hostCfg.NetworkMode) != "host" {
		t.Errorf("NetworkMode = %q, want %q", hostCfg.NetworkMode, "host")
	}
}

func TestBuildContainerConfig_NoNetworkWhenEmpty(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	if string(hostCfg.NetworkMode) != "" {
		t.Errorf("NetworkMode should be empty, got %q", hostCfg.NetworkMode)
	}
}

func TestBuildContainerConfig_ReadOnlyMounts(t *testing.T) {
	mountDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		ReadOnlyMounts: []MountConfig{
			{HostPath: mountDir, ContainerPath: "/deps/node_modules"},
		},
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	found := false
	for _, m := range hostCfg.Mounts {
		if m.Target == "/deps/node_modules" && m.Source == mountDir && m.ReadOnly && m.Type == mount.TypeBind {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing read-only mount for /deps/node_modules")
	}
}

func TestBuildContainerConfig_BindMounts(t *testing.T) {
	mountDir := t.TempDir()
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		BindMounts: []MountConfig{
			{HostPath: mountDir, ContainerPath: "/credentials"},
		},
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	found := false
	for _, m := range hostCfg.Mounts {
		if m.Target == "/credentials" && m.Source == mountDir && !m.ReadOnly && m.Type == mount.TypeBind {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing bind mount for /credentials")
	}
}

func TestBuildContainerConfig_MCPSocketPresent(t *testing.T) {
	sockFile := filepath.Join(t.TempDir(), "mcp.sock")
	if err := os.WriteFile(sockFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		MCP:   MCPConfig{SocketPath: sockFile},
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	found := false
	for _, m := range hostCfg.Mounts {
		if m.Target == "/run/mcp.sock" && m.Source == sockFile && m.ReadOnly {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing MCP socket mount at /run/mcp.sock")
	}
}

func TestBuildContainerConfig_MCPSocketPathEmpty_NoMount(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		MCP:   MCPConfig{SocketPath: ""},
	}, t.TempDir(), nil)
	_, hostCfg := d.buildContainerConfig()

	for _, m := range hostCfg.Mounts {
		if m.Target == "/run/mcp.sock" {
			t.Error("should not have MCP socket mount when SocketPath is empty")
		}
	}
}

func TestBuildContainerConfig_Labels(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), nil)
	containerCfg, _ := d.buildContainerConfig()

	if containerCfg.Labels[LabelOwner] != LabelOwnerValue {
		t.Errorf("label %s = %q, want %q", LabelOwner, containerCfg.Labels[LabelOwner], LabelOwnerValue)
	}
	if containerCfg.Labels[LabelSession] != d.id {
		t.Errorf("label %s = %q, want %q", LabelSession, containerCfg.Labels[LabelSession], d.id)
	}
}

func TestBuildContainerConfig_EnvironmentVars(t *testing.T) {
	d := NewDockerSandbox(Config{Image: "orqestra-sandbox:test"}, t.TempDir(), []string{
		"CLAUDE_API_KEY=secret",
		"ORQESTRA_SESSION=abc123",
	})
	containerCfg, _ := d.buildContainerConfig()

	envContains := func(want string) bool {
		for _, e := range containerCfg.Env {
			if e == want {
				return true
			}
		}
		return false
	}
	if !envContains("CLAUDE_API_KEY=secret") {
		t.Error("missing CLAUDE_API_KEY in container env")
	}
	if !envContains("ORQESTRA_SESSION=abc123") {
		t.Error("missing ORQESTRA_SESSION in container env")
	}
}

// --- Provision MCP validation ---

func TestProvision_MCPSocketMissing_ReturnsError(t *testing.T) {
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:test",
		MCP:   MCPConfig{SocketPath: "/nonexistent/mcp.sock"},
	}, t.TempDir(), nil)

	err := d.Provision(context.Background())
	if err == nil {
		t.Fatal("expected error when MCP socket path is missing")
	}
	if got := err.Error(); !containsSubstring([]string{got}, "MCP socket path") {
		t.Errorf("error %q should mention MCP socket path", got)
	}
}

// --- Client creation error ---

func TestNewDockerClient_CanCreate(t *testing.T) {
	// This test verifies that the client creation path works.
	// It does NOT require Docker to be running — only that the client struct is created.
	cli, err := newDockerClient()
	if err != nil {
		t.Fatalf("newDockerClient() error: %v", err)
	}
	if cli == nil {
		t.Fatal("newDockerClient() returned nil client")
	}
	cli.Close()
}

// --- parseMemoryBytes ---

func TestParseMemoryBytes(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"4g", 4 * 1024 * 1024 * 1024},
		{"512m", 512 * 1024 * 1024},
		{"1024k", 1024 * 1024},
		{"", 0},
		{"2G", 2 * 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseMemoryBytes(tt.input)
			if got != tt.want {
				t.Errorf("parseMemoryBytes(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
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
	listFn         func(ctx context.Context) ([]TrackedContainer, error)
	killFn         func(ctx context.Context, id string) error
	listOrphanedFn func(ctx context.Context) ([]string, error)
	removeImageFn  func(ctx context.Context, imageRef string) error
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

func (m *funcTracker) ListOrphanedImages(ctx context.Context) ([]string, error) {
	if m.listOrphanedFn != nil {
		return m.listOrphanedFn(ctx)
	}
	return nil, nil
}

func (m *funcTracker) RemoveImage(ctx context.Context, imageRef string) error {
	if m.removeImageFn != nil {
		return m.removeImageFn(ctx, imageRef)
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

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation within 2s")
	}
	if sweepCount == 0 {
		t.Error("expected at least one sweep before cancellation")
	}
}

func TestReaper_Run_FinalCleanupOnCancel(t *testing.T) {
	cleanupCalled := false
	tracker := &funcTracker{
		listFn: func(_ context.Context) ([]TrackedContainer, error) {
			return []TrackedContainer{{ID: "orphan-1", CreatedAt: time.Now()}}, nil
		},
		killFn: func(_ context.Context, id string) error {
			if id == "orphan-1" {
				cleanupCalled = true
			}
			return nil
		},
	}
	r := NewReaper(tracker, 10*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, time.Hour)
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
		{"clean relative", "src/main.go", false},
		{"dot only", ".", false},
		{"double dot", "..", true},
		{"embedded traversal stays in workspace", "src/../etc/passwd", false},
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
