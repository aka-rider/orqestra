package harness

import (
	"encoding/json"
	"fmt"
)

// SpecArgs returns the full subprocess argument list buildSpecArgs would
// produce for spec, treating spec.InputPlane as the input-plane flag — the
// same proxy AgentSupervisor.Run's needsInputPlane derivation uses for
// reporter/executor-class specs. Exported for golden/regression tests that
// need to inspect the final CLI invocation without spawning a process (see
// cmd/orqestra's WP14 golden spec test); production code always calls
// buildSpecArgs directly from Run with the ACTUAL hasInputPlane in use for
// that invocation (which can differ from spec.InputPlane in the narrow
// windows AgentSupervisor documents — e.g. an upstream in already open).
//
// A6/§1.6: buildSpecArgs can now fail (an inline MCP merge — the set that
// carries the question/report bridge — cannot be silently dropped). Callers
// that only care about the happy path (golden/regression tests building a
// known-good spec) use SpecArgs and fail the test on error; production code
// goes through harness.Run, which returns the wrapped error instead of
// starting a subprocess with a silently-incomplete arg list.
func SpecArgs(spec ProcessSpec) ([]string, error) {
	return buildSpecArgs(spec, spec.InputPlane)
}

// buildSpecArgs consolidates all CLI arg assembly into one builder.
// Ordering: -p -> output-format -> input-format (input plane only) ->
// --verbose/--include-partial-messages (stream-json only) ->
// --append-system-prompt -> --resume -> ExtraArgs -> inline MCP merge ->
// inline agents merge.
func buildSpecArgs(spec ProcessSpec, hasInputPlane bool) ([]string, error) {
	var args []string

	// -p prompt (empty string when input plane handles it via NDJSON stdin)
	prompt := spec.Prompt
	if hasInputPlane {
		prompt = ""
	}
	args = append(args, "-p", prompt)

	// Output format and associated flags
	switch spec.Output {
	case OutputJSON:
		args = append(args, "--output-format", "json", "--print")
	default: // OutputStreamJSON
		args = append(args, "--output-format", "stream-json")
		if hasInputPlane {
			args = append(args, "--input-format", "stream-json")
		}
		args = append(args, "--verbose", "--include-partial-messages")
	}

	// System prompt
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}

	// Session continuation
	if spec.Resume.Valid {
		args = append(args, "--resume", spec.Resume.ID)
	}

	// Caller-supplied extra args (allowedTools, disallowedTools, permission-mode, etc.)
	args = append(args, spec.ExtraArgs...)

	// Merge inline MCP servers into --mcp-config.
	if len(spec.Inline) > 0 {
		merged, err := mergeInlineMCP(args, spec.Inline)
		if err != nil {
			return nil, fmt.Errorf("build spec args: %w", err)
		}
		args = merged
	}

	// Serialize inline subagent definitions into --agents. Appended unconditionally
	// alongside ExtraArgs/Inline, so it survives --resume (the validator path).
	if len(spec.Agents) > 0 {
		args = appendAgentsArg(args, spec.Agents)
	}

	return args, nil
}

// appendAgentsArg serializes inline subagent definitions into a single
// --agents <json> flag of shape {"<name>": {AgentDef…}}. The names are map keys, so
// encoding/json emits them in sorted order — deterministic output (CLAUDE.md).
func appendAgentsArg(args []string, agents []InlineAgent) []string {
	defs := make(map[string]AgentDef, len(agents))
	for _, a := range agents {
		if a.Name == "" {
			continue
		}
		defs[a.Name] = a.Def
	}
	if len(defs) == 0 {
		return args
	}
	data, err := json.Marshal(defs)
	if err != nil {
		// fire-and-forget: defs holds only strings/[]string, so json.Marshal cannot fail
		return args
	}
	return append(args, "--agents", string(data))
}

// mergeInlineMCP merges named inline MCP server definitions into an existing
// --mcp-config arg or appends a new one.
//
// A6/§1.1/§1.6: every failure here used to be swallowed (a corrupt existing
// --mcp-config was silently replaced with an empty one; a marshal failure
// silently dropped the ENTIRE inline-MCP set — which includes the
// question/report bridge — falling back to the args as if nothing had been
// requested). inline is never empty when built from bridgeMCP/researcherAgent
// (both always set Name/Command), so a marshal failure here would mean a
// broken InlineMCP/AgentDef value slipped through some other caller — still
// worth failing closed on rather than silently running the agent with no
// bridge.
func mergeInlineMCP(args []string, inline []InlineMCP) ([]string, error) {
	type mcpConfig struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	var existing mcpConfig
	mcpIdx := -1
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			mcpIdx = i + 1
			if err := json.Unmarshal([]byte(args[mcpIdx]), &existing); err != nil {
				return nil, fmt.Errorf("merge inline mcp: parse existing --mcp-config: %w", err)
			}
			break
		}
	}
	if existing.MCPServers == nil {
		existing.MCPServers = make(map[string]json.RawMessage)
	}

	for _, srv := range inline {
		if srv.Name == "" {
			continue
		}
		def := struct {
			Command string   `json:"command"`
			Args    []string `json:"args,omitempty"`
		}{Command: srv.Command, Args: srv.Args}
		data, err := json.Marshal(def)
		if err != nil {
			return nil, fmt.Errorf("merge inline mcp: marshal server %q: %w", srv.Name, err)
		}
		existing.MCPServers[srv.Name] = data
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("merge inline mcp: marshal merged config: %w", err)
	}

	out := make([]string, len(args))
	copy(out, args)
	if mcpIdx >= 0 {
		out[mcpIdx] = string(merged)
	} else {
		out = append(out, "--mcp-config", string(merged))
	}
	return out, nil
}
