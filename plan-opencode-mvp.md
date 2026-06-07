# MVP Prototype Plan — Opencode Harness

## Goal

Build a working `opencode` harness (`harness.ContinuableRunner`) as a side experiment, validated entirely by integration tests. No config changes, no orchestrator wiring, no TUI changes, no session log copying, no SQLite reading — until the MVP is proven.

## Discovery Results

### Opencode CLI Interface (verified + source-traced)

| Flag | Alias | Type | Description |
|---|---|---|---|
| `opencode run [message..]` | — | positional string[] | Headless command; message is joined from args |
| `--format` | — | `default` \| `json` | NDJSON output (default: `default`) |
| `-m`, `--model` | — | `provider/model` | **REQUIRED** — parsed as `{providerID, modelID}` |
| `--agent` | — | string | Agent name (`plan`, `build`, or custom from config) |
| `--dangerously-skip-permissions` | — | boolean | Auto-approve all permissions (required headless) |
| `--pure` | — | boolean | Disable all plugins/MCPs (sets `OPENCODE_PURE=1`) |
| `-s`, `--session` | — | string | Resume specific session (`ses_...`) |
| `-c`, `--continue` | — | boolean | Resume last session |
| `--fork` | — | boolean | Fork session before continuing (requires `-s` or `-c`) |
| `--dir` | — | string | Working directory |
| `--title` | — | string | Session title |
| `--file`, `-f` | — | string[] | Files to attach |
| `--command` | — | string | Slash command (uses message as args) |
| `--variant` | — | string | Model variant (`high`, `max`, `minimal`) |
| `--thinking` | — | boolean | Show thinking blocks |

**Flags that DO NOT EXIST** (in current `opencode_cli.go`):
- `--print` — not a valid flag
- `-p` — not a valid flag for message (message is positional)
- `--output-format stream-json` — not a valid flag
- `--resume` — not a valid flag (use `-s` or `-c`)
- `--append-system-prompt` — not a valid flag

**Top-level flags** (applied before subcommand):
- `--pure` — sets `OPENCODE_PURE=1`, disables all plugins/MCPs
- `--print-logs` — prints logs to stderr
- `--log-level` — `DEBUG`, `INFO`, `WARN`, `ERROR`

**Provider/model configuration:**
- Config loaded from multiple paths (merged, later wins):
  - `~/.config/opencode/config.json`
  - `~/.config/opencode/opencode.json`
  - `~/.config/opencode/opencode.jsonc`
  - Project-level `.opencode/opencode.json`
- Config structure: `provider.<name>` with `npm`, `options.baseURL`, `options.apiKey`, `models.<name>`
- CLI `-m <provider>/<model>` overrides config default — parsed as `{providerID, modelID}`
- Without explicit `-m`, opencode defaults to `huggingface` provider (uses cloud tokens — **must not happen**)
- HuggingFace credits are depleted (402 error) — confirms fallback risk is real
- **The llama-server at `192.168.50.212:11434/v1` has `qwen3.6-coder` (16GB GGUF)**
- Provider `npm: "@ai-sdk/openai-compatible"` + `options.baseURL` produces OpenAI-compatible requests to llama-server

**Environment variables:**
| Variable | Set By | Description |
|----------|--------|-------------|
| `OPENCODE_PURE` | `--pure` | Disables all plugins/MCPs |
| `OPENCODE` | internal | Set to `"1"` |
| `OPENCODE_PID` | internal | Process PID |
| `AGENT` | internal | Set to `"1"` |
| `OPENCODE_CONFIG` | — | Path to custom config file |
| `OPENCODE_CONFIG_CONTENT` | — | Inline config JSON |

**Stdin support:** Message can be piped via stdin. Combined with positional args: `msg + "\n" + stdin`.

### Opencode NDJSON Output Format (verified + source-traced)

Each line is a JSON event: `{"type": "...", "timestamp": number, "sessionID": "...", ...data}`.

Source-traced from `@opencode-ai/sdk/v2` types. Event emission in `run.ts` dispatches SDK events:

| Type | SDK Event | Part type | Key fields in `part` |
|---|---|---|---|
| `step_start` | `message.part.updated` | `"step-start"` | `id`, `sessionID`, `messageID`, `snapshot?` |
| `step_finish` | `message.part.updated` | `"step-finish"` | `id`, `sessionID`, `messageID`, `reason`, `cost`, `tokens.total`, `tokens.input`, `tokens.output`, `tokens.reasoning`, `tokens.cache.read`, `tokens.cache.write` |
| `text` | `message.part.updated` | `"text"` | `id`, `sessionID`, `messageID`, `text`, `time.start`, `time.end?` |
| `tool_use` | `message.part.updated` | `"tool"` | `id`, `sessionID`, `messageID`, `tool`, `callID`, `state.status`, `state.input`, `state.output`, `state.title` |
| `reasoning` | `message.part.updated` | `"reasoning"` | `id`, `sessionID`, `messageID`, `text`, `time.start`, `time.end?` |
| `error` | `session.error` | — | `error.name`, `error.data.message` |

