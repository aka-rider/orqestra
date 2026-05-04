package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
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
	cli         *dockerclient.Client
}

// newDockerClient creates a Docker client from environment (respects DOCKER_HOST, etc.).
// When DOCKER_HOST is unset, it probes common Docker Desktop socket paths on macOS.
func newDockerClient() (*dockerclient.Client, error) {
	opts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}

	// If DOCKER_HOST is not set, probe for Docker Desktop's non-default socket locations.
	if os.Getenv("DOCKER_HOST") == "" {
		if sock := discoverDockerSocket(); sock != "" {
			opts = append(opts, dockerclient.WithHost("unix://"+sock))
		}
	}

	return dockerclient.NewClientWithOpts(opts...)
}

// discoverDockerSocket probes common Docker socket paths and returns the first that exists.
func discoverDockerSocket() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/var/run/docker.sock",
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"),
		)
	}
	for _, sock := range candidates {
		if fi, err := os.Stat(sock); err == nil && fi.Mode().Type()&os.ModeSocket != 0 {
			return sock
		}
	}
	return ""
}

// newDockerClientWithHost creates a Docker client pointing to a specific host.
// Used in tests to simulate unreachable daemons.
func newDockerClientWithHost(host string) (*dockerclient.Client, error) {
	return dockerclient.NewClientWithOpts(
		dockerclient.WithHost(host),
		dockerclient.WithAPIVersionNegotiation(),
	)
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

// ensureClient lazily initializes the Docker SDK client.
func (d *DockerSandbox) ensureClient() error {
	if d.cli != nil {
		return nil
	}
	cli, err := newDockerClient()
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	d.cli = cli
	return nil
}

// Provision creates the Docker container with ephemeral volumes and read-only mounts.
func (d *DockerSandbox) Provision(ctx context.Context) error {
	d.setState(StateProvisioning)
	d.createdAt = time.Now()

	if err := d.ensureClient(); err != nil {
		d.setState(StatePending)
		return err
	}

	// Validate MCP socket path if configured — fail loudly if user specified it but it's missing.
	if d.cfg.MCP.SocketPath != "" {
		if _, err := os.Stat(d.cfg.MCP.SocketPath); err != nil {
			d.setState(StatePending)
			return fmt.Errorf("MCP socket path %q: %w", d.cfg.MCP.SocketPath, err)
		}
	}

	// Create ephemeral volumes — both destroyed with the sandbox.
	if _, err := d.cli.VolumeCreate(ctx, volume.CreateOptions{Name: d.volumeName}); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("creating workspace volume: %w", err)
	}
	if _, err := d.cli.VolumeCreate(ctx, volume.CreateOptions{Name: d.btrfsVolume()}); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("creating btrfs volume: %w", err)
	}

	containerConfig, hostConfig := d.buildContainerConfig()

	slog.Debug("sandbox: creating container", "id", d.id, "image", d.cfg.Image)
	resp, err := d.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		d.setState(StatePending)
		return fmt.Errorf("docker create: %w", err)
	}

	containerID := resp.ID
	if containerID == "" {
		d.setState(StatePending)
		return fmt.Errorf("docker create returned empty container ID")
	}

	d.mu.Lock()
	d.containerID = containerID
	d.mu.Unlock()

	// Start the container (entrypoint copies workspace).
	if err := d.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("docker start: %w", err)
	}

	// Wait for entrypoint init to complete.
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

	if err := d.ensureClient(); err != nil {
		d.setState(StateStopped)
		return -1, err
	}

	execCfg := container.ExecOptions{
		Cmd:          command,
		Env:          env,
		User:         "sandbox",
		AttachStdout: true,
		AttachStderr: true,
	}

	slog.Debug("sandbox: exec", "id", d.id, "command", command)
	execResp, err := d.cli.ContainerExecCreate(ctx, d.containerID, execCfg)
	if err != nil {
		d.setState(StateStopped)
		return -1, fmt.Errorf("docker exec create: %w", err)
	}

	attachResp, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		d.setState(StateStopped)
		return -1, fmt.Errorf("docker exec attach: %w", err)
	}
	defer attachResp.Close()

	// Demultiplex Docker's binary-prefixed stdout/stderr streams.
	if _, err := stdcopy.StdCopy(out, io.Discard, attachResp.Reader); err != nil {
		d.setState(StateStopped)
		return -1, fmt.Errorf("docker exec stream: %w", err)
	}

	// Get exit code.
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		d.setState(StateStopped)
		return -1, fmt.Errorf("docker exec inspect: %w", err)
	}

	d.setState(StateStopped)
	return inspectResp.ExitCode, nil
}

