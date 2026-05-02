## Integration Plan

### 1. Export model env builder from `harness` package

`ClaudeCLI.buildEnv()` is private. The sandbox runner needs the same env vars to route the claude binary inside the container to the right model. Add a public function in claude_cli.go:

> **Fix (Blocker 5):** Strip any existing `/v1` suffix from `BaseURL` before appending it, preventing double-suffix (`/v1/v1`) on already-normalised URLs. Apply the same fix to the existing private `buildEnv()` when refactoring it.

```go
// BuildModelEnv returns the environment variables needed to route the claude binary
// to the given model. Used by sandbox runners that exec claude inside a container.
func BuildModelEnv(resolved config.ResolvedModel, small *config.ResolvedModel) []string {
    // Same logic as buildEnv, minus os.Environ() — the caller controls the base env
    var env []string
    switch resolved.Type {
    case "native":
        // no override
    case "anthropic":
        env = append(env,
            "ANTHROPIC_BASE_URL="+resolved.BaseURL,
            "ANTHROPIC_AUTH_TOKEN="+resolved.APIKey,
            "ANTHROPIC_MODEL="+resolved.Model,
            "ANTHROPIC_DEFAULT_SONNET_MODEL="+resolved.Model,
        )
        if small != nil {
            env = append(env,
                "ANTHROPIC_SMALL_FAST_MODEL="+small.Model,
                "ANTHROPIC_DEFAULT_HAIKU_MODEL="+small.Model,
            )
        }
    case "openai":
        // Strip trailing /v1 or /v1/ before appending — prevents double-suffix
        // when BaseURL is already normalised (e.g. http://host/v1).
        baseURL := strings.TrimRight(resolved.BaseURL, "/")
        baseURL = strings.TrimSuffix(baseURL, "/v1")
        baseURL += "/v1"
        env = append(env,
            "OPENAI_BASE_URL="+baseURL,
            "OPENAI_API_KEY="+resolved.APIKey,
        )
    }
    env = append(env,
        "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
    )
    return env
}
```

Then refactor `buildEnv()` to call `BuildModelEnv` + prepend `os.Environ()`. The same URL-normalisation fix applies inside `buildEnv()` before this refactor.

### 2. Add config conversion helper

> **Fix (Blocker 1):** `config` cannot import `sandbox` — `sandbox` already imports `harness` which imports `config`, creating a cycle. Move this as a free function in `main.go` (and duplicate a copy in `agent.go`) where both packages are already imported. Do **not** add it as a method on `config.SandboxConfig`.

Add a package-level helper `sandboxCfgFrom` in both `main.go` and `agent.go`:

```go
// sandboxCfgFrom converts a config.SandboxConfig to the sandbox.Config the runner expects.
func sandboxCfgFrom(sc config.SandboxConfig) sandbox.Config {
    cfg := sandbox.DefaultConfig()
    cfg.Enabled = sc.Enabled
    if sc.Image != "" { cfg.Image = sc.Image }
    if sc.Memory != "" { cfg.Memory = sc.Memory }
    if sc.CPUs > 0 { cfg.CPUs = sc.CPUs }
    if sc.PidsLimit > 0 { cfg.PidsLimit = sc.PidsLimit }
    if sc.MaxLifetime.Duration > 0 { cfg.MaxLifetime = sc.MaxLifetime.Duration }
    cfg.AllowedExecutables = sc.AllowedExecutables
    if sc.MCP.SocketPath != "" { cfg.MCP.SocketPath = sc.MCP.SocketPath }
    for _, m := range sc.ReadOnlyMounts {
        cfg.ReadOnlyMounts = append(cfg.ReadOnlyMounts, sandbox.MountConfig{
            HostPath: m.Host, ContainerPath: m.Container,
        })
    }
    return cfg
}
```

### 3. Wire into main.go

**Three touch points** — `runTUI()`, `runHeadless()`, and `runExecOnly()`. The pattern is the same in each: after creating the base `workerRunner`, conditionally replace it. Use `sandboxCfgFrom` (defined in Step 2) instead of the removed `ToSandboxConfig()` method.

> **Fix (Blocker 2 — TUI path only):** `p.Send` is not available at the time `runTUI()` constructs its runners. For the TUI path, build the `SandboxedCLIRunner` **inside the `Execute` closure** (where `pipeline.Send` is wired), not at the top of `runTUI`. For `runHeadless` and `runExecOnly` no `OnState` callback is needed, so the runner can be built normally before the closure.

