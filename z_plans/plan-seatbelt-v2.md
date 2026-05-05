# Seatbelt v2 — Full Rewrite Plan

## Why the current implementation must be deleted

1. **Silent error swallowing.** Every `Detect*` function does `if err == nil { append }`. Broken symlinks, permission errors, moved paths — all invisible. The system runs with a degraded profile and nobody knows.

2. **ToolProfile is a naked struct.** Zero encapsulation. Anyone constructs `ToolProfile{ExecDirs: []Path{{Resolved: "/", IsDir: true}}}` and the builder emits it. No validation, no invariants.

3. **SBPL rules are wrong:**
   - `(subpath "/private/etc")` in the `file-map-executable` block — makes config files executable (privilege escalation if writable).
   - `(subpath "/Library")` with `file-map-executable` — allows mapping ANY file under `/Library` as executable code.
   - `(allow file-read* (literal "/private"))` — unnecessary readdir on system root. `file-read-metadata` already handles `stat()`.

4. **Detection in the security package.** Seatbelt knows about Docker, Claude, Git, Homebrew, NPM. When any tool moves a file, the security boundary breaks. Dependency inversion violation.

5. **Integration tests are theatrical.** "Read ~/.ssh/id_rsa" tests file existence, not sandbox enforcement. Most machines don't have `id_rsa`.

---

## Architecture

```
internal/seatbelt/           ← security boundary, knows NOTHING about tools
    path.go                  ← Path type, ResolvePath
    profile.go               ← ToolProfile (builder), Snapshot (immutable)
    builder.go               ← ProfileBuilder → SBPL string
    sandbox.go               ← Config, New, Wrap, Run (manages env scrub, pgid logic, rlimits)
    env.go                   ← BaseEnv
    sandbox_test.go          ← security property tests

internal/seatbelt/detect/    ← tool knowledge lives HERE, returns Snapshots
    detect.go                ← DetectClaude, DetectGit, DetectDocker, etc. (Read-only, no mutations)
    detect_test.go

cmd/sandbox/                 ← current-phase executable target
    main.go                  ← local Darwin sandbox runner for Claude/tool execution
```

Current phase deliverable: build a standalone `cmd/sandbox` binary that composes detection profiles, constructs a Seatbelt sandbox, and runs Claude/tool commands locally on Darwin. The existing Docker-based `internal/sandbox` package remains the production Orqestra path for now, but it is legacy and should be phased out after the Seatbelt runner proves stable. Seatbelt never imports `detect`.

Future migration path: after `cmd/sandbox` is stable, introduce a Darwin implementation of the existing sandbox interface or a small runner adapter so `internal/agent` can select Seatbelt instead of Docker. Do not wire Seatbelt into `internal/agent` until the config schema and lifecycle semantics are explicit.

### Security & Operational Hardening (The "Rest of the Owl")

