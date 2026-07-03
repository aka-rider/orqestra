package rundir

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// metaSuffix is the file-naming convention every step-meta artifact shares
// (e.g. architect_meta.json, worker_meta.json, critic_meta_round1.json).
// LoadStepMetas discovers metas by this suffix alone — it does not require
// callers to enumerate exact filenames, so historical runs whose step names
// evolved (rounds, revisions) still load.
const metaSuffix = "_meta.json"

// StepMeta is the per-agent metadata persisted as JSON in the session
// directory. This is the schema's canonical definition (orchestrator holds a
// type alias — see orchestrator/step_meta.go) because rundir owns the
// artifact schema; orchestrator owns how/when a step decides to write one.
type StepMeta struct {
	AgentID              string    `json:"agent_id"`
	ModelRef             string    `json:"model_ref,omitempty"`
	ModelDisplay         string    `json:"model_display,omitempty"`
	Provider             string    `json:"provider,omitempty"`
	ContextWindow        int64     `json:"context_window,omitempty"`
	StartTime            time.Time `json:"start_time"`
	EndTime              time.Time `json:"end_time"`
	ClaudeSessionID      string    `json:"claude_session_id,omitempty"`
	ClaudeSessionLogPath string    `json:"claude_session_log_path,omitempty"`
	ClaudePlanFilePath   string    `json:"claude_plan_file_path,omitempty"`
	Status               string    `json:"status"` // "done", "failed", or "fallback" (J12: chat-only architect revision that produced no plan rewrite)
	Error                string    `json:"error,omitempty"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`

	// Report harvest provenance (WP11/RC3): populated by report-producing
	// steps (architect, critic, worker, gate-loop revisions) via
	// orchestrator.ReportHarvester so a scavenged report (tier 2/3) is never
	// silently indistinguishable from a SubmitReport delivery (tier 1).
	ReportTier     int      `json:"report_tier,omitempty"`
	ReportSource   string   `json:"report_source,omitempty"`   // "submit_report" | "plan_file" | "final_message" | "raw_output"
	ReportDetail   string   `json:"report_detail,omitempty"`   // path/session/agent detail, or "freshness-unverified" (J35)
	ReportRejected []string `json:"report_rejected,omitempty"` // tier Source names that produced text but failed the sanity check
	ReportErrored  []string `json:"report_errored,omitempty"`  // "<tier>: <err>" entries for tiers whose retrieval itself errored (A5)
}

// SaveStepMeta persists m as "<role>_meta.json" under the run directory.
// role identifies the step/round (e.g. "architect", "critic_round1",
// "worker") — the caller picks it so multiple rounds of the same agent don't
// collide. This is a diagnostic artifact: callers that want fail-closed
// integrity should check the returned error themselves (SaveStepMeta does not
// decide that policy — see ArtifactSink.Write vs WriteBestEffort).
func (d Dir) SaveStepMeta(role string, m StepMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("rundir: marshal step meta for %q: %w", role, err)
	}
	return d.SaveArtifact(role+metaSuffix, data)
}

// LoadStepMetas reads every "*_meta.json" file in the run directory and
// returns the parsed StepMeta values, sorted deterministically by start time
// then agent ID then filename (§1.7: never persist/render an unordered scan).
// A file that fails to parse as JSON at all is skipped rather than failing
// the whole load — one corrupt meta file must not hide every other step's
// history. Note: a differently-shaped meta file that still parses as valid
// JSON (e.g. integrator_meta.json, which has no agent_id/start_time fields)
// is included with those fields at their zero value — this is pre-existing
// behavior carried over from the old glob-based scan, not a WP15 change.
func (d Dir) LoadStepMetas() ([]StepMeta, error) {
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rundir: read dir %q: %w", d.Path, err)
	}

	type named struct {
		meta StepMeta
		file string
	}
	var found []named
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), metaSuffix) {
			continue
		}
		data, readErr := os.ReadFile(d.path(e.Name()))
		if readErr != nil {
			continue // fire-and-forget: best-effort historical scan, one unreadable meta must not hide the rest
		}
		var m StepMeta
		if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
			continue // fire-and-forget: a corrupt *_meta.json is skipped, not fatal to the whole scan
		}
		found = append(found, named{meta: m, file: e.Name()})
	}

	sort.Slice(found, func(i, j int) bool {
		if !found[i].meta.StartTime.Equal(found[j].meta.StartTime) {
			return found[i].meta.StartTime.Before(found[j].meta.StartTime)
		}
		if found[i].meta.AgentID != found[j].meta.AgentID {
			return found[i].meta.AgentID < found[j].meta.AgentID
		}
		return found[i].file < found[j].file
	})

	metas := make([]StepMeta, len(found))
	for i, n := range found {
		metas[i] = n.meta
	}
	return metas, nil
}
