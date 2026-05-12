# Implementation Plan: TUI Question Bridge Multi-select and Custom Option Text

This plan implements explicit TUI UX paradigms for `AskUserQuestion` tool calls from MCP agents. It enables checkbox `[x]/[ ]` formatting for `multi_select: true`, radio button `(*)/( )` formatting for single-select, and contextual injected textareas for custom context (`allow_custom: true`).

## 1. Struct Updates (`internal/tui/screen_pipeline.go`)

Update the `PipelineScreen` struct to track textarea states per option when `allow_custom` is enabled, alongside the `Tab` focus state.

```go
	// User question state (MCP AskUserQuestion bridge)
	userQuestion      harness.MCPToolCall
	questionCursor    int
	questionSelected  map[int]bool
	questionTextareas map[int]textarea.Model // custom text per option
	globalTextarea    textarea.Model         // freeform when no options
	focusInTextarea   bool                   // true when a textarea receives keystrokes
```

Initialize these fields inside the `EventUserQuestion` case in `ApplyEvent`:

```go
	case orchestrator.EventUserQuestion:
		s.content = ContentUserQuestion
		s.userQuestion = event.UserQuestion
		s.questionCursor = 0
		s.questionSelected = make(map[int]bool)
		s.focusInTextarea = false
		s.contentVP.GotoTop()

		contentWidth := max(1, int(float64(width)*splitRatio))

		if len(event.UserQuestion.Options) == 0 {
			// Freeform mode
			ta := textarea.New()
			ta.Placeholder = "Type your answer..."
			ta.SetWidth(max(1, contentWidth-4))
			ta.SetHeight(3)
			ta.CharLimit = 1024
			ta.Focus()
			s.globalTextarea = ta
			s.focusInTextarea = true
		} else {
			// Options mode: initialize textareas for each element
			s.questionTextareas = make(map[int]textarea.Model)
			allowCustom := true
			if event.UserQuestion.AllowCustom != nil {
				allowCustom = *event.UserQuestion.AllowCustom
			}
			if allowCustom {
				for i := range event.UserQuestion.Options {
					ta := textarea.New()
					ta.Placeholder = "Optional custom context..."
					ta.SetWidth(max(1, contentWidth-8))
					ta.SetHeight(1)
					ta.CharLimit = 512
					// Ensure textarea is not focused until toggled
					s.questionTextareas[i] = ta
				}
			}
		}
```

## 2. State Machine (`handleUserQuestionKey`)

Update the update loop to securely block list navigation bindings when the textarea is focused. Define `Tab` as the toggle for switching between list navigation and input modes.