**Session status events** (not emitted to NDJSON, used internally to detect idle): `session.status` → `{sessionID, status: {type: "idle"}}`

**Token usage comes from `step_finish` events** (`StepFinishPart`), NOT from a separate `result` event like Claude. The last `step_finish` event has cumulative token counts.

**Session ID** is in every event's `sessionID` field.

**Permission handling (non-interactive):** Without `--interactive`, permissions for `question`, `plan_enter`, `plan_exit` are auto-denied unless `--dangerously-skip-permissions` is set.

### Plan mode and Build mode (verified)

- `--agent plan`: Agent explores the repo, asks clarifying questions, writes a plan. Does NOT execute code.
- `--agent build`: Agent executes code, creates files, runs commands.
- Both modes produce NDJSON with the same event types.
- `--dangerously-skip-permissions` is required for headless mode in both.
- Agents are defined in config with `mode: "primary"` or `mode: "subagent"`.

### MCP Control (verified)

- `--pure` flag disables all plugins/MCPs (sets `OPENCODE_PURE=1`)
- Config file has `"mcp"` field — empty `{}` disables all MCPs
- `OPENCODE_DISABLE_DEFAULT_PLUGINS` env var also controls plugin loading
- Plugin origins tracked per-source for location-sensitive decisions

---

## MVP Steps

### Step 1: Verify opencode runs headlessly with the llama-server

**Goal:** Prove we can run `opencode run` headlessly with `llama/qwen3.6-coder` pointing to `http://192.168.50.212:11434/v1`, and that missing model fails closed (no silent cloud fallback).

**Integration test: `TestOpenCodeCLI_NoFallbackToCloud`**
- Create `OpenCodeCLI` with model `llama/qwen3.6-coder`, binary `/opt/homebrew/bin/opencode`
- Call `RunPrint(ctx, "say hello in one word", "")`
- Assert:
  - `RunResult.Output` is non-empty
  - `RunResult.SessionID` is populated from NDJSON
  - `RunResult.Usage` is populated from `step_finish` tokens
  - No error returned

**Anti-regression test: `TestOpenCodeCLI_ModelRequired_FailClosed`**
- Create `OpenCodeCLI` with NO model set (empty string)
- Call `RunPrint(ctx, "say hello", "")`
- Assert:
  - Error is returned **before spawning the opencode process** (harness-level validation)
  - Error message is explicit: `"model is required"` or similar — not a 402, not a cloud error
  - The opencode binary is NOT invoked (no network calls, no token spend)
  - This proves fail-closed behavior: unknown/missing provider+model fails immediately, never silently falls back to a cloud provider that burns user tokens

**Done when:**
- Both integration tests pass
- `opencode run --format json` output format is fully understood
- Harness-level model validation is implemented (empty model → error before process spawn)

---

### Step 2: Fix `OpenCodeCLI` — correct CLI flags + NDJSON parser

**Goal:** Replace the wrong CLI flags and add a proper NDJSON parser for opencode's output format.

**Changes to `internal/harness/opencode_cli.go`:**

1. **Fix CLI args in all methods:**
   ```go
   args := []string{"run", "--format", "json", "--dangerously-skip-permissions"}
   if c.model != "" {
       args = append(args, "-m", c.model)
   }
   if c.agent != "" {
       args = append(args, "--agent", c.agent)
   }
   if c.sessionID != "" {
       args = append(args, "-s", c.sessionID)
   }
   args = append(c.extraArgs...)
   args = append(args, prompt)  // message is positional
   ```

2. **Add `OpenCodeCLI` fields:**
   ```go
   type OpenCodeCLI struct {
       binary    string   // path to opencode binary
       model     string   // e.g. "llama/qwen3.6-coder" — REQUIRED, no fallback
       agent     string   // e.g. "plan" or "build"
       sessionID string   // set after RunStreaming to track the session
       extraArgs []string
       workDir   string
       pure      bool     // pass --pure to disable MCPs
   }
   ```

3. **Add `OpenCodeOption` functional options:**
   ```go
   func WithOpenCodeModel(model string) OpenCodeOption
   func WithOpenCodeAgent(agent string) OpenCodeOption
   func WithOpenCodePure(pure bool) OpenCodeOption
   ```

