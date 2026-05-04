# Work Package: session-naming

| Field | Value |
|-------|-------|
| **ID** | `session-naming` |
| **Wave** | 1 |
| **depends_on** | — |
| **Files** | `internal/sandbox/session_name.go`, `internal/sandbox/session_name_test.go` |

## Goal

Implement session naming and directory creation for orchestrated agent sessions.

## Steps

1. Create `internal/sandbox/session_name.go` with:
   - Two word lists (~100 adjectives, ~100 nouns) as package-level `var` slices.
   - `GenerateSessionName() string` — format: `2026-05-04T16-42-07-amber-serpent` (timestamp prefix + random adjective-noun).
   - `CreateSessionDir(basePath string) (string, error)` — creates the directory tree under basePath using the generated name.

2. Create `internal/sandbox/session_name_test.go` with table-driven tests:
   - Format validation: output matches regex `^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-[a-z]+-[a-z]+$`.
   - Uniqueness: 1000 sequential calls produce no duplicates.
   - `CreateSessionDir`: directory exists after call, cleanup via `t.TempDir()`.
   - Error case: basePath that doesn't exist returns wrapped error.

## Acceptance

- `go test ./internal/sandbox/ -run TestSessionName` passes.
- `go vet ./internal/sandbox/` clean.
- No dependencies added to `go.mod` (uses only stdlib `crypto/rand`, `time`, `os`, `fmt`).
- Files touched: ONLY `internal/sandbox/session_name.go` and `internal/sandbox/session_name_test.go`.
