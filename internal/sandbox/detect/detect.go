//go:build darwin

// Package detect discovers tool installations and returns sandbox.Snapshot values.
// This package is read-only: no side-effect IO, no mkdir, no mutations.
// It lives outside the seatbelt security boundary so seatbelt never imports it.
package detect

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xiii/orqestra/internal/sandbox"
)

// DetectClaude returns a mandatory Claude CLI profile.
// The binary anchor is mandatory; state/config/cache paths are optional.
func DetectClaude(home string, binary string) (sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("claude", home)

	if binary == "" {
		binary = "claude"
	}
	binPath, err := exec.LookPath(binary)
	if err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("detect claude binary %q: %w", binary, err)
	}
	if err := p.Allow(filepath.Dir(binPath), sandbox.Exec); err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("detect claude binary dir: %w", err)
	}

	optionals := []struct {
		path string
		perm sandbox.Permission
	}{
		{"~/.claude.json", sandbox.Write},
		{"~/.claude.json.lock", sandbox.Write},
		{"~/.claude", sandbox.Write},
		{"~/.local/state/claude", sandbox.Write},
		{"~/Library/Caches/claude-cli-nodejs", sandbox.Write},
		{"~/.local/bin", sandbox.Exec},
		{"~/.local/share", sandbox.Exec},
		{"~", sandbox.Read}, // readdir on $HOME; Claude probes it
	}
	for _, opt := range optionals {
		if err := p.AllowOptional(opt.path, opt.perm); err != nil {
			return sandbox.Snapshot{}, fmt.Errorf("detect claude optional path %q: %w", opt.path, err)
		}
	}

	return p.Snapshot(), nil
}

// DetectHomebrew returns a Homebrew profile, or nil if not installed.
// Returns error if brew is found but its prefix can't be determined.
func DetectHomebrew(home string) (*sandbox.Snapshot, error) {
	brewPath, err := exec.LookPath("brew")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("detect homebrew binary: %w", err)
	}

	prefix, err := exec.Command(brewPath, "--prefix").Output()
	if err != nil {
		return nil, fmt.Errorf("detect homebrew prefix: %w", err)
	}

	basePath := strings.TrimSpace(string(prefix))
	p := sandbox.NewToolProfile("homebrew", home)

	p.AddEnv("HOMEBREW_PREFIX", basePath)
	p.AddEnv("HOMEBREW_CELLAR", filepath.Join(basePath, "Cellar"))
	p.AddEnv("HOMEBREW_REPOSITORY", basePath)

	if err := p.Allow(basePath, sandbox.Read); err != nil {
		return nil, fmt.Errorf("detect homebrew core read: %w", err)
	}

	// Homebrew bin dir for exec
	binDir := filepath.Join(basePath, "bin")
	if err := p.AllowOptional(binDir, sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect homebrew bin: %w", err)
	}

	// Homebrew opt/lib for dynamic linking
	for _, sub := range []string{"opt", "lib"} {
		if err := p.AllowOptional(filepath.Join(basePath, sub), sandbox.Read); err != nil {
			return nil, fmt.Errorf("detect homebrew %s: %w", sub, err)
		}
	}

	snap := p.Snapshot()
	return &snap, nil
}

// DetectDocker returns a Docker profile, or nil if not installed.
// Returns error if Docker is found but paths are genuinely broken.
func DetectDocker(home string) (*sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("docker", home)
	found := false

	// Docker Desktop binary dirs
	for _, dir := range []string{
		"/Applications/Docker.app/Contents/Resources/bin",
		"/Applications/Docker.app/Contents/Resources/cli-plugins",
	} {
		if err := p.AllowOptional(dir, sandbox.Exec); err != nil {
			return nil, fmt.Errorf("detect docker path %q: %w", dir, err)
		}
		if _, err := os.Stat(dir); err == nil {
			found = true
		}
	}

	// ~/.docker/bin
	dockerBin := filepath.Join(home, ".docker", "bin")
	if err := p.AllowOptional("~/.docker/bin", sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect docker bin: %w", err)
	}
	if _, statErr := os.Stat(dockerBin); statErr == nil {
		found = true
	}

	// Docker config (read-only)
	if err := p.AllowOptional("~/.docker/config.json", sandbox.Read); err != nil {
		return nil, fmt.Errorf("detect docker config: %w", err)
	}

	// Docker contexts
	if err := p.AllowOptional("~/.docker/contexts", sandbox.Read); err != nil {
		return nil, fmt.Errorf("detect docker contexts: %w", err)
	}

	// Docker socket dir (needs write for docker commands)
	if err := p.AllowOptional("~/.docker/run", sandbox.Write); err != nil {
		return nil, fmt.Errorf("detect docker run: %w", err)
	}

	// Docker socket (system-level, needs write for docker commands)
	for _, sock := range []string{"/var/run/docker.sock", "/private/var/run/docker.sock"} {
		if info, err := os.Stat(sock); err == nil && !info.IsDir() {
			if err := p.Allow(sock, sandbox.Write); err != nil {
				return nil, fmt.Errorf("detect docker socket %q: %w", sock, err)
			}
			found = true
		}
	}

	if !found {
		return nil, nil
	}
	snap := p.Snapshot()
	return &snap, nil
}

