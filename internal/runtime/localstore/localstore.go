// Package localstore is Gateway's durable local state (RFC-0003 §6.3,
// §7.2). It follows the Store-struct/sentinel-error/wrapped-error shape
// established by internal/core/identity (docs/implementation-rules.md §5),
// adapted for SQLite. Private Gateway state is a strict v6 format epoch;
// tracked portable state remains governed by its own Git format.
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

// ErrEnrolmentAttemptConflict is returned when an active credential profile
// is already bound to a different project or canonical enrolment request.
var ErrEnrolmentAttemptConflict = errors.New("localstore: enrolment attempt conflict")

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

	CREATE TABLE IF NOT EXISTS projects (
		namespace_id TEXT PRIMARY KEY,
		id           TEXT NOT NULL,
		name         TEXT NOT NULL,
		owner        TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS agents (
		namespace_id TEXT NOT NULL,
		id           TEXT NOT NULL,
		owner        TEXT NOT NULL,
		model        TEXT NOT NULL,
		capabilities TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL,
		PRIMARY KEY (namespace_id, id)
	);

	CREATE TABLE IF NOT EXISTS passports (
		namespace_id TEXT NOT NULL,
		id           TEXT NOT NULL,
		agent_id     TEXT NOT NULL,
		project_id   TEXT NOT NULL,
		repositories TEXT NOT NULL,
		roles        TEXT NOT NULL,
		issued_at    TIMESTAMP NOT NULL,
		PRIMARY KEY (namespace_id, id)
	);

	CREATE TABLE IF NOT EXISTS auth_scopes (
		namespace_id TEXT NOT NULL,
		agent_id     TEXT NOT NULL,
		passport_id  TEXT NOT NULL,
		permissions  TEXT NOT NULL,
		PRIMARY KEY (namespace_id, agent_id, passport_id)
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
		name           TEXT NOT NULL,
		created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
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

CREATE TABLE IF NOT EXISTS git_links (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	task_id     TEXT NOT NULL,
	repo        TEXT NOT NULL,
	commit_sha  TEXT NOT NULL,
	summary     TEXT NOT NULL,
	agent_id    TEXT NOT NULL,
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
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

	CREATE TABLE IF NOT EXISTS enrolment_attempts (
	project_id         TEXT NOT NULL,
	idempotency_key    TEXT NOT NULL,
	request_hash       TEXT NOT NULL,
	state              TEXT NOT NULL,
	credential_profile TEXT NOT NULL,
	agent_id           TEXT NOT NULL DEFAULT '',
	passport_id        TEXT NOT NULL DEFAULT '',
	terminal           INTEGER NOT NULL DEFAULT 0 CHECK (terminal IN (0, 1)),
	created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (project_id, idempotency_key)
);

	CREATE UNIQUE INDEX IF NOT EXISTS enrolment_attempts_active_profile
		ON enrolment_attempts(credential_profile) WHERE terminal = 0;

	CREATE TABLE IF NOT EXISTS bootstrap_metadata (
		namespace_id                 TEXT PRIMARY KEY,
		schema_version               INTEGER NOT NULL,
		integration_manifest_metadata TEXT NOT NULL,
		bootstrap_timestamp          TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS integration_manifest_bodies (
		project_id          TEXT NOT NULL,
		manifest_id         TEXT NOT NULL,
		manifest_version    INTEGER NOT NULL CHECK (manifest_version > 0),
		digest              TEXT NOT NULL,
		body                TEXT NOT NULL,
		tool_contract_digest TEXT NOT NULL DEFAULT '',
		resolved_role       TEXT NOT NULL DEFAULT '',
		verified_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (project_id, manifest_id, manifest_version),
		UNIQUE (project_id, manifest_id, manifest_version, digest)
	);

	CREATE TRIGGER IF NOT EXISTS integration_manifest_bodies_no_update
	BEFORE UPDATE ON integration_manifest_bodies
	BEGIN SELECT RAISE(ABORT, 'verified integration manifest bodies are immutable'); END;

	CREATE TRIGGER IF NOT EXISTS integration_manifest_bodies_no_delete
	BEFORE DELETE ON integration_manifest_bodies
	BEGIN SELECT RAISE(ABORT, 'verified integration manifest bodies are retained'); END;

	CREATE TABLE IF NOT EXISTS integration_manifest_project_state (
		project_id       TEXT PRIMARY KEY,
		state            TEXT NOT NULL,
		repository_root  TEXT NOT NULL DEFAULT '',
		updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS integration_manifest_decisions (
		project_id       TEXT NOT NULL,
		manifest_id      TEXT NOT NULL,
		manifest_version INTEGER NOT NULL,
		digest           TEXT NOT NULL,
		decision         TEXT NOT NULL,
		decided_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (project_id, manifest_id, manifest_version, digest)
	);

	CREATE TABLE IF NOT EXISTS integration_manifest_revocations (
		project_id       TEXT NOT NULL,
		manifest_id      TEXT NOT NULL,
		manifest_version INTEGER NOT NULL,
		digest           TEXT NOT NULL,
		revoked_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (project_id, manifest_id, manifest_version, digest)
	);

	CREATE TABLE IF NOT EXISTS integration_manifest_journal (
		operation_id TEXT PRIMARY KEY,
		project_id   TEXT NOT NULL,
		operation    TEXT NOT NULL,
		status       TEXT NOT NULL,
		payload      TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS integration_manifest_audit (
		project_id       TEXT NOT NULL,
		id               TEXT NOT NULL,
		action           TEXT NOT NULL,
		payload          TEXT NOT NULL,
		actor_kind       TEXT NOT NULL DEFAULT 'gateway',
		operation_id     TEXT NOT NULL DEFAULT '',
		manifest_id      TEXT NOT NULL DEFAULT '',
		manifest_version INTEGER,
		manifest_digest  TEXT NOT NULL DEFAULT '',
		outcome          TEXT NOT NULL DEFAULT '',
		reason_code      TEXT NOT NULL DEFAULT '',
		created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (project_id, id)
	);

	CREATE TRIGGER IF NOT EXISTS integration_manifest_audit_no_update
	BEFORE UPDATE ON integration_manifest_audit
	BEGIN SELECT RAISE(ABORT, 'integration manifest audit is append-only'); END;

	CREATE TRIGGER IF NOT EXISTS integration_manifest_audit_no_delete
	BEFORE DELETE ON integration_manifest_audit
	BEGIN SELECT RAISE(ABORT, 'integration manifest audit is append-only'); END;
	`

// Store wraps a *sql.DB backed by a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the SQLite file at path, applying the
// schema. Callers must Close the returned Store.
func Open(path string) (*Store, error) {
	format, err := classifyPrivateDatabase(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("localstore: open %s: %w", path, err)
	}
	if format == privateFormatFresh {
		if err := initializePrivateSchemaV6(context.Background(), db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func sqliteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = query.Encode()
	return u.String()
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for constructing repositories that share
// the same connection. P2: used by cmd/gatewayd to wire TaskRepo, EventRepo,
// and KBRepo on the same SQLite file.
func (s *Store) DB() *sql.DB {
	return s.db
}

// EnrolmentAttemptRecord is Gateway's non-secret durable checkpoint for one
// user-approved enrolment. Raw credential material has no representation in
// this record or its SQLite table.
type EnrolmentAttemptRecord struct {
	ProjectID         string
	IdempotencyKey    string
	RequestHash       string
	State             string
	CredentialProfile string
	AgentID           string
	PassportID        string
	Terminal          bool
}

// ResolveEnrolmentAttempt creates the first durable requested checkpoint or
// resumes the active attempt already owning the credential profile. A new CLI
// process may propose a new key; the stored key wins when the canonical digest
// and project match.
func (s *Store) ResolveEnrolmentAttempt(ctx context.Context, candidate EnrolmentAttemptRecord) (EnrolmentAttemptRecord, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: begin enrolment attempt: %w", err)
	}
	defer tx.Rollback()
	resumable, err := queryResumableEnrolmentAttempt(ctx, tx, candidate.ProjectID, candidate.CredentialProfile)
	if err == nil {
		if resumable.RequestHash != candidate.RequestHash {
			return EnrolmentAttemptRecord{}, false, ErrEnrolmentAttemptConflict
		}
		if err := tx.Commit(); err != nil {
			return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: commit resumed enrolment attempt: %w", err)
		}
		return resumable, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: read resumable enrolment attempt: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO enrolment_attempts
			(project_id, idempotency_key, request_hash, state, credential_profile, terminal)
		VALUES (?, ?, ?, ?, ?, 0)
	`, candidate.ProjectID, candidate.IdempotencyKey, candidate.RequestHash, candidate.State, candidate.CredentialProfile)
	if err != nil {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: create enrolment attempt: %w", err)
	}
	createdRows, err := result.RowsAffected()
	if err != nil {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: inspect enrolment attempt insert: %w", err)
	}
	record, err := queryEnrolmentAttemptByKey(ctx, tx, candidate.ProjectID, candidate.IdempotencyKey)
	if errors.Is(err, sql.ErrNoRows) {
		record, err = queryActiveEnrolmentAttempt(ctx, tx, candidate.ProjectID, candidate.CredentialProfile)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return EnrolmentAttemptRecord{}, false, ErrEnrolmentAttemptConflict
	}
	if err != nil {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: read enrolment attempt: %w", err)
	}
	if record.RequestHash != candidate.RequestHash || record.CredentialProfile != candidate.CredentialProfile {
		return EnrolmentAttemptRecord{}, false, ErrEnrolmentAttemptConflict
	}
	if err := tx.Commit(); err != nil {
		return EnrolmentAttemptRecord{}, false, fmt.Errorf("localstore: commit enrolment attempt: %w", err)
	}
	return record, createdRows == 1, nil
}

func queryResumableEnrolmentAttempt(ctx context.Context, queryer enrolmentAttemptQueryRower, projectID, credentialProfile string) (EnrolmentAttemptRecord, error) {
	return scanEnrolmentAttempt(queryer.QueryRowContext(ctx, `
		SELECT project_id, idempotency_key, request_hash, state, credential_profile,
		       agent_id, passport_id, terminal
		FROM enrolment_attempts
		WHERE project_id = ? AND credential_profile = ?
		  AND (terminal = 0 OR state = 'ready')
		ORDER BY CASE WHEN state = 'ready' THEN 0 ELSE 1 END
		LIMIT 1
	`, projectID, credentialProfile))
}

func queryEnrolmentAttemptByKey(ctx context.Context, queryer enrolmentAttemptQueryRower, projectID, idempotencyKey string) (EnrolmentAttemptRecord, error) {
	return scanEnrolmentAttempt(queryer.QueryRowContext(ctx, `
		SELECT project_id, idempotency_key, request_hash, state, credential_profile,
		       agent_id, passport_id, terminal
		FROM enrolment_attempts
		WHERE project_id = ? AND idempotency_key = ?
	`, projectID, idempotencyKey))
}

type enrolmentAttemptQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryActiveEnrolmentAttempt(ctx context.Context, queryer enrolmentAttemptQueryRower, projectID, credentialProfile string) (EnrolmentAttemptRecord, error) {
	return scanEnrolmentAttempt(queryer.QueryRowContext(ctx, `
		SELECT project_id, idempotency_key, request_hash, state, credential_profile,
		       agent_id, passport_id, terminal
		FROM enrolment_attempts
		WHERE project_id = ? AND credential_profile = ? AND terminal = 0
	`, projectID, credentialProfile))
}

func (s *Store) getActiveEnrolmentAttempt(ctx context.Context, projectID, credentialProfile string) (EnrolmentAttemptRecord, error) {
	record, err := queryActiveEnrolmentAttempt(ctx, s.db, projectID, credentialProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrolmentAttemptRecord{}, ErrNotFound
	}
	if err != nil {
		return EnrolmentAttemptRecord{}, fmt.Errorf("localstore: get active enrolment attempt: %w", err)
	}
	return record, nil
}

// UpdateEnrolmentAttempt advances a durable checkpoint and optional identity
// references. It refuses to update a record whose immutable binding changed.
func (s *Store) UpdateEnrolmentAttempt(ctx context.Context, record EnrolmentAttemptRecord, state, agentID, passportID string, terminal bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE enrolment_attempts
		SET state = ?, agent_id = ?, passport_id = ?, terminal = ?, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND idempotency_key = ? AND request_hash = ? AND credential_profile = ?
	`, state, agentID, passportID, terminal, record.ProjectID, record.IdempotencyKey, record.RequestHash, record.CredentialProfile)
	if err != nil {
		return fmt.Errorf("localstore: update enrolment attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect enrolment attempt update: %w", err)
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}

func scanEnrolmentAttempt(row interface{ Scan(...any) error }) (EnrolmentAttemptRecord, error) {
	var record EnrolmentAttemptRecord
	var terminal int
	err := row.Scan(&record.ProjectID, &record.IdempotencyKey, &record.RequestHash, &record.State,
		&record.CredentialProfile, &record.AgentID, &record.PassportID, &terminal)
	record.Terminal = terminal != 0
	return record, err
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
