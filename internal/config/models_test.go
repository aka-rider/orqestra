package config

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveModel_Success(t *testing.T) {
	// INV-P5-ROUTE: resolved model carries correct BaseURL and type for subprocess env building
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"my-provider": {
				BaseURL: "http://localhost:4141",
				APIKey:  "secret",
				Type:    "anthropic",
			},
		},
		Models: map[string]ModelConfig{
			"my-model": {
				Provider: "my-provider",
				Model:    "claude-medium",
			},
		},
	}

	resolved, err := cfg.ResolveModel("my-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.BaseURL != "http://localhost:4141" {
		t.Errorf("BaseURL = %q, want %q", resolved.BaseURL, "http://localhost:4141")
	}
	if resolved.APIKey != "secret" {
		t.Errorf("APIKey = %q, want %q", resolved.APIKey, "secret")
	}
	if resolved.Model != "claude-medium" {
		t.Errorf("Model = %q, want %q", resolved.Model, "claude-medium")
	}
	if resolved.Type != "anthropic" {
		t.Errorf("Type = %q, want %q", resolved.Type, "anthropic")
	}
}

func TestResolveModel_MissingModel(t *testing.T) {
	// INV-P5-FAILCLOSED: referencing an unknown model must produce *ModelNotFoundError
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models:    map[string]ModelConfig{},
	}

	_, err := cfg.ResolveModel("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	var notFound *ModelNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ModelNotFoundError, got %T", err)
	}
	if notFound.Name != "nonexistent" {
		t.Errorf("Name = %q, want %q", notFound.Name, "nonexistent")
	}
}

// TestResolveModel_CaseMismatchIsExactMiss_WithDidYouMean is WP18's J31c QA
// gate: a model_ref that differs from a configured alias only by case (e.g.
// "Medium" vs "medium") must NOT silently resolve — a case-insensitive
// fallback previously masked exactly this class of typo. The miss must still
// be diagnosable: ModelNotFoundError.DidYouMean lists the case-insensitive
// candidate.
//
// RED-first: against the pre-J31c lookupModel (exact match, then a
// case-insensitive scan of every key), ResolveModel("Medium") returned NO
// error at all (it silently resolved to "medium") — this test's `err == nil`
// check failed outright.
func TestResolveModel_CaseMismatchIsExactMiss_WithDidYouMean(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost:9999", Type: ProviderTypeOpenAI},
		},
		Models: map[string]ModelConfig{
			"medium": {Provider: "local", Model: "test-model"},
		},
	}

	_, err := cfg.ResolveModel("Medium")
	if err == nil {
		t.Fatal("J31c: ResolveModel(\"Medium\") silently resolved to the \"medium\" alias — exact lookup must reject a case mismatch")
	}
	var notFound *ModelNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ModelNotFoundError, got %T (%v)", err, err)
	}
	if len(notFound.DidYouMean) != 1 || notFound.DidYouMean[0] != "medium" {
		t.Errorf("DidYouMean = %v, want [\"medium\"]", notFound.DidYouMean)
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "medium") {
		t.Errorf("error message %q does not name the did-you-mean candidate", err.Error())
	}
}

func TestResolveModel_MissingProvider(t *testing.T) {
	// INV-P5-FAILCLOSED: model referencing a nonexistent provider must error
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models: map[string]ModelConfig{
			"my-model": {Provider: "no-such-provider", Model: "x"},
		},
	}

	_, err := cfg.ResolveModel("my-model")
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestParseTokenLimit(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"unlimited", TokenLimitUnlimited, false},
		{"UNLIMITED", TokenLimitUnlimited, false},
		{"UnLiMiTeD", TokenLimitUnlimited, false},
		{"300K", 300_000, false},
		{"300k", 300_000, false},
		{"1M", 1_000_000, false},
		{"1m", 1_000_000, false},
		{"1.5M", 1_500_000, false},
		{"2.5K", 2_500, false},
		{"500000", 500_000, false},
		{"100", 100, false},
		{"0.5M", 500_000, false},
		// INV-P5-FAILCLOSED: malformed token limits must error
		{"abc", 0, true},
		{"0K", 0, true},
		{"-1K", 0, true},
		{"-500", 0, true},
		{"0", 0, true},
		{"K", 0, true},
		{"M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseTokenLimit(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTokenLimit(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseTokenLimit(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolvedTokenLimits_Conflict(t *testing.T) {
	// INV-P5-FAILCLOSED: conflicting token limits on the same underlying model must error
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {BaseURL: "http://localhost", Type: "openai"},
		},
		Models: map[string]ModelConfig{
			"a": {Provider: "local", Model: "qwen36", TokenLimit: "1M"},
			"b": {Provider: "local", Model: "qwen36", TokenLimit: "500K"},
		},
	}

	_, err := cfg.ResolvedTokenLimits()
	if err == nil {
		t.Fatal("expected error for conflicting limits on same underlying model")
	}
}
