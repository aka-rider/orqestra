# Orqestra — Context

Shared language for Orqestra, a macOS-first Go CLI/TUI that orchestrates Claude Code
through a harness. This file is a glossary, not a spec — it defines what terms mean, not
how anything is built.

## Timeline & Presentation (TUI)

**Timeline**:
The single, chronological, read-only scrollback that is the main pipeline view: a vertical
stack of full-width Frames in the order events occurred. Replaces the older split of a
text transcript above a separate live streaming area.
_Avoid_: transcript, scrollback, log, feed (when referring to this unified view)

**Frame**:
One full-width unit in the Timeline — a single event rendered as a stacked block (a run of
model prose, a tool use, a markdown document, a phase separator, an orchestration notice).
The unit of copy-to-clipboard.
_Avoid_: block, card, entry, row (for the Timeline unit)

**Live Frame**:
The most-recent Frame while its event is still unfolding (model text still streaming, a tool
still pending, an interaction still open). At most one is live at a time; it is the only Frame
that changes between renders.
_Avoid_: active block, streaming area, console

**Static Frame**:
A Frame whose event has concluded. It is frozen, read-only history — rendered once and never
changes again. A Live Frame becomes Static when superseded.
_Avoid_: frozen block, committed line

**Phase separator**:
A Static Frame that marks an orchestration boundary in the Timeline (e.g. the move from
research to deliberation to plan to worker). Rendered as a labelled full-width rule.
_Avoid_: rule line, divider, banner

**Prose Frame**:
A Frame holding a run of agent commentary, shown as plain text (no markdown rendering).
The streaming tail is a live Prose Frame; it freezes when the run pauses for a tool or ends.
_Avoid_: speech, message, text block

**Tool Frame**:
A Frame recording one tool invocation — its name, a human-readable detail, and an
outcome (pending → ok/error). Persists in the Timeline (it is not wiped on agent change).
_Avoid_: activity, tool line, action

**Plan Frame**:
The single markdown-rendered Frame kind: a document (today, the implementation plan)
shown with rich formatting including width-responsive tables. The only Frame that is not
plain text.
_Avoid_: plan view, gate view, document

**Steer message**:
A Static Frame recording a user turn — a message typed to redirect the agent, a plan
comment, an approval, or an answer to a question. The user's half of the Timeline record.
_Avoid_: post, user input, prompt (for this Frame)

## Agent stream (harness)

**Event frame**:
One line of Claude CLI stream-json (NDJSON) emitted by the agent subprocess. Always qualified
as "event frame" to stay distinct from a Timeline **Frame**, which is a presentation unit.
_Avoid_: frame (unqualified, in harness/orchestrator code)
