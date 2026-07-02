package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// errReportArrived is the cancellation cause used when a SubmitReport signal
// stops the agent early. It is internal — callers see nil (clean stop).
var errReportArrived = errors.New("supervisor: report arrived")

// ReportSignaler is satisfied by *mcp.QuestionBridge.
// Defined here so the orchestrator package does not need to import mcp beyond
// what engine_types.go already has.
type ReportSignaler interface {
	// RegisterSession re-arms the correlation slot for agentID with the given
	// sessionID. Call on EventSessionStart, before calling ReportSignal.
	RegisterSession(agentID, sessionID string)
	// ReportSignal returns a channel closed when a report arrives for agentID.
	// The bridge resolves the correlation key from the agent's registered session
	// internally, so the supervisor never threads a session ID through this path.
	// Returns a pre-closed channel if the report has already arrived. Callers must
	// not close the channel.
	ReportSignal(agentID string) <-chan struct{}
}

// AgentSupervisor is the single owner of the agent lifecycle.
// It replaces the 7-deep middleware chain with one struct that:
//   - Enforces the token budget
//   - Applies a per-run wall-clock timeout
//   - Watches for a SubmitReport signal to stop the run early
//   - Runs pure policies (loop, silence, pre-timeout, drift) via one event+tick loop
//
// AgentSupervisor implements harness.Executor and is a drop-in for sc.Exec.
//
// Cancellation discipline: the only stop signal is context.Context.
// stop() is the only intentional caller of cancelCause; the deferred cancelCause(nil)
// is a ctx-leak safety net and is idempotent after a prior cancelCause call.
type AgentSupervisor struct {
	base    harness.Executor
	reports ReportSignaler
	guard   *BudgetGuard
}

// NewAgentSupervisor constructs a supervisor.
// reports may be nil (disables report-arrival stop).
// guard must be non-nil.
func NewAgentSupervisor(base harness.Executor, reports ReportSignaler, guard *BudgetGuard) *AgentSupervisor {
	return &AgentSupervisor{base: base, reports: reports, guard: guard}
}

