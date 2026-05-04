//go:build integration

package intent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// TestIntakeRunner_E2E verifies the full intake runner lifecycle:
// provision sandbox → stage inputs → run PTY agent → extract artifact.
//
// Requirements:
// - Docker daemon running
// - orqestra-sandbox:latest image available
// - Claude CLI installed in the sandbox image
// - Network connectivity to the configured LLM endpoint
//
// Run with: go test ./internal/intent/ -run TestIntakeRunner_E2E -tags integration -timeout 5m
func TestIntakeRunner_E2E(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved := config.ResolvedModel{
		BaseURL: envOrDefault("TEST_LLM_BASE_URL", "http://192.168.50.212:11434"),
		Model:   envOrDefault("TEST_LLM_MODEL", "qwen3:8b"),
		Type:    "openai",
	}

	sb := sandbox.NewDockerSandbox(sandbox.Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := sb.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer sb.Destroy(context.Background())

	sess := sandbox.Session{
		Name:      "test-intake",
		StartedAt: time.Now(),
	}

	runner := NewIntakeRunner(sb, resolved, nil)

	// Collect callbacks from the runner.
	var mu sync.Mutex
	var outputChunks int
	var doneExitCode int
	var gotDone bool

	cb := IntakeCallbacks{
		OnOutput: func(data []byte) {
			mu.Lock()
			outputChunks++
			mu.Unlock()
		},
		OnDone: func(exitCode int) {
			mu.Lock()
			doneExitCode = exitCode
			gotDone = true
			mu.Unlock()
		},
	}

	artifact, err := runner.Execute(ctx, sess, "Build a simple REST API with a /health endpoint that returns 200 OK", cb)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Assert: PTY output was received.
	mu.Lock()
	outputs := outputChunks
	done := gotDone
	exitCode := doneExitCode
	mu.Unlock()

	if outputs == 0 {
		t.Error("expected PTY output chunks, got none")
	}

	// Assert: PTY done was received.
	if !done {
		t.Error("expected OnDone callback, not called")
	} else if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert: artifact was extracted.
	if len(artifact) == 0 {
		t.Error("expected non-empty artifact, got empty")
	}

	t.Logf("intake runner completed: %d PTY output chunks, artifact size=%d bytes", outputs, len(artifact))
}

// dockerTempDir creates a temp directory accessible to Docker Desktop.
func dockerTempDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	base := filepath.Join(wd, ".tmp-integration-test")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("creating docker-accessible temp base: %v", err)
	}
	dir, err := os.MkdirTemp(base, t.Name()+"-*")
	if err != nil {
		t.Fatalf("creating docker-accessible temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
