# Plan: Gateway Prompt QA & Improvement Framework

## The Gateway's Real Job

Not a binary accept/coach classifier. It **extracts root cause thinking**:

| Input                                | Bad gateway (current)                 | Good gateway (target)                                                                                                                                      |
| ------------------------------------ | ------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| _"fix nil panic in config"_          | Accept. Task: "Fix nil panic." Done.  | Coach: _"What allowed nil to reach the config reader? Should we validate all config entry points?"_ Then accept with a brief that frames the systemic fix. |
| _"add retry button to error dialog"_ | Accept. Scope: `internal/tui`.        | Coach: _"Are these errors transient? Should we fix the root cause rather than add retry UX?"_                                                              |
| _"design error handling strategy"_   | Accept (user already thinks like PE). | Accept. Preserve the PE framing faithfully.                                                                                                                |
| _"build me a SaaS"_                  | Coach with vague scope questions.     | Deflect: _"This needs to be much more specific before a team can work on it."_                                                                             |

The current prompt's ACCEPT BIAS section — _"User names a file → accept"_ — actively prevents this. It short-circuits the depth extraction.

The gateway has TWO jobs:

1. **Extract root cause** — "fix nil panic" → "why did the nil reach here? should we fix the config validation boundary?"
2. **Deflect fantasy** — "build me a SaaS" → "be more specific"

Even clear prompts get driven toward the bigger picture. Naming a file is necessary but not sufficient — the gateway should still ask WHY.

---

## End State

1. `make eval-gateway` runs 50 golden cases against configured models, prints depth-graded results with violated-section tracing, exits 0/1
2. Prompt changes cannot merge without eval results committed alongside (golden file discipline)
3. Gateway output demonstrates deeper understanding than the input — surfaces root causes, architectural questions, PE-level framing
4. The eval pattern is reusable for researcher/planner prompts

---

## Phase 0: Golden Corpus

LLM-generated. Human verified. 50 cases.

### Structure

Each case: `{id, category, input, expected_behavior}` — where `expected_behavior` describes what good output looks like (not just a verdict).

| Category              | Count | What it tests                                                               |
| --------------------- | ----- | --------------------------------------------------------------------------- |
| Root cause extraction | 15    | User names a bug. Gateway should probe for systemic cause                   |
| Scope escalation      | 10    | User asks for a patch. Gateway should surface the architectural question    |
| PE coaching           | 10    | User gives implementation instructions. Gateway should redirect to specs/QA |
| Clear + deep          | 5     | User already thinks like PE. Gateway should accept faithfully               |
| Fantasy deflection    | 10    | Unbounded scope. Gateway should push back                                   |

### Expansion (later, after baseline)

Opus generates ~200 more in the style of the golden 50. Deduplicate by embedding similarity. Human reviews. Gets to ~250 clean cases with known lineage.

### Variance calibration (before trusting any metric)

Run the 50 golden cases 5 times against the same model. Measure run-to-run fluctuation. If pass rate varies ±3%, a 10% improvement is detectable at this corpus size. If it varies ±15%, the model is unreliable for this prompt — more cases won't help.

---

## Phase 1: Eval Binary — `cmd/eval-gateway/main.go`

Standalone binary. Not a test file.

```
./bin/eval-gateway \
  --config orqestra.local.yaml \
  --config orqestra.anthropic.yaml \
  --judge-config orqestra.yaml \
  --parallelism 10 \
  --category root-cause
```

### Flags

- `--config path` (repeatable): each config provides a model-under-test (qwen3.6, haiku, flash)
- `--judge-config`: provides judge models (Sonnet for bulk, Opus for low-confidence escalation)
- `--category`: run only one category for targeted iteration
- `--parallelism N`: concurrent claude subprocess calls (default 10)
- `--output path`: write results JSON

### What it does

