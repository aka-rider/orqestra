# Plan: Seatbelt E2E - Replace Docker Sandbox with macOS-Native Seatbelt

**TL;DR**: Replace the Docker sandbox execution path with `internal/seatbelt` (`sandbox-exec`) for Claude Code agent sessions, while keeping Orqestra runnable throughout the migration. The final system runs agents directly on macOS with kernel-enforced path permissions: readonly agents can inspect the repo and write artifacts only to their session directory; workers can write the repo and session directory. File-based communication remains the contract between intake, planner, validators, and workers.

This plan intentionally stays small: macOS only, no compatibility matrix, no new orchestration framework, and no enterprise-grade abstraction layer. The goal is to finish the migration cleanly in this repo.

## Current Repo Context

The repo already has most of the low-level seatbelt pieces:

- `internal/seatbelt/sandbox.go` owns `sandbox-exec`, SBPL temp files, env scrubbing, `Wrap`, `Run`, PGID isolation, and cancellation cleanup.
- `internal/seatbelt/profile.go` owns `ToolProfile`, `Allow`, `AllowOptional`, and immutable `Snapshot` values.
- `internal/seatbelt/detect/detect.go` owns tool-specific detection for Claude, Homebrew, Git, Docker, and NPM.
- `internal/agent/runner.go` is still Docker-shaped: it stages files into `/workspace`, starts Docker PTYs, scans BEL bytes, copies artifacts out, and destroys containers.
- `cmd/orqestra/main.go` still has Docker integration in multiple execution paths, not just the TUI.

The migration should reuse the existing seatbelt primitives and replace the Docker-shaped agent lifecycle only after the native path is proven.

## End State

1. Docker is no longer required to run Orqestra agents.
2. `internal/sandbox/`, `build/sandbox/`, `docker-compose.yml`, and Docker SDK dependencies are removed only after the seatbelt runner has replaced all call sites.
3. All Claude Code agent execution goes through `sandbox-exec` via `internal/seatbelt`.
4. Agent communication stays file-based in `.orqestra/sessions/...`.
5. The TUI still talks to a `PTYWriter` (`Write`, `Resize`) and still receives output, BEL attention, and completion messages.
6. Workers write directly to the real repo, with `git diff` used after execution for human-visible audit.
7. Readonly agents can read the repo but cannot modify it.

## Access Model

The plan must not overload one `Workspace` path to mean both "repo" and "session". Seatbelt needs two explicit roots.

| Role           | Repo root  | Session root | Purpose |
| -------------- | ---------- | ------------ | ------- |
| intake         | read       | read+write   | Inspect repo context and produce intake artifact |
| planner        | read       | read+write   | Inspect repo and prior artifacts, produce plan artifact |
| plan-validator | read       | read+write   | Inspect repo, intake, and plan; produce verdict artifact |
| worker         | read+write | read+write   | Implement approved work and write execution artifacts |

Concrete API shape:

```go
type AccessMode uint8

const (
    RepoReadOnly AccessMode = iota
    RepoReadWrite
)

type RootConfig struct {
    RepoPath    string
    SessionPath string
    Mode        AccessMode
}
```

`seatbelt.Config` should accept these roots directly, or equivalent fields:

```go
type Config struct {
    RepoPath     string
    SessionPath  string
    RepoWritable bool
    Profiles     []Snapshot
    HarnessEnv   []string
    ProxyEnv     []string
    ExtraEnv     map[string]string
}
```

SBPL emission must enforce:

- repo root: `file-read*` for readonly agents
- repo root: `file-read* file-write* file-map-executable process-exec` for workers
- session root: `file-read* file-write* file-map-executable process-exec` for all agents
- tool profile paths: as provided by `Snapshot` values
- base system paths: unchanged unless a failing test proves a specific rule is unsafe or unnecessary

