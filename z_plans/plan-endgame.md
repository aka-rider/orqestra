# Orqestra Plan

## ValidationVerdict

fail

## ValidationSummary

The previous plan described the intended architecture, but a worker could not execute it without clarification because it referenced packages that do not exist in this checkout, mixed design notes with implementation steps, and kept execution-critical boundaries outside the worker-visible goal, steps, and acceptance fields.

## ValidationIssues

1. GOAL_CLARITY: The previous goal was spread across the title, TL;DR, and core insight instead of one concrete sentence.
2. STEP_SPECIFICITY: Several implementation bullets used broad phrases such as "Implement", "Extend", and "Develop" without naming the exact current files to edit.
3. WORKER_VISIBILITY: Critical boundaries, including "YAML controls parameters, not topology" and "execution-critical boundaries must appear in steps or acceptance", were present in prose but not repeated in worker-visible steps or acceptance.
4. CONTRADICTIONS: The previous current-codebase section named split packages such as `internal/planner`, `internal/pm`, `internal/qa`, `internal/types`, and `internal/validator`, but this checkout currently stores those concepts under `internal/agent`.
5. VALIDATION_COMMANDS: The previous verification section included useful commands, but it did not define them as a concrete command list for automated validation.

## SchemaVersion

1

## Goal

Implement Orqestra v3 as a programmatic Claude harness pipeline with typed stream events, a hardcoded Go orchestrator, and a Bubble Tea dashboard wired into `cmd/orqestra/main.go`.

## Context

The current repository uses `internal/agent` for planner, project manager, intent, plan validation, worker execution, QA validation, and session artifacts; `internal/harness/claude_cli.go` wraps Claude CLI calls; `internal/config/config.go` still exposes dynamic `ExecutionGraphConfig`; `cmd/orqestra/main.go` has headless command paths and a TUI stub; `go.mod` currently lacks Bubble Tea dependencies.

## Steps

1. Add `internal/harness/output.go` and `internal/harness/output_test.go` with `ParseLLMOutput(raw string, target any) error` that strips markdown fences, unwraps Claude JSON result envelopes, unmarshals into `target`, and returns wrapped parse errors containing the operation name and raw payload.
2. Replace duplicated fence and envelope parsing in `internal/agent/planner.go`, `internal/agent/pm.go`, `internal/agent/qa.go`, and `internal/agent/intent.go` with calls to `harness.ParseLLMOutput`, while preserving `agent.Specification.UnmarshalJSON` and `parseFlexibleSpec` behavior for flexible planner output.
3. Add `internal/harness/query.go` and `internal/harness/query_test.go` defining `QueryConfig`, `StreamEvent`, `TextDelta`, `ToolUse`, `ToolResult`, `UsageDelta`, `Result`, and `ErrorEvent`, and implement `Query(ctx, QueryConfig) (<-chan StreamEvent, error)` to launch `claude --print --output-format stream-json --verbose` with `--max-turns`, `--allowedTools`, `--disallowedTools`, `--continue`, `--system-prompt`, `WorkDir`, and `Env` from `QueryConfig`.
4. Implement the stream-json scanner in `internal/harness/query.go` with a 1 MiB scanner buffer, JSON-line decoding for Claude `assistant`, `content_block_delta`, `tool_use`, `tool_result`, `result`, and `usage` shapes, and an `ErrorEvent` sent before channel close when the process, scanner, or JSON parsing fails.
5. Add `internal/harness/ringbuf.go`, `internal/harness/ringbuf_test.go`, `internal/harness/stats.go`, and `internal/harness/stats_test.go` with a mutex-protected circular buffer for the last 1000 `StreamEvent` values and an `AgentStats` type that updates state, token counts, token rate, session ID, turn count, tool call summaries, and context percentage from `StreamEvent` values.
6. Update `internal/config/config.go`, `internal/config/pipeline.yaml`, and `internal/config/config_test.go` by adding `PipelineConfig{TokenBudget int64, RunDir string, WorkerConcurrency int}`, adding `MaxTurns int` and `DisallowedTools []string` to worker-capable agent config, adding `ContextWindow int64` to `ModelConfig`, and removing `ExecutionGraphConfig`, `AgentNodeConfig`, `ValidatorNodeConfig`, and validation logic for `execution_graph`.
7. Keep `internal/scheduler` files compiling but detach `cmd/orqestra/main.go` and `internal/config` from dynamic `execution_graph` configuration; do not delete `internal/scheduler` in this plan.
8. Add `internal/agent/run_dir.go` and `internal/agent/run_dir_test.go` by renaming the behavior of `SessionDir` into `RunDir`, writing runs under `Config.Pipeline.RunDir`, and preserving artifact read and write error wrapping; update existing callers in `cmd/orqestra/main.go` to use `RunDir` names.
9. Add `internal/orchestrator/orchestrator.go` and `internal/orchestrator/orchestrator_test.go` with a typed `Engine.Run(ctx, input, emit)` pipeline ordered as intent, planner, plan validator, project manager, dependency-wave workers, and QA gate, using `agent.Recognizer`, `agent.Planner`, `agent.PlanValidator`, `agent.ProjectManager`, `agent.WorkPackage.ToSpecification`, `agent.TopoWaves`, `harness.CLIRunner`, and `agent.Gate` from the current `internal/agent` package.
10. In `internal/orchestrator/orchestrator.go`, run worker dependency waves with `errgroup.WithContext(ctx)`, copy each loop variable before `g.Go`, stop the pipeline on the first worker error returned by `g.Wait`, and return an error that includes the failed package ID.
11. In `internal/orchestrator/orchestrator.go`, persist `specification.json`, `project_plan.json`, `validation_report.json`, `qa_report.json`, and `summary.json` into the `RunDir` using `encoding/json` after each stage completes; return the first write error with the artifact path in the error message.
12. Add Bubble Tea dependencies by importing `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles/textarea`, and `github.com/charmbracelet/lipgloss` from new `internal/tui` files, then run `go mod tidy` to update `go.mod` and `go.sum`.
13. Add `internal/tui/app.go`, `internal/tui/screens.go`, `internal/tui/styles.go`, and `internal/tui/app_test.go` implementing Bubble Tea screens for prompt entry, clarification, dashboard, agent detail, plan review, failure action, QA result, and completion summary with a fixed header, scrollable content area, fixed input line, and fixed key legend.
14. Implement `internal/tui.Model.Update` handlers for `Enter`, arrow keys, `Space`, `Tab`, `Esc`, `S`, `F`, `Y`, `N`, `E`, `R`, `A`, `D`, `Q`, `Ctrl+C`, and `Ctrl+D`, including double `Ctrl+C` and double `Ctrl+D` exit confirmation text in the key legend.
15. Add `cmd/tui_test/main.go` that constructs a mock `internal/tui` model, sends mock orchestrator events through a channel, and exercises screens 1 through 8 without calling Claude or touching the network.
16. Update `cmd/orqestra/main.go` so the no-argument interactive path constructs the orchestrator engine, starts `orchestrator.Engine.Run` in a goroutine, sends orchestrator updates to the Bubble Tea program with `Program.Send`, and runs `tea.NewProgram(model, tea.WithAltScreen())` on the main goroutine.
17. Keep the existing `cmd/orqestra/main.go` headless subcommands `plan`, `validate`, `exec`, `usage`, `reset-usage`, and `--plan` working with the new config fields and without requiring a TUI terminal.
18. Update tests in `internal/agent`, `internal/config`, `internal/harness`, `internal/orchestrator`, `internal/tui`, and `cmd/orqestra` so they use current package paths under `internal/agent` and do not import nonexistent packages such as `internal/types`, `internal/planner`, `internal/pm`, `internal/qa`, or `internal/validator`.

