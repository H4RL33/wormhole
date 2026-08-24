package localstore

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// ErrUnsupportedPrivateFormat reports a private Gateway database that this
// closed pre-alpha binary must preserve rather than interpret or mutate.
type ErrUnsupportedPrivateFormat struct {
	Path   string
	Reason string
}

func (e ErrUnsupportedPrivateFormat) Error() string {
	return fmt.Sprintf("localstore: unsupported closed-pre-alpha private database %s (%s); no mutation was made; inspect or back up unpublished state before deliberate manual removal", e.Path, e.Reason)
}

type privateFormatClass int

const (
	privateFormatFresh privateFormatClass = iota
	privateFormatCurrent
)

var (
	//go:embed private_schema_v6.sql
	privateSchemaV6 string
)

var requiredPrivateTables = map[string]struct{}{
	"whoami_cache": {}, "projects": {}, "agents": {}, "passports": {}, "auth_scopes": {},
	"tasks": {}, "channels": {}, "events": {}, "kb_articles": {}, "kb_links": {},
	"git_links": {}, "sync_queue": {}, "sync_audit": {}, "enrolment_attempts": {},
	"bootstrap_metadata": {}, "integration_manifest_bodies": {}, "integration_manifest_project_state": {},
	"integration_manifest_decisions": {}, "integration_manifest_revocations": {},
	"integration_manifest_journal": {}, "integration_manifest_audit": {},
	"gateway_schema_migrations": {}, "workspace_bindings": {}, "workspace_candidates": {},
	"workspace_overlay_operations": {}, "workspace_materializations": {}, "workspace_stashes": {},
	"workspace_conflicts": {}, "legacy_integration_state_migrations": {},
	"workspace_transition_receipts": {}, "workspace_publication_policies": {},
	"workspace_publication_policy_history": {},
}

var requiredPrivateColumns = map[string][]string{
	"whoami_cache":                 {"agent_id", "project_id"},
	"channels":                     {"created_at"},
	"workspace_bindings":           {"workspace_revision"},
	"workspace_conflicts":          {"occurrence_id"},
	"workspace_overlay_operations": {"stashed_by_stash_id"},
	"workspace_materializations":   {"included_operations_json", "publication_review_json", "prior_candidate_json", "publication_review_proof_version"},
}

func classifyPrivateDatabase(path string) (privateFormatClass, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return privateFormatFresh, nil
	}
	if err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "private database cannot be classified safely"}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "private database is not a regular file"}
	}
	if info.Size() == 0 {
		return privateFormatFresh, nil
	}

	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "read-only preflight could not open database"}
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "read-only preflight could not read database"}
	}
	if err := validateCurrentPrivateSchema(db); err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: err.Error()}
	}
	return privateFormatCurrent, nil
}

func validateCurrentPrivateSchema(db *sql.DB) error {
	var tableCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='gateway_schema_migrations'`).Scan(&tableCount); err != nil {
		return fmt.Errorf("private schema ledger is unreadable")
	}
	if tableCount != 1 {
		return fmt.Errorf("private schema ledger is missing")
	}
	var version, count int
	if err := db.QueryRow(`SELECT coalesce(max(version),0),count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		return fmt.Errorf("private schema ledger is unreadable")
	}
	if version != GatewaySchemaVersion || count != 1 {
		return fmt.Errorf("private schema ledger is not exact v6")
	}
	var ledgerVersion int
	if err := db.QueryRow(`SELECT version FROM gateway_schema_migrations`).Scan(&ledgerVersion); err != nil || ledgerVersion != GatewaySchemaVersion {
		return fmt.Errorf("private schema ledger is not exact v6")
	}

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("private schema object inventory is unreadable")
	}
	seen := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("private schema object inventory is unreadable")
		}
		seen[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("private schema object inventory is unreadable")
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("private schema object inventory is unreadable")
	}
	if len(seen) != len(requiredPrivateTables) {
		return fmt.Errorf("private schema object set is incomplete or unexpected")
	}
	for name := range requiredPrivateTables {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("private schema object %q is missing", name)
		}
	}
	for table, columns := range requiredPrivateColumns {
		available, err := privateTableColumns(db, table)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if _, ok := available[column]; !ok {
				return fmt.Errorf("private schema table %q is missing current column %q", table, column)
			}
		}
	}
	var unsupportedProofs int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_materializations WHERE included_operations_json IS NULL OR publication_review_proof_version <> 1 OR publication_review_json IS NULL OR prior_candidate_json IS NULL`).Scan(&unsupportedProofs); err != nil {
		return fmt.Errorf("private materialization proof inventory is unreadable")
	}
	if unsupportedProofs != 0 {
		return fmt.Errorf("private database contains unsupported materialization proof evidence")
	}
	return nil
}

func privateTableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("private schema table %q is unreadable", table)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("private schema table %q is unreadable", table)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("private schema table %q is unreadable", table)
	}
	return columns, nil
}

func sqliteReadOnlyDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = query.Encode()
	return u.String()
}

func initializePrivateSchemaV6(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("localstore: begin private v6 initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("localstore: initialize base private schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, privateSchemaV6); err != nil {
		return fmt.Errorf("localstore: initialize private schema v6: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("localstore: commit private schema v6: %w", err)
	}
	return nil
}
