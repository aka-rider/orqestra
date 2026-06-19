package tui

// bottomMode is the sum type for the lower region of the pipeline alt-screen
// layout. It replaces the flat ContentMode flag set with mutually-exclusive
// value sub-models, each carrying only the state it actually needs.
//
// Key routing (key handling) still uses ContentMode during the transition
// period; only View() dispatch uses bottomMode. Full migration happens when
// ContentMode is retired.
type bottomMode interface {
	// viewBottom renders the region into at most bodyH rows at width w.
	viewBottom(w, bodyH int) string
	// footerHint returns the key-hint text for the pipeline input zone.
	footerHint() string
}

// streamingBottom is the bottomMode active while an agent is running.
// It wraps the streaming console (live tool lines + partial text) and provides
// a slot for a post-message textarea (not yet fully wired; activated later).
type streamingBottom struct {
	console streamingConsole
}

func (m streamingBottom) viewBottom(w, bodyH int) string {
	return m.console.RenderFixed(bodyH, w)
}

func (m streamingBottom) footerHint() string {
	return " [Ctrl+C] cancel"
}

// gateBottom is the bottomMode active when a human-gate decision is pending.
// It wraps the HumanChatMode component that drives the plan-review conversation.
type gateBottom struct {
	chat HumanChatMode
}

func (m gateBottom) viewBottom(w, bodyH int) string {
	if m.chat == nil {
		return ""
	}
	return m.chat.View(w)
}

func (m gateBottom) footerHint() string {
	if m.chat == nil {
		return ""
	}
	return m.chat.Footer()
}

// questionBottom is the bottomMode active when an MCP AskUserQuestion is pending.
type questionBottom struct {
	q userQuestionModel
}

func (m questionBottom) viewBottom(w, bodyH int) string {
	return m.q.View(w)
}

func (m questionBottom) footerHint() string {
	return m.q.InputZone()
}
