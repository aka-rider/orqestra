package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DockerSandbox is a Sandbox backed by a Docker container.
type DockerSandbox struct {
	mu          sync.RWMutex
	id          string
	containerID string
	volumeName  string
	state       State
	createdAt   time.Time
	cfg         Config
	repoPath    string // absolute path to the host repo
	env         []string
}

// NewDockerSandbox creates a new sandbox instance. Call Provision to start it.
func NewDockerSandbox(cfg Config, repoPath string, env []string) *DockerSandbox {
	id := generateID()
	return &DockerSandbox{
		id:         id,
		volumeName: "orqestra-ws-" + id,
		state:      StatePending,
		cfg:        cfg,
		repoPath:   repoPath,
		env:        env,
	}
}

func (d *DockerSandbox) ID() string {
	return d.id
}

func (d *DockerSandbox) State() State {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state
}

func (d *DockerSandbox) setState(s State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = s
}

func (d *DockerSandbox) Info() Info {
	d.mu.RLock()
	defer d.mu.RUnlock()
	short := d.containerID
	if len(short) > 12 {
		short = short[:12]
	}
	return Info{
		ID:          d.id,
		ContainerID: short,
		State:       d.state,
		CreatedAt:   d.createdAt,
		Image:       d.cfg.Image,
	}
}

// Provision creates the Docker container with ephemeral volumes and read-only mounts.
func (d *DockerSandbox) Provision(ctx context.Context) error {
	d.setState(StateProvisioning)
	d.createdAt = time.Now()

	// Create ephemeral volumes — both destroyed with the sandbox.
	if err := d.dockerRun(ctx, "volume", "create", d.volumeName); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("creating workspace volume: %w", err)
	}
	if err := d.dockerRun(ctx, "volume", "create", d.btrfsVolume()); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("creating btrfs volume: %w", err)
	}

	args := d.buildCreateArgs()

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Debug("sandbox: creating container", "id", d.id, "args", args)
	if err := cmd.Run(); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("docker create: %w (stderr: %s)", err, stderr.String())
	}

	containerID := strings.TrimSpace(stdout.String())
	if containerID == "" {
		d.setState(StatePending)
		return fmt.Errorf("docker create returned empty container ID")
	}

	d.mu.Lock()
	d.containerID = containerID
	d.mu.Unlock()

	// Start the container (entrypoint copies workspace).
	if err := d.dockerRun(ctx, "start", containerID); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("docker start: %w", err)
	}

	// Wait for entrypoint init to complete (it should exit 0 if init-only,
	// or stay running as a long-lived shell). We wait briefly then check state.
	if err := d.waitReady(ctx); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("waiting for container ready: %w", err)
	}

	d.setState(StateReady)
	slog.Info("sandbox: provisioned", "id", d.id, "container", containerID[:min(12, len(containerID))])
	return nil
}

// Exec runs a command inside the container, streaming stdout to out.
// Returns the exit code and any error.
func (d *DockerSandbox) Exec(ctx context.Context, command []string, env []string, out io.Writer) (int, error) {
	d.setState(StateRunning)

	args := []string{"exec"}
	// Pass environment variables.
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, d.containerID)
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Debug("sandbox: exec", "id", d.id, "command", command)
	err := cmd.Run()

	d.setState(StateStopped)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("docker exec: %w (stderr: %s)", err, stderr.String())
	}
	return 0, nil
}

