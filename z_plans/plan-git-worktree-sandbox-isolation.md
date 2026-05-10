Completed Task: "Explore internal/run, internal/sandbox, internal/harness, and internal/orchestrator"

# Plan

## Goal

Implement git worktree-based sandbox isolation so each agent run operates in an isolated environment, preventing LLM changes from mixing with uncommitted human work and enabling parallel runs and atomic apply-to-main.

## BLOCKED BY

Big workpackage will be messed up by one worker.
Splitting the plan, especially for Qwen3.6 workers will require a PM agent.

## Context

- `internal/sandbox/sandbox.go` uses `Config{RepoPath, SessionPath, RepoWritable}` to configure the sandbox.
- `internal/sandbox/builder.go` sets up SBPL rules based on a `workspace` parameter which currently matches `RepoPath`.
  <<<<<<<<<< wrong level of abstraction, the sandbox operates on ro/rw/rexec paths, not the concept of a "workspace" or "repo".
- `internal/harness/sandbox_cli_runner.go` passes the `RepoPath` to `sandbox.Config` and doesn't explicitly set `cmd.Dir`.
- `internal/orchestrator/orchestrator.go` uses `Engine.RunDirFactory` (defaulting to `agent.NewSessionDir` in `internal/agent/session.go`) to set up the session directory.
- `cmd/orqestra/main.go` instantiates the `SandboxCLIRunner` once at startup using `os.Getwd()`.

_Architectural Decisions:_

- Introduce `WorkspacePath` to differentiate the git repo root from the isolated worktree directory. Rationale: The agent needs read-access to the whole repo but write-access confined only to the worktree to ensure safety.
- The Orchestrator handles worktree lifecycle (creation, auto-commit, QA branch creation, apply-to-main, and cleanup). Rationale: Orchestration is the logical owner of the run lifecycle and multi-stage pipeline, keeping runners stateless.
- Failed runs retain their worktrees for inspection. Rationale: Preserves debugging context for the user.

## Constraints

- Do NOT modify the core SBPL language or engine logic; only adjust the builder rules.
- OPTIONALLY implement AI-generated commit messages.
- implement parallel per-package worktrees.
- Do NOT touch `internal/tui/` without reading `.github/tui-instructions.md`.
- Ensure fallback to `RepoPath` if `WorkspacePath` is empty for backward compatibility.
- Ensure the agent retains read access to the entire `RepoPath`.

## Risks

- **Dirty Working Tree Conflicts**: If the repo has uncommitted changes, `git worktree add` or `git apply` might fail or behave unexpectedly. _Mitigation_: Implement a strict dirty-tree pre-flight check before starting a run.
- **Dangling Worktrees**: If the orchestrator crashes, worktrees might be left behind. _Mitigation_: Keep track of runs in `.orqestra/runs/` and clean up `orqestra/run-<slug>` branches cleanly on success; leave them on failure for manual intervention.

## Work Packages

### 1. Introduce `internal/run` package and Worktree Lifecycle

Create the core types and functions for managing run directories, worktrees, and run metadata.

**Steps:**

1. Create `internal/run/run.go`.
2. Define `RunDir` struct with `Root` and `Artifacts` fields.
3. Implement `NewRunDir(repoPath, slug string) (RunDir, error)`.
4. Implement `CreateWorkerWorktree(ctx context.Context)` to run `git worktree add <Root/worker> -b orqestra/run-<slug>`.
5. Implement `CommitWorkerChanges(ctx context.Context, message string)` to stage and commit changes in the worker worktree.
6. Implement `CreateQAWorktree(ctx context.Context, commitHash string)` to run `git worktree add <Root/qa> --detach <commitHash>`.
7. Implement `ApplyToMain(ctx context.Context)` to apply the diff from the worker branch to the main repo.
8. Implement `CleanupWorktrees(ctx context.Context)` to forcefully remove the worktrees and delete the branch.
9. Implement `CheckWorkingTree(repoPath string)` to return an error if `git status --porcelain` is not empty.

**Done when:**

- `go test ./internal/run/...` passes.
- Functions correctly execute the underlying `git` commands and handle errors appropriately.

### 2. Update Sandbox Configuration for WorkspacePath

