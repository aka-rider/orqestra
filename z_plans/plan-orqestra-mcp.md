# Plan: Orqestra as MCP Server — Bidirectional Tool Bridge

**Date**: 2026-05-07
**Status**: Placeholder — future enhancement, not v3 scope.

---

## Concept

Expose Orqestra's orchestration capabilities as an MCP server, enabling bidirectional integration:

1. **Orqestra → Claude Code MCP**: Agents running under Orqestra can access external MCP servers (databases, APIs, browser, etc.) through Claude Code's native MCP support — this already works via `--allowedTools "mcp__*"`.

2. **External → Orqestra MCP**: Expose Orqestra itself as an MCP server that other tools (VS Code Copilot, Claude Desktop, other agents) can invoke. This turns Orqestra from a standalone CLI into a composable building block.

---

## Possible MCP Tools Orqestra Could Expose

```
orqestra.plan         — Generate a validated specification from a prompt
orqestra.decompose    — Break a spec into parallel work packages
orqestra.execute      — Run a spec or work package under sandbox
orqestra.validate     — Validate a plan or work output
orqestra.status       — Query running pipeline status
orqestra.budget       — Check/set token budget
```

## Possible MCP Resources

```
orqestra://runs/{id}/spec        — Specification for a run
orqestra://runs/{id}/artifacts   — Output artifacts
orqestra://runs/{id}/events      — Stream of pipeline events
```

---

## Why This Matters

- **Agent-of-agents**: A Claude Code session could invoke `orqestra.execute` to delegate a complex multi-step task to a full sandboxed pipeline, then continue its own work.
- **IDE integration**: VS Code Copilot could use Orqestra MCP tools to run validated, sandboxed code changes without leaving the editor.
- **Composability**: Multiple Orqestra instances could chain — one orchestrator's output feeds another's input.

---

## Prerequisites

- v3 harness `query()` API must be stable (Phase 1 of plan-endgame.md)
- Orchestrator must support programmatic invocation, not just CLI (Phase 3)
- MCP SDK for Go (or implement protocol directly — it's JSON-RPC over stdio)

---

## Open Questions

- Authentication/authorization model for MCP access to sandboxed execution
- Resource lifecycle — how long do run artifacts persist?
- Streaming: MCP supports SSE for resources — map ring buffer events to SSE?
- Should Orqestra MCP server run as a sidecar, or embed in the main binary?
