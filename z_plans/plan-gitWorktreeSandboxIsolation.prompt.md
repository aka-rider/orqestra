# Plan: Git Worktree Sandbox Isolation

Replace the "mount whole repo" sandbox with git-worktree-based isolation. Read-only agents see the full repo without write. Worker and QA each get their own worktree under `.orqestra/runs/<timestamp-slug>/`. On QA pass, diff is applied flat to the current branch. On failure, worktrees are left for inspection.

**Architecture:**

```
.orqestra/runs/<2026-05-11T01-13-25-azure-pokemon>/
├── worker/       ← git worktree (branch: orqestra/run-<slug>)
├── qa/           ← git worktree (detached at worker's final commit)
├── artifacts/    ← plan.json, qa_report.json, etc.
└── meta.json     ← status, timestamps, branch info
```

---

## Phase 1: Run Directory & Worktree Management

1. **New `internal/run/` package** — Replaces `agent.SessionDir`. Manages full run lifecycle: `RunDir` struct, `CreateWorkerWorktree()` (`git worktree add ... -b orqestra/run-<slug>`), `CreateQAWorktree()` (detached HEAD at worker's commit), `CommitWorkerChanges()`, `ApplyToMain()` (git diff | git apply), `CleanupWorktrees()`, `WriteMeta()`/`ReadMeta()`.

2. **Dirty tree pre-flight** — `CheckWorkingTree()` returns uncommitted/untracked status. Gates worktree creation.

3. **TUI pre-flight screen** — _depends on 2_. When dirty tree detected, offer: stash / commit with AI message (x-small model, user approves/edits) / proceed anyway / cancel.

## Phase 2: Sandbox Config Refactoring

4. **Add `WorkspacePath` to `sandbox.Config`** — _parallel with Phase 1_. The path the agent actually works in. SBPL subpath rules target this instead of `RepoPath`. Worker's workspace = its worktree. Read-only agents: workspace = repo root, `RepoWritable=false`.

5. **Update `SandboxCLIRunner`** — _depends on 4_. Gains `WorkspacePath` field. Sets `cmd.Dir` to it. Agent is sandboxed to its worktree, not the main repo.

6. **Worker sees ONLY worktree** — The worktree IS a full copy of repo state (that's how git worktrees work), so no second read-only mount needed. Simpler SBPL, stronger isolation.

## Phase 3: Orchestrator Integration

7. **Orchestrator creates worktrees before worker launch** — _depends on 1, 5_. Runner construction moves from startup to per-run. `Engine.Runners.Worker` becomes a factory or is constructed in `run()`.

8. **Auto-commit worker output** — _depends on 7_. After worker completes, orchestrator commits all changes. If empty diff → auto-pass QA.

9. **Create QA worktree from worker commit** — _depends on 8_. Second worktree at same commit, separate sandbox. QA's test side-effects (build artifacts, coverage) don't touch worker's tree.

10. **Post-QA logic** — _depends on 9_. Pass → `git diff | git apply` onto current branch + cleanup worktrees. Fail → leave on disk with `meta.json` status=failed.

## Phase 4: Migration

11. **Replace `SessionDir` with `RunDir`** — Artifacts go to `runDir.ArtifactsPath()`. Remove `internal/agent/session.go`.

12. **`.gitignore`** — Add `.orqestra/runs/`.

---

## Relevant Files

- `internal/sandbox/sandbox.go` — Add `WorkspacePath` to `Config`
- `internal/sandbox/builder.go` — SBPL targets `WorkspacePath`
- `internal/harness/sandbox_cli_runner.go` — Accept `WorkspacePath`, set `cmd.Dir`
- `internal/orchestrator/orchestrator.go` — Worktree lifecycle, per-run runner construction, post-QA merge/discard
- `internal/agent/session.go` — Replaced by new `internal/run/`
- `internal/agent/work_audit.go` — `GitDiffSummary` operates on worktree path
- `cmd/orqestra/main.go` — Worker/QA runner moves from startup to per-run
- `internal/tui/model.go` — New dirty-tree pre-flight state

## Verification

1. Unit tests for `internal/run/`: temp git repo → create worktree → commit → apply → cleanup
2. Sandbox SBPL test: `WorkspacePath` set → subpath rules point to worktree not repo root
3. Integration: full pipeline creates worktrees, worker writes, QA validates, diff applied
4. Manual: dirty tree → TUI pre-flight screen appears
5. Manual: QA fail → worktrees on disk, `meta.json` shows failure
6. Sandbox isolation: worker cannot write outside worktree

## Decisions

- Orchestrator auto-commits worker changes (not the worker itself)
- QA worktree is detached HEAD — no branch pollution
- `git diff | git apply` for merge (flat, no merge history)
- Failed runs kept forever (user responsibility)
- Single shared worktree for parallel work packages (MVP); per-package worktrees as follow-up
- `orqestra/run-<slug>` branch deleted on successful cleanup

## Further Considerations

1. Multi-package parallel workers could each get their own worktree — defer to follow-up
2. TUI commit-message generation needs an unsandboxed lightweight runner (just text generation, no file access)
3. Should `orqestra runs list` / `orqestra runs prune` CLI commands be added? Recommend yes as separate follow-up