// ExtractChanges uses btrfs send to diff the workspace snapshot against the source.
// This is a metadata-level diff — no file scanning, O(changed blocks) not O(all files).
func (d *DockerSandbox) ExtractChanges(ctx context.Context) ([]ChangedFile, error) {
	prev := d.State()
	d.setState(StateExtracting)
	defer d.setState(prev)

	if err := d.ensureClient(); err != nil {
		return nil, err
	}

	// Snapshot the workspace as read-only (required for btrfs send).
	snapCode, err := d.execInternal(ctx, []string{
		"btrfs", "subvolume", "snapshot", "-r",
		"/mnt/btrfs/workspace", "/mnt/btrfs/workspace-final",
	})
	if err != nil {
		return nil, fmt.Errorf("creating ro snapshot for diff: %w", err)
	}
	if snapCode != 0 {
		return nil, fmt.Errorf("creating ro snapshot for diff: exit code %d", snapCode)
	}

	// btrfs send --no-data diffs workspace-final against source (parent).
	var stdout bytes.Buffer
	diffCode, err := d.execInternalWithOutput(ctx, []string{
		"sh", "-c",
		"btrfs send --no-data -p /mnt/btrfs/source /mnt/btrfs/workspace-final | btrfs receive --dump",
	}, &stdout)
	if err != nil {
		return nil, fmt.Errorf("btrfs send/receive diff: %w", err)
	}
	if diffCode != 0 {
		return nil, fmt.Errorf("btrfs send/receive diff: exit code %d", diffCode)
	}

	lines := strings.Split(stdout.String(), "\n")
	files := parseBtrfsDump(lines)

	// Enrich files with size and executable info via stat inside the container.
	var verified []ChangedFile
	for _, f := range files {
		if f.Op == FileDeleted {
			verified = append(verified, f)
			continue
		}
		info, err := d.statFile(ctx, "/workspace/"+f.Path)
		if err != nil {
			slog.Debug("sandbox: skipping non-existent diff entry", "path", f.Path)
			continue
		}
		f.Size = info.size
		f.IsExecutable = info.executable
		verified = append(verified, f)
	}
	files = verified

	slog.Info("sandbox: extracted changes", "id", d.id, "files", len(files))
	return files, nil
}

// CopyOut copies a file from the sandbox workspace to a host path.
// Uses exec + cat because /workspace is a bind-mounted btrfs snapshot that
// Docker's CopyFromContainer API cannot see (it only sees storage driver layers).
func (d *DockerSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	if err := d.ensureClient(); err != nil {
		return err
	}

	// Ensure parent directory exists on host.
	dir := filepath.Dir(hostPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	// Stream file content via exec cat.
	var stdout bytes.Buffer
	exitCode, err := d.execInternalWithOutput(ctx, []string{"cat", "/workspace/" + sandboxPath}, &stdout)
	if err != nil {
		return fmt.Errorf("reading %s from container: %w", sandboxPath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("reading %s from container: exit code %d", sandboxPath, exitCode)
	}

	if err := os.WriteFile(hostPath, stdout.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", hostPath, err)
	}
	return nil
}

// Destroy stops and removes the container and ALL associated volumes.
// A sandbox is fully ephemeral — nothing survives destruction.
func (d *DockerSandbox) Destroy(ctx context.Context) error {
	if err := d.ensureClient(); err != nil {
		d.setState(StateDestroyed)
		return err
	}

	d.mu.RLock()
	cid := d.containerID
	d.mu.RUnlock()

	if cid != "" {
		timeout := 5
		_ = d.cli.ContainerStop(ctx, cid, container.StopOptions{Timeout: &timeout})                    // fire-and-forget: best-effort stop before force remove
		_ = d.cli.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true, RemoveVolumes: true}) // fire-and-forget: container may already be gone
	}

	// Remove both named volumes — workspace and btrfs pool. Nothing persists.
	_ = d.cli.VolumeRemove(ctx, d.volumeName, true)    // fire-and-forget: volume may already be removed
	_ = d.cli.VolumeRemove(ctx, d.btrfsVolume(), true) // fire-and-forget: volume may already be removed

	d.setState(StateDestroyed)
	slog.Info("sandbox: destroyed", "id", d.id)
	return nil
}

// buildContainerConfig constructs the container and host configs for Docker SDK.
func (d *DockerSandbox) buildContainerConfig() (*container.Config, *container.HostConfig) {
	containerCfg := &container.Config{
		Image:     d.cfg.Image,
		Tty:       true,
		OpenStdin: true,
		Env:       d.env,
		Labels: map[string]string{
			LabelOwner:   LabelOwnerValue,
			LabelSession: d.id,
			LabelCreated: time.Now().UTC().Format(time.RFC3339),
		},
	}

	initTrue := true
	hostCfg := &container.HostConfig{
		Init:       &initTrue,
		Privileged: true, // TODO: remove once OverlayFS migration package lands (required for btrfs loop mount)
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=512m",
		},
		Mounts: []mount.Mount{
			{
				Type:     mount.TypeBind,
				Source:   d.repoPath,
				Target:   "/workspace-src",
				ReadOnly: true,
			},
			{
				Type:   mount.TypeVolume,
				Source: d.btrfsVolume(),
				Target: "/btrfs-pool",
			},
			{
				Type:   mount.TypeVolume,
				Source: d.volumeName,
				Target: "/workspace",
			},
		},
	}

	// Network mode.
	if d.cfg.Network != "" {
		hostCfg.NetworkMode = container.NetworkMode(d.cfg.Network)
	}

	// Read-only mounts for heavy dependency directories.
	for _, m := range d.cfg.ReadOnlyMounts {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.HostPath,
			Target:   m.ContainerPath,
			ReadOnly: true,
		})
	}

	// Read-write bind mounts (e.g. credentials that need token refresh).
	for _, m := range d.cfg.BindMounts {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: m.HostPath,
			Target: m.ContainerPath,
		})
	}

	// Docker MCP gateway socket — exposes host MCP servers inside the container.
	if d.cfg.MCP.SocketPath != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   d.cfg.MCP.SocketPath,
			Target:   "/run/mcp.sock",
			ReadOnly: true,
		})
	}

	// Resource limits.
	if d.cfg.Memory != "" {
		hostCfg.Resources.Memory = parseMemoryBytes(d.cfg.Memory)
	}
	if d.cfg.CPUs > 0 {
		hostCfg.Resources.NanoCPUs = int64(d.cfg.CPUs * 1e9)
	}
	if d.cfg.PidsLimit > 0 {
		hostCfg.Resources.PidsLimit = &d.cfg.PidsLimit
	}

	return containerCfg, hostCfg
}

