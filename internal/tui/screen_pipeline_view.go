package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// --- View rendering ---

// View renders the pipeline screen.
func (s PipelineScreen) View(width, height int) string {
	w := width
	if w < minWidth {
		w = minWidth
	}
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" + s.viewFooter()

	// Input zone (2 lines)
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" + s.viewInputZone() + "\n"

	// Status bar (1 line)
	sidebar := s.viewStatusLine(w) + "\n"

	// Available lines for content (height minus chrome: footer=2, input=2, sidebar=1)
	chromeH := constFooterHeight + constPipelineInputHeight + constSidebarHeight
	contentH := max(0, height-chromeH)

	var body string
	switch s.content {
	case ContentHumanGate:
		if s.activeChat != nil {
			body = s.activeChat.View(w)
		}
	case ContentUserQuestion:
		body = s.question.View(w)
	case ContentEditConfirm:
		body = s.viewEditConfirm(w)
	case ContentCompletion:
		body = s.viewCompletion(w)
	case ContentStreaming:
		body = s.viewStreaming(w)
	}

	if contentH > 0 && body != "" {
		return lipgloss.NewStyle().MaxHeight(contentH).Render(body) + "\n" + input + sidebar + footer
	}

	return input + sidebar + footer
}

// --- Status Bar ---

var spinningFrames = []string{"✻", "*", "※"}

func (s PipelineScreen) viewStatusLine(width int) string {
	if len(s.agents) == 0 {
		if s.configName != "" {
			return dimStyle.Render(" " + s.configName)
		}
		return ""
	}

	var chain strings.Builder
	var activeRow *AgentRow
	for i := range s.agents {
		a := &s.agents[i]
		var icon string
		switch a.State {
		case AgentStateDone:
			icon = "✓"
		case AgentStateFailed:
			icon = "✗"
		case AgentStateCancelled:
			icon = "⊘"
		case AgentStateGate:
			icon = "●"
		case AgentStateRunning:
			icon = "▶"
			activeRow = a
		default:
			icon = "○"
		}
		name := a.ID
		if len(name) > 4 {
			name = name[:4]
		}
		if chain.Len() > 0 {
			chain.WriteString(" ")
		}
		chain.WriteString(icon)
		chain.WriteString(name)
	}

	var detail string
	if activeRow != nil {
		var d strings.Builder
		if activeRow.ModelDisplay != "" {
			model := activeRow.ModelDisplay
			if len(model) > 16 {
				model = model[:16]
			}
			d.WriteString(model)
			d.WriteString(" ")
		}
		d.WriteString(fmt.Sprintf("↑%s ↓%s", formatTokenCompact(s.liveInput), formatTokenCompact(s.liveOutput)))
		if activeRow.ContextWindow > 0 {
			pct := (s.liveInput + s.liveOutput) * 100 / activeRow.ContextWindow
			d.WriteString(fmt.Sprintf(" ⊞%d%%", pct))
		}
		elapsed := time.Since(s.liveStart).Seconds()
		if elapsed > 0 && s.liveOutput > 0 {
			tokPS := float64(s.liveOutput) / elapsed
			d.WriteString(fmt.Sprintf(" %dt/s", int(tokPS)))
		}
		detail = d.String()
	}

	var full string
	if detail != "" {
		full = " " + chain.String() + ": " + detail
	} else {
		full = " " + chain.String()
	}

	if len(full) > width && width > 4 {
		excess := len(full) - width + 3
		if excess < len(full) {
			full = " <.." + full[excess+1:]
		}
	}
	if len(full) > width {
		full = full[:width]
	}

	return dimStyle.Render(full)
}

func formatTokenCompact(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

func (s PipelineScreen) viewInputZone() string {
	switch s.content {
	case ContentStreaming:
		status := fmt.Sprintf(" %s running...", s.phase)
		if s.lastErr != nil {
			status = errorStyle.Render(fmt.Sprintf(" Error: %v", s.lastErr))
		}
		return status
	case ContentUserQuestion:
		return keyStyle.Render(s.question.InputZone())
	case ContentEditConfirm:
		if s.hasEditComment {
			return keyStyle.Render(" [Tab/Enter] save context | [Esc] discard")
		}
		return keyStyle.Render(" [Enter] confirm | [Tab] add context | [Esc] discard")
	case ContentHumanGate:
		if s.activeChat != nil {
			return keyStyle.Render(s.activeChat.Footer())
		}
		return ""
	case ContentCompletion:
		if s.lastErr != nil {
			return errorStyle.Render(fmt.Sprintf(" Error: %v", s.lastErr))
		}
		return keyStyle.Render(" Pipeline complete")
	}
	return ""
}

func (s PipelineScreen) viewStreaming(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(renderPrefixedText(goalStyle, " Goal: ", s.goal, width))
	}

	var streamAgent string
	var completedLines []string
	var partial string
	var activities []orchestrator.Activity
	if s.streamBuf != nil {
		streamAgent, completedLines, partial, activities = s.streamBuf.SnapshotText()
	}

	b.WriteString(fmt.Sprintf(" Phase: %s", s.phase))
	if streamAgent != "" {
		b.WriteString(fmt.Sprintf("  (%s)", streamAgent))
	}
	b.WriteString("\n\n")

	if len(activities) > 0 {
		b.WriteString(renderActivityLog(activities, width, s.cwd, 20))
	}

	if len(completedLines) > 0 || partial != "" {
		b.WriteString("\n")
		b.WriteString(streamStyle.Render(" Stream"))
		b.WriteString("\n")

		innerWidth := max(1, width-constContentInset-4)
		unique := deduplicateLines(completedLines)

		const previewMax = 15
		start := 0
		if len(unique) > previewMax {
			start = len(unique) - previewMax
		}
		var contentLines []string
		contentLines = append(contentLines, unique[start:]...)

		if partial != "" {
			display := partial
			if len(display) > innerWidth {
				display = display[len(display)-innerWidth:]
			}
			contentLines = append(contentLines, display)
		}

		content := strings.Join(contentLines, "\n")
		b.WriteString(streamBlockStyle.Width(innerWidth).Render(content))
		b.WriteString("\n")
	}
	return b.String()
}