1. **Concrete Proxied Environment:** \`cmd.Env\` must be scrubbed block-by-block (\`env -i\` equivalent).
   - **Start empty.**
   - **Terminal & TUI Baselines (Required for Claude Code/Node.js UI):** \`TERM\` (vital for cursor addressing), \`COLORTERM\`, \`TERM_PROGRAM\`, \`TERM_PROGRAM_VERSION\`, \`FORCE_COLOR\` (for Node \`supports-color\` reliance), and \`TERM_FEATURES\` (vital for modern emulator capability signaling like hyperlink or kitty keyboard protocols).
   - **Locale & Identity:** \`LANG\`, \`LC_ALL\`, \`USER\`, \`LOGNAME\`, \`HOME\`.
   - **Dependent Tool Env (Mapped from DetectProfiles):**
     - *Homebrew:* Must inject \`HOMEBREW_PREFIX\`, \`HOMEBREW_CELLAR\`, \`HOMEBREW_REPOSITORY\`. If omitted, \`brew\` binaries fall back to guessing via \`pwd\` or user configs, which frequently break under SBPL path restrictions.
     - *Git:* Proxy global pass-throughs like \`GIT_CONFIG_GLOBAL\` (if strictly required) or allow standard fallback to \`~/.gitconfig\`.
   - **Map user-defined** \`ProxyEnv\` and Orqestra \`ExtraEnv\` overrides.
   - **Restrict \`PATH\`** to seatbelt exact bounds + \`/usr/bin:/bin\`.
2. **Process Group Isolation (Anti-Zombie):** LLMs spawn daemons. Using \`cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\` guarantees all subprocesses sit in a predictable tree. Timeout cleanup uses \`syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)\`.
3. **Symlink TOCTOU (Time-of-Check to Time-of-Use):** Orqestra computes canonical inode paths *at startup* using \`filepath.EvalSymlinks()\`. SBPL checks privileges at the macOS kernel/VFS layer on the *real* path boundaries. If an attacker converts \`~/.docker\` to a symlink pointing to \`/\` during execution, the kernel sees the traversal requesting \`/etc/passwd\` mapped against \`/var/lib/docker\` (the original lock) and instantly denies it. No runtime manual tracking needed.
4. **Resource Limiting (rlimits):** Hardcode \`syscall.Setrlimit\` inside the exec wrapper or via a pre-hook:
   - \`RLIMIT_NPROC: 512\` (Stops fork-bombs)
   - \`RLIMIT_NOFILE: 4096\` (Prevent FD exhaustions stalling out the orchestrator).
5. **No CLI Argument Secrets:** LLM credentials / tokens must feed via \`ExtraEnv\` or \`stdin\`, never via raw \`sandbox-exec\` CLI args where \`ps aux\` traces can leak them.
6. **No Mutation in Detection:** The \`detect\` package exclusively observes. No side effect IO (no "mkdir -p" on missing folders).

---

## The single API

```go
type Permission uint8

const (
    Read  Permission = 1 << iota // SBPL: file-read*
    Write                        // SBPL: file-read* + file-write*
    Exec                         // SBPL: file-read* + file-map-executable + process-exec
)

// ToolProfile accumulates filesystem access rules & specific env variables.
type ToolProfile struct {
    name    string
    home    string
    entries []entry
    env     map[string]string // specific envs required by this tool
}

type entry struct {
    path Path
    perm Permission
}

func NewToolProfile(name, home string) *ToolProfile {
    return &ToolProfile{
        name: name,
        home: home,
        env:  make(map[string]string),
    }
}

func (p *ToolProfile) AddEnv(key, value string) {
    p.env[key] = value
}

// Allow is the ONLY mutation method. Resolves raw path (expands ~, resolves symlinks, stats).
// Returns error on any failure — never swallows.
func (p *ToolProfile) Allow(raw string, perm Permission) error {
    path, err := ResolvePath(raw, p.home)
    if err != nil {
        return fmt.Errorf("profile %q: %w", p.name, err)
    }
    if perm&Exec != 0 && !path.IsDir {
        return fmt.Errorf("profile %q: exec requires directory, got file %q", p.name, raw)
    }
    p.entries = append(p.entries, entry{path: path, perm: perm})
    return nil
}

// AllowOptional skips paths that do not exist, but propagates permission denied,
// symlink loops, and all other non-not-exist errors.
func (p *ToolProfile) AllowOptional(raw string, perm Permission) error {
    if err := p.Allow(raw, perm); err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil
        }
        return err
    }
    return nil
}

// Snapshot returns an immutable, opaque value for ProfileBuilder.
type Snapshot struct {
    name    string
    entries []entry
    env     map[string]string
}

func (p *ToolProfile) Snapshot() Snapshot {
    // 1. Explicit Deep Copy for entries to sever slice backing array leakage.
    cp := make([]entry, len(p.entries))
    copy(cp, p.entries)

    // 2. Explicit Deep Copy for environment mappings.
    envCp := make(map[string]string, len(p.env))
    for k, v := range p.env {
        envCp[k] = v
    }

    return Snapshot{
        name:    p.name,
        entries: cp,
        env:     envCp,
    }
}
```

The SBPL builder groups entries by (permission, isDir) to emit compact rules.

### The Execution Boundary (`Sandbox.Wrap` + `Sandbox.Run`)

To ensure there is zero ambiguity on how `rlimits`, `sandbox-exec`, `envCp`, process-group cleanup, and context cancellation are applied without mutating the orchestrator parent process:

```go
type Sandbox struct {
    sbplPath string   // Pre-compiled SBPL profile stored in a chmod 0400 temp file
    env      []string // Pre-compiled scrubbed environment
}

