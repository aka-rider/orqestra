package orchestrator

// EventReportHarvested marks that a report-producing step obtained its
// deliverable, naming exactly which tier supplied it (WP11/RC3): a tier-3
// (or later) scavenge is fine — a SILENT one is not. Emitted whenever
// ReportHarvester.Harvest succeeds for a RoleReporter-class agent
// (researcher/architect/critic/revise) or a RoleExecutor-class one (worker);
// never emitted on a Harvest failure (nothing was harvested).
//
// This event lives in its own file rather than event.go — a parallel wave
// is concurrently reworking event.go/observer plumbing (WP9/WP10); a new
// file here is additive and keeps that wave's diff small instead of
// colliding on the same lines.
type EventReportHarvested struct {
	AgentID    AgentID
	Provenance ReportProvenance
}

func (EventReportHarvested) runEvent() {}