```go
func (s PipelineScreen) handleUserQuestionKey(msg tea.KeyPressMsg) (PipelineScreen, tea.Cmd) {
	opts := s.userQuestion.Options
	hasOptions := len(opts) > 0

	// Handle 'Esc' globally to cancel/skip
	if msg.Code == tea.KeyEscape && !s.focusInTextarea {
		s.content = ContentStreaming
		s.SyncViewports()
		s.PendingIntent = SubmitQuestionAnswerIntent{Answer: harness.MCPAnswer{Skipped: true}}
		return s, nil
	}

	// Handle 'Esc' while focused inside a textarea drops focus back to the list
	if msg.Code == tea.KeyEscape && s.focusInTextarea {
		s.focusInTextarea = false
		if hasOptions {
			ta := s.questionTextareas[s.questionCursor]
			ta.Blur()
			s.questionTextareas[s.questionCursor] = ta
		} else {
			s.globalTextarea.Blur()
		}
		s.SyncViewports()
		return s, nil
	}

	// Tab toggles focus for the active list item context input
	if msg.Code == tea.KeyTab && hasOptions {
		s.focusInTextarea = !s.focusInTextarea
		ta := s.questionTextareas[s.questionCursor]
		if s.focusInTextarea {
			ta.Focus()
		} else {
			ta.Blur()
		}
		s.questionTextareas[s.questionCursor] = ta
		s.SyncViewports()
		return s, nil
	}

	// Submit on Enter
	if msg.Code == tea.KeyEnter {
		if !msg.Mod.Contains(tea.ModShift) && !msg.Mod.Contains(tea.ModAlt) {
			answer := s.buildQuestionAnswer()
			s.content = ContentStreaming
			s.SyncViewports()
			s.PendingIntent = SubmitQuestionAnswerIntent{Answer: answer}
			return s, nil
		}
	}

	// Intercept keys if focused in any textarea
	if s.focusInTextarea || !hasOptions {
		var cmd tea.Cmd
		if hasOptions {
			ta := s.questionTextareas[s.questionCursor]
			ta, cmd = ta.Update(msg)
			s.questionTextareas[s.questionCursor] = ta
		} else {
			s.globalTextarea, cmd = s.globalTextarea.Update(msg)
		}
		s.SyncViewports()
		return s, cmd
	}

	// List Navigation
	switch msg.Code {
	case tea.KeyUp:
		s.questionCursor = max(0, s.questionCursor-1)
	case tea.KeyDown:
		s.questionCursor = min(len(opts)-1, s.questionCursor+1)
	}

	switch msg.String() {
	case "k":
		s.questionCursor = max(0, s.questionCursor-1)
	case "j":
		s.questionCursor = min(len(opts)-1, s.questionCursor+1)
	case " ":
		if s.userQuestion.MultiSelect {
			s.questionSelected[s.questionCursor] = !s.questionSelected[s.questionCursor]
		} else {
			// Single select: clear all, select current
			for k := range s.questionSelected {
				delete(s.questionSelected, k)
			}
			s.questionSelected[s.questionCursor] = true
		}
	}
	s.SyncViewports()
	return s, nil
}
```

## 3. Render View (`viewUserQuestion`)

Modify the list rendering to display native radios/checkboxes and conditionally print the assigned custom `textarea.Model` horizontally indented for visually injected rendering.

```go
func (s PipelineScreen) viewUserQuestion(_ int) string {
	var b strings.Builder
	q := s.userQuestion

	b.WriteString(fmt.Sprintf(" %s asks:\n\n", phaseStyle.Render(q.Question)))

	if len(q.Options) == 0 {
		b.WriteString(fmt.Sprintf("   %s\n", s.globalTextarea.View()))
		return b.String()
	}

	allowCustom := true
	if q.AllowCustom != nil {
		allowCustom = *q.AllowCustom
	}

	for i, opt := range q.Options {
		isCursor := i == s.questionCursor
		isSelected := s.questionSelected[i]

		cursorMarker := "  "
		if isCursor {
			if s.focusInTextarea {
				cursorMarker = goalStyle.Render("▶ ")  // Active typing state
			} else {
				cursorMarker = phaseStyle.Render("▶ ") // Navigation state
			}
		}

		var selMarker string
		if q.MultiSelect {
			if isSelected {
				selMarker = passStyle.Render("[x] ")
			} else {
				selMarker = dimStyle.Render("[ ] ")
			}
		} else {
			if isSelected {
				selMarker = passStyle.Render("(*) ")
			} else {
				selMarker = dimStyle.Render("( ) ")
			}
		}

		label := opt.Label
		if isCursor && !s.focusInTextarea {
			label = goalStyle.Render(opt.Label)
		} else if isCursor && s.focusInTextarea {
			label = dimStyle.Render(opt.Label)
		}

		b.WriteString(fmt.Sprintf("%s%s%s", cursorMarker, selMarker, label))
		if opt.Hint != "" {
			b.WriteString(fmt.Sprintf("  %s", dimStyle.Render(opt.Hint)))
		}
		b.WriteString("\n")

		// Conditionally render contextual textarea inline
		if allowCustom {
			ta := s.questionTextareas[i]
			if isSelected || ta.Value() != "" || (isCursor && s.focusInTextarea) {
				taStr := ta.View()
				for _, line := range strings.Split(taStr, "\n") {
					b.WriteString(fmt.Sprintf("       %s\n", line))
				}
			}
		}
	}

	return b.String()
}
```

## 4. Hydration Code (`buildQuestionAnswer`)

Aggregate answers logically, including auto-selecting items if custom text is provided without toggling the checkbox.

