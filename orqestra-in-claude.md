# Orqestra in Claude Code — Design Spec

> **Status:** design, agreed via grill-me session 2026-06-16.
> **Supersedes** `plan-flexible-pipeline.md` for the *interactive* runtime, and folds
> its `PipelineSetup` effort into `internal/config/pipeline.yaml`.

---

## 1. Thesis

Run the orqestra pipeline **natively inside Claude Code** as composable slash commands —
*and* keep the Go orchestrator. They **coexist**:

- **Go orchestrator** — headless, seatbelt-sandboxed, no human in the loop → autonomous / CI.
- **Claude adapter** — main-session-driven, native diffs, a human at every gate → interactive.

The thing that makes coexistence cheap instead of two-codebases-rotting:

> **`internal/config/pipeline.yaml` is the core.** One harness-agnostic contract;
> both runtimes are *consumers* of it. Prompts are authored once, never forked.

```
internal/config/pipeline.yaml   roles · prompts · models(abstract) · tools · permission · retries · sandbox · composition
   ├── Go orchestrator   reads it (embedded)   → subprocess harnesses, seatbelt sandbox, MCP human gate
   └── Claude adapter    render-claude emits    → ~/.claude/agents/*.md + ~/.claude/commands/*.md
```

Shared contract = the YAML **plus** the `.orqestra/` artifact filename layout (so `validate`
and `merge` find artifacts identically regardless of which runtime produced them).

---

## 2. The core grows: `pipeline.yaml`

Today it holds `defaults`, `researcher`, `architect`, `critic`, `worker`, `retry`, `sandbox`.
Two additions:

1. a **`validator`** role (§7).
2. a **`composition`** section — stages, presets, gates — so the Go `PipelineSetup` and the
   slash presets are **one declarative spec**. Sketch (schema TBD):

```yaml
composition:
  stages: [research, plan, grill, implement, validate, merge]

  presets:
    full:   { research: agent,  grill_rounds: 1, validate: independent }
    quick:  { research: inline, grill_rounds: 0, validate: self }
    # research: agent → separate Explorer pass | inline → architect self-researches
    # validate: independent → validator subagent | self → worker self-validation only

  merge:
    mode: separate                  # NEVER auto-runs after implement; its own command
    conflicts: resolve_trivial_or_fail

  # Gates are runtime-shaped:
  #  - interactive (Claude): every command boundary IS a human gate — no config needed.
  #  - headless (Go): which boundaries pause for the MCP human gate.
  headless_human_gates: [after_plan]
```

Per-role render metadata (which roles the Claude adapter runs as the **main session** vs a
**subagent**):

```yaml
architect:
  claude_execution: main_session    # ← the one role that is NOT a subagent in Claude
# researcher / critic / worker / validator default to: claude_execution: subagent
```

---

## 3. Roles — one family, two executions

| Role | Reference / job | Go runtime | Claude adapter |
|------|-----------------|------------|----------------|
| **Explorer** (`researcher`) | gather codebase facts, read-only | subagent | `/orqestra-research` → subagent |
| **Architect** | author plan, disposition critiques, drive merge, talk to human | subagent | **main session** in `plan` / `grill` / `merge` |
| **Critic** | fresh-eyes review: plan ⟂ request (pre-build) | subagent | subagent in `/orqestra-grill` |
| **Builder** (`worker`) | make the change, self-validate, commit | subagent (seatbelt) | subagent (`isolation: worktree`) in `/orqestra-implement` |
| **Validator** *(new)* | build ⟂ plan + build ⟂ repo-rules (post-build) | subagent | subagent in `/orqestra-validate` |

**Why the architect is special:** in Claude, plan authoring and revision live in the *main
session* so revision is conversational and diffs are native (`git diff` for code, file diffs
for `plan.md`). In Go (no human), the architect is an ordinary sandboxed subagent. The
renderer therefore routes the architect prompt into **command bodies**, the other four into
**agent files**.

---

## 4. `render-claude` — the bridge (new Go subcommand)

