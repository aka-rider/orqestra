//go:build darwin

package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCodeCLI_ModelRequired(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode")
	if c.model != "" {
		t.Fatal("expected empty model by default")
	}

	// validateModel should fail with empty model.
	err := c.validateModel()
	if err == nil {
		t.Fatal("expected error for empty model")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error should mention 'model is required', got: %v", err)
	}
}

func TestOpenCodeCLI_ModelValid(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode", WithOpenCodeModel("llama/qwen3.6-coder"))
	if c.model != "llama/qwen3.6-coder" {
		t.Fatalf("model = %q, want %q", c.model, "llama/qwen3.6-coder")
	}

	err := c.validateModel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenCodeCLI_BuildArgs_ModelRequired(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode", WithOpenCodeModel("llama/qwen3.6-coder"))
	args := c.buildArgs("hello")

	// Should NOT contain -p (wrong flag)
	for _, a := range args {
		if a == "-p" {
			t.Errorf("buildArgs should not contain -p, got: %v", args)
		}
	}

	// Should contain --format json
	found := false
	for i, a := range args {
		if a == "--format" && i+1 < len(args) && args[i+1] == "json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs should contain --format json, got: %v", args)
	}

	// Should contain --dangerously-skip-permissions
	found = false
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs should contain --dangerously-skip-permissions, got: %v", args)
	}

	// Should contain -m llama/qwen3.6-coder
	found = false
	for i, a := range args {
		if a == "-m" && i+1 < len(args) && args[i+1] == "llama/qwen3.6-coder" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs should contain -m llama/qwen3.6-coder, got: %v", args)
	}

	// Should end with the prompt as positional arg
	if args[len(args)-1] != "hello" {
		t.Errorf("last arg should be 'hello', got: %v", args[len(args)-1])
	}
}

func TestOpenCodeCLI_BuildArgs_WithAgent(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode",
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodeAgent("plan"),
	)
	args := c.buildArgs("write a plan")

	found := false
	for i, a := range args {
		if a == "--agent" && i+1 < len(args) && args[i+1] == "plan" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs should contain --agent plan, got: %v", args)
	}
}

func TestOpenCodeCLI_BuildArgs_WithPure(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode",
		WithOpenCodeModel("llama/qwen3.6-coder"),
		WithOpenCodePure(true),
	)
	args := c.buildArgs("hello")

	found := false
	for _, a := range args {
		if a == "--pure" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs should contain --pure, got: %v", args)
	}
}

func TestOpenCodeCLI_BuildArgs_WithSession(t *testing.T) {
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode",
		WithOpenCodeModel("llama/qwen3.6-coder"),
	)
	args := c.buildArgsWithSession("continue", "ses_abc123")

	found := false
	for i, a := range args {
		if a == "-s" && i+1 < len(args) && args[i+1] == "ses_abc123" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgsWithSession should contain -s ses_abc123, got: %v", args)
	}
}

func TestOpenCodeCLI_BuildArgs_NoSystemPrompt(t *testing.T) {
	// The old implementation had --append-system-prompt. Verify it's gone.
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode",
		WithOpenCodeModel("llama/qwen3.6-coder"),
	)
	args := c.buildArgs("hello")

	for _, a := range args {
		if a == "--append-system-prompt" {
			t.Errorf("buildArgs should not contain --append-system-prompt, got: %v", args)
		}
	}
	for _, a := range args {
		if a == "--print" {
			t.Errorf("buildArgs should not contain --print, got: %v", args)
		}
	}
	for _, a := range args {
		if a == "-p" {
			t.Errorf("buildArgs should not contain -p, got: %v", args)
		}
	}
	for _, a := range args {
		if a == "--output-format" {
			t.Errorf("buildArgs should not contain --output-format, got: %v", args)
		}
	}
}

func TestOpenCodeCLI_BuildArgs_NoExtraFormatFlag(t *testing.T) {
	// Verify --format json is not duplicated.
	c := NewOpenCodeCLI("/opt/homebrew/bin/opencode",
		WithOpenCodeModel("llama/qwen3.6-coder"),
	)
	args := c.buildArgs("hello")

	count := 0
	for _, a := range args {
		if a == "--format" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("buildArgs should contain exactly one --format, got %d", count)
	}
}

