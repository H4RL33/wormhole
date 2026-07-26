// Package store owns the Gateway-local SQLite representation of Code Graph
// metadata. Its schema and migration ledger are component-local: Fabric's
// PostgreSQL migrations never create or alter these tables.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
)

const CurrentSchemaVersion = 2

var ErrSchemaTooNew = errors.New("codegraph store: schema is newer than this binary")

var ErrProjectScope = errors.New("codegraph store: project scope mismatch")

var ErrNotFound = errors.New("codegraph store: not found")

var ErrNotCandidate = errors.New("codegraph store: revision is not a candidate")

var ErrDuplicateID = errors.New("codegraph store: duplicate deterministic id")

var ErrReservedDiagnosticID = errors.New("codegraph store: diagnostic id uses reserved namespace")

// ErrDisabling rejects snapshot admission after destructive disablement has
// begun. Existing SQLite snapshots remain coherent and may finish normally.
var ErrDisabling = errors.New("codegraph store: project is being disabled")

var ErrBuildInProgress = errors.New("codegraph store: build already in progress")

// afterRecoverInterruptedLifecycle is a test seam invoked after startup
// lifecycle inspection while the cross-process writer barrier is held.
// Production leaves it nil.
var afterRecoverInterruptedLifecycle func()

var processLeaseProbe = processLeaseLiveness

const systemDiagnosticPrefix = "@wormhole/system/"

type RevisionState string

const (
	RevisionCandidate RevisionState = "candidate"
	RevisionActive    RevisionState = "active"
	RevisionRetired   RevisionState = "retired"
	RevisionFailed    RevisionState = "failed"
)

type NodeKind string

const (
	NodeRepository NodeKind = "repository"
	NodePackage    NodeKind = "package"
	NodeFile       NodeKind = "file"
	NodeSymbol     NodeKind = "symbol"
)

type Relationship string

const (
	RelationshipContains   Relationship = "contains"
	RelationshipDefines    Relationship = "defines"
	RelationshipImports    Relationship = "imports"
	RelationshipCalls      Relationship = "calls"
	RelationshipReferences Relationship = "references"
	RelationshipUsesType   Relationship = "uses_type"
)

type Provenance string

const (
	ProvenanceGoPackages Provenance = "go_packages"
	ProvenanceGoTypes    Provenance = "go_types"
	ProvenanceGoAST      Provenance = "go_ast"
	ProvenanceParser     Provenance = "parser"
	ProvenanceHeuristic  Provenance = "heuristic"
)

type DiagnosticSeverity string

const (
	DiagnosticInfo    DiagnosticSeverity = "info"
	DiagnosticWarning DiagnosticSeverity = "warning"
	DiagnosticError   DiagnosticSeverity = "error"
)

type Revision struct {
	ProjectID     string
	ID            string
	State         RevisionState
	IndexedCommit string
	CreatedAt     time.Time
	CompletedAt   *time.Time
}

type Node struct {
	ProjectID  string
	RevisionID string
	ID         string
	Kind       NodeKind
	Name       string
	Path       string
}

type File struct {
	ProjectID   string
	RevisionID  string
	ID          string
	Path        string
	IndexedHash string
	ByteSize    int64
}

type Symbol struct {
	ProjectID     string
	RevisionID    string
	ID            string
	FileID        string
	QualifiedName string
	Signature     string
	StartByte     int64
	EndByte       int64
	StartLine     int
	EndLine       int
}

type Edge struct {
	ProjectID    string
	RevisionID   string
	ID           string
	SourceNodeID string
	TargetNodeID string
	Relationship Relationship
	Confidence   float64
	Provenance   Provenance
}

type Diagnostic struct {
	ProjectID  string
	RevisionID string
	ID         string
	Severity   DiagnosticSeverity
	Code       string
	Message    string
	CreatedAt  time.Time
}

type PayloadCounts struct {
	Nodes   int
	Files   int
	Symbols int
	Edges   int
}

// PublicationReader is the read-only view of the SQLite publication
// transaction exposed to a caller-owned authorization guard.
type PublicationReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type PublicationGuard func(context.Context, PublicationReader) error

const schemaVersionOne = `
CREATE TABLE codegraph_config (
    project_id                  TEXT PRIMARY KEY,
    enabled                     INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    canonical_remote            TEXT NOT NULL,
    active_checkout             TEXT NOT NULL,
    project_source_byte_ceiling INTEGER NOT NULL CHECK (project_source_byte_ceiling > 0),
    last_successful_build       TIMESTAMP,
    active_revision_id          TEXT
);

CREATE TABLE codegraph_revisions (
    project_id       TEXT NOT NULL,
    revision_id      TEXT NOT NULL,
    state            TEXT NOT NULL CHECK (state IN ('candidate', 'active', 'retired', 'failed')),
    indexed_commit   TEXT NOT NULL,
    created_at       TIMESTAMP NOT NULL,
    completed_at     TIMESTAMP,
    PRIMARY KEY (project_id, revision_id)
);

CREATE UNIQUE INDEX codegraph_one_active_revision
    ON codegraph_revisions(project_id) WHERE state = 'active';

CREATE TABLE codegraph_nodes (
    project_id  TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('repository', 'package', 'file', 'symbol')),
    name        TEXT NOT NULL,
    path        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, revision_id, node_id)
);

CREATE TABLE codegraph_files (
    project_id   TEXT NOT NULL,
    revision_id  TEXT NOT NULL,
    file_id      TEXT NOT NULL,
    path         TEXT NOT NULL,
    indexed_hash TEXT NOT NULL,
    byte_size    INTEGER NOT NULL CHECK (byte_size >= 0),
    PRIMARY KEY (project_id, revision_id, file_id),
    UNIQUE (project_id, revision_id, path)
);

CREATE TABLE codegraph_symbols (
    project_id     TEXT NOT NULL,
    revision_id    TEXT NOT NULL,
    symbol_id      TEXT NOT NULL,
    file_id        TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    signature      TEXT NOT NULL,
    start_byte     INTEGER NOT NULL,
    end_byte       INTEGER NOT NULL,
    start_line     INTEGER NOT NULL,
    end_line       INTEGER NOT NULL,
    PRIMARY KEY (project_id, revision_id, symbol_id)
);

CREATE TABLE codegraph_edges (
    project_id       TEXT NOT NULL,
    revision_id      TEXT NOT NULL,
    edge_id          TEXT NOT NULL,
    source_node_id   TEXT NOT NULL,
    target_node_id   TEXT NOT NULL,
    relationship     TEXT NOT NULL CHECK (relationship IN ('contains', 'defines', 'imports', 'calls', 'references', 'uses_type')),
    confidence       REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    provenance       TEXT NOT NULL CHECK (provenance IN ('go_packages', 'go_types', 'go_ast', 'parser', 'heuristic')),
    PRIMARY KEY (project_id, revision_id, edge_id)
);

CREATE INDEX codegraph_edges_source_traversal
    ON codegraph_edges(project_id, revision_id, source_node_id, relationship, confidence);
CREATE INDEX codegraph_edges_target_traversal
    ON codegraph_edges(project_id, revision_id, target_node_id, relationship, confidence);
CREATE INDEX codegraph_symbols_qualified_lookup
    ON codegraph_symbols(project_id, revision_id, qualified_name);

CREATE TABLE codegraph_diagnostics (
    project_id    TEXT NOT NULL,
    revision_id   TEXT NOT NULL,
    diagnostic_id TEXT NOT NULL,
    severity      TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error')),
    code          TEXT NOT NULL,
    message       TEXT NOT NULL,
    created_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (project_id, revision_id, diagnostic_id)
);
`

const schemaVersionTwo = `
CREATE TABLE codegraph_lifecycle (
	project_id  TEXT PRIMARY KEY,
	state       TEXT NOT NULL CHECK (state IN ('building', 'disabling')),
	build_token TEXT,
	owner_pid   INTEGER NOT NULL,
	owner_start TEXT NOT NULL,
	build_owner_pid   INTEGER,
	build_owner_start TEXT
);
`

// Store uses the Gateway-owned SQLite handle but owns only codegraph_* tables.
// The caller retains ownership of db and closes it after all runtime packages.
type Store struct {
	db        *sql.DB
	projectID string
	writeMu   sync.RWMutex
}

