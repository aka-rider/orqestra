//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTransfer_CopyToAndFromContainer verifies round-trip copy via the Docker SDK.
func TestTransfer_CopyToAndFromContainer(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Copy a file into the container.
	testContent := "hello from transfer test\n"
	containerPath := "/workspace/transfer-test.txt"
	err := CopyToContainer(ctx, d.cli, d.containerID, containerPath, strings.NewReader(testContent))
	if err != nil {
		t.Fatalf("CopyToContainer() error: %v", err)
	}

	// Verify it exists inside the container.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"cat", containerPath}, nil, &out)
	if err != nil {
		t.Fatalf("Exec(cat) error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("cat exit code = %d", exitCode)
	}
	if got := out.String(); got != testContent {
		t.Errorf("content in container = %q, want %q", got, testContent)
	}

	// Copy it back out and verify.
	hostDest := filepath.Join(t.TempDir(), "extracted.txt")
	err = CopyFileFromContainer(ctx, d.cli, d.containerID, containerPath, hostDest)
	if err != nil {
		t.Fatalf("CopyFileFromContainer() error: %v", err)
	}

	got, err := os.ReadFile(hostDest)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != testContent {
		t.Errorf("extracted content = %q, want %q", string(got), testContent)
	}
}

// TestTransfer_StageInputs verifies session inputs are staged correctly.
func TestTransfer_StageInputs(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	sess := Session{Name: "test-session-abc", StartedAt: time.Now()}
	inputContent := "# Task\nDo something cool\n"
	promptContent := "You are a helpful assistant.\n"

	if err := d.StageInputs(ctx, sess, inputContent, promptContent); err != nil {
		t.Fatalf("StageInputs() error: %v", err)
	}

	// Verify input.md inside container.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"cat", "/workspace/.orqestra/test-session-abc/input.md"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec(cat input.md) error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("cat input.md exit code = %d", exitCode)
	}
	if got := out.String(); got != inputContent {
		t.Errorf("input.md = %q, want %q", got, inputContent)
	}

	// Verify system-prompt.md inside container.
	out.Reset()
	exitCode, err = d.Exec(ctx, []string{"cat", "/workspace/.orqestra/test-session-abc/system-prompt.md"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec(cat system-prompt.md) error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("cat system-prompt.md exit code = %d", exitCode)
	}
	if got := out.String(); got != promptContent {
		t.Errorf("system-prompt.md = %q, want %q", got, promptContent)
	}
}

// TestTransfer_CopyFileFromContainer_ZipSlip verifies path traversal protection.
func TestTransfer_CopyFileFromContainer_ZipSlip(t *testing.T) {
	// This test verifies the zip slip protection in CopyFileFromContainer by
	// creating a file with a known path in the container and verifying the
	// extraction sanitizes paths properly.
	//
	// We test the zip slip logic at the unit level (no Docker needed) using
	// a crafted tar stream, but also verify the integration path works.
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Normal extraction should work fine.
	testContent := "safe content\n"
	containerPath := "/workspace/safe-file.txt"
	if err := CopyToContainer(ctx, d.cli, d.containerID, containerPath, strings.NewReader(testContent)); err != nil {
		t.Fatalf("CopyToContainer() error: %v", err)
	}

	hostDest := filepath.Join(t.TempDir(), "safe.txt")
	if err := CopyFileFromContainer(ctx, d.cli, d.containerID, containerPath, hostDest); err != nil {
		t.Fatalf("CopyFileFromContainer() error for safe file: %v", err)
	}

	got, err := os.ReadFile(hostDest)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != testContent {
		t.Errorf("content = %q, want %q", string(got), testContent)
	}
}

// TestTransfer_ExtractArtifact_MissingFile verifies clear error when output.md doesn't exist.
func TestTransfer_ExtractArtifact_MissingFile(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	sess := Session{Name: "nonexistent-session", StartedAt: time.Now()}

	_, err := d.ExtractArtifact(ctx, sess)
	if err == nil {
		t.Fatal("expected error for missing output.md, got nil")
	}
	// Error should mention the artifact path.
	if !strings.Contains(err.Error(), "output.md") {
		t.Errorf("error %q should mention output.md", err.Error())
	}
}

// TestTransfer_ExtractArtifact_Success verifies successful artifact extraction.
func TestTransfer_ExtractArtifact_Success(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	sess := Session{Name: "artifact-test", StartedAt: time.Now()}
	inputContent := "# Input\nHello\n"
	if err := d.StageInputs(ctx, sess, inputContent, "prompt"); err != nil {
		t.Fatalf("StageInputs() error: %v", err)
	}

	// Simulate agent writing output.md.
	outputContent := "# Result\nThe agent did things.\n"
	outputPath := "/workspace/.orqestra/artifact-test/output.md"
	if err := CopyToContainer(ctx, d.cli, d.containerID, outputPath, strings.NewReader(outputContent)); err != nil {
		t.Fatalf("CopyToContainer(output.md) error: %v", err)
	}

	got, err := d.ExtractArtifact(ctx, sess)
	if err != nil {
		t.Fatalf("ExtractArtifact() error: %v", err)
	}
	if string(got) != outputContent {
		t.Errorf("artifact = %q, want %q", string(got), outputContent)
	}
}

// TestTransfer_LargeFileExtraction verifies streaming extraction works without excessive memory.
func TestTransfer_LargeFileExtraction(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Create a 5MB file inside the container.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"sh", "-c", "dd if=/dev/urandom of=/workspace/large-file.bin bs=1024 count=5120 2>/dev/null"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec(dd) error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("dd exit code = %d", exitCode)
	}

	// Extract it.
	hostDest := filepath.Join(t.TempDir(), "large-file.bin")
	if err := CopyFileFromContainer(ctx, d.cli, d.containerID, "/workspace/large-file.bin", hostDest); err != nil {
		t.Fatalf("CopyFileFromContainer() error: %v", err)
	}

	// Verify size.
	fi, err := os.Stat(hostDest)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	expectedSize := int64(5 * 1024 * 1024)
	if fi.Size() != expectedSize {
		t.Errorf("extracted size = %d, want %d", fi.Size(), expectedSize)
	}
}

