package tui

import (
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"
)

type editorReturnMsg struct {
	err error
}

// openExternalEditor opens the given file in the user's preferred editor.
func openExternalEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "open" // macOS fallback
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorReturnMsg{err: err}
	})
}
