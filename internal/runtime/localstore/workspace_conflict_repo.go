package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type WorkspaceConflictEvidence struct {
	ConflictID   string
	Key          projectstate.RecordKey
	FieldPath    string
	ConflictKind string
	BaseJSON     string
	OursJSON     string
	TheirsJSON   string
}

type WorkspaceConflictOccurrence struct {
	WorkspaceConflictEvidence
	OccurrenceID string
	CreatedAt    time.Time
}

// OpenConflictOccurrences returns every strictly validated open occurrence in
// this transaction's exact workspace.
func (tx *WorkspaceMutationTx) OpenConflictOccurrences(ctx context.Context) ([]WorkspaceConflictOccurrence, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
		       field_path, conflict_kind, base_json, ours_json, theirs_json,
		       state, created_at, resolved_at
		FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND state='open'
		ORDER BY CASE record_kind
			WHEN 'project' THEN 0
			WHEN 'actor' THEN 1
			WHEN 'task' THEN 2
			WHEN 'task_link' THEN 3
			WHEN 'kb_article' THEN 4
			WHEN 'channel' THEN 5
			WHEN 'event' THEN 6
			WHEN 'git_link' THEN 7
			ELSE 8
		END, record_id, field_path, conflict_kind, conflict_id, occurrence_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query open conflict occurrences: %w", err)
	}
	defer rows.Close()

	occurrences := make([]WorkspaceConflictOccurrence, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var projectID, workspaceID, state string
		var resolvedAt sql.NullTime
		var occurrence WorkspaceConflictOccurrence
		if err := rows.Scan(
			&projectID, &workspaceID, &occurrence.OccurrenceID, &occurrence.ConflictID,
			&occurrence.Key.Kind, &occurrence.Key.ID, &occurrence.FieldPath,
			&occurrence.ConflictKind, &occurrence.BaseJSON, &occurrence.OursJSON,
			&occurrence.TheirsJSON, &state, &occurrence.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("localstore: scan open conflict occurrence: %w", err)
		}
		if projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) || state != "open" || resolvedAt.Valid {
			return nil, fmt.Errorf("localstore: malformed open conflict occurrence scope or state")
		}
		if err := validateWorkspaceConflictOccurrence(tx.scope, occurrence); err != nil {
			return nil, fmt.Errorf("localstore: validate open conflict occurrence: %w", err)
		}
		if _, duplicate := seen[occurrence.ConflictID]; duplicate {
			return nil, fmt.Errorf("localstore: duplicate open semantic conflict ID")
		}
		seen[occurrence.ConflictID] = struct{}{}
		occurrence.CreatedAt = occurrence.CreatedAt.UTC()
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate open conflict occurrences: %w", err)
	}
	return occurrences, nil
}

// ReplaceOpenConflictOccurrences atomically reuses exact open evidence,
// resolves absent evidence, and creates a fresh history row for new evidence.
func (tx *WorkspaceMutationTx) ReplaceOpenConflictOccurrences(ctx context.Context, desired []WorkspaceConflictEvidence, resolvedAt time.Time) ([]WorkspaceConflictOccurrence, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if !validUTCTimestamp(resolvedAt) {
		return nil, fmt.Errorf("localstore: invalid conflict resolution timestamp")
	}
	desiredByID := make(map[string]WorkspaceConflictEvidence, len(desired))
	for _, evidence := range desired {
		if err := validateWorkspaceConflictEvidence(tx.scope, evidence); err != nil {
			return nil, fmt.Errorf("localstore: validate desired conflict evidence: %w", err)
		}
		if _, duplicate := desiredByID[evidence.ConflictID]; duplicate {
			return nil, fmt.Errorf("localstore: duplicate desired semantic conflict ID")
		}
		desiredByID[evidence.ConflictID] = evidence
	}

	open, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]WorkspaceConflictOccurrence, len(open))
	for _, occurrence := range open {
		existingByID[occurrence.ConflictID] = occurrence
		if evidence, retained := desiredByID[occurrence.ConflictID]; retained && evidence != occurrence.WorkspaceConflictEvidence {
			return nil, fmt.Errorf("localstore: open semantic conflict evidence changed")
		}
	}
	changed := false

	for _, occurrence := range open {
		if _, retained := desiredByID[occurrence.ConflictID]; retained {
			continue
		}
		result, err := tx.conn.ExecContext(ctx, `
			UPDATE workspace_conflicts
			SET state='resolved', resolved_at=?
			WHERE project_id=? AND workspace_id=? AND occurrence_id=? AND conflict_id=?
			  AND state='open' AND resolved_at IS NULL
		`, resolvedAt.UTC(), tx.scope.ProjectID, tx.scope.WorkspaceID,
			occurrence.OccurrenceID, occurrence.ConflictID,
		)
		if err != nil {
			return nil, fmt.Errorf("localstore: resolve conflict occurrence: %w", err)
		}
		if err := requireConflictRowsAffected(result, "resolve", 1); err != nil {
			return nil, err
		}
		changed = true
	}

	for _, evidence := range desired {
		if _, retained := existingByID[evidence.ConflictID]; retained {
			continue
		}
		occurrenceID, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("localstore: allocate conflict occurrence ID: %w", err)
		}
		result, err := tx.conn.ExecContext(ctx, `
			INSERT INTO workspace_conflicts
			(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
			 field_path, conflict_kind, base_json, ours_json, theirs_json, state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open')
		`, tx.scope.ProjectID, tx.scope.WorkspaceID, occurrenceID.String(),
			evidence.ConflictID, evidence.Key.Kind, evidence.Key.ID, evidence.FieldPath,
			evidence.ConflictKind, evidence.BaseJSON, evidence.OursJSON, evidence.TheirsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("localstore: insert conflict occurrence: %w", err)
		}
		if err := requireConflictRowsAffected(result, "insert", 1); err != nil {
			return nil, err
		}
		changed = true
	}

	replaced, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return nil, err
	}
	if len(replaced) != len(desiredByID) {
		return nil, fmt.Errorf("localstore: open conflict replacement membership mismatch")
	}
	for _, occurrence := range replaced {
		evidence, ok := desiredByID[occurrence.ConflictID]
		if !ok || occurrence.WorkspaceConflictEvidence != evidence {
			return nil, fmt.Errorf("localstore: open conflict replacement evidence mismatch")
		}
	}
	if changed {
		if err := tx.markWorkspaceDirty(ctx); err != nil {
			return nil, err
		}
	}
	return replaced, nil
}

