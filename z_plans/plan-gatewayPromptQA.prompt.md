# Plan: Gateway Prompt QA & Improvement Framework

## The Gateway's Real Job

Good user prompts can already drive good results with or without the gateway system prompt. The gateway prompt is therefore not a quality booster. It is a policy layer: it should only change behavior when the user prompt leaves the pipeline too much freedom, points at the wrong task shape, or asks for work the pipeline cannot route directly.

Signal: correct task shaping by prompt class.

| Prompt class | Bad gateway | Good gateway policy |
| ------------ | ----------- | ------------------- |
| Small bounded task | Widens a valid narrow ask into architecture work | Preserve and compress. Accept without widening. |
| Ambiguous task | Passes vague work downstream | Coach to resolve the end state. |
| Suspicious local fix | Always accepts the local patch or always asks "why" | Escalate only when prompt cues suggest a workaround, recurring symptom, or cross-cutting problem. |
| Already strategic task | Rewrites the user's framing | Preserve the framing faithfully. |
| Fantasy / infeasible task | Accepts or asks vague follow-ups | Narrow or deflect into executable scope. |

The current ACCEPT BIAS rule, _"User names a file -> accept"_, is too blunt. It protects small tasks, but it also suppresses justified escalation. The target is not "always ask WHY". The target is "ask WHY only when prompt evidence says widening is useful."

Decision: treat the system prompt as a policy layer, not a quality booster.

Problems solved:
- avoids giving the prompt credit for cases where the user prompt already saturates the outcome
- protects Orqestra's small-task UX from unnecessary coaching
- creates a causal link between prompt text and gateway behavior

Decision: root cause extraction is conditional escalation, not the universal job.

Problems solved:
- prevents blanket widening of valid small asks
- defines when escalation is helpful instead of treating it as always good
- makes the thesis falsifiable

---

## End State

1. `make eval-gateway` runs 50 human-authored prompt-class cases against configured models and reports class-specific task-shaping outcomes.
2. Prompt changes cannot merge without a paired eval report that shows no meaningful regression on small bounded tasks.
3. Prompt changes are judged by paired baseline vs candidate deltas on the same user prompts. The plan measures the marginal effect of the system prompt, not raw model quality.
4. The eval pattern is reusable for researcher/planner prompts after the gateway policy is stable.

---

## Phase 0: Golden Corpus

Human-authored. Human-reviewed. 50 cases. Ground truth must be defensible on both small-task preservation and justified escalation.

### Structure

Each case: `{id, class, input, expected_verdict, expected_policy_action, expected_behavior}`.

| Class | Count | What it tests |
| ----- | ----- | ------------- |
| Small bounded | 15 | Valid narrow asks that should be preserved and compressed |
| Ambiguous | 10 | Asks with multiple plausible end states that should be clarified |
| Suspicious local fix | 10 | Local patches that may hide a larger problem and should escalate only when prompt cues justify it |
| Already strategic | 5 | High-signal prompts that already frame the work correctly |
| Fantasy / infeasible | 10 | Unbounded asks that should be narrowed or deflected |

Decision: classify cases by task shape, not by generic "depth."

Problems solved:
- defines the signal instead of using "noise reduction" as a loose proxy
- makes small-task preservation visible as a first-class constraint
- gives justified escalation and false escalation separate measurements

### Expansion (later, after baseline)

Opus generates ~200 more in the style of the golden 50. Deduplicate by embedding similarity. Human reviews. Gets to ~250 clean cases with known lineage.

### Variance calibration (before trusting any metric)

Run the 50 golden cases 5 times against the same model. Measure run-to-run fluctuation. If pass rate varies +/- 3%, a 10% improvement is detectable at this corpus size. If it varies +/- 15%, the model is unreliable for this prompt and paired prompt comparisons will be noisy.

---

## Phase 1: Eval Binary - `cmd/eval-gateway/main.go`

Standalone binary. Not a test file.

```
./bin/eval-gateway \
  --config orqestra.local.yaml \
  --candidate-prompt-file gateway_candidate.md \
  --judge-config orqestra.yaml \
  --parallelism 10 \
  --class suspicious-local-fix
```

### Flags

- `--config path` (repeatable): each config provides a model under test
- `--baseline-prompt-file path` (optional): override `cfg.Gateway.SystemPrompt`
- `--candidate-prompt-file path` (optional): paired comparison against the baseline prompt
- `--judge-config`: provides judge models (Sonnet for bulk, Opus for low-confidence escalation)
- `--class`: run only one prompt class for targeted iteration
- `--parallelism N`: concurrent claude subprocess calls (default 10)
- `--output path`: write results JSON

### What it does

