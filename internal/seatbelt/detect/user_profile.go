//go:build darwin

package detect

import (
	"fmt"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/seatbelt"
)

// UserProfile compiles a seatbelt profile from user-configured filesystem permissions.
// It translates config.SeatbeltConfig allow_read/allow_write/allow_exec into seatbelt rules.
// Environment merging is NOT handled here — it belongs to seatbelt.New.
func UserProfile(home string, cfg config.SeatbeltConfig) (seatbelt.Snapshot, error) {
	p := seatbelt.NewToolProfile("user-config", home)
	for _, path := range cfg.AllowRead {
		if err := p.AllowOptional(path, seatbelt.Read); err != nil {
			return seatbelt.Snapshot{}, fmt.Errorf("user profile allow_read %q: %w", path, err)
		}
	}
	for _, path := range cfg.AllowWrite {
		if err := p.AllowOptional(path, seatbelt.Write); err != nil {
			return seatbelt.Snapshot{}, fmt.Errorf("user profile allow_write %q: %w", path, err)
		}
	}
	for _, dir := range cfg.AllowExec {
		if err := p.AllowOptional(dir, seatbelt.Exec); err != nil {
			return seatbelt.Snapshot{}, fmt.Errorf("user profile allow_exec %q: %w", dir, err)
		}
	}
	return p.Snapshot(), nil
}

// AllProfiles composes all detection results into a slice of snapshots ready for seatbelt.New.
// Docker detection is deliberately excluded from this composition — it is not needed
// for the seatbelt migration path.
func AllProfiles(home, claudeBin string, cfg config.SeatbeltConfig) ([]seatbelt.Snapshot, error) {
	profiles := make([]seatbelt.Snapshot, 0, 4)

	user, err := UserProfile(home, cfg)
	if err != nil {
		return nil, fmt.Errorf("compile user seatbelt profile: %w", err)
	}
	profiles = append(profiles, user)

	claude, err := DetectClaude(home, claudeBin)
	if err != nil {
		return nil, err
	}
	profiles = append(profiles, claude)

	for _, detect := range []struct {
		name string
		fn   func(string) (*seatbelt.Snapshot, error)
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
