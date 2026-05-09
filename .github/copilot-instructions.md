# Orqestra — Copilot Instructions

<system_router>
**CRITICAL ROUTING INSTRUCTIONS**:
When working on the codebase, ALWAYS check if your task falls into these domains. If so, you MUST read the corresponding instruction file before generating code or planning architecture changes.

1. **Terminal UI & Elm Architecture**: If editing `internal/tui/`
   👉 Read `.github/tui-instructions.md` first.
2. **Execution, Pipelines, Sandboxing, or Validators**: If editing `internal/agent/`, `internal/seatbelt/`, `internal/harness/`, `internal/tokenlimit/`, or `internal/plan/`
   👉 Read `.github/agent-instructions.md` first.
   </system_router>

<core_principles>

## Core Principles

1. **Mature solutions** — no cowboy code. Strong types, explicit error handling, no errors swallowed.
2. **Pragmatism** — keep it simple. No premature abstraction.
3. **Small iterations** — always a working MVP. Every commit should be runnable.
4. **TDD** — spec first, tests second, implementation third, validation fourth.
5. **Heavy LLM reliance** — the system orchestrates LLMs; use them for planning and validation.
6. **Harness over Direct API** — Harnesses (like VS Code Copilot, Opencode, Claude Code, and VS Code Third-Party Agents) define model behavior. A raw model API call loses MCP integrations, memory context, reasoning loops, autonomous tool usage, and prompt logic built securely by the providers.
7. **Fail fast on corruption** — If data looks inconsistent, abort immediately with a clear error. Never propagate suspect state. Silent failures are bugs.
8. **No silent errors** — Every error must be logged, surfaced, or returned. `_ = err` is banned unless the operation is truly fire-and-forget AND documented why.
9. **User sees truth** — The TUI must never show stale state. If something is running, show what and for how long. If something failed, show why. Activity is always observable.
10. **Security boundary at LLM output** — LLM-generated content (specs, commands, file paths) is untrusted input. Validate, sanitize, or gate before execution. Never exec() LLM output without strict validation buffers.
11. **Idiomatic Go** — `(T, error)` over Result types. Value receivers in Bubble Tea models. No generics where interfaces suffice. Blend into the ecosystem; no surprises.
12. **No magic numbers in layout** — Dimensions must be derived from measurement (`lipgloss.Height`, `lipgloss.Width`, `GetFrameSize`) or from explicit design constraints (min terminal size, split ratio). Bare arithmetic offsets that account for unmeasured chrome are layout bugs.
13. **Value semantics by default** — Prefer value types over pointers. A pointer is justified only when: the type contains a sync primitive, nil has distinct meaning from zero value, or the type is a process/resource handle. 24-byte structs with a working zero value are not justified. Gratuitous pointers create invisible aliasing, nil-guard ceremony, and GC pressure.
14. **Execution metadata is not domain state** — Token usage, timing, session IDs, and other infrastructure metadata must not live as fields on domain types (`RawPlan`, `GatewayResult`, `ValidationReport`, etc.). Return metadata as a separate value: `(Result, harness.TokenUsage, error)`. Domain types belong to the domain; the caller decides metadata lifecycle.
    </core_principles>

<banned_patterns>

## Banned Patterns

These are concrete code patterns that violate the core principles. Reject them in code review and never generate them:

1. **Silent fallback on missing user input** — If the user explicitly specifies a file path, URL, model name, or any resource identifier, its absence is ALWAYS an error. Never fall back to defaults when the user expressed intent. `--config foo.yaml` → file must exist or fatal.
2. **`if os.IsNotExist(err) { /* use defaults */ }`** — This is the canonical silent-failure footgun. The only acceptable use is for truly optional files that are auto-discovered (not user-specified).
3. **Swallowing `os.Stat` errors** — E.g., `if _, err := os.Stat(path); err == nil { ... return err; }`. This swallows permission denied or other system level errors. Always propagate the actual error (`%w`).
4. **`_ = err` without `// fire-and-forget: <reason>`** — Banned without explicit doc comment explaining why.
5. **`err != nil` followed by `log` but no `return`** — Log-and-continue is silent degradation. If you log an error, you must also return it or surface it to the user.
6. **Default values that mask misconfiguration** — If a config field is required for operation, its zero value must cause a clear error at startup, not silently produce broken behavior at runtime.
7. **Fallback model/provider resolution** — If `model_ref` doesn't resolve, fail. Don't silently try a different resolution path or return a degraded runner.
8. **Manual chrome accounting** — `height - headerLines - footerLines` where line counts are hardcoded inline. Use named constants for chrome zones and derive content dimensions via subtraction. If chrome changes, only the constant needs updating.
9. **Gratuitous pointer fields when zero value works** — `Usage *TokenUsage` on any struct where `TokenUsage{}` (zero value) represents "not reported" — banned. Use `Usage TokenUsage`. A real LLM call never reports zero total tokens, so zero is unambiguous absence.
10. **Infrastructure metadata on domain types** — `Usage *harness.TokenUsage` buried on `GatewayResult`, `RawPlan`, `ValidationReport`, or any other agent domain struct — banned. Infrastructure packages must not be imported by domain types to carry side-channel metadata. Surface it at the call boundary.
   </banned_patterns>