1. For each `--config`: `config.Load(path)` -> construct the gateway runner using the same runner path as `cmd/orqestra/main.go`. Do not hand-roll model resolution in the eval binary.
2. Resolve the baseline system prompt from `cfg.Gateway.SystemPrompt`, or override it with `--baseline-prompt-file`.
3. If `--candidate-prompt-file` is provided, run both baseline and candidate prompts against the same case, same model, and same runtime policy. Only the system prompt text changes.
4. For each result: run contract checks first, then Sonnet judges policy fit. Low-confidence cases escalate to Opus.
5. Aggregate by class and report per-class deltas, false escalation, and regressions.
6. Print report, append to history, write results JSON.

### Reused infrastructure

- `config.Load()` for provider/model resolution and embedded prompt loading
- `harness.NewClaudeCLIFromConfig()` or a shared helper extracted from `cmd/orqestra/main.go`
- `agent.NewGateway()` as the gateway under test
- `harness.BuildModelEnv()` for claude binary environment wiring

Decision: compare baseline and candidate on the same user prompt.

Problems solved:
- isolates the system prompt contribution from the user prompt
- shows where the system prompt matters and where it should be silent
- makes regressions on small tasks visible instead of hiding them inside pooled averages

---

## Phase 2: The Metric - Task Shaping, Not Depth

### Two metric families, tracked separately

**Metric 1: Contract correctness**

- valid JSON object extracted from model output
- `result.Verdict == expected_verdict` (where applicable)
- required brief fields populated
- question count <= 3
- regression detector for parser/schema failures

**Metric 2: Policy fit**

- small-task preservation rate
- ambiguity clarification precision
- fantasy deflection rate
- justified escalation precision
- false escalation rate
- strategic-prompt preservation rate

These diverge: verdict accuracy 100% + false escalation 20% = gateway gets `accept|coach` right but still damages small-task UX.

### Paired delta interpretation

- If baseline and candidate both preserve a clear small task, delta = 0 and that is correct.
- Candidate gets no bonus for being more verbose on a prompt that already needed no intervention.
- Candidate gets credit only where it improves policy fit in a class where the system prompt should matter.

Decision: score the prompt on policy fit, not on generic quality or verbosity.

Problems solved:
- defines the signal instead of relying on vague "noise reduction"
- gives zero-delta good-user-prompt cases the correct interpretation
- prevents aggressive coaching from masquerading as improvement

### Judge prompt

The judge receives: INPUT, BASELINE OUTPUT, CANDIDATE OUTPUT (if any), EXPECTED_BEHAVIOR, EXPECTED_POLICY_ACTION, and the gateway system prompt's labeled sections (GROUNDING RULES, VERDICT RULES, ACCEPT BIAS, COACHING TRIGGERS, COACHING VOICE, BRIEF FIELDS).

Returns:

```json
{
  "pass": false,
  "expected_action": "preserve",
  "actual_action": "escalate",
  "reason": "Candidate widened a bounded UI task into architectural coaching with no prompt cues that justify escalation.",
  "violated_section": "ACCEPT BIAS",
  "confidence": 0.85
}
```

### Cascade: Sonnet -> Opus

- Sonnet judges all cases (~$0.50 for 50 cases)
- Confidence < 0.7 -> escalate to Opus (~5-10 cases, ~$0.30)
- Total per paired eval run: roughly 2x the single-prompt model cost, with judge cost still dominating

### Judge calibration (run once before trusting)

1. Take 30 random eval outputs
2. Human labels each: pass/fail with reason
3. Sonnet judges the same 30
4. Agreement > 85% -> judge is trustworthy
5. Agreement < 70% -> fix judge prompt first

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
  "small_task_preservation": 0.94,
  "ambiguity_clarification": 0.82,
  "fantasy_deflection": 0.90,
  "justified_escalation": 0.63,
  "false_escalation": 0.06
}
```

### Report output

```
Run 42 (prompt a1b2c3, model qwen3.6)
  Contract correctness:      96% (prev: 95%, best: 96%)
  Small-task preservation:   94% (delta:  0%)
  Ambiguity clarification:   82% (delta: +8%)
  Fantasy deflection:        90% (delta: +2%)
  Justified escalation:      63% (delta: +14%)
  False escalation:           6% (delta: -5%)

  Worst regression: none
  Biggest gain: suspicious_local_fix
