# Plan: Fix TUI Silently Showing Blank Completion Screen on Pipeline Failure

## Context

In the run `.orqestra/sessions/2026-06-17-160711-run`, the researcher agent consumed ~1M input tokens and exited without writing a plan file. The orchestrator correctly detected this. The TUI woke up on `obsNotifyMsg`, called `ApplySnapshot()`, transitioned to `ContentCompletion`, and then showed nothing — no error, no status — because `s.lastErr` was never set.

**The orchestrator did not hang.** All cleanup ran correctly. This is purely a TUI rendering bug.

## Root Cause

`internal/tui/screen_pipeline.go`, `ApplySnapshot()`:

Both `viewCompletion()` and `buildCompletionSummary()` gate their error output on `s.lastErr != nil`. Since `lastErr` stays nil, both render nothing.

## Fix

In `ApplySnapshot()`, add `s.lastErr = snap.Terminal.Err` before `AppendFrame` so the frame summary also captures the error.

## Verification

- `make build` passes
- `make test` passes
- `make run` shows the error message on pipeline failure