1. For each `--config`: `config.Load(path)` → `ResolveModel(cfg.Gateway.Model)` → `NewClaudeCLI(resolved, WithNoTools())` → `NewGateway(runner, cfg.Gateway)`
2. System prompt is always the same (compiled into binary via `pipeline.yaml` embed). What varies is the MODEL executing it.
3. For each corpus case: `gw.Evaluate(ctx, input, nil)` — real LLM call. Connection failures → ERROR, continue.
4. For each result: Sonnet judge grades depth. Low-confidence → Opus escalation.
5. Aggregate by category → group by violated prompt section.
6. Print report, append to history, write results JSON.

### Reused infrastructure (zero new packages)

- `config.Load()` — provider/model resolution
- `harness.NewClaudeCLI()` + `WithNoTools()` — subprocess runner
- `agent.NewGateway()` — the gateway under test
- `harness.BuildModelEnv()` — env vars for claude binary

---

## Phase 2: The Metric — Depth, Not Correctness

### Two metrics, tracked separately

**Metric 1: Verdict accuracy** — binary, no judge needed

- `result.Verdict == expected_verdict` (where applicable)
- Regression detector. Inarguable.

**Metric 2: Depth score** — requires Sonnet judge

- Did the gateway increase the depth of the prompt?
- For bugs: did it probe for systemic cause?
- For patches: did it surface the architectural question?
- For PE-level inputs: did it preserve the framing?
- For fantasy: did it push back firmly?
- Improvement signal. Softer. Requires calibration.

These diverge: verdict accuracy 100% + depth 60% = gateway always gets accept/coach right but never probes deeper.

### Judge prompt

The judge receives: INPUT, MODEL OUTPUT, EXPECTED BEHAVIOR, and the gateway system prompt's labeled sections (GROUNDING RULES, VERDICT RULES, ACCEPT BIAS, COACHING TRIGGERS, COACHING VOICE, BRIEF FIELDS).

Returns:

```json
{
  "pass": false,
  "depth": "shallow",
  "reason": "Accepted without probing why nil reached config reader. Should have asked about config validation boundary pattern.",
  "violated_section": "ACCEPT BIAS",
  "confidence": 0.85
}
```

### Cascade: Sonnet → Opus

- Sonnet judges all cases (~$0.50 for 50 cases)
- Confidence < 0.7 → escalate to Opus (~5-10 cases, ~$0.30)
- Total per eval run: ~$1

### Judge calibration (run once before trusting)

1. Take 30 random eval outputs
2. Human labels each: pass/fail with reason
3. Sonnet judges the same 30
4. Agreement > 85% → judge is trustworthy
5. Agreement < 70% → fix judge prompt first

---

## Phase 3: Tracking and Plateau Detection

### History: `eval_history.jsonl`

One line per run, appended:

```json
{
  "run": 42,
  "timestamp": "...",
  "config": "orqestra.local.yaml",
  "prompt_hash": "a1b2c3",
  "verdict_accuracy": 0.92,
  "depth_score": 0.78,
  "category_scores": { "root_cause": 0.65, "fantasy": 0.9 }
}
```

### Report output

```
Run 42 (prompt a1b2c3, model qwen3.6)
  Verdict accuracy: 92% (prev: 90%, best: 94%)
  Depth score:      78% (prev: 76%, best: 78%)  ← plateau

  Trend (last 10 runs):
  verdict: 85 87 88 90 90 91 90 92 92 92  ↗ improving
  depth:   70 72 74 76 76 77 76 78 78 78  → plateau (5 runs flat)

  Worst category: root_cause 65% (ACCEPT BIAS ×4, COACHING TRIGGERS ×2)
```

### Plateau detection

If depth*score hasn't improved > 2% over the last 5 runs AND prompt_hash has changed each time → print: *"Quality plateau. Last 5 prompt changes had no measurable effect."\_

### Dual-model divergence

When > 1 config provided, print divergence table:

| Model A | Model B | Diagnosis                                   |
| ------- | ------- | ------------------------------------------- |
| ✓       | ✓       | Prompt is clear                             |
| ✓       | ✗       | Model capability gap (OK if primary passes) |
| ✗       | ✗       | Prompt is the bug — both models misled      |
| diverge |         | Prompt is ambiguous for small models        |

Target: divergence rate < 15%.

---

## Phase 4: Prompt Mutation Workflow

### How failure → prompt edit actually works

