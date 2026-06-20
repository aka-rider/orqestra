package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
)

// --- MCP JSON-RPC 2.0 types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // null for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- MCP Tool Schema (Claude Code AskUserQuestion-compatible) ---

var askUserQuestionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "The question to ask the user. Keep it clear and concise."
    },
    "options": {
      "type": "array",
      "description": "Optional pre-defined answer choices. Omit for freeform text input.",
      "items": {
        "type": "object",
        "properties": {
          "label": {
            "type": "string",
            "description": "The selectable option text."
          },
          "hint": {
            "type": "string",
            "description": "Optional context hint shown alongside the option."
          }
        },
        "required": ["label"]
      }
    },
    "allow_custom": {
      "type": "boolean",
      "description": "Whether the user can provide custom text alongside or instead of selecting options. Defaults to true."
    },
    "multi_select": {
      "type": "boolean",
      "description": "Whether multiple options can be selected simultaneously. Defaults to false."
    }
  },
  "required": ["question"]
}`)

var submitReportSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "report": {
      "type": "string",
      "description": "The full markdown deliverable for this pipeline stage. Pass the complete report — do not truncate."
    },
    "summary": {
      "type": "string",
      "description": "Optional one-sentence summary of the report for logging purposes."
    }
  },
  "required": ["report"]
}`)

// ReportCall is the parsed input from a SubmitReport tools/call invocation.
type ReportCall struct {
	Report  string `json:"report"`
	Summary string `json:"summary,omitempty"`
}

// ToolCall is the parsed input from a tools/call invocation.
type ToolCall struct {
	Question    string       `json:"question"`
	Options     []ToolOption `json:"options,omitempty"`
	AllowCustom *bool        `json:"allow_custom,omitempty"`
	MultiSelect bool         `json:"multi_select,omitempty"`
}

// ToolOption is a single selectable option in a question.
type ToolOption struct {
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// Answer is the answer received back from the question bridge.
type Answer struct {
	SelectedIndices []int          `json:"selected_indices,omitempty"`
	CustomTexts     map[int]string `json:"custom_texts,omitempty"`
	Skipped         bool           `json:"skipped,omitempty"`
	FreeformText    string         `json:"freeform_text,omitempty"`
}

// RunServer starts a minimal MCP JSON-RPC 2.0 server on stdin/stdout.
// It connects to the QuestionBridge via the given Unix socket path.
// agentID identifies which pipeline role this server is serving.
// The server exits cleanly when stdin is closed (MCP lifecycle).
func RunServer(socketPath, agentID string) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			slog.Debug("mcp-bridge: invalid JSON-RPC", "err", err)
			continue
		}

		resp := handleMCPRequest(req, socketPath, agentID)
		if resp == nil {
			continue // notification, no response needed
		}

		out, err := json.Marshal(resp)
		if err != nil {
			slog.Error("mcp-bridge: marshal response", "err", err)
			continue
		}
		fmt.Fprintf(os.Stdout, "%s\n", out)
	}

	return scanner.Err()
}

func handleMCPRequest(req jsonRPCRequest, socketPath, agentID string) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return respondMCP(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "orqestra-bridge",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		})

	case "notifications/initialized":
		return nil // notification, no response

	case "tools/list":
		return respondMCP(req.ID, map[string]any{
			"tools": []map[string]any{
				{
					"name":        "AskUserQuestion",
					"description": "Ask the user a question. Use this when you need clarification, want the user to choose between options, or need any input from the user. The user will see your question in the Orqestra TUI and can respond with text or by selecting from options you provide.",
					"inputSchema": json.RawMessage(askUserQuestionSchema),
				},
				{
					"name":        "SubmitReport",
					"description": "Submit your final markdown deliverable to the Orqestra pipeline. Call this once when your report is complete, passing the full markdown in the \"report\" argument. This is the only reliable delivery channel — do not rely on emitting the report as your final message.",
					"inputSchema": json.RawMessage(submitReportSchema),
				},
			},
		})

	case "tools/call":
		return handleToolCall(req, socketPath, agentID)

	default:
		if req.ID == nil {
			return nil // unknown notification, ignore
		}
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func handleToolCall(req jsonRPCRequest, socketPath, agentID string) *jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params"},
		}
	}

	switch params.Name {
	case "AskUserQuestion":
		return handleAskUserQuestion(req, params.Arguments, socketPath, agentID)
	case "SubmitReport":
		return handleSubmitReport(req, params.Arguments, socketPath, agentID)
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)},
		}
	}
}

func handleAskUserQuestion(req jsonRPCRequest, arguments json.RawMessage, socketPath, agentID string) *jsonRPCResponse {
	var toolCall ToolCall
	if err := json.Unmarshal(arguments, &toolCall); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: fmt.Sprintf("invalid tool arguments: %v", err)},
		}
	}

	if toolCall.Question == "" {
		return respondMCPToolResult(req.ID, true, "Error: question is required")
	}

	answer, err := sendQuestionToBridge(socketPath, agentID, toolCall)
	if err != nil {
		return respondMCPToolResult(req.ID, true, fmt.Sprintf("Error communicating with Orqestra: %v", err))
	}

	text := FormatAnswer(toolCall, answer)
	return respondMCPToolResult(req.ID, false, text)
}

