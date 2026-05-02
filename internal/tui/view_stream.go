package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// streamView displays streaming output for a single harness session.
type streamView struct {
	tabIndex  int
	viewport  viewport.Model
	spinner   spinner.Model
	content   *strings.Builder
	done      bool
	err       error
	ready     bool
	startedAt time.Time
}

func newStreamView(tabIndex int) streamView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return streamView{
		tabIndex: tabIndex,
		spinner:  s,
		content:  &strings.Builder{},
	}
}

func (s *streamView) SetSize(width, height int) {
	s.viewport = viewport.New(width, height)
	s.viewport.SetContent(s.content.String())
	s.ready = true
}

func (s streamView) Update(msg tea.Msg) (streamView, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamChunkMsg:
		if msg.TabIndex == s.tabIndex {
			if s.startedAt.IsZero() {
				s.startedAt = time.Now()
			}
			s.content.WriteString(msg.Content)
			if s.ready {
				s.viewport.SetContent(s.content.String())
				s.viewport.GotoBottom()
			}
		}
		return s, nil

	case HarnessDoneMsg:
		if msg.TabIndex == s.tabIndex {
			s.done = true
			s.err = msg.Err
		}
		return s, nil

	case spinner.TickMsg:
		if !s.done {
			var cmd tea.Cmd
			s.spinner, cmd = s.spinner.Update(msg)
			return s, cmd
		}
		return s, nil
	}

	if s.ready {
		var cmd tea.Cmd
		s.viewport, cmd = s.viewport.Update(msg)
		return s, cmd
	}

	return s, nil
}

func (s streamView) View() string {
	var status string
	if s.done {
		if s.err != nil {
			status = errorStyle.Render("✗ Failed: " + s.err.Error())
		} else {
			status = goalStyle.Render("✓ Complete")
		}
	} else {
		elapsed := ""
		if !s.startedAt.IsZero() {
			d := time.Since(s.startedAt).Truncate(time.Second)
			elapsed = fmt.Sprintf(" (%s)", d)
		}
		status = s.spinner.View() + " Running..." + elapsed
	}

	if !s.ready {
		return status
	}

	return s.viewport.View() + "\n" + statusStyle.Render(status)
}
