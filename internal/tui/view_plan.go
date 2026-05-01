package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/types"
)

// planView displays the specification in a scrollable viewport.
type planView struct {
	viewport viewport.Model
	spec     types.Specification
	ready    bool
}

func newPlanView(spec types.Specification) planView {
	return planView{spec: spec}
}

func (p planView) Update(msg tea.Msg) (planView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.viewport = viewport.New(msg.Width-4, msg.Height-8)
		p.viewport.SetContent(p.renderPlan())
		p.ready = true
		return p, nil
	}

	if p.ready {
		var cmd tea.Cmd
		p.viewport, cmd = p.viewport.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p planView) View() string {
	if !p.ready {
		return "Loading plan..."
	}
	return p.viewport.View()
}

func (p planView) renderPlan() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("╔══════════════════════════════════════════════╗"))
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("║             EXECUTION PLAN                   ║"))
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("╚══════════════════════════════════════════════╝"))
	b.WriteString("\n\n")

	b.WriteString(goalStyle.Render("Goal: " + p.spec.Goal))
	b.WriteString("\n\n")

	b.WriteString(titleStyle.Render("Steps:"))
	b.WriteString("\n")
	for i, step := range p.spec.Steps {
		b.WriteString(stepStyle.Render(fmt.Sprintf("%d. %s", i+1, step)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Acceptance Criteria:"))
	b.WriteString("\n")
	for i, criterion := range p.spec.Acceptance {
		b.WriteString(acceptanceStyle.Render(fmt.Sprintf("✓ %d. %s", i+1, criterion)))
		b.WriteString("\n")
	}

	return b.String()
}