// --- NDJSON parser tests ---

func TestParseOpencodeStream_stepStart(t *testing.T) {
	line := `{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"id":"s1","sessionID":"ses_1","messageID":"m1","type":"step-start"}}`
	events := make(chan StreamUpdate, 10)
	raw, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty raw output")
	}

	select {
	case e := <-events:
		if e.Text != "step_start" {
			t.Errorf("expected Text=step_start, got Text=%q Detail=%q", e.Text, e.Detail)
		}
	default:
		t.Fatal("expected a StreamUpdate event")
	}
}

func TestParseOpencodeStream_text(t *testing.T) {
	line := `{"type":"text","timestamp":1,"sessionID":"ses_1","part":{"id":"t1","sessionID":"ses_1","messageID":"m1","type":"text","text":"hello world","time":{"start":1}}}`
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if e.Text != "hello world" {
			t.Errorf("expected Text=hello world, got Text=%q", e.Text)
		}
	default:
		t.Fatal("expected a StreamUpdate event with text")
	}
}

func TestParseOpencodeStream_toolUse(t *testing.T) {
	line := `{"type":"tool_use","timestamp":1,"sessionID":"ses_1","part":{"id":"tu1","sessionID":"ses_1","messageID":"m1","type":"tool","tool":"Bash","callID":"c1","state":{"status":"completed","title":"Running bash","input":{},"output":"done"}}}`
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if e.Tool != "Bash" {
			t.Errorf("expected Tool=Bash, got Tool=%q", e.Tool)
		}
		if e.Detail != "Running bash" {
			t.Errorf("expected Detail=Running bash, got Detail=%q", e.Detail)
		}
	default:
		t.Fatal("expected a StreamUpdate event with Tool")
	}
}

func TestParseOpencodeStream_stepFinish(t *testing.T) {
	line := `{"type":"step_finish","timestamp":1,"sessionID":"ses_1","part":{"id":"sf1","sessionID":"ses_1","reason":"end_turn","type":"step-finish","tokens":{"total":100,"input":50,"output":50,"reasoning":0,"cache_read":0,"cache_write":0},"cost":0.001}}`
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if !e.UsageValid {
			t.Error("expected UsageValid=true")
		}
		if e.Input != 50 {
			t.Errorf("expected Input=50, got Input=%d", e.Input)
		}
		if e.Output != 50 {
			t.Errorf("expected Output=50, got Output=%d", e.Output)
		}
	default:
		t.Fatal("expected a StreamUpdate event with usage")
	}
}

func TestParseOpencodeStream_reasoning(t *testing.T) {
	line := `{"type":"reasoning","timestamp":1,"sessionID":"ses_1","part":{"id":"r1","sessionID":"ses_1","messageID":"m1","type":"reasoning","text":"let me think about this","time":{"start":1}}}`
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if e.Text != "let me think about this" {
			t.Errorf("expected Text=let me think about this, got Text=%q", e.Text)
		}
	default:
		t.Fatal("expected a StreamUpdate event with reasoning text")
	}
}

func TestParseOpencodeStream_error(t *testing.T) {
	line := `{"type":"error","timestamp":1,"error":{"name":"ProviderError","message":"API key invalid"}}`
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader(line), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if !strings.Contains(e.Detail, "ProviderError") {
			t.Errorf("expected Detail to contain 'ProviderError', got: %q", e.Detail)
		}
	default:
		t.Fatal("expected a StreamUpdate event with error detail")
	}
}