## Acceptance

1. `internal/harness/output_test.go` verifies that `ParseLLMOutput` parses direct JSON, fenced JSON, and Claude `{"type":"result","result":"..."}` envelopes into `agent.Specification` and `agent.ValidationReport` values.
2. `internal/harness/query_test.go` verifies that a hardcoded stream-json fixture emits `TextDelta`, `ToolUse`, `ToolResult`, `UsageDelta`, `Result`, and `ErrorEvent` values without invoking the real `claude` binary.
3. `internal/harness/ringbuf_test.go` verifies that the ring buffer retains exactly the newest 1000 stream events after more than 1000 appends.
4. `internal/harness/stats_test.go` verifies that `AgentStats` reports running, done, failed, token totals, token rate, context percentage from `ModelConfig.ContextWindow`, session ID, turn count, and tool summaries from stream events.
5. `internal/config/config_test.go` verifies that `pipeline.token_budget`, `pipeline.run_dir`, `pipeline.worker_concurrency`, worker `max_turns`, worker `disallowed_tools`, and model `context_window` load from YAML and that an invalid `model_ref` returns an error during `config.Load`.
6. `go test ./internal/config` passes without any references to `ExecutionGraphConfig`, `AgentNodeConfig`, `ValidatorNodeConfig`, or YAML key `execution_graph`.
7. `internal/orchestrator/orchestrator_test.go` verifies that the engine runs intent, planner, validator, project manager, two worker dependency waves, and QA in order using mocks.
8. `internal/orchestrator/orchestrator_test.go` verifies that `errgroup.WithContext` waits for all workers in the current wave and prevents later waves and QA from running after any worker returns an error.
9. `internal/orchestrator/orchestrator_test.go` verifies that the engine writes `specification.json`, `project_plan.json`, `validation_report.json`, `qa_report.json`, and `summary.json` under the configured run directory.
10. `internal/tui/app_test.go` verifies that key handling moves through prompt entry, clarification, dashboard, agent detail, plan review, failure action, QA result, and completion summary without panics.
11. `cmd/tui_test/main.go` runs with `go run ./cmd/tui_test` and displays mock data without invoking Claude, reading provider API keys, or modifying repository files.
12. `cmd/orqestra/main.go` no longer prints "interactive TUI is not yet implemented in v3" when run with no subcommand in a TTY.
13. `cmd/orqestra/main.go` still supports `orqestra plan <prompt>`, `orqestra validate <spec-json-file>`, `orqestra exec <spec-json-file>`, `orqestra usage`, `orqestra reset-usage`, and `orqestra --plan <file.md>`.
14. `go test ./internal/harness` passes.
15. `go test ./internal/agent` passes.
16. `go test ./internal/orchestrator` passes.
17. `go test ./internal/tui` passes.
18. `go test ./cmd/orqestra` passes if command package tests exist; if no command package tests exist, `go test ./cmd/orqestra` exits 0 with no test files.
19. `go build ./cmd/orqestra` exits 0.
20. `go test ./...` exits 0.
21. No directories named `internal/planner`, `internal/pm`, `internal/qa`, `internal/types`, or `internal/validator` exist after implementation.
22. The interactive TUI path in `cmd/orqestra/main.go` does not contain stdin `[y/N]` approval prompts and does not import `creack/pty`.

