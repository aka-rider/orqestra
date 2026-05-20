package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
)

// LoadStepLog populates logLines from the selected step's Claude JSONL.
func (s *RunDetailScreen) LoadStepLog() {
	if len(s.detail.Steps) == 0 || s.stepCursor >= len(s.detail.Steps) {
		s.logLines = []string{dimStyle.Render("  (no agent log available)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	step := s.detail.Steps[s.stepCursor]
	if step.ClaudeSessionID == "" {
		s.logLines = []string{dimStyle.Render("  (no agent log available)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	// Prefer local copy in session directory.
	if step.ClaudeSessionLogPath != "" {
		if _, statErr := os.Stat(step.ClaudeSessionLogPath); statErr == nil {
			updates, err := parseSessionLogFile(step.ClaudeSessionLogPath, 200)
			if err != nil || len(updates) == 0 {
				s.logLines = []string{dimStyle.Render("  (empty log)")}
				s.logVP.SetContent(strings.Join(s.logLines, "\n"))
				return
			}
			s.logLines = formatLogUpdates(updates)
			s.logVP.SetContent(strings.Join(s.logLines, "\n"))
			s.logVP.GotoBottom()
			return
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		s.logLines = []string{dimStyle.Render("  (cannot determine cwd)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		s.logLines = []string{dimStyle.Render(fmt.Sprintf("  (log not found: %s)", step.ClaudeSessionID))}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	updates, err := parseSessionLogFile(logPath, 200)
	if err != nil || len(updates) == 0 {
		s.logLines = []string{dimStyle.Render("  (empty log)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	s.logLines = formatLogUpdates(updates)
	s.logVP.SetContent(strings.Join(s.logLines, "\n"))
	s.logVP.GotoBottom()
}

func parseSessionLogFile(path string, maxLines int) ([]harness.StreamUpdate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	updates, err := harness.ParseSessionLogStream(f)
	if err != nil {
		return nil, err
	}
	if maxLines > 0 && len(updates) > maxLines {
		updates = updates[len(updates)-maxLines:]
	}
	return updates, nil
}

// formatLogUpdates converts parsed stream updates to styled display lines.
func formatLogUpdates(updates []harness.StreamUpdate) []string {
	lines := make([]string, 0, len(updates))
	for _, update := range updates {
		if update.Tool != "" {
			line := "  " + activityToolStyle.Render(update.Tool) + " " + activityPathStyle.Render(update.Detail)
			lines = append(lines, line)
			continue
		}
		if update.Text != "" {
			text := strings.TrimSpace(update.Text)
			if text == "" {
				continue
			}
			line := "  ╶ " + dimStyle.Render(text)
			lines = append(lines, line)
		}
	}
	return lines
}

// openStepLog opens the JSONL file for the selected step in the system editor.
func (s RunDetailScreen) openStepLog() (RunDetailScreen, tea.Cmd) {
	if len(s.detail.Steps) == 0 || s.stepCursor >= len(s.detail.Steps) {
		return s, nil
	}
	step := s.detail.Steps[s.stepCursor]
	if step.ClaudeSessionID == "" {
		return s, nil
	}

	// Prefer local copy.
	if step.ClaudeSessionLogPath != "" {
		if _, statErr := os.Stat(step.ClaudeSessionLogPath); statErr == nil {
			cmd := exec.Command("open", step.ClaudeSessionLogPath)
			if err := cmd.Start(); err != nil {
				_ = err // fire-and-forget: opening external editor for log file
			}
			return s, nil
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return s, nil
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		return s, nil
	}

	cmd := exec.Command("open", logPath)
	if err := cmd.Start(); err != nil {
		_ = err // fire-and-forget: opening external editor for log file
	}
	return s, nil
}
