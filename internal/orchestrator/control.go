package orchestrator

import (
	"context"
	"fmt"

	"github.com/xiii/orqestra/internal/harness"
)

// Control is the two-way interaction plane.
//
//   (a) Gate — between-step human decision. The pipeline blocks HERE only,
//       always with ctx.Done() as the escape hatch. Cannot deadlock: the
//       decision channel is buffered-1 with the gate as sole reader; UI
//       Submit is non-blocking; ctx cancel always unwinds.
//
//   (b) Input — live Post handle. A step that wants interactivity registers
//       its send-end with Control keyed by AgentID. A TUI keystroke calls
//       Input("researcher") and sends on the returned channel. Run's stdin
//       writer goroutine drains the receive end. Posting is independent of
//       output and termination.
type Control interface {
	// Gate publishes req to the observer, then blocks until the user decides
	// or ctx is done. Returns ctx.Err() when cancelled.
	Gate(ctx context.Context, req GateRequest) (Decision, error)

	// Input returns the send-end of the input channel for the given agent.
	// Returns nil if no channel is registered for that agent ID.
	Input(id AgentID) chan<- harness.Message

	// RegisterInput registers the send-end of an agent's stdin channel.
	// The step calls this before passing the receive-end to Exec.Run.
	RegisterInput(id AgentID, ch chan<- harness.Message)

	// Submit delivers a user decision to a waiting Gate call. Non-blocking.
	Submit(dec Decision)
}

// controlImpl implements Control backed by an ObsStore for gate publishing.
type controlImpl struct {
	obs       *ObsStore
	decisions chan Decision              // buffered-1; Gate reads, Submit writes
	inputs    map[AgentID]chan<- harness.Message
}

// NewControl creates a Control backed by the given ObsStore.
func NewControl(obs *ObsStore) Control {
	return &controlImpl{
		obs:       obs,
		decisions: make(chan Decision, 1),
		inputs:    make(map[AgentID]chan<- harness.Message),
	}
}

func (c *controlImpl) Gate(ctx context.Context, req GateRequest) (Decision, error) {
	c.obs.GateOpened(req)
	defer c.obs.GateClosed()

	select {
	case dec := <-c.decisions:
		return dec, nil
	case <-ctx.Done():
		return Decision{}, fmt.Errorf("gate: %w", ctx.Err())
	}
}

func (c *controlImpl) Input(id AgentID) chan<- harness.Message {
	ch, ok := c.inputs[id]
	if !ok {
		return nil
	}
	return ch
}

func (c *controlImpl) RegisterInput(id AgentID, ch chan<- harness.Message) {
	c.inputs[id] = ch
}

func (c *controlImpl) Submit(dec Decision) {
	select {
	case c.decisions <- dec:
	default:
	}
}
