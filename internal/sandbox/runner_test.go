package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockSandbox implements Sandbox for unit testing without Docker.
type mockSandbox struct {
	id          string
	provisionFn func(ctx context.Context) error
	execFn      func(ctx context.Context, command []string, env []string, stdout io.Writer) (int, error)
	extractFn   func(ctx context.Context) ([]ChangedFile, error)
	copyOutFn   func(ctx context.Context, sandboxPath, hostPath string) error
	destroyFn   func(ctx context.Context) error
}

func (m *mockSandbox) ID() string    { return m.id }
func (m *mockSandbox) State() State  { return StatePending }
func (m *mockSandbox) Info() Info    { return Info{ID: m.id} }

func (m *mockSandbox) Provision(ctx context.Context) error {
	if m.provisionFn != nil {
		return m.provisionFn(ctx)
	}
	return nil
}

func (m *mockSandbox) Exec(ctx context.Context, command []string, env []string, stdout io.Writer) (int, error) {
	if m.execFn != nil {
		return m.execFn(ctx, command, env, stdout)
	}
	return 0, nil
}

func (m *mockSandbox) ExtractChanges(ctx context.Context) ([]ChangedFile, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx)
	}
	return nil, nil
}

func (m *mockSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	if m.copyOutFn != nil {
		return m.copyOutFn(ctx, sandboxPath, hostPath)
	}
	return nil
}

func (m *mockSandbox) Destroy(ctx context.Context) error {
	if m.destroyFn != nil {
		return m.destroyFn(ctx)
	}
	return nil
}

// newRunnerWithMock wires a RunnerConfig to always return the given mockSandbox.
func newRunnerWithMock(cfg RunnerConfig, mock *mockSandbox) *SandboxedCLIRunner {
	cfg.SandboxFactory = func(_ Config, _ string, _ []string) Sandbox {
		return mock
	}
	return NewSandboxedCLIRunner(cfg)
}

// mustContain fails the test if want is not in args.
func mustContain(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}

// containsStr returns true if any element equals s.
func containsStr(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// mustContainState fails the test if want is not in states.
func mustContainState(t *testing.T, states []State, want State) {
	t.Helper()
	for _, s := range states {
		if s == want {
			return
		}
	}
	t.Errorf("states %v does not contain %v", states, want)
}

// --- buildCommand ---

func TestBuildCommand_PrintMode(t *testing.T) {
	r := NewSandboxedCLIRunner(RunnerConfig{Model: "claude-opus-4-5"})
	args := r.buildCommand("hello world", "sys", false)

	if args[0] != "claude" {
		t.Errorf("args[0] = %q, want %q", args[0], "claude")
	}
	mustContain(t, args, "--print")
	mustContain(t, args, "hello world")
	mustContain(t, args, "--model")
	mustContain(t, args, "claude-opus-4-5")
	mustContain(t, args, "--dangerously-skip-permissions")

	// Print mode must not use streaming format.
	if containsStr(args, "stream-json") {
		t.Error("print mode should not include stream-json")
	}
	if containsStr(args, "--verbose") {
		t.Error("print mode should not include --verbose")
	}
}

func TestBuildCommand_StreamingMode(t *testing.T) {
	r := NewSandboxedCLIRunner(RunnerConfig{})
	args := r.buildCommand("hi", "", true)

	mustContain(t, args, "--output-format")
	mustContain(t, args, "stream-json")
	mustContain(t, args, "--verbose")

	// Streaming mode must not use --print.
	if containsStr(args, "--print") {
		t.Error("streaming mode should not include --print")
	}
}

func TestBuildCommand_NoModelFlag(t *testing.T) {
	r := NewSandboxedCLIRunner(RunnerConfig{})
	args := r.buildCommand("prompt", "sys", false)

	if containsStr(args, "--model") {
		t.Error("expected no --model flag when RunnerConfig.Model is empty")
	}
}

func TestBuildCommand_SystemPromptInjected(t *testing.T) {
	r := NewSandboxedCLIRunner(RunnerConfig{})
	args := r.buildCommand("prompt", "MY-SENTINEL-SYS", false)

	mustContain(t, args, "--system-prompt")
	for i, a := range args {
		if a == "--system-prompt" && i+1 < len(args) {
			if !strings.Contains(args[i+1], "MY-SENTINEL-SYS") {
				t.Errorf("--system-prompt value %q does not contain custom text", args[i+1])
			}
			if !strings.Contains(args[i+1], sandboxSystemPrompt[:20]) {
				t.Errorf("--system-prompt value %q missing sandbox context preamble", args[i+1])
			}
			return
		}
	}
	t.Error("--system-prompt not found in args")
}

// --- applyChanges ---

func TestApplyChanges_CopiesAddedFile(t *testing.T) {
	repoDir := t.TempDir()
	stagingDir := t.TempDir()
	content := []byte("package main\n")

	mock := &mockSandbox{
		id: "sb-add",
		copyOutFn: func(_ context.Context, _, hostPath string) error {
			return os.WriteFile(hostPath, content, 0o644)
		},
	}
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir, StagingDir: stagingDir})

	changes := []ChangedFile{{Path: "src/main.go", Op: FileAdded}}
	if err := r.applyChanges(context.Background(), mock, changes); err != nil {
		t.Fatalf("applyChanges() error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(repoDir, "src/main.go"))
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("file content = %q, want %q", got, content)
	}
}