1. Run eval. Read report:

   ```
   root_cause (15 cases): 65% depth
     ACCEPT BIAS: 4 failures
     COACHING TRIGGERS: 2 failures
   ```

2. Read the 4 ACCEPT BIAS failures. Judge reasons cluster: _"model accepts 'fix nil panic in config.go' because ACCEPT BIAS says 'User names a file → accept' — but the prompt should probe for systemic cause"_

3. Now you know the exact problem: ACCEPT BIAS line _"User names a file, package, command, error, or existing feature → accept"_ short-circuits root cause extraction.

4. Edit that section in `pipeline.yaml`: replace with _"User names a file → probe WHY before accepting. Ask what allowed this bug/gap to exist."_

5. `go build -o bin/eval-gateway ./cmd/eval-gateway` (recompiles embedded prompt)

6. `./bin/eval-gateway --category root-cause` → root_cause depth rises 65% → 82%

7. `./bin/eval-gateway` → full suite, verify no regression

8. Commit: `pipeline.yaml` + `eval_results.json` together

### Prompt distillation (Opus-assisted, after manual iteration plateaus)

```sh
claude --print --model claude-opus-4-7 \
  -p "Current gateway prompt: $(...) Eval failures: $(...) Rewrite to fix failures, reduce tokens 30%." \
  > candidate.txt
```

Swap into pipeline.yaml → rebuild → eval → diff. Accept or reject.

---

## Phase 5: Schema Evolution (AFTER eval pipeline exists)

1. Add `Motivation string`, `Verification []string`, `EscalationHint string` to `PromptBrief` in `internal/agent/gateway.go`
2. Update `Evaluate()` validation: accept requires `Verification ≥ 1`
3. Update gateway system prompt in `pipeline.yaml` with root-cause extraction behavior
4. Update `buildPlannerInput()` in `internal/orchestrator/orchestrator.go` to pass new fields
5. Update `incorporateAnswers()` — structured context instead of text append
6. `make eval-gateway` — must show improvement over baseline

Schema changes come AFTER eval exists so we measure before/after.

---

## Token Budget

| Operation                                  | Cost       |
| ------------------------------------------ | ---------- |
| Model-under-test (50 cases, 1 small model) | ~$0.01     |
| Sonnet judge (50 cases)                    | ~$0.50     |
| Opus escalation (~8 cases)                 | ~$0.30     |
| **Full eval run, 1 model**                 | **~$1**    |
| **Targeted category re-run**               | **~$0.20** |

---

## Files

- `cmd/eval-gateway/main.go` — eval binary
- `cmd/eval-gateway/judge.go` — judge prompt + cascade Sonnet→Opus
- `cmd/eval-gateway/report.go` — JSONL history, trend, plateau detection
- `cmd/eval-gateway/testdata/golden.json` — 50 human-authored cases
- `cmd/eval-gateway/testdata/eval_history.jsonl` — run log (append-only)
- `scripts/gen_gateway_corpus.md` — Opus prompt for expanding from golden set
- `scripts/calibrate_judge.md` — instructions for judge validation
- `internal/config/pipeline.yaml` — the gateway prompt (the thing being improved)
- `Makefile` — `eval-gateway` and `eval-gateway-quick` targets

## Verification

1. `go build ./cmd/eval-gateway` — compiles
2. `./bin/eval-gateway --config orqestra.local.yaml` — runs against qwen3.6, prints depth report
3. `./bin/eval-gateway --config orqestra.local.yaml --config orqestra.anthropic.yaml` — dual-model divergence
4. After prompt edit: rebuild → re-run → depth score improves, no regressions
5. Variance check: 5 runs of same prompt → fluctuation < ±5%

## Decisions

- Corpus: 50 human-authored golden cases. NOT generated.
- Expansion: Opus generates from golden seed, AFTER baseline is established
- Metric: depth (judge-graded) + verdict accuracy (binary). Tracked separately.
- Judge: Sonnet → Opus cascade. Calibrated against human labels before trusting.
- Plateau: 5-run rolling window, 2% threshold.
- Schema changes gated on eval pipeline existing first.
