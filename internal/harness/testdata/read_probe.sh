#!/bin/bash
# Fixture for the consolidated read-grant-boundary test (sandbox_test.go).
#
# Ignores its own argv — harness.Run always invokes its subprocess with
# claude-CLI-shaped flags regardless of spec.Binary (buildSpecArgs,
# spec_args.go) — and communicates via env vars, the same convention
# hold_stdout.sh/quick_exit.sh/workspace_write_probe.sh established.
#
# Reads $ORQESTRA_TEST_READ_ALLOWED and $ORQESTRA_TEST_READ_DENIED (each
# optional) and records what it could read from each into a plain-text
# verdict file at $ORQESTRA_TEST_VERDICT (required) — a location inside the
# sandbox's writable grant, so the driving test can assert via the
# filesystem instead of parsing subprocess stdout.
set -u

if [ -z "${ORQESTRA_TEST_VERDICT:-}" ]; then
  echo "read_probe.sh: ORQESTRA_TEST_VERDICT not set" >&2
  exit 1
fi

allowed="UNSET"
if [ -n "${ORQESTRA_TEST_READ_ALLOWED:-}" ]; then
  allowed="$(cat "$ORQESTRA_TEST_READ_ALLOWED" 2>/dev/null || echo DENIED)"
fi

denied="UNSET"
if [ -n "${ORQESTRA_TEST_READ_DENIED:-}" ]; then
  denied="$(cat "$ORQESTRA_TEST_READ_DENIED" 2>/dev/null || echo DENIED)"
fi

{
  printf 'allowed=%s\n' "$allowed"
  printf 'denied=%s\n' "$denied"
} > "$ORQESTRA_TEST_VERDICT"

echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"read-probe"}'
