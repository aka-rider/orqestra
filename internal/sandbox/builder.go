//go:build darwin

package sandbox

import (
	"fmt"
	"sort"
	"strings"
)

// ProfileBuilder assembles a complete SBPL profile from system base rules,
// workspace path, home, tmpdir, and zero or more tool snapshots.
type ProfileBuilder struct {
	workspace    Path
	home         string
	tmpDir       string // resolved TMPDIR
	snapshots    []Snapshot
	RepoWritable bool  // if false, workspace (repo) is read-only
	SessionPath  *Path // optional separate session directory (always read+write)
	WorktreePath *Path // optional worktree directory (always read+write; main repo stays read-only)
}

// NewProfileBuilder creates a builder with mandatory system paths.
func NewProfileBuilder(workspace Path, home string, tmpDir string) (*ProfileBuilder, error) {
	if !workspace.IsDir {
		return nil, fmt.Errorf("profile builder: workspace must be a directory")
	}
	if home == "" {
		return nil, fmt.Errorf("profile builder: home is required")
	}
	if tmpDir == "" {
		return nil, fmt.Errorf("profile builder: tmpdir is required")
	}
	return &ProfileBuilder{
		workspace: workspace,
		home:      home,
		tmpDir:    tmpDir,
	}, nil
}

// Add appends a tool snapshot to the builder.
func (b *ProfileBuilder) Add(s Snapshot) {
	b.snapshots = append(b.snapshots, s)
}

// Build produces the final SBPL profile string.
func (b *ProfileBuilder) Build() (string, error) {
	var sb strings.Builder

	// --- System base (corrected per plan) ---
	sb.WriteString(`(version 1)
(deny default (with message "orqestra"))

;; Process fundamentals (required for any process to start)
(allow file-read-metadata)
(allow sysctl-read)
(allow signal)
(allow process-info*)
(allow process-fork)

;; Root directory traversal (required by dyld/linker at process startup)
(allow file-read-data
  (literal "/")
  (literal "/private")
  (literal "/private/var"))

;; Dynamic linker + system libraries (read + map-executable)
(allow file-read* file-map-executable
  (subpath "/System")
  (subpath "/usr/lib")
  (subpath "/usr/share")
  (subpath "/var/db/dyld")
  (subpath "/Library/Frameworks")
  (subpath "/Library/Apple"))

;; System binaries (exec)
(allow process-exec
  (subpath "/usr/bin")
  (subpath "/usr/sbin")
  (subpath "/usr/libexec")
  (subpath "/bin")
  (subpath "/sbin"))

;; System binaries (read for shell builtins, scripting)
(allow file-read*
  (subpath "/usr/bin")
  (subpath "/usr/sbin")
  (subpath "/usr/libexec")
  (subpath "/bin")
  (subpath "/sbin"))

;; System config (READ-ONLY, not executable)
(allow file-read*
  (subpath "/private/etc")
  (subpath "/Library/Preferences")
  (subpath "/Library/Keychains")
  (subpath "/private/var/db/timezone")
  (subpath "/private/var/db/mds"))

;; Tmp (read + write)
(allow file-read* file-write*
  (subpath "/tmp")
  (subpath "/private/tmp")
  (subpath "/private/var/folders")
  (subpath "/var/folders")
  (subpath "` + b.tmpDir + `"))

;; Devices
(allow file-read* file-write*
  (regex "^/dev/(tty.*|null|zero|random|urandom|dtracehelper)"))
(allow file-ioctl)

;; IPC (Keychain, Security.framework, DNS)
(allow mach-lookup)
(allow ipc-posix-shm*)
(allow file-read* (subpath "/private/var/run/mDNSResponder"))

;; Network (Full outbound allowed for dev tools/LLMs)
(allow system-socket)
(allow network-outbound)
(allow network-inbound (local ip "localhost:*"))
`)

	// --- Tool profile allows ---
	for _, snap := range b.snapshots {
		if len(snap.entries) == 0 {
			continue
		}
		sb.WriteString("\n;; Tool: " + snap.name + "\n")
		b.emitEntries(&sb, snap.entries)
	}

	// --- Workspace allow (always last — wins over everything) ---
	if b.RepoWritable {
		sb.WriteString(`
;; Repo (read+write — worker mode)
(allow file-read* file-write* file-map-executable process-exec
  (subpath "` + b.workspace.Resolved + `"))
`)
	} else {
		sb.WriteString(`
;; Repo (read-only)
(allow file-read*
  (subpath "` + b.workspace.Resolved + `"))
`)
	}

	// --- Session directory (always read+write if provided) ---
	if b.SessionPath != nil {
		sb.WriteString(`
;; Session (always read+write)
(allow file-read* file-write* file-map-executable process-exec
  (subpath "` + b.SessionPath.Resolved + `"))
`)
	}

	// --- Worktree directory (always read+write if provided; overrides repo read-only) ---
	if b.WorktreePath != nil {
		sb.WriteString(`
;; Worktree (always read+write — isolated workspace for worker)
(allow file-read* file-write* file-map-executable process-exec
  (subpath "` + b.WorktreePath.Resolved + `"))
`)
	}

	return sb.String(), nil
}

// groupKey identifies a unique (permission, isDir) combination for compact SBPL emission.
type groupKey struct {
	perm  Permission
	isDir bool
}

// emitEntries groups entries by (permission, isDir) for compact SBPL output.
func (b *ProfileBuilder) emitEntries(sb *strings.Builder, entries []entry) {
	groups := make(map[groupKey][]string)
	for _, e := range entries {
		k := groupKey{perm: e.perm, isDir: e.path.IsDir}
		groups[k] = append(groups[k], e.path.Resolved)
	}

	// Sort keys for deterministic output
	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].perm != keys[j].perm {
			return keys[i].perm < keys[j].perm
		}
		if keys[i].isDir != keys[j].isDir {
			return !keys[i].isDir // files before dirs
		}
		return false
	})

	for _, k := range keys {
		paths := groups[k]
		sort.Strings(paths)

		sb.WriteString("(allow ")
		sb.WriteString(sbplOps(k.perm))

		pathType := "literal"
		if k.isDir {
			pathType = "subpath"
		}

		for _, p := range paths {
			sb.WriteString(fmt.Sprintf("\n  (%s %q)", pathType, p))
		}
		sb.WriteString(")\n")
	}
}

// sbplOps returns the SBPL operation string for a permission level.
func sbplOps(p Permission) string {
	switch {
	case p&Exec != 0:
		return "file-read* file-map-executable process-exec"
	case p&Write != 0:
		return "file-read* file-write*"
	default:
		return "file-read*"
	}
}
