package orchestrator

import (
	"log/slog"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
)

// guardPrompt runs the prompt integrity canary and returns the sanitized prompt.
// log is the per-run injected logger (StepContext.Log) — never slog.Default().
func guardPrompt(log *slog.Logger, assembled, original, agentID string) string {
	out, tripped := agent.CheckPromptIntegrity(assembled, original)
	if tripped {
		log.Warn("prompt integrity canary tripped", "agent", agentID)
	}
	return out
}

// resolveAgentMeta builds AgentMeta from the config for the given model ref.
// Returns a partially-populated AgentMeta (with ModelRef set) if the model is
// not found — never a silent fallback to a different model.
func resolveAgentMeta(cfg *config.Config, modelRef string) AgentMeta {
	meta := AgentMeta{ModelRef: modelRef}
	if cfg == nil || modelRef == "" {
		return meta
	}
	mc, ok := cfg.ModelMeta(modelRef)
	if !ok {
		return meta
	}
	meta.ModelDisplay = mc.Model
	meta.Provider = mc.Provider
	meta.ContextWindow = mc.ContextWindow
	return meta
}

// DefaultRunDirFactory returns a RunDirFactory that creates session directories
// under the given project root.
func DefaultRunDirFactory(repoPath string) RunDirFactory {
	return func(slug string) (agent.SessionDir, error) {
		return agent.NewSessionDir(repoPath, slug)
	}
}
