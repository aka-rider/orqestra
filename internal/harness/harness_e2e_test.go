//go:build e2e

package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/tokenlimit"
)

// serverTimings is the llama-server specific timings object returned alongside
// standard OpenAI fields. This is the hardware-level truth.
type serverTimings struct {
	PromptN           int     `json:"prompt_n"`
	PromptMS          float64 `json:"prompt_ms"`
	PromptPerSecond   float64 `json:"prompt_per_second"`
	PredictedN        int     `json:"predicted_n"`
	PredictedMS       float64 `json:"predicted_ms"`
	PredictedPerSec   float64 `json:"predicted_per_second"`
	PredictedPerTokMS float64 `json:"predicted_per_token_ms"`
}

// rawCompletion is the full response including llama-server's timings field.
type rawCompletion struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
	Timings *serverTimings `json:"timings,omitempty"`
}

// queryRaw makes a direct request and returns the full server response including timings.
func queryRaw(ctx context.Context, baseURL, model, prompt, systemPrompt string) (*rawCompletion, time.Duration, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var msgs []msg
	if systemPrompt != "" {
		msgs = append(msgs, msg{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, msg{Role: "user", Content: prompt})

	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   false,
	})

	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var raw rawCompletion
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}
	return &raw, elapsed, nil
}

func e2eBaseURL() string {
	if v := os.Getenv("ORQESTRA_LLM_URL"); v != "" {
		return v
	}
	return "http://192.168.50.212:11434"
}

func e2eModel() string {
	if v := os.Getenv("ORQESTRA_LLM_MODEL"); v != "" {
		return v
	}
	return "qwen3.6"
}

const (
	goldenPrompt = "Count from 1 to 20, one number per line. Output only the numbers, nothing else."
	goldenSystem = "You are a precise instruction follower. Output exactly what is asked, no commentary."
)

