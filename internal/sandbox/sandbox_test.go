package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifyFiles_RejectsUnexpectedExecutable(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		AllowedExecutables: []string{"scripts/*.sh"},
		MaxFileSize:        10 * 1024 * 1024,
	})

	files := []ChangedFile{
		{Path: "main.go", Op: FileModified, Size: 1024, IsExecutable: false},
		{Path: "backdoor", Op: FileAdded, Size: 512, IsExecutable: true},
	}

	result := v.Verify(files)
	if result.Passed {
		t.Fatal("expected verification to fail for unexpected executable")
	}
	if len(result.Rejected) != 1 {
		t.Fatalf("expected 1 rejected file, got %d", len(result.Rejected))
	}
	if result.Rejected[0].Path != "backdoor" {
		t.Errorf("expected rejected file 'backdoor', got %q", result.Rejected[0].Path)
	}
}

func TestVerifyFiles_AllowsWhitelistedExecutable(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		AllowedExecutables: []string{"scripts/*.sh"},
		MaxFileSize:        10 * 1024 * 1024,
	})

	files := []ChangedFile{
		{Path: "scripts/run-tests.sh", Op: FileAdded, Size: 256, IsExecutable: true},
	}

	result := v.Verify(files)
	if !result.Passed {
		t.Fatalf("expected verification to pass for whitelisted executable, rejected: %v", result.Rejected)
	}
}

func TestVerifyFiles_RejectsDangerousExtensions(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		MaxFileSize: 10 * 1024 * 1024,
	})

	tests := []struct {
		name string
		path string
	}{
		{"shared object", "lib/hack.so"},
		{"dynamic lib", "lib/hack.dylib"},
		{"DLL", "lib/hack.dll"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := []ChangedFile{
				{Path: tt.path, Op: FileAdded, Size: 1024, IsExecutable: false},
			}
			result := v.Verify(files)
			if result.Passed {
				t.Fatalf("expected rejection for %q", tt.path)
			}
		})
	}
}

func TestVerifyFiles_RejectsOversizedFiles(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		MaxFileSize: 1024, // 1KB limit
	})

	files := []ChangedFile{
		{Path: "huge.bin", Op: FileAdded, Size: 2048},
	}

	result := v.Verify(files)
	if result.Passed {
		t.Fatal("expected rejection for oversized file")
	}
	if result.Rejected[0].Reason != ReasonOversized {
		t.Errorf("expected reason %q, got %q", ReasonOversized, result.Rejected[0].Reason)
	}
}

func TestVerifyFiles_PassesCleanFiles(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		MaxFileSize: 10 * 1024 * 1024,
	})

	files := []ChangedFile{
		{Path: "main.go", Op: FileModified, Size: 4096},
		{Path: "README.md", Op: FileAdded, Size: 512},
		{Path: "internal/foo/bar.go", Op: FileModified, Size: 2048},
	}

	result := v.Verify(files)
	if !result.Passed {
		t.Fatalf("expected pass for clean files, rejected: %v", result.Rejected)
	}
}

func TestVerifyFiles_EmptyChangeset(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		MaxFileSize: 10 * 1024 * 1024,
	})

	result := v.Verify(nil)
	if !result.Passed {
		t.Fatal("expected pass for empty changeset")
	}
}

func TestVerifyFiles_PathTraversalRejected(t *testing.T) {
	v := NewVerifier(VerifierConfig{
		MaxFileSize: 10 * 1024 * 1024,
	})

	files := []ChangedFile{
		{Path: "../../../etc/passwd", Op: FileModified, Size: 100},
	}

	result := v.Verify(files)
	if result.Passed {
		t.Fatal("expected rejection for path traversal")
	}
	if result.Rejected[0].Reason != ReasonPathTraversal {
		t.Errorf("expected reason %q, got %q", ReasonPathTraversal, result.Rejected[0].Reason)
	}
}