// Open applies component-local migrations and returns a Code Graph store
// without treating another process's live candidate as interrupted.
func Open(ctx context.Context, db *sql.DB, projectID string) (*Store, error) {
	if db == nil {
		return nil, errors.New("codegraph store: nil database")
	}
	if projectID == "" {
		return nil, fmt.Errorf("%w: project id is required", ErrProjectScope)
	}
	if err := requireWAL(ctx, db); err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	store := &Store{db: db, projectID: projectID}
	return store, nil
}

func requireWAL(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("codegraph store: enable WAL journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("codegraph store: WAL journal mode required, got %q", mode)
	}
	return nil
}

// OpenRecovering is the Gateway startup path. Only process startup may decide
// that durable candidate/disable markers were abandoned by a prior process.
func OpenRecovering(ctx context.Context, db *sql.DB, projectID string) (*Store, error) {
	store, err := Open(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	if err := store.recoverAtStartup(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("codegraph store: acquire migration connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("codegraph store: begin migration: %w", err)
	}
	inTransaction := true
	defer func() {
		if inTransaction {
			_, _ = connection.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, err := connection.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS codegraph_schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("codegraph store: create migration ledger: %w", err)
	}
	var version int
	if err := connection.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM codegraph_schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("codegraph store: read migration version: %w", err)
	}
	if version > CurrentSchemaVersion {
		return fmt.Errorf("%w: database=%d binary=%d", ErrSchemaTooNew, version, CurrentSchemaVersion)
	}
	if version < 1 {
		if _, err := connection.ExecContext(ctx, schemaVersionOne); err != nil {
			return fmt.Errorf("codegraph store: apply schema version 1: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO codegraph_schema_migrations (version, applied_at) VALUES (?, ?)`, 1, time.Now().UTC()); err != nil {
			return fmt.Errorf("codegraph store: record schema version 1: %w", err)
		}
	}
	if version < 2 {
		if _, err := connection.ExecContext(ctx, schemaVersionTwo); err != nil {
			return fmt.Errorf("codegraph store: apply schema version 2: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO codegraph_schema_migrations (version, applied_at) VALUES (?, ?)`, 2, time.Now().UTC()); err != nil {
			return fmt.Errorf("codegraph store: record schema version 2: %w", err)
		}
	}
	if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("codegraph store: commit migration: %w", err)
	}
	inTransaction = false
	return nil
}

// PutProjectConfig persists exactly one configuration row for project.ProjectID.
func (s *Store) PutProjectConfig(ctx context.Context, project config.Project) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if project.ProjectID != s.projectID {
		return fmt.Errorf("%w: store=%q config=%q", ErrProjectScope, s.projectID, project.ProjectID)
	}
	if project.CanonicalRemote != "" {
		canonical, err := config.CanonicalRemote(project.CanonicalRemote)
		if err != nil {
			return err
		}
		project.CanonicalRemote = canonical
	}
	if err := config.ValidateProject(project); err != nil {
		return err
	}
	var lastSuccessfulBuild any
	if project.LastSuccessfulBuild != nil {
		lastSuccessfulBuild = project.LastSuccessfulBuild.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("codegraph store: begin project config %q: %w", project.ProjectID, err)
	}
	defer tx.Rollback()
	if err := s.rejectDisabling(ctx, tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO codegraph_config (
			project_id, enabled, canonical_remote, active_checkout,
			project_source_byte_ceiling, last_successful_build
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			enabled = excluded.enabled,
			canonical_remote = excluded.canonical_remote,
			active_checkout = excluded.active_checkout,
			project_source_byte_ceiling = excluded.project_source_byte_ceiling,
			last_successful_build = excluded.last_successful_build
	`, project.ProjectID, project.Enabled, project.CanonicalRemote, project.ActiveCheckout,
		project.ProjectSourceByteCeiling, lastSuccessfulBuild)
	if err != nil {
		return fmt.Errorf("codegraph store: put project config %q: %w", project.ProjectID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit project config %q: %w", project.ProjectID, err)
	}
	return nil
}

// ProjectConfig reads only the requested project. An absent row is the
// project-bound disabled default rather than an implicit global configuration.
func (s *Store) ProjectConfig(ctx context.Context) (config.Project, error) {
	project := config.Project{ProjectID: s.projectID}
	var enabled bool
	var lastSuccessfulBuild sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, canonical_remote, active_checkout,
		       project_source_byte_ceiling, last_successful_build
		FROM codegraph_config
		WHERE project_id = ?
	`, s.projectID).Scan(&enabled, &project.CanonicalRemote, &project.ActiveCheckout,
		&project.ProjectSourceByteCeiling, &lastSuccessfulBuild)
	if errors.Is(err, sql.ErrNoRows) {
		return config.DefaultProject(s.projectID), nil
	}
	if err != nil {
		return config.Project{}, fmt.Errorf("codegraph store: get project config %q: %w", s.projectID, err)
	}
	project.Enabled = enabled
	if lastSuccessfulBuild.Valid {
		build := lastSuccessfulBuild.Time.UTC()
		project.LastSuccessfulBuild = &build
	}
	return project, nil
}

// DatabaseSize reports the current SQLite allocation without modifying graph
// state. The component shares Gateway's database, so this is the local graph
// store's observable backing-database size rather than a source-file count.
func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("codegraph store: read page count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("codegraph store: read page size: %w", err)
	}
	return pageCount * pageSize, nil
}

// BeginBuild acquires the cross-handle project build lease used by both MCP
// rebuilds and human lifecycle builds. Disablement can flip the same row to a
// draining state before deleting graph data.
func (s *Store) BeginBuild(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("codegraph store: build token is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ownerPID, ownerStart := currentProcessLeaseIdentity()
	for {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO codegraph_lifecycle (
				project_id, state, build_token, owner_pid, owner_start, build_owner_pid, build_owner_start
			) VALUES (?, 'building', ?, ?, ?, ?, ?) ON CONFLICT(project_id) DO NOTHING
		`, s.projectID, token, ownerPID, ownerStart, ownerPID, ownerStart)
		if err != nil {
			return fmt.Errorf("codegraph store: acquire build lease: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("codegraph store: inspect build lease: %w", err)
		}
		if rows == 1 {
			return nil
		}
		var state string
		var storedToken sql.NullString
		var buildOwnerPID sql.NullInt64
		var buildOwnerStart sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT state, build_token, build_owner_pid, build_owner_start
			FROM codegraph_lifecycle WHERE project_id = ?
		`, s.projectID).Scan(&state, &storedToken, &buildOwnerPID, &buildOwnerStart)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("codegraph store: read occupied build lease: %w", err)
		}
		if state == "disabling" {
			return ErrDisabling
		}
		if !storedToken.Valid || !buildOwnerPID.Valid || !buildOwnerStart.Valid ||
			processLeaseProbe(int(buildOwnerPID.Int64), buildOwnerStart.String) != leaseDead {
			return ErrBuildInProgress
		}
		reclaimed, err := s.reclaimDeadBuild(ctx, storedToken.String, int(buildOwnerPID.Int64), buildOwnerStart.String)
		if err != nil {
			return err
		}
		if !reclaimed {
			continue
		}
	}
}

// reclaimDeadBuild removes exactly the lease identity that was positively
// verified dead and fails its same-token candidate in the same transaction.
// A replaced lease therefore cannot be cleared by an old observation.
func (s *Store) reclaimDeadBuild(ctx context.Context, token string, ownerPID int, ownerStart string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("codegraph store: begin stale build reclamation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM codegraph_lifecycle
		WHERE project_id = ? AND state = 'building' AND build_token = ?
		  AND build_owner_pid = ? AND build_owner_start = ?
	`, s.projectID, token, ownerPID, ownerStart)
	if err != nil {
		return false, fmt.Errorf("codegraph store: reclaim stale build lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("codegraph store: inspect stale build reclamation: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	for _, table := range []string{"codegraph_edges", "codegraph_symbols", "codegraph_files", "codegraph_nodes"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ? AND revision_id = ?`, s.projectID, token); err != nil {
			return false, fmt.Errorf("codegraph store: clean %s for interrupted build %q: %w", table, token, err)
		}
	}
	completedAt := time.Now().UTC()
	result, err = tx.ExecContext(ctx, `
		UPDATE codegraph_revisions SET state = 'failed', completed_at = ?
		WHERE project_id = ? AND revision_id = ? AND state = 'candidate'
	`, completedAt, s.projectID, token)
	if err != nil {
		return false, fmt.Errorf("codegraph store: fail interrupted build candidate %q: %w", token, err)
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("codegraph store: inspect interrupted build candidate %q: %w", token, err)
	}
	if rows == 1 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO codegraph_diagnostics (
				project_id, revision_id, diagnostic_id, severity, code, message, created_at
			) VALUES (?, ?, ?, 'error', 'interrupted_candidate', 'candidate build owner exited before publication', ?)
		`, s.projectID, token, systemDiagnosticPrefix+"interrupted_candidate", completedAt); err != nil {
			return false, fmt.Errorf("codegraph store: diagnose interrupted build candidate %q: %w", token, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("codegraph store: commit stale build reclamation: %w", err)
	}
	return true, nil
}

// EndBuild releases a matching build lease. If disablement is draining this
// build, only the token is cleared so the disabler remains authoritative.
func (s *Store) EndBuild(ctx context.Context, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("codegraph store: begin build lease release: %w", err)
	}
	defer tx.Rollback()
	var state string
	var stored sql.NullString
	var ownerPID int
	var ownerStart string
	err = tx.QueryRowContext(ctx, `SELECT state, build_token, owner_pid, owner_start FROM codegraph_lifecycle WHERE project_id = ?`, s.projectID).Scan(&state, &stored, &ownerPID, &ownerStart)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("codegraph store: read build lease for release: %w", err)
	}
	if !stored.Valid || stored.String != token {
		return errors.New("codegraph store: build lease token mismatch")
	}
	if state == "disabling" {
		_, err = tx.ExecContext(ctx, `
			UPDATE codegraph_lifecycle
			SET build_token = NULL, build_owner_pid = NULL, build_owner_start = NULL
			WHERE project_id = ? AND state = 'disabling' AND build_token = ?
		`, s.projectID, token)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM codegraph_lifecycle WHERE project_id = ? AND state = 'building' AND build_token = ?`, s.projectID, token)
	}
	if err != nil {
		return fmt.Errorf("codegraph store: release build lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit build lease release: %w", err)
	}
	if state == "disabling" && processLeaseProbe(ownerPID, ownerStart) == leaseDead {
		if err := s.finishDisableLocked(ctx, true); err != nil {
			return fmt.Errorf("codegraph store: finish abandoned disable after build drain: %w", err)
		}
	}
	return nil
}