// TestE2E_BillingTokenCapture is the critical test: verifies that our Client
// captures EXACTLY the token count the provider reports — because that's what
// they invoice. If this pipeline drops tokens, the killswitch fires too late
// and the user eats a surprise bill.
//
// Chain: server response → Client.Run().Usage.TotalTokens → LimitedRunner.Record() → killswitch
//
// Run: go test ./internal/harness/ -tags e2e -v -run TestE2E_Billing -timeout 120s
func TestE2E_BillingTokenCapture(t *testing.T) {
	baseURL := e2eBaseURL()
	model := e2eModel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("ClientCapturesExactServerUsage", func(t *testing.T) {
		// We intercept a real server response by making one call through Client
		// AND separately parsing the raw JSON to verify field mapping is lossless.
		// Use a proxy approach: make a call, then verify our Client's usage fields
		// equal what the server self-reports.
		//
		// Since the model is non-deterministic, we use a SINGLE call through Client
		// and verify internal consistency: TotalTokens == Input + Output.
		// Then separately verify against a raw call that prompt_tokens is stable.
		client := harness.NewClient(model, nil)
		client.BaseURL = baseURL
		resp, err := client.Run(ctx, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("Client.Run() error: %v", err)
		}

		if resp.Usage == nil {
			t.Fatal("Client.Run() returned nil Usage — billing tokens LOST")
		}

		t.Logf("Client captured: input=%d output=%d total=%d",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)

		// CRITICAL: TotalTokens must equal Input + Output.
		// If the server reports total_tokens, we pass it through directly.
		// If total != input+output, the server's math is inconsistent.
		expectedTotal := resp.Usage.InputTokens + resp.Usage.OutputTokens
		if resp.Usage.TotalTokens != expectedTotal {
			t.Errorf("TotalTokens=%d != Input(%d)+Output(%d)=%d — usage field mismatch",
				resp.Usage.TotalTokens, resp.Usage.InputTokens, resp.Usage.OutputTokens, expectedTotal)
		}

		// OutputTokens must be > 0 (the model always generates tokens).
		if resp.Usage.OutputTokens <= 0 {
			t.Errorf("OutputTokens=%d — billing tokens dropped", resp.Usage.OutputTokens)
		}

		// InputTokens must match raw server (deterministic for same prompt).
		raw, _, err := queryRaw(ctx, baseURL, model, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("raw query error: %v", err)
		}
		if resp.Usage.InputTokens != raw.Usage.PromptTokens {
			t.Errorf("InputTokens: Client=%d vs raw server=%d — prompt token capture broken",
				resp.Usage.InputTokens, raw.Usage.PromptTokens)
		}
		t.Logf("Input tokens (deterministic): client=%d server=%d ✓",
			resp.Usage.InputTokens, raw.Usage.PromptTokens)

		// Verify our token count is in the same order of magnitude as raw
		// (can't be exact since it's a different call with different thinking).
		// This catches gross errors like always returning 0.
		if resp.Usage.OutputTokens < 50 {
			t.Errorf("OutputTokens=%d implausibly low — likely not capturing thinking tokens", resp.Usage.OutputTokens)
		}
	})

	t.Run("KillswitchFiresAtExactBudget", func(t *testing.T) {
		// End-to-end: run a real LLM call through the full killswitch pipeline.
		// Set budget to slightly above one call's worth, verify second call is blocked.
		dbPath := filepath.Join(t.TempDir(), "budget.db")
		store, err := tokenlimit.NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore error: %v", err)
		}
		defer store.Close()

		// First call to discover actual token consumption.
		client := harness.NewClient(model, nil)
		client.BaseURL = baseURL
		resp, err := client.Run(ctx, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("first call error: %v", err)
		}
		if resp.Usage == nil {
			t.Fatal("no usage reported")
		}
		firstCallTokens := resp.Usage.TotalTokens
		t.Logf("First call consumed: %d total tokens", firstCallTokens)

		// Set budget to exactly firstCallTokens — second call must be blocked.
		limits := map[string]int64{model: firstCallTokens}
		limiter := tokenlimit.NewLimiter(store, limits)

		// Record the first call's tokens (simulating the pipeline).
		err = limiter.Record(ctx, model, "test-agent", firstCallTokens)
		// Record may return ErrBudgetExhausted if first call exactly hit budget.
		if err != nil && !tokenlimit.IsBudgetExhausted(err) {
			t.Fatalf("Record error: %v", err)
		}

		// Now Check must block — budget is at or over limit.
		err = limiter.Check(ctx, model, "test-agent")
		if err == nil {
			t.Fatal("Check() should BLOCK after recording firstCallTokens at exact budget")
		}
		if !tokenlimit.IsBudgetExhausted(err) {
			t.Fatalf("expected ErrBudgetExhausted, got: %v", err)
		}
		t.Logf("Killswitch fired correctly at %d/%d tokens", firstCallTokens, firstCallTokens)
	})

	t.Run("BudgetAccountsForEveryToken", func(t *testing.T) {
		// Verify: two calls → budget reflects sum of both.
		// No token is lost in the pipeline.
		dbPath := filepath.Join(t.TempDir(), "budget2.db")
		store, err := tokenlimit.NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore error: %v", err)
		}
		defer store.Close()

		// Set a generous budget so calls succeed.
		limits := map[string]int64{model: 100_000}
		limiter := tokenlimit.NewLimiter(store, limits)

		client := harness.NewClient(model, nil)
		client.BaseURL = baseURL

		var totalRecorded int64
		for i := 0; i < 2; i++ {
			resp, err := client.Run(ctx, goldenPrompt, goldenSystem)
			if err != nil {
				t.Fatalf("call #%d error: %v", i+1, err)
			}
			if resp.Usage == nil {
				t.Fatalf("call #%d: nil usage", i+1)
			}
			tokensThisCall := resp.Usage.TotalTokens
			t.Logf("Call #%d: %d tokens", i+1, tokensThisCall)
			totalRecorded += tokensThisCall

			_ = limiter.Record(ctx, model, "test-agent", tokensThisCall)
		}

		// Verify store has the exact sum.
		stored, err := store.UsageByModel(ctx, model)
		if err != nil {
			t.Fatalf("UsageByModel error: %v", err)
		}
		if stored != totalRecorded {
			t.Errorf("BILLING LEAK: recorded %d but store has %d — %d tokens unaccounted",
				totalRecorded, stored, totalRecorded-stored)
		}
		t.Logf("Store total: %d (matches sum of %d)", stored, totalRecorded)
	})
}

