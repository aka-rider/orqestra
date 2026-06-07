//go:build darwin

package detect

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/xiii/orqestra/internal/sandbox"
)

// DetectOpenCode returns a mandatory opencode CLI profile.
// Unlike DetectClaude, the binary path must be explicitly provided — no default.
// The binary anchor is mandatory; state/config/cache paths are optional.
func DetectOpenCode(home string, binary string) (sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("opencode", home)

	if binary == "" {
		return sandbox.Snapshot{}, fmt.Errorf("detect opencode: binary path is required, must be explicitly configured")
	}
	binPath, err := exec.LookPath(binary)
	if err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("detect opencode binary %q: %w", binary, err)
	}
	if err := p.Allow(filepath.Dir(binPath), sandbox.Exec); err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("detect opencode binary dir: %w", err)
	}

	optionals := []struct {
		path string
		perm sandbox.Permission
	}{
		{"~/.local/state/opencode", sandbox.Write},
		{"~/.cache/opencode", sandbox.Write},
		{"~", sandbox.Read}, // readdir on $HOME
	}
	for _, opt := range optionals {
		if err := p.AllowOptional(opt.path, opt.perm); err != nil {
			return sandbox.Snapshot{}, fmt.Errorf("detect opencode optional path %q: %w", opt.path, err)
		}
	}

	return p.Snapshot(), nil
}