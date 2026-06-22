package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

		prompt := readStringArtifact(dirPath, "prompt.md")
		status, duration, totalTokens := lastStepStatus(dirPath)

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

// LoadRunDetail loads all data for a single run.
func LoadRunDetail(runPath string) (RunDetail, error) {
	name := filepath.Base(runPath)
	ts, slug := parseSessionDirName(name)

	prompt := readStringArtifact(runPath, "prompt.md")
	status, duration, totalTokens := lastStepStatus(runPath)

	detail := RunDetail{
		RunSummary: RunSummary{
			Timestamp:   ts,
			Slug:        slug,
			Path:        runPath,
			Prompt:      prompt,
			Status:      status,
			Duration:    duration,
			TotalTokens: totalTokens,
		},
		PlanMarkdown: readStringArtifact(runPath, "final_plan.md"),
		WorkerOutput: readStringArtifact(runPath, "worker_output.txt"),
		Validation:   readStringArtifact(runPath, "worker_validation.txt"),
	}

	matches, _ := filepath.Glob(filepath.Join(runPath, "*_meta.json")) // fire-and-forget: Glob errors only on malformed patterns, not fs issues
	for _, match := range matches {
		data, err := os.ReadFile(match)
		if err != nil {
			continue
		}
		var meta StepMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		detail.Steps = append(detail.Steps, meta)
	}
	sort.Slice(detail.Steps, func(i, j int) bool {
		return detail.Steps[i].StartTime.Before(detail.Steps[j].StartTime)
	})

	return detail, nil
}

// AnalyzeRunCompleteness inspects a session directory and returns a summary of
// what is missing or failed.
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

func readStringArtifact(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

func lastStepStatus(dir string) (status string, duration time.Duration, totalTokens int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, 0
	}

	var earliest, latest time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_meta.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var meta StepMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		totalTokens += meta.InputTokens + meta.OutputTokens
		if earliest.IsZero() || meta.StartTime.Before(earliest) {
			earliest = meta.StartTime
		}
		if meta.EndTime.After(latest) {
			latest = meta.EndTime
			status = meta.Status
		}
	}
	if !earliest.IsZero() && !latest.IsZero() {
		duration = latest.Sub(earliest)
	}
	return status, duration, totalTokens
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
