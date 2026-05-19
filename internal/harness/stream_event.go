package harness

// StreamUpdate is a typed event emitted during Claude CLI streaming.
// Fields are mutually exclusive in normal operation: Text, Tool, or UsageValid.
type StreamUpdate struct {
	Text       string
	Tool       string
	Detail     string
	Input      int64
	Output     int64
	UsageValid bool
}

const (
	initialScanBufferBytes = 64 << 10
	maxJSONLLineBytes      = 2 << 20
)
