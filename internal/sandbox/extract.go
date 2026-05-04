package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// stageCopy writes content to a destination path, creating parent directories as needed.
func stageCopy(content []byte, destPath string) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating staging directory %q: %w", dir, err)
	}
	if err := os.WriteFile(destPath, content, 0o644); err != nil {
		return fmt.Errorf("writing staged file %q: %w", destPath, err)
	}
	return nil
}
