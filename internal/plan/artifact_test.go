package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseArtifact_Valid(t *testing.T) {
	parent := []byte("parent content")
	h := sha256.Sum256(parent)
	hashHex := hex.EncodeToString(h[:])

	raw := "---\nagent: intake\nsession: test-session\ninput_hash: " + hashHex + "\ncreated_at: \"2026-01-01T00:00:00Z\"\n---\nThis is the body.\n"

	meta, body, err := ParseArtifact([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Agent != "intake" {
		t.Errorf("expected agent 'intake', got %q", meta.Agent)
	}
	if meta.Session != "test-session" {
		t.Errorf("expected session 'test-session', got %q", meta.Session)
	}
	if meta.InputHash != hashHex {
		t.Errorf("expected input_hash %s, got %s", hashHex, meta.InputHash)
	}
	if !strings.Contains(string(body), "This is the body.") {
		t.Errorf("expected body to contain 'This is the body.', got %q", string(body))
	}
}

func TestParseArtifact_MissingOpening(t *testing.T) {
	raw := []byte("no frontmatter here\n")
	_, _, err := ParseArtifact(raw)
	if err == nil {
		t.Fatal("expected error for missing opening delimiter")
	}
}

func TestParseArtifact_MissingClosing(t *testing.T) {
	raw := []byte("---\nagent: test\n")
	_, _, err := ParseArtifact(raw)
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestValidateChain_Success(t *testing.T) {
	parent := []byte("the input content")
	meta := NewArtifactMeta("intake", "sess-1", parent)

	artifact, err := MarshalArtifact(meta, []byte("output body"))
	if err != nil {
		t.Fatalf("MarshalArtifact error: %v", err)
	}

	if err := ValidateChain(artifact, parent); err != nil {
		t.Fatalf("ValidateChain error: %v", err)
	}
}

func TestValidateChain_BrokenChain(t *testing.T) {
	parent := []byte("the input content")
	meta := NewArtifactMeta("intake", "sess-1", parent)

	artifact, err := MarshalArtifact(meta, []byte("output body"))
	if err != nil {
		t.Fatalf("MarshalArtifact error: %v", err)
	}

	differentParent := []byte("different content")
	if err := ValidateChain(artifact, differentParent); err == nil {
		t.Fatal("expected error for broken chain")
	}
}

func TestMarshalArtifact_RoundTrip(t *testing.T) {
	parent := []byte("input data")
	meta := NewArtifactMeta("architect", "sess-2", parent)
	body := []byte("## Plan\n\n1. Do stuff\n")

	raw, err := MarshalArtifact(meta, body)
	if err != nil {
		t.Fatalf("MarshalArtifact error: %v", err)
	}

	parsedMeta, parsedBody, err := ParseArtifact(raw)
	if err != nil {
		t.Fatalf("ParseArtifact error: %v", err)
	}

	if parsedMeta.Agent != "architect" {
		t.Errorf("expected agent 'architect', got %q", parsedMeta.Agent)
	}
	if parsedMeta.Session != "sess-2" {
		t.Errorf("expected session 'sess-2', got %q", parsedMeta.Session)
	}
	if string(parsedBody) != string(body) {
		t.Errorf("body mismatch: got %q, want %q", string(parsedBody), string(body))
	}
}
