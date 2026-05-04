# Work Package: artifact-system

| Field | Value |
|-------|-------|
| **ID** | `artifact-system` |
| **Wave** | 3 |
| **depends_on** | `docker-sdk-migration, overlayfs-sandbox, pty-session` |
| **Files** | `internal/sandbox/artifact.go`, `internal/sandbox/artifact_test.go`, `internal/sandbox/testdata/` |

## Goal

Implement the structured markdown artifact system for inter-agent communication (YAML frontmatter + markdown body) featuring strict validation, memory-bound parsing, atomic I/O, and secure sandboxing bridges.

## Steps

1. Create `internal/sandbox/artifact.go` with:
   - `Artifact` struct explicitly tagged for YAML serialization (`yaml:"..."`). Use strongly-typed fields: `Schema`, `Session`, `ProducedBy`, `Step`, `Timestamp`, `InputHash`, `Body` (string). Do not use untyped `map[string]any` arrays; missing attributes should result in empty strings.
   - `MaxArtifactSize` constant (e.g., 5MB) to protect orchestrator memory.
   - `SecurePath(baseDir, targetPath string) (string, error)` — ensures the path falls entirely within `baseDir`, ends strictly with `.md`, and is free from `../` directory escapes.

2. Implement Reader / Parsing logic:
   - `ReadArtifactFromReader(r io.Reader) (Artifact, error)` — Reads via `io.LimitReader` to enforce `MaxArtifactSize`. Escapes the tar-stream provided by the Docker sandbox `CopyOut` (from overlayfs migration). Uses regex `(?s)^\s*---\r?\n(.*?)\r?\n---\r?\n(.*)$` to robustly split YAML on LF or CRLF and separate the body.
   - `ReadArtifact(path string) (Artifact, error)` — Wrapper around `ReadArtifactFromReader` fetching it from the host filesystem.

3. Implement Write / Hash logic:
   - `WriteArtifact(baseDir, filename string, art Artifact) error` — Passes through `SecurePath`. Uses atomic writes (`os.WriteFile` to a `.tmp` file, followed by `os.Rename`) ensuring `0600` or `0644` file permissions (explicitly non-executable). Creates parent directories securely.
   - `HashArtifact(path string) (string, error)` — Returns SHA-256 hex digest of the raw file bytes on disk using a streaming `hash.Hash` inside `io.Copy` (memory-safe).

4. Implement Validation logic:
   - `ValidateChain(artifactPath, inputPath string) error` — Checks artifact's `InputHash` matches the hash of `inputPath`. Bypasses checking only if `InputHash` is empty AND the `Schema` equals `"genesis"`. Fails aggressively otherwise.

5. Create `internal/sandbox/artifact_test.go` with:
   - Path Traversal Test: Assert `SecurePath` fails maliciously crafted destinations (`../../etc/passwd`, `script.sh`).
   - Round-trip test: `WriteArtifact` → `ReadArtifact` → assert all fields equal.
   - Stream test: parsing from memory `io.Reader` (simulating Docker PTY/overlayfs copy-out stream).
   - Security limit test: Attempting to read a string larger than `MaxArtifactSize` fails out properly avoiding OOM.
   - Atomic write verification.
   - Chain validation tests including successful chain matching and graceful "genesis" node handling.
   - Malformed rejection checks (CRLF tests, invalid YAML, oversize files, empty body tests).

6. Create golden test fixtures in `internal/sandbox/testdata/`:
   - `golden_artifact.md` — valid artifact with all fields.
   - `malformed_no_delimiters.md` — missing `---`.
   - `malformed_bad_yaml.md` — invalid YAML in frontmatter.
   - `crlf_artifact.md` — valid artifact utilizing windows `\r\n` line endings.

## Acceptance

- `go test ./internal/sandbox/ -run TestArtifact` passes.
- `go vet ./internal/sandbox/` clean.
- All artifact processing explicitly utilizes fixed bounds (no risk of memory explosion).
- Artifact paths are protected against directory traversal and constrained to non-executable markdown footprints.
- Dependencies: `gopkg.in/yaml.v3` (already in go.mod) and stdlib only.
- Files touched: ONLY `internal/sandbox/artifact.go`, `internal/sandbox/artifact_test.go`, and `internal/sandbox/testdata/` files.