// CreateCandidate creates an immutable revision namespace for candidate
// payload writes and ensures the bound project has a disabled-by-default
// configuration row to hold the future active pointer.
func (s *Store) CreateCandidate(ctx context.Context, revision Revision) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.checkProject(revision.ProjectID); err != nil {
		return err
	}
	if revision.ID == "" || revision.IndexedCommit == "" || revision.CreatedAt.IsZero() {
		return errors.New("codegraph store: candidate id, indexed commit, and creation time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("codegraph store: begin candidate %q: %w", revision.ID, err)
	}
	defer tx.Rollback()
	defaultProject := config.DefaultProject(s.projectID)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO codegraph_config (
			project_id, enabled, canonical_remote, active_checkout,
			project_source_byte_ceiling, last_successful_build, active_revision_id
		) VALUES (?, 0, '', '', ?, NULL, NULL)
		ON CONFLICT(project_id) DO NOTHING
	`, s.projectID, defaultProject.ProjectSourceByteCeiling); err != nil {
		return fmt.Errorf("codegraph store: ensure project config %q: %w", s.projectID, err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO codegraph_revisions (
			project_id, revision_id, state, indexed_commit, created_at, completed_at
		) VALUES (?, ?, 'candidate', ?, ?, NULL)
		ON CONFLICT(project_id, revision_id) DO NOTHING
	`, s.projectID, revision.ID, revision.IndexedCommit, revision.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("codegraph store: create candidate %q: %w", revision.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("codegraph store: inspect candidate %q: %w", revision.ID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: revision %q", ErrDuplicateID, revision.ID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit candidate %q: %w", revision.ID, err)
	}
	return nil
}

func (s *Store) PutNode(ctx context.Context, node Node) error {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.checkProject(node.ProjectID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_nodes (project_id, revision_id, node_id, kind, name, path)
		SELECT ?, ?, ?, ?, ?, ?
		FROM codegraph_revisions AS revision
		WHERE revision.project_id = ? AND revision.revision_id = ? AND revision.state = 'candidate'
		ON CONFLICT DO NOTHING
	`, s.projectID, node.RevisionID, node.ID, node.Kind, node.Name, node.Path,
		s.projectID, node.RevisionID)
	return s.candidateInserted(ctx, result, err, "node", node.ID, node.RevisionID)
}

func (s *Store) PutFile(ctx context.Context, file File) error {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.checkProject(file.ProjectID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_files (
			project_id, revision_id, file_id, path, indexed_hash, byte_size
		)
		SELECT ?, ?, ?, ?, ?, ?
		FROM codegraph_revisions AS revision
		WHERE revision.project_id = ? AND revision.revision_id = ? AND revision.state = 'candidate'
		ON CONFLICT DO NOTHING
	`, s.projectID, file.RevisionID, file.ID, file.Path, file.IndexedHash, file.ByteSize,
		s.projectID, file.RevisionID)
	return s.candidateInserted(ctx, result, err, "file", file.ID, file.RevisionID)
}

func (s *Store) PutSymbol(ctx context.Context, symbol Symbol) error {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.checkProject(symbol.ProjectID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_symbols (
			project_id, revision_id, symbol_id, file_id, qualified_name,
			signature, start_byte, end_byte, start_line, end_line
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM codegraph_revisions AS revision
		WHERE revision.project_id = ? AND revision.revision_id = ? AND revision.state = 'candidate'
		ON CONFLICT DO NOTHING
	`, s.projectID, symbol.RevisionID, symbol.ID, symbol.FileID, symbol.QualifiedName,
		symbol.Signature, symbol.StartByte, symbol.EndByte, symbol.StartLine, symbol.EndLine,
		s.projectID, symbol.RevisionID)
	return s.candidateInserted(ctx, result, err, "symbol", symbol.ID, symbol.RevisionID)
}

func (s *Store) PutEdge(ctx context.Context, edge Edge) error {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.checkProject(edge.ProjectID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_edges (
			project_id, revision_id, edge_id, source_node_id, target_node_id,
			relationship, confidence, provenance
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		FROM codegraph_revisions AS revision
		WHERE revision.project_id = ? AND revision.revision_id = ? AND revision.state = 'candidate'
		ON CONFLICT DO NOTHING
	`, s.projectID, edge.RevisionID, edge.ID, edge.SourceNodeID, edge.TargetNodeID,
		edge.Relationship, edge.Confidence, edge.Provenance, s.projectID, edge.RevisionID)
	return s.candidateInserted(ctx, result, err, "edge", edge.ID, edge.RevisionID)
}

// PutDiagnostic records metadata-only build output for a candidate. Callers
// must not place source bodies or returned context packages in Message.
func (s *Store) PutDiagnostic(ctx context.Context, diagnostic Diagnostic) error {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.checkProject(diagnostic.ProjectID); err != nil {
		return err
	}
	if diagnostic.CreatedAt.IsZero() {
		return errors.New("codegraph store: diagnostic creation time is required")
	}
	if strings.HasPrefix(diagnostic.ID, systemDiagnosticPrefix) {
		return fmt.Errorf("%w: %q", ErrReservedDiagnosticID, diagnostic.ID)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_diagnostics (
			project_id, revision_id, diagnostic_id, severity, code, message, created_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?
		FROM codegraph_revisions AS revision
		WHERE revision.project_id = ? AND revision.revision_id = ? AND revision.state = 'candidate'
		ON CONFLICT DO NOTHING
	`, s.projectID, diagnostic.RevisionID, diagnostic.ID, diagnostic.Severity,
		diagnostic.Code, diagnostic.Message, diagnostic.CreatedAt.UTC(),
		s.projectID, diagnostic.RevisionID)
	return s.candidateInserted(ctx, result, err, "diagnostic", diagnostic.ID, diagnostic.RevisionID)
}

func (s *Store) candidateInserted(ctx context.Context, result sql.Result, err error, kind, id, revisionID string) error {
	if err != nil {
		return fmt.Errorf("codegraph store: put %s %q: %w", kind, id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("codegraph store: inspect %s %q: %w", kind, id, err)
	}
	if rows == 1 {
		return nil
	}
	if err := s.ensureCandidate(ctx, revisionID); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s %q", ErrDuplicateID, kind, id)
}

func (s *Store) checkProject(projectID string) error {
	if projectID != s.projectID {
		return fmt.Errorf("%w: store=%q record=%q", ErrProjectScope, s.projectID, projectID)
	}
	return nil
}

func (s *Store) ensureCandidate(ctx context.Context, revisionID string) error {
	var lifecycleState string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM codegraph_lifecycle WHERE project_id = ?`, s.projectID).Scan(&lifecycleState); err == nil && lifecycleState == "disabling" {
		return ErrDisabling
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("codegraph store: inspect lifecycle before candidate write: %w", err)
	}
	var state RevisionState
	err := s.db.QueryRowContext(ctx, `
		SELECT state FROM codegraph_revisions
		WHERE project_id = ? AND revision_id = ?
	`, s.projectID, revisionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: revision %q", ErrNotFound, revisionID)
	}
	if err != nil {
		return fmt.Errorf("codegraph store: inspect candidate %q: %w", revisionID, err)
	}
	if state != RevisionCandidate {
		return fmt.Errorf("%w: revision %q state %q", ErrNotCandidate, revisionID, state)
	}
	return nil
}

func (s *Store) Revision(ctx context.Context, revisionID string) (Revision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, `
		SELECT project_id, revision_id, state, indexed_commit, created_at, completed_at
		FROM codegraph_revisions
		WHERE project_id = ? AND revision_id = ?
	`, s.projectID, revisionID), revisionID)
}

