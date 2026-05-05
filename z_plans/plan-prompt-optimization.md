# Plan: Prompt Optimization — Intent, Planner, Plan Validator

## Status: ACTIONABLE DRAFT (v2)

## Self-Critique of v1

The previous version was a fantasy plan. Problems:

1. Metrics were vague floats ("rephrased_fidelity: 0.20") with no grounding
2. Dataset was "synthesize with Opus" — circular (measures agreement with Opus, not task success)
3. Judge was under-specified prose that would produce arbitrary scores
4. Weighted composites hide failure modes (0.9 accuracy + 0.2 JSON validity = "passing" 0.76)

This version defines **binary or ordinal metrics you can verify by reading the output**, concrete dataset examples, and judges with testable correctness criteria.

---

## Scope: 3 Prompts

| Prompt | Runs On | Current Weakness |
|--------|---------|-----------------|
| **Intent** | Qwen3.6 local | Over-clarifies clear prompts; sometimes rejects reasonable scope |
| **Planner** | Opus (via copilot-proxy) | Steps can be vague; validation_commands often trivial ("go build") |
| **Plan Validator** | Gemini 3.1 Pro | 2-line prompt. No rubric. Produces inconsistent verdicts. |

---

## 1. INTENT PROMPT — Metric, Dataset, Judgment

### The Metric (Binary + Ordinal, No Floats)

The Intent prompt produces a JSON with a `verdict` field. This makes evaluation simple — it's a **classification task**. Composites are wrong here. Use:

| Check | Type | Pass Condition | Stage |
|-------|------|----------------|-------|
| `json_parses` | Binary | `json.Unmarshal` succeeds | Gate 1 (Fatal) |
| `schema_valid` | Binary | All required fields present, correct types | Gate 1 (Fatal) |
| `verdict_correct` | Binary | Matches human label exactly | Gate 2 (Fatal) |
| `rephrased_preserves_intent` | Binary | A human reading rephrased would attempt the same task | Quality (Multiplier) |
| `no_scope_creep` | Binary | Rephrased does not add work the user didn't request | Quality (Multiplier) |

**Strict Multiplicative Scoring**:

1. If Gate 1 or Gate 2 fails -> **Score = 0**. (A syntactically perfect JSON that outputs the wrong verdict is completely useless. A correct verdict that is malformed JSON crashes the pipeline).
2. Otherwise: **Score = 1.0** (Base passing score).
3. If it passes Gates, apply penalties for quality flaws: e.g. if `!rephrased_preserves_intent` multiply by `0.5`. If `!no_scope_creep` multiply by `0.8`.

Why strict gating? *Because your eval metric is 80% of the problem. A bad metric with OPRO will confidently converge to a wrong answer.* If you use linear composites (e.g., `0.6*verdict + 0.4*format`), the optimizer will learn to write beautifully formatted wrong verdicts because it gets 40% credit for free. With strict gating, fatal flaws get exactly 0%.

### The Dataset (60 examples, human-labeled)

I don't need 100. I need 60 that are **unambiguous to a human reviewer** — if two humans would disagree on the label, the example is bad, not useful.

**Source**: Real prompts from this project's history + manually written. NOT LLM-generated.

