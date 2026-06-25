package orchestrator

// reportKey returns the bridge correlation key for an agent invocation.
// When sessionID is non-empty, it is unique per Claude process and used as-is.
// When sessionID is empty (local/non-Claude models that emit no session_id in
// the stream), agentID is used as a fallback — same key across runs for the
// same role, but safe because RegisterSession re-arms the slot on each new run.
func reportKey(agentID, sessionID string) string {
	if sessionID != "" {
		return sessionID
	}
	return agentID
}