func (s *Store) ActiveRevision(ctx context.Context) (Revision, error) {
	return scanRevision(s.db.QueryRowContext(ctx, `
		SELECT r.project_id, r.revision_id, r.state, r.indexed_commit, r.created_at, r.completed_at
		FROM codegraph_config AS c
		JOIN codegraph_revisions AS r
		  ON r.project_id = c.project_id AND r.revision_id = c.active_revision_id
		WHERE c.project_id = ? AND r.project_id = ? AND r.state = 'active'
	`, s.projectID, s.projectID), "active")
}

func scanRevision(row interface{ Scan(...any) error }, description string) (Revision, error) {
	var revision Revision
	var completed sql.NullTime
	err := row.Scan(&revision.ProjectID, &revision.ID, &revision.State, &revision.IndexedCommit, &revision.CreatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, fmt.Errorf("%w: revision %q", ErrNotFound, description)
	}
	if err != nil {
		return Revision{}, fmt.Errorf("codegraph store: scan revision %q: %w", description, err)
	}
	revision.CreatedAt = revision.CreatedAt.UTC()
	if completed.Valid {
		value := completed.Time.UTC()
		revision.CompletedAt = &value
	}
	return revision, nil
}

// Snapshot pins all reads to one SQLite transaction and one project/revision.
type Snapshot struct {
	tx       *sql.Tx
	revision Revision
}

// SymbolRecord is bounded query metadata assembled without source contents.
type SymbolRecord struct {
	Symbol       Symbol
	Name         string
	FilePath     string
	IndexedHash  string
	FileByteSize int64
}

func (snapshot *Snapshot) Revision() Revision {
	return snapshot.revision
}

// ProjectConfig reads the project configuration from the same SQLite
// transaction that pins this revision snapshot. Publication validators use it
// to bind an active-pointer swap to the approved checkout atomically.
func (snapshot *Snapshot) ProjectConfig(ctx context.Context) (config.Project, error) {
	project := config.Project{ProjectID: snapshot.revision.ProjectID}
	var enabled bool
	var lastSuccessfulBuild sql.NullTime
	err := snapshot.tx.QueryRowContext(ctx, `
		SELECT enabled, canonical_remote, active_checkout,
		       project_source_byte_ceiling, last_successful_build
		FROM codegraph_config
		WHERE project_id = ?
	`, snapshot.revision.ProjectID).Scan(&enabled, &project.CanonicalRemote, &project.ActiveCheckout,
		&project.ProjectSourceByteCeiling, &lastSuccessfulBuild)
	if err != nil {
		return config.Project{}, fmt.Errorf("codegraph store: read snapshot project config: %w", err)
	}
	project.Enabled = enabled
	if lastSuccessfulBuild.Valid {
		build := lastSuccessfulBuild.Time.UTC()
		project.LastSuccessfulBuild = &build
	}
	return project, nil
}