func handleSubmitReport(req jsonRPCRequest, arguments json.RawMessage, socketPath, agentID string) *jsonRPCResponse {
	var call ReportCall
	if err := json.Unmarshal(arguments, &call); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: fmt.Sprintf("invalid tool arguments: %v", err)},
		}
	}
	if call.Report == "" {
		return respondMCPToolResult(req.ID, true, "Error: report is required and must not be empty")
	}
	if err := sendReportToBridge(socketPath, agentID, call); err != nil {
		return respondMCPToolResult(req.ID, true, fmt.Sprintf("Error delivering report to Orqestra: %v", err))
	}
	return respondMCPToolResult(req.ID, false, "Report submitted successfully.")
}

func sendReportToBridge(socketPath, agentID string, call ReportCall) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial bridge socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	payload, err := json.Marshal(ReportSubmission{Report: call.Report, Summary: call.Summary})
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	env := bridgeEnvelope{Kind: "report", AgentID: agentID, Payload: payload}
	envData, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal report envelope: %w", err)
	}
	if err := writeFrame(conn, envData); err != nil {
		return fmt.Errorf("write report frame: %w", err)
	}

	ackData, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("read report ack: %w", err)
	}
	var ack struct{ OK bool `json:"ok"` }
	if err := json.Unmarshal(ackData, &ack); err != nil {
		return fmt.Errorf("unmarshal report ack: %w", err)
	}
	if !ack.OK {
		return fmt.Errorf("bridge rejected report submission")
	}
	return nil
}

func sendQuestionToBridge(socketPath, agentID string, toolCall ToolCall) (Answer, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return Answer{}, fmt.Errorf("dial bridge socket: %w", err)
	}
	defer conn.Close()

	payload, err := json.Marshal(toolCall)
	if err != nil {
		return Answer{}, fmt.Errorf("marshal question: %w", err)
	}

	env := bridgeEnvelope{Kind: "question", AgentID: agentID, Payload: payload}
	envData, err := json.Marshal(env)
	if err != nil {
		return Answer{}, fmt.Errorf("marshal envelope: %w", err)
	}
	if err := writeFrame(conn, envData); err != nil {
		return Answer{}, fmt.Errorf("write question: %w", err)
	}

	answerData, err := readFrame(conn)
	if err != nil {
		return Answer{}, fmt.Errorf("read answer: %w", err)
	}

	var answer Answer
	if err := json.Unmarshal(answerData, &answer); err != nil {
		return Answer{}, fmt.Errorf("unmarshal answer: %w", err)
	}
	return answer, nil
}

// FormatAnswer converts an Answer to a human-readable text tool result.
func FormatAnswer(toolCall ToolCall, answer Answer) string {
	if answer.Skipped {
		return "The user explicitly skipped this question. Proceed with your best judgment based on the codebase evidence you've gathered."
	}

	if len(toolCall.Options) == 0 {
		if answer.FreeformText != "" {
			return fmt.Sprintf("User's answer: %s", answer.FreeformText)
		}
		return "User confirmed without providing additional input. Proceed with your best judgment."
	}

	if !toolCall.MultiSelect {
		if len(answer.SelectedIndices) == 0 {
			if answer.FreeformText != "" {
				return fmt.Sprintf("User's answer: %s", answer.FreeformText)
			}
			return "User confirmed without selecting any option. Proceed with your best judgment."
		}
		idx := answer.SelectedIndices[0]
		if idx < 0 || idx >= len(toolCall.Options) {
			return "User confirmed without selecting any option. Proceed with your best judgment."
		}
		label := toolCall.Options[idx].Label
		if custom, ok := answer.CustomTexts[idx]; ok && custom != "" {
			return fmt.Sprintf("Selected: %s\n  Context: %s", label, custom)
		}
		return fmt.Sprintf("Selected: %s", label)
	}

	selected := answer.SelectedIndices
	if len(selected) == 0 {
		if answer.FreeformText != "" {
			return fmt.Sprintf("User's answer: %s", answer.FreeformText)
		}
		return "User confirmed without selecting any option. Proceed with your best judgment."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Selected (%d of %d):\n", len(selected), len(toolCall.Options))
	for _, idx := range selected {
		if idx < 0 || idx >= len(toolCall.Options) {
			continue
		}
		fmt.Fprintf(&b, "- %s\n", toolCall.Options[idx].Label)
		if custom, ok := answer.CustomTexts[idx]; ok && custom != "" {
			fmt.Fprintf(&b, "  Context: %s\n", custom)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func respondMCP(id json.RawMessage, result any) *jsonRPCResponse {
	data, err := json.Marshal(result)
	if err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &jsonRPCError{Code: -32603, Message: fmt.Sprintf("marshal result: %v", err)},
		}
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  data,
	}
}

func respondMCPToolResult(id json.RawMessage, isError bool, text string) *jsonRPCResponse {
	content := []map[string]string{{"type": "text", "text": text}}
	result := map[string]any{
		"content": content,
	}
	if isError {
		result["isError"] = true
	}
	data, err := json.Marshal(result)
	if err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &jsonRPCError{Code: -32603, Message: fmt.Sprintf("marshal tool result: %v", err)},
		}
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  data,
	}
}
