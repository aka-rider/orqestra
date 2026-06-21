package agent

// DEFECT-02 (INV-P3-VALID) was fixed: ParseValidationOutput now returns
// VerdictFail when no marker-prefixed lines are found (fail-closed).
// Gate: TestParseValidationOutput cases "empty output" and "LLM ignored format"
// now assert VerdictFail. Canary retired 2026-06-21.
