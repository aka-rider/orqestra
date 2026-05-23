package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/project"
)

// InitGateResult is the outcome of the init-gate TUI.
type InitGateResult int

const (
	InitGateOK       InitGateResult = iota // project root is valid, proceed
	InitGateUserQuit                       // user chose not to init or fatal error
	InitGateInitDone                       // user chose to init and it succeeded
)

// RunInitGate launches a minimal TUI to handle the project-root gate.
// It checks .git and .orqestra at cwd, renders errors in the TUI, and
// optionally initializes. Returns the result so main can proceed or exit.
func RunInitGate(cwd string) InitGateResult {
	// Fast path: already valid — no TUI needed.
	if project.CheckGitRoot(cwd) == nil && project.IsInitialized(cwd) {
		return InitGateOK
	}

	m := initGateModel{cwd: cwd}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return InitGateUserQuit
	}
	return result.(initGateModel).result
}

// initGateModel is the Bubble Tea model for the project-root gate screen.
type initGateModel struct {
	cwd    string
	result InitGateResult
	state  initGateState
	width  int
	height int
}

type initGateState int

const (
	gateCheckingGit initGateState = iota
	gateNoGit                     // fatal: no .git
	gateAskInit                   // .git exists, no .orqestra — ask user
	gateInitFailed                // Init() returned error
	gateDone                      // success or quit
)

func (m initGateModel) Init() tea.Cmd {
	return nil
}

func (m initGateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Perform the check on first size (TUI is ready)
		if m.state == gateCheckingGit {
			if err := project.CheckGitRoot(m.cwd); err != nil {
				m.state = gateNoGit
				return m, nil
			}
			if project.IsInitialized(m.cwd) {
				m.result = InitGateOK
				return m, tea.Quit
			}
			m.state = gateAskInit
		}
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case gateNoGit, gateInitFailed:
			// Any key quits
			m.result = InitGateUserQuit
			return m, tea.Quit

		case gateAskInit:
			switch msg.String() {
			case "y", "Y", "enter":
				if err := project.Init(m.cwd); err != nil {
					m.state = gateInitFailed
					return m, nil
				}
				m.result = InitGateInitDone
				return m, tea.Quit
			case "n", "N", "q", "esc", "ctrl+c":
				m.result = InitGateUserQuit
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m initGateModel) View() tea.View {
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	pathStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	path := pathStyle.Render(m.cwd)

	var content string
	switch m.state {
	case gateNoGit:
		content = fmt.Sprintf(
			"\n  %s\n\n  %s\n\n  %s\n",
			errStyle.Render("ERROR: Not a git repository"),
			fmt.Sprintf("Directory: %s", path),
			dimStyle.Render("Initialize a git repo first:  git init\n\n  Press any key to exit."),
		)

	case gateAskInit:
		content = fmt.Sprintf(
			"\n  %s is not initialized.\n\n  %s\n\n  %s\n",
			path,
			errStyle.Render("No .orqestra directory found."),
			"Initialize? [Y/n]",
		)

	case gateInitFailed:
		content = fmt.Sprintf(
			"\n  %s\n\n  %s\n\n  %s\n",
			errStyle.Render("ERROR: Failed to initialize .orqestra"),
			fmt.Sprintf("Directory: %s", path),
			dimStyle.Render("Press any key to exit."),
		)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
