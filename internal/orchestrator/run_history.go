package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/rundir"
)

const sessionTimeFmt = "2006-01-02-150405"

// RunSummary is a lightweight overview of a past pipeline run.
type RunSummary struct {
	Timestamp   time.Time
	Slug        string
	Path        string
	Prompt      string
	Status      string        // from last *_meta.json, or empty
	Duration    time.Duration // derived from first start → last end in step metas
	TotalTokens int64         // sum of input+output tokens across all steps
}

// RunDetail is the full data for a single historical run.
type RunDetail struct {
	RunSummary
	Steps        []StepMeta
	PlanMarkdown string
	WorkerOutput string
	Validation   string
}

// ArtifactRequirement describes a required artifact and which agent produced it.
type ArtifactRequirement struct {
	Name    string
	AgentID string
}

// RunCompleteness describes whether a historical run is complete or what is missing.
type RunCompleteness struct {
	Complete         bool
	MissingAgents    []string
	FailedAgents     []string
	MissingArtifacts []ArtifactRequirement
	RestartPhase     string // "deliberation"|"execution"|"validation" or ""
	Reason           string
}

// ListRuns scans .orqestra/sessions/ under repoPath and returns summaries sorted newest-first.
func ListRuns(repoPath string) ([]RunSummary, error) {
	sessionsDir := filepath.Join(repoPath, ".orqestra", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	var runs []RunSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dirPath := filepath.Join(sessionsDir, name)

		ts, slug := parseSessionDirName(name)
		if ts.IsZero() {
			continue
		}

		dir := rundir.Dir{Path: dirPath}
		// fire-and-forget: advisory historical display — a genuine read
		// failure on one run's prompt or step metas must not hide every other
		// run from the list; it degrades to an empty/zero summary for this
		// entry only.
		prompt, _ := dir.LoadPrompt()
		metas, _ := dir.LoadStepMetas()
		status, duration, totalTokens := deriveSummary(metas)

		runs = append(runs, RunSummary{
			Timestamp:   ts,
			Slug:        slug,
			Path:        dirPath,
			Prompt:      prompt,
			Status:      status,
			Duration:    duration,
			TotalTokens: totalTokens,
		})
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Timestamp.After(runs[j].Timestamp)
	})
	return runs, nil
}

// LoadRunDetail loads all data for a single run. All artifact reads go
// through rundir's typed accessors (WP15/J11) — this is the ONE reader of the
// ONE schema every writer (steps, via ArtifactSink) persists through.
func LoadRunDetail(runPath string) (RunDetail, error) {
	name := filepath.Base(runPath)
	ts, slug := parseSessionDirName(name)

	dir := rundir.Dir{Path: runPath}
	// fire-and-forget: advisory historical display — absence is expected for
	// older/partial runs; a genuine read failure degrades to "" rather than
	// failing the whole detail view over one optional artifact.
	prompt, _ := dir.LoadPrompt()
	planMarkdown, _ := dir.LoadFinalPlan()
	workerOutput, _ := dir.LoadWorkerOutput()
	validation, _ := dir.LoadValidation()

	steps, err := dir.LoadStepMetas()
	if err != nil {
		// A genuine directory-read failure (not "no step metas found" — that
		// returns nil, nil) — surface it truthfully rather than silently
		// claiming a complete-but-empty step history (§0).
		return RunDetail{}, fmt.Errorf("load step metas for %s: %w", runPath, err)
	}
	status, duration, totalTokens := deriveSummary(steps)

	return RunDetail{
		RunSummary: RunSummary{
			Timestamp:   ts,
			Slug:        slug,
			Path:        runPath,
			Prompt:      prompt,
			Status:      status,
			Duration:    duration,
			TotalTokens: totalTokens,
		},
		Steps:        steps,
		PlanMarkdown: planMarkdown,
		WorkerOutput: workerOutput,
		Validation:   validation,
	}, nil
}

// deriveSummary derives the run's overall status, wall-clock duration, and
// total token usage from its step metas: status is the Status of whichever
// step ended latest; duration spans the earliest start to the latest end.
func deriveSummary(metas []StepMeta) (status string, duration time.Duration, totalTokens int64) {
	if len(metas) == 0 {
		return "", 0, 0
	}

	var earliest, latest time.Time
	for _, m := range metas {
		totalTokens += m.InputTokens + m.OutputTokens
		if earliest.IsZero() || m.StartTime.Before(earliest) {
			earliest = m.StartTime
		}
		if m.EndTime.After(latest) {
			latest = m.EndTime
			status = m.Status
		}
	}
	if !earliest.IsZero() && !latest.IsZero() {
		duration = latest.Sub(earliest)
	}
	return status, duration, totalTokens
}

// AnalyzeRunCompleteness inspects a session directory and returns a summary of
// what is missing or failed.
//
// Tier-B dead code (critic-verified, docs/bug-journal-2026-07-02.md): no
// current writer produces run_config.json, so this always reports "no
// run_config.json (old format run)" for every run. Left compiling and
// unchanged — it is owned by a later package/WP, not WP15 (J11).
func AnalyzeRunCompleteness(runPath string) RunCompleteness {
	var c RunCompleteness

	var intended runPhases
	configPath := filepath.Join(runPath, "run_config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		c.Reason = "no run_config.json (old format run)"
		return c
	}
	if err := json.Unmarshal(configData, &intended); err != nil {
		c.Reason = "invalid run_config.json"
		return c
	}

	// Deliberation is the first stage and always runs (the architect researches on
	// demand via subagent; there is no standalone research phase to check).
	if !dirHasPlans(runPath, "deliberation") {
		c.Complete = false
		c.RestartPhase = "deliberation"
		c.Reason = "deliberation phase incomplete (no deliberation/plan-v*.md)"
		return c
	}

	if intended.Execution {
		if !fileExists(filepath.Join(runPath, "execution", "output.txt")) {
			c.Complete = false
			c.RestartPhase = "execution"
			c.Reason = "execution phase incomplete (no execution/output.txt)"
			return c
		}
	}

	if intended.Validation {
		if !fileExists(filepath.Join(runPath, "validation", "validation.txt")) {
			c.Complete = false
			c.RestartPhase = "validation"
			c.Reason = "validation phase incomplete (no validation/validation.txt)"
			return c
		}
	}

	c.Complete = true
	return c
}

// runPhases is a local struct for decoding run_config.json. A legacy "research"
// key in older run_config.json files is harmlessly ignored (unknown field).
type runPhases struct {
	Execution  bool `json:"execution"`
	Validation bool `json:"validation"`
}

func parseSessionDirName(name string) (time.Time, string) {
	if len(name) < len(sessionTimeFmt) {
		return time.Time{}, ""
	}
	ts, err := time.Parse(sessionTimeFmt, name[:len(sessionTimeFmt)])
	if err != nil {
		return time.Time{}, ""
	}
	slug := ""
	rest := name[len(sessionTimeFmt):]
	if strings.HasPrefix(rest, "-") {
		slug = rest[1:]
	}
	return ts, slug
}

func dirHasPlans(runPath, phase string) bool {
	dir := filepath.Join(runPath, phase)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "plan-v") && strings.HasSuffix(name, ".md") {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
