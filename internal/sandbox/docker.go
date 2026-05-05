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
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerSandbox is a Sandbox backed by a Docker container.
type DockerSandbox struct {
	mu          sync.RWMutex
	id          string
	containerID string
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
		id:       id,
		state:    StatePending,
		cfg:      cfg,
		repoPath: repoPath,
		env:      env,
	}
}

// isDockerVMPath returns true for paths that exist inside the Docker Desktop VM
// but are not accessible on the macOS host filesystem.
func isDockerVMPath(path string) bool {
	return strings.HasPrefix(path, "/run/host-services/")
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

// ContainerID returns the Docker container ID. Empty if not provisioned.
func (d *DockerSandbox) ContainerID() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.containerID
}

// Client returns the Docker client. May be nil if not yet initialized.
func (d *DockerSandbox) Client() *dockerclient.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cli
}

// Provision creates the Docker container:
// 1. Create and start the runtime container from the base image.
// 2. Copy the workspace into the container via the Docker API (tar-based).
// 3. Fix ownership and wait for readiness.
func (d *DockerSandbox) Provision(ctx context.Context) error {
	d.setState(StateProvisioning)
	d.createdAt = time.Now()

	if err := d.ensureClient(); err != nil {
		d.setState(StatePending)
		return err
	}

	// Validate MCP socket path if configured.
	// On Docker Desktop for Mac, /run/host-services/* paths exist inside the Docker
	// VM but are NOT stat-able from macOS. Skip host-side validation for these paths
	// — Docker resolves them at container creation time.
	if d.cfg.MCP.SocketPath != "" && !isDockerVMPath(d.cfg.MCP.SocketPath) {
		if _, err := os.Stat(d.cfg.MCP.SocketPath); err != nil {
			d.setState(StatePending)
			return fmt.Errorf("MCP socket path %q: %w", d.cfg.MCP.SocketPath, err)
		}
	}

	// Create and start the runtime container directly from the base image.
	containerConfig, hostConfig := d.buildContainerConfig()

	slog.Info("sandbox: creating container", "id", d.id, "image", d.cfg.Image)
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

	slog.Info("sandbox: starting container", "id", d.id)
	if err := d.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("docker start: %w", err)
	}

	// Wait for the container to be fully running before copying files.
	if err := d.waitRunning(ctx); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("waiting for container to start: %w", err)
	}

	// Copy workspace into the running container via the Docker API.
	slog.Info("sandbox: copying workspace", "id", d.id, "repo", d.repoPath)
	if err := CopyDirToContainer(ctx, d.cli, containerID, "/workspace", d.repoPath); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("copying workspace: %w", err)
	}

	// Fix ownership — CopyDirToContainer preserves host UIDs, but the sandbox
	// user (1000:1000) needs to own /workspace.
	slog.Info("sandbox: fixing ownership", "id", d.id)
	if _, err := d.execInternal(ctx, []string{"chown", "-R", "sandbox:sandbox", "/workspace"}); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("setting workspace ownership: %w", err)
	}

	// Wait for entrypoint to finish and workspace to be ready.
	slog.Info("sandbox: waiting for ready", "id", d.id)
	if err := d.waitReady(ctx); err != nil {
		d.setState(StatePending)
		return fmt.Errorf("waiting for container ready: %w", err)
	}

	d.setState(StateReady)
	slog.Info("sandbox: provisioned", "id", d.id, "container", containerID[:min(12, len(containerID))])
	return nil
}

