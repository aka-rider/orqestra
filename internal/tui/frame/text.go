package frame

import "strings"

// textFrame is the shared mechanism for every plain, word-wrapped transcript
// item: prose, a user steer line, an end-of-agent summary, an answered
// question. They differ only in how their source text is styled/prefixed, so
// they are distinct constructors over one type rather than repeated types. Each
// constructor owns its style by reading the frame palette (see styles.go).
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

// NewProse is a completed assistant prose line. A prose line carries no trailing
// newline — that is row separation, not content.
func NewProse(text string) StaticFrame {
	return textFrame{spans: []Span{{Text: strings.TrimRight(text, "\n\r"), Style: theme.Prose}}}
}

// NewSteer is a user action echoed into the transcript (post/approve/comment),
// shown as "you: …".
func NewSteer(text string) StaticFrame {
	return textFrame{spans: []Span{{Text: "you: " + text, Style: theme.Steer}}}
}

// NewSummary is an end-of-agent meta line (e.g. "Done: ✓ architect …").
func NewSummary(text string) StaticFrame {
	return textFrame{spans: []Span{{Text: text, Style: theme.Summary}}}
}

// NewAnswer is a user's answer to a model question, echoed into the transcript.
func NewAnswer(question, answer string) StaticFrame {
	return textFrame{spans: []Span{
		{Text: question + " ", Style: theme.Question},
		{Text: answer, Style: theme.Answer},
	}}
}
