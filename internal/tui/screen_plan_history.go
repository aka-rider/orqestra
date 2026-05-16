package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/plan"
)

// Layout constants for the plan-history viewer.
const (
	planHistoryListMinWidth = 24
	planHistoryListFrac     = 3 // list takes 1/Nth of width, clamped to min
)

// paneMode selects which slice of revision detail the right viewport shows.
type paneMode int

const (
	paneDiff    paneMode = iota // diff vs HEAD
	paneContent                 // full plan.md content at revision
)

// PlanHistoryScreen browses every revision committed to plan-history/. It is
// pure model state: all IO happens inside tea.Cmd closures dispatched by
// loadPlanRevisions / loadPlanRevisionDetail.
type PlanHistoryScreen struct {
	historyDir string
	headSHA    string
	readOnly   bool

	revisions []plan.Revision
	cursor    int

	loading bool
	loadErr error

	detailLoad    bool
	detailSHA     string
	detailContent string
	detailDiff    string
	detailErr     error

	paneMode paneMode
	rightVP  viewport.Model

	revertPrompt     bool
	revertCursor     int // 0 = Yes, 1 = No
	pendingRevertSHA string

	width  int
	height int

	listBounds  image.Rectangle
	rightBounds image.Rectangle

	PendingIntent tea.Msg
}

// NewPlanHistoryScreen creates a fresh viewer in its zero state.
func NewPlanHistoryScreen() PlanHistoryScreen {
	vp := viewport.New()
	vp.MouseWheelEnabled = true
	return PlanHistoryScreen{
		rightVP:          vp,
		paneMode:         paneDiff,
		pendingRevertSHA: "",
	}
}

// Open resets the viewer for a fresh load and returns the load command.
func (s *PlanHistoryScreen) Open(dir string, readOnly bool, headSHA string) tea.Cmd {
	s.historyDir = dir
	s.headSHA = headSHA
	s.readOnly = readOnly
	s.revisions = nil
	s.cursor = 0
	s.loading = true
	s.loadErr = nil
	s.detailLoad = false
	s.detailSHA = ""
	s.detailContent = ""
	s.detailDiff = ""
	s.detailErr = nil
	s.paneMode = paneDiff
	s.revertPrompt = false
	s.revertCursor = 0
	s.pendingRevertSHA = ""
	s.rightVP.GotoTop()
	return loadPlanRevisions(dir)
}

// RecalculateLayout sets pane dimensions and viewport size. Called by the
// root Model.recalculateLayout when the viewer is visible.
func (s *PlanHistoryScreen) RecalculateLayout(width, height int) {
	s.width = width
	s.height = height
	if width < 50 || height < 8 {
		s.listBounds = image.Rectangle{}
		s.rightBounds = image.Rectangle{}
		return
	}
	listW := width / planHistoryListFrac
	if listW < planHistoryListMinWidth {
		listW = planHistoryListMinWidth
	}
	if listW > width-planHistoryListMinWidth {
		listW = width - planHistoryListMinWidth
	}
	rightW := width - listW - 1
	// Reserve 1 row for header, 1 row for footer.
	innerHeight := max(1, height-2)
	s.rightVP.SetWidth(max(1, rightW))
	s.rightVP.SetHeight(innerHeight)
	s.listBounds = image.Rect(0, 1, listW, 1+innerHeight)
	s.rightBounds = image.Rect(listW+1, 1, width, 1+innerHeight)
}

// SyncViewports rewrites the right viewport's content for the current pane
// mode and detail data. This is the sole SetContent site for rightVP. On
// revision change (resetOffset=true) the cursor moves to the top; on pane
// switch the existing scroll position is preserved.
func (s *PlanHistoryScreen) SyncViewports(resetOffset bool) {
	var content string
	if s.detailErr != nil {
		content = errorStyle.Render(fmt.Sprintf("error loading revision %s: %v", shortOr(s.detailSHA, 7), s.detailErr))
	} else if s.detailLoad {
		content = dimStyle.Render("loading revision…")
	} else {
		switch s.paneMode {
		case paneDiff:
			if s.detailDiff == "" {
				if s.detailSHA != "" && s.detailSHA == s.headSHA {
					content = dimStyle.Render("(no diff — selected revision is HEAD)")
				} else if s.detailContent == "" {
					content = ""
				} else {
					content = dimStyle.Render("(no diff)")
				}
			} else {
				content = s.detailDiff
			}
		case paneContent:
			content = s.detailContent
		}
	}
	prevOffset := s.rightVP.YOffset()
	s.rightVP.SetContent(content)
	if resetOffset {
		s.rightVP.SetYOffset(0)
	} else {
		s.rightVP.SetYOffset(prevOffset)
	}
}

