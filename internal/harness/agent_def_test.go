package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

// findAgentsJSON returns the value following the --agents flag, or "" if absent.
func findAgentsJSON(args []string) string {
	for i, a := range args {
		if a == "--agents" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestWithInlineAgent_SerializedToAgentsFlag(t *testing.T) {
	spec := ProcessSpec{
		Model: ModelSpec{Provider: "anthropic", Model: "x"},
		Agents: []InlineAgent{{
			Name: "orqestra-researcher",
			Def: AgentDef{
				Description: "researcher",
				Prompt:      "do research",
				Tools:       []string{"Read", "Glob", "Grep", "Bash", "WebFetch", "WebSearch"},
			},
		}},
	}

	if len(spec.Agents) != 1 || spec.Agents[0].Name != "orqestra-researcher" {
		t.Fatalf("spec did not carry the inline agent: %+v", spec.Agents)
	}

	args, err := buildSpecArgs(spec, false)
	if err != nil {
		t.Fatalf("buildSpecArgs: %v", err)
	}
	raw := findAgentsJSON(args)
	if raw == "" {
		t.Fatal("--agents flag not emitted")
	}

	// Well-formed JSON of shape {"<name>": {AgentDef…}}.
	var defs map[string]AgentDef
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		t.Fatalf("--agents JSON is malformed: %v\n%s", err, raw)
	}
	def, ok := defs["orqestra-researcher"]
	if !ok {
		t.Fatalf("researcher missing from --agents JSON: %s", raw)
	}

	// The researcher holds the exploration tools and NO mcp tool (it cannot reach
	// the orqestra bridge — it returns its report as the final message).
	for _, tool := range def.Tools {
		if strings.Contains(tool, "mcp__") {
			t.Errorf("researcher tools must not include an mcp tool, got %q", tool)
		}
	}
	if !contains(def.Tools, "Grep") || !contains(def.Tools, "WebSearch") {
		t.Errorf("researcher should hold the exploration tools, got %v", def.Tools)
	}

	// Model omitted → empty (inherits parent); never an orqestra alias.
	if def.Model != "" {
		t.Errorf("researcher model should be omitted (inherit), got %q", def.Model)
	}
}

func TestAppendAgentsArg_DeterministicKeyOrder(t *testing.T) {
	// encoding/json sorts string map keys, so two agents serialize in a stable order
	// regardless of insertion order (CLAUDE.md determinism rule).
	agents := []InlineAgent{
		{Name: "zeta", Def: AgentDef{Description: "z"}},
		{Name: "alpha", Def: AgentDef{Description: "a"}},
	}
	args := appendAgentsArg(nil, agents)
	raw := findAgentsJSON(args)
	if raw == "" {
		t.Fatal("--agents not emitted")
	}
	if strings.Index(raw, `"alpha"`) > strings.Index(raw, `"zeta"`) {
		t.Errorf("agent keys not sorted: %s", raw)
	}
}

func TestAppendAgentsArg_EmptyNameSkipped(t *testing.T) {
	args := appendAgentsArg(nil, []InlineAgent{{Name: "", Def: AgentDef{Description: "x"}}})
	if findAgentsJSON(args) != "" {
		t.Errorf("empty-named agent should be skipped, got %v", args)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