Both `RepoPath` and `SessionPath` must be absolute and resolved with `filepath.EvalSymlinks()` before SBPL compilation. macOS evaluates real paths at the kernel/VFS layer; symlinked paths such as `/tmp` must not leak into policy decisions.

Validation for this section:

- `TestSeatbelt_ReadonlyRepoWriteDenied`: readonly sandbox cannot create or modify a file under repo root.
- `TestSeatbelt_ReadonlySessionWriteAllowed`: readonly sandbox can write under session root.
- `TestSeatbelt_WorkerRepoWriteAllowed`: worker sandbox can write under repo root.
- `TestSeatbelt_SymlinkRootsResolved`: `/tmp` and other symlinked roots compile to their evaluated real paths.

## Phase 0: Prove Claude Agent Frontmatter Before Depending On It

The original plan makes `--agent` and frontmatter hooks central. That is fine if true, but it must be proven before the runner depends on it.

1. Add a small local probe or integration test that creates a temporary agent markdown file with:

   ```markdown
   ---
   name: orqestra-probe
   hooks:
     Stop:
       - hooks:
           - type: command
             command: "printf '\\a' > /dev/tty"
     Notification:
       - matcher: "idle_prompt"
         hooks:
           - type: command
             command: "printf '\\a' > /dev/tty"
   ---

   You are an Orqestra probe agent. Print a short response and stop.
   ```

2. Run Claude Code with the exact intended invocation shape:

   ```sh
   claude --agent <agent-file> --dangerously-skip-permissions -p "Say probe-ok"
   ```

3. Verify:
   - the installed Claude Code supports `--agent`
   - frontmatter is accepted
   - the command still works with `-p`
   - hooks can write BEL to the active TTY
   - the output mode Orqestra needs is still available

4. If `--agent` is not supported, fail fast at startup with a clear error and keep the old `--append-system-prompt-file` path available only as a temporary fallback while implementing the runner.

Validation for this phase:

- `TestAgentFrontmatter_Golden`: generated agent file matches expected markdown.
- `TestClaudeAgentFlagProbe` (integration): skipped unless Claude Code is installed; fails clearly if `--agent` is unavailable.
- Manual smoke: BEL appears in PTY stream when the hook fires.

## Phase 1: Config Migration Without Breaking Load

Replace Docker-facing config with seatbelt-facing config, but keep the codebase compiling while call sites move.

1. Add `SeatbeltConfig` to `internal/config/config.go`:

   ```go
   type SeatbeltConfig struct {
       MaxLifetime Duration          `yaml:"max_lifetime"`
       ProxyEnv    []string          `yaml:"proxy_env"`
       ExtraEnv    map[string]string `yaml:"extra_env"`
       AllowRead   []string          `yaml:"allow_read"`
       AllowWrite  []string          `yaml:"allow_write"`
       AllowExec   []string          `yaml:"allow_exec"`
   }
   ```

2. Add `Seatbelt SeatbeltConfig` to `Config` with `yaml:"seatbelt"`.
3. Add `Seatbelt *SeatbeltConfig` to `AgentNodeConfig` with `yaml:"seatbelt"` for per-agent overrides.
4. Keep `Sandbox SandboxConfig` temporarily during migration so existing Docker code continues compiling.
5. Update `internal/config/pipeline.yaml` and repo YAML files from `sandbox:` to `seatbelt:` once the runner integration is ready. Until then, tests can load both.
6. Validate required config strictly. If a user specifies a path or env var, failure to resolve it is an error except for documented optional path permissions.

Path permission semantics:

- `allow_read`: optional paths granted read access if present; permission or symlink errors fail.
- `allow_write`: optional paths granted read/write access if present; permission or symlink errors fail.
- `allow_exec`: directories containing executables, not individual binary files. The current `ToolProfile.Allow(..., Exec)` requires directories. If executable-file literals are desired later, add that support deliberately.

Example YAML:

