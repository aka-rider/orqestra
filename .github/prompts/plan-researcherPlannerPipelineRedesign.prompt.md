# Plan: Pipeline Redesign — Researcher → Planner, Worker Self-Validation, Kill QA

**Old**: planner → validator → PM → worker → QA
**New**: researcher → planner → PM → worker(+self-validate)

---

## Phase 1: Config & Model System Overhaul

1. **Drop tier abstraction** — `model_ref` already maps to arbitrary keys in the `models` map (`sonnet`, `opus`, `gemini`). Remove `applyModelTierDefaults()` (the `x-large→large` fallback magic). Models are referenced by name, not rank.

2. **Rename Config struct fields** in `internal/config/config.go`:
   - `Planner PlannerConfig` → `Researcher ResearcherConfig` (`yaml:"researcher"`)
   - `Validator ValidatorConfig` → `Planner PlannerConfig` (`yaml:"planner"`)
   - Remove `QA ValidatorConfig`
   - Update `validate()`: mandatory refs are now `researcher`, `planner`, `worker`

3. **Rewrite `pipeline.yaml`** with full system prompts:

   **Researcher prompt:**

   ```yaml
   researcher:
     model_ref: sonnet
     allowed_tools:
       - Read
     system_prompt: |
       You are a codebase researcher. You will explore a repository and produce
       a draft implementation plan. A senior architect will refine your output,
       so focus on THOROUGH RESEARCH over polish.

       WORKFLOW:
       1. Read the user's request carefully.
       2. Use the Read tool to explore relevant code: file structure, function
          signatures, type definitions, existing tests, config files.
       3. Produce a draft plan as markdown.

       MANDATORY SECTIONS in your output:
       ## Goal
       One sentence: what changes when this is done.

       ## Context
       Brief facts from the codebase you discovered. File paths, function names,
       patterns you observed. This is the most valuable section — the architect
       cannot explore as deeply as you.

       ## Steps
       Ordered implementation steps. Each step:
       - Starts with an imperative verb
       - Names at least one concrete file path, function, or command
       - Describes WHAT changes, not just that something changes
         BAD:  "Refactor the config parser"
         GOOD: "Split parseConfig() in config/loader.go into parseProviders()
                and parseModels(), update callers in main.go and config_test.go"

       ## Acceptance
       Testable assertions. Each must be verifiable by command exit code, output,
       or named file content. Examples:
       - "go test ./internal/config/... passes"
       - "orqestra --config test.yaml starts without error"

       ## Gotchas
       MANDATORY. Non-obvious things you discovered during exploration:
       - Unexpected coupling between packages
       - Files that look related but serve different purposes
       - Edge cases in existing code that the implementation must preserve
       - Test fixtures or golden files that will need updates
       If you found nothing non-obvious, say why — the architect will verify.

       ## Constraints
       What this change should NOT do. Explicit non-goals.

       ## Assumptions
       Things the prompt didn't specify that you assumed.

       ## Risks
       Ambiguities, breaking-change risks, or open questions.

       ## Verification
       Commands the worker should run after implementation. Write them naturally:
       - "Run go test ./internal/config/... to verify config parsing"
       - "Run go build ./cmd/orqestra to verify compilation"

       RULES:
       - Banned words in steps: "appropriate", "as needed", "properly", "ensure",
         "consider", "various". Replace with the specific action.
       - Do not invent files, APIs, or product requirements. If unsure, say so
         in Assumptions or Risks.
       - Your output is MARKDOWN. No JSON, no code fences around the whole output.
   ```

   **Planner prompt:**

   ```yaml
   planner:
     model_ref: opus
     allowed_tools:
       - Read
     system_prompt: |
       You are the chief architect. You receive a draft plan from a researcher
       who explored the codebase. Your job is to produce the FINAL implementation
       plan that a sandboxed worker will execute autonomously.

       The researcher had deep tool access and explored many files. Their draft
       contains real implementation context but may be:
       - Vague on HOW ("refactor X" instead of concrete steps)
       - Missing edge cases the researcher didn't notice
       - Structurally weak (wrong step order, missing dependencies)
       - Over-scoped or under-scoped relative to the user's request

       YOUR WORKFLOW:
       1. Read the draft carefully. Identify vague, incomplete, or suspicious claims.
       2. Use the Read tool to SPOT-CHECK: verify file paths exist, function
          signatures match, package structure is as claimed. Do NOT re-explore
          the entire codebase — the researcher already did that.
       3. Deepen any step that reads as "draw the rest of the owl."
       4. Produce the final plan as a JSON object.

       CRITICAL RULES:
       - The worker prompt includes ONLY goal, steps, and acceptance.
         Execution-critical boundaries must appear in steps or acceptance.
       - Every step must name concrete files, functions, or commands.
       - Every acceptance criterion must be falsifiable by command or file check.
       - Derive validation_commands from the Verification section. Convert
         natural language commands to structured form.
       - If the draft plan is fundamentally flawed (wrong approach, impossible
         steps), say so in risks and fix it. Do not rubber-stamp bad plans.

       OUTPUT FORMAT — your complete response must be a single JSON object:
       {
         "schema_version": "1",
         "goal": "<one sentence>",
         "context": "<brief facts or empty string>",
         "steps": ["<imperative step naming files/functions/commands>"],
         "acceptance": ["<falsifiable criterion>"],
         "constraints": ["<explicit non-goals and boundaries>"],
         "assumptions": ["<assumptions>"],
         "risks": ["<risks and open questions>"],
         "validation_commands": [
           {
             "command": "<executable>",
             "args": ["<arg1>"],
             "cwd": "<optional repo-relative directory>",
             "expected_exit": 0
           }
         ],
         "expected_artifacts": ["<repo-relative file paths>"]
       }
   ```

   **Worker section** (minimal change — add self-validation note):

   ```yaml
   worker:
     model_ref: sonnet
     permission_mode: full
     timeout: 45m
   ```

   **Remove `validator:` and `qa:` sections entirely.**

   **Keep `project_manager:` unchanged.**

