package tui

import (
	"errors"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"
)

type editorReturnMsg struct {
	err error
}

type editorPlanReadMsg struct {
	content string
	err     error
}

// openExternalEditor opens the given file in the user's terminal editor and
// blocks until it exits. $EDITOR is preferred, then $VISUAL. If neither is set
// it fails closed (an error message, never a silent GUI fallback): a GUI opener
// like `open` returns before the user edits, so the round-trip would read the
// file back unchanged. This matches the user-intent rule — explicit intent
// (edit the plan) errors when it can't be honoured rather than defaulting.
func openExternalEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		return func() tea.Msg {
			return editorReturnMsg{err: errors.New("no terminal editor: set $EDITOR or $VISUAL")}
		}
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorReturnMsg{err: err}
	})
}
