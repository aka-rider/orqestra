package sandbox

import (
	"context"
	"log/slog"
	"time"
)

// Container labels used for tracking Orqestra-owned containers.
const (
	LabelOwner      = "orqestra"
	LabelOwnerValue = "true"
	LabelSession    = "orqestra-session"
	LabelCreated    = "orqestra-created"
)

// TrackedContainer is a minimal container descriptor returned by the tracker.
type TrackedContainer struct {
	ID        string
	Labels    map[string]string
	CreatedAt time.Time
}

// ContainerTracker abstracts Docker container queries for the reaper.
// This allows unit testing without a real Docker daemon.
type ContainerTracker interface {
	ListOrqestraContainers(ctx context.Context) ([]TrackedContainer, error)
	KillAndRemove(ctx context.Context, id string) error
}

// Reaper periodically kills and removes expired Orqestra sandbox containers.
type Reaper struct {
	tracker     ContainerTracker
	maxLifetime time.Duration
}

// NewReaper creates a reaper that kills containers older than maxLifetime.
func NewReaper(tracker ContainerTracker, maxLifetime time.Duration) *Reaper {
	return &Reaper{
		tracker:     tracker,
		maxLifetime: maxLifetime,
	}
}

// Sweep checks all Orqestra containers and kills those that have exceeded maxLifetime.
// Returns the IDs of containers that were killed.
func (r *Reaper) Sweep(ctx context.Context) []string {
	containers, err := r.tracker.ListOrqestraContainers(ctx)
	if err != nil {
		slog.Error("reaper: failed to list containers", "err", err)
		return nil
	}

	var killed []string
	for _, c := range containers {
		age := time.Since(c.CreatedAt)
		if age > r.maxLifetime {
			slog.Warn("reaper: killing expired container", "id", c.ID, "age", age.Round(time.Second))
			if err := r.tracker.KillAndRemove(ctx, c.ID); err != nil {
				slog.Error("reaper: failed to kill container", "id", c.ID, "err", err)
				continue
			}
			killed = append(killed, c.ID)
		}
	}

	return killed
}

// CleanupAll kills and removes all Orqestra containers regardless of age.
// Used during graceful shutdown.
func (r *Reaper) CleanupAll(ctx context.Context) {
	containers, err := r.tracker.ListOrqestraContainers(ctx)
	if err != nil {
		slog.Error("reaper: failed to list containers for cleanup", "err", err)
		return
	}

	for _, c := range containers {
		slog.Info("reaper: cleaning up container", "id", c.ID)
		if err := r.tracker.KillAndRemove(ctx, c.ID); err != nil {
			slog.Error("reaper: failed to remove container", "id", c.ID, "err", err)
		}
	}
}

// Run starts the reaper loop. It sweeps every interval until the context is cancelled.
func (r *Reaper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final cleanup on shutdown.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			r.CleanupAll(cleanupCtx)
			cancel()
			return
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}
