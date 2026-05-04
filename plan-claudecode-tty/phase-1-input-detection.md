# Work Package: input-detection

| Field | Value |
|-------|-------|
| **ID** | `input-detection` |
| **Wave** | 4 |
| **depends_on** | — |
| **Files** | `internal/harness/input_detector.go`, `internal/harness/input_detector_test.go` |

## Goal

Implement heuristic-based detection that a PTY subprocess is waiting for user input, based on deterministic VT text matching and cursor positions (no time-based heuristics).

## Steps

1. Create `internal/harness/input_detector.go` with:
   - `InputDetector` struct holding pre-compiled `*regexp.Regexp` patterns. (Stateless regarding the buffer).
   - `NewInputDetector() *InputDetector` — initializes with default prompt patterns (e.g., `(?i)^(Select an option|Enter value).*> *$`).
   - `Check(plainLines []string, cursorRow, cursorCol int) bool`:
     - Pattern matching: scans the specific line in `plainLines` at index `cursorRow`. The `plainLines` slice must already be stripped of ANSI escape codes via `vt.String()`.
     - Generic 1-character suffix rules (`?`, `>`, `:`) are NOT allowed independently, they must be part of explicit interactive prompt regexes to prevent false positives when output pauses mid-generation.
     - Cursor validation: The cursor column must be >= the length of the matched prompt text on the current line.
     - Return true if the strict regex heuristic matches the cursor line.
   - `DetectedPattern() string` — returns the pattern that triggered the last positive detection (for diagnostics/logging).

2. Create `internal/harness/input_detector_test.go` with table-driven tests:

   | Input Buffer | Cursor Pos | Expected |
   |-------------|------------|----------|
   | `"Do you want to proceed? (y/n) "` | end of line | true |
   | `"Allow this tool to run? [Y/n] "` | end of line | true |
   | `"Enter filename: "` | end of line | true |
   | `"Processing files...\nDone."` | end of line | false |
   | `"Searching codebase... found 12 matches"` | end of line | false |
   | `"? Select an option\n> "` | end of line | true |
   | `"Error: file not found\n$ "` | end of line | true (shell prompt) |
   | Mid-output with no prompt | mid-line | false |
   | Empty buffer | 0,0 | false |
Code block generation paused mid-line ending in `:` | end of line | false |
   | Markdown string paused at `> ` | end of line | false |
   | 
3. Do NOT use time-based heuristics (idle timeout). Claude delays are common and must not trigger false positives. Detection is purely deterministic based on buffer content.

## Acceptance

- `go test ./internal/harness/ -run TestInputDetector` passes.
- `go vet ./internal/harness/` clean.
- No time-based detection logic (no `time.After`, no idle timers).
- Zero false positives on the "Processing files..." and "Searching codebase..." test cases.
- Files touched: ONLY `internal/harness/input_detector.go`, `internal/harness/input_detector_test.go`.
