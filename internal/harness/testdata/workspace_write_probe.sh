#!/bin/bash
# Fixture for the ported INV-ROLE-WORKER test (role_worker_test.go) and the
# consolidated read-only-repo+writable-worktree test (sandbox_test.go),
# driven through harness.Run end to end (harness.Run -> startSandboxed ->
# leash.Execute), rather than sandbox.New+sb.Wrap called directly as the
# pre-migration test did.
#
# Ignores its own argv — harness.Run always invokes its subprocess with
# claude-CLI-shaped flags (-p/--output-format/--verbose/...) regardless of
# spec.Binary (buildSpecArgs, spec_args.go), so this fixture communicates via
# env vars instead, the same convention exec_cancel_test.go already
# established for hold_stdout.sh/quick_exit.sh.
#
# $ORQESTRA_TEST_INSIDE (required): path to write — expected to succeed
#   (inside the sandbox's writable grant).
# $ORQESTRA_TEST_OUTSIDE (required): path to write — expected to be DENIED by
#   the sandbox. Failure here is the point of the test, not a script bug: no
#   `set -e`, and the failing write's exit status is deliberately ignored so
#   this script still reaches its own exit 0 and terminal result line.
set -u

if [ -z "${ORQESTRA_TEST_INSIDE:-}" ] || [ -z "${ORQESTRA_TEST_OUTSIDE:-}" ]; then
  echo "workspace_write_probe.sh: ORQESTRA_TEST_INSIDE/ORQESTRA_TEST_OUTSIDE not set" >&2
  exit 1
fi

echo 'package main' > "$ORQESTRA_TEST_INSIDE"
echo BREACH > "$ORQESTRA_TEST_OUTSIDE" 2>/dev/null

echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"workspace-write-probe"}'