func (snapshot *Snapshot) Nodes(ctx context.Context) ([]Node, error) {
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, node_id, kind, name, path
		FROM codegraph_nodes
		WHERE project_id = ? AND revision_id = ?
		ORDER BY node_id
	`, snapshot.revision.ProjectID, snapshot.revision.ID)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read nodes: %w", err)
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var node Node
		if err := rows.Scan(&node.ProjectID, &node.RevisionID, &node.ID, &node.Kind, &node.Name, &node.Path); err != nil {
			return nil, fmt.Errorf("codegraph store: scan node: %w", err)
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (snapshot *Snapshot) Files(ctx context.Context) ([]File, error) {
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, file_id, path, indexed_hash, byte_size
		FROM codegraph_files
		WHERE project_id = ? AND revision_id = ?
		ORDER BY file_id
	`, snapshot.revision.ProjectID, snapshot.revision.ID)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read files: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var file File
		if err := rows.Scan(&file.ProjectID, &file.RevisionID, &file.ID, &file.Path, &file.IndexedHash, &file.ByteSize); err != nil {
			return nil, fmt.Errorf("codegraph store: scan file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (snapshot *Snapshot) Symbols(ctx context.Context) ([]Symbol, error) {
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, symbol_id, file_id, qualified_name,
		       signature, start_byte, end_byte, start_line, end_line
		FROM codegraph_symbols
		WHERE project_id = ? AND revision_id = ?
		ORDER BY symbol_id
	`, snapshot.revision.ProjectID, snapshot.revision.ID)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read symbols: %w", err)
	}
	defer rows.Close()
	var symbols []Symbol
	for rows.Next() {
		var symbol Symbol
		if err := rows.Scan(&symbol.ProjectID, &symbol.RevisionID, &symbol.ID, &symbol.FileID,
			&symbol.QualifiedName, &symbol.Signature, &symbol.StartByte, &symbol.EndByte,
			&symbol.StartLine, &symbol.EndLine); err != nil {
			return nil, fmt.Errorf("codegraph store: scan symbol: %w", err)
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (snapshot *Snapshot) Edges(ctx context.Context) ([]Edge, error) {
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, edge_id, source_node_id, target_node_id,
		       relationship, confidence, provenance
		FROM codegraph_edges
		WHERE project_id = ? AND revision_id = ?
		ORDER BY edge_id
	`, snapshot.revision.ProjectID, snapshot.revision.ID)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read edges: %w", err)
	}
	defer rows.Close()
	var edges []Edge
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.ProjectID, &edge.RevisionID, &edge.ID, &edge.SourceNodeID,
			&edge.TargetNodeID, &edge.Relationship, &edge.Confidence, &edge.Provenance); err != nil {
			return nil, fmt.Errorf("codegraph store: scan edge: %w", err)
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// SearchSymbols returns at most limit lexical candidates inside this pinned
// revision. SQL applies the cap before rows enter Go memory.
func (snapshot *Snapshot) SearchSymbols(ctx context.Context, entries, terms []string, limit int) ([]SymbolRecord, int, error) {
	if limit <= 0 {
		return nil, 0, errors.New("codegraph store: symbol search limit must be positive")
	}
	conditions := make([]string, 0, len(entries)*2+len(terms)*4)
	filterArguments := make([]any, 0, 2+len(entries)*2+len(terms)*4)
	filterArguments = append(filterArguments, snapshot.revision.ProjectID, snapshot.revision.ID)
	for _, entry := range entries {
		conditions = append(conditions, `(s.qualified_name = ? OR n.name = ?)`)
		filterArguments = append(filterArguments, entry, entry)
	}
	for _, term := range terms {
		pattern := "%" + escapeLike(strings.ToLower(term)) + "%"
		conditions = append(conditions, `(lower(n.name) LIKE ? ESCAPE '\' OR lower(s.signature) LIKE ? ESCAPE '\' OR lower(f.path) LIKE ? ESCAPE '\' OR lower(s.qualified_name) LIKE ? ESCAPE '\')`)
		filterArguments = append(filterArguments, pattern, pattern, pattern, pattern)
	}
	if len(conditions) == 0 {
		return nil, 0, nil
	}
	fromAndWhere := `
		FROM codegraph_symbols AS s
		JOIN codegraph_nodes AS n
		  ON n.project_id = s.project_id AND n.revision_id = s.revision_id AND n.node_id = s.symbol_id
		JOIN codegraph_files AS f
		  ON f.project_id = s.project_id AND f.revision_id = s.revision_id AND f.file_id = s.file_id
		WHERE s.project_id = ? AND s.revision_id = ? AND (` + strings.Join(conditions, " OR ") + `)
	`
	var total int
	if err := snapshot.tx.QueryRowContext(ctx, `SELECT COUNT(*) `+fromAndWhere, filterArguments...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("codegraph store: count symbol candidates: %w", err)
	}
	arguments := append([]any(nil), filterArguments...)
	priority := "2"
	if len(entries) > 0 {
		qualifiedPlaceholders := placeholders(len(entries))
		namePlaceholders := placeholders(len(entries))
		priority = "CASE WHEN s.qualified_name IN (" + qualifiedPlaceholders + ") THEN 0 WHEN n.name IN (" + namePlaceholders + ") THEN 1 ELSE 2 END"
		for _, entry := range entries {
			arguments = append(arguments, entry)
		}
		for _, entry := range entries {
			arguments = append(arguments, entry)
		}
	}
	arguments = append(arguments, limit)
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT s.project_id, s.revision_id, s.symbol_id, s.file_id,
		       s.qualified_name, s.signature, s.start_byte, s.end_byte,
		       s.start_line, s.end_line, n.name, f.path, f.indexed_hash, f.byte_size
		`+fromAndWhere+`
		ORDER BY `+priority+`, s.qualified_name, s.symbol_id
		LIMIT ?
	`, arguments...)
	if err != nil {
		return nil, 0, fmt.Errorf("codegraph store: search symbols: %w", err)
	}
	defer rows.Close()
	var records []SymbolRecord
	for rows.Next() {
		var record SymbolRecord
		if err := rows.Scan(&record.Symbol.ProjectID, &record.Symbol.RevisionID, &record.Symbol.ID, &record.Symbol.FileID,
			&record.Symbol.QualifiedName, &record.Symbol.Signature, &record.Symbol.StartByte, &record.Symbol.EndByte,
			&record.Symbol.StartLine, &record.Symbol.EndLine, &record.Name, &record.FilePath, &record.IndexedHash, &record.FileByteSize); err != nil {
			return nil, 0, fmt.Errorf("codegraph store: scan symbol search: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("codegraph store: iterate symbol search: %w", err)
	}
	return records, total, nil
}

// SymbolRecords returns metadata for only the requested node ids.
func (snapshot *Snapshot) SymbolRecords(ctx context.Context, nodeIDs []string) ([]SymbolRecord, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	arguments := []any{snapshot.revision.ProjectID, snapshot.revision.ID}
	for _, id := range nodeIDs {
		arguments = append(arguments, id)
	}
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT s.project_id, s.revision_id, s.symbol_id, s.file_id,
		       s.qualified_name, s.signature, s.start_byte, s.end_byte,
		       s.start_line, s.end_line, n.name, f.path, f.indexed_hash, f.byte_size
		FROM codegraph_symbols AS s
		JOIN codegraph_nodes AS n
		  ON n.project_id = s.project_id AND n.revision_id = s.revision_id AND n.node_id = s.symbol_id
		JOIN codegraph_files AS f
		  ON f.project_id = s.project_id AND f.revision_id = s.revision_id AND f.file_id = s.file_id
		WHERE s.project_id = ? AND s.revision_id = ? AND s.symbol_id IN (`+placeholders(len(nodeIDs))+`)
		ORDER BY s.symbol_id
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read symbol records: %w", err)
	}
	defer rows.Close()
	return scanSymbolRecords(rows)
}

// NodesByIDs returns only requested nodes from this revision.
func (snapshot *Snapshot) NodesByIDs(ctx context.Context, nodeIDs []string) ([]Node, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	arguments := []any{snapshot.revision.ProjectID, snapshot.revision.ID}
	for _, id := range nodeIDs {
		arguments = append(arguments, id)
	}
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, node_id, kind, name, path
		FROM codegraph_nodes
		WHERE project_id = ? AND revision_id = ? AND node_id IN (`+placeholders(len(nodeIDs))+`)
		ORDER BY node_id
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read nodes by id: %w", err)
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var node Node
		if err := rows.Scan(&node.ProjectID, &node.RevisionID, &node.ID, &node.Kind, &node.Name, &node.Path); err != nil {
			return nil, fmt.Errorf("codegraph store: scan node by id: %w", err)
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

type EdgeSelection struct {
	SourceNodeIDs   []string
	TargetNodeIDs   []string
	ExcludedEdgeIDs []string
}

// EdgesForNodes returns filtered, previously unseen frontier edges with a
// SQL-level row cap. Directional endpoint predicates and exclusions are
// applied before LIMIT.
func (snapshot *Snapshot) EdgesForNodes(ctx context.Context, selection EdgeSelection, relationships []Relationship, minimumConfidence float64, limit int) ([]Edge, error) {
	if len(selection.SourceNodeIDs) == 0 && len(selection.TargetNodeIDs) == 0 || len(relationships) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		return nil, errors.New("codegraph store: edge query limit must be positive")
	}
	filter, arguments := snapshot.edgeFilter(selection, relationships, minimumConfidence)
	queryArguments := append(append([]any(nil), arguments...), limit)
	rows, err := snapshot.tx.QueryContext(ctx, `
		SELECT project_id, revision_id, edge_id, source_node_id, target_node_id,
		       relationship, confidence, provenance
		`+filter+`
		ORDER BY edge_id
		LIMIT ?
	`, queryArguments...)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read frontier edges: %w", err)
	}
	defer rows.Close()
	var edges []Edge
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.ProjectID, &edge.RevisionID, &edge.ID, &edge.SourceNodeID, &edge.TargetNodeID, &edge.Relationship, &edge.Confidence, &edge.Provenance); err != nil {
			return nil, fmt.Errorf("codegraph store: scan frontier edge: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edges, nil
}

// CountEdgesForNodes counts unique eligible edge rows using the same endpoint,
// relationship, confidence, and exclusion predicates as EdgesForNodes.
func (snapshot *Snapshot) CountEdgesForNodes(ctx context.Context, selection EdgeSelection, relationships []Relationship, minimumConfidence float64) (int, error) {
	if len(selection.SourceNodeIDs) == 0 && len(selection.TargetNodeIDs) == 0 || len(relationships) == 0 {
		return 0, nil
	}
	filter, arguments := snapshot.edgeFilter(selection, relationships, minimumConfidence)
	var total int
	if err := snapshot.tx.QueryRowContext(ctx, `SELECT COUNT(*) `+filter, arguments...).Scan(&total); err != nil {
		return 0, fmt.Errorf("codegraph store: count frontier edges: %w", err)
	}
	return total, nil
}

func (snapshot *Snapshot) edgeFilter(selection EdgeSelection, relationships []Relationship, minimumConfidence float64) (string, []any) {
	conditions := make([]string, 0, 5)
	arguments := []any{snapshot.revision.ProjectID, snapshot.revision.ID}
	var directionConditions []string
	if len(selection.SourceNodeIDs) > 0 {
		directionConditions = append(directionConditions, "source_node_id IN ("+placeholders(len(selection.SourceNodeIDs))+")")
		for _, id := range selection.SourceNodeIDs {
			arguments = append(arguments, id)
		}
	}
	if len(selection.TargetNodeIDs) > 0 {
		directionConditions = append(directionConditions, "target_node_id IN ("+placeholders(len(selection.TargetNodeIDs))+")")
		for _, id := range selection.TargetNodeIDs {
			arguments = append(arguments, id)
		}
	}
	conditions = append(conditions, "("+strings.Join(directionConditions, " OR ")+")")
	conditions = append(conditions, "relationship IN ("+placeholders(len(relationships))+")")
	for _, relationship := range relationships {
		arguments = append(arguments, relationship)
	}
	conditions = append(conditions, "confidence >= ?")
	arguments = append(arguments, minimumConfidence)
	if len(selection.ExcludedEdgeIDs) > 0 {
		conditions = append(conditions, "edge_id NOT IN ("+placeholders(len(selection.ExcludedEdgeIDs))+")")
		for _, id := range selection.ExcludedEdgeIDs {
			arguments = append(arguments, id)
		}
	}
	filter := `
		FROM codegraph_edges
		WHERE project_id = ? AND revision_id = ? AND ` + strings.Join(conditions, " AND ")
	return filter, arguments
}

type symbolRecordRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanSymbolRecords(rows symbolRecordRows) ([]SymbolRecord, error) {
	var records []SymbolRecord
	for rows.Next() {
		var record SymbolRecord
		if err := rows.Scan(&record.Symbol.ProjectID, &record.Symbol.RevisionID, &record.Symbol.ID, &record.Symbol.FileID,
			&record.Symbol.QualifiedName, &record.Symbol.Signature, &record.Symbol.StartByte, &record.Symbol.EndByte,
			&record.Symbol.StartLine, &record.Symbol.EndLine, &record.Name, &record.FilePath, &record.IndexedHash, &record.FileByteSize); err != nil {
			return nil, fmt.Errorf("codegraph store: scan symbol record: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("codegraph store: iterate symbol records: %w", err)
	}
	return records, nil
}

func placeholders(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = "?"
	}
	return strings.Join(values, ",")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (snapshot *Snapshot) PayloadCounts(ctx context.Context) (PayloadCounts, error) {
	var counts PayloadCounts
	err := snapshot.tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM codegraph_nodes WHERE project_id = ? AND revision_id = ?),
			(SELECT COUNT(*) FROM codegraph_files WHERE project_id = ? AND revision_id = ?),
			(SELECT COUNT(*) FROM codegraph_symbols WHERE project_id = ? AND revision_id = ?),
			(SELECT COUNT(*) FROM codegraph_edges WHERE project_id = ? AND revision_id = ?)
	`, snapshot.revision.ProjectID, snapshot.revision.ID,
		snapshot.revision.ProjectID, snapshot.revision.ID,
		snapshot.revision.ProjectID, snapshot.revision.ID,
		snapshot.revision.ProjectID, snapshot.revision.ID).Scan(
		&counts.Nodes, &counts.Files, &counts.Symbols, &counts.Edges)
	if err != nil {
		return PayloadCounts{}, fmt.Errorf("codegraph store: count revision payload: %w", err)
	}
	return counts, nil
}

