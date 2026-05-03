package tui

import "strings"

// renderIntent renders the intent confirmation view showing rephrased intent,
// end-state vision, and any clarification or rejection feedback.
func renderIntent(rephrased, endState, reason string, questions, examples []string, verdict string) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Intent Recognition"))
	b.WriteString("\n\n")

	b.WriteString(goalStyle.Render("Rephrased: "))
	b.WriteString(rephrased)
	b.WriteString("\n\n")

	if endState != "" {
		b.WriteString(subtitleStyle.Render("End State: "))
		b.WriteString(endState)
		b.WriteString("\n\n")
	}

	if reason != "" {
		b.WriteString(errorStyle.Render("Reason: "))
		b.WriteString(reason)
		b.WriteString("\n\n")
	}

	if len(questions) > 0 {
		b.WriteString(subtitleStyle.Render("Questions:"))
		b.WriteString("\n")
		for _, q := range questions {
			b.WriteString("  • " + q + "\n")
		}
		b.WriteString("\n")
	}

	if len(examples) > 0 {
		b.WriteString(subtitleStyle.Render("Try instead:"))
		b.WriteString("\n")
		for _, ex := range examples {
			b.WriteString("  → " + dimStyle.Render(ex) + "\n")
		}
		b.WriteString("\n")
	}

	switch verdict {
	case "reject":
		b.WriteString(errorStyle.Render("✗ Rejected") + " — refine your prompt and try again.")
	case "clarify":
		approve := approveKeyStyle.Render("[A]")
		reject := rejectKeyStyle.Render("[R]")
		b.WriteString(approve + "ccept anyway or " + reject + "efine prompt")
	default:
		approve := approveKeyStyle.Render("[A]")
		reject := rejectKeyStyle.Render("[R]")
		b.WriteString(approve + "pprove or " + reject + "eject")
	}

	return b.String()
}
