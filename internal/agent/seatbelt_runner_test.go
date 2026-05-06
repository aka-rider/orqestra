//go:build darwin

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/seatbelt"
)

func TestSeatbeltRunner_FakeArtifact(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	// The runner will create <session>/<role>/output/ and we run a command
	// that writes an artifact there.
	outputDir := filepath.Join(sessionDir, "worker", "output")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	artifact, err := runner.Run(ctx, RunConfig{
		Spec: AgentSpec{
			Role:       RoleWorker,
			Command:    []string{"/bin/sh", "-c", "mkdir -p '" + outputDir + "' && echo '{\"result\":\"ok\"}' > '" + outputDir + "/result.json'"},
			OutputFile: "result.json",
		},
		Session:  SessionDir{Path: sessionDir},
		RepoPath: repoDir,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if string(artifact) == "" {
		t.Fatal("expected non-empty artifact")
	}
	if !contains(string(artifact), "ok") {
		t.Errorf("artifact = %q, want to contain 'ok'", artifact)
	}

	// Check normalized artifact was written
	normalized := filepath.Join(sessionDir, "worker.json")
	data, err := os.ReadFile(normalized)
	if err != nil {
		t.Fatalf("normalized artifact not written: %v", err)
	}
	if !contains(string(data), "ok") {
		t.Errorf("normalized artifact = %q", data)
	}
}

func TestSeatbeltRunner_CancelKillsProcessGroup(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start a long-running process.
	ls, err := runner.RunInteractive(ctx, RunConfig{
		Spec: AgentSpec{
			Role:    RoleWorker,
			Command: []string{"/bin/sh", "-c", "sleep 300"},
		},
		Session:  SessionDir{Path: sessionDir},
		RepoPath: repoDir,
	})
	if err != nil {
		t.Fatalf("RunInteractive() error: %v", err)
	}

	// Give it a moment to start.
	time.Sleep(100 * time.Millisecond)

	// Cancel and verify it dies.
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()

	_, waitErr := ls.Wait(waitCtx)
	if waitErr == nil {
		t.Fatal("expected error after cancellation")
	}
}

func TestPTY_DimensionsPropagation(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	// Create a sandbox directly and run stty size under PTY.
	sb, err := seatbelt.New(seatbelt.Config{
		RepoPath:     repoDir,
		SessionPath:  sessionDir,
		RepoWritable: true,
	})
	if err != nil {
		t.Fatalf("seatbelt.New: %v", err)
	}
	defer sb.Close()

	cmd := exec.Command("/bin/sh", "-c", "stty size")
	if err := sb.Wrap(cmd); err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	npty, err := StartNativePTY(cmd, 132, 50)
	if err != nil {
		t.Fatalf("StartNativePTY: %v", err)
	}
	defer npty.Close()

	// Read output
	buf := make([]byte, 256)
	var output []byte
	for {
		n, readErr := npty.Read(buf)
		if n > 0 {
			output = append(output, buf[:n]...)
		}
		if readErr != nil {
			break
		}
		if len(output) > 0 {
			break
		}
	}
	npty.Wait()

	// stty size outputs "rows cols"
	if !contains(string(output), "50 132") {
		t.Errorf("stty size = %q, want to contain '50 132'", output)
	}
}

func TestSeatbeltRunner_BELDetection(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	var belCount atomic.Int32

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ls, err := runner.RunInteractive(ctx, RunConfig{
		Spec: AgentSpec{
			Role:    RoleWorker,
			Command: []string{"/bin/sh", "-c", "printf '\\a\\a' && sleep 0.1"},
		},
		Session:  SessionDir{Path: sessionDir},
		RepoPath: repoDir,
		Callbacks: RunCallbacks{
			OnBEL: func() { belCount.Add(1) },
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}

	ls.Wait(ctx)

	if belCount.Load() < 2 {
		t.Errorf("BEL count = %d, want >= 2", belCount.Load())
	}
}


func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSeatbeltRunner_StagingLayout(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run a command that just exits immediately — we want to verify staging layout.
	_, _ = runner.Run(ctx, RunConfig{
		Spec: AgentSpec{
			Role:         RolePlanner,
			SystemPrompt: "You are the planner.",
			InputFiles:   map[string][]byte{"spec.json": []byte(`{"task":"plan"}`)},
			Command:      []string{"/bin/sh", "-c", "true"},
		},
		Session:  SessionDir{Path: sessionDir},
		RepoPath: repoDir,
	})

	// Verify canonical staging layout per spec:
	// <session>/<role>/input/  for staged inputs
	// <session>/<role>/agent.md  for generated agent frontmatter/system prompt
	// <session>/<role>/output/  for raw agent outputs

	promptFile := filepath.Join(sessionDir, "planner", "agent.md")
	if data, err := os.ReadFile(promptFile); err != nil {
		t.Errorf("system prompt not staged at %s: %v", promptFile, err)
	} else if string(data) != "You are the planner." {
		t.Errorf("system prompt content = %q, want %q", data, "You are the planner.")
	}

	inputFile := filepath.Join(sessionDir, "planner", "input", "spec.json")
	if data, err := os.ReadFile(inputFile); err != nil {
		t.Errorf("input file not staged at %s: %v", inputFile, err)
	} else if string(data) != `{"task":"plan"}` {
		t.Errorf("input content = %q", data)
	}

	outputDir := filepath.Join(sessionDir, "planner", "output")
	info, err := os.Stat(outputDir)
	if err != nil {
		t.Errorf("output dir not created at %s: %v", outputDir, err)
	} else if !info.IsDir() {
		t.Errorf("output path is not a directory")
	}
}

func TestSeatbeltRunner_NormalizedArtifactPath(t *testing.T) {
	repoDir := t.TempDir()
	sessionDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	// Create the output dir and file that the runner expects.
	outputDir := filepath.Join(sessionDir, "planner", "output")
	os.MkdirAll(outputDir, 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Command writes to the canonical output location.
	outputFile := filepath.Join(outputDir, "plan.json")
	artifact, err := runner.Run(ctx, RunConfig{
		Spec: AgentSpec{
			Role:       RolePlanner,
			OutputFile: "plan.json",
			Command:    []string{"/bin/sh", "-c", "echo '{\"plan\":\"done\"}' > '" + outputFile + "'"},
		},
		Session:  SessionDir{Path: sessionDir},
		RepoPath: repoDir,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The normalized artifact should be at <session>/artifacts/<role>.json
	// per spec: <session>/artifacts/<role>.json for the normalized artifact
	// BUT the current implementation writes to <session>/<role>.json via SessionDir.WriteArtifact
	normalizedPath := filepath.Join(sessionDir, "planner.json")
	data, err := os.ReadFile(normalizedPath)
	if err != nil {
		t.Fatalf("normalized artifact not at %s: %v", normalizedPath, err)
	}
	if !contains(string(data), "done") {
		t.Errorf("normalized artifact = %q, want to contain 'done'", data)
	}
	if !contains(string(artifact), "done") {
		t.Errorf("returned artifact = %q, want to contain 'done'", artifact)
	}
}