// Wrap mutates an *exec.Cmd so it will run inside sandbox-exec with the scrubbed env.
// It does NOT run the command and therefore does NOT own timeout cleanup.
func (s *Sandbox) Wrap(cmd *exec.Cmd) error {
    if cmd.Path == "" || len(cmd.Args) == 0 {
        return fmt.Errorf("sandbox: cannot wrap empty command")
    }

    // 1. Apply absolute environment boundary
    cmd.Env = s.env

    // 2. Apply Process Group Isolation (Anti-Zombie)
    if cmd.SysProcAttr == nil {
        cmd.SysProcAttr = &syscall.SysProcAttr{}
    }
    cmd.SysProcAttr.Setpgid = true

    // 3. Apply Resource Limits (rlimits) via trampoline & sandbox-exec wrapper.
    // We cannot use syscall.Setrlimit in the parent because it mutates the orchestrator.
    // Instead we prepend a secure shell trampoline.

    originalBin := cmd.Path
    originalArgs := cmd.Args[1:] // skip argv[0]

    // Trampoline script: sets limits, then execs sandbox-exec which replaces the shell process
    trampoline := `
ulimit -u 512      # RLIMIT_NPROC
ulimit -n 4096     # RLIMIT_NOFILE
exec sandbox-exec -f "$1" "$2" "$@"
`
    // Re-construct exactly. The SBPL profile is passed by file path, not inline argv.
    newArgs := []string{"sh", "-c", trampoline, "sh", s.sbplPath, originalBin}
    newArgs = append(newArgs, originalArgs...)

    cmd.Path = "/bin/sh"
    cmd.Args = newArgs

    return nil
}

// Run owns the secure lifecycle: wrap, start, wait, and kill the process group on cancel.
// Tests that claim anti-zombie behavior MUST use Run, not Wrap directly.
func (s *Sandbox) Run(ctx context.Context, cmd *exec.Cmd) error {
    if err := s.Wrap(cmd); err != nil {
        return err
    }
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("sandbox: start: %w", err)
    }

    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()

    select {
    case err := <-done:
        return err
    case <-ctx.Done():
        if cmd.Process != nil {
            _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // fire-and-forget: best-effort cleanup after caller cancellation
        }
        err := <-done
        if err != nil {
            return fmt.Errorf("sandbox: canceled: %w", ctx.Err())
        }
        return ctx.Err()
    }
}
```

---

## Config

```go
type Config struct {
    Workspace  string
    Profiles   []Snapshot
    HarnessEnv []string           // exact key=value env from harness.BuildModelEnv or cmd/sandbox flags
    ProxyEnv   []string           // env var NAMES to forward from host — MUST exist or error
    ExtraEnv   map[string]string  // explicit key=value pairs
}
```

`New()` validates:

- Workspace exists and is a directory
- Every `ProxyEnv` name exists in host env — fail if not
- sandbox-exec is available
- HOME is set
- `HarnessEnv` entries are valid `KEY=value` pairs and are preserved by the env scrub

No silent defaults. No fallbacks for user-specified resources.

Environment merge order is deterministic:

1. `BaseEnv` from a clean slate: terminal, locale, identity, `HOME`, `TMPDIR`, and bounded `PATH`.
2. Tool profile env from `Snapshot` values, such as Homebrew variables.
3. `HarnessEnv` from Orqestra/Claude model routing, including `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`, model names, small-model variables, and traffic-disable flags.
4. `ProxyEnv` values copied from the host after existence validation.
5. `ExtraEnv` explicit overrides.

Later entries intentionally override earlier entries. Tests must assert that the current `harness.BuildModelEnv` contract survives the Seatbelt env scrub.

---

## System base rules (corrected)

```scheme
(version 1)
(deny default (with message "orqestra"))