```jsonc
// data/opro/intent/dataset.json
[
  // === ACCEPT (25 examples) ===
  {
    "id": "A01",
    "prompt": "add shift+tab to cycle tabs backwards in the TUI",
    "context": "internal/tui/ has view_tabs.go with forward tab cycling already",
    "label": "accept",
    "why": "Clear feature, clear location, planner can discover the implementation"
  },
  {
    "id": "A02",
    "prompt": "the spinner in view_stream.go doesnt animate when no output is flowing, fix it",
    "context": "view_stream.go exists, uses bubbletea spinner model",
    "label": "accept",
    "why": "Bug report with file reference, clear enough"
  },
  {
    "id": "A03",
    "prompt": "rename CLIRunner to HarnessRunner everywhere",
    "context": "CLIRunner interface in internal/harness/client.go",
    "label": "accept",
    "why": "Mechanical refactor, zero ambiguity"
  },
  // ... 22 more accept examples covering: config changes, test fixes,
  //     file moves, doc updates, dependency bumps, error handling additions

  // === CLARIFY (20 examples) ===
  {
    "id": "C01",
    "prompt": "add auth",
    "context": "No auth system exists. Could mean: API key validation, OAuth for proxy, SSH key for sandbox",
    "label": "clarify",
    "why": "Multiple materially different implementations possible"
  },
  {
    "id": "C02",
    "prompt": "make it faster",
    "context": "Unclear what 'it' is — TUI rendering, planner inference, sandbox startup?",
    "label": "clarify",
    "why": "No clear target"
  },
  {
    "id": "C03",
    "prompt": "add a dashboard",
    "context": "Could be TUI panel, web UI, metrics export, or log viewer",
    "label": "clarify",
    "why": "Form factor and data source unspecified"
  },
  // ... 17 more clarify examples

  // === REJECT (15 examples) ===
  {
    "id": "R01",
    "prompt": "Turn Orqestra into a full SaaS platform with multi-tenant billing, team management, and marketplace",
    "context": "Single-binary CLI tool for 1 contributor",
    "label": "reject",
    "why": "Multi-month, requires inventing business logic"
  },
  {
    "id": "R02",
    "prompt": "Build me a competitor to Cursor with full IDE, LSP, AI chat, and marketplace",
    "context": "This is a CLI orchestrator, not an IDE",
    "label": "reject",
    "why": "Completely different product"
  }
  // ... 13 more reject examples
]
```

**How this is actually created**:

1. I write all 60 examples by hand in one sitting (~90 min)
2. Each gets a `label` and `why` — the `why` is the human reasoning, used to validate the judge
3. No LLM touches the labels. LLM only produces the rephrasings I compare against.

### The Judge (Deterministic + Selective LLM)

**Gate checks (code, not LLM)**:

```go
func judgeIntent(output []byte, example DatasetExample) Score {
    var intent Intent
    if err := json.Unmarshal(output, &intent); err != nil {
        return Score{JSONParses: false, Feedback: []string{"Failed to parse JSON"}} // total fail, score = 0
    }
    if !hasRequiredFields(intent) {
        return Score{SchemaValid: false, Feedback: []string{"Missing required fields in JSON schema"}} // total fail, score = 0
    }

    verdictCorrect := intent.Verdict == example.Label
    feedback := []string{}
    if !verdictCorrect {
        feedback = append(feedback, fmt.Sprintf("Expected verdict %s, got %s", example.Label, intent.Verdict))
    }

    // Only call LLM judge for the subjective checks. (LLM functions now return (bool, string) for qualitative feedback)
    rephraseOK, rephraseErr := judgeLLM_preservesIntent(example.Prompt, intent.Rephrased, example.Context)
    if !rephraseOK { feedback = append(feedback, "Intent lost: "+rephraseErr) }

    noCreep, creepErr := judgeLLM_noScopeCreep(example.Prompt, intent.Rephrased)
    if !noCreep { feedback = append(feedback, "Scope creep detected: "+creepErr) }

    return Score{
        JSONParses: true,
        SchemaValid: true,
        VerdictCorrect: verdictCorrect,
        RephrasedOK: rephraseOK,
        NoScopeCreep: noCreep,
        Feedback: feedback,
    }
}
```

**The LLM judge only answers 2 yes/no questions** (not "score 0-1"):

```
Question 1 (rephrased preserves intent):
Original user prompt: "{original}"
Rephrased version: "{rephrased}"
Repo context: "{context}"

Would a planner executing the rephrased version produce the same
end result as one executing the original? Answer YES or NO only.
```

