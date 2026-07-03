package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// errReportArrived is the cancellation cause used when a SubmitReport signal
// stops the agent early. It is internal — callers see nil (clean stop).
var errReportArrived = errors.New("supervisor: report arrived")

// ReportSignaler is satisfied by *mcp.QuestionBridge.
// Defined here so the orchestrator package does not need to import mcp beyond
// what engine_types.go already has.
//
// WP12/RC3,J34-reports: correlation is by per-invocation nonce, not session
// ID. There is no re-arming step — every invocation gets its own nonce, so a
// fresh call can never collide with a still-pending earlier one, even for the
// same agentID (the old session-based correlation could and did collide —
// see agent_supervisor_test.go / bridge_test.go's WP12 coverage).
type ReportSignaler interface {
	// ReportSignal arms the report-correlation slot for one invocation,
	// identified by (agentID, nonce), and returns a channel closed when a
	// report arrives for it. Call BEFORE starting the subprocess. Returns a
	// pre-closed channel if the report has already arrived. Callers must not
	// close the returned channel.
	ReportSignal(agentID, nonce string) <-chan struct{}
}

// invocationSeq generates the per-invocation nonce counter component. A
// process-wide atomic counter combined with the PID (nextInvocationID) is
// sufficient uniqueness: the bridge these nonces correlate against is itself
// scoped to one orqestra process (WP4b/J5,J41 made QuestionBridge.Run a
// once-per-process lifetime), so cross-process collision is not a concern.
var invocationSeq atomic.Uint64

// nextInvocationID returns a fresh, process-unique invocation nonce
// (WP12/J34-reports). AgentSupervisor.Run calls this once per Run call and
// injects the result into the "orqestra" inline MCP server's
// --invocation-id arg (see withInvocationNonce) — the bridge then keys
// SubmitReport correlation by this value instead of session ID or agentID
// alone, so two invocations of the same agentID (sequential retries, or a
// role whose model never emits a session_id) can never cross-contaminate.
func nextInvocationID() string {
	return fmt.Sprintf("inv-%d-%d", os.Getpid(), invocationSeq.Add(1))
}