4. **Rewrite all provider YAMLs** — `orqestra.yaml`, `orqestra.anthropic.yaml`, `orqestra.flash.yaml`, `orqestra.local.yaml`:
   - Models use descriptive names: `opus`, `sonnet`, `haiku`, `flash`, `gemini`
   - Agent fields: `researcher:` / `planner:` / `worker:` / `project_manager:` / `gateway:`
   - No `validator:` or `qa:` fields

   Example for `orqestra.anthropic.yaml`:

   ```yaml
   models:
     opus:
       provider: anthropic-native
       model: claude-opus-4-7
     sonnet:
       provider: anthropic-native
       model: claude-sonnet-4-6
     haiku:
       provider: anthropic-native
       model: claude-haiku-4-5

   gateway:
     model_ref: haiku
   researcher:
     model_ref: sonnet
   planner:
     model_ref: opus
   worker:
     model_ref: sonnet
   project_manager:
     model_ref: sonnet
   ```

5. **Update `config_test.go`** — new field names, remove QA, add researcher.

---

## Phase 2: Agent Rename & Rewrite

6. **Add `RawPlan` type** to `internal/agent/spec.go`:

   ```go
   type RawPlan struct {
       Markdown string
       Usage    *harness.TokenUsage
   }
   ```

7. **Rename `planner.go` → `researcher.go`**, type `Planner` → `Researcher`:
   - `NewResearcher(runner, cfg)`, methods `Research()` / `ResearchStreaming()` → return `(RawPlan, error)`
   - Zero parsing. Raw markdown passthrough.
   - DELETE: `ParsePlanOutput`, `parseFlexibleSpec`, `stripCodeFences`, envelope unwrapping
   - MOVE: `parseMarkdownPlan` to shared utility (deterministic pre-check for the new planner)

8. **Rename `plan_validator.go` → `planner.go`**, type `PlanValidator` → `Planner`:
   - `NewPlanner(runner, cfg)`, method `Refine(ctx, rawMarkdown) → (PlanOutput, error)`
   - Deterministic pre-check: `parseMarkdownPlan()` — if zero structure, fail fast before burning Opus tokens
   - Send raw markdown to LLM → parse response as PlanOutput JSON (reuse flexible parsing)
   - No `ValidationReport` — planner either produces the plan or returns error
   - DELETE: `ValidatePlan`, `deterministicChecks` (structural checks fold into `Refine`)

9. **Rename test files** accordingly.

---

## Phase 3: Worker Self-Validation (QueryRunner)

10. **Add `RunContinue` to `CLIRunner` interface** — `RunContinue(ctx, sessionID, prompt, systemPrompt) (RunResult, error)`. Backward-compatible: existing implementations can return `ErrNotSupported`.

11. **Implement on `ClaudeCLI`** in `internal/harness/claude_cli.go` — same as `RunPrint` but adds `--continue <sessionID>`. Must capture `session_id` from first call's result (already present in `query.go` `Result.SessionID`).

12. **Update `SandboxCLIRunner`** in `internal/harness/sandbox_cli_runner.go` — lifecycle change: sandbox persists across turns. New shape: `Run → RunContinue → Close()`. Orchestrator calls `Close()` after all turns.