Reads embedded `pipeline.yaml`, emits the Claude artifacts. **Build-time codegen**, not
runtime YAML parsing (Claude wants static agent files; `subagent_type: orqestra-critic` must
resolve by name). Adaptation rules:

| pipeline.yaml | → Claude artifact |
|---|---|
| `model: medium \| large` | `sonnet \| opus` |
| `mcp__orqestra__AskUserQuestion` | native `AskUserQuestion` |
| "the orchestrator saves it…", "ExitPlanMode is not available…" | stripped (harness-isms) |
| `disallowed_tools: [...]` | inverted into the agent file's allowed `tools:` |
| `permission_mode` | frontmatter `permissionMode` |
| `architect` (`claude_execution: main_session`) | rendered into `plan`/`grill` **command bodies** |
| `researcher / critic / worker / validator` | rendered into `~/.claude/agents/orqestra-*.md` |
| `composition.presets`, `composition.stages` | the `/orqestra` preset command + one command per stage |

**One source, two runtimes, zero drift.** (The four hand-written `~/.claude/agents/orqestra-*.md`
already differ from the YAML — e.g. native vs `mcp__orqestra__AskUserQuestion`, critic
`inherit` vs YAML `medium` — exactly the drift this removes. They become generated artifacts.)

---

## 5. Command set (rendered) — each = one artifact = one human gate

`[ ]` = optional path/branch arg → absent uses the session's latest → **that arg is the entire
BYO-plan mechanism**. Boundaries are gates because **disk is the source of truth**: a command
ends, writes its artifact, and *you* choose whether to run the next.

```
/orqestra-research              → research.md            Explorer (subagent, read-only)
/orqestra-plan                  → plan.md                main session = Architect; self-critique; eats latest research.md (else spawns Explorer)
/orqestra-grill     [plan]      → plan.md′ + critic.md   Critic (subagent) → Architect per-finding disposition; REPEATABLE
/orqestra-implement [plan]      → branch orq/<id> + work.md   Builder (worktree subagent), self-validates
/orqestra-validate  [plan][br]  → validation.md          Validator (subagent); plan optional
/orqestra-merge     [br]        → base                   main session; git-diff gate; resolve-trivial-or-fail; cleanup. ALWAYS separate.

/orqestra <task>                FULL    research → plan → grill×N → implement → validate   ⏹ STOP @ worktree
/orqestra <task> --quick        QUICK   plan(inline-research + self-critique) → implement   ⏹ STOP @ worktree
```

- **Every run stops at the worktree** with the diff shown and *"run `/orqestra-merge`"*. Merge
  is never inside a run — the only step that touches the base branch.
- **`/orqestra-grill` repeatability** is the iteration knob (run it again for a harder pass),
  replacing a `deliberation_loops` config in the interactive flow.
- **Cancel/abandon** at any post-build point preserves the worktree branch; it never discards
  the Builder's commits.

### Grill internals (the loop)
Critic subagent returns findings → the main-session Architect gives **each finding an explicit
disposition**: *accept → fix `plan.md`* or *reject → justify*. Justified rejections expose
author-bias instead of hiding it; you review the `plan.md` diff + dispositions at the boundary.
No separate architect subagent, no whole-plan re-feed.

---

## 6. State & session layout

- **Disk is the source of truth.** Resumable; `/clear`-friendly between phases.
- **Session dir:** `.orqestra/<claude-session-name>/` — ties the run to the Claude session so
  same-session commands share state with no pointer/mtime guessing.
  *(Go runtime keeps `.orqestra/sessions/<run>/`; the inner filenames below are the shared
  contract — open: unify the parent dirs.)*

```
.orqestra/<claude-session-name>/
  prompt.md            task
  research.md          Explorer fact report
  plan.md              current plan (overwritten; git-trackable)
  critic.md            latest Critic report
  grill_journal.md     per-finding dispositions across grill rounds
  work.md              Builder validation report
  validation.md        Validator journal (evidence, typed checks)
  branch: orq/<id>     worktree branch (Builder commits here)
```