**`runHeadless` / `runExecOnly` (no streaming callback needed):**

```go
// After: workerRunner := harness.NewClaudeCLIFromConfig(...)
if cfg.Sandbox.Enabled {
    repoPath, _ := os.Getwd()
    resolved, _ := cfg.ResolveModel(cfg.Worker.ModelRef)
    var small *config.ResolvedModel
    if rt, err := cfg.RuntimeOptions(cfg.Worker.ModelRef); err == nil && rt.SmallRef != "" {
        if s, err := cfg.ResolveModel(rt.SmallRef); err == nil {
            small = &s
        }
    }
    workerRunner = sandbox.NewSandboxedCLIRunner(sandbox.RunnerConfig{
        Sandbox:  sandboxCfgFrom(cfg.Sandbox),
        RepoPath: repoPath,
        Env:      harness.BuildModelEnv(resolved, small),
    })
}
// Token limit wrapping still applies after:
workerRunner = wrapRunner(workerRunner, limiter, cfg, cfg.Worker.ModelRef, "worker")
```

**`runTUI` — defer runner construction into the `Execute` closure:**

Extend `tui.PipelineFuncs` with a `Send func(tea.Msg)` field (wired inside `tui.Run` after `p` is created). Build the sandboxed runner lazily inside `Execute`:

```go
Execute: func(ctx context.Context, spec types.Specification, stdout io.Writer) error {
    activeWorker := workerRunner // baseline (non-sandbox)
    if cfg.Sandbox.Enabled {
        repoPath, _ := os.Getwd()
        resolved, _ := cfg.ResolveModel(cfg.Worker.ModelRef)
        var small *config.ResolvedModel
        if rt, err := cfg.RuntimeOptions(cfg.Worker.ModelRef); err == nil && rt.SmallRef != "" {
            if s, err := cfg.ResolveModel(rt.SmallRef); err == nil {
                small = &s
            }
        }
        activeWorker = wrapRunner(
            sandbox.NewSandboxedCLIRunner(sandbox.RunnerConfig{
                Sandbox:  sandboxCfgFrom(cfg.Sandbox),
                RepoPath: repoPath,
                Env:      harness.BuildModelEnv(resolved, small),
                OnState: func(id string, state sandbox.State) {
                    if pipeline.Send != nil {
                        pipeline.Send(tui.SandboxStateMsg{SandboxID: id, State: state.String()})
                    }
                },
            }),
            limiter, cfg, cfg.Worker.ModelRef, "worker",
        )
    }
    // ... use activeWorker instead of workerRunner ...
},
```

### 4. Wire into `Agent.NewFromConfig`

> **Fix (Blocker 4):** `NewFromConfig` has no access to a `*tokenlimit.Limiter`, so any sandbox runner created here silently bypasses token accounting. **Do not inject sandbox into `NewFromConfig`.** `NewFromConfig` is a convenience constructor for tests and future scheduler paths; sandbox wiring belongs at the `main.go` call sites where `wrapRunner` is already applied.
>
> For the scheduler path, per-node sandbox overrides (`AgentNodeConfig.Sandbox`) will be wired when that integration lands — not here.

Leave `NewFromConfig` unchanged. All sandbox injection lives in `main.go`.

### 5. TUI sandbox state events

Add a message type in messages.go:

```go
type SandboxStateMsg struct {
    SandboxID string
    State     string // "provisioning", "ready", "running", "extracting", "destroyed"
}
```

Add a `Send func(tea.Msg)` field to `PipelineFuncs` in model.go (or tui.go) and wire it inside `tui.Run` after `p` is created:

```go
// In tui.Run(), after: p := tea.NewProgram(...)
pipeline.Send = p.Send
```

The `OnState` callback in the `Execute` closure (Step 3) then calls `pipeline.Send` — no direct reference to `p` is needed outside `tui.Run`.

Handle `SandboxStateMsg` in model.go `Update()`:

```go
case SandboxStateMsg:
    m.logPanel.Add(LogEntry{
        Message: fmt.Sprintf("sandbox %s: %s", msg.SandboxID[:8], msg.State),
    })
    return m, nil
```

### 6. Implement `DockerTracker` and start the reaper