func TestParseOpencodeStream_sessionIDTracking(t *testing.T) {
	textLine := `{"type":"message.part.updated","timestamp":1,"sessionID":"ses_xyz","part":{"id":"t1","sessionID":"ses_xyz","messageID":"m1","type":"text","text":"hi","time":{"start":1}}}`
	events := make(chan StreamUpdate, 10)
	raw, err := parseOpencodeStream(strings.NewReader(textLine), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sid := extractOpencodeSessionID(raw)
	if sid != "ses_xyz" {
		t.Errorf("extractOpencodeSessionID = %q, want %q", sid, "ses_xyz")
	}
}

func TestParseOpencodeStream_multipleLines(t *testing.T) {
	input := `{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"id":"s1","sessionID":"ses_1","messageID":"m1","type":"step-start"}}
{"type":"text","timestamp":2,"sessionID":"ses_1","part":{"id":"t1","sessionID":"ses_1","messageID":"m1","type":"text","text":"hello","time":{"start":2}}}
{"type":"step_finish","timestamp":3,"sessionID":"ses_1","part":{"id":"sf1","sessionID":"ses_1","reason":"end_turn","type":"step-finish","tokens":{"total":100,"input":50,"output":50,"reasoning":0,"cache_read":0,"cache_write":0},"cost":0.001}}`
	events := make(chan StreamUpdate, 10)
	raw, err := parseOpencodeStream(strings.NewReader(input), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if raw == "" {
		t.Fatal("expected non-empty raw output")
	}

	// Collect all events.
	var updates []StreamUpdate
	close(events)
	for e := range events {
		updates = append(updates, e)
	}

	// Should have at least 3 events: step_start, text, token_usage.
	if len(updates) < 3 {
		t.Errorf("expected >= 3 events, got %d: %v", len(updates), updates)
	}

	// Verify session ID extraction.
	sid := extractOpencodeSessionID(raw)
	if sid != "ses_1" {
		t.Errorf("extractOpencodeSessionID = %q, want %q", sid, "ses_1")
	}

	// Verify usage extraction.
	usage := extractOpencodeUsage(raw)
	if usage.Input != 50 || usage.Output != 50 {
		t.Errorf("extractOpencodeUsage = %+v, want Input=50 Output=50", usage)
	}

	// Verify result extraction.
	result := extractOpencodeResult(raw)
	if result != "hello" {
		t.Errorf("extractOpencodeResult = %q, want %q", result, "hello")
	}
}

func TestParseOpencodeStream_nonJSONLine(t *testing.T) {
	events := make(chan StreamUpdate, 10)
	_, err := parseOpencodeStream(strings.NewReader("not json\n"), events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case e := <-events:
		if !strings.Contains(e.Text, "not json") {
			t.Errorf("expected Text to contain 'not json', got: %q", e.Text)
		}
	default:
		t.Fatal("expected a StreamUpdate event for non-JSON line")
	}
}

// --- Result/usage extraction tests ---

func TestExtractOpencodeResult(t *testing.T) {
	input := `{"type":"text","sessionID":"s","part":{"type":"text","text":"hello "}}
{"type":"text","sessionID":"s","part":{"type":"text","text":"world"}}
{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","tokens":{"input":10,"output":5}}}`
	result := extractOpencodeResult(input)
	if result != "hello world" {
		t.Errorf("extractOpencodeResult = %q, want %q", result, "hello world")
	}
}

func TestExtractOpencodeUsage(t *testing.T) {
	input := `{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","tokens":{"input":10,"output":5}}}
{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","tokens":{"input":20,"output":15}}}`
	usage := extractOpencodeUsage(input)
	// Should return the LAST step_finish.
	if usage.Input != 20 || usage.Output != 15 {
		t.Errorf("extractOpencodeUsage = %+v, want Input=20 Output=15", usage)
	}
}

func TestExtractOpencodeUsage_empty(t *testing.T) {
	usage := extractOpencodeUsage(`{"type":"text","part":{"type":"text","text":"hi"}}`)
	if usage.Input != 0 || usage.Output != 0 {
		t.Errorf("extractOpencodeUsage on non-step event = %+v, want zero", usage)
	}
}

func TestExtractOpencodeSessionID(t *testing.T) {
	input := `{"type":"step_start","sessionID":"ses_abc","part":{"type":"step-start"}}
{"type":"message.part.updated","sessionID":"ses_abc","part":{"type":"text","text":"hi"}}`
	sid := extractOpencodeSessionID(input)
	if sid != "ses_abc" {
		t.Errorf("extractOpencodeSessionID = %q, want %q", sid, "ses_abc")
	}
}

func TestExtractOpencodeSessionID_empty(t *testing.T) {
	sid := extractOpencodeSessionID(`{"type":"step_start","part":{"type":"step-start"}}`)
	if sid != "" {
		t.Errorf("extractOpencodeSessionID = %q, want empty", sid)
	}
}

func TestExtractOpencodeJSONUsage(t *testing.T) {
	input := `{"usage":{"input_tokens":100,"output_tokens":50}}`
	usage := extractOpencodeJSONUsage(input)
	if usage.Input != 100 || usage.Output != 50 {
		t.Errorf("extractOpencodeJSONUsage = %+v, want Input=100 Output=50", usage)
	}
}

func TestExtractOpencodeJSONUsage_empty(t *testing.T) {
	usage := extractOpencodeJSONUsage(`{"text":"hello"}`)
	if usage.Input != 0 || usage.Output != 0 {
		t.Errorf("extractOpencodeJSONUsage on non-usage = %+v, want zero", usage)
	}
}

// --- OpencodeEvent struct validation ---

func TestOpencodeEvent_JSONRoundTrip(t *testing.T) {
	// Verify the opencodeEvent struct can correctly parse a real event.
	textEvent := `{"type":"message.part.updated","timestamp":1700000000,"sessionID":"ses_test","part":{"id":"p1","sessionID":"ses_test","messageID":"m1","type":"text","text":"test output","time":{"start":1700000000}}}`
	var evt opencodeEvent
	if err := json.Unmarshal([]byte(textEvent), &evt); err != nil {
		t.Fatalf("unmarshal text event: %v", err)
	}
	if evt.Type != "message.part.updated" {
		t.Errorf("Type = %q, want %q", evt.Type, "message.part.updated")
	}
	if evt.Part.PartType != "text" {
		t.Errorf("PartType = %q, want %q", evt.Part.PartType, "text")
	}
	if evt.Part.Text != "test output" {
		t.Errorf("Part.Text = %q, want %q", evt.Part.Text, "test output")
	}
	if evt.SessionID != "ses_test" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "ses_test")
	}
}

