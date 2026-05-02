package tokenlimit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// AgentUsage holds per-agent token consumption for a model.
type AgentUsage struct {
	AgentID     string
	TokensUsed  int64
	LastUpdated time.Time
}

// Store persists token usage in a local SQLite database.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite database at dbPath and runs migrations.
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating db: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS token_usage (
			model        TEXT NOT NULL,
			agent_id     TEXT NOT NULL,
			tokens_used  INTEGER NOT NULL DEFAULT 0,
			last_updated TEXT NOT NULL,
			PRIMARY KEY (model, agent_id)
		)
	`)
	return err
}

// Record atomically increments token usage for a model+agent pair.
func (s *Store) Record(ctx context.Context, model, agentID string, tokens int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO token_usage (model, agent_id, tokens_used, last_updated)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(model, agent_id) DO UPDATE SET
			tokens_used = tokens_used + excluded.tokens_used,
			last_updated = excluded.last_updated
	`, model, agentID, tokens, now)
	if err != nil {
		return fmt.Errorf("recording token usage: %w", err)
	}
	return nil
}

// UsageByModel returns the total tokens consumed across all agents for a model.
func (s *Store) UsageByModel(ctx context.Context, model string) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(tokens_used), 0) FROM token_usage WHERE model = ?
	`, model).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying model usage: %w", err)
	}
	return total, nil
}

// UsageByModelAgent returns per-agent breakdown for a model.
func (s *Store) UsageByModelAgent(ctx context.Context, model string) ([]AgentUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_id, tokens_used, last_updated
		FROM token_usage WHERE model = ?
		ORDER BY tokens_used DESC
	`, model)
	if err != nil {
		return nil, fmt.Errorf("querying agent usage: %w", err)
	}
	defer rows.Close()

	var results []AgentUsage
	for rows.Next() {
		var au AgentUsage
		var ts string
		if err := rows.Scan(&au.AgentID, &au.TokensUsed, &ts); err != nil {
			return nil, fmt.Errorf("scanning agent usage: %w", err)
		}
		au.LastUpdated, _ = time.Parse(time.RFC3339, ts)
		results = append(results, au)
	}
	return results, rows.Err()
}

// AllUsage returns total usage per model across all agents.
func (s *Store) AllUsage(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, SUM(tokens_used) FROM token_usage GROUP BY model
	`)
	if err != nil {
		return nil, fmt.Errorf("querying all usage: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var total int64
		if err := rows.Scan(&model, &total); err != nil {
			return nil, fmt.Errorf("scanning usage: %w", err)
		}
		result[model] = total
	}
	return result, rows.Err()
}

// ResetModel deletes all usage records for a specific model.
func (s *Store) ResetModel(ctx context.Context, model string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM token_usage WHERE model = ?`, model)
	if err != nil {
		return fmt.Errorf("resetting model usage: %w", err)
	}
	return nil
}

// ResetAll deletes all usage records.
func (s *Store) ResetAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM token_usage`)
	if err != nil {
		return fmt.Errorf("resetting all usage: %w", err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