// ExtractChanges uses btrfs send to diff the workspace snapshot against the source.
// This is a metadata-level diff — no file scanning, O(changed blocks) not O(all files).
func (d *DockerSandbox) ExtractChanges(ctx context.Context) ([]ChangedFile, error) {
	prev := d.State()
	d.setState(StateExtracting)
	defer d.setState(prev) // restore to Stopped after extraction

	// Snapshot the workspace as read-only (required for btrfs send).
	var stderr bytes.Buffer
	snapCmd := exec.CommandContext(ctx, "docker", "exec", d.containerID,
		"btrfs", "subvolume", "snapshot", "-r",
		"/mnt/btrfs/workspace", "/mnt/btrfs/workspace-final",
	)
	snapCmd.Stderr = &stderr
	if err := snapCmd.Run(); err != nil {
		return nil, fmt.Errorf("creating ro snapshot for diff: %w (stderr: %s)", err, stderr.String())
	}

	// btrfs send --no-data diffs workspace-final against source (parent).
	// Pipe through btrfs receive --dump for human-readable output.
	var stdout bytes.Buffer
	stderr.Reset()
	diffCmd := exec.CommandContext(ctx, "docker", "exec", d.containerID,
		"sh", "-c",
		"btrfs send --no-data -p /mnt/btrfs/source /mnt/btrfs/workspace-final | btrfs receive --dump",
	)
	diffCmd.Stdout = &stdout
	diffCmd.Stderr = &stderr

	if err := diffCmd.Run(); err != nil {
		return nil, fmt.Errorf("btrfs send/receive diff: %w (stderr: %s)", err, stderr.String())
	}

	lines := strings.Split(stdout.String(), "\n")
	files := parseBtrfsDump(lines)

	// Enrich files with size and executable info via stat inside the container.
	for i, f := range files {
		if f.Op == FileDeleted {
			continue
		}
		info, err := d.statFile(ctx, "/workspace/"+f.Path)
		if err != nil {
			slog.Warn("sandbox: stat failed", "path", f.Path, "err", err)
			continue
		}
		files[i].Size = info.size
		files[i].IsExecutable = info.executable
	}

	slog.Info("sandbox: extracted changes", "id", d.id, "files", len(files))
	return files, nil
}

// CopyOut copies a file from the sandbox workspace to a host path.
func (d *DockerSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	src := d.containerID + ":/workspace/" + sandboxPath
	cmd := exec.CommandContext(ctx, "docker", "cp", src, hostPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp %s → %s: %w (stderr: %s)", src, hostPath, err, stderr.String())
	}
	return nil
}

// Destroy stops and removes the container and ALL associated volumes.
// A sandbox is fully ephemeral — nothing survives destruction.
func (d *DockerSandbox) Destroy(ctx context.Context) error {
	d.mu.RLock()
	cid := d.containerID
	d.mu.RUnlock()

	if cid != "" {
		// Stop with a short grace period, then force kill.
		_ = d.dockerRunIgnoreErr(ctx, "stop", "-t", "5", cid)
		_ = d.dockerRunIgnoreErr(ctx, "rm", "-f", "-v", cid) // -v removes anonymous volumes
	}

	// Remove both named volumes — workspace and btrfs pool. Nothing persists.
	_ = d.dockerRunIgnoreErr(ctx, "volume", "rm", "-f", d.volumeName)
	_ = d.dockerRunIgnoreErr(ctx, "volume", "rm", "-f", d.btrfsVolume())

	d.setState(StateDestroyed)
	slog.Info("sandbox: destroyed", "id", d.id)
	return nil
}

// buildCreateArgs constructs the `docker create` arguments.
func (d *DockerSandbox) buildCreateArgs() []string {
	args := []string{
		"create",
		"--init",                 // tini as PID 1 — prevents zombie processes
		"--cap-add", "SYS_ADMIN", // required for btrfs mount inside container
		"--device", "/dev/loop-control", // loopback device for btrfs image
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
		"--mount", fmt.Sprintf("type=bind,source=%s,target=/workspace-src,readonly", d.repoPath),
		"--mount", fmt.Sprintf("type=volume,source=%s,target=/btrfs-pool", d.btrfsVolume()),
		"--mount", fmt.Sprintf("type=volume,source=%s,target=/workspace", d.volumeName),
	}

	// Read-only mounts for heavy dependency directories.
	for _, m := range d.cfg.ReadOnlyMounts {
		args = append(args, "--mount",
			fmt.Sprintf("type=bind,source=%s,target=%s,readonly", m.HostPath, m.ContainerPath))
	}

	// Docker MCP gateway socket — exposes host MCP servers inside the container.
	if d.cfg.MCP.SocketPath != "" {
		args = append(args, "--mount",
			fmt.Sprintf("type=bind,source=%s,target=/run/mcp.sock,readonly", d.cfg.MCP.SocketPath))
	}

	// Resource limits.
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.1f", d.cfg.CPUs))
	}
	if d.cfg.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", d.cfg.PidsLimit))
	}

	// Labels for reaper tracking.
	args = append(args,
		"--label", LabelOwner+"="+LabelOwnerValue,
		"--label", LabelSession+"="+d.id,
		"--label", LabelCreated+"="+time.Now().UTC().Format(time.RFC3339),
	)

	// Environment variables.
	for _, e := range d.env {
		args = append(args, "-e", e)
	}

	// Image — entrypoint is baked in (tini + /entrypoint.sh handles btrfs snapshot).
	args = append(args, d.cfg.Image)

	return args
}

