package main

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
)

// tickMsg fires periodically to refresh the view.
type tickMsg time.Time

// Model is the Bubble Tea model for the harness TUI.
type Model struct {
	width, height int

	// Output state.
	output   []OutputBlock
	rendered string
	dirty    bool

	// Input.
	input textarea.Model

	// Session lifecycle.
	runner harness.Runner
	err    error

	// Auto-scroll.
	atBottom bool

	// Status bar state.
	liveInput  int64
	liveOutput int64
	modelRef   string
}

// OutputBlock is a rendered unit of Claude CLI output.
type OutputBlock interface {
	Render(width int) string
}

// TextBlock is accumulated text output. IsDelta=true means partial (streaming).
type TextBlock struct {
	Text    string
	IsDelta bool
}

func (b TextBlock) Render(width int) string {
	if b.IsDelta {
		return "▎ " + b.Text
	}
	return b.Text
}

// ToolUseBlock is a tool invocation marker.
type ToolUseBlock struct {
	Name string
	Args string
}

func (b ToolUseBlock) Render(width int) string {
	return "→ " + b.Name + ": " + b.Args
}

// UsageBlock represents token usage from a result event.
type UsageBlock struct {
	Input  int64
	Output int64
}

func (b UsageBlock) Render(width int) string {
	return "" // rendered in status bar, not in content
}

// ErrorBlock is an error message.
type ErrorBlock struct {
	Err error
}

func (b ErrorBlock) Render(width int) string {
	return errorStyle.Render("Error: " + b.Err.Error())
}

const maxOutputBlocks = 200

func NewModel() Model {
	input := textarea.New()
	input.Placeholder = "Type a message..."
	input.SetWidth(80)
	input.SetHeight(3)
	input.CharLimit = 4096
	input.ShowLineNumbers = false
	input.Focus()

	return Model{
		input:    input,
		output:   make([]OutputBlock, 0),
		atBottom: true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickMsg(time.Now())
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(max(0, msg.Width-4))

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case harness.Event:
		return m.handleEvent(msg)

	case tickMsg:
		return m, tickCmd()

	case error:
		m.err = msg
		return m, nil

	default:
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C: quit.
	if key.String() == "ctrl+c" {
		if m.runner != nil {
			m.runner.Cancel()
		}
		return m, tea.Quit
	}

	// q: quit (without shift) — only when textarea is not focused.
	if key.String() == "q" && !key.Mod.Contains(tea.ModShift) && !m.input.Focused() {
		if m.runner != nil {
			m.runner.Cancel()
		}
		return m, tea.Quit
	}

	// Escape: blur input.
	if key.String() == "esc" && m.input.Focused() {
		m.input.Blur()
	}

	// Enter: send message if textarea has content.
	if key.String() == "enter" && !key.Mod.Contains(tea.ModShift) && !key.Mod.Contains(tea.ModAlt) {
		if m.runner == nil {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.runner.Post(text)
		m.input.SetValue("")
		return m, nil
	}

	// All other keys: pass to textarea.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m Model) handleEvent(u harness.Event) (tea.Model, tea.Cmd) {
	block := eventToBlock(u)
	if block == nil {
		return m, nil
	}

	// UsageBlock is rendered in the status bar, not content.
	if _, ok := block.(UsageBlock); ok {
		m.liveInput = u.Input
		m.liveOutput = u.Output
		m.dirty = true
		return m, nil
	}

	m.output = append(m.output, block)
	if len(m.output) > maxOutputBlocks {
		m.output = m.output[len(m.output)-maxOutputBlocks:]
	}
	m.dirty = true

	// Update live metrics from text blocks.
	if tb, ok := block.(TextBlock); ok {
		m.liveInput += int64(len(tb.Text))
	}

	return m, nil
}

func (m Model) View() tea.View {
	if m.height < 12 {
		v := tea.NewView(dimStyle.Render("Terminal too small. Need at least 12 rows."))
		return v
	}

	var b strings.Builder

	// Content zone.
	contentH := m.contentHeight()
	if contentH > 0 {
		b.WriteString(m.renderContent(contentH))
	}

	// Divider.
	b.WriteString(dividerStyle.Render(strings.Repeat("─", min(m.width-1, 80))))
	b.WriteString("\n")

	// Status bar.
	b.WriteString(statusStyle.Render(m.renderStatusBar()))
	b.WriteString("\n")

	// Divider.
	b.WriteString(dividerStyle.Render(strings.Repeat("─", min(m.width-1, 80))))
	b.WriteString("\n")

	// Input zone.
	b.WriteString(m.input.View())

	// Footer.
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(" [Enter] send | [Esc] blur input | [Ctrl+C] quit | [q] quit"))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m *Model) contentHeight() int {
	const (
		footerLines    = 2
		statusBarLines = 1
		inputLines     = 3
		dividerLines   = 2
	)
	return max(0, m.height - footerLines - statusBarLines - inputLines - dividerLines)
}

func (m *Model) renderContent(h int) string {
	// Re-render markdown if dirty.
	if m.dirty {
		m.renderMarkdown()
		m.dirty = false
	}

	// Build content string from blocks.
	var lines []string
	for _, block := range m.output {
		lines = append(lines, block.Render(m.width))
	}

	content := strings.Join(lines, "\n")
	if content == "" {
		content = dimStyle.Render("Awaiting Claude response...")
	}

	// If at bottom, show all content; otherwise show last h lines.
	if m.atBottom {
		return content
	}

	// Show last h lines.
	allLines := strings.Split(content, "\n")
	if len(allLines) > h {
		allLines = allLines[len(allLines)-h:]
	}
	return strings.Join(allLines, "\n")
}

func (m *Model) renderMarkdown() {
	// Collect non-delta text blocks and render as markdown.
	var mdBuf strings.Builder
	for _, block := range m.output {
		if tb, ok := block.(TextBlock); ok && !tb.IsDelta {
			mdBuf.WriteString(tb.Text)
			mdBuf.WriteString("\n\n")
		}
	}
	if mdBuf.Len() > 0 {
		m.rendered = renderMarkdown(mdBuf.String(), m.width)
	}
}

func (m *Model) renderStatusBar() string {
	var parts []string

	// Token counts.
	if m.liveInput > 0 || m.liveOutput > 0 {
		parts = append(parts, formatTokens(m.liveInput, "in"))
		parts = append(parts, formatTokens(m.liveOutput, "out"))
	}

	// Model name.
	if m.modelRef != "" {
		parts = append(parts, m.modelRef)
	}

	return strings.Join(parts, "  ")
}

func formatTokens(n int64, suffix string) string {
	if n >= 1000 {
		return statusStyle.Render(friendlyCount(n) + suffix)
	}
	return statusStyle.Render(strconv.FormatInt(n, 10) + suffix)
}

func friendlyCount(n int64) string {
	if n >= 1000000 {
		return strconv.FormatFloat(float64(n)/1000000, 'f', 1, 64) + "M"
	}
	if n >= 1000 {
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func eventToBlock(u harness.Event) OutputBlock {
	if u.Kind == harness.EventUsage {
		return UsageBlock{Input: u.Input, Output: u.Output}
	}
	if u.Tool != "" {
		return ToolUseBlock{Name: u.Tool, Args: u.Detail}
	}
	return TextBlock{Text: u.Text, IsDelta: u.IsDelta}
}
