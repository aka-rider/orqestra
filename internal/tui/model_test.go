package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModel_KeysForwardToTermTab(t *testing.T) {
	mock := &mockPTYWriter{}

	pipeline := PipelineFuncs{
		LaunchInteractive: func(ctx context.Context, prompt string, send func(tea.Msg), tabIndex int) (PTYWriter, WaitFunc, error) {
			// Return mock PTY immediately — don't actually launch anything.
			return mock, func(ctx context.Context) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}, nil
		},
	}

	m := NewModel(pipeline)

	// Simulate program startup: window size then setProgramMsg.
	var model tea.Model
	model, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = model.(Model)

	// setProgramMsg triggers LaunchInteractive and creates a tab.
	p := tea.NewProgram(m) // We need a real program for p.Send to work.
	model, _ = m.Update(setProgramMsg{program: p})
	m = model.(Model)

	assert.Equal(t, StateRunning, m.state)
	require.Len(t, m.tabsView.termTabs, 1)
	assert.True(t, m.tabsView.termTabs[0].focused, "term tab should be focused after setProgramMsg")

	// Simulate the attachPTYMsg that the goroutine sends.
	model, _ = m.Update(attachPTYMsg{tabIndex: 0, pty: mock})
	m = model.(Model)

	// Now send a keystroke — it should reach the mock PTY.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)

	require.Len(t, mock.written, 1, "keystroke should have been forwarded to PTY")
	assert.Equal(t, []byte("h"), mock.written[0])
}

func TestModel_CtrlCQuits(t *testing.T) {
	pipeline := PipelineFuncs{
		LaunchInteractive: func(ctx context.Context, prompt string, send func(tea.Msg), tabIndex int) (PTYWriter, WaitFunc, error) {
			return &mockPTYWriter{}, func(ctx context.Context) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}, nil
		},
	}

	m := NewModel(pipeline)

	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = model.(Model)

	p := tea.NewProgram(m)
	model, _ = m.Update(setProgramMsg{program: p})
	m = model.(Model)

	// Ctrl+C should produce a tea.Quit command.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = model

	// tea.Quit is a func() tea.Msg that returns QuitMsg.
	require.NotNil(t, cmd, "ctrl+c should produce a quit command")
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "ctrl+c command should produce QuitMsg")
}
