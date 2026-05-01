package tui

import "strings"

// renderIntent renders the intent confirmation view showing rephrased intent and outcome.
func renderIntent(rephrased, outcome string) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Intent Confirmation"))
	b.WriteString("\n\n")

	b.WriteString(goalStyle.Render("Rephrased: "))
	b.WriteString(rephrased)
	b.WriteString("\n\n")

	if outcome != "" {
		b.WriteString(subtitleStyle.Render("Expected Outcome: "))
		b.WriteString(outcome)
		b.WriteString("\n\n")
	}

	approve := approveKeyStyle.Render("[A]")
	reject := rejectKeyStyle.Render("[R]")
	b.WriteString(approve + "pprove or " + reject + "eject")

	return b.String()
}