4. **Add NDJSON parser** — scan lines, parse `opencodeEvent` structs, dispatch to `StreamUpdate` events:
   - `step_start` → `StreamUpdateStepStart`
   - `text` → `StreamUpdateText` (accumulate `part.text` into `RunResult.Output`)
   - `tool_use` → `StreamUpdateToolUse`
   - `step_finish` → `StreamUpdateTokenUsage` (accumulate last `part.tokens` into `RunResult.Usage`)
   - Track last `sessionID` from any event

5. **Remove `--append-system-prompt`** from all invocations.

**Done when:**
- `go vet ./internal/harness/` exits 0.
- `OpenCodeCLI` implements `ContinuableRunner`.
- `parseOpencodeStream()` correctly parses the verified NDJSON format.

---

### Step 3: Integration test — plan mode + build mode + NDJSON parsing

**Test: `TestOpenCodeCLI_RunStreaming_PlanMode`**
- Create `OpenCodeCLI` with model `llama/qwen3.6-coder`, agent `plan`
- Call `RunStreaming(ctx, "Write a plan for a hello-world Go app", "", events)`
- Assert:
  - `RunResult.Output` is non-empty (plan text from `text` events)
  - `RunResult.SessionID` is populated
  - `RunResult.Usage` is populated (sum of `step_finish` tokens)
  - At least one `StreamUpdate` event of type `Text` was received
  - At least one `StreamUpdate` event of type `TokenUsage` was received

**Test: `TestOpenCodeCLI_RunStreaming_BuildMode`**
- Create `OpenCodeCLI` with model `llama/qwen3.6-coder`, agent `build`
- Call `RunStreaming(ctx, "Create a file /tmp/opencode-test-<random>.txt containing 'hello'", "", events)`
- Assert:
  - `RunResult.Output` is non-empty
  - `RunResult.SessionID` is populated
  - `RunResult.Usage` is populated
  - At least one `StreamUpdate` event of type `ToolUse` was received (file write tool)
  - The file exists and contains the expected content (verify via `os.ReadFile`)
  - Clean up the file

**Test: `TestOpenCodeCLI_RunContinue_SessionResume`**
- First call `RunStreaming` with a prompt to create a session
- Capture the `SessionID` from `RunResult`
- Second call `RunContinue(ctx, sessionID, "continue the conversation", events)`
- Assert:
  - `RunResult.SessionID` matches the original session ID
  - `RunResult.Output` is non-empty (continuation response)
  - `RunResult.Usage` is populated

**Done when:**
- All integration tests pass
- `opencode run --format json` output is fully parsed and converted to `RunResult` + `StreamUpdate` events

---

### Step 4: Integration test — MCP control and anti-regression

**Test: `TestOpenCodeCLI_PureModeNoMCP`**
- Create `OpenCodeCLI` with `WithOpenCodePure(true)`
- Call `RunStreaming(ctx, "say hello", "", events)`
- Assert:
  - No `StreamUpdate` events of type `ToolUse` with tool name containing "mcp" or "plugin"
  - `RunResult.Output` is non-empty
  - `RunResult.Usage` is populated

**Test: `TestOpenCodeCLI_AgentPlanVsBuild`**
- Run with `--agent plan` — assert output contains plan-like text (no file writes)
- Run with `--agent build` — assert output contains tool_use events with file write
- This proves agent mode selection works correctly

**Done when:**
- All integration tests pass
- MCP control is verified
- Anti-regression tests confirm no cloud token fallback

---

## What the MVP explicitly does NOT do

| Excluded from MVP | When to add |
|---|---|
| Config wiring (`Harness` field, `harnesses` map) | After MVP proves `OpenCodeCLI` works |
| `StepMeta` extension | After MVP |
| Session log copying | After MVP |
| TUI run detail rendering | After MVP |
| `buildEngine()` changes | After MVP |
| `SandboxCLIRunner` binary parameterization | After MVP |
| `DetectOpenCode` | After MVP |
| SQLite session reader | After MVP |
| Plan file extraction (Claude-format plans) | After MVP |
| `WorktreeRunnerFactory` changes | After MVP |

## Verification

```bash
# Unit tests
make build
make lint
go vet ./internal/harness/

# Integration tests (requires opencode + llama-server at 192.168.50.212:11434)
go test -tags integration -race ./internal/harness/ -v -run TestOpenCodeCLI_
```

All integration tests must pass when opencode is installed and the llama-server at `192.168.50.212:11434` is reachable.