```go
func (s PipelineScreen) buildQuestionAnswer() harness.MCPAnswer {
	if len(s.userQuestion.Options) == 0 {
		return harness.MCPAnswer{
			FreeformText: strings.TrimSpace(s.globalTextarea.Value()),
		}
	}

	var selected []int
	customTexts := make(map[int]string)

	allowCustom := true
	if s.userQuestion.AllowCustom != nil {
		allowCustom = *s.userQuestion.AllowCustom
	}

	for i := range s.userQuestion.Options {
		isSelected := s.questionSelected[i]

		if allowCustom {
			val := strings.TrimSpace(s.questionTextareas[i].Value())
			if val != "" {
				customTexts[i] = val
				// Provide QoL checkmark leniency if text is supplied
				if !isSelected {
					isSelected = true
				}
			}
		}

		if isSelected {
			selected = append(selected, i)
		}
	}

	// Single select fallback if nothing checked
	if len(selected) == 0 && !s.userQuestion.MultiSelect && len(s.userQuestion.Options) > 0 {
		selected = []int{s.questionCursor}
	}

	return harness.MCPAnswer{
		SelectedIndices: selected,
		CustomTexts:     customTexts,
	}
}
```

## 5. UI Test Stubs (`internal/tui/app_smoke_test.go` and `internal/harness/mcp_server_test.go`)

### `internal/tui/app_smoke_test.go`

Inject into `hydratedModels`:

```go
	// StatePipeline + ContentUserQuestion (Checkbox multi-select & custom text)
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentUserQuestion
		allowCustom := true
		m.pipelineScreen.userQuestion = harness.MCPToolCall{
			Question:    "Which features should we include?",
			Options:     []harness.MCPToolOption{{Label: "Auth"}, {Label: "Database", Hint: "Postgres"}},
			MultiSelect: true,
			AllowCustom: &allowCustom,
		}
		m.pipelineScreen.questionSelected = map[int]bool{0: true}
		m.pipelineScreen.questionCursor = 1
		m.pipelineScreen.focusInTextarea = true
		
		m.pipelineScreen.questionTextareas = make(map[int]textarea.Model)
		ta0 := textarea.New()
		m.pipelineScreen.questionTextareas[0] = ta0
		ta1 := textarea.New()
		ta1.SetValue("Prefer Neon DB for edge.")
		ta1.Focus()
		m.pipelineScreen.questionTextareas[1] = ta1
		
		models["pipeline-user-question-multiselect-custom"] = m
	}

	// StatePipeline + ContentUserQuestion (Radio single-select)
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentUserQuestion
		allowCustom := false
		m.pipelineScreen.userQuestion = harness.MCPToolCall{
			Question:    "Target environment?",
			Options:     []harness.MCPToolOption{{Label: "Staging"}, {Label: "Production"}},
			MultiSelect: false,
			AllowCustom: &allowCustom,
		}
		m.pipelineScreen.questionSelected = map[int]bool{1: true}
		m.pipelineScreen.questionCursor = 0
		models["pipeline-user-question-singleselect"] = m
	}
```

### `internal/harness/mcp_server_test.go`

Append this test case validating `FormatAnswer` logic with custom text:

```go
func TestFormatAnswer_MultiSelectWithCustomTexts(t *testing.T) {
	tc := MCPToolCall{
		Question:    "Configure resources?",
		MultiSelect: true,
		Options: []MCPToolOption{
			{Label: "Redis"},
			{Label: "RabbitMQ"},
		},
	}
	ans := MCPAnswer{
		SelectedIndices: []int{0, 1},
		CustomTexts:     map[int]string{0: "Use version 7.x"},
	}
	got := FormatAnswer(tc, ans)
	if !strings.Contains(got, "Selected (2 of 2):") {
		t.Errorf("expected multi-select text, got %q", got)
	}
	if !strings.Contains(got, "- Redis\n  Context: Use version 7.x") {
		t.Errorf("missing custom context for Redis, got %q", got)
	}
	if !strings.Contains(got, "- RabbitMQ") {
		t.Errorf("missing selection for RabbitMQ, got %q", got)
	}
}
```
