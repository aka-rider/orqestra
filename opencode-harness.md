I have verified all the critical code paths. Here's the plan:

# Plan

## Goal

Implement an `opencode` harness that wraps `/opt/homebrew/bin/opencode` as a `harness.ContinuableRunner`, wire it through the orchestrator pipeline, add sandbox detection for opencode, and write integration tests verifying: (1) opencode responds to a simple prompt on local qwen3.6, (2) markdown output is stored and retrievable via the control plane, (3) opencode creates files in a sandboxed worktree and the worktree is verifiable and cleanable.

## Context

Verified codebase facts via direct reads:

**Harness interfaces** (`internal/harness/claude_cli.go:36-48`):
- `CLIRunner` — `RunPrint(ctx, prompt, systemPrompt) (RunResult, error)` and `RunStreaming(ctx, prompt, systemPrompt, events) (RunResult, error)`
- `ContinuableRunner` — extends `CLIRunner` with `RunContinue(ctx, sessionID, prompt, events) (RunResult, error)`

**RunResult** (`claude_cli.go:28-33`): `Output string`, `Usage TokenUsage`, `SessionID string`, `PlanFilePath string`

**ClaudeCLI struct** (`claude_cli.go:51-59`): uses `exec.CommandContext` with configurable `binary` field (default `"claude"`). Functional options pattern via `ClaudeCLIOption`.

**SandboxCLIRunner** (`internal/harness/sandbox_cli_runner.go:23-30,92-103`): hardcodes `"claude"` in `buildCommand()`. Uses `sandbox.New()` and `sb.Wrap()` / `sb.Run()` for seatbelt execution.

**Stream parsing** (`claude_cli.go:388-442`): `parseStream()` reads NDJSON, dispatches `StreamUpdate` events, accumulates result text, session ID, plan file path, and token usage from Claude's `streamEvent` format.

**Sandbox detect** (`internal/sandbox/detect/detect.go:21-55`): `DetectClaude()` returns `sandbox.Snapshot` with binary path, config paths, cache paths. Uses `sandbox.NewToolProfile()` and `Allow()`/`AllowOptional()`.

**Provider types** (`internal/config/config.go:22-33`): `ProviderTypeNative`, `ProviderTypeAnthropic`, `ProviderTypeOpenAI` are validated via `validProviderTypes` map. Unknown types cause config load failure.

**Builder** (`internal/sandbox/builder.go:47-167`): `ProfileBuilder.Build()` emits SBPL with tool profile entries. Tool snapshots contribute filesystem allow rules and env vars.

**Integration test pattern** (`internal/sandbox/sandbox_integration_test.go`): uses `//go:build darwin && integration`, creates temp workspace, uses `detect.DetectClaude()`, runs via `execSandbox()`, verifies output and file creation.

**Worktree** (`internal/worktree/worktree.go`): `Create(ctx, repoPath, sessionDir, runID)` creates git worktree; `Remove(ctx, force)` removes it; `CommitAll()` stages changes.

**Session artifacts** (`internal/agent/session.go`): `SessionDir` creates `.orqestra/sessions/<timestamp>-run/`; per-agent artifacts include `*_meta.json` (StepMeta), `*_session.jsonl` (Claude session log copy), and output markdown. `StepMeta` has `ClaudeSessionID`, `ClaudeProjectPath`, `ClaudeSessionLogPath`, `ClaudePlanFilePath`. `ResolveSessionLogPath` constructs `~/.claude/projects/-Users-<user>-<repo>/<session>.jsonl`.

**OpenCode SQLite database** (`~/.local/share/opencode/opencode.db`): opencode stores all session data in SQLite, NOT NDJSON. Schema:
- `session` — one row per session with `id` (text, `ses_...`), `model`, `agent`, `tokens_input`, `tokens_output`, `tokens_reasoning`, `cost`, `time_created`, `time_updated`
- `message` — one row per conversation turn with `session_id`, `data` JSON (`role`, `agent`, `tokens`, `cost`, `modelID`, `providerID`, `time.created`, `time.completed`, `finish`)
- `part` — one row per content part linked to `message` via `message_id`, with `data` JSON (`type`: `text`/`reasoning`/`tool`/`step-start`/`step-finish`, plus `text` or `tool`/`snapshot` content)
- `event` — event-sourcing backbone table (currently empty in real DB)
- `session_message` — typed session messages (`agent-switched`, etc.)

