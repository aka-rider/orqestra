package orchestrator

import (
	"context"
	"log/slog"
	"sync"

	"github.com/xiii/orqestra/internal/worktree"
)

// Supervisor owns every OS resource a run spawns.
// defer sup.Shutdown(log) in the run goroutine fires on every exit path —
// success, error, cancel, panic — ensuring no orphaned worktrees. (Process-group
// kill-on-cancel lives in harness.Run since WP1; this Supervisor tracks worktrees only.)
type Supervisor struct {
	mu        sync.Mutex
	worktrees []worktree.Worktree
}

// TrackWorktree registers a worktree to be removed on Shutdown (unless merged).
func (s *Supervisor) TrackWorktree(wt worktree.Worktree) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.worktrees = append(s.worktrees, wt)
}

// UntrackWorktree removes a merged worktree so Shutdown doesn't double-remove it.
func (s *Supervisor) UntrackWorktree(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.worktrees[:0]
	for _, wt := range s.worktrees {
		if wt.Path != path {
			filtered = append(filtered, wt)
		}
	}
	s.worktrees = filtered
}

// Shutdown removes every tracked, unmerged worktree.
// Uses context.Background() so cleanup runs even when the run ctx is cancelled.
// Safe to call multiple times (idempotent).
func (s *Supervisor) Shutdown(log *slog.Logger) {
	s.mu.Lock()
	wts := s.worktrees
	s.worktrees = nil
	s.mu.Unlock()

	for _, wt := range wts {
		if wt.Path == "" {
			continue
		}
		if err := wt.Remove(context.Background(), true); err != nil {
			log.Warn("supervisor: remove worktree", "path", wt.Path, "err", err)
		}
	}
}