// DetectGit returns a Git profile, or nil if no git config exists.
// Returns error if git config is found but paths are genuinely broken.
func DetectGit(home string) (*sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("git", home)
	found := false

	// Git config
	for _, path := range []string{"~/.gitconfig", "~/.config/git"} {
		if err := p.AllowOptional(path, sandbox.Read); err != nil {
			return nil, fmt.Errorf("detect git path %q: %w", path, err)
		}
	}
	// Check if any git config exists
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); err == nil {
		found = true
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "git")); err == nil {
		found = true
	}

	// SSH known_hosts (read only — not the keys)
	if err := p.AllowOptional("~/.ssh/known_hosts", sandbox.Read); err != nil {
		return nil, fmt.Errorf("detect git ssh known_hosts: %w", err)
	}

	// Xcode Command Line Tools (where git binary lives on macOS)
	cltPath := "/Library/Developer/CommandLineTools"
	if err := p.AllowOptional(cltPath, sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect git clt: %w", err)
	}
	if _, err := os.Stat(cltPath); err == nil {
		found = true
	}

	if !found {
		return nil, nil
	}
	snap := p.Snapshot()
	return &snap, nil
}

// DetectNPM returns an npm/nvm profile, or nil if not detected.
func DetectNPM(home string) (*sandbox.Snapshot, error) {
	p := sandbox.NewToolProfile("npm", home)
	found := false

	for _, path := range []string{"~/.npmrc", "~/.npm", "~/.nvm"} {
		if err := p.AllowOptional(path, sandbox.Read); err != nil {
			return nil, fmt.Errorf("detect npm path %q: %w", path, err)
		}
	}

	if _, err := os.Stat(filepath.Join(home, ".npm")); err == nil {
		found = true
	}
	if _, err := os.Stat(filepath.Join(home, ".nvm")); err == nil {
		found = true
	}

	// NVM needs exec on its managed node binaries
	nvmDir := filepath.Join(home, ".nvm")
	if info, err := os.Stat(nvmDir); err == nil && info.IsDir() {
		if err := p.Allow("~/.nvm", sandbox.Exec); err != nil {
			return nil, fmt.Errorf("detect npm nvm exec: %w", err)
		}
	}

	if !found {
		return nil, nil
	}
	snap := p.Snapshot()
	return &snap, nil
}

// DetectXcodeDeveloper returns a profile for the active Xcode developer tools.
// On macOS, /usr/bin/xcrun, /usr/bin/make, etc. dispatch into this directory.
// Returns nil if xcode-select reports no developer directory.
func DetectXcodeDeveloper(home string) (*sandbox.Snapshot, error) {
	out, err := exec.Command("xcode-select", "-p").Output()
	if err != nil {
		// xcode-select not found or no developer dir configured
		return nil, nil
	}
	devDir := strings.TrimSpace(string(out))
	if devDir == "" {
		return nil, nil
	}
	if _, err := os.Stat(devDir); err != nil {
		return nil, nil
	}

	p := sandbox.NewToolProfile("xcode-developer", home)
	if err := p.Allow(devDir, sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect xcode developer dir %q: %w", devDir, err)
	}

	// macOS tool stubs (/usr/bin/git, /usr/bin/python3, …) invoke xcodebuild to
	// locate the real binary when Xcode.app is the active developer directory.
	// xcodebuild loads frameworks from the parent .app bundle (SharedFrameworks/,
	// Frameworks/) — paths outside devDir — and aborts if they are not accessible.
	// Allow the whole Xcode.app bundle so xcodebuild can map its own libraries.
	if bundle := xcodeAppBundle(devDir); bundle != "" {
		if err := p.AllowOptional(bundle, sandbox.Exec); err != nil {
			return nil, fmt.Errorf("detect xcode app bundle %q: %w", bundle, err)
		}
	}

	snap := p.Snapshot()
	return &snap, nil
}

