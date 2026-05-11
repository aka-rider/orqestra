# Orqestra — Agent, Sandbox & Pipeline Instructions

[How to build/test/run](../Makefile)

<agent_architecture>

## LLM Integration & Orchestration Pipeline (`internal/agent`)

The system orchestrates planning, validation, and execution against independent LLMs.

- LLM calls are routed via an OpenAI-compatible proxy (`internal/harness.Client`), no direct SaaS API calls unless necessary for OAuth boundaries.
- **Pipeline State Machine**: The state transitions rigidly through `PipelineIdle` → `PipelineIntake` → `PipelinePlanning` → `PipelineValidating` → `PipelineExecuting` → `PipelineDone` (or `PipelineHalted` on failure).
- **Token Breaking**: Executions use `internal/tokenlimit.Limiter`. If threshold usage indicates exhaustion, iterations gracefully circuit break yielding `ErrBudgetExhausted`.
</agent_architecture>

<sandbox_rules>

## Seatbelt Sandbox (`internal/seatbelt`)

Host security entirely relies on macOS `sandbox-exec` kernel enforcement, not Docker.

- **Dynamic `.sb` profile generation**: Profiles securely interpolate variables (repo path, session directory) to create on-the-fly rigid SBPL execution profiles locked at `0400`.
- **`exec.Cmd` execution constraints**: When executing LLM worker shells:
  - All environments are completely scrubbed (`cmd.Env`) except for white-listed variables, offering zero host-leakage.
  - Enforced POSIX process group isolation (`cmd.SysProcAttr.Setpgid = true`) ensures runaway zombie processes are wiped tightly.
- Standard agents write only to `.orqestra/sessions/<timestamp>/`. Worker agents executing plans gain full `RepoWritable` access, bounded structurally.
</sandbox_rules>

<pm_and_dependency_graphs>

## Project Manager & Dependency Parsing (`internal/agent/pm.go`)

The PM (`agent.ProjectManager`) parses specifications (`agent.Specification`) into atomic tasks (`WorkPackage`).

- **Kahn's Algorithm Check**: LLM outputs containing `DependsOn` fields are NOT blindly trusted. The PM traverses dependencies via Kahn's algorithm; dependency graphs containing cycles deadlock fast with a structural error.
- **Defensive Unmarshaling**: Always `stripCodeFences` and assume hostile/fuzzy parsing arrays.
</pm_and_dependency_graphs>

<validation_boundaries>

## Execution Security & Validation (`internal/agent`)

- **PlanValidator** (`agent.PlanValidator`): Phase 1 ensures structural existence (goal, acceptance criteria). Phase 2 offloads to LLM inference to score logic flow.
- **WorkValidator** (`agent.Gate`): Bounded severely. When executing tasks against shell commands internally (e.g. assessing completion), strictly abide by the internal `allowlist` (e.g., `go`, `make`, `pytest`, `grep`).
- **Artifact Frontmatter**: Specifications passed over session networks MUST contain verifiable YAML frontmatter `ArtifactMeta` to cryptographically prove input hash alignments.
- **Type ownership**: All agent domain types (`Specification`, `ValidationReport`, `ProjectPlan`, `WorkPackage`, `GatewayResult`) live in `internal/agent/`. The `plan/` package is a markdown persistence adapter that imports `agent.Specification` for conversion.
</validation_boundaries>
