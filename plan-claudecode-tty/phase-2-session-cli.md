# Work Package: session-cli

| Field | Value |
|-------|-------|
| **ID** | `session-cli` |
| **Wave** | 8 |
| **depends_on** | `session-naming`, `artifact-system` |
| **Files** | `cmd/orqestra/main.go`, `cmd/orqestra/sessions.go`, `cmd/orqestra/sessions_test.go`, `internal/config/config.go` |

## Goal

Implement session management CLI subcommands for listing, inspecting, and pruning session directories.

## Steps

1. Create `cmd/orqestra/sessions.go` and hook it into `cmd/orqestra/main.go`:
   - Extend `internal/config/config.go` to explicitly include `Session.BasePath`. If missing, fallback to `./.orqestra/sessions/`.
   - `orqestra sessions list` — list session directories under the configured base path, strictly sorted by the timestamp prefix in the directory name.
   - `orqestra sessions inspect <name>` — show pipeline trace for a session by exclusively using `sandbox.ReadArtifact` to parse the `output.md` artifacts per step to safely extract status and duration from the struct.
   - `orqestra sessions prune --older-than <duration>` — delete session directories. Must check `isatty.IsTerminal(os.Stdin.Fd())` and fail proactively if ran without `--force` in a non-TTY CI environment.

2. Session directory discovery:
   - Scan configured base path for directories matching session name format.
   - Parse timestamp from directory name for age-based operations.

3. Create `cmd/orqestra/sessions_test.go`:
   - Test list: create temp session dirs, assert listed in order.
   - Test inspect: create session with known artifacts, assert trace output.
   - Test prune: create old + new sessions, prune old, assert only new remains.
   - Test prune without --force: assert prompts for confirmation.

## Acceptance

- `go test ./cmd/orqestra/ -run TestSessions` passes., sorted by timestamp via name format.
- `orqestra sessions inspect <name>` shows step-by-step trace safely derived from Artifact struct.
- `orqestra sessions prune --older-than 0s --force` removes all sessions (in test with temp dirs).
- Prune without `--force` requires TTY confirmation (fails fast if no TTY).
- Files touched: ONLY `cmd/orqestra/main.go`, `cmd/orqestra/sessions.go`, `cmd/orqestra/sessions_test.go`, `internal/config/config
- Files touched: ONLY `cmd/orqestra/sessions.go`, `cmd/orqestra/sessions_test.go`.
