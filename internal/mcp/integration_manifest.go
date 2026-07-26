package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/google/uuid"
)

const (
	IntegrationManifestPublishPermission = "integration_manifest.publish"
	IntegrationManifestRevokePermission  = "integration_manifest.revoke"

	IntegrationManifestOffered = "offered"
	IntegrationManifestRevoked = "revoked"

	maxIntegrationManifestVersion = int64(9007199254740991)
)

var (
	ErrIntegrationManifestPermission   = errors.New("integration manifest: permission denied")
	ErrIntegrationManifestProject      = errors.New("integration manifest: project binding mismatch")
	ErrIntegrationManifestInvalid      = errors.New("integration manifest: invalid")
	ErrIntegrationManifestNotFound     = errors.New("integration manifest: not found")
	ErrIntegrationManifestEquivocation = errors.New("integration manifest: equivocation")
	ErrIntegrationManifestReplay       = errors.New("integration manifest: replay")
	ErrIntegrationManifestLineage      = errors.New("integration manifest: active lineage conflict")
	ErrIntegrationManifestRevoked      = errors.New("integration manifest: lineage revoked")
)

var (
	integrationDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	integrationSlugPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	integrationSkillTarget   = regexp.MustCompile(`^\.agents/skills/wormhole-[a-z][a-z0-9]*(?:-[a-z0-9]+)*/SKILL\.md$`)
	integrationRefTarget     = regexp.MustCompile(`^\.agents/skills/wormhole-[a-z][a-z0-9]*(?:-[a-z0-9]+)*/references/[a-z][a-z0-9]*(?:-[a-z0-9]+)*\.md$`)
)

// IntegrationManifest is the exact declarative V1 body Fabric preserves.
// Gateway independently verifies its content and structured digests before
// treating an offered body as an approval candidate.
type IntegrationManifest struct {
	SchemaVersion      int                        `json:"schema_version"`
	ManifestID         string                     `json:"manifest_id"`
	ManifestVersion    int64                      `json:"manifest_version"`
	ProjectID          string                     `json:"project_id"`
	Source             string                     `json:"source"`
	CreatedAt          string                     `json:"created_at"`
	ToolContractDigest string                     `json:"tool_contract_digest"`
	ManifestDigest     string                     `json:"manifest_digest"`
	RoleFilters        []string                   `json:"role_filters"`
	Entries            []IntegrationManifestEntry `json:"entries"`
}

type IntegrationManifestEntry struct {
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	Content       string   `json:"content"`
	ContentDigest string   `json:"content_digest"`
	MergePolicy   string   `json:"merge_policy"`
	Required      bool     `json:"required"`
	RoleFilters   []string `json:"role_filters"`
}

// IntegrationManifestRecord is Fabric's immutable stored version plus
// authenticated publication/revocation metadata.
type IntegrationManifestRecord struct {
	Manifest    IntegrationManifest
	PublishedAt time.Time
	PublishedBy string
	RevokedAt   *time.Time
	RevokedBy   *string
}

// IntegrationManifestChange is the bootstrap/incremental transport record.
// Revocations intentionally omit content while retaining the exact tuple.
type IntegrationManifestChange struct {
	Operation       string               `json:"operation"`
	ProjectID       string               `json:"project_id"`
	ManifestID      string               `json:"manifest_id"`
	ManifestVersion int64                `json:"manifest_version"`
	ManifestDigest  string               `json:"manifest_digest"`
	Manifest        *IntegrationManifest `json:"manifest"`
	ChangedAt       string               `json:"changed_at"`
}

type IntegrationManifestStore struct {
	db *sql.DB
}

func NewIntegrationManifestStore(db *sql.DB) *IntegrationManifestStore {
	return &IntegrationManifestStore{db: db}
}

