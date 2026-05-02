# Orqestra

LLM agent orchestration system for complex task execution. Coordinates planning, validation, and execution through a pipeline of specialized agents backed by frontier and local models.

## Architecture

```
User Prompt
    → Planner (Claude Opus via proxy → generates specification)
    → Plan Validator (Gemini/local model → validates spec independently)
    → Human Gate (TUI displays plan, user confirms)
    → Worker (executes against spec)
    → Work Validator (validates output against spec)
```

The **specification** is the shared contract. Planner, Worker, and Validators operate independently against it.

## Requirements

- Go 1.26+
- [copilot-api](https://github.com/nicepkg/copilot-api) proxy (or compatible OpenAI endpoint)
- Optional: local llama-server (Ollama) for validation/workers

## Quick Start

```bash
# Build
make build

# Run with a prompt (launches interactive TUI)
./orqestra "refactor the auth module to use JWT"

# Run with a specific config preset
./orqestra --config local "add unit tests for the parser"
```

## Usage

```
orqestra [flags] <prompt>
orqestra [flags] plan <prompt>
orqestra [flags] validate <spec-json-file>
orqestra [flags] exec <spec-json-file>
orqestra [flags] usage
orqestra [flags] reset-usage [model]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `orqestra.yaml` | Config file path or preset name (`local`, `flash`) |
| `--json` | `false` | Output JSON instead of human-friendly text |
| `--no-execute` | `false` | Plan and validate only, skip execution |

### Subcommands

- **`plan`** — Generate a specification from a prompt without executing
- **`validate`** — Validate an existing spec JSON file
- **`exec`** — Execute a previously validated spec
- **`usage`** — Show token usage statistics
- **`reset-usage`** — Reset token usage counters

With no subcommand, the prompt flows through the full pipeline (plan → validate → gate → execute → validate output).

## Configuration

Orqestra uses YAML config files. Three presets are included:

| File | Use Case |
|------|----------|
| `orqestra.yaml` | Default — Opus planner, Gemini validator, Qwen workers |
| `orqestra.local.yaml` | Fully offline — all models via local llama-server |
| `orqestra.flash.yaml` | Fast iteration — lighter models |

Presets can be referenced by name: `--config local` resolves to `orqestra.local.yaml`.

### Config Structure

```yaml
providers:
  copilot-proxy:
    base_url: http://127.0.0.1:4141
    api_key: dummy
    type: openai

models:
  opus-planner:
    provider: copilot-proxy
    model: claude-4.6-opus
    token_limit: 1M

planner:
  model_ref: opus-planner

validator:
  model_ref: gemini-reviewer

worker:
  model_ref: qwen3.6
  permission_mode: full
  timeout: 45m

work_validator:
  model_ref: qwen3.6

sandbox:
  enabled: false
  image: orqestra-sandbox:latest
  memory: 4g
```

## Infrastructure

### Copilot Proxy

The copilot-api proxy provides an OpenAI-compatible endpoint routing to Claude, Gemini, and other models:

```bash
docker compose up -d copilot-proxy
```

### Local Models (Optional)

For offline or cheap validation, point the `llama-cpp` provider at an Ollama instance:

```yaml
providers:
  llama-cpp:
    base_url: http://localhost:11434
    api_key: dummy
    type: openai
```

## Development

```bash
# Run tests
make test

# Lint
make lint

# Build sandbox image (for sandboxed execution)
make sandbox-image

# Run sandbox integration tests
make sandbox-test

# Clean build artifacts
make clean
```

## Project Structure

```
cmd/orqestra/       Entry point
internal/
  agent/            Top-level agent orchestration
  config/           YAML config loading and validation
  gate/             Human approval gate
  harness/          LLM harness client (Claude CLI, OpenAI-compatible)
  intent/           Intent classification
  planner/          Specification generation
  sandbox/          Docker-based sandboxed execution
  scheduler/        DAG-based execution graph scheduler
  tokenlimit/       Token budget tracking
  tui/              Bubble Tea terminal UI
  types/            Shared domain types
  validator/        Plan and work validation
```

## License

Private.
