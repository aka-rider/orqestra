package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// RoleClass selects which tier order ReportHarvester.Harvest applies. Tier
// order is a property of the CALLING STEP, not something derivable from
// harness.ProcessSpec alone: worker and critic both set ExpectsReport=true
// and leave PlanMode false, yet need entirely different tier orders (RC3).
//
// RoleClass is a type alias of config.RoleClass (WP14/RC4) — report-harvest
// tier selection and harness.SpecForRole's spec-building defaults share one
// role-classification vocabulary. config.RoleClassUtility (the integrator's
// one-shot commit-message/conflict-resolution invocations) never reaches
// Harvest: those steps read Output/INTEGRATOR-GIVE-UP directly (see
// step_integrate.go), so only two of the three classes have a RoleXxx alias
// here.
type RoleClass = config.RoleClass

const (
	// RoleReporter is researcher/architect/critic and the gate-loop revise
	// turn: SubmitReport → plan file (only when spec.PlanMode, and only if
	// it changed THIS invocation — J35) → final message (conversation
	// probe, sanity-checked).
	RoleReporter = config.RoleClassReporter
	// RoleExecutor is the worker: SubmitReport → raw subprocess output. Raw
	// output is not a structured report and is never sanity-checked — there
	// is nowhere left to fall back to, and today's behavior already accepts
	// whatever the worker produced.
	RoleExecutor = config.RoleClassExecutor
)

// Report source names — used both as ReportProvenance.Source and as the
// per-tier name recorded in ReportProvenance.Rejected.
const (
	SourceSubmitReport = "submit_report"
	SourcePlanFile     = "plan_file"
	SourceFinalMessage = "final_message"
	SourceRawOutput    = "raw_output"
)

// freshnessUnverified is the ReportProvenance.Detail value recorded when
// tier 2 (plan file) was accepted WITHOUT being able to prove the file
// changed during this invocation — e.g. the very first architect pass, or a
// revision whose prior plan-file path was never captured. Truthful, not
// silently wrong (J35).
const freshnessUnverified = "freshness-unverified"

// ReportProvenance records exactly which tier supplied a harvested report,
// and — when a lower tier had to be tried first — which tiers produced text
// that failed the sanity check on the way there. A tier-3 (or later)
// scavenge is fine; a SILENT one is not (RC3).
type ReportProvenance struct {
	Tier     int
	Source   string
	Detail   string   // path/session/agent identifying detail, or freshnessUnverified (J35)
	Rejected []string // Source names of tiers that produced text but failed looksLikeReport
	// Errored records tiers whose retrieval itself ERRORED (e.g. the plan
	// file couldn't be read, the session JSONL couldn't be resolved) as
	// opposed to merely failing the sanity check (Rejected) — A5: a tier
	// that errors leaves no trace today unless recorded here. Each entry is
	// "<tier source>: <err>".
	Errored []string
}

// planFileSnapshot is a cheap point-in-time fingerprint of a plan file —
// mtime+size, not a content hash — sufficient to answer "did this change"
// without reading (and re-security-checking) the file twice per invocation.
type planFileSnapshot struct {
	path    string
	modTime time.Time
	size    int64
}