// ReadRevision pins a callback to one explicitly selected revision snapshot.
func (s *Store) ReadRevision(ctx context.Context, revisionID string, read func(*Snapshot) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("codegraph store: begin revision snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := s.rejectDisabling(ctx, tx); err != nil {
		return err
	}
	revision, err := scanRevision(tx.QueryRowContext(ctx, `
		SELECT project_id, revision_id, state, indexed_commit, created_at, completed_at
		FROM codegraph_revisions
		WHERE project_id = ? AND revision_id = ?
	`, s.projectID, revisionID), revisionID)
	if err != nil {
		return err
	}
	if err := read(&Snapshot{tx: tx, revision: revision}); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadActive resolves the active pointer and serves the callback from the same
// read transaction, so a concurrent publication cannot mix revisions.
func (s *Store) ReadActive(ctx context.Context, read func(*Snapshot) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("codegraph store: begin active snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := s.rejectDisabling(ctx, tx); err != nil {
		return err
	}
	revision, err := scanRevision(tx.QueryRowContext(ctx, `
		SELECT r.project_id, r.revision_id, r.state, r.indexed_commit, r.created_at, r.completed_at
		FROM codegraph_config AS c
		JOIN codegraph_revisions AS r
		  ON r.project_id = c.project_id AND r.revision_id = c.active_revision_id
		WHERE c.project_id = ? AND r.project_id = ? AND r.state = 'active'
	`, s.projectID, s.projectID), "active")
	if err != nil {
		return err
	}
	if err := read(&Snapshot{tx: tx, revision: revision}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) rejectDisabling(ctx context.Context, tx *sql.Tx) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM codegraph_lifecycle WHERE project_id = ?`, s.projectID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("codegraph store: inspect lifecycle state: %w", err)
	}
	if state == "disabling" {
		return ErrDisabling
	}
	return nil
}

func (s *Store) Diagnostics(ctx context.Context, revisionID string) ([]Diagnostic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, revision_id, diagnostic_id, severity, code, message, created_at
		FROM codegraph_diagnostics
		WHERE project_id = ? AND revision_id = ?
		ORDER BY created_at, diagnostic_id
	`, s.projectID, revisionID)
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read diagnostics: %w", err)
	}
	defer rows.Close()
	var diagnostics []Diagnostic
	for rows.Next() {
		var diagnostic Diagnostic
		if err := rows.Scan(&diagnostic.ProjectID, &diagnostic.RevisionID, &diagnostic.ID,
			&diagnostic.Severity, &diagnostic.Code, &diagnostic.Message, &diagnostic.CreatedAt); err != nil {
			return nil, fmt.Errorf("codegraph store: scan diagnostic: %w", err)
		}
		diagnostic.CreatedAt = diagnostic.CreatedAt.UTC()
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, rows.Err()
}

// LatestDiagnostics returns diagnostics for the latest attempted revision,
// including failed candidates, so callers can surface rebuild failures without
// exposing candidate graph payload.
func (s *Store) LatestDiagnostics(ctx context.Context) ([]Diagnostic, error) {
	var revisionID string
	err := s.db.QueryRowContext(ctx, `
		SELECT revision_id FROM codegraph_revisions
		WHERE project_id = ? ORDER BY created_at DESC, revision_id DESC LIMIT 1
	`, s.projectID).Scan(&revisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return []Diagnostic{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codegraph store: read latest revision: %w", err)
	}
	return s.Diagnostics(ctx, revisionID)
}

// PublishCandidate validates and changes the active pointer and both revision
// states in one transaction. Validation reads the candidate from that same
// transaction. A validation error triggers a separate atomic failure cleanup.
func (s *Store) PublishCandidate(ctx context.Context, revisionID string, validate func(context.Context, *Snapshot) error) error {
	return s.PublishCandidateGuarded(ctx, revisionID, nil, validate)
}

// PublishCandidateGuarded revalidates caller-owned authorization from the
// same SQLite transaction that swaps the active revision.
func (s *Store) PublishCandidateGuarded(ctx context.Context, revisionID string, guard PublicationGuard, validate func(context.Context, *Snapshot) error) error {
	return s.publishCandidate(ctx, revisionID, nil, "", nil, guard, validate)
}

// PublishCandidateWithConfig atomically publishes a validated candidate and
// its human-approved checkout configuration. expected and expectedActive form
// a compare-and-swap guard so a concurrent rebuild or lifecycle change wins
// without partially changing either graph or configuration.
func (s *Store) PublishCandidateWithConfig(ctx context.Context, revisionID string, expected config.Project, expectedActive string, next config.Project, validate func(context.Context, *Snapshot) error) error {
	return s.PublishCandidateWithConfigGuarded(ctx, revisionID, expected, expectedActive, next, nil, validate)
}

// PublishCandidateWithConfigGuarded combines the checkout/configuration CAS,
// authorization guard, and candidate pointer swap in one transaction.
func (s *Store) PublishCandidateWithConfigGuarded(ctx context.Context, revisionID string, expected config.Project, expectedActive string, next config.Project, guard PublicationGuard, validate func(context.Context, *Snapshot) error) error {
	if expected.ProjectID != s.projectID || next.ProjectID != s.projectID {
		return fmt.Errorf("%w: lifecycle configuration project mismatch", ErrProjectScope)
	}
	if !next.Enabled {
		return fmt.Errorf("%w: published lifecycle configuration must be enabled", config.ErrInvalidProject)
	}
	canonical, err := config.CanonicalRemote(next.CanonicalRemote)
	if err != nil {
		return err
	}
	next.CanonicalRemote = canonical
	if err := config.ValidateProject(next); err != nil {
		return err
	}
	return s.publishCandidate(ctx, revisionID, &expected, expectedActive, &next, guard, validate)
}

func (s *Store) publishCandidate(ctx context.Context, revisionID string, expected *config.Project, expectedActive string, next *config.Project, guard PublicationGuard, validate func(context.Context, *Snapshot) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("codegraph store: begin publish %q: %w", revisionID, err)
	}
	if err := s.rejectDisabling(ctx, tx); err != nil {
		tx.Rollback()
		return err
	}
	revision, err := scanRevision(tx.QueryRowContext(ctx, `
		SELECT project_id, revision_id, state, indexed_commit, created_at, completed_at
		FROM codegraph_revisions
		WHERE project_id = ? AND revision_id = ?
	`, s.projectID, revisionID), revisionID)
	if err != nil {
		tx.Rollback()
		return err
	}
	if revision.State != RevisionCandidate {
		tx.Rollback()
		return fmt.Errorf("%w: revision %q state %q", ErrNotCandidate, revisionID, revision.State)
	}
	if err := validate(ctx, &Snapshot{tx: tx, revision: revision}); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("codegraph store: rollback invalid candidate: %w", rollbackErr))
		}
		if cleanupErr := s.failCandidateLocked(ctx, revisionID, "validation_failed", err.Error()); cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	if guard != nil {
		if err := guard(ctx, tx); err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("codegraph store: rollback unauthorized candidate: %w", rollbackErr))
			}
			if cleanupErr := s.failCandidateLocked(ctx, revisionID, "authorization_changed", err.Error()); cleanupErr != nil {
				return errors.Join(err, cleanupErr)
			}
			return err
		}
	}
	if expected != nil {
		current, active, err := projectConfigInTx(ctx, tx, s.projectID)
		if err != nil {
			tx.Rollback()
			return err
		}
		if !sameProjectConfig(current, *expected) || active != expectedActive {
			tx.Rollback()
			return errors.New("codegraph store: project configuration changed before lifecycle publication")
		}
	}
	completedAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE codegraph_revisions
		SET state = 'retired'
		WHERE project_id = ? AND state = 'active'
	`, s.projectID); err != nil {
		tx.Rollback()
		return fmt.Errorf("codegraph store: retire active revision: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE codegraph_revisions
		SET state = 'active', completed_at = ?
		WHERE project_id = ? AND revision_id = ? AND state = 'candidate'
	`, completedAt, s.projectID, revisionID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("codegraph store: activate revision %q: %w", revisionID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		tx.Rollback()
		if err != nil {
			return fmt.Errorf("codegraph store: inspect activation %q: %w", revisionID, err)
		}
		return fmt.Errorf("%w: revision %q", ErrNotCandidate, revisionID)
	}
	if next == nil {
		result, err = tx.ExecContext(ctx, `
			UPDATE codegraph_config
			SET active_revision_id = ?, last_successful_build = ?
			WHERE project_id = ?
		`, revisionID, completedAt, s.projectID)
	} else {
		result, err = tx.ExecContext(ctx, `
			UPDATE codegraph_config
			SET enabled = 1, canonical_remote = ?, active_checkout = ?,
			    project_source_byte_ceiling = ?, last_successful_build = ?, active_revision_id = ?
			WHERE project_id = ?
		`, next.CanonicalRemote, next.ActiveCheckout, next.ProjectSourceByteCeiling, completedAt, revisionID, s.projectID)
	}
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("codegraph store: update active pointer: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil || rows != 1 {
		tx.Rollback()
		if err != nil {
			return fmt.Errorf("codegraph store: inspect active pointer: %w", err)
		}
		return fmt.Errorf("codegraph store: project config %q missing", s.projectID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit publish %q: %w", revisionID, err)
	}
	return nil
}