```
Question 2 (no scope creep):
Original user prompt: "{original}"
Rephrased version: "{rephrased}"

Does the rephrased version add work, features, or scope that the
original did not request? Answer YES or NO only.
```

**Why yes/no**: Because LLMs are >95% reliable at binary classification of concrete questions, but <70% reliable at ordinal scoring. A "NO" from the judge means -1 on that dimension. Done.

**Judge model**: Gemini 3.1 Pro (not the same model that runs the prompt — avoids self-agreement bias. Opus judges would be better but the cross-model disagreement signal is more valuable).

### Judge Calibration (10 minutes, not 2 hours)

Run the judge on 10 hand-picked examples where the answer is obvious:

- 5 where rephrased clearly preserves intent (expect YES)
- 5 where rephrased clearly adds scope or changes intent (expect NO)

If judge gets <9/10, fix the judge question phrasing. If 9/10+, lock it.

---

## 2. PLANNER PROMPT — Metric, Dataset, Judgment

### The Metric (Checklist, not floats)

The planner outputs a Specification JSON. Quality = "would a Qwen3.6 worker succeed with this spec alone?"

| Check | Type | Pass Condition | Stage |
|-------|------|----------------|-------|
| `json_parses` | Binary/code | Valid JSON, all fields present | Gate (Fatal) |
| `validation_commands_run` | Binary/code | Each command is syntactically valid and uses allowed executables | Gate (Fatal) |
| `scope_matches_prompt` | Binary/LLM | Spec doesn't do more or less than what was asked | Gate (Fatal) |
| `references_required_files` | Binary/code | Must contain the specific target files requested | Gate (Fatal) |
| `goal_is_one_sentence` | Binary/code | `len(strings.Split(goal, ".")) <= 2` | Quality |
| `steps_are_imperative` | Binary/LLM | Each step starts with a verb and names a concrete target | Quality |
| `acceptance_is_falsifiable` | Binary/LLM | Each criterion could be checked by running a command | Quality |
| `no_vague_steps` | Count/LLM | Number of steps containing "appropriate", "as needed", "properly" | Quality |

**Strict Multiplicative Scoring**:
This replaces the naive `(sum of passes) / (total checks)` which is highly gamable.