```yaml
seatbelt:
  max_lifetime: 1h
  proxy_env:
    - AWS_PROFILE
    - SSH_AUTH_SOCK
  extra_env:
    NODE_ENV: "development"
  allow_read:
    - ~/.dotfiles
    - ~/.aws/config
  allow_write:
    - /tmp/my-custom-cache
  allow_exec:
    - /opt/homebrew/bin
```

Validation for this phase:

- Config load tests cover `seatbelt:` defaults and user overrides.
- Per-agent override tests confirm agent config merges or replaces global config exactly as documented.
- Tests confirm `allow_exec` rejects file paths with a clear message until executable-file support exists.

## Phase 2: User Profile Compilation and Detection Composition

Keep dependency direction simple:

- `internal/seatbelt` knows nothing about tools.
- `internal/seatbelt/detect` knows about tools and returns `seatbelt.Snapshot` values.
- `cmd/orqestra/main.go` composes detection and passes snapshots into the runner.
- `internal/agent` receives opaque snapshots and does not call detection.

Implement `detect.UserProfile` for user-configured filesystem permissions only:

```go
func UserProfile(home string, cfg config.SeatbeltConfig) (seatbelt.Snapshot, error) {
    p := seatbelt.NewToolProfile("user-config", home)
    for _, path := range cfg.AllowRead {
        if err := p.AllowOptional(path, seatbelt.Read); err != nil {
            return seatbelt.Snapshot{}, err
        }
    }
    for _, path := range cfg.AllowWrite {
        if err := p.AllowOptional(path, seatbelt.Write); err != nil {
            return seatbelt.Snapshot{}, err
        }
    }
    for _, dir := range cfg.AllowExec {
        if err := p.AllowOptional(dir, seatbelt.Exec); err != nil {
            return seatbelt.Snapshot{}, err
        }
    }
    return p.Snapshot(), nil
}
```

Do not add `ExtraEnv` to this profile. Environment merging already belongs to `seatbelt.New(...)`, which applies `BaseEnv -> Tool Profile Env -> HarnessEnv -> ProxyEnv -> ExtraEnv`.

Add detection composition:

```go
func AllProfiles(home, claudeBin string, cfg config.SeatbeltConfig) ([]seatbelt.Snapshot, error) {
    profiles := make([]seatbelt.Snapshot, 0, 4)

    user, err := UserProfile(home, cfg)
    if err != nil {
        return nil, fmt.Errorf("compile user seatbelt profile: %w", err)
    }
    profiles = append(profiles, user)

    claude, err := DetectClaude(home, claudeBin)
    if err != nil {
        return nil, err
    }
    profiles = append(profiles, claude)

    for _, detect := range []struct {
        name string
        fn   func(string) (*seatbelt.Snapshot, error)
    }{
        {"homebrew", DetectHomebrew},
        {"git", DetectGit},
        {"npm", DetectNPM},
    } {
        snap, err := detect.fn(home)
        if err != nil {
            return nil, fmt.Errorf("detect %s: %w", detect.name, err)
        }
        if snap != nil {
            profiles = append(profiles, *snap)
        }
    }
    return profiles, nil
}
```

Docker detection can stay only if there is a real need for workers to call Docker from inside seatbelt. It is not required for replacing Orqestra's own Docker sandbox.

Validation for this phase:

- Unit tests for `UserProfile` read/write/exec path compilation.
- Unit tests for missing optional paths being skipped while permission, symlink, and malformed-path errors are returned.
- Unit tests that `ExtraEnv` is not duplicated through snapshots.

## Phase 3: Extend Seatbelt for Repo + Session Roots

Modify `internal/seatbelt` before building the agent runner.