func projectConfigInTx(ctx context.Context, tx *sql.Tx, projectID string) (config.Project, string, error) {
	project := config.Project{ProjectID: projectID}
	var enabled bool
	var lastSuccessfulBuild sql.NullTime
	var active sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT enabled, canonical_remote, active_checkout,
		       project_source_byte_ceiling, last_successful_build, active_revision_id
		FROM codegraph_config WHERE project_id = ?
	`, projectID).Scan(&enabled, &project.CanonicalRemote, &project.ActiveCheckout,
		&project.ProjectSourceByteCeiling, &lastSuccessfulBuild, &active)
	if err != nil {
		return config.Project{}, "", fmt.Errorf("codegraph store: read lifecycle configuration: %w", err)
	}
	project.Enabled = enabled
	if lastSuccessfulBuild.Valid {
		value := lastSuccessfulBuild.Time.UTC()
		project.LastSuccessfulBuild = &value
	}
	return project, active.String, nil
}

func sameProjectConfig(left, right config.Project) bool {
	if left.ProjectID != right.ProjectID || left.Enabled != right.Enabled ||
		left.CanonicalRemote != right.CanonicalRemote || left.ActiveCheckout != right.ActiveCheckout ||
		left.ProjectSourceByteCeiling != right.ProjectSourceByteCeiling {
		return false
	}
	if left.LastSuccessfulBuild == nil || right.LastSuccessfulBuild == nil {
		return left.LastSuccessfulBuild == nil && right.LastSuccessfulBuild == nil
	}
	return left.LastSuccessfulBuild.Equal(*right.LastSuccessfulBuild)
}

// FailCandidate atomically marks an unfinished candidate failed, removes its
// graph payload, and retains caller diagnostics plus one system diagnostic.
// It never changes the active revision pointer.
func (s *Store) FailCandidate(ctx context.Context, revisionID, code, message string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if revisionID == "" || code == "" {
		return errors.New("codegraph store: failed candidate revision and code are required")
	}
	return s.failCandidateLocked(ctx, revisionID, code, message)
}

type revisionIDRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func readRevisionIDs(rows revisionIDRows) ([]string, error) {
	var revisionIDs []string
	for rows.Next() {
		var revisionID string
		if err := rows.Scan(&revisionID); err != nil {
			return nil, fmt.Errorf("codegraph store: scan interrupted candidate: %w", err)
		}
		revisionIDs = append(revisionIDs, revisionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("codegraph store: iterate interrupted candidates: %w", err)
	}
	return revisionIDs, nil
}

func (s *Store) failCandidateLocked(ctx context.Context, revisionID, code, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("codegraph store: begin candidate failure %q: %w", revisionID, err)
	}
	defer tx.Rollback()
	if err := s.rejectDisabling(ctx, tx); err != nil {
		return err
	}
	for _, table := range []string{"codegraph_edges", "codegraph_symbols", "codegraph_files", "codegraph_nodes"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ? AND revision_id = ?`, s.projectID, revisionID); err != nil {
			return fmt.Errorf("codegraph store: clean %s for candidate %q: %w", table, revisionID, err)
		}
	}
	completedAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE codegraph_revisions
		SET state = 'failed', completed_at = ?
		WHERE project_id = ? AND revision_id = ? AND state = 'candidate'
	`, completedAt, s.projectID, revisionID)
	if err != nil {
		return fmt.Errorf("codegraph store: fail candidate %q: %w", revisionID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("codegraph store: inspect failed candidate %q: %w", revisionID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: revision %q", ErrNotCandidate, revisionID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO codegraph_diagnostics (
			project_id, revision_id, diagnostic_id, severity, code, message, created_at
		) VALUES (?, ?, ?, 'error', ?, ?, ?)
	`, s.projectID, revisionID, systemDiagnosticPrefix+code, code, message, completedAt); err != nil {
		return fmt.Errorf("codegraph store: retain candidate diagnostic %q: %w", revisionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit candidate failure %q: %w", revisionID, err)
	}
	return nil
}