// StageFiles copies content into the sandbox at specified container paths.
// Keys are absolute paths inside the container. Parent directories are created automatically.
// Must be called after Provision and before Exec.
func (d *DockerSandbox) StageFiles(ctx context.Context, files map[string][]byte) error {
	if err := d.ensureClient(); err != nil {
		return err
	}

	for containerPath, content := range files {
		if err := CopyToContainer(ctx, d.cli, d.containerID, containerPath, bytes.NewReader(content)); err != nil {
			return fmt.Errorf("staging file %s: %w", containerPath, err)
		}
	}

	slog.Debug("sandbox: staged files", "id", d.id, "count", len(files))
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

// ExtractChanges uses Docker's ContainerDiff API to detect filesystem changes
// in the container's writable layer (native OverlayFS diff — no btrfs needed).
func (d *DockerSandbox) ExtractChanges(ctx context.Context) ([]ChangedFile, error) {
	prev := d.State()
	d.setState(StateExtracting)
	defer d.setState(prev)

	if err := d.ensureClient(); err != nil {
		return nil, err
	}

	changes, err := d.cli.ContainerDiff(ctx, d.containerID)
	if err != nil {
		return nil, fmt.Errorf("container diff: %w", err)
	}

	var files []ChangedFile
	for _, c := range changes {
		// Only include changes under /workspace/.
		if !strings.HasPrefix(c.Path, "/workspace/") {
			continue
		}
		relPath := strings.TrimPrefix(c.Path, "/workspace/")
		if relPath == "" {
			continue
		}

		var op FileOp
		switch c.Kind {
		case 0: // Modified
			op = FileModified
		case 1: // Added
			op = FileAdded
		case 2: // Deleted
			op = FileDeleted
		default:
			continue
		}

		f := ChangedFile{Path: relPath, Op: op}

		// Enrich non-deleted files with size and executable info.
		if op != FileDeleted {
			info, err := d.statFile(ctx, c.Path)
			if err != nil {
				slog.Debug("sandbox: skipping non-statable diff entry", "path", relPath)
				continue
			}
			f.Size = info.size
			f.IsExecutable = info.executable
		}

		files = append(files, f)
	}

	slog.Info("sandbox: extracted changes", "id", d.id, "files", len(files))
	return files, nil
}

// CopyOut copies a file from the sandbox workspace to a host path.
// Uses Docker SDK CopyFromContainer — works natively with OverlayFS layers.
func (d *DockerSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	if err := d.ensureClient(); err != nil {
		return err
	}

	containerPath := "/workspace/" + sandboxPath
	return CopyFileFromContainer(ctx, d.cli, d.containerID, containerPath, hostPath)
}

// Destroy stops and removes the container.
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
		Init: &initTrue,
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=512m",
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

// waitRunning polls the Docker API until the container is in the "running" state.
func (d *DockerSandbox) waitRunning(ctx context.Context) error {
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for container %s to start", d.id)
		case <-tick.C:
			inspect, err := d.cli.ContainerInspect(ctx, d.containerID)
			if err != nil {
				continue
			}
			if inspect.State != nil && inspect.State.Running {
				return nil
			}
		}
	}
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
	return d.execInternalAs(ctx, "", cmd)
}

// execInternalAs runs a command inside the container as the specified user, discarding output.
// An empty user means container default (root).
func (d *DockerSandbox) execInternalAs(ctx context.Context, user string, cmd []string) (int, error) {
	if user == "" {
		user = "root"
	}
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		User:         user,
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
		User:         "root",
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

func (dt *DockerTracker) ListOrphanedImages(ctx context.Context) ([]string, error) {
	if err := dt.ensureClient(); err != nil {
		return nil, err
	}

	filter := filters.NewArgs(filters.Arg("reference", EphemeralImagePrefix+"*"))
	images, err := dt.cli.ImageList(ctx, image.ListOptions{Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	// Get running container image IDs so we don't remove in-use images.
	containers, err := dt.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("listing containers for image check: %w", err)
	}
	inUse := make(map[string]bool)
	for _, c := range containers {
		inUse[c.ImageID] = true
	}

	var orphaned []string
	for _, img := range images {
		if inUse[img.ID] {
			continue
		}
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, EphemeralImagePrefix) {
				orphaned = append(orphaned, tag)
			}
		}
	}
	return orphaned, nil
}

func (dt *DockerTracker) RemoveImage(ctx context.Context, imageRef string) error {
	if err := dt.ensureClient(); err != nil {
		return err
	}
	_, err := dt.cli.ImageRemove(ctx, imageRef, image.RemoveOptions{Force: true})
	if err != nil {
		return fmt.Errorf("removing image %s: %w", imageRef, err)
	}
	return nil
}