func snapshotPlanFile(sessionID, planFilePath, repoPath string) (planFileSnapshot, error) {
	resolved, err := agent.ResolvePlanFilePath(sessionID, planFilePath, repoPath)
	if err != nil {
		return planFileSnapshot{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return planFileSnapshot{}, fmt.Errorf("stat plan file %q: %w", resolved, err)
	}
	return planFileSnapshot{path: resolved, modTime: info.ModTime(), size: info.Size()}, nil
}

// ReportHarvester is the ONE post-run report harvester for every pipeline
// role (RC3/WP11). It replaces three near-identical implementations —
// step.go's former extractReport, ExecuteStep's inline TakeReport-or-raw-
// output, and ReviseStep's inline TakeReport→preferReport→chat-fallback
// chain — with one component that additionally records WHICH tier supplied
// the report (ReportProvenance) and fixes J35: a revision's plan-file tier
// must reflect what THIS invocation actually wrote, not stale content left
// over from before it ran.
type ReportHarvester struct {
	sc    StepContext
	class RoleClass

	havePreSnapshot bool
	preSnapshot     planFileSnapshot
}

// NewReportHarvester constructs a harvester for one agent invocation. class
// selects the tier order (see RoleClass); sc carries the report store,
// repo path, and logger every tier needs.
func NewReportHarvester(sc StepContext, class RoleClass) *ReportHarvester {
	return &ReportHarvester{sc: sc, class: class}
}

// SnapshotPlanFile captures the plan file's pre-invocation state (J35),
// using the PRIOR known session ID and plan-file path — the session about
// to be resumed and (if known) the path it last wrote to — NEVER anything
// from the invocation about to run (that path is only known afterwards, via
// the returned harness.RunResult). Call this before sc.Exec.Run.
//
// sessionID == "" means no prior session exists (the very first plan write
// for this run): there is nothing to compare against, so Harvest's tier 2
// treats the plan file as accepted-but-unverified rather than silently
// pretending freshness was proven. The same fallback applies when the prior
// plan file cannot be resolved or stat'd (e.g. this is the first turn that
// will ever write one) — that is normal, not a failure, so it is dropped
// with a fire-and-forget rather than surfaced.
func (h *ReportHarvester) SnapshotPlanFile(sessionID, planFilePath, repoPath string) {
	h.havePreSnapshot = false
	if sessionID == "" {
		return
	}
	snap, err := snapshotPlanFile(sessionID, planFilePath, repoPath)
	if err != nil {
		return // fire-and-forget: no readable prior plan file is normal (first-ever write); Harvest falls back to freshness-unverified acceptance
	}
	h.preSnapshot = snap
	h.havePreSnapshot = true
}

// planFileChanged reports whether the plan file changed since the
// SnapshotPlanFile call for this invocation. unverified is true when no
// comparison could be made at all (no prior snapshot, or the post-run state
// could not be resolved) — Harvest still tries tier 2 in that case (today's
// pre-J35 behavior) but must label it honestly rather than claim freshness
// it never proved.
func (h *ReportHarvester) planFileChanged(res harness.RunResult) (changed, unverified bool) {
	if !h.havePreSnapshot {
		return true, true
	}
	post, err := snapshotPlanFile(res.SessionID, res.PlanFilePath, h.sc.RepoPath)
	if err != nil {
		return true, true
	}
	if post.path != h.preSnapshot.path {
		return true, false // a different file entirely is plainly a fresh write
	}
	return !post.modTime.Equal(h.preSnapshot.modTime) || post.size != h.preSnapshot.size, false
}

// Harvest returns the report/deliverable for one agent invocation and the
// provenance of where it came from. spec.AgentID identifies the role for
// error messages and SubmitReport correlation; res/runErr are the just-
// completed invocation's outcome (runErr may be nil).
func (h *ReportHarvester) Harvest(_ context.Context, spec harness.ProcessSpec, res harness.RunResult, runErr error) (string, ReportProvenance, error) {
	if h.class == RoleExecutor {
		report, prov := h.harvestExecutor(spec, res)
		return report, prov, nil
	}
	return h.harvestReporter(spec, res, runErr)
}

// harvestExecutor implements the worker's tier order: SubmitReport → raw
// subprocess output. No sanity check on raw output — it is arbitrary work
// output, not a structured report, and rejecting it has nowhere left to
// fall back to (matches ExecuteStep's pre-WP11 behavior exactly).
func (h *ReportHarvester) harvestExecutor(spec harness.ProcessSpec, res harness.RunResult) (string, ReportProvenance) {
	agentID := spec.AgentID
	if h.sc.Reports != nil {
		if sub, ok := h.sc.Reports.TakeReport(agentID); ok && sub != "" {
			return sub, ReportProvenance{Tier: 1, Source: SourceSubmitReport, Detail: res.SessionID}
		}
	}
	return res.Output, ReportProvenance{Tier: 2, Source: SourceRawOutput, Detail: res.SessionID}
}

// harvestReporter implements the researcher/architect/critic/revise tier
// order: SubmitReport → plan file (PlanMode only, changed-this-invocation
// only — J35) → final message (conversation probe, sanity-checked) → fail
// closed.
func (h *ReportHarvester) harvestReporter(spec harness.ProcessSpec, res harness.RunResult, runErr error) (string, ReportProvenance, error) {
	agentID := spec.AgentID
	var rejected []string
	var errored []string

	// Tier 1: SubmitReport submission. Keyed by agentID — the bridge
	// resolves the session internally, so this works even when
	// res.SessionID is empty after an early (report-arrival) stop.
	if h.sc.Reports != nil {
		if sub, ok := h.sc.Reports.TakeReport(agentID); ok && sub != "" {
			if looksLikeReport(sub) {
				return sub, ReportProvenance{Tier: 1, Source: SourceSubmitReport, Detail: agentID}, nil
			}
			rejected = append(rejected, SourceSubmitReport)
			h.sc.Log.Warn("submitted report failed sanity check, trying next tier", "agent", agentID)
		}
	}

	// Tier 2: Plan file (architect plan-mode only), fired only when it
	// changed during THIS invocation (J35) — otherwise it is silently the
	// stale pre-revision content and tier 3 must decide instead.
	if spec.PlanMode {
		changed, unverified := h.planFileChanged(res)
		if changed {
			if plan, err := preferReport(h.sc, agentID, res); err == nil {
				if looksLikeReport(plan) {
					detail := res.PlanFilePath
					if unverified {
						detail = freshnessUnverified
					}
					return plan, ReportProvenance{Tier: 2, Source: SourcePlanFile, Detail: detail, Rejected: rejected, Errored: errored}, nil
				}
				rejected = append(rejected, SourcePlanFile)
				h.sc.Log.Warn("plan file failed sanity check, trying next tier", "agent", agentID)
			} else {
				errored = append(errored, fmt.Sprintf("%s: %v", SourcePlanFile, err))
				h.sc.Log.Warn("plan file tier errored, trying next tier", "agent", agentID, "tier", SourcePlanFile, "err", err)
			}
		} else {
			h.sc.Log.Debug("report harvest: plan file unchanged this invocation, tier 2 skipped (J35)", "agent", agentID)
		}
	}

	// Tier 3: Conversation probe.
	if msg, err := finalMessage(h.sc, res); err == nil {
		if looksLikeReport(msg) {
			return msg, ReportProvenance{Tier: 3, Source: SourceFinalMessage, Detail: res.SessionID, Rejected: rejected, Errored: errored}, nil
		}
		rejected = append(rejected, SourceFinalMessage)
		h.sc.Log.Warn("conversation probe failed sanity check", "agent", agentID)
	} else {
		errored = append(errored, fmt.Sprintf("%s: %v", SourceFinalMessage, err))
		h.sc.Log.Warn("conversation probe tier errored", "agent", agentID, "tier", SourceFinalMessage, "err", err)
	}

	// Tier 4: Fail closed. The terminal error names both tiers that produced
	// text but failed the sanity check (rejected) and tiers whose retrieval
	// itself errored (errored) — A5: neither class of failure disappears
	// silently.
	if runErr != nil {
		return "", ReportProvenance{Rejected: rejected, Errored: errored}, fmt.Errorf("%s failed: %w (rejected tiers: %v, errored tiers: %v)", agentID, runErr, rejected, errored)
	}
	return "", ReportProvenance{Rejected: rejected, Errored: errored}, fmt.Errorf("%s: no valid report produced (rejected tiers: %v, errored tiers: %v)", agentID, rejected, errored)
}

// preferReport returns the plan written by the architect to its plan file.
// Uses ReadPlanFile (stream path → JSONL attachment), never the dir-scan
// fallback. Returns an error when no plan file exists for this session.
func preferReport(sc StepContext, agentID string, res harness.RunResult) (string, error) {
	content, err := agent.ReadPlanFile(res.SessionID, res.PlanFilePath, sc.RepoPath)
	if err != nil {
		return "", fmt.Errorf("read plan file for %s: %w", agentID, err)
	}
	return content, nil
}

// finalMessage returns an agent's final assistant text from the run result
// output or the session JSONL. Used as the conversation-probe tier in
// ReportHarvester.harvestReporter.
func finalMessage(sc StepContext, res harness.RunResult) (string, error) {
	if out := strings.TrimSpace(res.Output); out != "" {
		return out, nil
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("no session output and no session ID")
	}
	jsonl, err := harness.ResolveSessionLogPath(sc.RepoPath, res.SessionID)
	if err != nil {
		return "", fmt.Errorf("resolve session log for %s: %w", res.SessionID, err)
	}
	out, err := harness.ExtractFinalOutput(jsonl)
	if err != nil {
		return "", fmt.Errorf("extract final message for %s: %w", res.SessionID, err)
	}
	if out = strings.TrimSpace(out); out == "" {
		return "", fmt.Errorf("session %s produced no final message", res.SessionID)
	}
	return out, nil
}