<go_engineering>

## Go Engineering DOs and DON'Ts

### DO

- Write the failing test first, then the smallest production change, then run the narrowest relevant `go test` package before broadening.
- Return `(T, error)` from constructors, factories, parsers, resolvers, and runners when configuration, IO, subprocesses, or model references can fail.
- Keep interfaces small and consumer-owned. Prefer concrete structs internally until a seam is needed for tests or alternate implementations.
- Validate all config at load time. Required provider, model, runtime, sandbox, and prompt references must fail before orchestration starts.
- Wrap errors with operation and resource context: `fmt.Errorf("resolve worker model %q: %w", ref, err)`.
- Use table-driven tests for validation matrices and state transitions. Name cases after the behavior, not the implementation detail.
- Prefer channels, `sync.WaitGroup`, contexts, or deterministic test hooks for goroutine coordination.
- Keep package boundaries honest: `agent` owns all agent types (Specification, ValidationReport, ProjectPlan) and implementations (Planner, PlanValidator, Gate, ProjectManager, Recognizer), `plan` handles markdown persistence, `config` resolves config, `harness` runs harnesses, `sandbox` owns isolation, `scheduler` orchestrates execution graphs.
- Treat LLM text, file paths, command args, JSON, YAML, and streamed events as hostile until parsed and validated.

### DON'T

- Do not return `nil` interfaces to represent construction failure. Return `(Interface, error)` so callers cannot confuse "disabled" with "misconfigured".
- Do not log and continue after errors that affect correctness, state truth, model selection, sandbox setup, validation, or user-visible output.
- Do not use `time.Sleep` to synchronize tests. Sleeps are timing guesses, not correctness guarantees.
- Do not introduce package-level mutable state for orchestration, UI state, config, or test fixtures.
- Do not add generic abstractions, option plumbing, or framework layers unless there are at least two real call sites that need them now.
- Do not parse structured formats with string slicing when `encoding/json`, `yaml.v3`, shell quoting helpers, or typed structs can do the job.
- Do not silently truncate, drop, or ignore LLM/harness output unless the discarded data is explicitly non-critical and observable in diagnostics.
  </go_engineering>

<common_gotchas>

## Common Pitfalls and Gotchas

- **Nil interface trap**: an interface value holding a typed nil is not equal to nil. Avoid nullable interface returns from factories.
- **Loop variable capture**: copy loop variables before launching goroutines or creating closures that outlive the iteration.
- **Scanner token limits**: `bufio.Scanner` has a small default token limit. Set an explicit buffer for streamed LLM JSON lines and handle `scanner.Err()`.
- **Map iteration order**: never rely on map order in rendered output, logs, tests, or generated specs. Sort keys before display or comparison.
- **Context cancellation**: subprocesses, validators, sandboxes, and harness sessions must accept and respect `context.Context`.
- **Catch-all packages**: never create `types`, `utils`, `helpers`, or `misc` packages. Types belong in the package that owns the domain concept. If a type is shared, it belongs in the package that defines the behavior.
  </common_gotchas>

<mcp_servers>

## Available MCP Servers

Ensure the latest MCP capabilities are used via the active tools instead of attempting raw API interactions.

- **context7**
- **markitdown**
- **mcp_docker**
- **microsoft_mar**
- **microsoft_pla**
  </mcp_servers>