// TestE2E_LocalLLM_DecodeRate validates hardware-level metrics for observability.
// This is NOT about billing — it's about knowing the hardware is healthy.
//
// Run: go test ./internal/harness/ -tags e2e -v -run TestE2E_LocalLLM -timeout 120s
func TestE2E_LocalLLM_DecodeRate(t *testing.T) {
	baseURL := e2eBaseURL()
	model := e2eModel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Run("HardwareDecodeRate", func(t *testing.T) {
		raw, elapsed, err := queryRaw(ctx, baseURL, model, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("query error: %v", err)
		}

		content := ""
		if len(raw.Choices) > 0 {
			content = raw.Choices[0].Message.Content
		}
		if content == "" {
			t.Fatal("empty response")
		}

		t.Logf("Content: %q (%d chars)", content[:min(80, len(content))], len(content))
		t.Logf("Elapsed (wall clock): %v", elapsed)
		t.Logf("completion_tokens: %d", raw.Usage.CompletionTokens)
		t.Logf("prompt_tokens:     %d", raw.Usage.PromptTokens)

		if raw.Timings == nil {
			t.Skip("Server does not report timings field")
		}

		tm := raw.Timings
		t.Logf("--- Server Timings (hardware truth) ---")
		t.Logf("predicted_n:          %d tokens", tm.PredictedN)
		t.Logf("predicted_ms:         %.0f ms", tm.PredictedMS)
		t.Logf("predicted_per_second: %.1f tok/sec", tm.PredictedPerSec)
		t.Logf("prompt_per_second:    %.1f tok/sec", tm.PromptPerSecond)

		// ASSERTION: completion_tokens must equal predicted_n.
		// If these diverge, the server is lying about one of them.
		if int64(tm.PredictedN) != raw.Usage.CompletionTokens {
			t.Errorf("INCONSISTENCY: timings.predicted_n=%d != usage.completion_tokens=%d",
				tm.PredictedN, raw.Usage.CompletionTokens)
		}

		// Hardware plausibility for RTX 4090 + quantized model.
		if tm.PredictedPerSec < 10 || tm.PredictedPerSec > 500 {
			t.Errorf("Decode rate %.1f tok/sec outside plausible range [10, 500]", tm.PredictedPerSec)
		}
	})

	t.Run("DecodeRateStability", func(t *testing.T) {
		const runs = 3
		var rates []float64

		for i := 0; i < runs; i++ {
			raw, _, err := queryRaw(ctx, baseURL, model, goldenPrompt, goldenSystem)
			if err != nil {
				t.Fatalf("Run #%d error: %v", i+1, err)
			}
			if raw.Timings == nil {
				t.Skip("Server does not report timings")
			}
			rates = append(rates, raw.Timings.PredictedPerSec)
		}

		var minR, maxR, sum float64
		minR, maxR = rates[0], rates[0]
		for _, v := range rates {
			sum += v
			if v < minR {
				minR = v
			}
			if v > maxR {
				maxR = v
			}
		}
		avg := sum / float64(len(rates))
		spread := (maxR - minR) / avg

		t.Logf("Decode rate: min=%.1f max=%.1f avg=%.1f spread=%.1f%%", minR, maxR, avg, spread*100)

		if spread > 0.20 {
			t.Errorf("Decode rate unstable (%.0f%% spread) — thermal throttle or contention?", spread*100)
		}
	})

	t.Run("InputTokenDeterminism", func(t *testing.T) {
		raw1, _, err := queryRaw(ctx, baseURL, model, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("query #1 error: %v", err)
		}
		raw2, _, err := queryRaw(ctx, baseURL, model, goldenPrompt, goldenSystem)
		if err != nil {
			t.Fatalf("query #2 error: %v", err)
		}

		if raw1.Usage.PromptTokens != raw2.Usage.PromptTokens {
			t.Errorf("Input tokens not deterministic: %d vs %d", raw1.Usage.PromptTokens, raw2.Usage.PromptTokens)
		}
		t.Logf("Input tokens (stable): %d", raw1.Usage.PromptTokens)

		diff := math.Abs(float64(raw1.Usage.CompletionTokens - raw2.Usage.CompletionTokens))
		avg := float64(raw1.Usage.CompletionTokens+raw2.Usage.CompletionTokens) / 2.0
		t.Logf("Output tokens: %d vs %d (%.0f%% variance)",
			raw1.Usage.CompletionTokens, raw2.Usage.CompletionTokens, (diff/avg)*100)
	})

	t.Run("Streaming_ContentIntegrity", func(t *testing.T) {
		client := harness.NewClient(model, nil)
		client.BaseURL = baseURL

		var buf bytes.Buffer
		resp, err := client.RunStreaming(ctx, goldenPrompt, goldenSystem, &buf)
		if err != nil {
			t.Fatalf("RunStreaming() error: %v", err)
		}

		if resp.Content == "" {
			t.Fatal("empty streaming content")
		}
		if buf.String() != resp.Content {
			t.Errorf("Streamed output (%d bytes) != final content (%d bytes)", buf.Len(), len(resp.Content))
		}

		t.Logf("Streaming latency: %v, content: %d chars", resp.Latency, len(resp.Content))
	})
}
