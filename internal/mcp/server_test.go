package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testSock = "/tmp/test.sock"

func TestHandleMCPRequest_Initialize(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	}
	resp := handleMCPRequest(req, testSock, "researcher")
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("missing capabilities")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("missing tools in capabilities")
	}
}

func TestHandleMCPRequest_ToolsList(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	}
	resp := handleMCPRequest(req, testSock, "researcher")
	if resp == nil {
		t.Fatal("expected response")
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}

	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"AskUserQuestion", "SubmitReport"} {
		if !names[want] {
			t.Errorf("missing tool %q in tools/list", want)
		}
	}

	// AskUserQuestion schema still valid
	var askSchema json.RawMessage
	var submitSchema json.RawMessage
	for _, tool := range result.Tools {
		switch tool.Name {
		case "AskUserQuestion":
			askSchema = tool.InputSchema
		case "SubmitReport":
			submitSchema = tool.InputSchema
		}
	}

	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(askSchema, &schema); err != nil {
		t.Fatalf("unmarshal AskUserQuestion schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "question" {
		t.Errorf("AskUserQuestion required = %v, want [question]", schema.Required)
	}
	for _, key := range []string{"question", "options", "allow_custom", "multi_select"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("missing property %q in AskUserQuestion schema", key)
		}
	}

	// SubmitReport schema: required "report", optional "summary"
	var submitSchemaObj struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(submitSchema, &submitSchemaObj); err != nil {
		t.Fatalf("unmarshal SubmitReport schema: %v", err)
	}
	if len(submitSchemaObj.Required) != 1 || submitSchemaObj.Required[0] != "report" {
		t.Errorf("SubmitReport required = %v, want [report]", submitSchemaObj.Required)
	}
	for _, key := range []string{"report", "summary"} {
		if _, ok := submitSchemaObj.Properties[key]; !ok {
			t.Errorf("missing property %q in SubmitReport schema", key)
		}
	}
}

func TestHandleMCPRequest_Notification(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	resp := handleMCPRequest(req, testSock, "researcher")
	if resp != nil {
		t.Error("expected nil for notification, got response")
	}
}

func TestHandleMCPRequest_UnknownMethod(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "bogus/method",
	}
	resp := handleMCPRequest(req, testSock, "researcher")
	if resp == nil {
		t.Fatal("expected error response")
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestAskUserQuestion(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-ask-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		data, err := readFrame(conn)
		if err != nil {
			done <- err
			return
		}
		var env bridgeEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			done <- err
			return
		}
		if env.Kind != "question" {
			done <- fmt.Errorf("expected kind=question, got %q", env.Kind)
			return
		}
		answer := Answer{FreeformText: "42"}
		ansData, _ := json.Marshal(answer)
		if err := writeFrame(conn, ansData); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	_ = bridge // keep bridge alive for test

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`6`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"AskUserQuestion","arguments":{"question":"What is the answer?"}}`),
	}
	resp := handleMCPRequest(req, sockPath, "researcher")
	if resp == nil || resp.Error != nil {
		t.Fatalf("AskUserQuestion error: %v", resp)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bridge handler error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for bridge")
	}
	ln.Close()

	// Response should contain the answer
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "User's answer: 42" {
		t.Errorf("result = %+v, want 'User's answer: 42'", result)
	}
}

func TestFormatAnswer_Freeform(t *testing.T) {
	tc := ToolCall{Question: "What color?"}
	ans := Answer{FreeformText: "blue"}
	got := FormatAnswer(tc, ans)
	want := "User's answer: blue"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAnswer_Skipped(t *testing.T) {
	tc := ToolCall{Question: "What color?"}
	ans := Answer{Skipped: true}
	got := FormatAnswer(tc, ans)
	if got == "" {
		t.Error("expected non-empty skip message")
	}
	if !contains(got, "skipped") {
		t.Errorf("expected skip message, got %q", got)
	}
}

func TestFormatAnswer_SingleSelect(t *testing.T) {
	tc := ToolCall{
		Question: "Pick one",
		Options: []ToolOption{
			{Label: "Alpha"},
			{Label: "Beta"},
		},
	}
	ans := Answer{SelectedIndices: []int{1}}
	got := FormatAnswer(tc, ans)
	want := "Selected: Beta"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAnswer_SingleSelectWithCustom(t *testing.T) {
	tc := ToolCall{
		Question: "Pick one",
		Options: []ToolOption{
			{Label: "Alpha"},
			{Label: "Beta"},
		},
	}
	ans := Answer{
		SelectedIndices: []int{0},
		CustomTexts:     map[int]string{0: "with extra sauce"},
	}
	got := FormatAnswer(tc, ans)
	if !contains(got, "Selected: Alpha") || !contains(got, "Context: with extra sauce") {
		t.Errorf("unexpected format: %q", got)
	}
}

func TestFormatAnswer_MultiSelect(t *testing.T) {
	tc := ToolCall{
		Question:    "Pick many",
		MultiSelect: true,
		Options: []ToolOption{
			{Label: "A"},
			{Label: "B"},
			{Label: "C"},
		},
	}
	ans := Answer{SelectedIndices: []int{0, 2}}
	got := FormatAnswer(tc, ans)
	if !contains(got, "Selected (2 of 3)") {
		t.Errorf("unexpected header: %q", got)
	}
	if !contains(got, "- A") || !contains(got, "- C") {
		t.Errorf("missing selections: %q", got)
	}
}

func TestFormatAnswer_NoSelection(t *testing.T) {
	tc := ToolCall{
		Question: "Pick one",
		Options:  []ToolOption{{Label: "Alpha"}},
	}
	ans := Answer{}
	got := FormatAnswer(tc, ans)
	if !contains(got, "best judgment") {
		t.Errorf("expected fallback message, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
