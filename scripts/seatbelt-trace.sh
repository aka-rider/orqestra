#!/bin/bash
set -euo pipefail

# Seatbelt v2 Trace Script
# Runs Claude inside cmd/sandbox (Seatbelt v2) and captures ALL denied operations
# via macOS kernel log. Produces a clean trace for least-privilege analysis.
#
# Usage:
#   ./scripts/seatbelt-trace.sh [workspace_dir]
#
# Requires:
#   - cmd/sandbox built (or auto-builds from repo root)
#   - claude CLI installed
#   - macOS with sandbox-exec

WORKSPACE="${1:-/tmp/seatbelt-trace-ws}"
TRACE_DIR="/tmp/seatbelt-trace-results"
LOG_FILE="$TRACE_DIR/sandbox-denials.log"
RAW_LOG="$TRACE_DIR/raw-log.txt"
SUMMARY_FILE="$TRACE_DIR/summary.txt"

SANDBOX_BIN="${SANDBOX_BINARY:-}"
CLAUDE_BIN="${CLAUDE_BINARY:-claude}"

echo "=== Seatbelt v2 Trace: Claude Code ==="
echo ""

# --- Find or build sandbox binary ---
if [[ -z "$SANDBOX_BIN" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
  SANDBOX_BIN="$REPO_ROOT/orqestra-sandbox"

  if [[ ! -x "$SANDBOX_BIN" ]]; then
    echo "Building cmd/sandbox..."
    (cd "$REPO_ROOT" && go build -o "$SANDBOX_BIN" ./cmd/sandbox/)
    echo "Built: $SANDBOX_BIN"
  fi
fi

if [[ ! -x "$SANDBOX_BIN" ]]; then
  echo "ERROR: sandbox binary not found at $SANDBOX_BIN"
  echo "  Build it: go build -o ./orqestra-sandbox ./cmd/sandbox/"
  exit 1
fi

# --- Pre-flight ---
if ! command -v "$CLAUDE_BIN" &>/dev/null; then
  echo "ERROR: claude binary not found: $CLAUDE_BIN"
  exit 1
fi

if [[ -z "${ANTHROPIC_BASE_URL:-}" ]]; then
  echo "WARNING: ANTHROPIC_BASE_URL not set. Claude will use its default (Anthropic API)."
  echo "  Set ANTHROPIC_BASE_URL=http://host:port for local model"
  echo ""
fi

# --- Setup ---
mkdir -p "$WORKSPACE" "$TRACE_DIR"

echo "Configuration:"
echo "  Workspace: $WORKSPACE"
echo "  Sandbox:   $SANDBOX_BIN"
echo "  Claude:    $CLAUDE_BIN"
echo "  Trace dir: $TRACE_DIR"
echo ""

# --- Scaffold workspace ---
cd "$WORKSPACE"
if [[ ! -d .git ]]; then
  git init -q
  cat > main.go << 'GOEOF'
package main

import "fmt"

func main() {
	fmt.Println("hello seatbelt")
}
GOEOF
  cat > Dockerfile << 'DEOF'
FROM alpine:3.19
RUN apk add --no-cache git
COPY main.go /app/main.go
CMD ["cat", "/app/main.go"]
DEOF
  git add -A && git commit -q -m "initial"
  echo '// modified' >> main.go
fi

# --- Build sandbox flags ---
SANDBOX_FLAGS=(
  --workspace "$WORKSPACE"
  --claude-binary "$CLAUDE_BIN"
)

if [[ -n "${ANTHROPIC_BASE_URL:-}" ]]; then
  SANDBOX_FLAGS+=(--anthropic-base-url "$ANTHROPIC_BASE_URL")
fi
if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  SANDBOX_FLAGS+=(--anthropic-api-key "$ANTHROPIC_API_KEY")
fi
if [[ -n "${ANTHROPIC_AUTH_TOKEN:-}" ]]; then
  SANDBOX_FLAGS+=(--anthropic-auth-token "$ANTHROPIC_AUTH_TOKEN")
fi
if [[ -n "${ANTHROPIC_MODEL:-}" ]]; then
  SANDBOX_FLAGS+=(--anthropic-model "$ANTHROPIC_MODEL")
fi

# --- Start kernel log capture ---
echo "Starting sandbox denial capture..."
log stream --style compact --predicate 'eventMessage contains "orqestra"' > "$RAW_LOG" 2>&1 &
LOG_PID=$!
sleep 2

cleanup() {
  kill $LOG_PID 2>/dev/null || true
  wait $LOG_PID 2>/dev/null || true
}
trap cleanup EXIT

# --- Helper ---
run_scenario() {
  local label="$1"
  shift

  echo "=== Scenario: $label ==="
  "$SANDBOX_BIN" "${SANDBOX_FLAGS[@]}" -- "$@" \
    > "$TRACE_DIR/${label}-stdout.txt" 2>"$TRACE_DIR/${label}-stderr.txt" || true

  local out
  out=$(head -c 200 "$TRACE_DIR/${label}-stdout.txt" 2>/dev/null || echo "(empty)")
  echo "  Stdout: $out"
  echo ""
  sleep 3
}

# --- Scenarios ---

run_scenario "git-diff" \
  "$CLAUDE_BIN" --print -p 'Run git diff and summarize what changed. Use the Bash tool.' \
  --output-format json --allowedTools 'Bash(git diff:*)'

run_scenario "docker-ps" \
  "$CLAUDE_BIN" --print -p 'Run docker ps to list running containers. Use the Bash tool.' \
  --output-format json --allowedTools 'Bash(docker*:*)'

run_scenario "file-write" \
  "$CLAUDE_BIN" --print -p 'Create output.txt containing "traced", read it back, confirm contents.' \
  --output-format json --allowedTools 'Write,Read'

run_scenario "git-log" \
  "$CLAUDE_BIN" --print -p 'Run git log --oneline -5 and git status, summarize repo state.' \
  --output-format json --allowedTools 'Bash(git*:*)'

# --- Stop log capture ---
sleep 5
kill $LOG_PID 2>/dev/null || true
wait $LOG_PID 2>/dev/null || true
trap - EXIT

# --- Parse and summarize denials ---
echo "=== Parsing trace results ==="
echo ""

if [[ ! -s "$RAW_LOG" ]]; then
  echo "NO DENIALS RECORDED — profile may be overly permissive or log stream failed."
  echo "This means the current profile allows everything Claude needed."
else
  grep -a "deny" "$RAW_LOG" | \
    sed -n 's/.*Sandbox: [^)]*) \(deny([0-9]*) [^ ]* [^ ]*\).*/\1/p' | \
    sort -u > "$LOG_FILE"

  {
    echo "=== Denial Summary ==="
    echo ""
    echo "Total raw denial lines: $(grep -c "deny" "$RAW_LOG" 2>/dev/null || echo 0)"
    echo "Unique operations denied: $(wc -l < "$LOG_FILE" 2>/dev/null || echo 0)"
    echo ""

    for category in "file-" "network" "mach\|ipc" "process"; do
      label="${category%%\\*}"
      echo "--- ${label} operations ---"
      grep "$category" "$LOG_FILE" 2>/dev/null || echo "  (none)"
      echo ""
    done

    echo "--- Other ---"
    grep -vE "file-|network|mach|ipc|process" "$LOG_FILE" 2>/dev/null || echo "  (none)"
  } | tee "$SUMMARY_FILE"
fi

echo ""
echo "=== Trace complete ==="
echo ""
echo "Results in: $TRACE_DIR/"
echo "  raw-log.txt          — full kernel log output"
echo "  sandbox-denials.log  — unique denied operations"
echo "  summary.txt          — categorized summary"
echo "  *-stdout.txt         — claude output per scenario"
echo "  *-stderr.txt         — claude stderr per scenario"
echo ""
echo "Next: analyze denials to understand each source and adjust profiles."
