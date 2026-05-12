# Implementation Plan: Git Worktree Sandbox Isolation

## Goal

Implement git worktree-based sandbox isolation so each agent run operates in an isolated environment. This prevents LLM changes from mixing with uncommitted human work, enables parallel runs, and allows atomic apply-to-main.

## Principles & Architecture

- **Git Worktrees:** Use git worktrees to provide complete isolation without copying the repository.
- **Orchestrator Lifecycle:** The `orchestrator` manages the lifecycle of the worktrees (creation, commit, QA branch, apply, cleanup).
- **Abstractions:** Introduce `WorkspacePath` in `sandbox.Config` to separate the (read-only) repository root from the (read-write) execution environment.
- **Fail Fast:** Pre-flight checks must enforce a clean working directory before starting a run to avoid conflicts.
- **Error Handling:** All errors must be explicitly checked, wrapped with context, and never swallowed (especially `git` command failures).

## Step-by-Step Implementation

### Step 1: Implement the `run` package for Worktree Lifecycle

Create `internal/run` to manage run directories, git worktrees, and run metadata.

1. **Create `internal/run/run.go`:**
    - Define the `RunDir` struct containing `Root` (the session base path) and `Artifacts` (path for logs, specs).
    - Implement `NewRunDir(repoPath, slug string) (RunDir, error)`. This creates the base structure inside `.orqestra/runs/<slug>`.
2. **Implement Git Operations (in `internal/run/run.go`):**
    - `CheckWorkingTree(repoPath string) error`: Runs `git status --porcelain`. Returns an error if the output is not empty (dirty tree).
    - `CreateWorkerWorktree(ctx context.Context, repoPath, runRoot, slug string) (string, error)`: Runs `git -C <repoPath> worktree add <runRoot>/worker -b orqestra/run-<slug>`. Returns the worktree path.
    - `CommitWorkerChanges(ctx context.Context, worktreePath, message string) error`: Runs `git -C <worktreePath> add -A` and `git -C <worktreePath> commit -m "<message>" --allow-empty`.
    - `CreateQAWorktree(ctx context.Context, repoPath, runRoot, commitHash string) (string, error)`: Runs `git -C <repoPath> worktree add <runRoot>/qa --detach <commitHash>`. Returns the QA worktree path.
    - `ApplyToMain(ctx context.Context, repoPath, branchName string) error`: Runs `git -C <repoPath> merge --squash <branchName>` (or equivalent diff/apply logic depending on exact requirements, ensuring atomic commit).
    - `CleanupWorktrees(ctx context.Context, repoPath, runRoot, slug string) error`: Runs `git -C <repoPath> worktree remove -f <runRoot>/worker`, `git -C <repoPath> worktree remove -f <runRoot>/qa`, and `git -C <repoPath> branch -D orqestra/run-<slug>`.
3. **Gotcha Mitigation:** Ensure all `git` commands use `-C <path>` appropriately. Wrap all `exec.Command` errors meticulously with `fmt.Errorf`.

### Step 2: Update Sandbox and CLI Runner Configuration

Adjust the security profiles to recognize the new workspace path vs the repository root.

1. **Edit `internal/sandbox/sandbox.go`:**
    - Add `WorkspacePath string` to the `Config` struct.
    - In the `New` function, use `filepath.EvalSymlinks(cfg.WorkspacePath)` to resolve the true path (critical on macOS). If `cfg.WorkspacePath` is empty, fallback to `filepath.EvalSymlinks(cfg.RepoPath)`.
2. **Edit `internal/sandbox/builder.go`:**
    - Update `NewProfileBuilder`. The `.sb` template must grant read/write permissions to `workspace` (which maps to `WorkspacePath`) and strictly read-only access to `RepoPath`.
3. **Edit `internal/harness/sandbox_cli_runner.go`:**
    - Add `WorkspacePath string` to `SandboxCLIRunnerConfig`.
    - In the `run()` function, set `cmd.Dir = r.cfg.WorkspacePath` if it is not empty.
    - Pass `r.cfg.WorkspacePath` into the `sandbox.Config` struct when constructing the sandbox.

### Step 3: Orchestrator Integration

Modify the orchestrator to initialize worktrees and spawn localized runners.

1. **Update `internal/orchestrator/orchestrator.go`:**
    - Replace `agent.NewSessionDir` usage with `run.NewRunDir`.
    - In `Engine.run()`, immediately call `run.CheckWorkingTree(repoPath)`. If it fails, abort the pipeline and return/emit a clear error indicating the tree must be clean.
    - Call `run.CreateWorkerWorktree` to initialize the worker environment.
    - Modify runner instantiation: Instead of a global runner, use `harness.NewSandboxCLIRunner` dynamically, passing `WorkspacePath: workerPath` and `Writable: true`.
    - After the worker completes (planning/execution), call `run.CommitWorkerChanges`. Get the resulting commit hash.
    - Call `run.CreateQAWorktree` using the hash.
    - Construct the QA runner dynamically with `WorkspacePath: qaPath` and `Writable: false`.
    - Upon successful pipeline completion, call `run.ApplyToMain` followed by `run.CleanupWorktrees`. Leave worktrees intact on failure for debugging.
2. **Edit `cmd/orqestra/main.go`:**
    - Remove the global instantiation of `SandboxCLIRunner`. Pass runner factories or configuration structs to the orchestrator so it can build them per-phase.

### Step 4: TUI Integration and Cleanups

Surface the new constraints in the UI and clean up paths.

1. **Edit `internal/tui/model.go` (and `internal/tui/messages.go` if needed):**
    - Handle the dirty-tree error from the orchestrator. When caught, transition the UI state to prompt the user (e.g., "Working tree is dirty. Please stash or commit changes before running.") *Note: strictly follow `tui-instructions.md` regarding state transitions in `Update()`.*
2. **Global Path Updates:**
    - Audit and update all `s.ArtifactPath(...)` or `agent.SessionDir` usages to point to the new `runDir.Artifacts` path.
3. **Git Ignore:**
    - Add `.orqestra/runs/` to the project's `.gitignore`.
4. **Run History:**
    - Update history scanning logic (`agent.ListRuns` or equivalent) to read from `.orqestra/runs/` instead of `.orqestra/sessions/`.

### Verification & Testing

- Write comprehensive unit tests for `internal/run/...` mocking `exec.Command` if necessary, or utilizing a temporary git repository.
- Ensure `go test ./internal/sandbox/...` and `go test ./internal/harness/...` verify the directory contexts.
- Run the E2E application (`go build ./cmd/orqestra`) to manually verify that a dirty git tree prevents execution, and that a successful run correctly merges changes back to main while cleaning up the `.orqestra/runs/<slug>` directory.
