package plan

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/types"
)

const currentSchemaVersion = "1"

// Spec is the portable, markdown-serialisable representation of a plan.
// It is a subset of types.Specification focused on human-editable fields.
type Spec struct {
	SchemaVersion      string
	Goal               string
	Context            string
	Steps              []string
	Acceptance         []string
	Constraints        []string
	Risks              []string
	ValidationCommands []string
	ExpectedArtifacts  []string
}

// MarshalMarkdown serialises s to a section-headed markdown document.
// ValidationCommands are wrapped in a fenced code block; all other slice
// fields are written as numbered lists.
func MarshalMarkdown(s Spec) ([]byte, error) {
	var b bytes.Buffer

	b.WriteString("# Orqestra Plan\n\n")

	writeScalar := func(heading, value string) {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", heading, value)
	}
	writeList := func(heading string, items []string) {
		fmt.Fprintf(&b, "## %s\n\n", heading)
		for i, item := range items {
			fmt.Fprintf(&b, "%d. %s\n", i+1, item)
		}
		b.WriteString("\n")
	}
	writeCode := func(heading string, items []string) {
		fmt.Fprintf(&b, "## %s\n\n```\n", heading)
		for _, item := range items {
			b.WriteString(item)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	}

	writeScalar("SchemaVersion", s.SchemaVersion)
	writeScalar("Goal", s.Goal)
	writeScalar("Context", s.Context)
	writeList("Steps", s.Steps)
	writeList("Acceptance", s.Acceptance)
	writeList("Constraints", s.Constraints)
	writeList("Risks", s.Risks)
	writeCode("ValidationCommands", s.ValidationCommands)
	writeList("ExpectedArtifacts", s.ExpectedArtifacts)

	return b.Bytes(), nil
}

var (
	h2Re           = regexp.MustCompile(`^## (.+)$`)
	numberedItemRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	bulletItemRe   = regexp.MustCompile(`^[-*]\s+(.+)$`)
	nonAlphanumRe  = regexp.MustCompile(`[^a-z0-9]+`)
)

// UnmarshalMarkdown parses a markdown plan document produced by MarshalMarkdown.
// Returns a non-nil error if SchemaVersion or Goal are absent.
func UnmarshalMarkdown(data []byte) (Spec, error) {
	var s Spec
	sections := splitH2(data)
	for name, lines := range sections {
		switch name {
		case "SchemaVersion":
			s.SchemaVersion = strings.TrimSpace(strings.Join(lines, "\n"))
		case "Goal":
			s.Goal = strings.TrimSpace(strings.Join(lines, "\n"))
		case "Context":
			s.Context = strings.TrimSpace(strings.Join(lines, "\n"))
		case "Steps":
			s.Steps = parseListItems(lines)
		case "Acceptance":
			s.Acceptance = parseListItems(lines)
		case "Constraints":
			s.Constraints = parseListItems(lines)
		case "Risks":
			s.Risks = parseListItems(lines)
		case "ValidationCommands":
			s.ValidationCommands = parseFencedBlock(lines)
		case "ExpectedArtifacts":
			s.ExpectedArtifacts = parseListItems(lines)
		}
	}
	if s.SchemaVersion == "" {
		return Spec{}, fmt.Errorf("plan document missing required field: SchemaVersion")
	}
	if s.Goal == "" {
		return Spec{}, fmt.Errorf("plan document missing required field: Goal")
	}
	return s, nil
}

// splitH2 parses markdown and returns a map of H2 heading name → content lines.
func splitH2(data []byte) map[string][]string {
	result := make(map[string][]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var current string
	var lines []string

	flush := func() {
		if current != "" {
			result[current] = lines
			lines = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if m := h2Re.FindStringSubmatch(line); m != nil {
			flush()
			current = strings.TrimSpace(m[1])
		} else if current != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return result
}

func parseListItems(lines []string) []string {
	var items []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if m := numberedItemRe.FindStringSubmatch(t); m != nil {
			items = append(items, m[1])
		} else if m := bulletItemRe.FindStringSubmatch(t); m != nil {
			items = append(items, m[1])
		}
	}
	return items
}

func parseFencedBlock(lines []string) []string {
	inBlock := false
	var items []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock && strings.TrimSpace(line) != "" {
			items = append(items, line)
		}
	}
	return items
}

// SaveToFile writes spec as markdown to dir and returns the absolute file path.
// The filename is orqestra-plan-<goal-slug>-<unix-ts>.md.
func SaveToFile(spec Spec, dir string) (string, error) {
	data, err := MarshalMarkdown(spec)
	if err != nil {
		return "", fmt.Errorf("marshaling plan: %w", err)
	}
	slug := goalSlug(spec.Goal)
	ts := time.Now().Unix()
	name := fmt.Sprintf("orqestra-plan-%s-%d.md", slug, ts)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing plan to %s: %w", path, err)
	}
	return path, nil
}

// LoadFromFile reads and parses a plan markdown file.
// Hard-errors if the path does not exist — no IsNotExist fallback.
func LoadFromFile(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("reading plan file %s: %w", path, err)
	}
	return UnmarshalMarkdown(data)
}

// goalSlug converts a goal string to a lowercase URL-safe slug (max 50 chars).
func goalSlug(goal string) string {
	s := strings.ToLower(goal)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "plan"
	}
	return s
}

// FromSpecification converts a types.Specification to a plan.Spec for serialisation.
func FromSpecification(ts types.Specification) Spec {
	sv := ts.SchemaVersion
	if sv == "" {
		sv = currentSchemaVersion
	}
	var valCmds []string
	for _, vc := range ts.ValidationCommands {
		if vc.Command != "" {
			valCmds = append(valCmds, vc.Command)
		}
	}
	return Spec{
		SchemaVersion:      sv,
		Goal:               ts.Goal,
		Context:            ts.Context,
		Steps:              ts.Steps,
		Acceptance:         ts.Acceptance,
		Constraints:        ts.Constraints,
		Risks:              ts.Risks,
		ValidationCommands: valCmds,
		ExpectedArtifacts:  ts.ExpectedArtifacts,
	}
}

// ToSpecification converts a plan.Spec to a types.Specification.
func ToSpecification(s Spec) types.Specification {
	var valCmds []types.ValidationCommand
	for _, cmd := range s.ValidationCommands {
		valCmds = append(valCmds, types.ValidationCommand{Command: cmd})
	}
	return types.Specification{
		SchemaVersion:      s.SchemaVersion,
		Goal:               s.Goal,
		Context:            s.Context,
		Steps:              s.Steps,
		Acceptance:         s.Acceptance,
		Constraints:        s.Constraints,
		Risks:              s.Risks,
		ValidationCommands: valCmds,
		ExpectedArtifacts:  s.ExpectedArtifacts,
	}
}