func requireConflictRowsAffected(result sql.Result, action string, want int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect conflict occurrence %s: %w", action, err)
	}
	if affected != want {
		return fmt.Errorf("localstore: conflict occurrence %s affected %d rows, want %d", action, affected, want)
	}
	return nil
}

func validateWorkspaceConflictOccurrence(scope types.WorkspaceScope, occurrence WorkspaceConflictOccurrence) error {
	if err := validateWorkspaceConflictEvidence(scope, occurrence.WorkspaceConflictEvidence); err != nil {
		return err
	}
	if occurrence.OccurrenceID != occurrence.ConflictID && !validCanonicalUUIDv4(occurrence.OccurrenceID) {
		return fmt.Errorf("invalid conflict occurrence ID")
	}
	if !validUTCTimestamp(occurrence.CreatedAt) {
		return fmt.Errorf("invalid conflict creation timestamp")
	}
	return nil
}

func validateWorkspaceConflictEvidence(scope types.WorkspaceScope, evidence WorkspaceConflictEvidence) error {
	if !validConflictDigest(evidence.ConflictID) {
		return fmt.Errorf("invalid semantic conflict ID")
	}
	if !validConflictRecordKey(scope, evidence.Key) {
		return fmt.Errorf("invalid conflict record key")
	}
	if !validConflictFieldPath(evidence.FieldPath) {
		return fmt.Errorf("invalid conflict field path")
	}
	if !validConflictKind(evidence.ConflictKind) {
		return fmt.Errorf("invalid conflict kind")
	}
	for _, encoded := range []string{evidence.BaseJSON, evidence.OursJSON, evidence.TheirsJSON} {
		if err := validateWorkspaceConflictFieldEnvelope(encoded); err != nil {
			return err
		}
	}
	return nil
}

type workspaceConflictFieldEnvelope struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

func validateWorkspaceConflictFieldEnvelope(encoded string) error {
	if encoded == "" || !utf8.ValidString(encoded) {
		return fmt.Errorf("invalid conflict evidence bytes")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope *workspaceConflictFieldEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("invalid conflict field envelope: %w", err)
	}
	if envelope == nil {
		return fmt.Errorf("invalid conflict field envelope: null")
	}
	if err := requireWorkspaceConflictJSONEOF(decoder); err != nil {
		return err
	}
	if !envelope.Present {
		if envelope.Value != nil {
			return fmt.Errorf("absent conflict field has a value")
		}
	} else {
		if len(envelope.Value) == 0 {
			return fmt.Errorf("present conflict field has no value")
		}
		valueDecoder := json.NewDecoder(bytes.NewReader(envelope.Value))
		valueDecoder.UseNumber()
		var value any
		if err := valueDecoder.Decode(&value); err != nil {
			return fmt.Errorf("invalid conflict field value: %w", err)
		}
		if err := requireWorkspaceConflictJSONEOF(valueDecoder); err != nil {
			return err
		}
	}
	return nil
}

func requireWorkspaceConflictJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple conflict JSON values")
		}
		return fmt.Errorf("trailing conflict JSON: %w", err)
	}
	return nil
}

func validConflictDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validConflictFieldPath(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || value[0] != '/' {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] == '~' {
			if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
				return false
			}
			index++
		}
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validConflictKind(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (index > 0 && ((char >= '0' && char <= '9') || char == '_')) {
			continue
		}
		return false
	}
	return true
}
