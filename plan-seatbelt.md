# Plan: Seatbelt PoC — `internal/seatbelt`

## Goal

Run `claude` CLI inside `sandbox-exec` on macOS. Profile starts permissive (paulsmith-level, including `(allow network*)`), then hardens iteratively using trace data.

PoC success = `claude -p "echo hello" --output-format stream-json` produces valid output inside the sandbox with:

- FS writes confined to workspace + /tmp
- Sensitive dirs (~/.ssh, ~/.aws etc) inaccessible
- Clean env (no credential leakage via environment)

Network hardening is Phase 2 (after basic profile works).

---

## Deliverables

### File: `internal/seatbelt/profile.go` (`//go:build darwin`)

```go
type ProfileConfig struct {
    Workspace      string   // absolute resolved path (mandatory)
    Home           string   // $HOME (mandatory)
    HomebrewPrefix string   // /opt/homebrew or /usr/local (detected)
    ExtraReadPaths []string // e.g. nix store, .local
}

func GenerateProfile(cfg ProfileConfig) (string, error)
func DetectHomebrewPrefix() string
func DetectExtraReadPaths(home string) []string
```

GenerateProfile writes the SBPL string with:

- paulsmith-level permissive system reads
- `(allow network*)` (Phase 1 — works, not hardened)
- workspace RW
- deny ~/Documents, ~/Desktop, ~/Downloads, ~/Pictures, ~/Movies, ~/Music
- deny ~/.ssh, ~/.aws, ~/.gnupg, ~/.kube, ~/.docker, ~/.config
- deny message tagged "orqestra" for log stream filtering

### File: `internal/seatbelt/env.go` (`//go:build darwin`)

```go
// BaseEnv returns a minimal clean environment built from scratch.
// Nothing from the host process leaks unless explicitly injected via extra.
func BaseEnv(workspace string) []string

// WithVars appends key=value pairs to an existing env slice.
// Used to inject harness-specific vars (ANTHROPIC_MODEL, etc.) at call time.
func WithVars(base []string, extra map[string]string) []string
```

BaseEnv constructs from scratch, preserving essential XPC variables:

```go
// Inherit XPC_* to allow IPC with system daemons
env := []string{
    "PATH=/usr/bin:/bin:/usr/sbin:/sbin:" + homebrewBin,
    "HOME=" + home,
    "TMPDIR=" + realTmpDir, // resolved via filepath.EvalSymlinks
    "LANG=en_US.UTF-8",
    "TERM=xterm-256color",
    "USER=" + user,
    "SHELL=/bin/bash",
}
// Append XPC_SERVICE_NAME and XPC_FLAGS from os.Environ if present
```

The harness layer calls `WithVars(base, map[string]string{"ANTHROPIC_API_KEY": key, ...})` to inject only what's needed for the specific run. No ambient credentials ever enter the sandbox.

### File: `internal/seatbelt/sandbox.go` (`//go:build darwin`)

```go
type Config struct {
    Workspace string            // absolute path, mandatory
    ExtraEnv  map[string]string // injected on top of BaseEnv (e.g. ANTHROPIC_API_KEY)
}

type Sandbox struct { ... unexported fields ... }

func New(cfg Config) (*Sandbox, error)  // validates workspace exists, detects homebrew/paths, verifies sandbox-exec exists and warns if deprecated
func (s *Sandbox) Exec(ctx context.Context, command []string, stdout io.Writer) (exitCode int, err error)
```

Exec:

1. Validates `TMPDIR` via `filepath.EvalSymlinks(os.TempDir())`
2. Calls GenerateProfile with detected paths
3. Writes profile to os.CreateTemp (0400 permissions)
4. Builds command: `sandbox-exec -f <tmpfile> command[0] command[1:]...`
5. Sets cmd.Env = WithVars(BaseEnv(workspace), cfg.ExtraEnv) — clean env, inheriting only `XPC_*`
6. Sets cmd.Dir = workspace
7. Configures `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` and manually proxies signals to kill the entire process group for clean context cancellation.
8. Pipes stdout, captures stderr
9. Defers profile temp file removal
10. Returns exit code

### File: `internal/seatbelt/sandbox_test.go` (`//go:build darwin`)

Table-driven tests:

- `TestBaseEnv`: contains exactly PATH/HOME/TMPDIR/LANG/TERM/USER/SHELL, nothing else
- `TestBaseEnv_NoLeak`: os.Setenv("AWS_SECRET_ACCESS_KEY", ...) → not in BaseEnv output
- `TestWithVars`: injects ANTHROPIC_API_KEY into base, verify present
- `TestGenerateProfile`: output contains workspace subpath, deny rules, tagged deny message
- `TestDetectHomebrewPrefix`: returns /opt/homebrew or /usr/local based on existence
- `TestExec_WriteToWorkspace`: `echo test > workspace/file.txt` succeeds
- `TestExec_WriteOutsideWorkspace`: `touch /etc/pwned` fails with non-zero exit
- `TestExec_ReadDeniedPath`: `cat ~/.ssh/id_rsa` fails
- `TestExec_DeviceAccess`: `cat /dev/disk0` fails
- `TestExec_EnvNotLeaked`: `env` output inside sandbox contains no AWS/GH/OPENAI vars

