package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"

	"github.com/xiii/orqestra/internal/testutil"
	"github.com/xiii/orqestra/internal/types"
)

func TestE2E_HappyPath(t *testing.T) {
	// 1. Stand up Mock LLM Provider
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(testutil.Data.LLMMock)
	})
	
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 2. Build Pipeline
	pl := PipelineFuncs{
		RecognizeIntent: func(_ context.Context, p string) (IntentResult, error) {
			// Simulating LLM decoding using fixtures
			res := testutil.Data.Responses["intent_accept"]
			return IntentResult{
				Verdict:   res["verdict"].(string),
				Rephrased: res["rephrased"].(string),
				EndState:  res["end_state"].(string),
			}, nil
		},
		Plan: func(_ context.Context, _ string, _ io.Writer) (types.Specification, error) {
			return testutil.Data.Spec, nil
		},
		Execute: func(_ context.Context, _ types.Specification, _ io.Writer) error {
			return nil
		},
	}

	m := NewModel(pl)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 40))
	tm.Send(setProgramMsg{program: tm.GetProgram()})

	// Step 1: Submit happy path prompt
	tm.Send(PromptSubmitMsg{Prompt: testutil.Data.Prompts["happy_path"]})
	time.Sleep(200 * time.Millisecond)

	// Step 2: Approve the plan
	tm.Type("y")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	time.Sleep(200 * time.Millisecond)

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	finalModel := tm.FinalModel(t).(Model)
	assert.Equal(t, StateIdle, finalModel.state)
	
	// Ensure the view is correct and doesn't panic
	teatest.RequireEqualOutput(t, []byte(finalModel.View()))
}
