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

// View renders the pipeline screen in its alt-screen layout.
//
// Row layout (total = height):
//   body rows      : timeline (streaming/gate) or completion summary
//   +2 rows (input): divider + input zone
//   +2 rows (footer): divider + key hints
//
// There is no status bar: the active agent + model is shown by the phase-rule
// frame at the start of its turn, and an end-of-turn summary frame on completion.
func (s PipelineScreen) View(width, height int, ctrlCPending bool) string {
	w := width
	if w < minWidth {
		w = minWidth
	}
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Bottom chrome (input + footer).
	inputZone := dividerStyle.Render(strings.Repeat("─", w)) + "\n" + s.viewInputZone() + "\n"
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" + s.viewFooter(ctrlCPending)

	// Body area height (everything above the input+footer chrome).
	bodyH := max(0, height-constPipelineInputHeight-constFooterHeight)

	// Body: timeline for streaming/gate modes; completion summary when done.
	timelineView := s.timeline.View()

	// For streaming: timeline is the entire body.
	if s.content == ContentStreaming {
		return timelineView + inputZone + footer
	}

	// For completion: show the run summary instead of the live timeline.
	if s.content == ContentCompletion {
		body := lipgloss.NewStyle().MaxHeight(bodyH).Render(s.viewCompletion(width))
		return body + "\n" + inputZone + footer
	}

	// For interactive modes, show an overlay above the timeline
	// in the body area. Timeline is still visible as context behind it.
	var overlay string
	switch s.content {
	case ContentHumanGate:
		if s.activeChat != nil {
			overlay = s.activeChat.View(w)
		}
	case ContentUserQuestion:
		overlay = s.question.View(w)
	case ContentEditConfirm:
		overlay = s.editConfirm.View(w)
	}

	if bodyH > 0 && overlay != "" {
		body := lipgloss.NewStyle().MaxHeight(bodyH).Render(overlay)
		return body + "\n" + inputZone + footer
	}
	return timelineView + inputZone + footer
}

func (s PipelineScreen) viewInputZone() string {
	switch s.content {
	case ContentStreaming:
		return s.chat.View()
	case ContentUserQuestion:
		return keyStyle.Render(s.question.InputZone())
	case ContentEditConfirm:
		if s.editConfirm.hasComment {
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


func (s PipelineScreen) viewCompletion(width int) string {
	var b strings.Builder
	if s.goal != "" {
		b.WriteString(renderPrefixedText(goalStyle, " Goal: ", s.goal, width))
		b.WriteString("\n")
	}
	if s.lastErr != nil {
		b.WriteString(renderPrefixedText(errorStyle, " Error: ", s.lastErr.Error(), width))
	}
	if s.workerValidation != "" {
		b.WriteString(" Validation:\n")
		// The worker's final output is markdown — render it as markdown, not as
		// plain wrapped text (bug: final model output showed as simple text).
		b.WriteString(renderMarkdown(s.workerValidation, width))
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
			b.WriteString(renderActivityLog(fileActivities, s.cwd, 3))
		} else {
			b.WriteString("   (no file activities)\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatActivityLine returns a single styled line for a tool invocation.
// Used both for inline rendering and for scrollback emission.
func formatActivityLine(tool, detail, cwd string) string {
	icon := IconForAction(tool)
	toolLabel := activityToolStyle.Render(fmt.Sprintf("%s %-10s", icon, tool))
	if isFilePathTool(tool) && detail != "" {
		linked := fileHyperlink(detail, cwd)
		return toolLabel + " " + activityPathStyle.Render(linked)
	}
	return toolLabel + " " + activityDetailStyle.Render(detail)
}

// renderActivityLog renders the most recent tool-use entries as a compact log.
func renderActivityLog(activities []orchestrator.Activity, cwd string, maxShow int) string {
	start := 0
	if len(activities) > maxShow {
		start = len(activities) - maxShow
	}
	recent := activities[start:]

	var b strings.Builder
	for _, act := range recent {
		b.WriteString(formatActivityLine(act.Tool, act.Detail, cwd))
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

// agentDisplayName maps an orchestrator AgentID to a human-readable label.
// "researcher" is shortened to "research"; all other IDs pass through as-is.
func agentDisplayName(id string) string {
	if id == "researcher" {
		return "research"
	}
	return id
}