---

## SBPL Profile Content (Phase 1 — hardened for macOS & Node.js)

```scheme
(version 1)
(deny default (with message "orqestra"))
(allow file-read-metadata)
(allow sysctl-read)

;; Broad macOS system reads
(allow file-read* (literal "/"))
(allow file-read* (literal "/private"))
(allow file-read*
  (subpath "/private/var/db/timezone")
  (subpath "/Library/Preferences")
  (subpath "/Library/Keychains")
  (subpath "{Home}/Library/Preferences"))

;; Essential Node.js and System Executables
(allow file-read* file-map-executable
  (subpath "/System")
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/Library/Frameworks")
  (subpath "/private/etc")
  (subpath "/var/db/dyld")
  (subpath "{HomebrewPrefix}/bin")
  (subpath "{HomebrewPrefix}/opt")
  (subpath "{HomebrewPrefix}/lib"))

(allow process-exec
  (subpath "/usr")
  (subpath "/System")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "{HomebrewPrefix}/bin")
  (subpath "{Workspace}"))
(allow process-fork)

;; Tmp and device access
(allow file-read* file-write*
  (subpath "/tmp")
  (subpath "{TmpDirResolved}"))
(allow file-read* file-write*
  (regex "^/dev/(tty.*|null|zero|dtracehelper)"))
(allow file-ioctl
  (literal "/dev/dtracehelper")
  (regex "^/dev/tty.*"))

;; Selective Home Directory Reads (Git, NPM, Claude)
(allow file-read* file-write*
  (literal "{Home}/.claude.json")
  (subpath "{Home}/.claude"))
(allow file-read*
  (literal "{Home}/.gitconfig")
  (subpath "{Home}/.config/git")
  (literal "{Home}/.ssh/known_hosts")
  (literal "{Home}/.npmrc")
  (subpath "{Home}/.npm")
  (subpath "{Home}/.nvm"))

;; Mach services for DNS, Networking, and Security Certificates
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.SystemConfiguration.DNSConfiguration")
  (global-name "com.apple.coreservices.launchservicesd")
  (global-name "com.apple.CoreServices.coreservicesd")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.logd")
  (global-name "com.apple.diagnosticd")
  (global-name "com.apple.lsd.mapdb")
  (global-name "com.apple.lsd.modifydb")
  (global-name "com.apple.coreservices.quarantine-resolver"))
(allow mach-lookup
  (regex "^com\\.apple\\.lsd(\\..*)?$"))
(allow ipc-posix-shm-read-data
  (ipc-posix-name "apple.shm.notification_center"))
(allow file-read*
  (subpath "/private/var/run/mDNSResponder"))

;; Network
(allow network*)
(allow lsopen)

;; --- DENY SPECIFIC SENSITIVE PATHS (wins over prior allows) ---
(deny file-read* file-write*
  (subpath "{Home}/Documents")
  (subpath "{Home}/Desktop")
  (subpath "{Home}/Downloads")
  (subpath "{Home}/Pictures")
  (subpath "{Home}/Movies")
  (subpath "{Home}/Music")
  ;; Strict deny on sensitive credential dirs, ignoring the files allowed explicitly above where rules prioritize cleanly
  (regex "^{HomeEscaped}/\\.(ssh|aws|gnupg|kube|docker|config)($|/)"))

;; --- EXPLICIT WORKSPACE ALLOW (wins over EVERYTHING because evaluated last) ---
(allow file-read* file-write* file-map-executable
  (subpath "{Workspace}"))
```

---

## Integration Point (not in PoC, for later)

In `internal/harness/claude_cli.go`, the exec call:

```go
cmd := exec.CommandContext(ctx, c.binary, args...)
```

Becomes:

```go
cmd := exec.CommandContext(ctx, "sandbox-exec", append([]string{"-f", profilePath, c.binary}, args...)...)
```

The stdout/scanner infrastructure is unchanged — sandbox-exec is transparent to stdio.

---

## Verification

1. `go test ./internal/seatbelt/ -v -count=1` — all unit + integration tests pass on macOS
2. Manual trace run: `sandbox-exec -D -f /tmp/orqestra.sb claude -p "list files" --output-format json` — confirm output
3. `log stream --predicate 'eventMessage contains "orqestra"' --style compact` — see deny violations for .ssh/.aws

---

## Decisions

- Phase 1 allows full network (otherwise CC won't resolve DNS, won't work at all)
- Profile rules place broad allows first, specific denies second, and the crucial Workspace allow absolutely **last** (Seatbelt rules evaluate bottom-up/last-match-wins)
- Env is built from scratch with inherited `XPC_*` variables — defense in depth
- `sandbox-exec` is deprecated, so a validation check ensures it functions at startup before risking sandbox escapes or failures
- No new interface — just a struct with Exec(). Integration with harness layer is a later PR.

## Out of Scope

- Network hardening (Phase 2)
- git-based change extraction
- Replacing the Docker runner
- Implementing `sandbox.Sandbox` interface