;; Process fundamentals (required for any process to start)
(allow file-read-metadata)
(allow sysctl-read)
(allow signal)
(allow process-info*)
(allow process-fork)

;; Dynamic linker + system libraries (read + map-executable)
(allow file-read* file-map-executable
  (subpath "/System")
  (subpath "/usr/lib")
  (subpath "/usr/share")
  (subpath "/var/db/dyld")
  (subpath "/Library/Frameworks")
  (subpath "/Library/Apple"))

;; System binaries (exec)
;; Note: Future iterations should restrict this to specific necessary binaries
;; to prevent `curl` or `nc` from bypassing network isolation.
(allow process-exec
  (subpath "/usr/bin")
  (subpath "/usr/sbin")
  (subpath "/usr/libexec")
  (subpath "/bin")
  (subpath "/sbin"))

;; System binaries (read for shell builtins, scripting)
(allow file-read*
  (subpath "/usr/bin")
  (subpath "/usr/sbin")
  (subpath "/usr/libexec")
  (subpath "/bin")
  (subpath "/sbin"))

;; System config (READ-ONLY, not executable)
(allow file-read*
  (subpath "/private/etc")
  (subpath "/Library/Preferences")
  (subpath "/Library/Keychains")
  (subpath "/private/var/db/timezone")
  (subpath "/private/var/db/mds"))

;; Tmp (read + write)
(allow file-read* file-write*
  (subpath "/tmp")
  (subpath "/private/tmp")
  (subpath "/private/var/folders")
  (subpath "/var/folders")
  (subpath "$TMPDIR"))

;; Devices
(allow file-read* file-write*
  (regex "^/dev/(tty.*|null|zero|random|urandom|dtracehelper)"))
(allow file-ioctl)

;; IPC (Keychain, Security.framework, DNS)
(allow mach-lookup)
(allow ipc-posix-shm*)
(allow file-read* (subpath "/private/var/run/mDNSResponder"))

;; Network (Full outbound allowed for dev tools/LLMs)
(allow system-socket)
(allow network-outbound)
(allow network-inbound (local ip "localhost:*"))
```

**What changed:**

- `/private/etc` moved from executable to read-only
- `/Library` narrowed to `/Library/Frameworks` + `/Library/Apple` for exec-mapping
- Removed `file-read* (literal "/")`, `(literal "/private")`, `(literal "/private/var")` — unnecessary
- Removed `(literal "$HOME")` from system base — moved to Claude profile (their need)
- `process-exec` narrowed to actual binary directories only
- Removed `sysctl-write` — no process inside sandbox should modify kernel params
- Removed `/Library/Developer/CommandLineTools` from system — belongs in Git profile

---

## Detection (separate package)

```go
package detect

// DetectClaude — mandatory binary anchor, optional state/config paths.
func DetectClaude(home string, binary string) (seatbelt.Snapshot, error) {
    p := seatbelt.NewToolProfile("claude", home)

    if binary == "" {
        binary = "claude"
    }
    binPath, err := exec.LookPath(binary)
    if err != nil {
        return seatbelt.Snapshot{}, fmt.Errorf("detect claude binary %q: %w", binary, err)
    }
    if err := p.Allow(filepath.Dir(binPath), seatbelt.Exec); err != nil {
        return seatbelt.Snapshot{}, fmt.Errorf("detect claude binary dir: %w", err)
    }

    for _, optional := range []struct {
        path string
        perm seatbelt.Permission
    }{
        {"~/.claude.json", seatbelt.Write},
        {"~/.claude.json.lock", seatbelt.Write},
        {"~/.claude", seatbelt.Write},
        {"~/.local/state/claude", seatbelt.Write},
        {"~/Library/Caches/claude-cli-nodejs", seatbelt.Write},
        {"~/.local/bin", seatbelt.Exec},
        {"~/.local/share", seatbelt.Exec},
        {"~", seatbelt.Read}, // readdir on $HOME; Claude probes it
    } {
        if err := p.AllowOptional(optional.path, optional.perm); err != nil {
            return seatbelt.Snapshot{}, fmt.Errorf("detect claude optional path %q: %w", optional.path, err)
        }
    }

    return p.Snapshot(), nil
}