// Disable rejects new snapshot readers through a committed lifecycle marker,
// then atomically removes every project-scoped graph row. SQLite WAL snapshots
// admitted before the marker remain coherent while the deletion commits.
func (s *Store) Disable(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ownerPID, ownerStart := currentProcessLeaseIdentity()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO codegraph_lifecycle (
			project_id, state, build_token, owner_pid, owner_start, build_owner_pid, build_owner_start
		) VALUES (?, 'disabling', NULL, ?, ?, NULL, NULL)
		ON CONFLICT(project_id) DO UPDATE SET state = 'disabling', owner_pid = excluded.owner_pid, owner_start = excluded.owner_start
	`, s.projectID, ownerPID, ownerStart); err != nil {
		return fmt.Errorf("codegraph store: begin disablement for %q: %w", s.projectID, err)
	}
	for {
		var token sql.NullString
		var buildOwnerPID sql.NullInt64
		var buildOwnerStart sql.NullString
		if err := s.db.QueryRowContext(ctx, `
			SELECT build_token, build_owner_pid, build_owner_start
			FROM codegraph_lifecycle WHERE project_id = ? AND state = 'disabling'
		`, s.projectID).Scan(&token, &buildOwnerPID, &buildOwnerStart); err != nil {
			return fmt.Errorf("codegraph store: wait for project %q build drain: %w", s.projectID, err)
		}
		if !token.Valid {
			break
		}
		if buildOwnerPID.Valid && buildOwnerStart.Valid && processLeaseProbe(int(buildOwnerPID.Int64), buildOwnerStart.String) == leaseDead {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE codegraph_lifecycle
				SET build_token = NULL, build_owner_pid = NULL, build_owner_start = NULL
				WHERE project_id = ? AND state = 'disabling' AND build_token = ?
			`, s.projectID, token.String); err != nil {
				return fmt.Errorf("codegraph store: clear stale build owner during disable: %w", err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s.finishDisableLocked(ctx, true)
}

func (s *Store) finishDisableLocked(ctx context.Context, clearMarker bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("codegraph store: begin destructive disablement for %q: %w", s.projectID, err)
	}
	defer tx.Rollback()
	for _, table := range []string{
		"codegraph_diagnostics", "codegraph_edges", "codegraph_symbols",
		"codegraph_files", "codegraph_nodes", "codegraph_revisions", "codegraph_config",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ?`, s.projectID); err != nil {
			return fmt.Errorf("codegraph store: delete %s for project %q: %w", table, s.projectID, err)
		}
	}
	if clearMarker {
		if _, err := tx.ExecContext(ctx, `DELETE FROM codegraph_lifecycle WHERE project_id = ?`, s.projectID); err != nil {
			return fmt.Errorf("codegraph store: clear lifecycle marker for project %q: %w", s.projectID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("codegraph store: commit destructive disablement for %q: %w", s.projectID, err)
	}
	return nil
}

// recoverAtStartup holds SQLite's cross-process writer barrier from lifecycle
// inspection through all recovery cleanup. BeginBuild and Disable therefore
// cannot admit new work into the check-to-clean window.
func (s *Store) recoverAtStartup(ctx context.Context) error {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("codegraph store: acquire startup recovery connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("codegraph store: begin startup recovery barrier: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var state string
	var ownerPID int
	var ownerStart string
	var token sql.NullString
	var buildOwnerPID sql.NullInt64
	var buildOwnerStart sql.NullString
	err = connection.QueryRowContext(ctx, `
		SELECT state, build_token, owner_pid, owner_start, build_owner_pid, build_owner_start
		FROM codegraph_lifecycle WHERE project_id = ?
	`, s.projectID).Scan(&state, &token, &ownerPID, &ownerStart, &buildOwnerPID, &buildOwnerStart)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("codegraph store: inspect startup lifecycle: %w", err)
	}
	if afterRecoverInterruptedLifecycle != nil {
		afterRecoverInterruptedLifecycle()
	}
	preserveCandidate := ""
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No durable owner existed before the barrier, so all candidates are stale.
	case state == "building":
		buildStatus := leaseUnknown
		if buildOwnerPID.Valid && buildOwnerStart.Valid {
			buildStatus = processLeaseProbe(int(buildOwnerPID.Int64), buildOwnerStart.String)
		}
		if buildStatus == leaseDead {
			if _, err := connection.ExecContext(ctx, `DELETE FROM codegraph_lifecycle WHERE project_id = ? AND state = 'building' AND build_token IS ?`, s.projectID, token); err != nil {
				return fmt.Errorf("codegraph store: clear interrupted build lease: %w", err)
			}
		} else if token.Valid {
			preserveCandidate = token.String
		}
	case state == "disabling":
		disableStatus := processLeaseProbe(ownerPID, ownerStart)
		buildStatus := leaseDead
		if token.Valid {
			buildStatus = leaseUnknown
			if buildOwnerPID.Valid && buildOwnerStart.Valid {
				buildStatus = processLeaseProbe(int(buildOwnerPID.Int64), buildOwnerStart.String)
			}
		}
		if disableStatus != leaseDead || buildStatus != leaseDead {
			if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
				return fmt.Errorf("codegraph store: commit live startup lifecycle: %w", err)
			}
			committed = true
			return nil
		}
		if err := deleteProjectGraphRows(ctx, connection, s.projectID, true); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
			return fmt.Errorf("codegraph store: commit interrupted disable recovery: %w", err)
		}
		committed = true
		return nil
	}
	if err := failInterruptedCandidates(ctx, connection, s.projectID, preserveCandidate); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("codegraph store: commit startup candidate recovery: %w", err)
	}
	committed = true
	return nil
}

type recoveryConnection interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func deleteProjectGraphRows(ctx context.Context, connection recoveryConnection, projectID string, clearMarker bool) error {
	for _, table := range []string{
		"codegraph_diagnostics", "codegraph_edges", "codegraph_symbols",
		"codegraph_files", "codegraph_nodes", "codegraph_revisions", "codegraph_config",
	} {
		if _, err := connection.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ?`, projectID); err != nil {
			return fmt.Errorf("codegraph store: delete %s for project %q: %w", table, projectID, err)
		}
	}
	if clearMarker {
		if _, err := connection.ExecContext(ctx, `DELETE FROM codegraph_lifecycle WHERE project_id = ?`, projectID); err != nil {
			return fmt.Errorf("codegraph store: clear lifecycle marker for project %q: %w", projectID, err)
		}
	}
	return nil
}

func failInterruptedCandidates(ctx context.Context, connection recoveryConnection, projectID, preserve string) error {
	rows, err := connection.QueryContext(ctx, `
		SELECT revision_id FROM codegraph_revisions
		WHERE project_id = ? AND state = 'candidate' AND revision_id <> ?
		ORDER BY revision_id
	`, projectID, preserve)
	if err != nil {
		return fmt.Errorf("codegraph store: list startup candidates: %w", err)
	}
	revisionIDs, readErr := readRevisionIDs(rows)
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return fmt.Errorf("codegraph store: close startup candidates: %w", closeErr)
	}
	for _, revisionID := range revisionIDs {
		for _, table := range []string{"codegraph_edges", "codegraph_symbols", "codegraph_files", "codegraph_nodes"} {
			if _, err := connection.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ? AND revision_id = ?`, projectID, revisionID); err != nil {
				return fmt.Errorf("codegraph store: clean %s for startup candidate %q: %w", table, revisionID, err)
			}
		}
		completedAt := time.Now().UTC()
		if _, err := connection.ExecContext(ctx, `UPDATE codegraph_revisions SET state = 'failed', completed_at = ? WHERE project_id = ? AND revision_id = ? AND state = 'candidate'`, completedAt, projectID, revisionID); err != nil {
			return fmt.Errorf("codegraph store: fail startup candidate %q: %w", revisionID, err)
		}
		if _, err := connection.ExecContext(ctx, `
			INSERT INTO codegraph_diagnostics (project_id, revision_id, diagnostic_id, severity, code, message, created_at)
			VALUES (?, ?, ?, 'error', 'interrupted_candidate', 'candidate build was interrupted before publication', ?)
		`, projectID, revisionID, systemDiagnosticPrefix+"interrupted_candidate", completedAt); err != nil {
			return fmt.Errorf("codegraph store: diagnose startup candidate %q: %w", revisionID, err)
		}
	}
	return nil
}

func currentProcessLeaseIdentity() (int, string) {
	pid := os.Getpid()
	identity, err := processStartIdentity(pid)
	if err != nil {
		identity = "pid:" + strconv.Itoa(pid)
	}
	return pid, identity
}

type processLeaseStatus uint8

const (
	leaseUnknown processLeaseStatus = iota
	leaseLive
	leaseDead
)

func processLeaseLiveness(pid int, expectedStart string) processLeaseStatus {
	if pid <= 0 || expectedStart == "" {
		return leaseUnknown
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return leaseUnknown
	}
	signalErr := process.Signal(syscall.Signal(0))
	actualStart, startErr := processStartIdentity(pid)
	return classifyProcessLease(pid, expectedStart, signalErr, actualStart, startErr)
}

func classifyProcessLease(pid int, expectedStart string, signalErr error, actualStart string, startErr error) processLeaseStatus {
	if errors.Is(signalErr, syscall.ESRCH) || errors.Is(signalErr, os.ErrProcessDone) {
		return leaseDead
	}
	if signalErr != nil {
		return leaseUnknown
	}
	if startErr != nil {
		if expectedStart == "pid:"+strconv.Itoa(pid) {
			return leaseLive
		}
		return leaseUnknown
	}
	if actualStart == expectedStart {
		return leaseLive
	}
	return leaseDead
}

func processStartIdentity(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err == nil {
		closing := strings.LastIndexByte(string(data), ')')
		if closing >= 0 {
			fields := strings.Fields(string(data[closing+1:]))
			if len(fields) > 19 {
				return "proc:" + fields[19], nil
			}
		}
	}
	output, psErr := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	identity := strings.TrimSpace(string(output))
	if psErr != nil || identity == "" {
		return "", errors.New("codegraph store: process start identity unavailable")
	}
	return "ps:" + identity, nil
}
