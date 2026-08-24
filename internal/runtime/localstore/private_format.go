package localstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"

	codeschema "github.com/H4RL33/wormhole/internal/runtime/codegraph/schema"
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

	privateSchemaFingerprintOnce         sync.Once
	privateSchemaFingerprintValue        string
	privateSchemaFingerprintErr          error
	privateWithCodeGraphFingerprintOnce  sync.Once
	privateWithCodeGraphFingerprintValue string
	privateWithCodeGraphFingerprintErr   error

	// privateSchemaV6ValidationHook is a failure-injection seam for proving
	// fresh initialization rollback. It is nil in normal Gateway operation.
	privateSchemaV6ValidationHook func(*sql.Tx) error
)

type privateSchemaQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

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
		if present, sidecarErr := privateSidecarsPresent(path); sidecarErr != nil {
			return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: sidecarErr.Error()}
		} else if present {
			return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "database sidecar evidence exists without a main database"}
		}
		return privateFormatFresh, nil
	}
	if err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "private database cannot be classified safely"}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "private database is not a regular file"}
	}
	if info.Size() == 0 {
		if present, sidecarErr := privateSidecarsPresent(path); sidecarErr != nil {
			return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: sidecarErr.Error()}
		} else if present {
			return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "zero-byte database has sidecar evidence"}
		}
		return privateFormatFresh, nil
	}

	classificationPath, cleanup, err := copyPrivateEvidenceForClassification(path)
	if err != nil {
		return 0, ErrUnsupportedPrivateFormat{Path: path, Reason: "private database evidence could not be copied safely"}
	}
	defer cleanup()
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(classificationPath))
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

func copyPrivateEvidenceForClassification(path string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "wormhole-private-preflight-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	target := filepath.Join(directory, filepath.Base(path))
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		source := path + suffix
		info, statErr := os.Lstat(source)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			cleanup()
			return "", func() {}, fmt.Errorf("unsafe private evidence %s", source)
		}
		data, readErr := os.ReadFile(source)
		if readErr != nil {
			cleanup()
			return "", func() {}, readErr
		}
		if writeErr := os.WriteFile(target+suffix, data, info.Mode().Perm()); writeErr != nil {
			cleanup()
			return "", func() {}, writeErr
		}
	}
	return target, cleanup, nil
}

func validateCurrentPrivateSchema(db privateSchemaQueryer) error {
	actualFingerprint, err := privateSchemaFingerprint(db)
	if err != nil {
		return fmt.Errorf("private schema fingerprint is unreadable")
	}
	expectedFingerprint, err := canonicalPrivateSchemaFingerprint()
	if err != nil {
		return fmt.Errorf("private schema object definitions are not exact v6")
	}
	graphCatalogPresent := false
	if actualFingerprint != expectedFingerprint {
		withCodeGraph, graphErr := canonicalPrivateSchemaWithCodeGraphFingerprint()
		if graphErr != nil || actualFingerprint != withCodeGraph {
			return fmt.Errorf("private schema object definitions are not exact v6")
		}
		graphCatalogPresent = true
	}
	if graphCatalogPresent {
		if err := validateCurrentCodeGraphLedger(db); err != nil {
			return err
		}
	}
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

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'codegraph_%'`)
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

func validateCurrentCodeGraphLedger(db privateSchemaQueryer) error {
	rows, err := db.Query(`SELECT version FROM codegraph_schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("Code Graph schema ledger is unreadable")
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil || version != want {
			return fmt.Errorf("Code Graph schema ledger is not exact v2")
		}
		want++
	}
	if err := rows.Err(); err != nil || want != 3 {
		return fmt.Errorf("Code Graph schema ledger is not exact v2")
	}
	return nil
}

func privateTableColumns(db privateSchemaQueryer, table string) (map[string]struct{}, error) {
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

func privateSchemaFingerprint(db privateSchemaQueryer) (string, error) {
	rows, err := db.Query(`SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name,tbl_name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type object struct{ kind, name, table, sql string }
	objects := make([]object, 0)
	for rows.Next() {
		var value object
		if err := rows.Scan(&value.kind, &value.name, &value.table, &value.sql); err != nil {
			return "", err
		}
		objects = append(objects, value)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	// Keep the digest independent of driver row order even if SQLite changes
	// its ordering for an equivalent catalog query.
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].kind != objects[j].kind {
			return objects[i].kind < objects[j].kind
		}
		if objects[i].name != objects[j].name {
			return objects[i].name < objects[j].name
		}
		return objects[i].table < objects[j].table
	})
	hash := sha256.New()
	for _, value := range objects {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00", value.kind, value.name, value.table, value.sql)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func canonicalPrivateSchemaFingerprint() (string, error) {
	privateSchemaFingerprintOnce.Do(func() {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			privateSchemaFingerprintErr = err
			return
		}
		defer db.Close()
		if _, err := db.Exec(schema); err != nil {
			privateSchemaFingerprintErr = err
			return
		}
		if _, err := db.Exec(privateSchemaV6); err != nil {
			privateSchemaFingerprintErr = err
			return
		}
		privateSchemaFingerprintValue, privateSchemaFingerprintErr = privateSchemaFingerprint(db)
	})
	return privateSchemaFingerprintValue, privateSchemaFingerprintErr
}

func canonicalPrivateSchemaWithCodeGraphFingerprint() (string, error) {
	privateWithCodeGraphFingerprintOnce.Do(func() {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			privateWithCodeGraphFingerprintErr = err
			return
		}
		defer db.Close()
		if _, err := db.Exec(schema); err != nil {
			privateWithCodeGraphFingerprintErr = err
			return
		}
		if _, err := db.Exec(privateSchemaV6); err != nil {
			privateWithCodeGraphFingerprintErr = err
			return
		}
		if _, err := db.Exec(codeschema.CurrentSQL()); err != nil {
			privateWithCodeGraphFingerprintErr = err
			return
		}
		privateWithCodeGraphFingerprintValue, privateWithCodeGraphFingerprintErr = privateSchemaFingerprint(db)
	})
	return privateWithCodeGraphFingerprintValue, privateWithCodeGraphFingerprintErr
}

func privateSidecarsPresent(path string) (bool, error) {
	present := false
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("private database sidecar %s cannot be classified safely", sidecar)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("private database sidecar %s is not a regular file", sidecar)
		}
		present = true
	}
	return present, nil
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
	if privateSchemaV6ValidationHook != nil {
		if err := privateSchemaV6ValidationHook(tx); err != nil {
			return err
		}
	}
	if err := validateCurrentPrivateSchema(tx); err != nil {
		return fmt.Errorf("localstore: validate private schema v6: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("localstore: commit private schema v6: %w", err)
	}
	return nil
}