func TestOpencodeEvent_stepFinishRoundTrip(t *testing.T) {
	stepFinish := `{"type":"step_finish","timestamp":1700000001,"sessionID":"ses_test","part":{"id":"sf1","sessionID":"ses_test","reason":"end_turn","type":"step-finish","tokens":{"total":200,"input":100,"output":100,"reasoning":0,"cache_read":0,"cache_write":0},"cost":0.002}}`
	var evt opencodeEvent
	if err := json.Unmarshal([]byte(stepFinish), &evt); err != nil {
		t.Fatalf("unmarshal step_finish: %v", err)
	}
	if evt.Type != "step_finish" {
		t.Errorf("Type = %q, want %q", evt.Type, "step_finish")
	}
	if evt.Part.Tokens == nil {
		t.Fatal("Part.Tokens should not be nil")
	}
	if evt.Part.Tokens.Input != 100 || evt.Part.Tokens.Output != 100 {
		t.Errorf("Part.Tokens = %+v, want Input=100 Output=100", evt.Part.Tokens)
	}
}

func TestOpencodeEvent_toolUseRoundTrip(t *testing.T) {
	toolEvent := `{"type":"tool_use","timestamp":1700000000,"sessionID":"ses_test","part":{"id":"tu1","sessionID":"ses_test","messageID":"m1","type":"tool","tool":"Write","callID":"call1","state":{"status":"running","title":"Writing file","input":{"path":"/tmp/test.txt"},"output":null}}}`
	var evt opencodeEvent
	if err := json.Unmarshal([]byte(toolEvent), &evt); err != nil {
		t.Fatalf("unmarshal tool event: %v", err)
	}
	if evt.Part.PartType != "tool" {
		t.Errorf("PartType = %q, want %q", evt.Part.PartType, "tool")
	}
	if evt.Part.Tool != "Write" {
		t.Errorf("Part.Tool = %q, want %q", evt.Part.Tool, "Write")
	}
	if evt.Part.ToolState == nil || evt.Part.ToolState.Title != "Writing file" {
		t.Errorf("Part.ToolState.Title = %q, want %q", evt.Part.ToolState.Title, "Writing file")
	}
}
