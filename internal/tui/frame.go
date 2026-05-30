package tui

import "time"

// FrameKind classifies frames in the scrollable frame list.
type FrameKind int

const (
	AgentFrame      FrameKind = iota // Agent execution (Researcher, Architect, Worker, etc.)
	PlanFrame                        // Plan gate with markdown + conversation
	CompletionFrame                  // Final summary after pipeline finishes
	ErrorFrame                       // Pipeline-level error
)

// FrameState tracks the lifecycle of a single frame.
type FrameState int

const (
	FrameInit       FrameState = iota // Announced but no content yet
	FrameInProgress                   // Actively streaming content
	FrameFinished                     // Agent done, elapsed/tokens final
)

// ToolBlock represents a single tool invocation rendered as an inner sub-block.
type ToolBlock struct {
	Icon   string
	Name   string
	Detail string
}

// ContentPart is one segment of a frame's interleaved content.
// Consecutive text lines are coalesced into a single part until a tool interrupts.
type ContentPart struct {
	IsText bool
	Text   string    // when IsText: one or more completed lines joined by \n
	Tool   ToolBlock // when !IsText
}

// Frame holds all state for a single rendered frame in the scrollable list.
type Frame struct {
	Kind         FrameKind
	State        FrameState
	AgentID      string
	AgentModel   string
	Elapsed      time.Duration
	StartedAt    time.Time
	InputTokens  int64
	OutputTokens int64
	Collapsed    bool          // when true, renders as a compact summary block; only valid on FrameFinished
	Parts        []ContentPart // interleaved text + tools in insertion order
	Partial      string        // current incomplete line not yet flushed to Parts
}

// AppendText coalesces text into the frame's content parts.
// If the last part is text, it appends to it; otherwise creates a new text part.
func (f *Frame) AppendText(line string) {
	if len(f.Parts) > 0 && f.Parts[len(f.Parts)-1].IsText {
		f.Parts[len(f.Parts)-1].Text += line + "\n"
	} else {
		f.Parts = append(f.Parts, ContentPart{IsText: true, Text: line + "\n"})
	}
}

// AppendTool adds a tool block, flushing any partial text first.
func (f *Frame) AppendTool(tb ToolBlock) {
	if f.Partial != "" {
		f.AppendText(f.Partial)
		f.Partial = ""
	}
	f.Parts = append(f.Parts, ContentPart{IsText: false, Tool: tb})
}

// FlushPartial promotes the partial line to a completed text line.
func (f *Frame) FlushPartial() {
	if f.Partial != "" {
		f.AppendText(f.Partial)
		f.Partial = ""
	}
}