**Architectural decisions:**
1. **Opencode uses SQLite, NOT NDJSON**: opencode stores all session data in `~/.local/share/opencode/opencode.db`. `parseStream()` (Claude's NDJSON parser) is NOT reusable for opencode. We need a SQLite reader to extract session data.
2. **Opencode is a harness, NOT a provider**: Provider types (`native`, `anthropic`, `openai`) control env var routing for Claude CLI. Opencode is a separate binary — it does NOT use Claude CLI at all. No new provider type is added.
3. **Parameterize `SandboxCLIRunner` binary**: instead of creating a parallel `OpenCodeSandboxCLIRunner`, add an optional `Binary` field to `SandboxCLIRunner` so the same seatbelt runner works for any CLI binary. This avoids code duplication.
4. **Integration tests live in `internal/harness/`**: follows the pattern of `sandbox_integration_test.go` in `internal/sandbox/`. The harness package owns runner interfaces and implementations.
5. **Orchestrator wiring is in scope**: `buildEngine()` in `main.go` must be updated to select `OpenCodeCLI` when an agent's `harness` field is `"opencode"`. This is NOT excluded.
6. **Session log copying must work for opencode**: `ResolveSessionLogPath` is Claude-specific. A new resolver is needed for opencode to copy SQLite session data into the Orqestra session directory.
7. **`StepMeta` needs opencode fields**: `StepMeta` has Claude-specific fields only. Need `OpenCodeSessionID` and `OpenCodeDBPath` to surface opencode session data in the TUI.

## Constraints

- Must implement `harness.ContinuableRunner` interface exactly as defined — no signature changes.
- Must not modify `CLIRunner`, `ContinuableRunner`, `RunResult`, `TokenUsage`, or `StreamUpdate` types.
- Must not add new provider types to config — opencode uses native semantics (no env var overrides).
- All new code must use `//go:build darwin` build tag (macOS-only, like existing sandbox code).
- Integration tests must use `//go:build darwin && integration` build tag.
- Must not modify existing files outside `internal/harness/`, `internal/sandbox/detect/`, `internal/config/`, `internal/agent/`, `internal/orchestrator/`, `cmd/orqestra/`, and `internal/harness/logpath.go`.
- `StepMeta` can accept new optional fields — existing TUI rendering must not break when they're empty.
- All existing `orqestra*.yaml` configs must get `harness: claude` on every agent role to maintain backward compatibility.

## Risks

1. **Opencode CLI flags differ from Claude CLI**: Unknown exact flags. We need to verify `--help` output or test with `--print` / `-p` / `--output-format` before implementing `RunPrint`. *Mitigation*: Start with `opencode --help` to discover flags, then implement only verified flags. If opencode has no `--print` equivalent, `RunPrint` falls back to `RunStreaming` with output captured to a temp file.
2. **Opencode has no NDJSON stream**: Confirmed — opencode uses SQLite. `parseStream()` is NOT reusable. *Mitigation*: `RunStreaming` will run opencode, then after the command completes, query SQLite for the session data to populate `RunResult.Output` and `RunResult.Usage`. The `events` channel will receive synthesized `StreamUpdate` events from the SQLite data.
3. **Opencode session continuation via `--resume`**: Unknown if opencode supports `--resume <sessionID>`. *Mitigation*: Test with `opencode -s <session-id>` (opencode uses `-s` for session selection). If no CLI continuation exists, `RunContinue` will query SQLite for the session's last message and construct a continuation prompt.
4. **Sandbox profile missing opencode paths**: If `DetectOpenCode` doesn't include all needed paths (e.g., `~/.local/share/opencode/`, `~/.config/opencode/`, `~/.local/state/opencode/`), sandbox execution will fail. *Mitigation*: Include `~/.local/share/opencode/` (read+write for SQLite), `~/.config/opencode/` (read), and Homebrew cellar paths. The integration test will fail with a clear sandbox error if paths are missing.
5. **Token usage from SQLite**: `StepMeta.InputTokens` and `StepMeta.OutputTokens` must come from `session.tokens_input` / `session.tokens_output` in SQLite, not from stream events. *Mitigation*: After `RunStreaming` completes, query `SELECT tokens_input, tokens_output FROM session WHERE id = ?` to populate `RunResult.Usage`.
6. **Session log copying for opencode**: `ResolveSessionLogPath` constructs `~/.claude/projects/...` paths. For opencode, there is no Claude session log. *Mitigation*: `CopySessionLog` detects the harness type. For opencode, it copies the relevant SQLite session data (messages + parts) as a JSONL file into the session directory, similar to Claude's format but adapted for opencode's schema.

None found after checking: `RunResult` is shared and compatible, `TokenUsage` has no pointer semantics, `StreamUpdate` fields are generic enough. The orchestrator's `Runners` struct is unchanged — only `buildEngine()` wiring changes.

## Work Packages

### 1. Add `Harness` field to agent configs and top-level `harnesses` map

**Steps:**
1. Read `internal/config/config.go` — find `BaseAgentConfig` struct and `Config` struct.
2. Add `Harness string` field to `BaseAgentConfig` (after `Model`). This is the **mandatory** harness selector per agent. Allowed values: `"claude"`, `"opencode"`.
3. Add `Harnesses map[string]string` field to `Config` struct — a map of harness name to optional binary path.
4. In `validate()` — after existing agent validation, check that every agent with `Harness == ""` fails with an error. After provider validation, check that every referenced harness name exists in `Harnesses` map.
5. In `BuildModelEnv` — do NOT change. Provider types remain `native`, `anthropic`, `openai` only. Opencode does NOT add a provider type.

**Done when:**
- `go vet ./internal/config/` exits 0.
- A config missing `harness` on any agent fails validation with a clear error.
- A config referencing an unknown harness name fails validation.
- `grep -r "ProviderTypeOpenCode" .` returns nothing — no provider type was added.

### 2. Implement `OpenCodeCLI` harness with SQLite session reader

**Steps:**
1. Create `internal/harness/opencode_cli.go` with `//go:build darwin` build tag.
2. Define `OpenCodeCLI` struct (mirrors `ClaudeCLI` fields needed):
   ```go
   type OpenCodeCLI struct {
       binary             string // path to opencode binary, defaults to "/opt/homebrew/bin/opencode"
       extraArgs          []string
       workDir            string
       dbPath             string // optional: path to opencode.db; defaults to ~/.local/share/opencode/opencode.db
       sessionID          string // optional: set after RunStreaming to track the session
   }
   ```
3. Implement `NewOpenCodeCLI(binaryPath string, opts ...OpenCodeOption) *OpenCodeCLI`:
   - Default binary: `/opt/homebrew/bin/opencode`
   - Default dbPath: `~/.local/share/opencode/opencode.db` (resolved lazily)
   - Functional options pattern (same as `ClaudeCLIOption`)
4. Implement `OpenCodeOption` functional options:
   - `WithOpenCodeExtraArgs(args ...string)` — appends extra CLI flags
   - `WithOpenCodeWorkDir(dir string)` — sets working directory
   - `WithOpenCodeDBPath(path string)` — sets SQLite database path
5. Implement `RunPrint(ctx, prompt, systemPrompt) (RunResult, error)`:
   - **DISCOVERY NEEDED**: Run `opencode --help` to discover the print/sync output flag. If opencode has no `--print` equivalent, fall back to `RunStreaming` with output captured to a temp file.
   - `exec.CommandContext(ctx, c.binary, args...)`
   - Return `RunResult{Output: stdout.String()}` — token usage is populated post-command via SQLite query (see step 8).
6. Implement `RunStreaming(ctx, prompt, systemPrompt, events) (RunResult, error)`:
   - **DISCOVERY NEEDED**: Run `opencode --help` to discover the session creation flag. Opencode likely creates a new session on each invocation unless `-s <session-id>` is passed.
   - `exec.CommandContext(ctx, c.binary, args...)` — execute opencode with the prompt
   - `cmd.Wait()`, capture exit code
   - **After command completes**: Query SQLite `opencode.db` for the session data:
     ```sql
     SELECT id, tokens_input, tokens_output, model, agent, cost
     FROM session
     WHERE directory = ? AND time_created > ?
     ORDER BY time_created DESC LIMIT 1
     ```
     (The `directory` column matches the working directory; `time_created` filters to the current run.)
   - Query `message` and `part` tables for the session content, reconstructing the conversation as markdown text.
   - Synthesize `StreamUpdate` events from the SQLite data and emit them to the `events` channel.
   - Return `RunResult{Output: markdown, Usage: TokenUsage{Input: tokens_input, Output: tokens_output}, SessionID: sessionID}`.
7. Implement `RunContinue(ctx, sessionID, prompt, events) (RunResult, error)`:
   - **DISCOVERY NEEDED**: Determine if opencode supports session continuation via CLI (`-s <session-id>` to resume, or a `--continue` flag).
   - If opencode supports CLI continuation: pass `-s <sessionID>` as the session selector.
   - If NOT: query SQLite for the session's last assistant message, construct a new prompt that includes the conversation context, and run as a new session. Store the new session's ID in `c.sessionID`.
   - Same post-command SQLite query as `RunStreaming` for token usage and output.
8. Implement `buildEnv() []string` — returns `os.Environ()` (no model routing env vars for local opencode).
9. Implement `SessionMarkdown(sessionID string) (string, error)` — queries `message` and `part` tables, reconstructs conversation as markdown. Used by `RunStreaming` to populate `RunResult.Output`.
10. Implement `SessionTokenUsage(sessionID string) (TokenUsage, error)` — queries `session.tokens_input` / `session.tokens_output`.

**Done when:**
- `go vet ./internal/harness/` exits 0.
- `OpenCodeCLI` implements `harness.ContinuableRunner` (compile-time check: `var _ harness.ContinuableRunner = (*OpenCodeCLI)(nil)`).
- `RunPrint` with a known-broken binary returns a clear error containing the binary path.
- `RunStreaming` and `RunContinue` follow the same error-handling pattern as `ClaudeCLI.RunStreaming`.
- `RunStreaming` populates `RunResult.SessionID` from the SQLite `session.id` column.
- `RunStreaming` populates `RunResult.Usage` from SQLite `session.tokens_input` / `session.tokens_output`.

### 3. Parameterize `SandboxCLIRunner` for non-claude binaries

**Steps:**
1. Read `internal/harness/sandbox_cli_runner.go` lines 23-30 and 92-103.
2. Add `Binary string` field to `SandboxCLIRunner` struct (line 23-30). Default value: `"claude"`.
3. Add `Binary string` field to `SandboxCLIRunnerConfig` struct (line 33-40).
4. In `NewSandboxCLIRunner` (line 43), set `r.binary = cfg.Binary` (if empty, defaults to `"claude"`).
5. In `buildCommand` (line 92), use `r.binary` instead of hardcoded `"claude"`.
6. In `RunContinue` (line 83), use `r.binary` instead of hardcoded `"claude"`.
7. Verify `SandboxCLIRunner` still compiles and passes existing tests.

**Done when:**
- `go vet ./internal/harness/` exits 0.
- `go test -race ./internal/harness/ -run TestExtractStreamResult -v` passes.
- `go test -race ./internal/harness/ -run TestRunParsed -v` passes.
- `go test -race ./internal/harness/ -run TestRunStreaming_ErrorBranch -v` passes.

### 4. Add `DetectOpenCode` to sandbox detect package

**Steps:**
1. Create `internal/sandbox/detect/opencode.go` with `//go:build darwin` build tag.
2. Implement `DetectOpenCode(home string, binary string) (sandbox.Snapshot, error)`:
   - Use `sandbox.NewToolProfile("opencode", home)`
   - Default binary: `"opencode"` (will resolve via `exec.LookPath`)
   - Allow binary directory via `p.Allow(filepath.Dir(binPath), sandbox.Exec)`
   - Allow Homebrew cellars for opencode: `p.AllowOptional(filepath.Join(home, "Library", "Homebrew", "Cellar", "opencode"), sandbox.Read)`
   - Allow `~/.local/share/opencode/` (read+write — SQLite DB is here)
   - Allow `~/.config/opencode/` (read — config files)
   - Allow `~/.local/state/opencode/` (write) if it exists
   - Return `p.Snapshot()`
3. Follow the exact pattern of `DetectClaude` (`detect.go:21-55`).

**Done when:**
- `go vet ./internal/sandbox/detect/` exits 0.
- `DetectOpenCode` returns a valid `sandbox.Snapshot` when opencode binary is in PATH.
- `DetectOpenCode` returns an error when opencode binary is not found.

### 5. Wire `OpenCodeCLI` into orchestrator `buildEngine()` and `WorktreeRunnerFactory`

**Steps:**
1. Read `cmd/orqestra/main.go` `buildEngine()` function (lines 392-492).
2. After `ResolveModel()` for each agent, look up `agentConfig.Harness` to select the runner:
   - `harness == "claude"` → `harness.NewClaudeCLIFromConfig(cfg, model, opts...)` (existing behavior)
   - `harness == "opencode"` → `harness.NewOpenCodeCLI(binaryPath)` where `binaryPath` comes from `cfg.Harnesses["opencode"]` (or default `/opt/homebrew/bin/opencode`)
   - unknown harness → return error
3. For the worker (sandbox runner):
   - Read `internal/harness/sandbox_cli_runner.go` — the `binary` field is added in WP3.
   - Set `SandboxCLIRunnerConfig.Binary` from the worker's harness selection: `claude` → `"claude"`, `opencode` → detected binary path from `DetectOpenCode()`.
4. For `WorktreeRunnerFactory`:
   - When worker harness is `opencode`, use `DetectOpenCode()` instead of `DetectClaude()` for sandbox profile generation.
   - Pass the opencode binary path to `SandboxCLIRunnerConfig.Binary`.
5. Wire `DetectOpenCode` into the sandbox path in `engine.go` — when harness is `opencode`, use `DetectOpenCode()` for sandbox profile generation.

**Done when:**
- `go vet ./...` exits 0.
- `orqestra.yaml` with `harness: opencode` on any agent creates an `OpenCodeCLI` for that agent.
- `orqestra.yaml` with `harness: claude` (default) uses existing `ClaudeCLI` path.
- Worker with `harness: opencode` creates a sandbox runner with opencode binary under a worktree.

### 6. Extend `StepMeta` and session log copying for opencode

**Steps:**
1. Read `internal/agent/session.go` — find `StepMeta` struct (lines 58-75).
2. Add to `StepMeta`:
   ```go
   OpenCodeSessionID  string `json:"opencode_session_id,omitempty"`
   OpenCodeDBPath     string `json:"opencode_db_path,omitempty"`
   ```
   These are optional — existing TUI rendering must not break when they're empty.
3. Create `internal/harness/logpath_opencode.go` — opencode session log resolver:
   - `ResolveOpenCodeSessionLogPath(dbPath, sessionID string) (string, error)` — constructs a path within the session directory for the copied opencode session data.
   - `CopyOpenCodeSessionLog(dbPath, sessionID, destPath string) error` — queries SQLite for the session's `message` and `part` data, writes it as a JSONL file into the session directory.
4. In `engine.go`, replace the `copyLog` closure with a harness-aware version:
   - If agent harness is `claude`: use existing `agent.CopySessionLog()` + `ResolveSessionLogPath()`.
   - If agent harness is `opencode`: use `CopyOpenCodeSessionLog()` + populate `StepMeta.OpenCodeSessionID` and `StepMeta.OpenCodeDBPath`.
5. In `engine.go`, when writing `StepMeta` for opencode agents:
   - Set `ClaudeSessionID`, `ClaudeProjectPath`, `ClaudeSessionLogPath`, `ClaudePlanFilePath` to empty strings.
   - Set `OpenCodeSessionID` to the session ID from SQLite.
   - Set `OpenCodeDBPath` to the path of the opencode database.
6. Update `AnalyzeRunCompleteness` and `LoadRunDetail` to handle `OpenCodeSessionID` — they already handle empty `ClaudeSessionID` gracefully, so no changes needed there.

**Done when:**
- `go vet ./internal/agent/ ./internal/harness/ ./internal/orchestrator/` exits 0.
- A run with `harness: opencode` on any agent produces `*_meta.json` with `opencode_session_id` and `opencode_db_path` populated.
- The session directory contains a JSONL file with the opencode session data (messages + parts).
- `AnalyzeRunCompleteness` treats opencode agents the same as Claude agents (no special-casing needed).

### 7. Update TUI run detail screen to render opencode session data

**Steps:**
1. Read `internal/tui/screen_run_detail.go` — session log loading path.
2. In the session log loading code, add opencode path:
   - If `step.OpenCodeSessionID != ""`: use `CopyOpenCodeSessionLog` to ensure the JSONL is available in the session directory, then load it.
   - If `step.ClaudeSessionID != ""`: use existing `ResolveSessionLogPath` path.
   - If neither: show "No session log available" placeholder.
3. In the run detail cards, display `OpenCodeSessionID` alongside (or instead of) `ClaudeSessionID` when it's populated.

**Done when:**
- `go vet ./internal/tui/` exits 0.
- A run detail screen for an opencode agent shows the session log (loaded from the copied JSONL).
- A run detail screen for a Claude agent works exactly as before (no regression).

### 8. Update all `orqestra*.yaml` configs with `harness: claude`

**Steps:**
1. Update these files to add `harness: claude` under each agent role:
   - `orqestra.yaml` — add to `researcher`, `architect`, `critic`, `worker`
   - `orqestra.anthropic.yaml` — add to each agent
   - `orqestra.flash.yaml` — add to each agent
   - `orqestra.github.yaml` — add to each agent
   - `orqestra.huggingface.yaml` — add to each agent
2. Add top-level `harnesses:` mapping with optional binary paths:
   ```yaml
   harnesses:
     claude: ~/.local/bin/claude
     opencode: /opt/homebrew/bin/opencode
   ```
3. These are optional — if not specified, the binary resolver should fall back to `exec.LookPath`.

**Done when:**
- All 5 YAML files have `harness: claude` under every agent role.
- All 5 YAML files have the `harnesses:` top-level mapping.
- `go run ./cmd/orqestra --config orqestra.yaml --auto-approve --prompt "test"` loads without harness validation errors.

### 9. Write unit tests for harness validation and SQLite reader

**Steps:**
1. Create `internal/config/harness_test.go` with `//go:build darwin` tag.
2. `TestHarness_MissingHarnessField` — config with no `harness` on any agent → validation error.
3. `TestHarness_UnknownHarnessName` — config with `harness: unknown` → validation error.
4. `TestHarness_ClaudeDefault` — config with `harness: claude` → passes validation.
5. `TestHarness_OpenCode` — config with `harness: opencode` → passes validation.
6. Create `internal/harness/opencode_sqlite_test.go` with `//go:build darwin` tag.
7. `TestSessionTokenUsage_FromSQLite` — use a test SQLite database (or the real one) to verify `SessionTokenUsage` returns correct values.
8. `TestSessionMarkdown_FromSQLite` — verify `SessionMarkdown` reconstructs conversation from `message` + `part` tables.

**Done when:**
- `go test -race ./internal/config/ -run TestHarness_ -v` passes all 4 tests.
- `go test -race ./internal/harness/ -run TestSession_ -v` passes all 2 tests.

### 10. Write unit test for `OpenCodeCLI` construction and error handling

**Steps:**
1. Create `internal/harness/opencode_cli_test.go` with `//go:build darwin` build tag.
2. Implement `TestOpenCodeCLI_NewDefaultBinary`:
   - Create `OpenCodeCLI` with no options
   - Verify binary defaults to `/opt/homebrew/bin/opencode`
3. Implement `TestOpenCodeCLI_NewCustomBinary`:
   - Create `OpenCodeCLI` with `WithOpenCodeExtraArgs("--verbose")`
   - Verify extra args are set
4. Implement `TestOpenCodeCLI_RunPrint_BadBinary`:
   - Create `OpenCodeCLI` with a non-existent binary path
   - Call `RunPrint`
   - Verify error is returned and contains the binary path
5. Implement `TestOpenCodeCLI_BuildEnv_NoModelVars`:
   - Call `buildEnv()` on `OpenCodeCLI`
   - Verify no `ANTHROPIC_*` or `OPENAI_*` env vars are present
   - Verify standard env vars (`PATH`, `HOME`) are present

**Done when:**
- `go test -race ./internal/harness/ -run TestOpenCodeCLI_ -v` passes.
- All 4 tests pass.

### 11. Write integration tests

**Steps:**
1. Create `internal/harness/opencode_integration_test.go` with `//go:build darwin && integration` build tag.
2. Implement `TestOpenCodeCLI_SayHello`:
   - Detect opencode binary via `DetectOpenCode`
   - Create `OpenCodeCLI` with the detected binary
   - Call `RunStreaming(ctx, "say hello", "")` with 60s timeout
   - Verify `RunResult.Output` is non-empty and contains a greeting-like response
   - Verify `RunResult.SessionID` is populated from SQLite
   - Verify `RunResult.Usage` is populated from SQLite token counts
3. Implement `TestOpenCodeCLI_MarkdownStorageAndRetrieval`:
   - Create `OpenCodeCLI` with the detected binary
   - Prompt: `"Write a short markdown document about test integration and output it as plain text"`
   - Call `RunStreaming(ctx, prompt, "")` with 60s timeout
   - Verify `RunResult.Output` is non-empty
   - Write output to a temp file (simulating session artifact storage)
   - Read the file back and verify content matches
   - Clean up the temp file
   - Verify cleanup succeeded (file no longer exists)
4. Implement `TestOpenCodeCLI_SandboxWorktree`:
   - Detect opencode binary via `DetectOpenCode`
   - Create temp workspace directory
   - Create a git repo in the workspace (via `git init`)
   - Detect opencode snapshot via `DetectOpenCode`
   - Build sandbox with `sandbox.Config{RepoPath: workspace, RepoWritable: true, Profiles: []sandbox.Snapshot{opencodeSnap}}`
   - Create `OpenCodeCLI` with the detected binary
   - Prompt: `"Create a file called sandbox-test.txt in the current directory containing exactly 'opencode sandbox test passed'"`
   - Run via sandbox using `SandboxCLIRunner` with `OpenCodeCLI` binary parameterization
   - Verify `sandbox-test.txt` exists in workspace with correct content
   - Clean up workspace
5. Helper function `execSandbox(sb *sandbox.Sandbox, ctx context.Context, command []string, stdout *bytes.Buffer) (int, error)` — copy from `sandbox_integration_test.go:234-248`.

**Done when:**
- `go vet ./internal/harness/` exits 0.
- Tests compile with `go test -tags integration -c ./internal/harness/`.
- `TestOpenCodeCLI_SayHello` passes when opencode is installed and logged in to qwen3.6.
- `TestOpenCodeCLI_MarkdownStorageAndRetrieval` passes (no network required, tests local file I/O).
- `TestOpenCodeCLI_SandboxWorktree` passes when opencode is installed and sandbox-exec is available.

## Verification

After all work packages complete, run:

```bash
# Unit tests (no integration)
cd /Users/xiii/Developer/orqestra
make build
make lint
go test -race ./internal/config/ -v -run TestHarness_
go test -race ./internal/config/ -v -run TestIsProviderType
go test -race ./internal/harness/ -v -run TestOpenCodeCLI_
go test -race ./internal/harness/ -v -run TestSession_
go test -race ./internal/harness/ -v -run TestExtractStreamResult
go test -race ./internal/harness/ -v -run TestRunParsed
go test -race ./internal/harness/ -v -run TestRunStreaming_ErrorBranch

# Integration tests (requires opencode installed)
go test -tags integration -race ./internal/harness/ -v -run TestOpenCodeCLI_

# End-to-end smoke (requires opencode installed and authenticated)
./bin/orqestra --config orqestra.yaml --prompt "test" --auto-approve
```

All unit tests must pass. Integration tests must pass when opencode is installed and authenticated to qwen3.6.

## Assumptions

1. **Opencode CLI flags**: `opencode --help` will be run as the first step to discover the actual CLI interface. The plan's CLI args are tentative and will be adjusted based on the real output. Known: opencode uses `-s <session-id>` for session selection (confirmed from log: `args=["-s","ses_16fe3e297ffexPL3pEISCdmLKO"]`). Unknown: `--print` equivalent, `--output-format` flags, `--resume` flag.
2. **Opencode is logged in to qwen3.6 locally**: No API key or base URL configuration needed. The model is resolved via opencode's local auth mechanism (confirmed from DB: `providerID: "huggingface"`, `modelID: "deepseek-ai/DeepSeek-V4-Pro"`).
3. **Opencode session continuation**: May or may not support CLI-level session resume. If not, `RunContinue` will construct a continuation prompt from SQLite data. This is a known unknown — will be discovered during WP2 implementation.
4. **Sandbox-exec grants file write to the workspace**: The sandbox config uses `RepoWritable: true` for the worktree test, which grants `file-read* file-write* file-map-executable process-exec` to the workspace subpath.
5. **SQLite `go-sqlite3` or `modernc.org/sqlite` is available**: The harness needs to query opencode's SQLite database. If not already a dependency, add it. `modernc.org/sqlite` is preferred (pure Go, no CGO) but may be slower; `go-sqlite3` requires CGO.

## Gotchas

1. **`SandboxCLIRunner` currently hardcodes `"claude"`**: This is a known limitation in `sandbox_cli_runner.go:93` (`args := []string{"claude", ...}`). Work Package 3 fixes this by adding a `Binary` field. Existing `SandboxCLIRunner` usage (orchestrator, existing tests) passes no binary, so it defaults to `"claude"` — backward compatible.
2. **`parseStream` is Claude-specific and NOT reusable for opencode**: The struct at `claude_cli.go:445-457` has fields like `PlanFilePath` and `event.Event` for Claude's `stream_event` wrapper. Opencode produces NO NDJSON. A new SQLite reader (`SessionMarkdown`, `SessionTokenUsage`) is needed.
3. **`extractJSONUsage` and `extractStreamUsage` are unexported**: They live in `sandbox_cli_runner.go` (lines 236, 249). `opencode_cli.go` is in the same package (`harness`), so they're accessible. However, they parse NDJSON — NOT applicable for opencode's SQLite data. Token usage comes from SQLite queries instead.
4. **`BuildModelEnv` rejects unknown provider types**: The function at `claude_cli.go:624-628` returns an error for unknown types. Since opencode does NOT add a provider type, `BuildModelEnv` is never called for opencode agents. The harness's `buildEnv()` returns `os.Environ()` directly.
5. **Integration tests require `git init` for the worktree test**: `TestOpenCodeCLI_SandboxWorktree` creates a temp directory and runs `git init` in it. This requires git to be installed and in PATH. The `detect.DetectGit()` function already handles this for the main orchestrator.
6. **`OpenCodeCLI` has `inlineMCPServers` and `appendSystemPrompt` fields NOT in struct**: `ClaudeCLI` supports MCP servers and system prompt appending. `OpenCodeCLI` does NOT currently support these. If opencode has an equivalent MCP mechanism, it should be added. If not, the orchestrator should warn when MCP servers are configured for an opencode agent.
7. **`ResolveSessionLogPath` is Claude-specific**: `internal/harness/logpath.go` constructs `~/.claude/projects/...` paths. A new `ResolveOpenCodeSessionLogPath` is needed for opencode. The `CopyOpenCodeSessionLog` function queries SQLite and writes a JSONL file — this is the opencode equivalent of Claude's session log copy.
8. **Token usage unpopulated without SQLite query**: `RunResult.Usage` for opencode agents MUST come from SQLite `session.tokens_input` / `session.tokens_output`. If the SQLite query fails (DB locked, session not found), `RunResult.Usage` will be zero and `StepMeta` will show zero tokens. This is a known gap — the error should be logged but not fatal (the output markdown is still valid).
