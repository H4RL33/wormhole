package localstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

// AuditWorkspaceHistory strictly validates all retained workspace evidence in
// one exact-scope read snapshot. It never repairs, rewrites, or prunes rows.
func (r *WorkspaceRepo) AuditWorkspaceHistory(ctx context.Context, scope types.WorkspaceScope) error {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) {
		return ErrNotFound
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("localstore: acquire workspace history audit connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return fmt.Errorf("localstore: begin workspace history audit: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	tx := &WorkspaceMutationTx{conn: conn, scope: scope}
	if _, err := tx.Workspace(ctx); err != nil {
		return err
	}
	if _, err := tx.workspaceMaterializationHistory(ctx); err != nil {
		return fmt.Errorf("localstore: audit workspace materialization history: %w", err)
	}
	operations, err := tx.workspaceOperationHistory(ctx)
	if err != nil {
		return fmt.Errorf("localstore: audit workspace operation history: %w", err)
	}
	if err := tx.auditWorkspaceStashHistory(ctx, operations); err != nil {
		return err
	}
	if err := tx.auditWorkspaceConflictHistory(ctx); err != nil {
		return err
	}
	if _, _, err := tx.auditPublicationPolicyState(ctx); err != nil {
		return fmt.Errorf("localstore: audit workspace publication policy history: %w", err)
	}
	if err := tx.auditWorkspaceReceiptHistory(ctx); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("localstore: commit workspace history audit snapshot: %w", err)
	}
	committed = true
	return nil
}

func (tx *WorkspaceMutationTx) auditWorkspaceStashHistory(
	ctx context.Context,
	operations []WorkspaceOperationAuditRecord,
) error {
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, stash_id
		FROM workspace_stashes
		WHERE project_id=? AND workspace_id=?
		ORDER BY stash_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("localstore: query workspace stash history: %w", err)
	}
	stashIDs := make([]string, 0)
	for rows.Next() {
		var projectID, workspaceID, stashID string
		if err := rows.Scan(&projectID, &workspaceID, &stashID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("localstore: scan workspace stash history key: %w", err)
		}
		if projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) ||
			!validCanonicalUUIDv4(stashID) || (len(stashIDs) > 0 && stashID <= stashIDs[len(stashIDs)-1]) {
			_ = rows.Close()
			return fmt.Errorf("localstore: invalid ordered workspace stash history key")
		}
		stashIDs = append(stashIDs, stashID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("localstore: iterate workspace stash history keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("localstore: close workspace stash history keys: %w", err)
	}
	for _, stashID := range stashIDs {
		stash, err := tx.Stash(ctx, stashID)
		if err != nil {
			return fmt.Errorf("localstore: audit workspace stash %s: %w", stashID, err)
		}
		if stash == nil || stash.StashID != stashID {
			return fmt.Errorf("localstore: workspace stash history membership mismatch")
		}
	}
	stashMembership := make(map[string]struct{}, len(stashIDs))
	for _, stashID := range stashIDs {
		stashMembership[stashID] = struct{}{}
	}
	for _, operation := range operations {
		if operation.StashedByStashID == nil {
			continue
		}
		if _, ok := stashMembership[*operation.StashedByStashID]; !ok {
			return fmt.Errorf("localstore: workspace operation stash owner is absent from exact scope")
		}
	}
	return nil
}

func (tx *WorkspaceMutationTx) auditWorkspaceConflictHistory(ctx context.Context) error {
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
		       field_path, conflict_kind, base_json, ours_json, theirs_json,
		       state, created_at, resolved_at
		FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND state='resolved'
		ORDER BY created_at, occurrence_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("localstore: query resolved workspace conflict history: %w", err)
	}
	defer rows.Close()
	var previousCreated time.Time
	var previousOccurrence string
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
			return fmt.Errorf("localstore: scan resolved workspace conflict history: %w", err)
		}
		if projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) ||
			state != "resolved" || !resolvedAt.Valid || !validUTCTimestamp(resolvedAt.Time) ||
			resolvedAt.Time.Before(occurrence.CreatedAt) {
			return fmt.Errorf("localstore: malformed resolved workspace conflict history")
		}
		if err := validateWorkspaceConflictOccurrence(tx.scope, occurrence); err != nil {
			return fmt.Errorf("localstore: validate resolved workspace conflict history: %w", err)
		}
		if !previousCreated.IsZero() && (occurrence.CreatedAt.Before(previousCreated) ||
			(occurrence.CreatedAt.Equal(previousCreated) && occurrence.OccurrenceID <= previousOccurrence)) {
			return fmt.Errorf("localstore: resolved workspace conflict history is not strictly ordered")
		}
		previousCreated = occurrence.CreatedAt
		previousOccurrence = occurrence.OccurrenceID
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("localstore: iterate resolved workspace conflict history: %w", err)
	}
	return nil
}

func (tx *WorkspaceMutationTx) auditWorkspaceReceiptHistory(ctx context.Context) error {
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, request_id
		FROM workspace_transition_receipts
		WHERE project_id=? AND workspace_id=?
		ORDER BY request_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("localstore: query workspace receipt history: %w", err)
	}
	requestIDs := make([]string, 0)
	for rows.Next() {
		var projectID, workspaceID, requestID string
		if err := rows.Scan(&projectID, &workspaceID, &requestID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("localstore: scan workspace receipt history key: %w", err)
		}
		if projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) ||
			!types.CanonicalUUID(requestID) || (len(requestIDs) > 0 && requestID <= requestIDs[len(requestIDs)-1]) {
			_ = rows.Close()
			return fmt.Errorf("localstore: invalid ordered workspace receipt history key")
		}
		requestIDs = append(requestIDs, requestID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("localstore: iterate workspace receipt history keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("localstore: close workspace receipt history keys: %w", err)
	}
	for _, requestID := range requestIDs {
		receipt, err := tx.TransitionReceipt(ctx, requestID)
		if err != nil {
			return fmt.Errorf("localstore: audit workspace receipt %s: %w", requestID, err)
		}
		if receipt == nil || receipt.RequestID != requestID {
			return fmt.Errorf("localstore: workspace receipt history membership mismatch")
		}
	}
	return nil
}
