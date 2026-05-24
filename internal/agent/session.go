package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionDir manages the host-side session directory where pipeline artifacts accumulate.
type SessionDir struct {
	Path string // absolute path to the session directory
}

// NewSessionDir creates and returns a new session directory under .orqestra/sessions/.
// The directory name includes a timestamp and optional slug for identification.
func NewSessionDir(repoPath, slug string) (SessionDir, error) {
	ts := time.Now().Format("2006-01-02-150405")
	name := ts
	if slug != "" {
		name = ts + "-" + slug
	}
	dir := filepath.Join(repoPath, ".orqestra", "sessions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SessionDir{}, fmt.Errorf("creating session dir %s: %w", dir, err)
	}
	return SessionDir{Path: dir}, nil
}

// ArtifactPath returns the absolute path for a named artifact within the session.
func (s SessionDir) ArtifactPath(name string) string {
	return filepath.Join(s.Path, name)
}

// WriteArtifact writes an artifact to the session directory.
func (s SessionDir) WriteArtifact(name string, data []byte) error {
	path := s.ArtifactPath(name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing artifact %s: %w", name, err)
	}
	return nil
}

// ReadArtifact reads an artifact from the session directory.
func (s SessionDir) ReadArtifact(name string) ([]byte, error) {
	path := s.ArtifactPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading artifact %s: %w", name, err)
	}
	return data, nil
}

// StepMeta is the per-agent metadata persisted as JSON in the session directory.
type StepMeta struct {
	AgentID              string    `json:"agent_id"`
	ModelRef             string    `json:"model_ref,omitempty"`
	ModelDisplay         string    `json:"model_display,omitempty"`
	Provider             string    `json:"provider,omitempty"`
	ContextWindow        int64     `json:"context_window,omitempty"`
	StartTime            time.Time `json:"start_time"`
	EndTime              time.Time `json:"end_time"`
	ClaudeSessionID      string    `json:"claude_session_id,omitempty"`
	ClaudeProjectPath    string    `json:"claude_project_path,omitempty"`
	ClaudeSessionLogPath string    `json:"claude_session_log_path,omitempty"`
	ClaudePlanFilePath   string    `json:"claude_plan_file_path,omitempty"`
	Status               string    `json:"status"` // "done" or "failed"
	Error                string    `json:"error,omitempty"`
	PlanSource           string    `json:"plan_source,omitempty"` // "plan_file" (default) or "stream_fallback"
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
}

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

const sessionTimeFmt = "2006-01-02-150405"

// KnownAgents lists the canonical agent IDs in pipeline execution order.
var KnownAgents = []string{"researcher", "architect", "critic", "worker"}

// ArtifactRequirement describes a required artifact and which agent produced it.
type ArtifactRequirement struct {
	Name    string
	AgentID string
}

// RunCompleteness describes whether a historical run is complete or what is missing.
type RunCompleteness struct {
	Complete          bool
	MissingAgents     []string
	FailedAgents      []string
	MissingArtifacts  []ArtifactRequirement
	FirstMissingAgent string
	Reason            string
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
			continue // skip dirs we can't parse
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

	// Load step metas via glob-based discovery (supports revisions and critic)
	matches, _ := filepath.Glob(filepath.Join(runPath, "*_meta.json"))
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
	// Sort by StartTime to preserve pipeline order
	sort.Slice(detail.Steps, func(i, j int) bool {
		return detail.Steps[i].StartTime.Before(detail.Steps[j].StartTime)
	})

	return detail, nil
}

// parseSessionDirName splits "2006-01-02-150405-some-slug" into timestamp and slug.
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

