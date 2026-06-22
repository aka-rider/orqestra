package harness

// AgentDef defines an inline Claude subagent passed via the `claude --agents <json>`
// flag. It mirrors the CLI's agent-definition schema: the JSON shape is
// {"<name>": {"description","prompt","tools","disallowedTools","model","permissionMode"}}.
//
// Model must be a CLI-valid value (sonnet|opus|haiku|fable, a full model id, or
// "inherit") — NEVER an orqestra alias like "medium"/"large", which the CLI has
// never seen. Leave Model empty to inherit the parent's (env-routed) model; that is
// the only provider-agnostic choice, since orqestra routes models via env vars, not
// the --model flag.
type AgentDef struct {
	Description     string   `json:"description"`
	Prompt          string   `json:"prompt"`
	Tools           []string `json:"tools,omitempty"`
	DisallowedTools []string `json:"disallowedTools,omitempty"`
	Model           string   `json:"model,omitempty"`
	PermissionMode  string   `json:"permissionMode,omitempty"`
}

// InlineAgent pairs a subagent name with its definition for ProcessSpec.Agents.
// Name is the key in the --agents JSON object; empty-named entries are skipped.
type InlineAgent struct {
	Name string
	Def  AgentDef
}
