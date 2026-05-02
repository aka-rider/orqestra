package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/types"
)

// validateView renders the work validation result panel.
// While validation is in-flight it shows a spinner; once done it renders
// a green pass panel or red fail panel based on the result.
type validateView struct {
	spinner  spinner.Model
	criteria []string // acceptance criteria from the spec, for pass/fail labelling
	result   *types.ValidationResult
	err      error
	done     bool
}

func newValidateView(criteria []string) validateView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return validateView{
		spinner:  s,
		criteria: criteria,
	}
}

// Init starts the spinner tick loop.
func (v validateView) Init() tea.Cmd {
	return v.spinner.Tick
}

func (v validateView) Update(msg tea.Msg) (validateView, tea.Cmd) {
	switch msg := msg.(type) {
	case ValidationResultMsg:
		v.done = true
		if msg.Err != nil {
			v.err = msg.Err
		} else {
			v.result = &msg.Result
		}
		return v, nil

	case spinner.TickMsg:
		if !v.done {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}
		return v, nil
	}
	return v, nil
}

func (v validateView) View() string {
	if !v.done {
		return statusStyle.Render(v.spinner.View() + " Validating output against spec…")
	}

	if v.err != nil {
		return errorStyle.Render("✗ " + v.err.Error())
	}

	if v.result == nil {
		return statusStyle.Render("Validation complete")
	}

	if v.result.Passed {
		return v.renderPassed()
	}
	return v.renderFailed()
}

func (v validateView) renderPassed() string {
	var sb strings.Builder
	sb.WriteString(goalStyle.Render("✓ All acceptance criteria passed") + "\n\n")
	for _, criterion := range v.criteria {
		sb.WriteString(goalStyle.Render("  ✓ ") + criterion + "\n")
	}
	return borderStyle.Render(strings.TrimRight(sb.String(), "\n"))
}

func (v validateView) renderFailed() string {
	failedReasons := make(map[string]string, len(v.result.FailedCriteria))
	for _, fc := range v.result.FailedCriteria {
		failedReasons[fc.Criterion] = fc.Reason
	}

	var sb strings.Builder
	sb.WriteString(errorStyle.Render("✗ Work validation failed") + "\n\n")

	for _, criterion := range v.criteria {
		if reason, failed := failedReasons[criterion]; failed {
			sb.WriteString(errorStyle.Render("  ✗ ")+criterion+"\n")
			sb.WriteString(dimStyle.Render("    "+reason) + "\n")
		} else {
			sb.WriteString(goalStyle.Render("  ✓ ") + criterion + "\n")
		}
	}

	return borderStyle.Render(strings.TrimRight(sb.String(), "\n"))
}
