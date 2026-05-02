package sandbox

import (
	"path/filepath"
	"strings"
)

// RejectionReason classifies why a file was rejected.
type RejectionReason string

const (
	ReasonUnexpectedExec RejectionReason = "unexpected_executable"
	ReasonDangerousExt   RejectionReason = "dangerous_extension"
	ReasonOversized      RejectionReason = "oversized"
	ReasonPathTraversal  RejectionReason = "path_traversal"
)

// RejectedFile describes a file that failed security verification.
type RejectedFile struct {
	Path   string
	Reason RejectionReason
	Detail string
}

// VerifyResult is the outcome of security verification.
type VerifyResult struct {
	Passed   bool
	Rejected []RejectedFile
	Warnings []string
}

// VerifierConfig configures the security verifier.
type VerifierConfig struct {
	AllowedExecutables []string // glob patterns for allowed executable files
	MaxFileSize        int64    // max file size in bytes (0 = no limit)
}

// Verifier scans extracted files for security issues before they reach the host.
type Verifier struct {
	cfg VerifierConfig
}

// NewVerifier creates a security verifier with the given config.
func NewVerifier(cfg VerifierConfig) *Verifier {
	return &Verifier{cfg: cfg}
}

// dangerousExtensions are file extensions that should never be extracted from a sandbox.
var dangerousExtensions = []string{".so", ".dylib", ".dll"}

// Verify checks a set of changed files for security issues.
func (v *Verifier) Verify(files []ChangedFile) VerifyResult {
	result := VerifyResult{Passed: true}

	for _, f := range files {
		// Path traversal check: reject any path that escapes the workspace root.
		if containsTraversal(f.Path) {
			result.Passed = false
			result.Rejected = append(result.Rejected, RejectedFile{
				Path:   f.Path,
				Reason: ReasonPathTraversal,
				Detail: "path contains traversal sequence",
			})
			continue
		}

		// Size check.
		if v.cfg.MaxFileSize > 0 && f.Size > v.cfg.MaxFileSize {
			result.Passed = false
			result.Rejected = append(result.Rejected, RejectedFile{
				Path:   f.Path,
				Reason: ReasonOversized,
				Detail: "file exceeds maximum allowed size",
			})
			continue
		}

		// Dangerous extension check.
		ext := strings.ToLower(filepath.Ext(f.Path))
		for _, dangerous := range dangerousExtensions {
			if ext == dangerous {
				result.Passed = false
				result.Rejected = append(result.Rejected, RejectedFile{
					Path:   f.Path,
					Reason: ReasonDangerousExt,
					Detail: "file has dangerous extension: " + ext,
				})
				break
			}
		}

		// Executable check: reject unless the file matches an allowed pattern.
		if f.IsExecutable && !v.isAllowedExecutable(f.Path) {
			result.Passed = false
			result.Rejected = append(result.Rejected, RejectedFile{
				Path:   f.Path,
				Reason: ReasonUnexpectedExec,
				Detail: "executable file not in allowed list",
			})
		}
	}

	return result
}

// isAllowedExecutable checks if a file path matches any of the allowed executable patterns.
func (v *Verifier) isAllowedExecutable(path string) bool {
	for _, pattern := range v.cfg.AllowedExecutables {
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
	}
	return false
}

// containsTraversal returns true if the path tries to escape the workspace root.
func containsTraversal(path string) bool {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return true
	}
	if strings.HasPrefix(cleaned, "..") {
		return true
	}
	return false
}
