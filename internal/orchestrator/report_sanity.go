package orchestrator

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var headingRe = regexp.MustCompile(`(?m)^#`)

// looksLikeReport returns true when s is plausibly a structured text report.
// Rejects: empty/near-empty text, text with no '#' heading, and text that is
// mostly non-printable (binary/garbled). Accepts renamed or reordered sections,
// preamble before the first heading, and "# Goal" in place of "# Plan".
func looksLikeReport(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 20 {
		return false
	}
	if !headingRe.MatchString(s) {
		return false
	}
	total := utf8.RuneCountInString(s)
	if total == 0 {
		return false
	}
	var printable int
	for _, r := range s {
		if unicode.IsPrint(r) || r == '\n' || r == '\t' {
			printable++
		}
	}
	return float64(printable)/float64(total) >= 0.80
}