// btrfsVolume returns the ephemeral Docker volume name for this sandbox's btrfs pool.
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
			code, err := d.execInternal(ctx, []string{"test", "-d", "/workspace/.git"})
			if err == nil && code == 0 {
				return nil
			}
			var stdout bytes.Buffer
			code, err = d.execInternalWithOutput(ctx, []string{"ls", "/workspace/"}, &stdout)
			if err == nil && code == 0 && stdout.Len() > 0 {
				return nil
			}
		}
	}
}

// execInternal runs a command inside the container as root, discarding output.
func (d *DockerSandbox) execInternal(ctx context.Context, cmd []string) (int, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execResp, err := d.cli.ContainerExecCreate(ctx, d.containerID, execCfg)
	if err != nil {
		return -1, err
	}
	attachResp, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, err
	}
	defer attachResp.Close()
	if _, err := io.Copy(io.Discard, attachResp.Reader); err != nil {
		return -1, err
	}
	inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, err
	}
	return inspect.ExitCode, nil
}

// execInternalWithOutput runs a command inside the container as root, capturing stdout.
func (d *DockerSandbox) execInternalWithOutput(ctx context.Context, cmd []string, stdout *bytes.Buffer) (int, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execResp, err := d.cli.ContainerExecCreate(ctx, d.containerID, execCfg)
	if err != nil {
		return -1, err
	}
	attachResp, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, err
	}
	defer attachResp.Close()
	if _, err := stdcopy.StdCopy(stdout, io.Discard, attachResp.Reader); err != nil {
		return -1, err
	}
	inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, err
	}
	return inspect.ExitCode, nil
}

type fileInfo struct {
	size       int64
	executable bool
}

// statFile uses ContainerStatPath to get file metadata.
func (d *DockerSandbox) statFile(ctx context.Context, path string) (fileInfo, error) {
	stat, err := d.cli.ContainerStatPath(ctx, d.containerID, path)
	if err != nil {
		return fileInfo{}, err
	}
	return fileInfo{
		size:       stat.Size,
		executable: stat.Mode&0o111 != 0,
	}, nil
}

func generateID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sb-%d", time.Now().UnixNano())
	}
	return "sb-" + hex.EncodeToString(b)
}

// parseMemoryBytes converts a Docker-style memory string (e.g. "4g", "512m") to bytes.
func parseMemoryBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "m"):
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "k"):
		multiplier = 1024
		s = s[:len(s)-1]
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n * multiplier
}

// DockerTracker implements ContainerTracker using the Docker SDK.
type DockerTracker struct {
	cli *dockerclient.Client
}

// NewDockerTracker creates a DockerTracker.
func NewDockerTracker() *DockerTracker { return &DockerTracker{} }

func (dt *DockerTracker) ensureClient() error {
	if dt.cli != nil {
		return nil
	}
	cli, err := newDockerClient()
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	dt.cli = cli
	return nil
}

func (dt *DockerTracker) ListOrqestraContainers(ctx context.Context) ([]TrackedContainer, error) {
	if err := dt.ensureClient(); err != nil {
		return nil, err
	}

	filter := filters.NewArgs(filters.Arg("label", LabelOwner+"="+LabelOwnerValue))
	containers, err := dt.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var tracked []TrackedContainer
	for _, c := range containers {
		tracked = append(tracked, TrackedContainer{
			ID:        c.ID,
			Labels:    c.Labels,
			CreatedAt: time.Unix(c.Created, 0),
		})
	}
	return tracked, nil
}

func (dt *DockerTracker) KillAndRemove(ctx context.Context, id string) error {
	if err := dt.ensureClient(); err != nil {
		return err
	}

	timeout := 5
	_ = dt.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}) // fire-and-forget: best effort

	if err := dt.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		return fmt.Errorf("removing container %s: %w", id, err)
	}
	return nil
}
