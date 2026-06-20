// Command replayclaude is a deterministic stand-in for the `claude` binary used
// by QA gates. It is NOT a fake of any Orqestra type — it is a recording
// *player*: it writes a committed, verbatim recording of real `claude`
// stream-json output to stdout and exits. Drive the real production runner
// (sandboxedRunner/ClaudeCLI) against it via the `binary` config knob to get a
// hermetic, deterministic exercise of the actual code path — no live API, no
// hand-written behavior.
//
// The runner controls argv (it appends claude's flags), so all arguments are
// ignored. The recording is read from, in order:
//   1. $ORQESTRA_REPLAY_FILE (absolute path), if set
//   2. ./.orqestra-replay.ndjson relative to the working directory
//
// The working directory is the sandbox workspace (Wrap sets cmd.Dir), which is
// read-allowed under seatbelt, so option 2 needs no env passthrough.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const defaultFixtureName = ".orqestra-replay.ndjson"

func main() {
	// Drain stdin so the parent's NDJSON writes never block or EPIPE.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()

	path := os.Getenv("ORQESTRA_REPLAY_FILE")
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "replayclaude: getwd:", err)
			os.Exit(1)
		}
		path = filepath.Join(cwd, defaultFixtureName)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replayclaude: read recording:", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, "replayclaude: write stdout:", err)
		os.Exit(1)
	}
}