Separate the repository root from the writable workspace directory in the sandbox configuration.

**Steps:**

1. Edit `internal/sandbox/sandbox.go` to add `WorkspacePath string` to `Config`.
2. Update `New()` in `internal/sandbox/sandbox.go` to resolve symlinks for `WorkspacePath` and fallback to `RepoPath` if `WorkspacePath` is empty.
3. Edit `internal/sandbox/builder.go` `NewProfileBuilder`. Change the rules so it grants read/write access to the `workspace` parameter (which will be `WorkspacePath`) and read-only access to `RepoPath`.
4. Edit `internal/harness/sandbox_cli_runner.go` to add `WorkspacePath string` to `SandboxCLIRunnerConfig`.
5. Update `run()` in `internal/harness/sandbox_cli_runner.go` to set `cmd.Dir = r.cfg.WorkspacePath` if it is not empty before running the command.
6. Pass `r.cfg.WorkspacePath` to the `sandbox.Config` struct in `internal/harness/sandbox_cli_runner.go`.

**Done when:**

- `go test ./internal/sandbox/... ./internal/harness/...` passes.
- A test verifies that `cmd.Dir` is set correctly when `WorkspacePath` is provided.

### 3. Orchestrator Integration and Per-Run Runners

Update the orchestrator to manage worktrees and instantiate runner instances per-run instead of globally.

**Steps:**

1. Edit `internal/orchestrator/orchestrator.go` to replace `agent.NewSessionDir` usage with `run.NewRunDir`.
2. In `orchestrator.Engine.run()`, call `CheckWorkingTree` and abort if the tree is dirty.
3. Call `CreateWorkerWorktree` to create the worker environment.
4. Construct the `workerRunner` using `harness.NewSandboxCLIRunner` passing the `WorkspacePath` and `Writable: true`.
5. After the worker completes, call `CommitWorkerChanges`.
6. Construct the QA environment by calling `CreateQAWorktree`.
7. Construct the `qaRunner` with `WorkspacePath` set to the QA worktree and `Writable: false`.
8. After QA completes successfully, call `ApplyToMain` and `CleanupWorktrees`.
9. Edit `cmd/orqestra/main.go` to remove the global instantiation of `SandboxCLIRunner` for worker and QA, passing configuration factories to the orchestrator instead.

**Done when:**

- `go test ./internal/orchestrator/... ./cmd/orqestra/...` passes.
- Orchestrator tests verify that worktrees are created and cleaned up on success.

### 4. TUI Pre-flight Check and Cleanup

Integrate the dirty-tree check into the UI and migrate artifact paths.

**Steps:**

1. Edit `internal/tui/model.go` to surface the dirty-tree error from the orchestrator as a specific state prompting the user to stash or cancel (read `.github/tui-instructions.md` first).
2. Update all `s.ArtifactPath(...)` calls across the codebase to use `runDir.ArtifactPath(...)`.
3. Add `.orqestra/runs/` to `.gitignore`.
4. Update `agent.ListRuns` (or `run.ListRuns`) to scan `.orqestra/runs/` for run history.

**Done when:**

- `go test ./internal/tui/...` passes.
- `git status` ignores `.orqestra/runs/`.

## Verification

- `go test ./...`
- `go build ./cmd/orqestra`
- `go vet ./...`

## Assumptions

- The Orchestrator has access to the repository path and the current context when starting a run.
- Git is installed and available in the system PATH.
- `ApplyToMain` using `git diff | git apply` is acceptable and we don't need a full merge commit.

## Gotchas

- **Git command execution context**: Ensure all `git` commands executed by `internal/run` use the correct `-C <path>` arguments so they operate on the correct worktree or repository root.
- **Symlink Resolution**: Mac OS temporary directories are heavily symlinked; `WorkspacePath` must be fully evaluated (using `filepath.EvalSymlinks`) before passing it to SBPL.
- **Detached HEAD for QA**: The QA worktree uses a detached HEAD. If QA tests write any files and attempt to commit them, they will be lost. (Mitigated by QA being `Writable: false`).
- **Silent Fallbacks**: Do not swallow `os.Stat` or `git` command errors. Always return them wrapped with context.
