package frame

import "charm.land/lipgloss/v2"

// textFrame is the shared mechanism for every plain, word-wrapped transcript
// item: prose, a user steer line, an end-of-agent summary, an answered
// question. They differ only in how their source text is styled/prefixed, so
// they are distinct constructors over one type rather than repeated types.
type textFrame struct {
	spans []Span
	width int
	rows  []Row
}

func (t textFrame) SetWidth(w int) StaticFrame {
	t.width = w
	t.rows = wrapCells(cellsFromSpans(t.spans), w)
	return t
}

func (t textFrame) Rows() []Row { return t.rows }

// NewProse is a completed assistant prose line, rendered in the default style.
func NewProse(text string) StaticFrame {
	return textFrame{spans: []Span{{Text: text}}}
}

// NewSteer is a user action echoed into the transcript (post/approve/comment),
// shown as "you: …" in the given style.
func NewSteer(text string, style lipgloss.Style) StaticFrame {
	return textFrame{spans: []Span{{Text: "you: " + text, Style: style}}}
}

// NewSummary is an end-of-agent meta line (e.g. "Done: ✓ architect …").
func NewSummary(text string, style lipgloss.Style) StaticFrame {
	return textFrame{spans: []Span{{Text: text, Style: style}}}
}

// NewAnswer is a user's answer to a model question, echoed into the transcript.
func NewAnswer(question, answer string, qStyle, aStyle lipgloss.Style) StaticFrame {
	return textFrame{spans: []Span{
		{Text: question + " ", Style: qStyle},
		{Text: answer, Style: aStyle},
	}}
}
