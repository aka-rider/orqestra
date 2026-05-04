# Claude Code PTY — Work Package Index

Decomposition of `plan-claudecode-tty.md` into independent work packages for sandboxed worker execution.

## Dependency Graph

```
Wave 1 ─┬─ session-naming
         ├─ artifact-system
         └─ docker-sdk-migration

Wave 2 ─┬─ overlayfs-sandbox (← docker-sdk-migration)
         └─ sandbox-file-transfer (← docker-sdk-migration)

Wave 3 ─── pty-session (← docker-sdk-migration)

Wave 4 ─┬─ term-view (← pty-session)
         └─ input-detection (no deps — pure text matching)

Wave 5 ─── pty-runner-lifecycle (← session-naming, artifact-system, pty-session, overlayfs-sandbox, sandbox-file-transfer)

Wave 6 ─── tui-term-integration (← term-view, pty-runner-lifecycle, input-detection)

Wave 7 ─── e2e-intake (← pty-runner-lifecycle, tui-term-integration, input-detection)

Wave 8 ─┬─ planner-pty (← e2e-intake)
         └─ session-cli (← session-naming, artifact-system)

Wave 9 ─┬─ validator-pm-pty (← planner-pty)
         └─ workers-pty (← planner-pty)

Wave 10 ── work-validator-pty (← workers-pty)

Wave 11 ── remove-old-paths (← planner-pty, validator-pm-pty, workers-pty, work-validator-pty)
```

## Packages by Phase

### Phase 0 — Foundation (Waves 1–4)

| Package | Wave | File |
|---------|------|------|
| [session-naming](phase-0-session-naming.md) | 1 | `internal/sandbox/session_name.go` |
| [artifact-system](phase-0-artifact-system.md) | 1 | `internal/sandbox/artifact.go` |
| [docker-sdk-migration](phase-0-docker-sdk-migration.md) | 1 | `internal/sandbox/docker.go` — replace CLI shell-outs with Go SDK |
| [overlayfs-sandbox](phase-0-overlayfs-sandbox.md) | 2 | `internal/sandbox/docker.go`, `build/sandbox/` — replace BTRFS with OverlayFS |
| [sandbox-file-transfer](phase-0-sandbox-file-transfer.md) | 2 | `internal/sandbox/transfer.go` — SDK CopyTo/CopyFrom for artifacts and files |
| [pty-session](phase-0-pty-session.md) | 3 | `internal/harness/pty_session.go` — Docker exec-attach with TTY |
| [term-view](phase-0-term-view.md) | 4 | `internal/tui/view_term.go` |
| [input-detection](phase-1-input-detection.md) | 4 | `internal/harness/input_detector.go` |

### Phase 1 — Intake Agent E2E (Waves 5–7)

| Package | Wave | File |
|---------|------|------|
| [pty-runner-lifecycle](phase-1-pty-runner-lifecycle.md) | 5 | `internal/harness/pty_runner.go` |
| [tui-term-integration](phase-1-tui-term-integration.md) | 6 | `internal/tui/view_tabs.go`, `model.go` |
| [e2e-intake](phase-1-e2e-intake.md) | 7 | `internal/harness/pty_runner_e2e_test.go` |

### Phase 2 — Generalize (Waves 8–11)

| Package | Wave | File |
|---------|------|------|
| [planner-pty](phase-2-planner-pty.md) | 8 | `internal/planner/planner_pty.go` |
| [session-cli](phase-2-session-cli.md) | 8 | `cmd/orqestra/sessions.go` |
| [validator-pm-pty](phase-2-validator-pm-pty.md) | 9 | `internal/validator/`, `internal/pm/` |
| [workers-pty](phase-2-workers-pty.md) | 9 | `internal/harness/pty_runner.go` (worker path) |
| [work-validator-pty](phase-2-work-validator-pty.md) | 10 | `internal/validator/work_validator_pty.go` |
| [remove-old-paths](phase-2-remove-old-paths.md) | 11 | Multiple (deletions) |

## Execution Notes

- **Wave 1** (3 packages) are fully concurrent — disjoint files.
- **Wave 2** (2 packages) are concurrent — `overlayfs-sandbox` and `sandbox-file-transfer` touch different files.
- **Wave 3** (`pty-session`) is the critical pivot: it depends on the Docker SDK being in place to use `ContainerExecAttach` with TTY. This replaces the local `creack/pty` approach — we need a real Docker TTY, not a host-side PTY wrapping `docker exec`.
- **Wave 5** (`pty-runner-lifecycle`) is the integration point: it combines session naming, artifacts, PTY, OverlayFS extraction, and SDK file transfer into the complete Prepare→Launch→Collect→Destroy lifecycle.
- **Phase 1 must be rock-solid before starting Phase 2.**
- All integration tests require Docker and the rebuilt `orqestra-sandbox:latest` image (no BTRFS, no `--privileged`).
- The E2E test (`e2e-intake`) requires `ANTHROPIC_API_KEY` and costs tokens.