// DetectHomebrew — discovers env prefix dynamically
func DetectHomebrew(home string) (*seatbelt.Snapshot, error) {
    brewPath, err := exec.LookPath("brew")
    if errors.Is(err, exec.ErrNotFound) {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("detect homebrew binary: %w", err)
    }

    prefix, err := exec.Command(brewPath, "--prefix").Output()
    if err != nil {
        return nil, fmt.Errorf("detect homebrew prefix: %w", err)
    }

    basePath := strings.TrimSpace(string(prefix))
    p := seatbelt.NewToolProfile("homebrew", home)

    // Export required tool variables without requiring the user to ProxyEnv them manually
    p.AddEnv("HOMEBREW_PREFIX", basePath)
    p.AddEnv("HOMEBREW_CELLAR", filepath.Join(basePath, "Cellar"))
    p.AddEnv("HOMEBREW_REPOSITORY", basePath)

    if err := p.Allow(basePath, seatbelt.Read); err != nil {
        return nil, fmt.Errorf("detect homebrew core read: %w", err)
    }
    // Note: brew exec/bindings configured securely

    snap := p.Snapshot()
    return &snap, nil
}

// DetectDocker — nil if not installed. Error if installed but broken.
func DetectDocker(home string) (*seatbelt.Snapshot, error) { ... }

