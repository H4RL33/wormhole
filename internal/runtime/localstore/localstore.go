// Package localstore is wormholed's durable local state (RFC-0003 §6.3,
// §7.2). It follows the Store-struct/sentinel-error/wrapped-error shape
// established by internal/core/identity (docs/implementation-rules.md §5), adapted
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
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a cache lookup has no matching row.
var ErrNotFound = errors.New("localstore: not found")

const schema = `
CREATE TABLE IF NOT EXISTS whoami_cache (
	agent_id     TEXT NOT NULL,
	owner        TEXT NOT NULL,
	model        TEXT NOT NULL,
	capabilities TEXT NOT NULL DEFAULT '[]',
	project_id   TEXT NOT NULL,
	permissions  TEXT NOT NULL DEFAULT '[]',
	cached_at    TIMESTAMP NOT NULL,
	PRIMARY KEY (agent_id, project_id)
);

CREATE TABLE IF NOT EXISTS tasks (
	id              TEXT PRIMARY KEY,
	namespace_id    TEXT NOT NULL,
	parent_task_id  TEXT,
	title           TEXT NOT NULL,
	description     TEXT NOT NULL DEFAULT '',
	owner_agent_id  TEXT,
	status          TEXT NOT NULL DEFAULT 'todo',
	priority        INTEGER NOT NULL DEFAULT 0,
	due_by          TEXT,
	created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS channels (
	id             TEXT PRIMARY KEY,
	namespace_id   TEXT NOT NULL,
	name           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id              TEXT PRIMARY KEY,
	namespace_id    TEXT NOT NULL,
	channel_id      TEXT NOT NULL,
	agent_id        TEXT NOT NULL,
	event_type      TEXT NOT NULL,
	payload         TEXT NOT NULL DEFAULT '{}',
	note            TEXT,
	created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS kb_articles (
	id               TEXT PRIMARY KEY,
	namespace_id     TEXT NOT NULL,
	title            TEXT NOT NULL,
	body             TEXT NOT NULL,
	frontmatter      TEXT NOT NULL DEFAULT '{}',
	author_agent_id  TEXT NOT NULL,
	created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS kb_links (
	id               TEXT PRIMARY KEY,
	namespace_id     TEXT NOT NULL,
	from_article_id  TEXT NOT NULL,
	to_article_id    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_queue (
	id             TEXT PRIMARY KEY,
	namespace_id   TEXT NOT NULL,
	entity_type    TEXT NOT NULL,
	entity_id      TEXT NOT NULL,
	operation      TEXT NOT NULL,
	payload        TEXT NOT NULL,
	priority       INTEGER NOT NULL DEFAULT 0,
	created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	delivered_at   TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sync_audit (
	id             TEXT PRIMARY KEY,
	namespace_id   TEXT NOT NULL,
	entity_type    TEXT NOT NULL,
	entity_id      TEXT NOT NULL,
	conflict_type  TEXT,
	server_value   TEXT,
	local_value    TEXT,
	resolved_value TEXT,
	resolved_by    TEXT,
	created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// Store wraps a *sql.DB backed by a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the SQLite file at path, applying the
// schema. Callers must Close the returned Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("localstore: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("localstore: apply schema: %w", err)
	}
	if err := migrateWhoAmICacheProjectKey(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("localstore: migrate whoami cache: %w", err)
	}
	return &Store{db: db}, nil
}

func sqliteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = query.Encode()
	return u.String()
}

func migrateWhoAmICacheProjectKey(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(whoami_cache)`)
	if err != nil {
		return err
	}
	primaryColumns := 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if pk > 0 {
			primaryColumns++
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if primaryColumns != 1 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`ALTER TABLE whoami_cache RENAME TO whoami_cache_legacy`,
		`CREATE TABLE whoami_cache (agent_id TEXT NOT NULL, owner TEXT NOT NULL, model TEXT NOT NULL, capabilities TEXT NOT NULL DEFAULT '[]', project_id TEXT NOT NULL, permissions TEXT NOT NULL DEFAULT '[]', cached_at TIMESTAMP NOT NULL, PRIMARY KEY (agent_id, project_id))`,
		`INSERT INTO whoami_cache SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at FROM whoami_cache_legacy`,
		`DROP TABLE whoami_cache_legacy`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for constructing repositories that share
// the same connection. P2: used by cmd/wormholed to wire TaskRepo, EventRepo,
// and KBRepo on the same SQLite file.
func (s *Store) DB() *sql.DB {
	return s.db
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
	c.CachedAt = c.CachedAt.UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO whoami_cache (agent_id, owner, model, capabilities, project_id, permissions, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, project_id) DO UPDATE SET
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
	c, err := scanWhoAmICache(s.db.QueryRowContext(ctx, `
		SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at
		FROM whoami_cache WHERE agent_id = ? ORDER BY cached_at DESC LIMIT 1
	`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return WhoAmICache{}, ErrNotFound
	}
	if err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: get cached whoami for %s: %w", agentID, err)
	}
	return c, nil
}

// GetCachedWhoAmIForProject returns the most recently cached authenticated
// identity for projectID. The local MCP boundary uses this project-scoped
// lookup because every local tools/call supplies a project scope, while the
// single-org daemon configuration historically did not retain an agent id.
func (s *Store) GetCachedWhoAmIForProject(ctx context.Context, projectID string) (WhoAmICache, error) {
	c, err := scanWhoAmICache(s.db.QueryRowContext(ctx, `
		SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at
		FROM whoami_cache WHERE project_id = ?
		ORDER BY cached_at DESC LIMIT 1
	`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return WhoAmICache{}, ErrNotFound
	}
	if err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: get cached whoami for project %s: %w", projectID, err)
	}
	return c, nil
}

// GetCachedWhoAmIForAgentProject returns the cached scope for the exact
// credential identity and project. Authorization must prefer this over a
// project-only lookup so a stale identity cannot lend permissions to a
// replacement credential for the same tenant.
func (s *Store) GetCachedWhoAmIForAgentProject(ctx context.Context, agentID, projectID string) (WhoAmICache, error) {
	c, err := scanWhoAmICache(s.db.QueryRowContext(ctx, `
		SELECT agent_id, owner, model, capabilities, project_id, permissions, cached_at
		FROM whoami_cache WHERE agent_id = ? AND project_id = ?
	`, agentID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return WhoAmICache{}, ErrNotFound
	}
	if err != nil {
		return WhoAmICache{}, fmt.Errorf("localstore: get cached whoami for agent %s project %s: %w", agentID, projectID, err)
	}
	return c, nil
}

func scanWhoAmICache(row interface{ Scan(...any) error }) (WhoAmICache, error) {
	var c WhoAmICache
	var capsJSON, permsJSON, cachedAt string
	if err := row.Scan(&c.AgentID, &c.Owner, &c.Model, &capsJSON, &c.ProjectID, &permsJSON, &cachedAt); err != nil {
		return WhoAmICache{}, err
	}
	if err := json.Unmarshal([]byte(capsJSON), &c.Capabilities); err != nil {
		return WhoAmICache{}, fmt.Errorf("unmarshal capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(permsJSON), &c.Permissions); err != nil {
		return WhoAmICache{}, fmt.Errorf("unmarshal permissions: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, cachedAt)
	if err != nil {
		fields := strings.Fields(cachedAt)
		if len(fields) < 3 {
			return WhoAmICache{}, fmt.Errorf("parse cached_at: %w", err)
		}
		parsed, err = time.Parse("2006-01-02 15:04:05 -0700", strings.Join(fields[:3], " "))
		if err != nil {
			return WhoAmICache{}, fmt.Errorf("parse cached_at: %w", err)
		}
	}
	c.CachedAt = parsed.UTC()
	return c, nil
}

func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
