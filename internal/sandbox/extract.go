package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parseBtrfsDump parses the output of `btrfs receive --dump` into ChangedFile entries.
//
// btrfs receive --dump format (one operation per line):
//
//	mkfile       ./path/to/new-file.go
//	write        ./path/to/file.go offset=0 len=1234
//	truncate     ./path/to/file.go size=5678
//	rename       ./old/path -> ./new/path
//	unlink       ./path/to/deleted.go
//	rmdir        ./path/to/deleted-dir
//	chmod        ./path/to/file.go mode=755
//	chown        ./path/to/file.go uid=1000 gid=1000
//	utimes       ./path/to/file.go
//	set_xattr    ./path/to/file.go name=... data=...
//	link         ./path/to/link -> ./target
//	symlink      ./path/to/link -> ./target
//
// We track: mkfile (added), write/truncate (modified), unlink (deleted), rename (delete+add).
func parseBtrfsDump(lines []string) []ChangedFile {
	seen := make(map[string]FileOp) // path → latest operation

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Split into operation and rest.
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		op := strings.TrimSpace(parts[0])
		rest := strings.TrimSpace(parts[1])

		switch op {
		case "mkfile":
			path := cleanBtrfsPath(rest)
			if path != "" {
				seen[path] = FileAdded
			}

		case "write", "truncate":
			// write ./path offset=0 len=1234
			// truncate ./path size=5678
			path := extractPath(rest)
			if path != "" {
				// Only mark modified if not already marked as added.
				if seen[path] != FileAdded {
					seen[path] = FileModified
				}
			}

		case "unlink":
			path := cleanBtrfsPath(rest)
			if path != "" {
				seen[path] = FileDeleted
			}

		case "rename":
			// rename ./old -> ./new
			parts := strings.SplitN(rest, " -> ", 2)
			if len(parts) == 2 {
				oldPath := cleanBtrfsPath(parts[0])
				newPath := cleanBtrfsPath(parts[1])
				if oldPath != "" {
					seen[oldPath] = FileDeleted
				}
				if newPath != "" {
					seen[newPath] = FileAdded
				}
			}

		// Ignore: rmdir, chmod, chown, utimes, set_xattr, link, symlink, mkdir
		// (directory ops and metadata-only changes don't produce extractable files)
		}
	}

	// Convert map to slice, skipping directories.
	var files []ChangedFile
	for path, op := range seen {
		if strings.HasSuffix(path, "/") {
			continue
		}
		files = append(files, ChangedFile{Path: path, Op: op})
	}
	return files
}

// cleanBtrfsPath normalizes a btrfs dump path — strips leading "./" prefix.
func cleanBtrfsPath(raw string) string {
	// btrfs dump paths are relative: "./path/to/file" or "path/to/file"
	path := strings.TrimSpace(raw)
	path = strings.TrimPrefix(path, "./")
	// Strip any trailing attributes (e.g., "path mode=755")
	if idx := strings.IndexByte(path, ' '); idx != -1 {
		path = path[:idx]
	}
	return path
}

// extractPath gets the file path from a btrfs dump line with trailing attributes.
func extractPath(rest string) string {
	// "path/to/file offset=0 len=1234" → "path/to/file"
	return cleanBtrfsPath(rest)
}

// parseDiffOutput is kept for backward compatibility but delegates to parseBtrfsDump.
// Deprecated: use parseBtrfsDump directly.
func parseDiffOutput(lines []string) []ChangedFile {
	return parseBtrfsDump(lines)
}

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