1. If ANY Gate fails (e.g. Scope doesn't match the prompt, or JSON is broken), **Score = 0**.
2. If all Gates pass, start with Score = 1.0. Apply standard style penalties (e.g. `-0.1` for each vague word, `-0.2` if acceptance isn't falsifiable) down to a floor of `0.1`.
All dimensions are evaluated, but OPRO is forced to satisfy the gates before it can min-max the stylistic criteria.

### The Dataset (40 examples)

Planner optimization is expensive (each eval = one Opus call). 40 is enough if examples cover the failure modes.

```jsonc
// data/opro/planner/dataset.json
[
  {
    "id": "P01",
    "prompt": "Replace time.Sleep in scheduler_test.go with channel-based synchronization",
    "context_files": ["internal/scheduler/scheduler_test.go", "internal/scheduler/scheduler.go"],
    "difficulty": "medium",
    "expected_properties": {
      "must_reference_files": ["internal/scheduler/scheduler_test.go"],
      "must_have_validation_command": "go test ./internal/scheduler/...",
      "step_count_range": [3, 7],
      "must_not_contain": ["as appropriate", "if needed", "consider"]
    }
  },
  {
    "id": "P02",
    "prompt": "Add a --json flag to cmd/orqestra/main.go that outputs the plan as JSON instead of TUI",
    "context_files": ["cmd/orqestra/main.go"],
    "difficulty": "easy",
    "expected_properties": {
      "must_reference_files": ["cmd/orqestra/main.go"],
      "must_have_validation_command": "go build ./cmd/orqestra && ./orqestra --json",
      "step_count_range": [2, 5]
    }
  }
  // ... 38 more examples
]
```

**How ground truth works here**: I don't write "gold standard specs" (that's circular — I'd be optimizing agreement with my own spec-writing style). Instead, I define **properties the spec must have** — these are checkable assertions.

### The Judge (Mostly Code, LLM for 3 Checks Only)

```go
func judgePlanner(spec Specification, example PlannerDatasetExample) PlannerScore {
    s := PlannerScore{}
    s.Feedback = []string{}

    // === CODE CHECKS (deterministic, free) ===
    s.JSONParses = spec.Goal != "" && len(spec.Steps) > 0
    s.GoalOneSentence = len(strings.Split(spec.Goal, ". ")) <= 2
    s.StepCountInRange = len(spec.Steps) >= example.Expected.StepCountRange[0] &&
                         len(spec.Steps) <= example.Expected.StepCountRange[1]
    s.ReferencesRequiredFiles = allFilesReferenced(spec, example.Expected.MustReferenceFiles)

    // Explicit whitelist for validation commands to prevent unresolvable dependencies
    s.ValidationCommandsValid = allCommandsAllowed(spec.ValidationCommands, []string{"go", "make", "docker"})
    if !s.ValidationCommandsValid {
        s.Feedback = append(s.Feedback, "Used unapproved commands (must be go, make, or docker)")
    }

    s.NoVagueWords = countVagueWords(spec) == 0

    // === LLM CHECKS (yes/no questions, returning qualitative feedback on failure) ===
    var f1, f2, f3 string
    s.StepsAreImperative, f1 = judgeLLM_stepsImperative(spec.Steps)
    if !s.StepsAreImperative { s.Feedback = append(s.Feedback, f1) }

    s.AcceptanceFalsifiable, f2 = judgeLLM_criteriaFalsifiable(spec.Acceptance)
    if !s.AcceptanceFalsifiable { s.Feedback = append(s.Feedback, f2) }

    s.ScopeMatchesPrompt, f3 = judgeLLM_scopeMatch(example.Prompt, spec)
    if !s.ScopeMatchesPrompt { s.Feedback = append(s.Feedback, f3) }

    return s
}
```

**LLM judge questions** (yes/no only):

```
Steps imperative check:
Here are the steps from a task specification:
{steps as numbered list}

Does every step begin with a concrete action verb (create, modify, add,
remove, rename, extract, replace, run, configure) and name a specific
target (file, function, config key, test)?
Answer YES or NO. If NO, list which step numbers fail.
```

```
Acceptance falsifiable check:
Here are acceptance criteria:
{criteria as numbered list}

Could each criterion be verified by running a single shell command and
checking its exit code or output? (e.g., "go test passes" = yes,
"code is clean" = no)
Answer YES or NO. If NO, list which criteria fail.
```

```
Scope match check:
User asked: "{prompt}"
Spec goal: "{goal}"
Spec steps: {steps}

Does this spec do exactly what was asked — no extra features, no
missing parts? Answer YES or NO.
```

**Judge model**: Gemini 3.1 Pro (cross-model; Opus generates the specs, Gemini judges them).

---

## 3. PLAN VALIDATOR PROMPT — Metric, Dataset, Judgment

### The Metric

The Plan Validator is itself a judge — it must correctly classify specs as pass/warn/fail. This is the easiest to evaluate because it's a **classification task with known labels**.

| Check | Type | Pass Condition |
|-------|------|----------------|
| `json_parses` | Binary/code | Output is valid ValidationReport JSON |
| `verdict_correct` | Binary | Matches the human label (pass/warn/fail) |
| `issues_found_for_bad_specs` | Binary | If spec is flawed, validator identifies ≥1 real issue |
| `no_false_alarms_on_good_specs` | Binary | If spec is good, validator doesn't invent issues |
| `issues_are_specific` | Binary/LLM | Issues name a concrete field/step, not "could be improved" |

**Score = Strict Accuracy (`correct_verdicts / total_examples`)**

Precision/Recall dynamics matter here:

- False Positive (Flags a good spec as failing): Disrupts the loop, requires human override.
- False Negative (Passes a flawed spec): Degrades the end product.
We don't use averaged composites here. Measuring pure classification accuracy against human-labeled ground truth prevents the optimizer from cheating the metric.

### The Dataset (40 examples: 20 good specs, 20 flawed specs)

```jsonc
// data/opro/plan_validator/dataset.json
[
  // === GOOD SPECS (should pass) ===
  {
    "id": "V01",
    "spec": {
      "goal": "Add shift+tab keybinding for reverse tab cycling in the TUI",
      "steps": [
        "In internal/tui/view_tabs.go, add a case for key.ShiftTab in the Update method",
        "Decrement the active tab index, wrapping to len(tabs)-1 if at 0",
        "Add test case in view_tabs_test.go for shift+tab behavior"
      ],
      "acceptance": ["go test ./internal/tui/ passes", "shift+tab cycles tabs in reverse order"],
      "validation_commands": [{"command": "go test ./internal/tui/ -run TestTabCycling"}],
      "constraints": ["Do not change forward-tab behavior"],
      "risks": [],
      "expected_artifacts": ["internal/tui/view_tabs.go", "internal/tui/view_tabs_test.go"]
    },
    "label": "pass",
    "why": "Complete, concrete, testable, scoped"
  },

  // === FLAWED SPECS (should fail) ===
  {
    "id": "V21",
    "spec": {
      "goal": "Improve the TUI",
      "steps": ["Make appropriate changes to improve user experience"],
      "acceptance": ["TUI works better than before"],
      "validation_commands": [],
      "constraints": [],
      "risks": []
    },
    "label": "fail",
    "flaw_type": "vague_steps_and_acceptance",
    "why": "Steps are not actionable, acceptance is not falsifiable, no validation commands"
  },
  {
    "id": "V22",
    "spec": {
      "goal": "Add authentication to the CLI",
      "steps": [
        "Create internal/auth/oauth.go with OAuth2 flow",
        "Add login command to cmd/orqestra/main.go",
        "Store tokens in ~/.orqestra/tokens.json"
      ],
      "acceptance": ["User can log in", "Token persists across sessions"],
      "validation_commands": [{"command": "go build ./..."}],
      "constraints": [],
      "risks": []
    },
    "label": "fail",
    "flaw_type": "contradicts_project_scope",
    "why": "Orqestra uses dummy API keys, not user-facing OAuth. Steps are concrete but the goal contradicts project architecture."
  }
  // ... 36 more examples (mix of good/flawed)
]
```

**How I build this**:

1. Take 20 real specs that the current planner produces (from actual runs) → label them pass/fail manually
2. Write 20 deliberately broken specs by hand — each with a named `flaw_type`
3. The `flaw_type` field is NOT shown to the validator — it's used to check whether the validator's issues match the actual flaw

### The Judge (Entirely Deterministic)

No LLM judge needed here. The metric is **accuracy**:

```go
func judgePlanValidator(validatorOutput ValidationReport, example ValidatorDatasetExample) bool {
    // Did it get the verdict right?
    if validatorOutput.Verdict == example.Label {
        return true // correct
    }
    return false // incorrect
}

func overallScore(results []bool) float64 {
    correct := 0
    for _, r := range results {
        if r { correct++ }
    }
    return float64(correct) / float64(len(results))
}
```

For the flawed specs, I also check: **did the validator identify the correct flaw?**

```go
func issueMatchesFlaw(issues []Issue, expectedFlawType string, spec Specification) bool {
    // Check if any issue references the actual problem using an LLM evaluator
    // Naive string matching is too fragile (e.g. "vague_steps" vs "Lacks actionable instructions")
    for _, issue := range issues {
        if judgeLLM_isIssueMatch(issue.Message, expectedFlawType, spec) {
            return true
        }
    }
    return false
}
```

**This is the simplest judge of all three targets.** Pure code, no LLM, no calibration needed. The only thing that matters is: did the validator output the correct verdict?

---

## 4. OPRO Loop — Concrete Implementation

### What Actually Runs

```
for each prompt_target in [intent, planner, plan_validator]:

    candidates = [current_prompt] + seed_variants(3)
    cache = load_cache() # map of (model, candidate_hash, example_id) -> output
    best_score_overall = 0
    best_candidate_overall = current_prompt

    for iteration in range(12):

        scores = {}
        all_feedbacks = {}
        for candidate in candidates:
            # Use cache to prevent API volume and cost blowout
            results = run_dataset_with_cache(candidate, dataset, target_model, cache)
            score, feedback = judge(results, dataset)
            scores[candidate] = score
            all_feedbacks[candidate] = feedback
            log(iteration, get_hash(candidate), score)

        # Rollback/Degradation Handling: Always preserve the global best
        current_top = top_k(candidates, scores, k=1)[0]
        if scores[current_top] > best_score_overall:
            best_score_overall = scores[current_top]
            best_candidate_overall = current_top

        # Keep top 3 for next gen breeding
        candidates = top_k(candidates, scores, k=3)

        # Convergence check (prevent infinite wandering without improvement)
        if failed_to_beat_best_for_3_iterations():
            break

        # Generate 2 new candidates via OPRO meta-prompt, injecting qualitative context
        new = generate_candidates(
            history=[(c, scores[c]) for c in candidates],
            failure_examples=get_worst_examples(results, all_feedbacks),
            n=2
        )
        candidates.extend([best_candidate_overall] + new) # Re-add overall best to prevent forgetting it

    save(best_candidate_overall, best_score_overall)
```

### Seed Variants (Hand-Written, Not LLM-Generated)

For each prompt, I write 3 variants that test specific hypotheses:

**Intent seed variants**:

1. **Shorter** — strip all examples, keep only the decision rules
2. **More examples** — add 3 concrete in-context examples (few-shot)
3. **Role emphasis** — lean harder on "you are a senior BA who respects developer time"

**Planner seed variants**:

1. **Anti-gold-plate** — add "NEVER add steps the user didn't ask for. 3-7 steps maximum."
2. **File-reference mandate** — add "Every step MUST name at least one file path."
3. **Validation-command-first** — restructure to emphasize validation_commands as the primary output

**Plan Validator seed variants**:

1. **Structured rubric** — replace 2-line prompt with 10-point checklist
2. **Examples** — add 2 pass/fail examples inline
3. **Threshold language** — "FAIL only when a worker would be blocked. WARN for style issues."

---

## 5. What "Done" Looks Like

After optimization:

1. `pipeline.yaml` updated with winning prompts
2. Each prompt has a score file: `data/opro/{target}/final_score.json`
3. Improvement over baseline documented per-target
4. Held-out validation (20% of dataset never seen during OPRO) confirms no overfitting

### Minimum Viable Result

If OPRO produces zero improvement on a target, the baseline prompt was already near-optimal for that model/task pair. That's a valid finding — it means the bottleneck is elsewhere (model capability, not prompt quality).

The **Plan Validator** will almost certainly improve dramatically — going from a 2-line prompt to a structured rubric is not "optimization", it's "doing the job at all."

---

## 6. What I'm NOT Doing

- **DSPy/MIPRO**: Overkill for 3 prompts. OPRO is sufficient. DSPy adds value when you have 10+ prompts in a chain with shared optimization — that's future-state.
- **Genetic algorithms**: LLM-as-optimizer (OPRO) is empirically better because mutations are semantic, not random string perturbations.
- **Full E2E evaluation**: The plan validator optimization doesn't need the planner to run first. Each target is independently evaluable.
- **Human-in-the-loop per iteration**: Human labels the dataset once. The OPRO loop runs unattended. Human validates the winner at the end.