// withInvocationNonce returns a COPY of spec with --invocation-id nonce
// appended to the "orqestra" inline MCP server's args, if present. It never
// mutates spec.Inline or any InlineMCP.Args slice in place: spec.Inline is
// shared (by slice-header value) across every invocation built from the same
// ProcessSpecs.Architect/.Critic/.Worker value (e.g. every deliberation round
// reuses DeliberateStep.ArchSpec) — an in-place append that happened to reuse
// backing-array capacity would silently corrupt a sibling invocation's args
// (a concurrent retry, or the next round's copy taken before this one
// returns). Allocating fresh slices here means the caller's original spec is
// never touched, satisfying "two identical specs run identical processes"
// (ProcessSpec's own value-type contract, exec.go) for every OTHER call site
// that still holds the pre-nonce spec.
func withInvocationNonce(spec harness.ProcessSpec, nonce string) harness.ProcessSpec {
	if nonce == "" || len(spec.Inline) == 0 {
		return spec
	}
	inline := make([]harness.InlineMCP, len(spec.Inline))
	copy(inline, spec.Inline)
	for i, m := range inline {
		if m.Name != "orqestra" {
			continue
		}
		args := make([]string, len(m.Args), len(m.Args)+2)
		copy(args, m.Args)
		m.Args = append(args, "--invocation-id", nonce)
		inline[i] = m
	}
	spec.Inline = inline
	return spec
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

	// Per-invocation nonce (WP12/J34-reports): generated once per Run call,
	// injected into the "orqestra" inline MCP server's args (a fresh spec
	// value — see withInvocationNonce), and armed on the bridge BEFORE the
	// subprocess starts. Every invocation gets a unique nonce, so report
	// correlation never depends on session ID or agentID reuse.
	nonce := nextInvocationID()
	spec = withInvocationNonce(spec, nonce)

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

	// expectsReport mirrors the report-machinery guard below: a bridge-less
	// ExpectsReport spec should not enter the input plane (spec.Prompt would
	// be seeded into msgs and cleared, changing the subprocess invocation
	// mode) or arm a signal nothing will ever deliver.
	expectsReport := spec.ExpectsReport && s.reports != nil && spec.AgentID != ""

	// reportSigCh arms the BRIDGE's correlation slot BEFORE the subprocess
	// starts (WP12/J34-reports) — no re-arming, no session correlation on the
	// bridge side: the nonce above is unique per invocation, so this call can
	// never collide with any other invocation's slot, past or concurrent,
	// even for the same agentID.
	//
	// The supervise loop's OWN reaction to it is deliberately deferred: the
	// local reportSig variable used in the select below starts nil (a nil
	// channel never fires in select) and is only assigned reportSigCh once
	// this invocation's EventSessionStart has been observed (see the events
	// case). This preserves a DETERMINISTIC guarantee — capturedSID is always
	// set before a report-arrival stop can occur — that a bare race between
	// reportSigCh and the events channel could not offer: EventSessionStart
	// is always the very first thing a real Claude stream emits (long before
	// SubmitReport could ever fire in practice), but without this gate a
	// pathologically fast/pre-fired signal could win the select before the
	// executor goroutine had even been scheduled to emit it. Gating is purely
	// local bookkeeping — it changes nothing about when or how the BRIDGE is
	// armed or correlates reports.
	var reportSigCh <-chan struct{}
	if expectsReport {
		reportSigCh = s.reports.ReportSignal(spec.AgentID, nonce)
	}
	var reportSig <-chan struct{}

	// capturedSID holds the session ID observed from EventSessionStart. When the
	// run stops early (report arrival) the base executor's result event may never
	// arrive, leaving res.SessionID empty; we patch it from capturedSID before
	// returning so the session id is propagated to every caller (resume, artifacts,
	// TUI log viewer) regardless of how the run ended.
	var capturedSID string

	// Decide whether to open an input plane. WP13/J6: this is now a ROLE-CLASS
	// property (spec.InputPlane, set once by the spec builder per role) plus
	// the pre-existing in!=nil/expectsReport terms — it NO LONGER depends on
	// policy presence (len(policies) > 0). Before WP13, configuring a
	// SilenceGuard/LoopGuard/PreTimeoutNudge on an otherwise one-shot spec
	// silently flipped it into interactive stream-json mode — action at a
	// distance. Pure passthrough (none of these true) preserves the
	// single-shot -p behaviour.
	needsInputPlane := spec.InputPlane || in != nil || expectsReport

	var msgs chan harness.Message
	var baseIn <-chan harness.Message
	var events chan harness.Event
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
		runSink = &fanoutSink{inner: sink, events: events}
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

	// msgsClosed guards the graceful-stdin-close mechanism below (WP13.0
	// spike CONFIRMED: closing the input channel closes the subprocess's
	// stdin, which ends a `--input-format stream-json` process cleanly after
	// its current turn — see the spike evidence quoted in the WP13 report).
	// Once true, applyResult must never attempt msgs <- again (a send on a
	// closed channel panics).
	msgsClosed := false

	// reportSignaled reports whether a report has already arrived for this
	// invocation, without consuming/blocking — receiving from a channel that
	// may be nil (not yet bound, see reportSig above) or already closed is
	// always safe via a non-blocking select.
	reportSignaled := func() bool {
		select {
		case <-reportSig:
			return true
		default:
			return false
		}
	}

	applyResult := func(r policyResult) {
		if r.stop {
			cause := r.err
			if cause == nil {
				cause = ErrLoopEscalated // safety net; every escalating policy sets err
			}
			cancelCause(cause)
			return
		}
		if r.text != "" && !msgsClosed {
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

		case <-reportSig:
			// Report arrived: cancel with the report sentinel so resolveErr
			// can swallow context.Canceled and return nil to the caller.
			cancelCause(errReportArrived)
			// ctx.Done fires on the next iteration.

		case ev := <-events:
			lastEvent = time.Now()
			// First-wins session ID capture (matches stream_event.go:220's
			// first-wins parse and J34): a subagent spawned mid-run may emit
			// its own EventSessionStart later — capturedSID must stay pinned
			// to the FIRST one, this invocation's own session. Captured
			// directly here instead of via a dedicated sessionC channel
			// (WP12) — the events channel already delivers every event in
			// order, so no separate plumbing is needed. Binding reportSig
			// here (not at arm time) is what makes capturedSID-before-
			// report-stop deterministic — see the reportSigCh comment above.
			if ev.Kind == harness.EventSessionStart && ev.SessionID != "" && capturedSID == "" {
				capturedSID = ev.SessionID
				reportSig = reportSigCh
			}

			// WP13/J6, WP13.0 spike CONFIRMED: EventSessionDone marks the end
			// of one conversation turn (the "result" stream event) — NOT
			// necessarily the end of the whole invocation, since a nudge
			// policy may still send another message into msgs, starting a
			// new turn that ends in its own later EventSessionDone. Close the
			// input plane (ending the subprocess's stdin, which the spike
			// confirmed lets it exit cleanly on its own) ONLY when there is
			// nothing left to wait for: either this spec never expected a
			// report, or one has already arrived. Otherwise leave msgs open
			// so silence/pre-timeout/drift nudges still have a chance to
			// prompt a forgotten SubmitReport — the owner's "drift nudges are
			// intentional and stay" constraint (report §0) takes priority
			// over an early close here. early-stop-on-report (the case above)
			// remains the primary, faster path when a report DOES arrive
			// mid-turn — this is belt-and-braces for the natural-end-with-no-
			// report case, avoiding a full wall-clock timeout + SIGKILL for
			// a process that is simply sitting idle with nothing more to do.
			// Guarded to in == nil: an upstream in still forwards through a
			// dedicated goroutine (see below) that would race a direct close.
			if ev.Kind == harness.EventSessionDone && !msgsClosed && in == nil {
				if !expectsReport || reportSignaled() {
					close(msgs)
					msgsClosed = true
					// Hand off entirely to the natural-exit path (the case
					// oc := <-runDone branch below): if a report had already
					// arrived (reportSignaled() above), reportSig would stay
					// permanently ready and could otherwise win a LATER
					// select iteration too, triggering a redundant
					// cancelCause(errReportArrived) that would race the
					// subprocess's own graceful exit with a ctx-cancellation
					// SIGKILL instead of just letting it finish (which the
					// spike showed happens promptly once stdin is closed).
					reportSig = nil
				}
			}

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

// fanoutSink forwards every event to both the real sink and the events
// channel. Observe is called from the harness sink goroutine — the events
// channel send is non-blocking to avoid stalling the sink.
//
// WP12/RC3: EventSessionStart capture used to require a dedicated sessionC
// channel here, feeding the supervise loop's lazy report-signal bind. Report
// correlation is now nonce-based and armed before the subprocess even starts
// (see AgentSupervisor.Run), so fanoutSink no longer needs to single out
// EventSessionStart at all — the supervise loop reads it directly off events.
type fanoutSink struct {
	inner  harness.Sink
	events chan<- harness.Event
}

func (f *fanoutSink) Observe(ev harness.Event) {
	if f.inner != nil {
		f.inner.Observe(ev)
	}
	select {
	case f.events <- ev:
	default: // supervisor events channel is lossy — drop if full
	}
}
