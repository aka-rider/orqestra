package validator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/types"
)

// wrapInOpenAIEnvelope wraps a content string in an OpenAI chat completion response envelope.
func wrapInOpenAIEnvelope(content string) string {
	resp := completionResponse{
		Choices: []completionResponseChoice{
			{Message: completionMessage{Content: content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// marshalValidationResult serializes a ValidationResult for use as LLM content.
func marshalValidationResult(r types.ValidationResult) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func testSpec(criteria ...string) types.Specification {
	return types.Specification{
		Goal:       "test goal",
		Steps:      []string{"step 1"},
		Acceptance: criteria,
	}
}

// TestValidator_AllCriteriaMet — (a) all acceptance criteria met → Passed=true, FailedCriteria empty.
func TestValidator_AllCriteriaMet(t *testing.T) {
	result := types.ValidationResult{Passed: true, Score: 1.0}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(wrapInOpenAIEnvelope(marshalValidationResult(result))))
	}))
	defer srv.Close()

	v, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := v.Validate(context.Background(),
		testSpec("file.txt exists", "tests pass"),
		types.WorkOutput{Stdout: "Created file.txt\nAll tests passed\n"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Passed {
		t.Error("expected Passed=true")
	}
	if len(got.FailedCriteria) != 0 {
		t.Errorf("expected empty FailedCriteria, got %d", len(got.FailedCriteria))
	}
}

// TestValidator_OneCriterionUnmet — (b) one criterion unmet → Passed=false, FailedCriteria non-empty with Reason.
func TestValidator_OneCriterionUnmet(t *testing.T) {
	result := types.ValidationResult{
		Passed: false,
		Score:  0.5,
		FailedCriteria: []types.FailedCriterion{
			{Criterion: "tests pass", Reason: "go test exited with code 1"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(wrapInOpenAIEnvelope(marshalValidationResult(result))))
	}))
	defer srv.Close()

	v, _ := New(srv.URL)
	got, err := v.Validate(context.Background(),
		testSpec("file.txt exists", "tests pass"),
		types.WorkOutput{Stdout: "FAIL\n", ExitCode: 1},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Passed {
		t.Error("expected Passed=false")
	}
	if len(got.FailedCriteria) != 1 {
		t.Fatalf("expected 1 failed criterion, got %d", len(got.FailedCriteria))
	}
	if got.FailedCriteria[0].Criterion != "tests pass" {
		t.Errorf("unexpected criterion: %q", got.FailedCriteria[0].Criterion)
	}
	if got.FailedCriteria[0].Reason == "" {
		t.Error("expected non-empty Reason")
	}
}

// TestValidator_MalformedJSON — (c) llama-server returns malformed JSON → explicit error, no partial state.
func TestValidator_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Valid OpenAI envelope, but content is not JSON.
		w.Write([]byte(wrapInOpenAIEnvelope("not json at all {{{")))
	}))
	defer srv.Close()

	v, _ := New(srv.URL)
	_, err := v.Validate(context.Background(),
		testSpec("tests pass"),
		types.WorkOutput{Stdout: "done"},
	)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// TestValidator_HTTP5xx — (d) llama-server returns HTTP 5xx → wrapped error with status code.
func TestValidator_HTTP5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	v, _ := New(srv.URL)
	_, err := v.Validate(context.Background(),
		testSpec("tests pass"),
		types.WorkOutput{Stdout: "done"},
	)
	if err == nil {
		t.Fatal("expected error for HTTP 5xx, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain status code 500, got: %v", err)
	}
}

// TestValidator_EmptyAcceptanceCriteria — (e) empty acceptance criteria → specific error.
func TestValidator_EmptyAcceptanceCriteria(t *testing.T) {
	// No HTTP server needed — the validator returns before making any network call.
	v, err := New("http://127.0.0.1:19999") // port chosen to avoid accidental connection
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = v.Validate(context.Background(),
		types.Specification{Goal: "test", Steps: []string{"step"}},
		types.WorkOutput{Stdout: "done"},
	)
	if err == nil {
		t.Fatal("expected error for empty acceptance criteria, got nil")
	}
	const want = "spec has no acceptance criteria — validation contract is empty"
	if err.Error() != want {
		t.Errorf("expected error %q\ngot              %q", want, err.Error())
	}
}

// TestValidator_ExitCodeAndStderrInPrompt — (f) non-zero ExitCode and Stderr must appear in the request.
func TestValidator_ExitCodeAndStderrInPrompt(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		result := types.ValidationResult{Passed: true, Score: 1.0}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(wrapInOpenAIEnvelope(marshalValidationResult(result))))
	}))
	defer srv.Close()

	v, _ := New(srv.URL)
	_, err := v.Validate(context.Background(),
		testSpec("binary exits cleanly"),
		types.WorkOutput{
			Stdout:   "processing complete",
			Stderr:   "WARNING: deprecated API called",
			ExitCode: 2,
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedBody == nil {
		t.Fatal("no request body captured")
	}

	body := string(capturedBody)
	// The user message is JSON-encoded as part of the request body.
	// "Exit code: 2" appears verbatim in the message content.
	if !strings.Contains(body, "Exit code: 2") {
		t.Errorf("expected request body to contain 'Exit code: 2', body snippet: %.300s", body)
	}
	if !strings.Contains(body, "WARNING: deprecated API called") {
		t.Errorf("expected request body to contain stderr content, body snippet: %.300s", body)
	}
}
