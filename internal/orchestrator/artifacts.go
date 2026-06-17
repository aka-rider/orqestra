package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xiii/orqestra/internal/agent"
)

// mkdirAll creates a directory and all ancestors. If the leaf already exists
// as a directory, it returns nil (no error).
func mkdirAll(dir string, perm os.FileMode) error {
	info, err := os.Stat(dir)
	if err == nil && info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("mkdirAll %s: %w", dir, err)
	}
	info, err = os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path exists but is not a directory: %s", dir)
	}
	return nil
}

// writeArtifactIn writes content to a named file inside a phase subdirectory
// of the session directory. Creates the subdirectory if it does not exist.
// Returns the absolute path of the written file.
func writeArtifactIn(s agent.SessionDir, subdir, name, content string) string {
	dir := s.SubDir(subdir)
	if err := mkdirAll(dir, 0o755); err != nil {
		// Best-effort: log and fall back to session root.
		return writeArtifactFallback(s, name, content)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return writeArtifactFallback(s, name, content)
	}
	return path
}

// writeArtifactFallback writes to the session root as a best-effort fallback.
func writeArtifactFallback(s agent.SessionDir, name, content string) string {
	path := s.ArtifactPath(name)
	_ = os.WriteFile(path, []byte(content), 0o644) // fire-and-forget
	return path
}

// writeArtifactJSONIn writes JSON-marshaled v to a named file inside a phase
// subdirectory of the session directory. Returns the absolute path.
func writeArtifactJSONIn(s agent.SessionDir, subdir, name string, v any) string {
	dir := s.SubDir(subdir)
	if err := mkdirAll(dir, 0o755); err != nil {
		return writeArtifactJSONFallback(s, name, v)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return writeArtifactJSONFallback(s, name, v)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return writeArtifactJSONFallback(s, name, v)
	}
	return path
}

// writeArtifactJSONFallback writes JSON-marshaled v to the session root.
func writeArtifactJSONFallback(s agent.SessionDir, name string, v any) string {
	data, _ := json.MarshalIndent(v, "", "  ") // fire-and-forget: last-ditch fallback after the primary marshal+write already failed
	path := s.ArtifactPath(name)
	_ = os.WriteFile(path, data, 0o644) // fire-and-forget: best-effort diagnostic artifact
	return path
}

// appendDialog appends a dialog turn to dialog.md in the given directory.
// Format: "## <role>\n<message>\n\n---\n"
// Creates the file if it does not exist. Preserves prior turns.
func appendDialog(dir, role, message string) {
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "dialog.md")
	// Ensure the directory exists.
	if err := mkdirAll(dir, 0o755); err != nil {
		return
	}

	// Build the turn.
	turn := fmt.Sprintf("## %s\n%s\n\n---\n", role, message)

	// Read existing content, if any.
	existing, _ := os.ReadFile(path)
	existingStr := string(existing)

	// If the file already ends with "---\n", don't add another separator.
	// Otherwise prepend a separator.
	var buf strings.Builder
	if existingStr != "" && !strings.HasSuffix(existingStr, "\n---\n") && !strings.HasSuffix(existingStr, "---\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(existingStr)
	buf.WriteString(turn)

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		// Best-effort only.
	}
}

// highestPlanVersion scans a directory for plan-vN.md files and returns the
// highest N found. Returns 0 if no matching files exist.
func highestPlanVersion(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	planRegex := regexp.MustCompile(`^plan-v(\d+)\.md$`)
	max := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := planRegex.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max
}

// findHighestPlan scans a directory for plan-vN.md files and returns the
// absolute path of the highest-numbered plan. Returns "" if none exist.
func findHighestPlan(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	type planFile struct {
		path string
		name string
		n    int
	}
	var plans []planFile
	planRegex := regexp.MustCompile(`^plan-v(\d+)\.md$`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := planRegex.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		plans = append(plans, planFile{
			path: dir + "/" + entry.Name(),
			name: entry.Name(),
			n:    n,
		})
	}

	if len(plans) == 0 {
		return ""
	}

	// Sort by version number descending; pick the highest.
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].n > plans[j].n
	})
	return plans[0].path
}