// btrfsVolume returns the ephemeral Docker volume name for this sandbox's btrfs pool.
// Fully scoped to this sandbox instance — destroyed with it.
func (d *DockerSandbox) btrfsVolume() string {
	return "orqestra-btrfs-" + d.id
}

// waitReady polls the container until the workspace copy is complete.
func (d *DockerSandbox) waitReady(ctx context.Context) error {
	deadline := time.After(60 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for sandbox %s to be ready", d.id)
		case <-tick.C:
			// Check if workspace exists and is populated.
			var stdout bytes.Buffer
			cmd := exec.CommandContext(ctx, "docker", "exec", d.containerID, "test", "-d", "/workspace/.git")
			cmd.Stdout = &stdout
			if cmd.Run() == nil {
				return nil // workspace is ready
			}
			// Also try non-git repos — just check /workspace is non-empty.
			cmd = exec.CommandContext(ctx, "docker", "exec", d.containerID, "ls", "/workspace/")
			cmd.Stdout = &stdout
			if err := cmd.Run(); err == nil && stdout.Len() > 0 {
				return nil
			}
		}
	}
}

type fileInfo struct {
	size       int64
	executable bool
}

// statFile runs stat inside the container to get file metadata.
func (d *DockerSandbox) statFile(ctx context.Context, path string) (fileInfo, error) {
	var stdout bytes.Buffer
	// stat -c "%s %a" gives size and octal permissions.
	cmd := exec.CommandContext(ctx, "docker", "exec", d.containerID, "stat", "-c", "%s %a", path)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fileInfo{}, err
	}

	var size int64
	var mode int
	if _, err := fmt.Sscanf(strings.TrimSpace(stdout.String()), "%d %o", &size, &mode); err != nil {
		return fileInfo{}, fmt.Errorf("parsing stat output %q: %w", stdout.String(), err)
	}

	return fileInfo{
		size:       size,
		executable: mode&0o111 != 0,
	}, nil
}

// dockerRun executes a docker subcommand and returns any error.
func (d *DockerSandbox) dockerRun(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w (stderr: %s)", args[0], err, stderr.String())
	}
	return nil
}

// dockerRunIgnoreErr runs a docker command and logs but ignores errors.
func (d *DockerSandbox) dockerRunIgnoreErr(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Debug("sandbox: docker command failed (ignored)", "args", args, "err", err)
		return err
	}
	return nil
}

func generateID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (shouldn't happen).
		return fmt.Sprintf("sb-%d", time.Now().UnixNano())
	}
	return "sb-" + hex.EncodeToString(b)
}

// DockerTracker implements ContainerTracker using the docker CLI.
type DockerTracker struct{}

// NewDockerTracker creates a DockerTracker.
func NewDockerTracker() *DockerTracker { return &DockerTracker{} }

func (dt *DockerTracker) ListOrqestraContainers(ctx context.Context) ([]TrackedContainer, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label="+LabelOwner+"="+LabelOwnerValue,
		"--format", "{{.ID}}\t{{.CreatedAt}}",
	)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var containers []TrackedContainer
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		id := parts[0]
		// Docker's CreatedAt format: "2024-01-15 10:30:00 +0000 UTC"
		createdAt, err := time.Parse("2006-01-02 15:04:05 -0700 MST", parts[1])
		if err != nil {
			slog.Warn("reaper: could not parse container created time", "id", id, "raw", parts[1], "err", err)
			createdAt = time.Now() // treat unparseable as fresh (safe default)
		}
		containers = append(containers, TrackedContainer{
			ID:        id,
			Labels:    map[string]string{LabelOwner: LabelOwnerValue},
			CreatedAt: createdAt,
		})
	}
	return containers, nil
}

func (dt *DockerTracker) KillAndRemove(ctx context.Context, id string) error {
	// Stop with grace period.
	stopCmd := exec.CommandContext(ctx, "docker", "stop", "-t", "5", id)
	_ = stopCmd.Run() // best effort

	// Force remove.
	rmCmd := exec.CommandContext(ctx, "docker", "rm", "-f", "-v", id)
	if err := rmCmd.Run(); err != nil {
		return fmt.Errorf("removing container %s: %w", id, err)
	}
	return nil
}
