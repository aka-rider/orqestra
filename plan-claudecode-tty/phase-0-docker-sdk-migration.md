# Work Package: docker-sdk-migration

| Field | Value |
|-------|-------|
| **ID** | `docker-sdk-migration` |
| **Wave** | 1 |
| **depends_on** | — |
| **Files** | `internal/sandbox/docker.go`, `internal/sandbox/docker_test.go`, `go.mod`, `go.sum` |

## Goal

Replace all `exec.CommandContext(ctx, "docker", ...)` shell-outs in the sandbox package with the native Go Docker Engine SDK (`github.com/docker/docker/client`). This is the foundational prerequisite for native PTY attach, overlay diff API access, and proper error handling from Docker operations.

## Steps

1. Add `github.com/docker/docker` to `go.mod`:

   ```
   go get github.com/docker/docker@v27.1.1
   ```

2. Create a Docker client wrapper (or inline in `docker.go`):
   - `newDockerClient() (*client.Client, error)` — creates a client from environment (respects `DOCKER_HOST`, etc.). Must use `client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())`.
   - Client MUST be explicitly stored on `DockerSandbox` struct fields via dependency injection (no package-level singletons).

3. Replace `Provision()` shell-outs:
   - `docker volume create` → `cli.VolumeCreate(ctx, volume.CreateOptions{Name: ...})`
   - `docker create` → `cli.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, "")` with:
     - `container.Config`: Image, Env, Labels, Tty=true, OpenStdin=true
     - `container.HostConfig`: Mounts (bind + volume), Resources (Memory, NanoCPUs, PidsLimit), NetworkMode, Init (pointer to true), Tmpfs
     - `container.HostConfig` must explicitly set `Privileged: true`, with a TODO attached directly to that line indicating it should be removed once the OverlayFS migration package lands.
   - `docker start` → `cli.ContainerStart(ctx, containerID, container.StartOptions{})`

4. Replace `Exec()`:
   - `docker exec` → `cli.ContainerExecCreate(ctx, containerID, execConfig)` + `cli.ContainerExecAttach(ctx, execID, execStartCheck)` + `cli.ContainerExecInspect()` for exit code.
   - `execConfig`: `Cmd`, `Env`, `AttachStdout=true`, `AttachStderr=true`, `User="sandbox"`.
   - The attach response gives `HijackedResponse.Reader`. Must explicitly use `github.com/docker/docker/pkg/stdcopy.StdCopy` to safely demultiplex the binary-prefixed stdout and stderr streams.

5. Replace `Destroy()`:
   - `docker stop` → `cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: intPtr(5)})`
   - `docker rm -f -v` → `cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})`
   - `docker volume rm` → `cli.VolumeRemove(ctx, volumeName, true)`

6. Replace `CopyOut()`:
   - `docker exec cat` → `cli.CopyFromContainer(ctx, containerID, path)` — returns `io.ReadCloser` with tar archive.
   - Must explicitly use `archive/tar.NewReader` and iteratively call `Next()` to read payloads to safely extract the single file from the tar archive, writing to host path.

7. Replace `statFile()`:
   - `docker exec stat` → `cli.ContainerStatPath(ctx, containerID, path)` — returns `container.PathStat` with size, mode.

8. Replace `waitReady()` polling:
   - Keep polling logic but use `cli.ContainerExecCreate` + `cli.ContainerExecStart` to run `test -d /workspace/.git` instead of shelling out.
   - Alternative: use `cli.ContainerWait(ctx, containerID, condition)` if entrypoint signals readiness via exit.

9. Replace helper methods:
   - Remove `dockerRun()` and `dockerRunIgnoreErr()` private helpers (no longer needed).
   - Remove all `exec.CommandContext(ctx, "docker", ...)` patterns.

10. **Preserve MCP socket mount** (CRITICAL — agents are blind without MCP):
    - The existing `buildCreateArgs()` conditionally mounts the MCP gateway socket at `/run/mcp.sock` inside the container.
    - In the SDK equivalent: add `mount.Mount{Type: mount.TypeBind, Source: cfg.MCP.SocketPath, Target: "/run/mcp.sock", ReadOnly: true}` to `HostConfig.Mounts` when `cfg.MCP.SocketPath` is set and exists.
    - Validate at provisioning time: if `cfg.MCP.SocketPath` is explicitly configured but missing, return an error (do not silently skip — per anti-pattern rules).
    - Integration test: provision container with MCP socket, exec `test -S /run/mcp.sock` inside, assert success.
    - The MCP socket allows Claude Code inside the sandbox to access host MCP servers (context7, Serena, etc.) — without it, agents cannot use tools.

11. Update `internal/sandbox/docker_test.go`:
    - Existing tests should still pass against the new SDK-backed implementation.
    - Add test: client creation fails gracefully when Docker is not running.
    - Add test: `Provision` returns wrapped error with container config context on failure.
    - Add test: MCP socket mounted and accessible inside container.

## Acceptance

- `go test ./internal/sandbox/ -run TestDocker -tags integration` passes.
- `go vet ./internal/sandbox/` clean.
- Zero `exec.CommandContext(ctx, "docker"` calls remain in `internal/sandbox/docker.go`.
- `docker.go` imports `github.com/docker/docker/client` and uses SDK for all Docker operations.
- Existing `Sandbox` interface unchanged — this is a pure internal refactor.
- MCP socket mount preserved and verified (agents have tool access inside sandbox).
- `go.mod` adds `github.com/docker/docker` (with its transitive deps).
- Files touched: `internal/sandbox/docker.go`, `internal/sandbox/docker_test.go`, `go.mod`, `go.sum`.
