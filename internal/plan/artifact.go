package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// ArtifactMeta is YAML frontmatter for inter-agent artifacts.
type ArtifactMeta struct {
	Agent     string `yaml:"agent"`
	Session   string `yaml:"session"`
	InputHash string `yaml:"input_hash"` // SHA-256 of the input artifact
	CreatedAt string `yaml:"created_at"`
}

// NewArtifactMeta creates metadata for an artifact produced by the given agent.
// parentContent is the input artifact whose hash is recorded for chain validation.
func NewArtifactMeta(agent, session string, parentContent []byte) ArtifactMeta {
	h := sha256.Sum256(parentContent)
	return ArtifactMeta{
		Agent:     agent,
		Session:   session,
		InputHash: hex.EncodeToString(h[:]),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// MarshalArtifact wraps body content with YAML frontmatter metadata.
func MarshalArtifact(meta ArtifactMeta, body []byte) ([]byte, error) {
	header, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshaling artifact metadata: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(header)
	buf.WriteString("---\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

// ParseArtifact splits a YAML-frontmatter artifact into metadata and body.
// Returns an error if the frontmatter delimiters are missing or the YAML is malformed.
func ParseArtifact(raw []byte) (ArtifactMeta, []byte, error) {
	const delimiter = "---\n"

	if !bytes.HasPrefix(raw, []byte(delimiter)) {
		return ArtifactMeta{}, nil, fmt.Errorf("artifact missing opening frontmatter delimiter")
	}

	rest := raw[len(delimiter):]
	end := bytes.Index(rest, []byte(delimiter))
	if end < 0 {
		return ArtifactMeta{}, nil, fmt.Errorf("artifact missing closing frontmatter delimiter")
	}

	yamlData := rest[:end]
	body := rest[end+len(delimiter):]

	var meta ArtifactMeta
	if err := yaml.Unmarshal(yamlData, &meta); err != nil {
		return ArtifactMeta{}, nil, fmt.Errorf("parsing artifact frontmatter: %w", err)
	}

	return meta, body, nil
}

// ValidateChain verifies that the current artifact's InputHash matches
// the SHA-256 of the parentContent.
func ValidateChain(current, parentContent []byte) error {
	meta, _, err := ParseArtifact(current)
	if err != nil {
		return fmt.Errorf("validating chain: %w", err)
	}

	expected := sha256.Sum256(parentContent)
	expectedHex := hex.EncodeToString(expected[:])

	if meta.InputHash != expectedHex {
		return fmt.Errorf("artifact chain broken: expected input_hash %s, got %s", expectedHex, meta.InputHash)
	}

	return nil
}
