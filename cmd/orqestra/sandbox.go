//go:build darwin

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/config"
)

// resolveSandboxGrants translates the user's allow_read/allow_write/allow_exec
// config into path lists ready for harness.SandboxConfig.Reads/Writes/Execs.
// Preserves the deleted detect.UserProfile's lenient semantics: a configured
// allow_read/allow_write path that does not exist on disk is silently
// skipped (matching today's AllowOptional) — leash's own Reads/Writes/Execs
// have NO "skip if absent" concept (a missing path is a hard Execute()
// failure), so this pre-filter is what keeps a stale config entry from
// breaking every sandboxed run. allow_exec entries are held to a stricter
// standard: a missing path is skipped, but an EXISTING path that isn't a
// directory is a hard config error (leash grants Exec by directory).
func resolveSandboxGrants(home string, cfg config.SandboxConfig) (reads, writes, execs []string, err error) {
	for _, p := range cfg.AllowRead {
		ok, statErr := existsOptional(p, home)
		if statErr != nil {
			return nil, nil, nil, fmt.Errorf("sandbox.allow_read %q: %w", p, statErr)
		}
		if ok {
			reads = append(reads, p)
		}
	}
	for _, p := range cfg.AllowWrite {
		ok, statErr := existsOptional(p, home)
		if statErr != nil {
			return nil, nil, nil, fmt.Errorf("sandbox.allow_write %q: %w", p, statErr)
		}
		if ok {
			writes = append(writes, p)
		}
	}
	for _, p := range cfg.AllowExec {
		info, statErr := os.Stat(expandTilde(p, home))
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, nil, nil, fmt.Errorf("sandbox.allow_exec %q: %w", p, statErr)
		}
		if !info.IsDir() {
			return nil, nil, nil, fmt.Errorf("sandbox.allow_exec %q: must be a directory", p)
		}
		execs = append(execs, p)
	}
	return reads, writes, execs, nil
}

// resolveProxyEnv filters the user's sandbox.proxy_env names down to those
// actually present in the host environment.
//
// ADDED AFTER CRITIC REVIEW — not in the original draft. leash's own
// MergeEnv hard-errors (propagating out of leash.Execute as a run-ending
// failure) if a ProxyEnv name isn't set on the host; passing cfg.Sandbox.
// ProxyEnv straight through unfiltered would mean a user who lists a
// var that happens to be unset in a given shell (a plausible case — proxy_env
// is typically used for machine-specific values like API keys or local paths
// that vary per developer) breaks EVERY sandboxed run, not just a degraded
// grant. No shipped orqestra.*.yaml sets proxy_env today, so this isn't an
// active regression, but the fix is a direct extension of the same
// skip-if-absent pattern resolveSandboxGrants already applies to
// allow_read/allow_write, so it costs nothing to close now rather than leave
// as a documented (README.md) but silently-fragile feature.
func resolveProxyEnv(names []string) []string {
	var present []string
	for _, name := range names {
		if _, ok := os.LookupEnv(name); ok {
			present = append(present, name)
		} else {
			slog.Debug("sandbox.proxy_env var not set on host, skipping", "name", name)
		}
	}
	return present
}

func existsOptional(raw, home string) (bool, error) {
	_, err := os.Stat(expandTilde(raw, home))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func expandTilde(raw, home string) string {
	if raw == "~" {
		return home
	}
	if strings.HasPrefix(raw, "~/") {
		return filepath.Join(home, raw[2:])
	}
	return raw
}

// selfExecDir returns the resolved directory containing the running orqestra
// binary, for granting self-exec inside the sandbox (the MCP-bridge
// subprocess re-invokes orqestra itself for AskUserQuestion/SubmitReport).
// Today's grant is file-level and relies on the (now-deleted) sandbox
// package's internal symlink resolution of that one file; moving to a
// directory-level grant (per the request to add ./bin to leash's executable
// dirs) means THIS function must resolve symlinks itself before taking
// Dir() — Seatbelt checks process-exec against the resolved path, so if
// os.Executable() ever returned a symlink (e.g. a Homebrew-style shim),
// granting the symlink's own directory instead of the resolved target's
// directory would silently miss the grant the MCP bridge needs. A directory
// literally named "bin" also gets leash's automatic PATH-visibility bonus
// for free (sandbox.ExtraPathDirs's "bin"-basename convention) — not
// required for anything orqestra does (the bridge is always invoked by
// absolute path), but harmless and consistent with the request.
func selfExecDir() (dir string, err error) {
	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve self binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", fmt.Errorf("resolve self binary symlinks: %w", err)
	}
	return filepath.Dir(resolved), nil
}

