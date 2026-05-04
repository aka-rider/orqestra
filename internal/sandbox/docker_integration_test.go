//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests require a running Docker daemon and the orqestra-sandbox image.
// Run with: go test ./internal/sandbox/ -run TestDocker -tags integration

// dockerTempDir creates a temp directory that Docker Desktop can bind-mount.
// On macOS, t.TempDir() uses /var/folders which is not shared with Docker Desktop's VM.
// This helper creates temp dirs under the workspace directory which is known to be shared.
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

func TestDockerSDK_ClientCreation(t *testing.T) {
	cli, err := newDockerClient()
	if err != nil {
		t.Fatalf("newDockerClient() error: %v", err)
	}
	defer cli.Close()

	// Ping the daemon to verify connection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	if err != nil {
		t.Fatalf("Docker daemon not reachable: %v", err)
	}
}

func TestDockerSDK_ProvisionAndDestroy(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
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

	if d.State() != StateReady {
		t.Errorf("state = %v, want StateReady", d.State())
	}
	if d.containerID == "" {
		t.Error("containerID is empty after provisioning")
	}
}

func TestDockerSDK_Exec(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("world"), 0o644); err != nil {
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

	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"echo", "hello from sandbox"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if got := out.String(); got == "" {
		t.Error("expected output from echo command, got empty")
	}
}

func TestDockerSDK_ExecExitCode(t *testing.T) {
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

	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"sh", "-c", "exit 42"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec() error: %v", err)
	}
	if exitCode != 42 {
		t.Errorf("exit code = %d, want 42", exitCode)
	}
}

func TestDockerSDK_MCPSocketMounted(t *testing.T) {
	// Create a regular file to simulate the MCP socket — Docker Desktop's VirtioFS
	// cannot bind-mount Unix sockets from shared directories, but real usage mounts
	// the Docker MCP gateway socket from /run/host-services/ which is inside the VM.
	// We verify the mount plumbing works by mounting a regular file.
	sockDir := dockerTempDir(t)
	sockPath := filepath.Join(sockDir, "mcp.sock")
	if err := os.WriteFile(sockPath, nil, 0o666); err != nil {
		t.Fatalf("creating test socket file: %v", err)
	}

	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:   "orqestra-sandbox:latest",
		Network: "host",
		MCP:     MCPConfig{SocketPath: sockPath},
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Verify the MCP socket path exists inside the container.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"test", "-e", "/run/mcp.sock"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec(test -e) error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("MCP socket mount not found inside container (exit code %d)", exitCode)
	}
}

func TestDockerSDK_ProvisionFailsOnMissingMCPSocket(t *testing.T) {
	repoDir := dockerTempDir(t)
	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:latest",
		MCP:   MCPConfig{SocketPath: "/nonexistent/path/mcp.sock"},
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := d.Provision(ctx)
	if err == nil {
		d.Destroy(context.Background())
		t.Fatal("expected error when MCP socket path is missing, got nil")
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("MCP socket path")) {
		t.Errorf("error %q should mention MCP socket path", got)
	}
}

func TestDockerSDK_ProvisionFailsOnUnreachableDaemon(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image: "orqestra-sandbox:latest",
	}, repoDir, nil)
	// Override client with one pointing to a bogus socket.
	cli, err := newDockerClientWithHost("unix:///tmp/nonexistent-docker.sock")
	if err != nil {
		t.Fatalf("creating test client: %v", err)
	}
	d.cli = cli

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = d.Provision(ctx)
	if err == nil {
		d.Destroy(context.Background())
		t.Fatal("expected error when Docker daemon is unreachable, got nil")
	}
	// Error must propagate — not be swallowed.
	t.Logf("Got expected error: %v", err)
}

func TestDockerSDK_CopyOut(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	testContent := "copy-out-test-content\n"
	if err := os.WriteFile(filepath.Join(repoDir, "copytest.txt"), []byte(testContent), 0o644); err != nil {
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

	hostDest := filepath.Join(t.TempDir(), "extracted.txt")
	if err := d.CopyOut(ctx, "copytest.txt", hostDest); err != nil {
		t.Fatalf("CopyOut() error: %v", err)
	}

	got, err := os.ReadFile(hostDest)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(got) != testContent {
		t.Errorf("CopyOut content = %q, want %q", got, testContent)
	}
}

func TestDockerSDK_ResourceLimits(t *testing.T) {
	repoDir := dockerTempDir(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDockerSandbox(Config{
		Image:     "orqestra-sandbox:latest",
		Network:   "host",
		Memory:    "512m",
		CPUs:      1.0,
		PidsLimit: 64,
	}, repoDir, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := d.Provision(ctx); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	defer d.Destroy(context.Background())

	// Verify container is functional with resource limits applied.
	var out bytes.Buffer
	exitCode, err := d.Exec(ctx, []string{"echo", "limits-ok"}, nil, &out)
	if err != nil {
		t.Fatalf("Exec() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
}