// Run implements harness.Executor. All cancellation paths — timeout, parent cancel,
// loop escalation, and report arrival — converge on one cancelCause call;
// context.Cause distinguishes them without secondary state.
func (s *AgentSupervisor) Run(
	parent context.Context,
	spec harness.ProcessSpec,
	in <-chan harness.Message,
	sink harness.Sink,
) (harness.RunResult, error) {
	if err := s.guard.Check(); err != nil {
		return harness.RunResult{}, err
	}

	var ctx context.Context
	var cancelCause context.CancelCauseFunc
	if spec.Timeout > 0 {
		var timeoutCtx context.Context
		var timeoutCancel context.CancelFunc
		timeoutCtx, timeoutCancel = context.WithTimeout(parent, spec.Timeout)
		defer timeoutCancel()
		ctx, cancelCause = context.WithCancelCause(timeoutCtx)
	} else {
		ctx, cancelCause = context.WithCancelCause(parent)
	}
	defer cancelCause(nil)

	policies := buildPolicies(spec)

	// reportSig starts nil; it is lazily bound in the supervise loop when
	// EventSessionStart delivers a session ID via sessionC. A nil channel never
	// fires in select, so the case is a no-op until bound.
	var reportSig <-chan struct{}

	// capturedSID holds the session ID observed from EventSessionStart. When the
	// run stops early (report arrival) the base executor's result event may never
	// arrive, leaving res.SessionID empty; we patch it from capturedSID before
	// returning so the session id is propagated to every caller (resume, artifacts,
	// TUI log viewer) regardless of how the run ended.
	var capturedSID string

	// Decide whether to open an input plane.
	// Pure passthrough: no policies, no caller-supplied in, and no report machinery —
	// preserves the single-shot -p behaviour of the original merge path.
	// The report condition mirrors the sessionC creation guard below; a bridge-less
	// ExpectsReport spec should not enter the input plane (spec.Prompt would be
	// seeded into msgs and cleared, changing the subprocess invocation mode).
	needsInputPlane := len(policies) > 0 || in != nil || (spec.ExpectsReport && s.reports != nil && spec.AgentID != "")

	var msgs chan harness.Message
	var baseIn <-chan harness.Message
	var events chan harness.Event
	var sessionC chan string
	var runSink harness.Sink

	if needsInputPlane {
		msgs = make(chan harness.Message, 16)
		// Seed the initial prompt into the input plane and clear it from the spec
		// so buildSpecArgs passes an empty -p (input-plane mode).
		if spec.Prompt != "" {
			msgs <- harness.Message{Text: spec.Prompt}
			spec.Prompt = ""
		}
		// Forward upstream in to msgs via a goroutine so base always reads from one channel.
		if in != nil {
			go func() {
				for {
					select {
					case msg, ok := <-in:
						if !ok {
							return
						}
						select {
						case msgs <- msg:
						case <-ctx.Done():
							return
						}
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		baseIn = msgs

		events = make(chan harness.Event, 64)
		if spec.ExpectsReport && s.reports != nil && spec.AgentID != "" {
			// 1-slot buffer: EventSessionStart fires once; duplicates dropped.
			sessionC = make(chan string, 1)
		}
		runSink = &fanoutSink{inner: sink, events: events, sessionC: sessionC}
	} else {
		baseIn = in
		runSink = sink
	}

	// Launch the base executor.
	type outcome struct {
		res harness.RunResult
		err error
	}
	runDone := make(chan outcome, 1)
	go func() {
		r, e := s.base.Run(ctx, spec, baseIn, runSink)
		runDone <- outcome{r, e}
	}()

	if !needsInputPlane {
		// Pure passthrough: wait for the base to finish, apply budget post-check.
		oc := <-runDone
		s.recordUsage(spec.AgentID, oc.res)
		return s.budgetPostCheck(oc.res, oc.err)
	}

	// Supervise loop — one select, one cancellation owner (cancelCause).
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	deadline, hasDeadline := ctx.Deadline()
	lastEvent := time.Now()

	applyResult := func(r policyResult) {
		if r.stop {
			cause := r.err
			if cause == nil {
				cause = ErrLoopEscalated // safety net; every escalating policy sets err
			}
			cancelCause(cause)
			return
		}
		if r.text != "" {
			select {
			case msgs <- harness.Message{Text: r.text}:
			default: // buffer full — drop nudge
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Timeout, parent cancel, or our own cancelCause — join the base.
			oc := <-runDone
			if oc.res.SessionID == "" {
				oc.res.SessionID = capturedSID
			}
			s.recordUsage(spec.AgentID, oc.res)
			return s.resolveErr(ctx, oc.res, oc.err)

		case sid := <-sessionC:
			// Session ID arrived non-lossily. Remember it, re-arm the bridge slot,
			// and bind reportSig. After this, the case below becomes live.
			capturedSID = sid
			s.reports.RegisterSession(spec.AgentID, sid)
			reportSig = s.reports.ReportSignal(spec.AgentID)

		case <-reportSig:
			// Report arrived: cancel with the report sentinel so resolveErr
			// can swallow context.Canceled and return nil to the caller.
			cancelCause(errReportArrived)
			// ctx.Done fires on the next iteration.

		case ev := <-events:
			lastEvent = time.Now()
			for _, p := range policies {
				applyResult(p.observe(ev))
			}

		case <-ticker.C:
			now := time.Now()
			var dl time.Time
			if hasDeadline {
				dl = deadline
			}
			for _, p := range policies {
				applyResult(p.tick(now, lastEvent, dl))
			}

		case oc := <-runDone:
			if oc.res.SessionID == "" {
				oc.res.SessionID = capturedSID
			}
			s.recordUsage(spec.AgentID, oc.res)
			if ctx.Err() != nil {
				// A prior cancelCause call beat us here — resolve via cause.
				return s.resolveErr(ctx, oc.res, oc.err)
			}
			// Natural exit — apply budget post-check.
			return s.budgetPostCheck(oc.res, oc.err)
		}
	}
}

// resolveErr translates the cancellation cause into the error a caller sees.
func (s *AgentSupervisor) resolveErr(ctx context.Context, res harness.RunResult, err error) (harness.RunResult, error) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errReportArrived):
		// Report-driven stop: swallow context.Canceled from the base executor.
		if errors.Is(err, context.Canceled) {
			return res, nil
		}
		return res, err
	case cause != nil && cause != ctx.Err():
		// Custom policy cause (e.g. ErrLoopEscalated).
		return res, cause
	default:
		// Parent cancel or deadline — propagate ctx.Err().
		return res, ctx.Err()
	}
}

func (s *AgentSupervisor) recordUsage(agentID string, res harness.RunResult) {
	if res.Usage.Input > 0 || res.Usage.Output > 0 {
		s.guard.usage.Record(agentID, res.Usage.Input, res.Usage.Output)
	}
}

func (s *AgentSupervisor) budgetPostCheck(res harness.RunResult, err error) (harness.RunResult, error) {
	if err != nil {
		return res, err
	}
	if checkErr := s.guard.Check(); checkErr != nil {
		return res, checkErr
	}
	return res, nil
}

// buildPolicies assembles the active policies from the ProcessSpec.
func buildPolicies(spec harness.ProcessSpec) []supervisorPolicy {
	var ps []supervisorPolicy

	if spec.LoopGuard != (harness.LoopGuardSpec{}) {
		ps = append(ps, newLoopPolicy(spec.LoopGuard))
	}

	if spec.SilenceGuard.SilenceSecs > 0 {
		nudgeText := spec.SilenceGuard.NudgeText
		if nudgeText == "" {
			nudgeText = spec.PreTimeoutNudge
		}
		maxNudges := spec.SilenceGuard.MaxNudges
		if maxNudges <= 0 {
			maxNudges = 3
		}
		ps = append(ps, &silencePolicy{
			silenceDur: time.Duration(spec.SilenceGuard.SilenceSecs) * time.Second,
			nudgeText:  nudgeText,
			maxNudges:  maxNudges,
		})
	}

	if spec.PreTimeoutNudge != "" {
		ps = append(ps, &preTimeoutPolicy{nudgeText: spec.PreTimeoutNudge})
	}

	if spec.ExpectsReport && spec.PreTimeoutNudge != "" {
		ps = append(ps, &driftPolicy{nudgeText: spec.PreTimeoutNudge})
	}

	return ps
}

// fanoutSink forwards every event to both the real sink and the events channel.
// Observe is called from the harness sink goroutine — the events channel send
// is non-blocking to avoid stalling the sink.
//
// sessionC is a dedicated 1-slot channel for EventSessionStart delivery.
// It is non-lossy within a single session (only the first session_id is sent;
// duplicates are silently dropped by the default: branch).
type fanoutSink struct {
	inner    harness.Sink
	events   chan<- harness.Event
	sessionC chan<- string
}

func (f *fanoutSink) Observe(ev harness.Event) {
	if f.inner != nil {
		f.inner.Observe(ev)
	}
	if ev.Kind == harness.EventSessionStart && ev.SessionID != "" && f.sessionC != nil {
		select {
		case f.sessionC <- ev.SessionID:
		default: // first session_id already delivered; duplicates dropped
		}
	}
	select {
	case f.events <- ev:
	default: // supervisor events channel is lossy — drop if full
	}
}
