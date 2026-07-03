package orchestrator

import (
	"context"
	"log/slog"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

// Step is a typed pipeline transform: Run takes In, returns Out.
// Each step returns its result and error; no channel signals completion.
// ctx.Err() distinguishes cancel from timeout from error.
type Step[In, Out any] interface {
	ID() AgentID
	Run(ctx context.Context, in In, sc StepContext) (Out, error)
}

// ReportStore retrieves agent report submissions delivered via SubmitReport.
// TakeReport returns the report text and true if a submission exists, then
// removes it so it is consumed exactly once. It is keyed by agentID; the store
// resolves the agent's session internally, so report harvesting never depends on
// a separately-captured RunResult.SessionID that may be empty after an early stop.
type ReportStore interface {
	TakeReport(agentID string) (string, bool)
}

// StepContext carries cross-cutting capabilities by value.
// ctx is never stored — it arrives as an argument to each Step.Run call.
type StepContext struct {
	Exec      harness.Executor // P1: returns a value, never blocks the pipeline
	Obs       Observer         // P4: one-way, lossy observation
	Artifacts ArtifactSink     // P7: fail-closed at integrity boundaries
	Control   Control          // P5: gate request/response + live Post handle
	Sessions  rundir.Dir       // session artifact directory
	Log       *slog.Logger
	RepoPath  string      // absolute path to the repository root
	Reports   ReportStore // tier-1 report channel; nil when no bridge is configured
}

// runReportAgent runs spec via sc.Exec (with retries on harness error) and
// then harvests the report via harvester — a *ReportHarvester constructed
// with RoleReporter (see report_harvest.go). It is the DRY entry point for
// researcher, architect, and critic — roles that always produce a
// structured report.
//
// The caller must call sc.Obs.AgentStarted before invoking; runReportAgent
// re-calls it only on retries (attempt > 1). If harvester needs a
// pre-invocation plan-file snapshot (J35), the caller must call
// harvester.SnapshotPlanFile before invoking this function.
func runReportAgent(
	ctx context.Context,
	sc StepContext,
	harvester *ReportHarvester,
	spec harness.ProcessSpec,
	meta AgentMeta,
	attempts int,
) (harness.RunResult, string, ReportProvenance, error) {
	if attempts < 1 {
		attempts = 1
	}
	var res harness.RunResult
	var runErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		sink := SinkFromObserver(AgentID(spec.AgentID), sc.Obs)
		res, runErr = sc.Exec.Run(ctx, spec, nil, sink)
		if runErr == nil || attempt >= attempts {
			break
		}
		sc.Log.Warn("agent attempt failed, retrying",
			"agent", spec.AgentID, "attempt", attempt, "err", runErr)
		sc.Obs.AgentStarted(AgentID(spec.AgentID), meta)
	}

	report, prov, err := harvester.Harvest(ctx, spec, res, runErr)
	return res, report, prov, err
}

// SinkFromObserver returns a harness.Sink that forwards all events to Observer.Stream.
// Used by step implementations to bridge harness.Executor output to the observer bus.
func SinkFromObserver(id AgentID, obs Observer) harness.Sink {
	return &observerSink{id: id, obs: obs}
}

// observerSink implements harness.Sink by forwarding to Observer.Stream.
// Observe must not block — Observer.Stream is non-blocking by contract.
type observerSink struct {
	id  AgentID
	obs Observer
}

func (s *observerSink) Observe(ev harness.Event) { s.obs.Stream(s.id, ev) }