// DetectGit — nil if no git config. Error if present but broken.
func DetectGit(home string) (*seatbelt.Snapshot, error) { ... }
```

**Error handling contract:**

- "not installed" → `nil, nil` (skip)
- "installed but a path is genuinely broken" → `nil, error` (fail fast)
- `os.ErrNotExist` for optional sub-paths within an installed tool → skip that path, continue
- Any other `os.Stat` error (permission denied, I/O error) → propagate
- Claude detection is mandatory for `cmd/sandbox`, but only the actual binary anchor is mandatory. State/config/cache paths are optional unless the caller explicitly configured them.

---

## Caller integration

Current phase integration is a standalone binary, not the production agent runner:

```go
// cmd/sandbox: Darwin-only local sandbox runner.
func main() {
    // Parse flags: workspace, claude binary, model env, proxy env, extra env, command.
    // Compose detect profiles outside internal/seatbelt.
    // Construct seatbelt.New(...).
    // Run command via sandbox.Run(ctx, exec.Command(...)).
}
```

`cmd/sandbox` must support at minimum:

- `--workspace <path>`: required existing directory.
- `--claude-binary <path-or-name>`: optional, defaults to `claude`, resolved with `exec.LookPath` when not absolute.
- `--proxy-env KEY`: repeatable, fail if missing from host env.
- `--env KEY=value`: repeatable explicit override.
- `--anthropic-base-url`, `--anthropic-api-key`, `--anthropic-auth-token`, `--anthropic-model`, and small-model flags as convenience wrappers that populate `HarnessEnv`.
- `-- <command> [args...]`: command to run inside Seatbelt.

The old Docker-based sandbox remains in place until this binary is proven. The later production migration should add a Darwin Seatbelt adapter to `internal/sandbox` or `internal/agent`; it should not be smuggled into the security package.

Future production integration sketch:

```go
// In cmd/orqestra or internal/agent — NOT in seatbelt package
func buildSandbox(workspace string, cfg config.Config, resolved config.ResolvedModel) (*seatbelt.Sandbox, error) {
    home := os.Getenv("HOME")
    if home == "" {
        return nil, fmt.Errorf("HOME not set")
    }

    binary := "claude"
    if opts, err := cfg.RuntimeOptions(cfg.Worker.ModelRef); err == nil && opts.Binary != "" {
        binary = opts.Binary
    }

    // Mandatory
    claude, err := detect.DetectClaude(home, binary)
    if err != nil {
        return nil, err
    }
    profiles := []seatbelt.Snapshot{claude}

    // Optional tools
    optionals := []func() (*seatbelt.Snapshot, error){
        func() (*seatbelt.Snapshot, error) { return detect.DetectDocker(home) },
        func() (*seatbelt.Snapshot, error) { return detect.DetectGit(home) },
        func() (*seatbelt.Snapshot, error) { return detect.DetectHomebrew(home) },
    }
    for _, fn := range optionals {
        snap, err := fn()
        if err != nil {
            return nil, err
        }
        if snap != nil {
            profiles = append(profiles, *snap)
        }
    }

    // User config (from future seatbelt sandbox config; not the current Docker config)
    if len(cfg.Seatbelt.ReadPaths) > 0 || len(cfg.Seatbelt.ExecPaths) > 0 {
        user := seatbelt.NewToolProfile("user", home)
        for _, raw := range cfg.Seatbelt.ReadPaths {
            if err := user.Allow(raw, seatbelt.Read); err != nil {
                return nil, fmt.Errorf("sandbox config read path: %w", err)
            }
        }
        for _, raw := range cfg.Seatbelt.ExecPaths {
            if err := user.Allow(raw, seatbelt.Exec); err != nil {
                return nil, fmt.Errorf("sandbox config exec path: %w", err)
            }
        }
        profiles = append(profiles, user.Snapshot())
    }

    return seatbelt.New(seatbelt.Config{
        Workspace: workspace,
        Profiles:  profiles,
        HarnessEnv: harness.BuildModelEnv(resolved, cfg.ResolveSmallModel()),
        ProxyEnv:  cfg.Seatbelt.ProxyEnv,
        ExtraEnv:  cfg.Seatbelt.ExtraEnv,
    })
}
```

---

## Test strategy

### Security property tests (deterministic, no API, no network)

| Test | What it proves |
|------|---------------|
| `TestDeny_ReadFileOutsideAllowlist` | Create real file in tempdir, not in any profile. `cat` it → must fail, content must not appear in stdout. |
| `TestDeny_WriteOutsideWorkspace` | `echo > /path` outside workspace → must fail, file must not exist after. |
| `TestDeny_ExecArbitraryBinary` | Place binary in tempdir, not in exec profile. Execute it → must fail. |
| `TestAllow_ReadFromProfile` | Create file, add to profile, `cat` it → must succeed, content visible. |
| `TestAllow_WriteInWorkspace` | `echo > workspace/file` → file exists with correct content. |
| `TestAllow_ExecFromProfile` | Copy `/bin/echo` to profiled dir, exec it → must work. |
| `TestAllow_LocalhostNetwork` | Start local TCP listener, `nc -z localhost PORT` → must succeed. |

### Rule necessity tests (each system rule proven needed by removing and testing)

| Test | Removes | Proves |
|------|---------|--------|
| `TestNecessity_EtcReadable` | — | `/bin/sh` can read `/etc/hosts` (DNS resolution needs it) |
| `TestNecessity_EtcNotExecutable` | — | Cannot exec `/private/etc/hosts` directly |
| `TestNecessity_LibraryFrameworks` | — | Processes can link against system frameworks |
| `TestNecessity_MachLookup` | — | Keychain access works (needed for OAuth) |
| `TestNecessity_TmpWritable` | — | Process can write to `/tmp` |
| `TestNecessity_ProcessFork` | — | Shell can spawn subprocesses |

### Sandbox Tracing & Debugging (`scripts/seatbelt-trace.sh`)

Debugging Apple's Sandbox requires exact visibility into kernel denials. The script `scripts/seatbelt-trace.sh` acts as our lens.
When a tool (Claude, Git, NPM) fails unexpectedly or silently within the sandbox:

1. Run `seatbelt-trace.sh` in a parallel terminal.
2. Execute the failing tool or integration test.
3. Observe the exact source and path of the denial. Is it `node` trying to read `~/.npminfo`? Is it an embedded `python3` script trying to traverse `/usr/local`? Is it `git` looking for a global hooks directory?
4. Make an **explicit decision**: Do we adjust the tool's profile to allow this, or is the sandbox working as intended? A sandbox is a sandbox; we cannot and will not grant everything blindly. The exact source of the denial **MUST BE UNDERSTOOD**.

### Resource Limits & E2E Integration Tests

Unit coverage will be intentionally small because most meaningful Seatbelt guarantees depend on macOS kernel behavior, local tool installations, auth state, and process lifecycle. The correct proof is deterministic unit/property tests for pure code plus exhaustive setup-bound integration tests before code freeze.

For pure code, test `ResolvePath`, `ToolProfile`, `Snapshot`, env merging, SBPL rendering, config validation, and command wrapping without relying on external tools. For kernel/process behavior, test `sandbox.Run()` using standard UNIX utilities (`sh`, `sleep`, `python3`) and bounded hostile payloads. Tests that assert timeout cleanup, process-group kills, or no-zombie behavior MUST use `sandbox.Run()` because `sandbox.Wrap()` only prepares a command.

Full E2E integration tests are opt-in and setup-bound. They run real OS process calls (`claude`, `git`, `brew`, Docker CLI when installed) through `cmd/sandbox` and the exact profiles generated by `detect`. These tests should be exhaustive on the maintainer machine before freeze, but they must skip with explicit reasons when required binaries, auth, local model endpoints, or macOS Seatbelt features are unavailable.

| Test Name | Edge Case Attempt | Explicit Assertion Required |
|-----------|-------------------|---------------------------|
| **`TestProfile_Compilation`** | Mutate a `Snapshot` slices. | The compiled SBPL must not reflect the mutation. Proves Profile encapsulation invariants. |
| **`TestEnv_HarnessEnvPreserved`** | Scrub env while passing current `harness.BuildModelEnv` output. | All model routing, auth, small-model, and traffic-disable vars remain present with correct override order. |
| **`TestLimits_RlimitNoFile`** | `sandbox.Run()` inline script repeatedly opening `/dev/null` in a loop. | Must fail with `EMFILE` at or near limit. Proves we prevent host FD exhaustion. |
| **`TestLimits_RlimitNproc`** | `sandbox.Run()` inline script fork-bombing bounded `sleep` processes. | Must fail with `EAGAIN` at or near limit. Proves we prevent host OS crash. |
| **`TestLimits_ZombieReaping`**| `sandbox.Run()` script spawning child `sleep` processes. | Test triggers context cancellation. `sandbox.Run()` sends `SIGKILL` to `-PGID`; `kill -0` fails for known child PIDs. Proves no lingering descendants. |
| **`TestCmdSandbox_ClaudeVersion`** | `cmd/sandbox -- claude --version`. | Skips if Claude is absent; otherwise exits zero and prints a version. Proves binary detection, PATH, SBPL exec, env scrub, and command lifecycle. |
| **`TestCmdSandbox_GitReadonly`** | `cmd/sandbox -- git status --short` inside a temp git repo. | Exits zero and cannot read unrelated temp secrets. Proves Git profile and workspace allowlist. |
| **`TestCmdSandbox_ClaudeToolUse`** | Claude writes a file in a temp workspace using local configured endpoint/auth. | Opt-in only; proves real Claude tool execution under Seatbelt before freeze. |

### Path Resolution & Validation

We explicitly test boundary edge cases for `ResolvePath` and `p.Allow()` to guarantee 100% test coverage against encapsulation bugs.

| Test Name | Input Edge Case | Expected Assertion |
|-----------|-----------------|--------------------|
| **`TestPath_NullByteInjection`** | `/opt/dir\x00/hidden` | Must fail immediately with explicit path format error. |
| **`TestPath_MaxLengthExceeded`** | Random path string exceeding `PATH_MAX` (4096+). | Must fail cleanly with `syscall.ENAMETOOLONG` or validation error, zero panics. |
| **`TestPath_RecursiveSymlink`** | A symlink loop (A -> B -> A). | Must fail accurately with `ELOOP`. |
| **`TestPath_DirectoryTraversal`**| `~/../../../../etc/passwd` | Must resolve cleanly to the absolute canonical `/etc/passwd`. |
| **`TestPath_EmptyString`**       | `""` | Must fail immediately. |
| **`TestPath_BrokenSymlink`**     | Path traversing through a dangling symlink. | Must fail gracefully with `os.ErrNotExist`. |
| **`TestPath_RelativePath`**      | `some/relative/file` | Must fail explicitly (requires absolute anchors). |

*(Note: NO DESTRUCTIVE TESTS exist in this suite. It is okay to test read denies on `~/.ssh/id_*`, but it is NOT OKAY to do `echo "" > /etc/hosts`).*

## Endgame

1. **`cmd/sandbox` runs Claude successfully under Seatbelt** using the maintainer's configured local model endpoint.
    - **Launch Details:** The maintainer may set `ANTHROPIC_BASE_URL=http://192.168.50.212:11434` to direct Claude traffic to the local `llama-server` proxy, but the binary must accept this as configuration rather than hard-coding the LAN address.
    - It is using tools successfully and accomplishes complex development tasks under supervision.
    - All work is checked and vetted. All Claude logs are successfully read, observed, and validated.
    - `scripts/seatbelt-trace.sh` MUST be utilized to observe Claude behavior. All true-negative and false-negative sandbox violations must be actively reworked. *The source of any deny MUST BE UNDERSTOOD* (is it git? Claude? node? or forked python3?). Profiles must be adjusted (or explicitly NOT adjusted) accordingly. We cannot grant everything blindly.

