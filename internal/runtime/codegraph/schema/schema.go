// Package schema owns the canonical Code Graph SQLite catalog. It is kept
// below the store and private-control packages so every owner fingerprints
// the same component schema without importing the other's runtime.
package schema

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

const CurrentVersion = 2

var ErrFuture = errors.New("Code Graph schema is newer than this binary")

const ledgerSQL = `
	CREATE TABLE IF NOT EXISTS codegraph_schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL
	);
`

const versionOneSQL = `
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

const versionTwoSQL = `
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

// CurrentSQL is the canonical component catalog used by migration and
// read-only preflight. It intentionally excludes row data.
func CurrentSQL() string { return ledgerSQL + versionOneSQL + versionTwoSQL }

func LedgerSQL() string     { return ledgerSQL }
func VersionOneSQL() string { return versionOneSQL }
func VersionTwoSQL() string { return versionTwoSQL }

// Validate accepts an absent component catalog (fresh Gateway control DB) or
// proves that every present codegraph_* object and ledger row is exactly this
// released pre-alpha component schema. It performs no writes.
func Validate(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("Code Graph schema: nil database")
	}
	actual, err := fingerprint(db)
	if err != nil {
		return fmt.Errorf("Code Graph schema: catalog unreadable")
	}
	if actual == "" {
		return nil
	}
	ledgerRows, err := db.Query(`SELECT version FROM codegraph_schema_migrations`)
	if err == nil {
		for ledgerRows.Next() {
			var version int
			if scanErr := ledgerRows.Scan(&version); scanErr == nil && version > CurrentVersion {
				_ = ledgerRows.Close()
				return ErrFuture
			}
		}
		_ = ledgerRows.Close()
	}
	canonicalDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("Code Graph schema: canonical catalog unavailable")
	}
	defer canonicalDB.Close()
	if _, err := canonicalDB.Exec(CurrentSQL()); err != nil {
		return fmt.Errorf("Code Graph schema: canonical catalog unavailable")
	}
	expected, err := fingerprint(canonicalDB)
	if err != nil || actual != expected {
		return fmt.Errorf("Code Graph schema: object definitions are not exact v2")
	}
	rows, err := db.Query(`SELECT version FROM codegraph_schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("Code Graph schema: ledger unreadable")
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("Code Graph schema: ledger is not exact v2")
		}
		if version > CurrentVersion {
			return ErrFuture
		}
		if version != want {
			return fmt.Errorf("Code Graph schema: ledger is not exact v2")
		}
		want++
	}
	if err := rows.Err(); err != nil || want != CurrentVersion+1 {
		return fmt.Errorf("Code Graph schema: ledger is not exact v2")
	}
	return nil
}

func fingerprint(db *sql.DB) (string, error) {
	rows, err := db.Query(`SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master WHERE name LIKE 'codegraph_%' ORDER BY type,name,tbl_name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type object struct{ kind, name, table, sql string }
	var objects []object
	for rows.Next() {
		var item object
		if err := rows.Scan(&item.kind, &item.name, &item.table, &item.sql); err != nil {
			return "", err
		}
		objects = append(objects, item)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].kind != objects[j].kind {
			return objects[i].kind < objects[j].kind
		}
		if objects[i].name != objects[j].name {
			return objects[i].name < objects[j].name
		}
		return objects[i].table < objects[j].table
	})
	if len(objects) == 0 {
		return "", nil
	}
	hash := sha256.New()
	for _, item := range objects {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00", item.kind, item.name, item.table, item.sql)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}
