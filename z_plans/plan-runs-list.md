# Plan: Runs List UI

**Status**: Stub — not yet designed
**Depends on**: plan-gatewayAgent (split layout must exist first)

## Goal

Add a runs history view that lets users browse past pipeline runs, inspect artifacts, and restart from a previous prompt.

## Open Questions

1. Where does the runs list live in navigation? (top-level tab? slash command? key binding?)
2. What metadata is shown per run? (timestamp, prompt, status, tokens, duration, files changed)
3. Can a user "replay" a run or only view artifacts?
4. Storage format: current session directories under `.orqestra/sessions/` — is that sufficient?
5. How far back? All runs? Last N? Configurable retention?

## Rough Shape

- Runs stored as JSON artifacts in `.orqestra/sessions/<timestamp>-<slug>/`
- List view shows: timestamp, goal (truncated), status (success/failed/cancelled), tokens, duration
- Detail view shows: full goal, spec, QA report, files changed
- "New run from this prompt" action pre-fills the prompt screen

## Next Steps

- Design after the split layout from plan-gatewayAgent is implemented
- Consider whether this is a TUI-only feature or also headless (`orqestra runs list`)
