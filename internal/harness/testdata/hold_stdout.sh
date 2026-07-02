#!/bin/bash
# Fixture for TestRunCancelKillsProcessGroup (J32/J15).
#
# Stands in for a sandbox-exec leader whose claude/node grandchild survives a
# leader-only kill: this script (the process-group leader, since harness.Run
# sets Setpgid with no explicit Pgid, making its own PID == the group's PGID)
# backgrounds a grandchild that ignores SIGTERM and inherits stdout (the
# fd harness.Run reads from), then exits immediately — mirroring sandbox-exec
# dying while the real grandchild keeps the pipe open.
#
# $ORQESTRA_TEST_PIDFILE (required): path this script writes its own PID to,
# read by the test via os.Environ() passthrough (buildEnvFromSpec forwards the
# test process's env into the child). Because Setpgid creates a new group led
# by this PID, that PID doubles as the process-group ID the test verifies dead
# after cancel.
set -u

if [ -z "${ORQESTRA_TEST_PIDFILE:-}" ]; then
  echo "hold_stdout.sh: ORQESTRA_TEST_PIDFILE not set" >&2
  exit 1
fi

echo "$$" > "$ORQESTRA_TEST_PIDFILE"

# Grandchild: ignore SIGTERM (POSIX preserves an ignored disposition across
# exec), inherit this script's stdout (the pipe harness.Run is reading), sleep
# far longer than the test's bound. Same process group as this leader (no
# setsid/setpgid call here, so it inherits the leader's new group).
(
  trap '' TERM
  exec sleep 100
) &

# Leader exits immediately without waiting on the grandchild — the defect
# scenario: a leader-only kill (the pre-fix default cmd.Cancel) would succeed
# against this already-exiting process and leave the grandchild running.
exit 0