1. Replace or extend `seatbelt.Config.Workspace` with explicit `RepoPath`, `SessionPath`, and `RepoWritable` fields.
2. Keep existing `HarnessEnv`, `ProxyEnv`, `ExtraEnv`, and `Profiles` behavior.
3. Update `New` to resolve and validate both roots.
4. Update `ProfileBuilder` to emit base rules, tool profiles, repo rules, and session rules separately.
5. Keep env scrubbing in `seatbelt.New`; do not move `cmd.Env` manipulation into the agent runner.
6. Keep `Wrap` responsible for:
   - assigning `cmd.Env`
   - setting `SysProcAttr.Setpgid = true`
   - rewriting command execution through the `sandbox-exec` trampoline
   - defaulting `cmd.Dir` to the repo root unless a caller explicitly sets it

Validation for this phase:

- Existing seatbelt unit tests still pass after adapting names.
- Security tests assert the role matrix from the Access Model section.
- Env tests prove `HarnessEnv` wins over base/tool env and `ExtraEnv` wins over all.
- Do not test `/tmp` denial if the base profile intentionally allows temp writes.
- Do not test `/private/etc` denial if the base profile intentionally allows system config reads.

## Phase 4: Native PTY and Minimal Seatbelt Runner

Build the replacement runner next to the Docker runner. Do not delete Docker yet.

1. Add `internal/agent/pty_native.go` around `github.com/creack/pty` with:
   - `Read([]byte) (int, error)`
   - `Write([]byte) (int, error)`
   - `Resize(width, height int) error`
   - `ExitCode() int`
   - `Close() error`

2. Add `internal/agent/seatbelt_runner.go` with:

   ```go
   type SeatbeltRunner struct {
       cfg      config.SeatbeltConfig
       profiles []seatbelt.Snapshot
   }

   func NewSeatbeltRunner(cfg config.SeatbeltConfig, profiles []seatbelt.Snapshot) (*SeatbeltRunner, error)
   func (r *SeatbeltRunner) Run(ctx context.Context, cfg RunConfig) ([]byte, error)
   func (r *SeatbeltRunner) RunInteractive(ctx context.Context, cfg RunConfig) (*LiveSession, error)
   ```

3. Keep `RunCallbacks`, `RunConfig`, and `AgentSpec` recognizable so pipeline/TUI integration is small.
4. Stage input files directly on the host under `<session>/<role>/input/`.
5. Stage generated agent files under `<session>/<role>/agent.md`, not under user config.
6. Stage output paths under `<session>/<role>/output/`.
7. Build the Claude command through a harness helper. Prefer the proven `--agent` path once Phase 0 passes; otherwise keep temporary `--append-system-prompt-file` fallback.
8. Create `*exec.Cmd`, set `cmd.Dir` to repo root, wrap it with `seatbelt.Sandbox.Wrap(cmd)`, then start it with native PTY.
9. Reuse the existing BEL scanner logic from `Runner.scanForBEL`.
10. On completion, read the output artifact directly from the session output path. No `CopyOut` exists in the native path.
11. On cancellation or timeout, kill the process group and close the sandbox profile temp file.

Validation for this phase:

- Unit test command construction for `--agent` and fallback paths.
- Unit test staging layout.
- `TestPTY_DimensionsPropagation`: start a sandboxed `stty size` under native PTY and verify resize reaches the process.
- `TestSeatbeltRunner_FakeArtifact`: run a simple shell command in seatbelt that writes an output file; runner reads it from session output.
- `TestSeatbeltRunner_CancelKillsProcessGroup`: use a process tree and prove children do not survive cancellation.

## Phase 5: Pipeline and TUI Integration

Switch real Orqestra execution to `SeatbeltRunner` once the runner tests pass.

1. At startup, `main.go` resolves home, Claude binary, model config, and `detect.AllProfiles(...)`.
2. Construct one `SeatbeltRunner` with global config and detected snapshots.
3. Replace Docker `agent.NewRunner()` usage in TUI and pipeline paths with the seatbelt runner.
4. Update `RunConfig` to carry repo/session roots and seatbelt config instead of Docker sandbox config.
5. Replace Docker lifecycle states with native states that fit reality:
   - `Starting`
   - `Running`
   - `Done`
   - `Failed`
