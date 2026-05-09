package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := "# Hello\n\nThis is **bold** text.\n\n- item 1\n- item 2\n"
	out := renderMarkdown(md, 80)
	if out == md {
		t.Error("expected glamour to transform the markdown, got raw input back")
	}
	if !strings.Contains(out, "Hello") {
		t.Error("expected rendered output to contain 'Hello'")
	}
}

func TestRenderMarkdownFallback(t *testing.T) {
	// Empty content should not panic
	out := renderMarkdown("", 80)
	_ = out // glamour may add whitespace to empty input — that's fine
}
