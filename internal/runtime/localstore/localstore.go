// Package localstore is wormholed's durable local state (RFC-0003 §6.3,
// §7.2). It follows the Store-struct/sentinel-error/wrapped-error shape
// established by internal/core/identity (docs/architecture.md §3), adapted
// for SQLite: no transactions needed yet (single-statement writes only,
// P1 scope), schema applied on Open rather than via golang-migrate (that
// tooling targets the Coordination Server's Postgres only).
package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a cache lookup has no matching row.
var ErrNotFound = errors.New("localstore: not found")

const schema = `
CREATE TABLE IF NOT EXISTS whoami_cache (
	agent_id     TEXT PRIMARY KEY,
	owner        TEXT NOT NULL,
	model        TEXT NOT NULL,
	capabilities TEXT NOT NULL DEFAULT '[]',
	project_id   TEXT NOT NULL,
	permissions  TEXT NOT NULL DEFAULT '[]',
	cached_at    TIMESTAMP NOT NULL
);
`

// Store wraps a *sql.DB backed by a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the SQLite file at path, applying the
// schema. Callers must Close the returned Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("localstore: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("localstore: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// WhoAmICache is the cached wormhole.agent.whoami result for one agent.
type WhoAmICache struct {
	AgentID      string
	Owner        string
	Model        string
	Capabilities []string
	ProjectID    string
	Permissions  []string
	CachedAt     time.Time
}

// CacheWhoAmI upserts the cached identity for c.AgentID.
func (s *Store) CacheWhoAmI(ctx context.Context, c WhoAmICache) error {
	capsJSON, err := json.Marshal(nonNil(c.Capabilities))
	if err != nil {
		return fmt.Errorf("localstore: marshal capabilities: %w", err)
	}
	permsJSON, err := json.Marshal(nonNil(c.Permissions))
	if err != nil {
		return fmt.Errorf("localstore: marshal permissions: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			owner = excluded.owner,
			model = excluded.model,
			capabilities = excluded.capabilities,
			project_id = excluded.project_id,
			permissions = excluded.permissions,
			cached_at = excluded.cached_at
	`, c.AgentID, c.Owner, c.Model, string(capsJSON), c.ProjectID, string(permsJSON), c.CachedAt)
	if err != nil {
		return fmt.Errorf("localstore: cache whoami for %s: %w", c.AgentID, err)
	}
	return nil
}

// GetCachedWhoAmI returns the cached identity for agentID, or ErrNotFound.
func (s *Store) GetCachedWhoAmI(ctx context.Context, agentID string) (WhoAmICache, error) {
	var c WhoAmICache
	var capsJSON, permsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at
		FROM whoami_cache WHERE agent_id = ?
	`, agentID).Scan(&c.AgentID, &c.Owner, &c.Model, &capsJSON, &c.ProjectID, &permsJSON, &c.CachedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WhoAmICache{}, ErrNotFound
	}
	if err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: get cached whoami for %s: %w", agentID, err)
	}
	if err := json.Unmarshal([]byte(capsJSON), &c.Capabilities); err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: unmarshal capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(permsJSON), &c.Permissions); err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: unmarshal permissions: %w", err)
	}
	c.CachedAt = c.CachedAt.UTC()
	return c, nil
}

func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