6. Keep the TUI `PTYWriter` interface unchanged.
7. Keep BEL detection unchanged.
8. Ensure the following entry points are either migrated or explicitly left out with a TODO:
   - default TUI
   - `--plan <file.md>` TUI execution
   - headless `exec`
   - headless `--plan`
   - legacy `plan` and `validate` commands if they do not launch workers

Validation for this phase:

- TUI tests still pass with a fake `LaunchInteractive`.
- Pipeline unit tests use fake runner where possible.
- Manual smoke: default TUI starts a Claude session under seatbelt and streams output.
- Manual smoke: worker can edit a temp repo and `git diff` shows the change.

## Phase 6: Work Audit and Operational Debugging

Seatbelt prevents unauthorized paths; `git diff` shows authorized repo changes. Use both.

1. After worker execution, run `git diff --stat` and `git diff --name-status` for the TUI/audit view.
2. Do not trust the diff as the security boundary; it is human defense in depth.
3. Keep `scripts/seatbelt-trace.sh` as the first debugging tool for EPERM denials.
4. When Claude, Node, Git, or Homebrew fails under sandbox, reproduce with tracing and add the smallest necessary path allowance or tool profile fix.

Validation for this phase:

- Test diff summary generation against a temp git repo.
- Manual smoke: denied path errors are visible and actionable.
- Manual smoke: trace script identifies at least one intentionally denied path during a negative test.

## Phase 7: Remove Docker

Only now remove Docker dependencies.

1. Delete `internal/sandbox/`.
2. Delete `build/sandbox/`.
3. Delete `docker-compose.yml`.
4. Remove Docker SDK dependencies from `go.mod` and `go.sum`.
5. Remove Docker reaper/tracker startup from `cmd/orqestra/main.go`.
6. Remove `sandboxCfgFrom` functions.
7. Remove or rewrite tests that only validate Docker behavior.
8. Update README/config examples to use `seatbelt:`.

Final validation:

- `go test ./...`
- focused seatbelt integration tests on macOS
- default TUI smoke with real Claude Code
- worker edit smoke in a temp git repo
- `rg "internal/sandbox|NewDocker|docker-compose|orqestra-sandbox|sandbox:"` has no production references except historical plans or docs that intentionally mention old Docker behavior

## Decisions

- **macOS-only for this migration.** Linux is out of scope.
- **No Docker required post-migration.** Docker deletion is the final cleanup, not the first step.
- **File-based communication preserved.** Session directories remain the audit trail.
- **Workers write the real repo.** Seatbelt restricts where they can write; Git shows what they changed.
- **Readonly agents read repo, write session.** This is the core access invariant.
- **`github.com/creack/pty` for native PTY.** The TUI should not know whether the PTY is Docker-backed or native.
- **No reaper.** Process group kill plus runner timeout is sufficient for the native path.
- **Concurrent workers stay serialized for MVP.** Revisit with git worktrees only after the single-worker path is boring.
- **Agent frontmatter hooks are preferred only after proof.** Until then, avoid making BEL depend on unverified Claude Code behavior.

## Common Misinterpretations This Plan Avoids

1. **"Workspace" does not mean both repo and session.** The runner has explicit repo and session roots.
2. **Readonly agents are not session-only.** They still need repo read access.
3. **Docker is not deleted first.** It disappears after the native path is proven.
4. **`allow_exec` means directories for now.** The current profile API does not support executable-file literals.
5. **`ExtraEnv` is not a profile concern.** Environment merging remains in `seatbelt.New`.
6. **`/tmp` denial is not a security invariant.** The current base profile allows temp writes for real tool compatibility.
7. **`/private/etc` denial is not a security invariant.** The current base profile allows system config reads and denies executable mapping.
8. **`--agent` is not assumed blindly.** It is proven first, then used.
