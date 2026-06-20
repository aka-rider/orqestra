package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"github.com/xiii/orqestra/internal/worktree"
)

// ProcGroup identifies a running process group by its PID.
type ProcGroup struct{ Pid int }

// Supervisor owns every OS resource a run spawns.
// defer sup.Shutdown(log) in the run goroutine fires on every exit path —
// success, error, cancel, panic — ensuring no orphaned claude PIDs or worktrees.
type Supervisor struct {
	mu        sync.Mutex
	procs     []ProcGroup
	worktrees []worktree.Worktree
}

// TrackProc registers a process group to be killed on Shutdown.
func (s *Supervisor) TrackProc(pg ProcGroup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs = append(s.procs, pg)
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

// Shutdown kills every tracked process group and removes unmerged worktrees.
// Uses context.Background() so cleanup runs even when the run ctx is cancelled.
// Safe to call multiple times (idempotent).
func (s *Supervisor) Shutdown(log *slog.Logger) {
	s.mu.Lock()
	procs := s.procs
	wts := s.worktrees
	s.procs = nil
	s.worktrees = nil
	s.mu.Unlock()

	for _, pg := range procs {
		if pg.Pid <= 0 {
			continue
		}
		if err := syscall.Kill(-pg.Pid, syscall.SIGKILL); err != nil {
			if !isNoProcess(err) {
				log.Warn("supervisor: kill process group", "pid", pg.Pid, "err", err)
			}
		}
	}

	for _, wt := range wts {
		if wt.Path == "" {
			continue
		}
		if err := wt.Remove(context.Background(), true); err != nil {
			log.Warn("supervisor: remove worktree", "path", wt.Path, "err", err)
		}
	}
}

func isNoProcess(err error) bool {
	return err == os.ErrProcessDone || err == syscall.ESRCH
}