func (s *IntegrationManifestStore) Publish(ctx context.Context, scope *identity.AuthenticatedScope, manifest IntegrationManifest) (IntegrationManifestRecord, error) {
	if err := authoriseIntegrationManifest(scope, manifest.ProjectID, IntegrationManifestPublishPermission); err != nil {
		return IntegrationManifestRecord{}, err
	}
	if err := validateFabricIntegrationManifest(manifest); err != nil {
		return IntegrationManifestRecord{}, err
	}
	rolesJSON, err := json.Marshal(manifest.RoleFilters)
	if err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: encode role filters: %w", err)
	}
	entriesJSON, err := json.Marshal(manifest.Entries)
	if err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: encode entries: %w", err)
	}
	authoredAt, _ := time.Parse(time.RFC3339Nano, manifest.CreatedAt)

	tx, err := s.beginProjectTx(ctx, manifest.ProjectID)
	if err != nil {
		return IntegrationManifestRecord{}, err
	}
	defer tx.Rollback()

	var activeID string
	err = tx.QueryRowContext(ctx, `
		SELECT manifest_id FROM integration_manifest_lineages
		WHERE project_id = $1 AND active
		FOR UPDATE`, manifest.ProjectID).Scan(&activeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var existingActive bool
		err = tx.QueryRowContext(ctx, `
			SELECT active FROM integration_manifest_lineages
			WHERE project_id = $1 AND manifest_id = $2
			FOR UPDATE`, manifest.ProjectID, manifest.ManifestID).Scan(&existingActive)
		if err == nil {
			return IntegrationManifestRecord{}, ErrIntegrationManifestRevoked
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: inspect lineage: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO integration_manifest_lineages
				(project_id, manifest_id, active, created_by_agent_id)
			VALUES ($1, $2, true, $3)`, manifest.ProjectID, manifest.ManifestID, scope.Agent.ID); err != nil {
			return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: create lineage: %w", err)
		}
	case err != nil:
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: lock active lineage: %w", err)
	case activeID != manifest.ManifestID:
		return IntegrationManifestRecord{}, ErrIntegrationManifestLineage
	}

	var existingDigest string
	err = tx.QueryRowContext(ctx, `
		SELECT manifest_digest FROM integration_manifest_versions
		WHERE project_id = $1 AND manifest_id = $2 AND manifest_version = $3`,
		manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion).Scan(&existingDigest)
	if err == nil {
		if existingDigest != manifest.ManifestDigest {
			return IntegrationManifestRecord{}, ErrIntegrationManifestEquivocation
		}
		record, readErr := readIntegrationManifestRecord(ctx, tx, manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion)
		if readErr != nil {
			return IntegrationManifestRecord{}, readErr
		}
		if err := tx.Commit(); err != nil {
			return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: commit replay: %w", err)
		}
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: inspect version: %w", err)
	}
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT max(manifest_version) FROM integration_manifest_versions
		WHERE project_id = $1 AND manifest_id = $2`, manifest.ProjectID, manifest.ManifestID).Scan(&latest); err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: inspect latest version: %w", err)
	}
	if latest.Valid && manifest.ManifestVersion <= latest.Int64 {
		return IntegrationManifestRecord{}, ErrIntegrationManifestReplay
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO integration_manifest_versions
			(project_id, manifest_id, manifest_version, schema_version, source, authored_at,
			 tool_contract_digest, manifest_digest, role_filters, entries, published_by_agent_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion, manifest.SchemaVersion, manifest.Source,
		authoredAt, manifest.ToolContractDigest, manifest.ManifestDigest, rolesJSON, entriesJSON, scope.Agent.ID); err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: publish: %w", err)
	}
	record, err := readIntegrationManifestRecord(ctx, tx, manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion)
	if err != nil {
		return IntegrationManifestRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: commit publish: %w", err)
	}
	return record, nil
}