> **Fix (Blocker 3):** `sandbox.NewDockerTracker()` does not exist. It must be implemented before the reaper can be wired into `main.go`. Add it to `reaper.go` (or a new `docker_tracker.go` in `internal/sandbox/`).

**`DockerTracker` implementation** in `internal/sandbox/docker_tracker.go`:

```go
package sandbox

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "strconv"
    "time"
)

// DockerTracker implements ContainerTracker using the docker CLI.
type DockerTracker struct{}

func NewDockerTracker() *DockerTracker { return &DockerTracker{} }

func (t *DockerTracker) ListOrqestraContainers(ctx context.Context) ([]TrackedContainer, error) {
    out, err := exec.CommandContext(ctx,
        "docker", "ps", "--all",
        "--filter", "label="+LabelOwner+"="+LabelOwnerValue,
        "--format", "{{json .}}",
    ).Output()
    if err != nil {
        return nil, fmt.Errorf("docker ps: %w", err)
    }

    var containers []TrackedContainer
    for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
        if len(line) == 0 {
            continue
        }
        var row struct {
            ID     string `json:"ID"`
            Labels string `json:"Labels"`
        }
        if err := json.Unmarshal(line, &row); err != nil {
            continue
        }
        labels := parseDockerLabels(row.Labels)
        createdAt := time.Now() // fallback
        if ts, ok := labels[LabelCreated]; ok {
            if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
                createdAt = time.Unix(n, 0)
            }
        }
        containers = append(containers, TrackedContainer{
            ID: row.ID, Labels: labels, CreatedAt: createdAt,
        })
    }
    return containers, nil
}

func (t *DockerTracker) KillAndRemove(ctx context.Context, id string) error {
    if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
        return fmt.Errorf("docker rm -f %s: %w", id, err)
    }
    return nil
}

// parseDockerLabels parses the comma-separated key=value label string docker outputs.
func parseDockerLabels(s string) map[string]string {
    labels := make(map[string]string)
    for _, pair := range bytes.Split([]byte(s), []byte(",")) {
        kv := bytes.SplitN(pair, []byte("="), 2)
        if len(kv) == 2 {
            labels[string(kv[0])] = string(kv[1])
        }
    }
    return labels
}
```

**Wire in `main.go`** after config load:

```go
if cfg.Sandbox.Enabled {
    tracker := sandbox.NewDockerTracker()
    reaper := sandbox.NewReaper(tracker, cfg.Sandbox.MaxLifetime.Duration)
    go reaper.Run(ctx, 60*time.Second) // sweeps every minute, cleans up on ctx cancel
}
```

The reaper's `Run()` already handles final cleanup on context cancellation, which triggers on ctrl+c via `signal.NotifyContext`.

### 7. Add sandbox config to orqestra.yaml

```yaml
sandbox:
  enabled: false
  image: orqestra-sandbox:latest
  memory: 4g
  cpus: 2.0
  pids_limit: 256
  max_lifetime: 50m
  allowed_executables:
    - "scripts/*.sh"
  mcp:
    socket_path: /run/host-services/docker.proxy.sock
```

---

### Summary of files to change

| File                 | Change                                                                                            |
| -------------------- | ------------------------------------------------------------------------------------------------- |
| claude_cli.go        | Export `BuildModelEnv()` with URL-normalisation fix; refactor `buildEnv()` to use it             |
| main.go              | Add `sandboxCfgFrom()`; sandbox runner in `runHeadless`/`runExecOnly`; lazy runner in `runTUI` `Execute` closure; start reaper |
| agent.go             | Add `sandboxCfgFrom()` (no sandbox in `NewFromConfig` — see Step 4)                              |
| messages.go          | Add `SandboxStateMsg`                                                                             |
| model.go / tui.go    | Add `Send func(tea.Msg)` to `PipelineFuncs`; wire `p.Send` in `tui.Run`; handle `SandboxStateMsg` |
| docker_tracker.go    | New file — `DockerTracker` implementation of `ContainerTracker`                                  |
| orqestra.yaml        | Add `sandbox:` section                                                                            |

**Not changed:** `config.go` (no `ToSandboxConfig` — avoids import cycle), `agent.go NewFromConfig` (no sandbox injection — avoids token limit bypass).

The per-agent sandbox override (`AgentNodeConfig.Sandbox`) is already in the config struct — for the scheduler path, each agent node can get its own `SandboxedCLIRunner` when that integration lands.
