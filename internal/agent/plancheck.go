package agent

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// CheckPlanHealth inspects a markdown document's AST for structural errors
// that often indicate a broken or hallucinated LLM generation.
// It returns a slice of human-readable warnings. If the slice is empty,
// the plan appears structurally healthy.
func CheckPlanHealth(md string) []string {
	var warnings []string

	stripped := strings.TrimSpace(md)
	if len(stripped) < 100 {
		warnings = append(warnings, "Plan is suspiciously short (<100 characters)")
	}

	// Structural format checks: detect wrong-shaped output.
	if !strings.HasPrefix(stripped, "# Plan") {
		warnings = append(warnings, "Plan does not start with '# Plan' header")
	}
	if strings.HasPrefix(stripped, "# Critic Report") || strings.HasPrefix(stripped, "## Critic Report") {
		warnings = append(warnings, "Plan is critic-shaped (starts with Critic Report header)")
	}
	if !strings.Contains(md, "## Work Packages") && !strings.Contains(md, "## Work packages") {
		warnings = append(warnings, "Plan is missing '## Work Packages' section")
	}
	if strings.Contains(md, "**Done when:**") || strings.Contains(md, "**Done When:**") {
		// has done-when, good
	} else if strings.Contains(md, "## Work Packages") || strings.Contains(md, "## Work packages") {
		warnings = append(warnings, "Work Packages lack '**Done when:**' criteria")
	}

	reader := text.NewReader([]byte(md))
	parser := goldmark.DefaultParser()
	doc := parser.Parse(reader)

	hasHeading := false
	unclosedFence := false

	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch n.Kind() {
		case ast.KindHeading:
			hasHeading = true
		}

		return ast.WalkContinue, nil
	})
	_ = err // Walk doesn't return errors in our usage

	if !hasHeading && len(stripped) > 0 {
		warnings = append(warnings, "Plan has zero headings (expected structured markdown)")
	}

	// Unclosed code fences are hard to reliably detect purely through Goldmark's AST
	// because goldmark often parses broken blocks as text/paragraphs.
	// As a fast structural heuristic, we can count the number of code fence markers.
	fences := strings.Count(md, "```")
	if fences%2 != 0 {
		unclosedFence = true
	}
	if unclosedFence {
		warnings = append(warnings, "Plan contains an unclosed code fence")
	}

	// Truncation check: if the last line ends mid-word it's likely truncated.
	// Skip list items (lines starting with - or *) since they commonly end
	// in bare words without punctuation.
	if len(stripped) > 0 {
		lastChar := stripped[len(stripped)-1]
		if lastChar != '.' && lastChar != '!' && lastChar != '?' && lastChar != '`' && lastChar != '>' && lastChar != '*' && lastChar != '_' {
			if lastChar >= 'a' && lastChar <= 'z' || lastChar >= 'A' && lastChar <= 'Z' {
				// Find the last line and check if it's a list item.
				lastNewline := strings.LastIndex(stripped, "\n")
				lastLine := stripped
				if lastNewline >= 0 {
					lastLine = stripped[lastNewline+1:]
				}
				trimmedLine := strings.TrimSpace(lastLine)
				if !strings.HasPrefix(trimmedLine, "- ") && !strings.HasPrefix(trimmedLine, "* ") {
					warnings = append(warnings, "Plan appears to end abruptly mid-sentence (truncation)")
				}
			}
		}
	}

	return warnings
}