func (s *IntegrationManifestStore) Revoke(ctx context.Context, scope *identity.AuthenticatedScope, projectID, manifestID string, version int64, digest string) (IntegrationManifestChange, error) {
	if err := authoriseIntegrationManifest(scope, projectID, IntegrationManifestRevokePermission); err != nil {
		return IntegrationManifestChange{}, err
	}
	if !canonicalUUID(projectID) || !canonicalUUID(manifestID) || version < 1 || version > maxIntegrationManifestVersion || !integrationDigestPattern.MatchString(digest) {
		return IntegrationManifestChange{}, ErrIntegrationManifestInvalid
	}
	tx, err := s.beginProjectTx(ctx, projectID)
	if err != nil {
		return IntegrationManifestChange{}, err
	}
	defer tx.Rollback()

	var active bool
	if err := tx.QueryRowContext(ctx, `
		SELECT active FROM integration_manifest_lineages
		WHERE project_id = $1 AND manifest_id = $2 FOR UPDATE`, projectID, manifestID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IntegrationManifestChange{}, ErrIntegrationManifestNotFound
		}
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: lock revoke lineage: %w", err)
	}
	record, err := readIntegrationManifestRecord(ctx, tx, projectID, manifestID, version)
	if err != nil {
		return IntegrationManifestChange{}, err
	}
	if record.Manifest.ManifestDigest != digest {
		return IntegrationManifestChange{}, ErrIntegrationManifestEquivocation
	}
	if record.RevokedAt != nil {
		if err := tx.Commit(); err != nil {
			return IntegrationManifestChange{}, fmt.Errorf("integration manifest: commit revoked replay: %w", err)
		}
		return integrationManifestChange(record), nil
	}
	var latest int64
	if err := tx.QueryRowContext(ctx, `
		SELECT max(manifest_version) FROM integration_manifest_versions
		WHERE project_id = $1 AND manifest_id = $2`, projectID, manifestID).Scan(&latest); err != nil {
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: inspect revoke version: %w", err)
	}
	if !active || latest != version {
		return IntegrationManifestChange{}, ErrIntegrationManifestReplay
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE integration_manifest_versions
		SET revoked_at = now(), revoked_by_agent_id = $4
		WHERE project_id = $1 AND manifest_id = $2 AND manifest_version = $3`, projectID, manifestID, version, scope.Agent.ID); err != nil {
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: revoke: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE integration_manifest_lineages SET active = false
		WHERE project_id = $1 AND manifest_id = $2`, projectID, manifestID); err != nil {
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: deactivate lineage: %w", err)
	}
	record, err = readIntegrationManifestRecord(ctx, tx, projectID, manifestID, version)
	if err != nil {
		return IntegrationManifestChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: commit revoke: %w", err)
	}
	return integrationManifestChange(record), nil
}

func (s *IntegrationManifestStore) Current(ctx context.Context, projectID string) (IntegrationManifestChange, error) {
	if !canonicalUUID(projectID) {
		return IntegrationManifestChange{}, ErrIntegrationManifestProject
	}
	tx, err := s.beginProjectTx(ctx, projectID)
	if err != nil {
		return IntegrationManifestChange{}, err
	}
	defer tx.Rollback()
	change, err := s.currentInTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationManifestChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationManifestChange{}, fmt.Errorf("integration manifest: commit current read: %w", err)
	}
	return change, nil
}

func (s *IntegrationManifestStore) CurrentInTx(ctx context.Context, tx *sql.Tx, projectID string) (IntegrationManifestChange, error) {
	if tx == nil || !canonicalUUID(projectID) {
		return IntegrationManifestChange{}, ErrIntegrationManifestProject
	}
	return s.currentInTx(ctx, tx, projectID)
}

func (s *IntegrationManifestStore) currentInTx(ctx context.Context, tx *sql.Tx, projectID string) (IntegrationManifestChange, error) {
	row := tx.QueryRowContext(ctx, integrationManifestRecordSelect+`
		WHERE v.project_id = $1
		ORDER BY GREATEST(v.published_at, COALESCE(v.revoked_at, v.published_at)) DESC,
		         v.manifest_version DESC, v.manifest_id DESC
		LIMIT 1`, projectID)
	record, err := scanIntegrationManifestRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IntegrationManifestChange{}, ErrIntegrationManifestNotFound
		}
		return IntegrationManifestChange{}, err
	}
	return integrationManifestChange(record), nil
}

func (s *IntegrationManifestStore) History(ctx context.Context, projectID string) ([]IntegrationManifestRecord, error) {
	if !canonicalUUID(projectID) {
		return nil, ErrIntegrationManifestProject
	}
	tx, err := s.beginProjectTx(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, integrationManifestRecordSelect+`
		WHERE v.project_id = $1
		ORDER BY v.published_at, v.manifest_id, v.manifest_version`, projectID)
	if err != nil {
		return nil, fmt.Errorf("integration manifest: list history: %w", err)
	}
	defer rows.Close()
	records := []IntegrationManifestRecord{}
	for rows.Next() {
		record, err := scanIntegrationManifestRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integration manifest: list history rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("integration manifest: commit history read: %w", err)
	}
	return records, nil
}