func (s PipelineScreen) viewCompletion(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(renderPrefixedText(goalStyle, " Goal: ", s.goal, width))
		b.WriteString("\n")
	}
	if s.lastErr != nil {
		b.WriteString(renderPrefixedText(errorStyle, " Error: ", s.lastErr.Error(), width))
	}
	if s.hasValidation {
		b.WriteString(" Validation:\n")
		b.WriteString(renderPrefixedText(lipgloss.NewStyle(), "   ", s.workerValidation, width))
	}
	if s.mergeErrorMsg != "" {
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(" ⚠ Merge failed — manual recovery required"))
		b.WriteString("\n")
		b.WriteString(renderPrefixedText(dimStyle, "   ", s.mergeErrorMsg, width))
		b.WriteString("\n")
	}
	elapsed := time.Since(s.startTime).Truncate(time.Second)
	b.WriteString(fmt.Sprintf("\n Elapsed: %s\n", elapsed))

	var totalIn, totalOut int64
	for _, a := range s.agents {
		totalIn += a.InputTokens
		totalOut += a.OutputTokens
	}
	if totalIn+totalOut > 0 {
		b.WriteString(fmt.Sprintf(" Tokens: %s in, %s out (%s total)\n",
			formatTokens(totalIn), formatTokens(totalOut), formatTokens(totalIn+totalOut)))
	}

	b.WriteString("\n Run Summary\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", max(1, width-constContentInset))))
	b.WriteString("\n")

	for _, a := range s.agents {
		agentElapsed := "-"
		if a.Elapsed > 0 {
			agentElapsed = a.Elapsed.Round(time.Second).String()
		} else if !a.StartedAt.IsZero() {
			agentElapsed = time.Since(a.StartedAt).Round(time.Second).String()
		}
		tokens := "-"
		if a.InputTokens > 0 || a.OutputTokens > 0 {
			tokens = fmt.Sprintf("↓%s ↑%s", formatTokens(a.InputTokens), formatTokens(a.OutputTokens))
		}
		b.WriteString(fmt.Sprintf(" Agent: %s (%s)  ⏱ %s  Tokens: %s\n",
			goalStyle.Render(a.ID), a.State, agentElapsed, tokens))

		var activities []orchestrator.Activity
		if s.streamBuf != nil {
			activities = s.streamBuf.AgentActivities(a.ID)
		}
		var fileActivities []orchestrator.Activity
		for _, act := range activities {
			if isFilePathTool(act.Tool) {
				fileActivities = append(fileActivities, act)
			}
		}
		if len(fileActivities) > 0 {
			b.WriteString(renderActivityLog(fileActivities, width, s.cwd, 3))
		} else {
			b.WriteString("   (no file activities)\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s PipelineScreen) viewEditConfirm(width int) string {
	var b strings.Builder

	b.WriteString(goalStyle.Render("  Plan was modified"))
	b.WriteString("\n\n")
	b.WriteString("  Apply these changes?\n\n")

	options := []string{"Yes, apply changes", "No, discard changes"}
	for i, opt := range options {
		cursor := "  "
		style := dimStyle
		if i == s.editConfirmCursor {
			cursor = "> "
			style = phaseStyle.Bold(true)
		}
		b.WriteString(style.Render(cursor + opt))
		if i == 0 && s.editConfirmCursor == 0 {
			b.WriteString(dimStyle.Render("  [Tab: add context]"))
		}
		b.WriteString("\n")
	}

	if s.hasEditComment {
		b.WriteString("\n")
		b.WriteString(s.editConfirmComment.View())
		b.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// renderActivityLog renders the most recent tool-use entries as a compact log.
func renderActivityLog(activities []orchestrator.Activity, width int, cwd string, maxShow int) string {
	start := 0
	if len(activities) > maxShow {
		start = len(activities) - maxShow
	}
	recent := activities[start:]

	var b strings.Builder
	for _, act := range recent {
		toolName := act.Tool
		icon := IconForAction(toolName)
		toolLabel := activityToolStyle.Render(fmt.Sprintf(" %s %-10s", icon, toolName))
		detail := act.Detail
		if isFilePathTool(act.Tool) && detail != "" {
			detail = fileHyperlink(detail, cwd)
			b.WriteString(toolLabel + " " + activityPathStyle.Render(detail))
		} else {
			b.WriteString(toolLabel + " " + activityDetailStyle.Render(detail))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func isFilePathTool(tool string) bool {
	switch tool {
	case "Read", "Write", "MultiEdit", "TodoRead", "TodoWrite":
		return true
	}
	return false
}

func fileHyperlink(path string, cwd string) string {
	absPath := path
	if !strings.HasPrefix(path, "/") {
		absPath = filepath.Join(cwd, path)
	}
	return fmt.Sprintf("\033]8;;file://%s\033\\%s\033]8;;\033\\", absPath, path)
}

func formatTokens(n int64) string {
	if n == 0 {
		return "-"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}
