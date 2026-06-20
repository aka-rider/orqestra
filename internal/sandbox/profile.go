//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
	"os"
)

// Permission defines filesystem access levels for SBPL rules.
type Permission uint8

const (
	Read  Permission = 1 << iota // SBPL: file-read*
	Write                        // SBPL: file-read* + file-write*
	Exec                         // SBPL: file-read* + file-map-executable + process-exec
)

// entry pairs a resolved path with a permission.
type entry struct {
	path Path
	perm Permission
}

// ToolProfile accumulates filesystem access rules and specific env variables for a tool.
type ToolProfile struct {
	name    string
	home    string
	entries []entry
	env     map[string]string
}

// NewToolProfile creates a new profile for the named tool.
func NewToolProfile(name, home string) *ToolProfile {
	return &ToolProfile{
		name: name,
		home: home,
		env:  make(map[string]string),
	}
}

// AddEnv records an environment variable required by this tool.
func (p *ToolProfile) AddEnv(key, value string) {
	p.env[key] = value
}

// Allow is the ONLY mutation method. Resolves raw path (expands ~, resolves symlinks, stats).
// Returns error on any failure — never swallows.
// Exec on a file emits a (literal …) process-exec rule for that specific binary.
// Exec on a directory emits a (subpath …) process-exec rule allowing any binary beneath it.
func (p *ToolProfile) Allow(raw string, perm Permission) error {
	path, err := ResolvePath(raw, p.home)
	if err != nil {
		return fmt.Errorf("profile %q: %w", p.name, err)
	}
	p.entries = append(p.entries, entry{path: path, perm: perm})
	return nil
}

// AllowOptional skips paths that do not exist, but propagates permission denied,
// symlink loops, and all other non-not-exist errors.
func (p *ToolProfile) AllowOptional(raw string, perm Permission) error {
	if err := p.Allow(raw, perm); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

// Snapshot is an immutable, opaque value for ProfileBuilder.
type Snapshot struct {
	name    string
	entries []entry
	env     map[string]string
}

// Snapshot returns an immutable deep copy of the profile state.
func (p *ToolProfile) Snapshot() Snapshot {
	cp := make([]entry, len(p.entries))
	copy(cp, p.entries)

	envCp := make(map[string]string, len(p.env))
	for k, v := range p.env {
		envCp[k] = v
	}

	return Snapshot{
		name:    p.name,
		entries: cp,
		env:     envCp,
	}
}