**Open — session-name resolution:** how a command reads the current Claude session id is
unconfirmed (candidates: env var, `~/.claude/sessions/<pid>.json` lookup, or a marker the first
command writes). Pin before implementing the commands.

---

## 7. Validator spec (the sharp one)

**One validator agent. Governing principle: _discrete (falsifiable) conditions outrank
probabilistic (judgment) ones._**

- **Two reference layers**
  1. **Plan** — falsifiable gates (`Done when`, `Verification`) **+ deviation/gap detection**.
  2. **Repo rules** — the **union** of discovered instruction files
     (`CLAUDE.md`, `.github/copilot-instructions.md`, `.cursorrules`, `AGENTS.md`, …),
     symlink-deduped.
- **Plan is optional** → with a plan: conformance **+** compliance; without: compliance only.
  *That plan-free mode is the reuse story — point it at any diff, in any repo.*
- **Method:** run **check-by-check**, **journal to disk as you go**, **every finding cites
  source evidence** (command output / file:line).
- **Typed checks:** `[mechanical]` (command + exit code) vs `[judgment]` (cited reasoning).
  Mechanical checks **dominate the verdict**; judgment findings are **advisory** unless they
  map to a mechanized rule. The journal tags every line's kind — otherwise "source evidence"
  is a lie the moment a style-nit sits next to a test exit code.
- **File-scoped rules:** load instruction rules **scoped to the touched paths** — mirror
  orqestra's own `<system_router>` (it loads `tui-instructions.md` only for `internal/tui/`
  edits). No blanket enforcement.
- **Deviation/gap = structured bidirectional cross-reference**, not prose: every Work Package
  → satisfying diff hunk *or* `GAP`; every changed hunk → authorizing package *or* `DEVIATION`.
- **Tooling:** *executes* (`make test/lint`) but must **not mutate source** — a different
  profile than the read-only critic. Its value over the Builder's self-validation is
  **skeptical re-proof** ("assume the worker mis-read its own PASS; prove conformance from
  evidence"), not mere re-execution.
- **Verdict:** reports **conformance** and **compliance** *separately*; severity per layer;
  rule-blocking configurable (orqestra: banned-pattern = FAIL; a foreign repo: maybe advisory).

---

## 8. What changes in the repo

- **Decommission** `~/.claude/workflows/orqestra.js` and the `/orqestra` command that invokes
  it. The Workflow's inline prompt copies die with it.
- Hand-written `~/.claude/agents/orqestra-*.md` → **generated** by `render-claude`.
- `plan-flexible-pipeline.md` → **superseded**: the Go pipeline's configurable composition now
  lives in `pipeline.yaml` (`composition`), read by both runtimes.

---

## 9. Build order (each step builds green)

1. **Grow `pipeline.yaml`** — add `validator`; add `composition` (stages/presets/merge/gates);
   add `architect.claude_execution: main_session`.
2. **`render-claude` subcommand** — YAML → agents + commands with the §4 adaptation. Generate
   and commit the artifacts.
3. **Validator agent** (§7) in `pipeline.yaml`, rendered out.
4. **Decommission `orqestra.js`** + its command.
5. **Go runtime reads `composition`** from YAML (the old `PipelineSetup` work, now YAML-driven)
   — the surviving, re-scoped slice of `plan-flexible-pipeline.md`.

---

## 10. Open items

- **Session-name resolution** mechanism (§6) — blocks command implementation.
- **`composition` schema** — finalize keys/semantics; reconcile interactive gates (free at
  boundaries) vs `headless_human_gates`.
- **Parent-dir unification** — `.orqestra/<session>/` vs `.orqestra/sessions/<run>/`.
- **Instruction-rule caching** — extract discrete rules per-validate-run (simple, current
  lean) vs cache a committed checklist (faster, drift risk).
- **Grill disposition surfacing** — auto-apply + review-at-boundary (current lean) vs pause to
  confirm high-impact dispositions mid-command.