// worktreeGitGrants returns the extra Writes/FutureWrites leash needs for
// git commands (git add/commit/etc., if the worker's own Bash tool runs
// them directly) to succeed inside a linked worktree — grant SHAPE inspired
// by leash's own CLI reference implementation (cmd/leash/main.go's
// applyWorktree: write access to the worktree's private admin dir plus
// objects/refs/logs, FutureWrites on packed-refs* for git's ref-update
// lock), NOT a literal port of it. CORRECTED AFTER CRITIC REVIEW: leash's
// applyWorktree computes packed-refs*'s location from its own resolved
// gitDir, general enough to handle a worktree nested under another
// worktree; this function instead places packed-refs* directly under
// <repoRoot>/.git, which is only correct because orqestra's worktrees are
// ALWAYS created directly off the main repo (internal/worktree.Create's
// only caller passes the top-level repoPath, never another worktree's
// path) — for orqestra's specific, narrower usage pattern, the common git
// dir and <repoRoot>/.git are always the same directory, so this is a
// deliberate simplification for this codebase, not an oversight. Runtime
// sufficiency (whether this grant set is enough for whatever git commands
// the worker's Bash tool actually issues) is only checked by Verification
// step 6's real worktree-mode smoke test — this being best-effort (see
// below) means an insufficient grant degrades to a narrower-than-today
// permission error on that one git command, not a security or
// data-loss issue.
//
// Unlike leash's CLI (which trusts its own --worktree NAME argument as the
// admin-dir id), orqestra's worktree directory is ALWAYS named literally
// "worktree" (internal/worktree.Create's wtPath is always
// <sessionDir>/worktree) — and orqestra deliberately preserves worktrees on
// a controlled agent failure for inspection, so a stale
// .git/worktrees/worktree registration from a prior run can coexist with a
// new run's. This is not hypothetical: this repo's own .git/worktrees/
// already contains worktree, worktree1, worktree2, worktree3, worktree4,
// worktree5 from git's own numeric-suffix collision disambiguation. A
// hardcoded "worktree" assumption would silently target the wrong, stale
// admin directory under exactly this repo's own demonstrated usage
// pattern — so the id is resolved via `git rev-parse --git-dir` run with
// the worktree itself as cwd, not assumed from the directory's basename.
//
// Best-effort: this is a pure widening over today's baseline (which grants
// none of this — today's SBPL profile never touched .git/worktrees at all),
// so any failure (git not found, unexpected output) logs a warning and
// returns no extra grants rather than failing the run — that degrades to
// today's known-safe-but-narrower behavior instead of escalating a
// git-plumbing hiccup into a hard pipeline failure.
func worktreeGitGrants(repoRoot, wtPath string) (writes, futureWrites []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = wtPath
	out, err := cmd.Output()
	if err != nil {
		slog.Warn("worktree git-dir lookup failed; skipping extra git grants", "err", err)
		return nil, nil
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wtPath, gitDir)
	}
	if abs, absErr := filepath.Abs(gitDir); absErr == nil {
		gitDir = abs
	}
	if resolved, evalErr := filepath.EvalSymlinks(gitDir); evalErr == nil {
		gitDir = resolved
	}
	writes = append(writes, gitDir) // <repoRoot>/.git/worktrees/<id>

	mainGitDir := filepath.Join(repoRoot, ".git")
	for _, p := range []string{
		filepath.Join(mainGitDir, "objects"),
		filepath.Join(mainGitDir, "refs"),
		filepath.Join(mainGitDir, "logs"),
	} {
		if _, statErr := os.Stat(p); statErr == nil {
			writes = append(writes, p)
		}
	}
	futureWrites = append(futureWrites,
		filepath.Join(mainGitDir, "packed-refs"),
		filepath.Join(mainGitDir, "packed-refs.lock"),
		filepath.Join(mainGitDir, "packed-refs.new"),
	)
	return writes, futureWrites
}
