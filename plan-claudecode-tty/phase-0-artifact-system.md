# Work Package: artifact-system

| Field | Value |
|-------|-------|
| **ID** | `artifact-system` |
| **Wave** | 1 |
| **depends_on** | — |
| **Files** | `internal/sandbox/artifact.go`, `internal/sandbox/artifact_test.go`, `internal/sandbox/testdata/` |

## Goal

Implement the structured markdown artifact system for inter-agent communication (YAML frontmatter + markdown body).

## Steps

1. Create `internal/sandbox/artifact.go` with:
   - `Artifact` struct: `Schema`, `Session`, `ProducedBy`, `Step`, `Timestamp`, `InputHash`, `Body` (string), `Metadata` (map[string]any).
   - `WriteArtifact(path string, art Artifact) error` — serializes YAML frontmatter + markdown body to file.
   - `ReadArtifact(path string) (Artifact, error)` — parses file with YAML frontmatter into Artifact.
   - `HashArtifact(path string) (string, error)` — returns SHA-256 hex digest of file contents.
   - `ValidateChain(artifactPath, inputPath string) error` — checks artifact's InputHash matches hash of inputPath.

2. Create `internal/sandbox/artifact_test.go` with:
   - Round-trip test: `WriteArtifact` → `ReadArtifact` → assert all fields equal.
   - Golden file tests: known artifact format in `testdata/` parsed correctly.
   - Hash test: `HashArtifact` returns expected SHA-256 for a known file.
   - Chain validation: write two artifacts (input + output with correct InputHash), `ValidateChain` passes. Corrupt the input, assert error.
   - Malformed frontmatter rejection: missing `---` delimiters, invalid YAML, missing required fields → clear error.

3. Create golden test fixtures in `internal/sandbox/testdata/`:
   - `golden_artifact.md` — valid artifact with all fields.
   - `malformed_no_delimiters.md` — missing `---`.
   - `malformed_bad_yaml.md` — invalid YAML in frontmatter.

## Acceptance

- `go test ./internal/sandbox/ -run TestArtifact` passes.
- `go vet ./internal/sandbox/` clean.
- Dependencies: `gopkg.in/yaml.v3` (already in go.mod) and stdlib only.
- Files touched: ONLY `internal/sandbox/artifact.go`, `internal/sandbox/artifact_test.go`, and `internal/sandbox/testdata/` golden files.
