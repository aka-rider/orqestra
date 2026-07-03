package harness_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// TestRun_CorruptMCPConfigArg_Errors is WP18's A6 QA gate for harness.Run: a
// corrupt --mcp-config arg combined with inline MCP servers (which include
// the question/report bridge) must make Run return an error BEFORE ever
// starting a subprocess — never silently drop the inline set and proceed as
// if nothing had been configured.
//
// RED-first: against the pre-A6 exec.go (`args := buildSpecArgs(spec,
// hasInputPlane)`, no error return, and mergeInlineMCP's own silent-drop
// fallback), this test failed because Run swallowed the corrupt config and
// would have started "claude" (or whatever Binary names) with the bridge
// silently missing — undetectable from the caller's side.
func TestRun_CorruptMCPConfigArg_Errors(t *testing.T) {
	spec := harness.ProcessSpec{
		// Binary intentionally left as the zero value/nonexistent — buildSpecArgs
		// must fail before Run ever gets to exec.CommandContext/Start.
		Binary:    "this-binary-must-never-be-invoked",
		Prompt:    "hello",
		ExtraArgs: []string{"--mcp-config", "{not valid json"},
		Inline:    []harness.InlineMCP{{Name: "orqestra", Command: "/usr/bin/true"}},
	}

	_, err := harness.Run(context.Background(), spec, nil, nil)
	if err == nil {
		t.Fatal("expected harness.Run to error on a corrupt --mcp-config arg with inline servers present, got nil")
	}
	if !strings.Contains(err.Error(), "build spec args") {
		t.Errorf("error %q does not name the build-spec-args stage", err.Error())
	}
}
