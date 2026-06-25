package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// setupModel owns the pipeline-setup overlay state.
// It is a value sub-model: Update returns a new copy; View is pure.
type setupModel struct {
	keys          keymap.Bindings
	open          bool
	cursor        int // 0..numSetupItems-1
	setup         orchestrator.PipelineSetup
	PendingIntent tea.Msg // consumed by parent after each Update
}

// setupItem order mirrors the rendered list.
const (
	setupItemDeliberation = 0
	setupItemExecution    = 1
	setupItemValidation   = 2
	setupItemGateFirst    = 3 // GateAfterDeliberation
	numSetupItems         = 4
)

// gateOrder maps gate cursor positions (from setupItemGateFirst) to HumanGatePosition values.
var gateOrder = [1]orchestrator.HumanGatePosition{
	orchestrator.GateAfterDeliberation,
}

func newSetupModel(keys keymap.Bindings) setupModel {
	return setupModel{keys: keys, setup: orchestrator.DefaultPipelineSetup()}
}

// Open opens the panel with a fresh working draft initialised from setup.
func (s *setupModel) Open(setup orchestrator.PipelineSetup) {
	s.setup = setup
	s.open = true
	s.cursor = 0
}

// Close hides the panel without persisting the working draft.
func (s *setupModel) Close() { s.open = false }

// IsOpen reports whether the panel is visible.
func (s *setupModel) IsOpen() bool { return s.open }

// Update handles key events inside the setup panel.
// It returns a new setupModel and an optional command.
// When the user confirms or cancels, PendingIntent is set.
func (s setupModel) Update(msg tea.KeyPressMsg) (setupModel, tea.Cmd) {
	s.PendingIntent = nil
	switch {
	case key.Matches(msg, s.keys.Up):
		s.cursor = (s.cursor - 1 + numSetupItems) % numSetupItems
	case key.Matches(msg, s.keys.Down):
		s.cursor = (s.cursor + 1) % numSetupItems
	case key.Matches(msg, s.keys.Left):
		s = s.changeValue("left")
	case key.Matches(msg, s.keys.Right):
		s = s.changeValue("right")
	case key.Matches(msg, s.keys.Submit):
		s.open = false
		s.PendingIntent = ConfirmSetupIntent{Setup: s.setup}
	case key.Matches(msg, s.keys.Back), key.Matches(msg, s.keys.SetupPanel):
		s.open = false
	case msg.Code == ' ':
		// Space (rune 32): toggle — direction irrelevant for bools/gates.
		s = s.changeValue("left")
	}
	return s, nil
}

func (s setupModel) changeValue(dir string) setupModel {
	switch s.cursor {
	case setupItemDeliberation:
		switch dir {
		case "left":
			s.setup.DeliberationRounds = max(1, s.setup.DeliberationRounds-1)
		case "right":
			s.setup.DeliberationRounds = min(3, s.setup.DeliberationRounds+1)
		}
	case setupItemExecution:
		s.setup.Execution = !s.setup.Execution
	case setupItemValidation:
		s.setup.Validation = !s.setup.Validation
	default:
		gateIdx := s.cursor - setupItemGateFirst
		if gateIdx >= 0 && gateIdx < len(gateOrder) {
			s.setup.HumanGates = s.setup.HumanGates.Toggle(gateOrder[gateIdx])
		}
	}
	return s
}

var (
	setupBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("14")).
				Padding(0, 1)

	setupTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	setupCursorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("14"))

	setupValueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))

	setupDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	setupHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

// View renders the setup panel as a self-contained bordered box.
// The caller is responsible for placing it on screen.
func (s setupModel) View() string {
	var b strings.Builder

	b.WriteString(setupTitleStyle.Render("Pipeline Setup") + "\n\n")

	renderBool := func(idx int, label string, val bool) {
		cur := "  "
		if s.cursor == idx {
			cur = setupCursorStyle.Render("▶ ")
		}
		var v string
		if val {
			v = setupValueStyle.Render("◁ Enabled ▷")
		} else {
			v = setupValueStyle.Render("◁ Disabled ▷")
		}
		b.WriteString(fmt.Sprintf("%s%-24s%s\n", cur, label+":", v))
	}

	renderInt := func(idx int, label string, val int) {
		cur := "  "
		if s.cursor == idx {
			cur = setupCursorStyle.Render("▶ ")
		}
		v := setupValueStyle.Render(fmt.Sprintf("◁ %d ▷", val))
		b.WriteString(fmt.Sprintf("%s%-24s%s\n", cur, label+":", v))
	}

	renderInt(setupItemDeliberation, "Deliberation", s.setup.DeliberationRounds)
	renderBool(setupItemExecution, "Execution", s.setup.Execution)
	renderBool(setupItemValidation, "Validation", s.setup.Validation)

	b.WriteString("\n" + setupDimStyle.Render("  Human Review:") + "\n")

	gateLabels := [1]string{
		"After Deliberation",
	}
	for i, gate := range gateOrder {
		idx := setupItemGateFirst + i
		cur := "  "
		if s.cursor == idx {
			cur = setupCursorStyle.Render("▶ ")
		}
		check := "[ ]"
		if s.setup.HumanGates.Active(gate) {
			check = setupValueStyle.Render("[x]")
		}
		b.WriteString(fmt.Sprintf("%s  %s %s\n", cur, check, gateLabels[i]))
	}

	b.WriteString("\n" + setupHintStyle.Render("[↑↓] navigate  [←→/Space] change  [Enter] confirm  [Esc] cancel"))

	return setupBorderStyle.Render(b.String())
}

// viewSetupOverlay centers the setup panel within the terminal dimensions.
func viewSetupOverlay(s setupModel, w, h int) string {
	panel := s.View()
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, panel,
		lipgloss.WithWhitespaceChars(" "),
	)
}