func TestApplyChanges_ModifiesExistingFile(t *testing.T) {
	repoDir := t.TempDir()
	stagingDir := t.TempDir()

	hostPath := filepath.Join(repoDir, "main.go")
	os.WriteFile(hostPath, []byte("old"), 0o644)

	newContent := []byte("new content")
	mock := &mockSandbox{
		id: "sb-mod",
		copyOutFn: func(_ context.Context, _, hp string) error {
			return os.WriteFile(hp, newContent, 0o644)
		},
	}
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir, StagingDir: stagingDir})

	changes := []ChangedFile{{Path: "main.go", Op: FileModified}}
	if err := r.applyChanges(context.Background(), mock, changes); err != nil {
		t.Fatalf("applyChanges() error: %v", err)
	}

	got, _ := os.ReadFile(hostPath)
	if !bytes.Equal(got, newContent) {
		t.Errorf("got %q, want %q", got, newContent)
	}
}

func TestApplyChanges_DeletesFile(t *testing.T) {
	repoDir := t.TempDir()
	hostPath := filepath.Join(repoDir, "obsolete.go")
	os.WriteFile(hostPath, []byte("old"), 0o644)

	mock := &mockSandbox{id: "sb-del"}
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir})

	changes := []ChangedFile{{Path: "obsolete.go", Op: FileDeleted}}
	if err := r.applyChanges(context.Background(), mock, changes); err != nil {
		t.Fatalf("applyChanges() error: %v", err)
	}
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestApplyChanges_DeleteNonExistent_NoError(t *testing.T) {
	repoDir := t.TempDir()
	mock := &mockSandbox{id: "sb-del-missing"}
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir})

	changes := []ChangedFile{{Path: "ghost.go", Op: FileDeleted}}
	if err := r.applyChanges(context.Background(), mock, changes); err != nil {
		t.Fatalf("applyChanges() should not error when deleting a non-existent file, got: %v", err)
	}
}

func TestApplyChanges_StagingIsolatesToRepo(t *testing.T) {
	// Verify two-phase staging: if CopyOut fails for the 2nd file, the 1st
	// must NOT appear in the repo (staging phase fails before the apply phase).
	repoDir := t.TempDir()
	stagingDir := t.TempDir()

	errSimulated := errors.New("simulated copy failure")
	call := 0
	mock := &mockSandbox{
		id: "sb-stage",
		copyOutFn: func(_ context.Context, _, hostPath string) error {
			call++
			if call == 2 {
				return errSimulated
			}
			return os.WriteFile(hostPath, []byte("ok"), 0o644)
		},
	}
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir, StagingDir: stagingDir})

	changes := []ChangedFile{
		{Path: "file1.go", Op: FileAdded},
		{Path: "file2.go", Op: FileAdded},
	}
	err := r.applyChanges(context.Background(), mock, changes)
	if !errors.Is(err, errSimulated) {
		t.Fatalf("expected errSimulated, got %v", err)
	}

	// file1.go must NOT exist in the repo — staging failed before the apply phase.
	if _, err := os.Stat(filepath.Join(repoDir, "file1.go")); !os.IsNotExist(err) {
		t.Error("file1.go must not reach repo when staging phase fails")
	}
}

