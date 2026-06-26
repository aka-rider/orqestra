package frame

import (
	"strings"
	"testing"
)

func rowsText(f StaticFrame) []string {
	var out []string
	for _, r := range f.Rows() {
		out = append(out, r.Text())
	}
	return out
}

func TestWrapCells_WordWrap(t *testing.T) {
	cells := cellsFromSpans([]Span{{Text: "hello world foo"}})
	rows := wrapCells(cells, 8)
	if len(rows) < 2 {
		t.Fatalf("expected wrapping into multiple rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Width() > 8 {
			t.Errorf("row exceeds width 8: %q (w=%d)", r.Text(), r.Width())
		}
	}
}

func TestWrapCells_Empty(t *testing.T) {
	rows := wrapCells(nil, 10)
	if len(rows) != 1 || len(rows[0].Cells) != 0 {
		t.Errorf("empty input should yield one empty row, got %d rows", len(rows))
	}
}

func TestProse_RendersText(t *testing.T) {
	f := NewProse("the quick brown fox").SetWidth(80)
	got := rowsText(f)
	if len(got) != 1 || got[0] != "the quick brown fox" {
		t.Errorf("prose rows = %q", got)
	}
}

func TestTool_StatusIcons(t *testing.T) {
	tool := NewTool("Read", "main.go")
	pending := tool.SetWidth(40).(Tool)
	if !strings.HasPrefix(pending.Rows()[0].Text(), "◌") {
		t.Errorf("pending should start with ◌, got %q", pending.Rows()[0].Text())
	}
	ok := pending.WithStatus(ToolOK)
	if !strings.HasPrefix(ok.Rows()[0].Text(), "✓") {
		t.Errorf("ok should start with ✓, got %q", ok.Rows()[0].Text())
	}
	errd := pending.WithStatus(ToolErr)
	if !strings.HasPrefix(errd.Rows()[0].Text(), "✗") {
		t.Errorf("err should start with ✗, got %q", errd.Rows()[0].Text())
	}
}

func TestTool_NeverWrapsAndTruncates(t *testing.T) {
	tool := NewTool("Bash", strings.Repeat("x", 200)).SetWidth(20)
	if got := len(tool.Rows()); got != 1 {
		t.Errorf("tool must be a single row, got %d", got)
	}
	if w := tool.Rows()[0].Width(); w > 20 {
		t.Errorf("tool row width %d exceeds 20", w)
	}
}

func TestTool_TailPreserved_NoTruncation(t *testing.T) {
	// At width 80 the full path fits; baseline check that the path appears intact.
	tool := NewTool("Read", "/very/deeply/nested/path/file.go").SetWidth(80)
	text := tool.Rows()[0].Text()
	if !strings.Contains(text, "/very/deeply/nested/path/file.go") {
		t.Errorf("short path should appear in full at width 80, got %q", text)
	}
}

func TestTool_TailTruncate_Narrow(t *testing.T) {
	// At width 20, only 9 cols remain for the detail (prefixW=9, 2 for parens).
	// truncateToTail keeps the tail, so the directory prefix is dropped but the
	// basename survives.
	tool := NewTool("Read", "/very/deeply/nested/path/file.go").SetWidth(20)
	text := tool.Rows()[0].Text()
	if w := tool.Rows()[0].Width(); w > 20 {
		t.Errorf("row width %d exceeds 20", w)
	}
	if strings.Contains(text, "/very/") {
		t.Errorf("path prefix should be truncated at width 20, got %q", text)
	}
	if !strings.Contains(text, "file.go") {
		t.Errorf("basename should survive tail-truncation, got %q", text)
	}
}

func TestTool_NoDetailWhenNoRoom(t *testing.T) {
	// At width 10, there is no room for "(detail)" after the prefix (prefixW=9).
	// The frame must not overflow — detail is silently omitted rather than
	// appending the full string.
	tool := NewTool("Bash", "very-long-script.sh").SetWidth(10)
	if got := len(tool.Rows()); got != 1 {
		t.Errorf("tool must be a single row even when detail doesn't fit, got %d", got)
	}
	if w := tool.Rows()[0].Width(); w > 10 {
		t.Errorf("tool row width %d exceeds 10 (overflow)", w)
	}
}

func TestTool_NewFormat(t *testing.T) {
	tool := NewTool("Read", "/foo/bar.go").SetWidth(80)
	text := tool.Rows()[0].Text()
	// Format: ◌  ✑ Read(/foo/bar.go)
	if !strings.Contains(text, "✑") {
		t.Errorf("tool should contain Read icon ✑, got %q", text)
	}
	if !strings.Contains(text, "Read(") {
		t.Errorf("tool should contain 'Read(', got %q", text)
	}
	if !strings.Contains(text, "bar.go") {
		t.Errorf("tool should contain basename 'bar.go', got %q", text)
	}
}

func TestPhase_RendersLabelledRule(t *testing.T) {
	f := NewPhase("architect").SetWidth(40)
	rows := f.Rows()
	if len(rows) != 1 {
		t.Fatalf("phase should be one row, got %d", len(rows))
	}
	text := rows[0].Text()
	if !strings.Contains(text, "architect") || !strings.Contains(text, "─") {
		t.Errorf("phase rule = %q", text)
	}
	if rows[0].Width() != 40 {
		t.Errorf("phase rule should span full width 40, got %d", rows[0].Width())
	}
}

func TestSteerAndSummary(t *testing.T) {
	steer := rowsText(NewSteer("go ahead").SetWidth(80))
	if len(steer) != 1 || steer[0] != "you: go ahead" {
		t.Errorf("steer = %q", steer)
	}
	sum := rowsText(NewSummary("Done: ✓ architect").SetWidth(80))
	if len(sum) != 1 || sum[0] != "Done: ✓ architect" {
		t.Errorf("summary = %q", sum)
	}
}
