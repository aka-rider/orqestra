# Work Package: session-cli

| Field | Value |
|-------|-------|
| **ID** | `session-cli` |
| **Wave** | 8 |
| **depends_on** | `session-naming`, `artifact-system` |
| **Files** | `cmd/orqestra/sessions.go`, `cmd/orqestra/sessions_test.go` |

## Goal

Implement session management CLI subcommands for listing, inspecting, and pruning session directories.

## Steps

1. Create `cmd/orqestra/sessions.go` (or appropriate CLI entry point):
   - `orqestra sessions list` — list session directories under the configured session base path, sorted by timestamp. Show: name, date, step count, status (complete/partial).
   - `orqestra sessions inspect <name>` — show pipeline trace for a session: each step's name, artifact presence (input.md, output.md), duration if available, status.
   - `orqestra sessions prune --older-than <duration>` — delete session directories older than the given duration (e.g., `7d`, `24h`). Require confirmation unless `--force` is passed.

2. Session directory discovery:
   - Scan configured base path for directories matching session name format.
   - Parse timestamp from directory name for age-based operations.

3. Create `cmd/orqestra/sessions_test.go`:
   - Test list: create temp session dirs, assert listed in order.
   - Test inspect: create session with known artifacts, assert trace output.
   - Test prune: create old + new sessions, prune old, assert only new remains.
   - Test prune without --force: assert prompts for confirmation.

## Acceptance

- `go test ./cmd/orqestra/ -run TestSessions` passes.
- `orqestra sessions list` produces human-readable output.
- `orqestra sessions inspect <name>` shows step-by-step trace.
- `orqestra sessions prune --older-than 0s --force` removes all sessions (in test with temp dirs).
- Prune without `--force` requires confirmation (does not auto-delete).
- Files touched: ONLY `cmd/orqestra/sessions.go`, `cmd/orqestra/sessions_test.go`.