// Update handles loader messages and key presses. Window resize is delivered
// via RecalculateLayout from the root model.
func (s PlanHistoryScreen) Update(msg tea.Msg) (PlanHistoryScreen, tea.Cmd) {
	switch m := msg.(type) {
	case planRevisionsLoadedMsg:
		s.loading = false
		s.loadErr = m.Err
		s.revisions = m.Revisions
		if s.headSHA == "" && m.HeadSHA != "" {
			s.headSHA = m.HeadSHA
		}
		if m.Err != nil || len(s.revisions) == 0 {
			return s, nil
		}
		s.cursor = 0
		return s.startDetailLoad()
	case planRevisionDetailLoadedMsg:
		// Ignore late detail loads for a revision we've moved past.
		if m.SHA != s.detailSHA && s.detailSHA != "" {
			return s, nil
		}
		s.detailLoad = false
		s.detailErr = m.Err
		s.detailContent = m.Content
		s.detailDiff = m.Diff
		if s.pendingRevertSHA != "" && s.pendingRevertSHA == m.SHA && !s.readOnly && m.Err == nil {
			s.revertPrompt = true
			s.revertCursor = 0
			s.pendingRevertSHA = ""
		}
		s.SyncViewports(true)
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(m)
	}
	return s, nil
}

func (s PlanHistoryScreen) handleKey(msg tea.KeyPressMsg) (PlanHistoryScreen, tea.Cmd) {
	// Revert confirmation modal takes priority.
	if s.revertPrompt {
		switch msg.Code {
		case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
			if s.revertCursor == 0 {
				s.revertCursor = 1
			} else {
				s.revertCursor = 0
			}
			return s, nil
		case tea.KeyEnter:
			if s.revertCursor == 0 {
				rev := s.revisions[s.cursor]
				s.PendingIntent = RevertPlanIntent{Content: s.detailContent, ShortSHA: rev.ShortSHA}
				s.revertPrompt = false
				return s, nil
			}
			s.revertPrompt = false
			return s, nil
		case tea.KeyEscape:
			s.revertPrompt = false
			return s, nil
		}
		return s, nil
	}

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			return s.startDetailLoad()
		}
		return s, nil
	case "down", "j":
		if s.cursor < len(s.revisions)-1 {
			s.cursor++
			return s.startDetailLoad()
		}
		return s, nil
	case "d":
		s.paneMode = paneDiff
		s.SyncViewports(false)
		return s, nil
	case "f":
		s.paneMode = paneContent
		s.SyncViewports(false)
		return s, nil
	case "r":
		if s.readOnly || len(s.revisions) == 0 {
			return s, nil
		}
		rev := s.revisions[s.cursor]
		if rev.SHA == s.headSHA {
			return s, nil
		}
		if s.detailLoad || s.detailSHA != rev.SHA {
			// Detail not yet ready: stash the SHA so we can open the confirm
			// when planRevisionDetailLoadedMsg arrives.
			s.pendingRevertSHA = rev.SHA
			return s, nil
		}
		if s.detailErr != nil {
			return s, nil
		}
		s.revertPrompt = true
		s.revertCursor = 0
		return s, nil
	case "esc", "ctrl+y":
		s.PendingIntent = ClosePlanHistoryIntent{}
		return s, nil
	}

	switch msg.Code {
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		s.rightVP, cmd = s.rightVP.Update(msg)
		return s, cmd
	}
	return s, nil
}

// startDetailLoad sets the detail-loading state for the current cursor and
// returns the new screen + load command. The right viewport is reset to the
// top of the new revision after the detail arrives (SyncViewports(true) in
// the planRevisionDetailLoadedMsg branch of Update).
func (s PlanHistoryScreen) startDetailLoad() (PlanHistoryScreen, tea.Cmd) {
	if len(s.revisions) == 0 || s.cursor < 0 || s.cursor >= len(s.revisions) {
		return s, nil
	}
	sha := s.revisions[s.cursor].SHA
	s.detailSHA = sha
	s.detailLoad = true
	s.detailContent = ""
	s.detailDiff = ""
	s.detailErr = nil
	return s, loadPlanRevisionDetail(s.historyDir, sha, s.headSHA)
}