func TestReaper_KillsExpiredContainers(t *testing.T) {
	tracker := &mockTracker{
		containers: []TrackedContainer{
			{
				ID:        "abc123",
				Labels:    map[string]string{LabelOwner: LabelOwnerValue},
				CreatedAt: time.Now().Add(-2 * time.Hour),
			},
			{
				ID:        "def456",
				Labels:    map[string]string{LabelOwner: LabelOwnerValue},
				CreatedAt: time.Now(), // fresh
			},
		},
	}

	r := NewReaper(tracker, 1*time.Hour)
	killed := r.Sweep(context.Background())

	if len(killed) != 1 {
		t.Fatalf("expected 1 killed container, got %d", len(killed))
	}
	if killed[0] != "abc123" {
		t.Errorf("expected killed container 'abc123', got %q", killed[0])
	}
	if !tracker.killCalled["abc123"] {
		t.Error("expected Kill to be called for abc123")
	}
}

func TestReaper_IgnoresFreshContainers(t *testing.T) {
	tracker := &mockTracker{
		containers: []TrackedContainer{
			{
				ID:        "fresh1",
				Labels:    map[string]string{LabelOwner: LabelOwnerValue},
				CreatedAt: time.Now(),
			},
		},
	}

	r := NewReaper(tracker, 1*time.Hour)
	killed := r.Sweep(context.Background())

	if len(killed) != 0 {
		t.Fatalf("expected 0 killed containers, got %d", len(killed))
	}
}

func TestReaper_CleanupAll(t *testing.T) {
	tracker := &mockTracker{
		containers: []TrackedContainer{
			{ID: "a", Labels: map[string]string{LabelOwner: LabelOwnerValue}, CreatedAt: time.Now()},
			{ID: "b", Labels: map[string]string{LabelOwner: LabelOwnerValue}, CreatedAt: time.Now()},
		},
	}

	r := NewReaper(tracker, 1*time.Hour)
	r.CleanupAll(context.Background())

	if !tracker.killCalled["a"] || !tracker.killCalled["b"] {
		t.Error("expected all containers to be killed on CleanupAll")
	}
}

func TestSandboxStateTransitions(t *testing.T) {
	// Verify the state machine string representations
	tests := []struct {
		state State
		want  string
	}{
		{StatePending, "pending"},
		{StateProvisioning, "provisioning"},
		{StateReady, "ready"},
		{StateRunning, "running"},
		{StateStopped, "stopped"},
		{StateExtracting, "extracting"},
		{StateDestroyed, "destroyed"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestSandboxState_Terminal(t *testing.T) {
	if !StateDestroyed.Terminal() {
		t.Error("StateDestroyed should be terminal")
	}
	if StateRunning.Terminal() {
		t.Error("StateRunning should not be terminal")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Image != "orqestra-sandbox:latest" {
		t.Errorf("unexpected default image: %q", cfg.Image)
	}
	if cfg.MaxLifetime != 50*time.Minute {
		t.Errorf("unexpected default max lifetime: %v", cfg.MaxLifetime)
	}
	if cfg.MCP.SocketPath == "" {
		t.Error("MCP socket path should have a default")
	}
}

// TestCopyOutStaging verifies files are written to a staging directory.
func TestCopyOutStaging(t *testing.T) {
	staging := t.TempDir()
	dest := filepath.Join(staging, "subdir", "file.txt")

	content := []byte("hello from sandbox")
	if err := stageCopy(content, dest); err != nil {
		t.Fatalf("stageCopy error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading staged file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("staged content = %q, want %q", got, content)
	}
}

// mockTracker implements ContainerTracker for testing the reaper.
type mockTracker struct {
	containers []TrackedContainer
	killCalled map[string]bool
}

func (m *mockTracker) ListOrqestraContainers(_ context.Context) ([]TrackedContainer, error) {
	return m.containers, nil
}

func (m *mockTracker) KillAndRemove(_ context.Context, id string) error {
	if m.killCalled == nil {
		m.killCalled = make(map[string]bool)
	}
	m.killCalled[id] = true
	return nil
}

func (m *mockTracker) ListOrphanedImages(_ context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockTracker) RemoveImage(_ context.Context, _ string) error {
	return nil
}
