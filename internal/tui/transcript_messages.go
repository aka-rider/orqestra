package tui

// transcriptAutoscrollMsg drives continuous scroll while the user holds a
// mouse button beyond the transcript edge. The seq field is a generation
// token: stale ticks (seq != Transcript.dragSeq) are no-ops, preventing
// runaway loops after release.
type transcriptAutoscrollMsg struct{ seq uint64 }