## What Gets Replaced

| Before | After | Package |
|--------|-------|---------|
| `exec.CommandContext(ctx, "docker", ...)` | `github.com/docker/docker/client` SDK | `docker-sdk-migration` |
| BTRFS loop mount + `--privileged` + `btrfs send/receive` | Docker native OverlayFS + `ContainerDiff` API | `overlayfs-sandbox` |
| `docker exec cat` for file extraction | `CopyToContainer` / `CopyFromContainer` | `sandbox-file-transfer` |
| `creack/pty` local PTY wrapping docker exec CLI | `ContainerExecAttach(Tty: true)` hijacked conn | `pty-session` |

## Critical Invariants (Must Not Regress)

These capabilities exist today and MUST survive the migration:

| Capability | Current Implementation | Risk Point | Mitigation |
|------------|----------------------|------------|------------|
| **MCP server access inside sandbox** | Socket mount at `/run/mcp.sock` in `buildCreateArgs()` + entrypoint config | `docker-sdk-migration` rewrites provisioning | Explicit step 10 in `docker-sdk-migration`: mount preservation + integration test |
| **MCP env vars (`DOCKER_MCP_SOCKET`)** | Set in `entrypoint.sh` when socket detected | `overlayfs-sandbox` rewrites entrypoint | `overlayfs-sandbox` step 2.4: "Configure MCP socket if present (keep existing logic)" |
| **Read-only repo mount** | `--mount type=bind,source=<repo>,target=/workspace-src,readonly` | SDK mount config | Preserved in both `docker-sdk-migration` (HostConfig.Mounts) and `overlayfs-sandbox` |
| **Reaper label tracking** | Labels on container: `orqestra.owner`, `orqestra.session`, `orqestra.created` | SDK container create | Preserved in `docker-sdk-migration` step 3 (container.Config.Labels) |
| **Non-root execution** | `--user sandbox` on exec | SDK exec config | `User: "sandbox"` in ExecConfig |

## Future Roadmap

Items from the [original plan](original-plan.md) not covered by current work packages. These are NOT optional enhancements — they are planned features that will become work packages once the foundation is solid.

### Near-Term (after Phase 1 is stable)

#### MCP Socket Forwarding Verification

**Priority: CRITICAL** — Without MCP, agents inside sandboxes cannot use external tools (context7, Serena, postgres, etc.). The socket mount is preserved in `docker-sdk-migration` and `overlayfs-sandbox`, but must be verified end-to-end:

- Claude Code inside a sandbox container can discover and call MCP tools.
- The Docker MCP gateway socket path works through the SDK `ContainerCreate` mount config.
- Air-gapped sandboxes (`network=none`) can still reach MCP via the Unix socket (not over network).
- If MCP breaks post-migration, this becomes a **blocking P0 hotfix** before any other work proceeds.

#### Mouse Passthrough to PTY

Forward mouse events from bubbletea through the hijacked Docker connection to Claude Code. Required for:

- Clicking on file links in Claude Code's output
- Scrolling within Claude Code's own viewport
- Future: selecting text regions for copy

Implementation: `termView` captures `tea.MouseMsg`, converts to xterm mouse escape sequences (`\x1b[M...` or SGR `\x1b[<...`), writes to PTY session.

#### Copy/Paste from Terminal View

Selection model for the terminal buffer:

- Shift+click or shift+arrow to select text regions in the VT buffer
- `cmd+c` / `ctrl+c` (when selection active) copies selected text to system clipboard
- `cmd+v` / `ctrl+v` pastes from clipboard into PTY stdin
- Visual highlight on selected cells

Requires: `golang.design/x/clipboard` or bubbletea's clipboard integration.

#### Scrollback Search

`ctrl+f` within a focused terminal tab:

- Opens a search bar overlay
- Searches the VT scrollback buffer (not just visible screen)
- Highlights matches, `n`/`N` to cycle
- `esc` dismisses search

### Medium-Term (after Phase 2)

#### Session Diffing

Compare two session runs side-by-side:

- `orqestra sessions diff <name-1> <name-2>`
- Shows: prompt differences, spec differences, file change differences
- Useful for: debugging why a re-run produced different results

#### Agent-to-Agent Communication

Currently agents communicate ONLY through the filesystem (artifacts). Future:

- Structured message passing via a shared event bus
- Worker-to-worker coordination for shared resources
- Still mediated by the orchestrator (no direct peer connections)

#### Windows ConPTY Support

The current plan is macOS/Linux only (POSIX PTY via Docker SDK). Windows support requires:

- ConPTY API or Docker Desktop's Windows container support
- Different terminal emulation path
- Low priority — primary users are on macOS

### Out of Scope (Not Planned)

These are explicitly NOT on the roadmap:

- **Web UI / VS Code extension** — Orqestra is a CLI/TUI tool
- **Plugin system** — premature abstraction
- **Multi-tenant / shared server** — single-user tool
- **Streaming to external consumers** — TUI is the only consumer of PTY output