```

### Plateau detection

If no target class metric improves > 2% over the last 5 runs and small-task preservation stays flat, print: *"Quality plateau. Last 5 prompt changes had no measurable policy effect."*

### Dual-model divergence

When > 1 config provided, print divergence by prompt class:

| Model A | Model B | Diagnosis |
| ------- | ------- | --------- |
| ✓       | ✓       | Prompt policy is clear |
| ✓       | ✗       | Model capability gap |
| ✗       | ✗       | Prompt policy is the bug |
| diverge |         | Prompt policy is ambiguous for smaller models |

Target: divergence rate < 15%.

---

## Phase 4: Prompt Mutation Workflow

### How failure -> prompt edit actually works

1. Run eval. Read report:

   ```
   small_bounded (15 cases): preservation 80%
     FALSE_ESCALATION: 3

   suspicious_local_fix (10 cases): justified_escalation 50%
     MISSED_ESCALATION: 4
   ```

2. Read the false-escalation failures. Judge reasons cluster: _"candidate widened a bounded task with no symptom cues"_. Read the missed-escalation failures. Reasons cluster: _"prompt contained workaround/repeated-failure cues, but ACCEPT BIAS forced acceptance anyway"_.

3. Now you know the exact problem: ACCEPT BIAS and escalation cues are both too blunt. The prompt either protects named tasks too aggressively or widens them too aggressively.

4. Edit that section in `pipeline.yaml`: replace blanket logic with explicit policy, for example: _"User names a file or feature -> preserve by default. Escalate only when the prompt signals workaround, repeated failure, contradictory constraints, or likely cross-cutting cause."_

5. `go build -o bin/eval-gateway ./cmd/eval-gateway` (recompiles embedded prompt)

6. `./bin/eval-gateway --class suspicious-local-fix` and `./bin/eval-gateway --class small-bounded` -> verify both the target gain and the non-regression constraint

7. `./bin/eval-gateway` -> full suite, verify no regression

8. Commit: `pipeline.yaml` + `eval_results.json` together

### Prompt distillation (Opus-assisted, after manual iteration plateaus)

```sh
claude --print --model claude-opus-4-7 \
  -p "Current gateway prompt: $(...) Eval failures: $(...) Rewrite to improve small-task preservation and justified escalation, reduce tokens 30%." \
  > candidate.txt
```

Swap into pipeline.yaml -> rebuild -> eval -> diff. Accept or reject.

---

## Phase 5: Schema Evolution (AFTER eval pipeline exists)

1. If paired eval shows the planner benefits from explicit policy intent, add `TaskShape string`, `EscalationRationale string`, and `Verification []string` to `PromptBrief` in `internal/agent/gateway.go`
2. Update `Evaluate()` validation rules to match the new fields
3. Update gateway system prompt in `pipeline.yaml` with the chosen task-shaping policy
4. Update `buildPlannerInput()` in `internal/orchestrator/orchestrator.go` to pass the new fields
5. Update `incorporateAnswers()` to preserve structured context instead of free-text append only
6. `make eval-gateway` must show improvement over baseline with no meaningful small-task regression

Schema changes come AFTER eval exists so we measure before/after.

---

## Token Budget

| Operation | Cost |
| --------- | ---- |
| Baseline + candidate model calls (50 cases, 1 small model) | ~$0.02 |
| Sonnet judge (50 paired cases) | ~$0.50 |
| Opus escalation (~8 cases) | ~$0.30 |
| **Full paired eval run, 1 model** | **~$1** |
| **Targeted class re-run** | **~$0.20** |

---

## Files

- `cmd/eval-gateway/main.go` - eval binary
- `cmd/eval-gateway/judge.go` - judge prompt + cascade Sonnet -> Opus
- `cmd/eval-gateway/report.go` - JSONL history, trend, plateau detection
- `cmd/eval-gateway/testdata/golden.json` - 50 human-authored cases
- `cmd/eval-gateway/testdata/eval_history.jsonl` - run log (append-only)
- `scripts/gen_gateway_corpus.md` - Opus prompt for expanding from golden set
- `scripts/calibrate_judge.md` - instructions for judge validation
- `internal/config/pipeline.yaml` - the gateway prompt (the thing being improved)
- `Makefile` - `eval-gateway` and `eval-gateway-quick` targets

## Verification

1. `go build ./cmd/eval-gateway` - compiles
2. `./bin/eval-gateway --config orqestra.local.yaml` - baseline run against the current gateway prompt
3. `./bin/eval-gateway --config orqestra.local.yaml --candidate-prompt-file candidate.md` - paired comparison with the same user prompts
4. After prompt edit: rebuild -> re-run -> show target-class gains and no meaningful small-task regression
5. Variance check: 5 runs of same prompt -> fluctuation < +/- 5%

## Decisions

- Corpus: 50 human-authored prompt-class cases. NOT generated.
- System prompt: policy layer, not quality booster.
- Primary signal: class-specific task shaping, not generic depth.
- Good-user-prompt zero-delta cases are expected and correct.
- Root-cause extraction: conditional escalation, not default behavior.
- Judge: Sonnet -> Opus cascade. Calibrated against human labels before trusting.
- Plateau: 5-run rolling window, 2% threshold.
- Schema changes gated on eval pipeline existing first.
