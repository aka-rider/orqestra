package harness

import (
	"strings"
	"testing"
)

// TestMergeInlineMCP_CorruptExistingConfig_Errors is WP18's A6 QA gate for
// spec_args.go: a corrupt EXISTING --mcp-config arg (e.g. injected via
// ExtraArgs by some other role-building path) must not be silently replaced
// with an empty server set — replacing it would drop whatever the existing
// arg configured, including a real bridge if one had been serialized there.
//
// RED-first: against the pre-A6 mergeInlineMCP (`if err := json.Unmarshal(...);
// err != nil { existing = mcpConfig{...} }`, no error return), this failed
// because mergeInlineMCP had no error to check at all (single return value) —
// the corrupt config was silently discarded and replaced.
func TestMergeInlineMCP_CorruptExistingConfig_Errors(t *testing.T) {
	args := []string{"--mcp-config", "{not valid json"}
	inline := []InlineMCP{{Name: "orqestra", Command: "/usr/bin/true", Args: []string{"mcp-bridge"}}}

	_, err := mergeInlineMCP(args, inline)
	if err == nil {
		t.Fatal("expected an error for a corrupt existing --mcp-config arg, got nil")
	}
}

// TestBuildSpecArgs_PropagatesMergeInlineMCPError proves buildSpecArgs (and
// therefore harness.Run, which calls it before starting any subprocess)
// surfaces a mergeInlineMCP failure as an error rather than silently
// continuing with the args as they were before the merge — which would mean
// the "orqestra" bridge (question/report channel) silently never gets
// attached to the subprocess invocation.
func TestBuildSpecArgs_PropagatesMergeInlineMCPError(t *testing.T) {
	spec := ProcessSpec{
		Prompt:    "hello",
		ExtraArgs: []string{"--mcp-config", "{not valid json"},
		Inline:    []InlineMCP{{Name: "orqestra", Command: "/usr/bin/true"}},
	}
	_, err := buildSpecArgs(spec, false)
	if err == nil {
		t.Fatal("expected buildSpecArgs to propagate the mergeInlineMCP error, got nil")
	}
	if !strings.Contains(err.Error(), "merge inline mcp") {
		t.Errorf("error %q does not name the failing stage", err.Error())
	}
}
