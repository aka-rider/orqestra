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
   - `InputDetector` struct holding pattern list and VT buffer reference.
   - `NewInputDetector() *InputDetector` — initializes with default prompt patterns.
   - `Check(screenBuffer string, cursorRow, cursorCol int) bool`:
     - Pattern matching: scan recent VT buffer for Claude Code prompts:
       - `"Do you want to proceed?"`, `"(y/n)"`, `"Allow"`, `"[Y/n]"`, `"[y/N]"`
       - Prompt-like suffixes: lines ending in `?`, `>`, `:`
     - Cursor position: cursor at end of a line matching a prompt-like pattern.
     - Return true if any heuristic matches.
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

3. Do NOT use time-based heuristics (idle timeout). Claude delays are common and must not trigger false positives. Detection is purely deterministic based on buffer content.

## Acceptance

- `go test ./internal/harness/ -run TestInputDetector` passes.
- `go vet ./internal/harness/` clean.
- No time-based detection logic (no `time.After`, no idle timers).
- Zero false positives on the "Processing files..." and "Searching codebase..." test cases.
- Files touched: ONLY `internal/harness/input_detector.go`, `internal/harness/input_detector_test.go`.
