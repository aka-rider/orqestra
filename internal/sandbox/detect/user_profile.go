//go:build darwin

package detect

import (
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/sandbox"
)

// UserProfile compiles a seatbelt profile from user-configured filesystem permissions.
// It translates config.SandboxConfig allow_read/allow_write/allow_exec into seatbelt rules.
// Environment merging is NOT handled here — it belongs to sandbox.New.
func UserProfile(home string, cfg config.SandboxConfig) (sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("user-config", home)
	for _, path := range cfg.AllowRead {
		if err := p.AllowOptional(path, sandbox.Read); err != nil {
			return sandbox.Snapshot{}, fmt.Errorf("user profile allow_read %q: %w", path, err)
		}
	}
	for _, path := range cfg.AllowWrite {
		if err := p.AllowOptional(path, sandbox.Write); err != nil {
			return sandbox.Snapshot{}, fmt.Errorf("user profile allow_write %q: %w", path, err)
		}
	}
	for _, dir := range cfg.AllowExec {
		if err := p.AllowOptional(dir, sandbox.Exec); err != nil {
			return sandbox.Snapshot{}, fmt.Errorf("user profile allow_exec %q: %w", dir, err)
		}
	}
	return p.Snapshot(), nil
}

// AllProfiles composes all detection results into a slice of snapshots ready for sandbox.New.
// Docker detection is deliberately excluded from this composition — it is not needed
// for the seatbelt migration path.
func AllProfiles(home, claudeBin string, cfg config.SandboxConfig) ([]sandbox.Snapshot, error) {
	profiles := make([]sandbox.Snapshot, 0, 4)

	user, err := UserProfile(home, cfg)
	if err != nil {
		return nil, fmt.Errorf("compile user sandbox profile: %w", err)
	}
	profiles = append(profiles, user)

	claude, err := DetectClaude(home, claudeBin)
	if err != nil {
		return nil, err
	}
	profiles = append(profiles, claude)

	for _, detect := range []struct {
		name string
		fn   func(string) (*sandbox.Snapshot, error)
	}{
		{"homebrew", DetectHomebrew},
		{"git", DetectGit},
		{"npm", DetectNPM},
	} {
		snap, err := detect.fn(home)
		if err != nil {
			return nil, fmt.Errorf("detect %s: %w", detect.name, err)
		}
		if snap != nil {
			profiles = append(profiles, *snap)
		}
	}
	return profiles, nil
}