func TestApplyChanges_TempStagingUsedWhenNotConfigured(t *testing.T) {
	repoDir := t.TempDir()
	content := []byte("auto-staging test")

	mock := &mockSandbox{
		id: "sb-tmpstage",
		copyOutFn: func(_ context.Context, _, hostPath string) error {
			return os.WriteFile(hostPath, content, 0o644)
		},
	}
	// StagingDir deliberately left empty — must auto-create a temp dir.
	r := NewSandboxedCLIRunner(RunnerConfig{RepoPath: repoDir})

	changes := []ChangedFile{{Path: "auto.go", Op: FileAdded}}
	if err := r.applyChanges(context.Background(), mock, changes); err != nil {
		t.Fatalf("applyChanges() error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(repoDir, "auto.go"))
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// --- runInSandbox orchestration ---

func TestRunInSandbox_FullLifecycle(t *testing.T) {
	repoDir := t.TempDir()
	stagingDir := t.TempDir()
	var states []State

	mock := &mockSandbox{
		id: "lifecycle-sb",
		execFn: func(_ context.Context, _ []string, _ []string, stdout io.Writer) (int, error) {
			stdout.Write([]byte("agent output"))
			return 0, nil
		},
		extractFn: func(_ context.Context) ([]ChangedFile, error) {
			return []ChangedFile{{Path: "result.go", Op: FileAdded}}, nil
		},
		copyOutFn: func(_ context.Context, _, hostPath string) error {
			return os.WriteFile(hostPath, []byte("// generated"), 0o644)
		},
	}

	r := newRunnerWithMock(RunnerConfig{
		RepoPath:   repoDir,
		StagingDir: stagingDir,
		OnState: func(_ string, s State) {
			states = append(states, s)
		},
	}, mock)

	out, changes, err := r.runInSandbox(context.Background(), []string{"claude", "--print"}, nil)
	if err != nil {
		t.Fatalf("runInSandbox() error: %v", err)
	}
	if out != "agent output" {
		t.Errorf("output = %q, want %q", out, "agent output")
	}
	if len(changes) != 1 {
		t.Errorf("changes = %d, want 1", len(changes))
	}

	mustContainState(t, states, StateDestroyed)
}

func TestRunInSandbox_DestroyOnProvisionFailure(t *testing.T) {
	destroyed := false
	mock := &mockSandbox{
		id: "prov-fail-sb",
		provisionFn: func(_ context.Context) error {
			return errors.New("provision failed")
		},
		destroyFn: func(_ context.Context) error {
			destroyed = true
			return nil
		},
	}

	r := newRunnerWithMock(RunnerConfig{}, mock)
	_, _, err := r.runInSandbox(context.Background(), []string{"claude"}, nil)
	if err == nil {
		t.Fatal("expected error from provision failure")
	}
	if !destroyed {
		t.Error("sandbox must be destroyed even when provisioning fails")
	}
}

func TestRunInSandbox_SecurityRejectionHaltsApply(t *testing.T) {
	repoDir := t.TempDir()
	copyOutCalled := false

	mock := &mockSandbox{
		id: "sec-sb",
		execFn: func(_ context.Context, _ []string, _ []string, _ io.Writer) (int, error) {
			return 0, nil
		},
		extractFn: func(_ context.Context) ([]ChangedFile, error) {
			return []ChangedFile{
				{Path: "backdoor", Op: FileAdded, IsExecutable: true},
			}, nil
		},
		copyOutFn: func(_ context.Context, _, _ string) error {
			copyOutCalled = true
			return nil
		},
	}

	r := newRunnerWithMock(RunnerConfig{
		RepoPath: repoDir,
		Sandbox:  Config{AllowedExecutables: nil},
	}, mock)

	_, _, err := r.runInSandbox(context.Background(), []string{"claude"}, nil)
	if err == nil {
		t.Fatal("expected security rejection error")
	}
	if copyOutCalled {
		t.Error("CopyOut must not be called when security verification fails")
	}
}

func TestRunInSandbox_NoChanges_NoApply(t *testing.T) {
	repoDir := t.TempDir()
	copyOutCalled := false

	mock := &mockSandbox{
		id: "nochange-sb",
		execFn: func(_ context.Context, _ []string, _ []string, stdout io.Writer) (int, error) {
			stdout.Write([]byte("done"))
			return 0, nil
		},
		extractFn: func(_ context.Context) ([]ChangedFile, error) {
			return nil, nil // no changes
		},
		copyOutFn: func(_ context.Context, _, _ string) error {
			copyOutCalled = true
			return nil
		},
	}

	r := newRunnerWithMock(RunnerConfig{RepoPath: repoDir}, mock)
	out, changes, err := r.runInSandbox(context.Background(), []string{"claude"}, nil)
	if err != nil {
		t.Fatalf("runInSandbox() error: %v", err)
	}
	if out != "done" {
		t.Errorf("output = %q, want %q", out, "done")
	}
	if len(changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(changes))
	}
	if copyOutCalled {
		t.Error("CopyOut must not be called when there are no changes")
	}
}

func TestRunInSandbox_ContextCancellation(t *testing.T) {
	destroyed := false
	ctx, cancel := context.WithCancel(context.Background())

	mock := &mockSandbox{
		id: "cancel-sb",
		execFn: func(ctx context.Context, _ []string, _ []string, _ io.Writer) (int, error) {
			cancel() // cancel during exec
			return 0, ctx.Err()
		},
		destroyFn: func(_ context.Context) error {
			destroyed = true
			return nil
		},
	}

	r := newRunnerWithMock(RunnerConfig{}, mock)
	_, _, err := r.runInSandbox(ctx, []string{"claude"}, nil)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if !destroyed {
		t.Error("sandbox must be destroyed after context cancellation")
	}
}

func TestRunInSandbox_StreamingPassthrough(t *testing.T) {
	repoDir := t.TempDir()
	mock := &mockSandbox{
		id: "stream-sb",
		execFn: func(_ context.Context, _ []string, _ []string, stdout io.Writer) (int, error) {
			stdout.Write([]byte("streamed line\n"))
			return 0, nil
		},
		extractFn: func(_ context.Context) ([]ChangedFile, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	r := newRunnerWithMock(RunnerConfig{RepoPath: repoDir}, mock)
	out, _, err := r.runInSandbox(context.Background(), []string{"claude"}, &buf)
	if err != nil {
		t.Fatalf("runInSandbox() error: %v", err)
	}
	if out != "streamed line\n" {
		t.Errorf("captured output = %q, want %q", out, "streamed line\n")
	}
	if buf.String() != "streamed line\n" {
		t.Errorf("passthrough buf = %q, want %q", buf.String(), "streamed line\n")
	}
}

// --- emitState ---

func TestEmitState_NilCallback(t *testing.T) {
	r := NewSandboxedCLIRunner(RunnerConfig{}) // no OnState
	// Must not panic.
	r.emitState("id", StateRunning)
}

func TestEmitState_CallbackInvoked(t *testing.T) {
	var got []State
	r := NewSandboxedCLIRunner(RunnerConfig{
		OnState: func(_ string, s State) {
			got = append(got, s)
		},
	})
	r.emitState("x", StateRunning)
	r.emitState("x", StateDestroyed)

	if len(got) != 2 || got[0] != StateRunning || got[1] != StateDestroyed {
		t.Errorf("got states %v, want [Running Destroyed]", got)
	}
}