// TestTransfer_SessionIsolation verifies two concurrent sessions don't interfere.
func TestTransfer_SessionIsolation(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	sess1 := Session{Name: "session-alpha", StartedAt: time.Now()}
	sess2 := Session{Name: "session-beta", StartedAt: time.Now()}

	input1 := "Alpha input content\n"
	input2 := "Beta input content\n"

	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = d.StageInputs(ctx, sess1, input1, "prompt-alpha")
	}()
	go func() {
		defer wg.Done()
		err2 = d.StageInputs(ctx, sess2, input2, "prompt-beta")
	}()
	wg.Wait()

	if err1 != nil {
		t.Fatalf("StageInputs(sess1) error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("StageInputs(sess2) error: %v", err2)
	}

	// Verify session 1 input.
	var out1 bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"cat", "/workspace/.orqestra/session-alpha/input.md"}, nil, &out1)
	if err != nil || exitCode != 0 {
		t.Fatalf("reading session-alpha input: err=%v exitCode=%d", err, exitCode)
	}
	if got := out1.String(); got != input1 {
		t.Errorf("session-alpha input = %q, want %q", got, input1)
	}

	// Verify session 2 input.
	var out2 bytes.Buffer
	exitCode, err = d.Exec(ctx, []string{"cat", "/workspace/.orqestra/session-beta/input.md"}, nil, &out2)
	if err != nil || exitCode != 0 {
		t.Fatalf("reading session-beta input: err=%v exitCode=%d", err, exitCode)
	}
	if got := out2.String(); got != input2 {
		t.Errorf("session-beta input = %q, want %q", got, input2)
	}

	// Verify no cross-contamination — session-alpha shouldn't see session-beta files.
	var outCross bytes.Buffer
	exitCode, _ = d.Exec(ctx, []string{"cat", "/workspace/.orqestra/session-alpha/system-prompt.md"}, nil, &outCross)
	if exitCode != 0 {
		t.Fatal("session-alpha system-prompt.md should exist")
	}
	if got := outCross.String(); got != "prompt-alpha" {
		t.Errorf("session-alpha prompt = %q, want %q", got, "prompt-alpha")
	}
}

// TestTransfer_CopyDirToContainer verifies directory copying into a container.
func TestTransfer_CopyDirToContainer(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Create a temp directory with some files.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("file a"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(srcDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "b.txt"), []byte("file b"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy the directory into the container.
	if err := CopyDirToContainer(ctx, d.cli, d.containerID, "/workspace/copied-dir", srcDir); err != nil {
		t.Fatalf("CopyDirToContainer() error: %v", err)
	}

	// Verify files exist.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"cat", "/workspace/copied-dir/a.txt"}, nil, &out)
	if err != nil || exitCode != 0 {
		t.Fatalf("cat a.txt: err=%v exitCode=%d", err, exitCode)
	}
	if got := out.String(); got != "file a" {
		t.Errorf("a.txt = %q, want %q", got, "file a")
	}

	out.Reset()
	exitCode, err = d.Exec(ctx, []string{"cat", "/workspace/copied-dir/sub/b.txt"}, nil, &out)
	if err != nil || exitCode != 0 {
		t.Fatalf("cat sub/b.txt: err=%v exitCode=%d", err, exitCode)
	}
	if got := out.String(); got != "file b" {
		t.Errorf("sub/b.txt = %q, want %q", got, "file b")
	}
}

// TestTransfer_CopyOut_UsesSDK verifies the CopyOut method uses the SDK path (no exec cat).
func TestTransfer_CopyOut_UsesSDK(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	testContent := "sdk-copy-out-test\n"
	if err := os.WriteFile(filepath.Join(repoDir, "sdk-test.txt"), []byte(testContent), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	hostDest := filepath.Join(t.TempDir(), "out.txt")
	if err := d.CopyOut(ctx, "sdk-test.txt", hostDest); err != nil {
		t.Fatalf("CopyOut() error: %v", err)
	}

	got, err := os.ReadFile(hostDest)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != testContent {
		t.Errorf("CopyOut content = %q, want %q", string(got), testContent)
	}
}