2. **All deterministic structural tests pass**, including pure path/profile/env/SBPL tests and bounded process-limit tests that cleanly pass locally without damaging the host.

3. **All available integration tests are run exhaustively on the maintainer setup**, including `cmd/sandbox`, Claude, Git, Homebrew, Docker CLI if installed, trace-driven denial review, and local model routing. Each skipped integration test must print a specific missing prerequisite. After this exhaustive run passes and denials are understood, freeze the Seatbelt code for this phase.

4. **Docker sandbox remains legacy**, with no new feature work added there unless needed to keep existing production Orqestra flows running. The next phase is migration from Docker to Seatbelt, not continued parallel investment.

---

## Execution order

1. Nuke `internal/seatbelt/` completely
2. `path.go` — `Path`, `ResolvePath` (unchanged logic)
3. `profile.go` — `Permission`, `ToolProfile` (with ENV accumulation logic), `Allow()`, `AddEnv()`, `Snapshot` (deep copying slices and maps)
4. `builder.go` — `ProfileBuilder`, corrected system base, SBPL emission from Snapshots
5. `sandbox.go` — `Config` (with exact environment scrubs, `Setpgid` limits, explicitly setting `RLIMIT_NOFILE` and `RLIMIT_NPROC`), `New`, `Wrap`, `Run`
6. `env.go` — `BaseEnv` (merges `ToolProfile` variables + explicitly allowed global proxies)
7. `internal/seatbelt/detect/detect.go` — all detection logic
8. `internal/seatbelt/detect/detect_test.go` — comprehensive unit tests for `Detect*` using `t.TempDir` to simulate environments and guarantee coverage.
9. `cmd/sandbox/main.go` — Darwin-only current-phase binary for local Seatbelt execution.
10. `sandbox_e2e_test.go` — opt-in setup-bound E2E integration tests. Evaluates real `claude`, `git`, `brew`, or Docker CLI calls through `cmd/sandbox` and `sandbox.Run()`.
11. `sandbox_test.go` — security property tests, internal state checks, env merge checks, and bounded `RLIMIT` testing (evaluating bounds dynamically via inline scripts passed through `Run`).
12. Update trace script to invoke `cmd/sandbox` instead of maintaining a parallel hand-written SBPL profile.
13. Run trace → confirm no regressions and understand all denials.
14. Exhaustively run all available integration tests on the maintainer setup.
15. Code freeze Seatbelt v2 phase.
16. Future phase: migrate production Orqestra from Docker sandbox to Seatbelt adapter and then remove the old Docker path.
