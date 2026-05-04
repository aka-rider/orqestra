package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"
)

// PTYWriter is the interface termView uses to send input and resize the PTY.
// Decouples TUI from concrete sandbox.PTYSession to avoid circular imports.
type PTYWriter interface {
	Write([]byte) (int, error)
	Resize(cols, rows uint) error
}

// termView renders PTY output in a virtual terminal buffer inside the TUI.
type termView struct {
	tabIndex   int
	ptySession PTYWriter // nil until attached
	vt         *vt.SafeEmulator
	needsInput bool
	done       bool
	exitCode   int
	err        error
	startedAt  time.Time
	width      int
	height     int
	focused    bool
}

// newTermView creates a new terminal view with the given dimensions.
// Reserves the last row for a status bar.
func newTermView(tabIndex int, cols, rows int) termView {
	vtHeight := rows - 1
	if vtHeight < 1 {
		vtHeight = 1
	}
	return termView{
		tabIndex:  tabIndex,
		vt:        vt.NewSafeEmulator(cols, vtHeight),
		startedAt: time.Now(),
		width:     cols,
		height:    rows,
	}
}

// AttachPTY connects the terminal view to a PTY writer for input forwarding.
func (tv *termView) AttachPTY(pw PTYWriter) {
	tv.ptySession = pw
}

// Update handles messages for the terminal view.
func (tv termView) Update(msg tea.Msg) (termView, tea.Cmd) {
	switch msg := msg.(type) {
	case PTYOutputMsg:
		if msg.TabIndex != tv.tabIndex {
			return tv, nil
		}
		tv.needsInput = false
		_, _ = tv.vt.Write(msg.Data)
		return tv, nil

	case PTYNeedsInputMsg:
		if msg.TabIndex != tv.tabIndex {
			return tv, nil
		}
		tv.needsInput = true
		return tv, nil

	case PTYDoneMsg:
		if msg.TabIndex != tv.tabIndex {
			return tv, nil
		}
		tv.done = true
		tv.err = msg.Err
		tv.exitCode = msg.ExitCode
		return tv, nil

	case tea.KeyMsg:
		if !tv.focused || tv.done {
			return tv, nil
		}
		if tv.ptySession == nil {
			return tv, nil
		}
		b := keyToBytes(msg)
		if b != nil {
			_, _ = tv.ptySession.Write(b)
		}
		return tv, nil

	case tea.WindowSizeMsg:
		tv.width = msg.Width
		tv.height = msg.Height
		vtHeight := msg.Height - 1
		if vtHeight < 1 {
			vtHeight = 1
		}
		tv.vt.Resize(msg.Width, vtHeight)
		if tv.ptySession != nil {
			_ = tv.ptySession.Resize(uint(msg.Width), uint(vtHeight))
		}
		return tv, nil
	}

	return tv, nil
}

// View renders the terminal buffer and status bar.
func (tv termView) View() string {
	screen := tv.vt.String()
	statusBar := tv.statusLine()
	return screen + "\n" + statusBar
}

// statusLine renders the status bar at the bottom.
func (tv termView) statusLine() string {
	if tv.done {
		if tv.exitCode == 0 && tv.err == nil {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(
				"✓ Exited (code 0)",
			)
		}
		errStr := ""
		if tv.err != nil {
			errStr = " " + tv.err.Error()
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(
			fmt.Sprintf("✗ Exited (code %d)%s", tv.exitCode, errStr),
		)
	}

	if tv.needsInput {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true).Render(
			"⚡ Waiting for input",
		)
	}

	elapsed := time.Since(tv.startedAt).Truncate(time.Second)
	return lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Render(
		fmt.Sprintf("● Running (%s)", elapsed),
	)
}

// keyToBytes converts a bubbletea key message to raw terminal byte sequences.
func keyToBytes(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyEscape:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte{0x1b, '[', 'A'}
	case tea.KeyDown:
		return []byte{0x1b, '[', 'B'}
	case tea.KeyRight:
		return []byte{0x1b, '[', 'C'}
	case tea.KeyLeft:
		return []byte{0x1b, '[', 'D'}
	case tea.KeyCtrlC:
		return []byte{0x03}
	case tea.KeyCtrlD:
		return []byte{0x04}
	case tea.KeyCtrlZ:
		return []byte{0x1a}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			return []byte(string(msg.Runes))
		}
		return nil
	}
	return nil
}
