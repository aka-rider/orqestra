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
	out := renderMarkdown("", 80)
	_ = out
}

func TestRenderMarkdown_HasStyle(t *testing.T) {
	md := "# Header\n\nSome text with **bold** and `code`.\n"
	out := renderMarkdown(md, 80)
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI escape codes in styled markdown output")
	}
	if !strings.Contains(out, "Header") {
		t.Error("expected 'Header' in rendered output")
	}
}