13. **Worker validation turn** — second turn prompt: "Validate against acceptance criteria: [...]. Run commands: [...]. If tests fail, fix and re-run." One retry budget.

14. **Repurpose QA command execution logic** — extract allowlist and exit-code checking from `internal/agent/qa.go` into shared utility for worker validation turn.

---

## Phase 4: Orchestrator Rewiring

15. **Update `Engine.run()`** in `internal/orchestrator/orchestrator.go`:

    ```
    gateway → researcher(streaming) → plan approval gate → planner(refine) → PM → worker(+self-validate)
    ```

    - Research phase: `researcher.ResearchStreaming()` → `RawPlan`
    - Plan gate: show `RawPlan.Markdown` to user
    - `PhasePlanning` → `planner.Refine(markdown)` → `PlanOutput`
    - Emit `EventPlanReady` with `PlanOutput`
    - PM + worker phases unchanged (except worker uses `RunContinue` for self-validation)
    - No QA phase

16. **Update `Runners` struct** — `Planner→Researcher`, `Validator→Planner`, remove `QA`

17. **Update phases/events** — `PhaseResearching`, `PhasePlanning`, remove `PhaseQA`. Agent IDs: `"researcher"`, `"planner"`.

18. **Update `GateRequest`** — add `RawMarkdown string` for plan approval gate. Edit flow sends edited markdown to `planner.Refine()`.

19. **Update `cmd/orqestra/main.go`** — runner construction, subcommand adjustments, remove QA runner.

---

## Phase 5: TUI & Persistence

20. **Update TUI** phase labels: "Researching" → "Planning" → "Executing". Remove QA display.

21. **Update `internal/plan/spec.go`** — handle `## Gotchas`, `## Verification` headings in markdown adapter.

---

## Phase 6: Tests

22. Researcher tests — raw passthrough only.
23. Planner tests — markdown→PlanOutput extraction, command inference, deterministic fast-fail.
24. Worker self-validation tests — session continuation, retry on test failure.
25. Orchestrator tests — new pipeline flow.
26. Config tests — new field names.
27. Plan adapter tests — new headings, updated golden.md.

---

## Relevant Files

| Area         | Files                                                                                                                                      | Changes                                                         |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------- |
| Config       | `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/pipeline.yaml`                                             | Struct renames, system prompts, remove QA/validator             |
| Config YAMLs | `orqestra.yaml`, `orqestra.anthropic.yaml`, `orqestra.flash.yaml`, `orqestra.local.yaml`                                                   | Named models, new agent fields                                  |
| Agent        | `internal/agent/planner.go`→researcher.go, `internal/agent/plan_validator.go`→planner.go, `internal/agent/spec.go`, `internal/agent/qa.go` | Researcher passthrough, Planner Refine(), RawPlan type, kill QA |
| Harness      | `internal/harness/claude_cli.go`, `internal/harness/sandbox_cli_runner.go`, `internal/harness/query.go`                                    | RunContinue, session continuation, sandbox lifecycle            |
| Orchestrator | `internal/orchestrator/orchestrator.go`, `cmd/orqestra/main.go`                                                                            | Pipeline rewiring, runner construction                          |
| TUI          | `internal/tui/`                                                                                                                            | Phase labels                                                    |
| Plan         | `internal/plan/spec.go`, `internal/plan/testdata/golden.md`                                                                                | New headings                                                    |

---

## Verification

1. `go build ./cmd/orqestra` — compiles cleanly
2. `go test ./internal/config/...` — config parsing with new field names
3. `go test ./internal/agent/...` — researcher passthrough, planner extraction
4. `go test ./internal/harness/...` — RunContinue, session continuation
5. `go test ./internal/orchestrator/...` — new pipeline flow
6. `go test ./internal/plan/...` — markdown adapter with new headings
7. `go vet ./...` — no issues
8. Manual E2E: `orqestra --config orqestra.yaml` → researcher produces markdown → plan gate shows it → planner refines to PlanOutput → worker implements and self-validates

---

## Decisions

- **Researcher=Sonnet, Planner=Opus** — Sonnet explores, Opus judges
- **Planner has full Read access** — different model + different prompt + plan-anchored start = fresh perspective with code grounding
- **No researcher self-critique** — prompt demands specificity; Opus critique is strictly better
- **QA eliminated** — worker self-validates, test exit codes are ground truth
- **Worker uses `--continue` for validation turn** — true context preservation via QueryRunner infra
- **Config: named models, not tiers** — `model_ref: sonnet` not `model_ref: large`. Drop `applyModelTierDefaults()`.
- **Mandatory `## Gotchas` in researcher template** — forces surfacing non-obvious discoveries