## Constraints

1. Do not create split packages named `internal/planner`, `internal/pm`, `internal/qa`, `internal/types`, or `internal/validator`; use the current `internal/agent` package for those types and constructors.
2. Do not reintroduce `creack/pty`, old terminal passthrough mux code, OpenAI raw HTTP harness clients, or stdin `[y/N]` gates in the interactive TUI path.
3. Do not delete `internal/scheduler` in this plan; only remove dynamic execution graph configuration from `internal/config` and stop using it from the v3 orchestrator path.
4. Do not silently fall back to another model, provider, config path, run directory, or Claude binary when the user or config specifies one that cannot be resolved.
5. Do not place execution-critical boundaries only in `Context`, `Constraints`, `Risks`, `ValidationCommands`, or `ExpectedArtifacts`; steps and acceptance must repeat critical file boundaries and non-goals.
6. Do not use validation commands with shell operators `&&`, `||`, `|`, `;`, `>`, or `<`.
7. Do not parse YAML, JSON, or stream-json with ad hoc string slicing when `encoding/json`, `yaml.v3`, or typed structs can parse the data.
8. Do not add package-level mutable orchestration state; pass `context.Context`, config, channels, and callbacks through constructors or method arguments.
9. Do not use `time.Sleep` in tests; use channels, contexts, fake clocks, or deterministic test hooks.
10. Do not swallow errors from process execution, scanner errors, artifact writes, config loading, model resolution, or TUI startup.

## Assumptions

1. The worker can add Go dependencies with `go mod tidy` after new imports are present.
2. The Claude CLI stream-json fixture can be represented in tests without requiring the actual Claude binary.
3. The first implementation can keep headless commands and the interactive TUI in the same `cmd/orqestra/main.go` file before later refactoring.
4. The TUI can use mock orchestrator events for tests and `cmd/tui_test/main.go` instead of a real Claude session.

## Risks

1. Claude CLI stream-json event shapes may differ by version; `internal/harness/query.go` must pass through unknown event types as debug-safe `ErrorEvent` or ignore them with tests documenting the behavior.
2. Removing `ExecutionGraphConfig` from `internal/config` may break tests or YAML files that still contain `execution_graph`; update fixtures and repository configs in the same change.
3. Bubble Tea keyboard behavior differs across terminals; keep key handling tests at the message level and reserve full terminal behavior for manual verification.
4. Concurrent workers write to the same checkout; `internal/orchestrator` must rely on project-manager package boundaries and fail on worker errors rather than merging conflicting edits silently.

## ValidationCommands

```sh
go test ./internal/harness
go test ./internal/agent
go test ./internal/config
go test ./internal/orchestrator
go test ./internal/tui
go test ./cmd/orqestra
go run ./cmd/tui_test
go build ./cmd/orqestra
go test ./...
```

## ExpectedArtifacts

1. `internal/harness/output.go`
2. `internal/harness/output_test.go`
3. `internal/harness/query.go`
4. `internal/harness/query_test.go`
5. `internal/harness/ringbuf.go`
6. `internal/harness/ringbuf_test.go`
7. `internal/harness/stats.go`
8. `internal/harness/stats_test.go`
9. `internal/config/config.go`
10. `internal/config/pipeline.yaml`
11. `internal/config/config_test.go`
12. `internal/agent/run_dir.go`
13. `internal/agent/run_dir_test.go`
14. `internal/orchestrator/orchestrator.go`
15. `internal/orchestrator/orchestrator_test.go`
16. `internal/tui/app.go`
17. `internal/tui/screens.go`
18. `internal/tui/styles.go`
19. `internal/tui/app_test.go`
20. `cmd/tui_test/main.go`
21. `cmd/orqestra/main.go`
22. `go.mod`
23. `go.sum`