// readStringArtifact reads a file as a string, returning empty on any error.
func readStringArtifact(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// SessionLogResolver resolves the on-disk path of a Claude CLI session JSONL.
type SessionLogResolver func(repoPath, sessionID string) (string, error)

// CopySessionLog copies the Claude CLI session JSONL for sessionID into this session
// directory as destName (e.g., "researcher_session.jsonl").
// repoPath is the repository root used to locate the source JSONL.
// Returns ("", nil) if sessionID is empty or if the session dir is unset.
// Returns ("", err) on IO failure — callers should slog.Warn and continue.
func CopySessionLog(s SessionDir, repoPath, sessionID, destName string, resolve SessionLogResolver) (string, error) {
	if sessionID == "" || s.Path == "" {
		return "", nil
	}
	src, err := resolve(repoPath, sessionID)
	if err != nil {
		return "", fmt.Errorf("copy session log: resolve %s: %w", sessionID, err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("copy session log: read %s: %w", src, err)
	}
	if err := s.WriteArtifact(destName, data); err != nil {
		return "", fmt.Errorf("copy session log: write %s: %w", destName, err)
	}
	return s.ArtifactPath(destName), nil
}

// lastStepStatus reads all *_meta.json in a directory and returns the status
// of the last step by end time, the total duration, and the total token count.
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

// deduplicate returns a copy of sl with all duplicate strings removed (order preserved).
func deduplicate(sl []string) []string {
	if len(sl) == 0 {
		return sl
	}
	seen := make(map[string]struct{}, len(sl))
	out := make([]string, 0, len(sl))
	for _, s := range sl {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// AnalyzeRunCompleteness inspects a session directory and returns a summary of
// what is missing or failed. It does NOT modify RunDetail.
func AnalyzeRunCompleteness(runPath string, detail RunDetail) RunCompleteness {
	var c RunCompleteness

	// Build a map of agentID -> status from step metas.
	agentStatus := map[string]string{}
	for _, meta := range detail.Steps {
		agentStatus[meta.AgentID] = meta.Status
	}

	// Check each known agent.
	for _, agentID := range KnownAgents {
		status, ok := agentStatus[agentID]
		if !ok {
			c.MissingAgents = append(c.MissingAgents, agentID)
			continue
		}
		if status == "failed" {
			c.FailedAgents = append(c.FailedAgents, agentID)
		}
	}

	// Check required artifacts.
	requiredArtifacts := []ArtifactRequirement{
		{Name: "researcher_draft.md", AgentID: "researcher"},
		{Name: "architect_meta.json", AgentID: "architect"},
		{Name: "final_plan.md", AgentID: "architect"},
	}
	// Critic artifacts are optional (critic may be nil).
	if agentStatus["critic"] != "" {
		requiredArtifacts = append(requiredArtifacts,
			ArtifactRequirement{Name: "critic_report.md", AgentID: "critic"},
		)
	}
	// Worker artifacts.
	if agentStatus["worker"] != "" {
		requiredArtifacts = append(requiredArtifacts,
			ArtifactRequirement{Name: "worker_output.txt", AgentID: "worker"},
		)
	}

	for _, art := range requiredArtifacts {
		if !fileExists(filepath.Join(runPath, art.Name)) {
			c.MissingArtifacts = append(c.MissingArtifacts, art)
		}
	}

	// Determine first missing agent (pipeline order).
	for _, agentID := range KnownAgents {
		if contains(c.MissingAgents, agentID) || contains(c.FailedAgents, agentID) {
			c.FirstMissingAgent = agentID
			break
		}
	}

	// Build reason string.
	var reasons []string
	if len(c.MissingAgents) > 0 {
		reasons = append(reasons, fmt.Sprintf("agents never ran: %s", strings.Join(c.MissingAgents, ", ")))
	}
	if len(c.FailedAgents) > 0 {
		reasons = append(reasons, fmt.Sprintf("agents failed: %s", strings.Join(c.FailedAgents, ", ")))
	}
	if len(c.MissingArtifacts) > 0 {
		var names []string
		for _, a := range c.MissingArtifacts {
			names = append(names, a.Name)
		}
		reasons = append(reasons, fmt.Sprintf("missing artifacts: %s", strings.Join(names, ", ")))
	}
	if len(reasons) > 0 {
		c.Reason = strings.Join(reasons, "; ")
	}

	// NoExecute path: stops at plan gate — not "incomplete" in a broken sense,
	// but still not a full run.
	if detail.Status == "noexecute" || detail.Status == "" {
		// If we have a status but no agents ran, mark as incomplete.
		if len(c.MissingAgents) > 0 && len(c.FailedAgents) == 0 {
			c.Complete = false
		}
	}

	c.Complete = len(c.MissingAgents) == 0 && len(c.FailedAgents) == 0 && len(c.MissingArtifacts) == 0
	c.MissingAgents = deduplicate(c.MissingAgents)
	c.FailedAgents = deduplicate(c.FailedAgents)

	return c
}

// fileExists reports whether the given path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// contains reports whether a string slice contains the given value.
func contains(sl []string, v string) bool {
	for _, s := range sl {
		if s == v {
			return true
		}
	}
	return false
}