// xcodeAppBundle walks up from devDir and returns the first ancestor whose name
// ends in ".app", or "" if devDir is not inside an application bundle
// (e.g. /Library/Developer/CommandLineTools).
func xcodeAppBundle(devDir string) string {
	dir := filepath.Dir(devDir)
	for dir != "/" && dir != "." {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// DetectGo returns a profile for Go toolchain, or nil if not installed.
func DetectGo(home string) (*sandbox.Snapshot, error) {
	goPath, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("detect go binary: %w", err)
	}

	p := sandbox.NewToolProfile("go", home)

	// Go binary directory (exec)
	if err := p.Allow(filepath.Dir(goPath), sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect go bin dir: %w", err)
	}

	// GOROOT — the Go installation (read + exec for tool binaries)
	goRoot, rootErr := exec.Command(goPath, "env", "GOROOT").Output()
	if rootErr == nil {
		root := strings.TrimSpace(string(goRoot))
		if root != "" {
			if err := p.AllowOptional(root, sandbox.Exec); err != nil {
				return nil, fmt.Errorf("detect go GOROOT: %w", err)
			}
		}
	}

	// GOPATH — module cache and built binaries (read+write)
	goPathDir, pathErr := exec.Command(goPath, "env", "GOPATH").Output()
	if pathErr == nil {
		gp := strings.TrimSpace(string(goPathDir))
		if gp != "" {
			if err := p.AllowOptional(gp, sandbox.Write); err != nil {
				return nil, fmt.Errorf("detect go GOPATH: %w", err)
			}
			// GOPATH/bin for installed tools (go install'd binaries)
			if err := p.AllowOptional(filepath.Join(gp, "bin"), sandbox.Exec); err != nil {
				return nil, fmt.Errorf("detect go GOPATH/bin: %w", err)
			}
		}
	}

	// GOMODCACHE — module download cache (read+write)
	goModCache, cacheErr := exec.Command(goPath, "env", "GOMODCACHE").Output()
	if cacheErr == nil {
		mc := strings.TrimSpace(string(goModCache))
		if mc != "" {
			if err := p.AllowOptional(mc, sandbox.Write); err != nil {
				return nil, fmt.Errorf("detect go GOMODCACHE: %w", err)
			}
		}
	}

	snap := p.Snapshot()
	return &snap, nil
}

// DetectPython returns a profile for Python toolchain, or nil if not installed.
func DetectPython(home string) (*sandbox.Snapshot, error) {
	// Try python3 first, then python
	var pyPath string
	for _, bin := range []string{"python3", "python"} {
		p, err := exec.LookPath(bin)
		if err == nil {
			pyPath = p
			break
		}
	}
	if pyPath == "" {
		return nil, nil
	}

	p := sandbox.NewToolProfile("python", home)

	// Python binary directory (exec)
	if err := p.Allow(filepath.Dir(pyPath), sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect python bin dir: %w", err)
	}

	// Common Python paths
	for _, path := range []string{
		"~/.local/lib/python3",
		"~/.local/lib",
		"~/.pyenv",
	} {
		if err := p.AllowOptional(path, sandbox.Exec); err != nil {
			return nil, fmt.Errorf("detect python path %q: %w", path, err)
		}
	}

	// pip/pipx cache and installed scripts
	if err := p.AllowOptional("~/.local/bin", sandbox.Exec); err != nil {
		return nil, fmt.Errorf("detect python local bin: %w", err)
	}
	if err := p.AllowOptional("~/.cache/pip", sandbox.Write); err != nil {
		return nil, fmt.Errorf("detect python pip cache: %w", err)
	}

	snap := p.Snapshot()
	return &snap, nil
}