func (s *IntegrationManifestStore) ChangesSince(ctx context.Context, projectID string, cursor time.Time) ([]IntegrationManifestChange, error) {
	if !canonicalUUID(projectID) {
		return nil, ErrIntegrationManifestProject
	}
	tx, err := s.beginProjectTx(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, integrationManifestRecordSelect+`
		WHERE v.project_id = $1
		  AND GREATEST(v.published_at, COALESCE(v.revoked_at, v.published_at)) > $2
		ORDER BY GREATEST(v.published_at, COALESCE(v.revoked_at, v.published_at)),
		         v.manifest_id, v.manifest_version`, projectID, cursor)
	if err != nil {
		return nil, fmt.Errorf("integration manifest: list changes: %w", err)
	}
	defer rows.Close()
	changes := []IntegrationManifestChange{}
	for rows.Next() {
		record, err := scanIntegrationManifestRecord(rows)
		if err != nil {
			return nil, err
		}
		changes = append(changes, integrationManifestChange(record))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integration manifest: list change rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("integration manifest: commit changes read: %w", err)
	}
	return changes, nil
}

func (s *IntegrationManifestStore) beginProjectTx(ctx context.Context, projectID string) (*sql.Tx, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("integration manifest: store unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("integration manifest: begin project transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('wormhole.project_id', $1, true)`, projectID); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("integration manifest: set project scope: %w", err)
	}
	return tx, nil
}

const integrationManifestRecordSelect = `
	SELECT v.schema_version, v.manifest_id, v.manifest_version, v.project_id,
	       v.source, v.authored_at, v.tool_contract_digest, v.manifest_digest,
	       v.role_filters, v.entries, v.published_at, v.published_by_agent_id,
	       v.revoked_at, v.revoked_by_agent_id
	FROM integration_manifest_versions v `

type integrationManifestScanner interface {
	Scan(...any) error
}

func readIntegrationManifestRecord(ctx context.Context, tx *sql.Tx, projectID, manifestID string, version int64) (IntegrationManifestRecord, error) {
	row := tx.QueryRowContext(ctx, integrationManifestRecordSelect+`
		WHERE v.project_id = $1 AND v.manifest_id = $2 AND v.manifest_version = $3`, projectID, manifestID, version)
	record, err := scanIntegrationManifestRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationManifestRecord{}, ErrIntegrationManifestNotFound
	}
	return record, err
}

func scanIntegrationManifestRecord(scanner integrationManifestScanner) (IntegrationManifestRecord, error) {
	var record IntegrationManifestRecord
	var authoredAt time.Time
	var rolesJSON, entriesJSON []byte
	var revokedAt sql.NullTime
	var revokedBy sql.NullString
	err := scanner.Scan(
		&record.Manifest.SchemaVersion, &record.Manifest.ManifestID, &record.Manifest.ManifestVersion, &record.Manifest.ProjectID,
		&record.Manifest.Source, &authoredAt, &record.Manifest.ToolContractDigest, &record.Manifest.ManifestDigest,
		&rolesJSON, &entriesJSON, &record.PublishedAt, &record.PublishedBy, &revokedAt, &revokedBy,
	)
	if err != nil {
		return IntegrationManifestRecord{}, err
	}
	record.Manifest.CreatedAt = authoredAt.UTC().Format(time.RFC3339Nano)
	if err := json.Unmarshal(rolesJSON, &record.Manifest.RoleFilters); err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: decode role filters: %w", err)
	}
	if err := json.Unmarshal(entriesJSON, &record.Manifest.Entries); err != nil {
		return IntegrationManifestRecord{}, fmt.Errorf("integration manifest: decode entries: %w", err)
	}
	if revokedAt.Valid {
		value := revokedAt.Time.UTC()
		record.RevokedAt = &value
	}
	if revokedBy.Valid {
		value := revokedBy.String
		record.RevokedBy = &value
	}
	return record, nil
}

func integrationManifestChange(record IntegrationManifestRecord) IntegrationManifestChange {
	operation := IntegrationManifestOffered
	changedAt := record.PublishedAt.UTC()
	manifest := record.Manifest
	manifestPointer := &manifest
	if record.RevokedAt != nil {
		operation = IntegrationManifestRevoked
		changedAt = record.RevokedAt.UTC()
		manifestPointer = nil
	}
	return IntegrationManifestChange{
		Operation: operation, ProjectID: record.Manifest.ProjectID, ManifestID: record.Manifest.ManifestID,
		ManifestVersion: record.Manifest.ManifestVersion, ManifestDigest: record.Manifest.ManifestDigest,
		Manifest: manifestPointer, ChangedAt: changedAt.Format(time.RFC3339Nano),
	}
}

func authoriseIntegrationManifest(scope *identity.AuthenticatedScope, projectID, permission string) error {
	if scope == nil || scope.Agent.ID == "" || scope.ProjectID != projectID {
		return ErrIntegrationManifestProject
	}
	if !scope.HasPermission(permission) {
		return ErrIntegrationManifestPermission
	}
	return nil
}

func validateFabricIntegrationManifest(manifest IntegrationManifest) error {
	if manifest.SchemaVersion != 1 || manifest.Source != "fabric" || !canonicalUUID(manifest.ProjectID) || !canonicalUUID(manifest.ManifestID) ||
		manifest.ManifestVersion < 1 || manifest.ManifestVersion > maxIntegrationManifestVersion ||
		!integrationDigestPattern.MatchString(manifest.ToolContractDigest) || !integrationDigestPattern.MatchString(manifest.ManifestDigest) {
		return ErrIntegrationManifestInvalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil || createdAt.UTC().Format(time.RFC3339Nano) != manifest.CreatedAt {
		return ErrIntegrationManifestInvalid
	}
	if !validIntegrationRoles(manifest.RoleFilters) || len(manifest.Entries) == 0 || len(manifest.Entries) > 64 {
		return ErrIntegrationManifestInvalid
	}
	total := 0
	previousTarget := ""
	for _, entry := range manifest.Entries {
		if previousTarget != "" && entry.Target <= previousTarget {
			return ErrIntegrationManifestInvalid
		}
		previousTarget = entry.Target
		if !validFabricIntegrationEntry(entry) {
			return ErrIntegrationManifestInvalid
		}
		total += len(entry.Content)
	}
	if total > 1<<20 {
		return ErrIntegrationManifestInvalid
	}
	return nil
}

func validFabricIntegrationEntry(entry IntegrationManifestEntry) bool {
	if !integrationDigestPattern.MatchString(entry.ContentDigest) || !validIntegrationRoles(entry.RoleFilters) ||
		!utf8.ValidString(entry.Content) || len(entry.Content) > 262144 || strings.ContainsAny(entry.Content, "\x00\r") ||
		!strings.HasSuffix(entry.Content, "\n") || strings.HasSuffix(entry.Content, "\n\n") || strings.Trim(entry.Content, "\n") == "" ||
		strings.Contains(entry.Content, "<!-- wormhole:") || strings.Contains(entry.Target, "\\") || strings.Contains(entry.Target, "%") ||
		filepath.IsAbs(entry.Target) {
		return false
	}
	switch entry.Kind {
	case "agents_bootstrap":
		return entry.Target == "AGENTS.md" && entry.MergePolicy == "managed_section"
	case "skill":
		return integrationSkillTarget.MatchString(entry.Target) && entry.MergePolicy == "managed_file"
	case "reference":
		return integrationRefTarget.MatchString(entry.Target) && entry.MergePolicy == "managed_file"
	default:
		return false
	}
}

func validIntegrationRoles(roles []string) bool {
	if roles == nil || len(roles) > 64 || !sort.StringsAreSorted(roles) {
		return false
	}
	for index, role := range roles {
		if len(role) < 1 || len(role) > 63 || !integrationSlugPattern.MatchString(role) || (index > 0 && roles[index-1] == role) {
			return false
		}
	}
	return true
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func integrationManifestApplies(change IntegrationManifestChange, roles []string) bool {
	if len(roles) != 1 || !validIntegrationRoles(roles) {
		return false
	}
	if change.Operation == IntegrationManifestRevoked || change.Manifest == nil || len(change.Manifest.RoleFilters) == 0 {
		return true
	}
	index := sort.SearchStrings(change.Manifest.RoleFilters, roles[0])
	return index < len(change.Manifest.RoleFilters) && change.Manifest.RoleFilters[index] == roles[0]
}