// HandleMouse routes mouse events through tracked bounds (list vs right pane).
func (s PlanHistoryScreen) HandleMouse(msg tea.MouseMsg) (PlanHistoryScreen, tea.Cmd) {
	mouse := msg.Mouse()
	p := image.Pt(mouse.X, mouse.Y)

	if p.In(s.rightBounds) {
		var cmd tea.Cmd
		s.rightVP, cmd = s.rightVP.Update(msg)
		return s, cmd
	}
	if p.In(s.listBounds) {
		switch e := msg.(type) {
		case tea.MouseWheelMsg:
			switch e.Button {
			case tea.MouseWheelUp:
				if s.cursor > 0 {
					s.cursor--
					return s.startDetailLoad()
				}
			case tea.MouseWheelDown:
				if s.cursor < len(s.revisions)-1 {
					s.cursor++
					return s.startDetailLoad()
				}
			}
		case tea.MouseClickMsg:
			row := p.Y - s.listBounds.Min.Y
			if row >= 0 && row < len(s.revisions) && row != s.cursor {
				s.cursor = row
				return s.startDetailLoad()
			}
		}
	}
	return s, nil
}

// View renders the viewer. It is pure: no SetContent, no IO, no state
// mutation. Errors stored in loadErr / detailErr are rendered inline.
func (s PlanHistoryScreen) View(width, height int) string {
	if width < 50 || height < 8 {
		return " Terminal too small. Please resize."
	}
	if s.loadErr != nil {
		return errorStyle.Render(fmt.Sprintf(" Plan history unavailable: %v", s.loadErr))
	}
	if s.loading {
		return dimStyle.Render(" Loading plan history…")
	}
	if len(s.revisions) == 0 {
		return dimStyle.Render(" No plan revisions found.")
	}

	listW := width / planHistoryListFrac
	if listW < planHistoryListMinWidth {
		listW = planHistoryListMinWidth
	}
	if listW > width-planHistoryListMinWidth {
		listW = width - planHistoryListMinWidth
	}

	// Header
	headHint := ""
	if s.headSHA != "" {
		headHint = fmt.Sprintf(" • HEAD %s", shortOr(s.headSHA, 7))
	}
	roHint := ""
	if s.readOnly {
		roHint = warnStyle.Render(" [read-only]")
	}
	header := goalStyle.Render(fmt.Sprintf(" Plan History — %d revisions%s", len(s.revisions), headHint)) + roHint

	// List
	listLines := renderRevList(s.revisions, s.cursor, s.headSHA, listW, s.rightVP.Height())

	// Right pane
	right := s.rightVP.View()

	// Compose two columns line-by-line.
	body := joinColumns(listLines, right, listW, max(1, width-listW-1), s.rightVP.Height())

	// Footer or revert prompt.
	var footer string
	if s.revertPrompt {
		rev := s.revisions[s.cursor]
		yes := " Yes "
		no := " No "
		if s.revertCursor == 0 {
			yes = selectedStyle.Render(yes)
			no = dimStyle.Render(no)
		} else {
			yes = dimStyle.Render(yes)
			no = selectedStyle.Render(no)
		}
		footer = keyStyle.Render(fmt.Sprintf(" Revert plan to %s?  [%s]  [%s]  | [Enter] confirm  [Esc] cancel",
			rev.ShortSHA, yes, no))
	} else {
		hint := " ↑/↓ select • d diff • f full"
		if !s.readOnly {
			hint += " • r revert"
		}
		hint += " • esc back"
		footer = keyStyle.Render(hint)
	}

	return header + "\n" + body + "\n" + footer
}

func renderRevList(revisions []plan.Revision, cursor int, headSHA string, width, rows int) []string {
	out := make([]string, 0, len(revisions))
	for i, rev := range revisions {
		marker := "  "
		if i == cursor {
			marker = "▶ "
		}
		isHead := rev.SHA == headSHA && headSHA != ""
		rel := relTime(rev.Time)
		subjectMax := max(4, width-30)
		subject := rev.Subject
		if isHead {
			subject = subject + " (HEAD)"
		}
		if len(subject) > subjectMax {
			subject = subject[:subjectMax]
		}
		line := fmt.Sprintf("%s%s  %s  %s  %s", marker, rev.ShortSHA, rel, truncMid(rev.Author, 10), subject)
		if len(line) > width {
			line = line[:width]
		}
		if i == cursor {
			line = selectedStyle.Render(line)
		}
		out = append(out, line)
		if rows > 0 && len(out) >= rows {
			break
		}
	}
	return out
}

func joinColumns(leftLines []string, right string, leftW, rightW, rows int) string {
	rightLines := strings.Split(right, "\n")
	var b strings.Builder
	for i := 0; i < rows; i++ {
		var l string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		var r string
		if i < len(rightLines) {
			r = rightLines[i]
		}
		l = padRight(l, leftW)
		b.WriteString(l)
		b.WriteString(" ")
		if len(r) > rightW {
			r = r[:rightW]
		}
		b.WriteString(r)
		if i < rows-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func padRight(s string, w int) string {
	visible := lipgloss.Width(s)
	if visible >= w {
		return s
	}
	return s + strings.Repeat(" ", w-visible)
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func truncMid(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func shortOr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
