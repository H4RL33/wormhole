package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const activityPromotionExtensionName = "dev.wormhole.promotion"

type ActivityPromotionReceiptRecord struct {
	Scope            types.WorkspaceScope
	SourceActivityID string
	SourceKey        types.ActivityOriginKey
	SourceDigest     state.Digest
	EventID          string
	OperationID      string
	Promoter         types.ActorEnvelope
	PromotedAt       time.Time
}

type activityPromotionExtensionDataV1 struct {
	SourceActivityID     string       `json:"source_activity_id"`
	SourceActivityDigest state.Digest `json:"source_activity_digest"`
}

// ActivityPromotionSource strict-loads the one retained source with activityID
// in this transaction's immutable workspace and returns its receipt-owned
// policy. Source IDs that are ambiguous across origins fail closed.
func (tx *WorkspaceMutationTx) ActivityPromotionSource(ctx context.Context, activityID string, expectedDigest state.Digest) (ActivityRecord, ActivityPolicyRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) || !types.CanonicalUUID(activityID) ||
		!activityDigestPattern.MatchString(string(expectedDigest)) {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("source", ErrActivityNotFound)
	}
	rows, err := tx.conn.QueryContext(ctx, `SELECT fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id
		FROM activity_ledger WHERE project_id=? AND workspace_id=? AND activity_id=?
		ORDER BY fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id`,
		tx.scope.ProjectID, tx.scope.WorkspaceID, activityID)
	if err != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("query source", ErrActivityReplayConflict)
	}
	keys := make([]types.ActivityOriginKey, 0, 2)
	for rows.Next() {
		key := types.ActivityOriginKey{Route: types.ActivityRouteKey{ProjectID: tx.scope.ProjectID, WorkspaceID: tx.scope.WorkspaceID}, ActivityID: activityID}
		if err := rows.Scan(&key.Route.FabricInstanceID, &key.Route.RemoteProjectID, &key.Route.StreamID,
			&key.Route.CanonicalRef, &key.SourceWorkspaceID); err != nil {
			_ = rows.Close()
			return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("scan source", ErrActivityReplayConflict)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("iterate source", ErrActivityReplayConflict)
	}
	if err := rows.Close(); err != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("close source", ErrActivityReplayConflict)
	}
	if len(keys) == 0 {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("source", ErrActivityNotFound)
	}
	if len(keys) != 1 || keys[0].Validate() != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("source", ErrActivityReplayConflict)
	}
	evidence, err := newActivityEvidenceLoader().load(ctx, tx.conn, keys[0])
	if err != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, err
	}
	if evidence.record.ActivityDigest != expectedDigest || evidence.receipt == nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, activityPromotionError("source", ErrActivityReplayConflict)
	}
	receipt := evidence.receipt.receipt
	record, err := evidence.recordForPolicy(receipt.PolicyVersion, receipt.PolicyDigest)
	if err != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, err
	}
	policy, err := activityPolicyVersion(ctx, tx.conn, keys[0].Route, receipt.PolicyVersion, receipt.PolicyDigest)
	if err != nil {
		return ActivityRecord{}, ActivityPolicyRecord{}, err
	}
	return cloneActivityRecord(record), cloneActivityPolicyRecord(policy), nil
}

// ActivityPromotionReceipt strict-reads the immutable source marker and the
// exact retained canonical operation it identifies. An absent marker is a
// normal nil result; malformed or incomplete evidence fails closed.
func (tx *WorkspaceMutationTx) ActivityPromotionReceipt(ctx context.Context, sourceActivityID string) (*ActivityPromotionReceiptRecord, WorkspaceOperation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) || !types.CanonicalUUID(sourceActivityID) {
		return nil, WorkspaceOperation{}, activityPromotionError("receipt", ErrActivityNotFound)
	}
	record, err := tx.readActivityPromotionReceipt(ctx, sourceActivityID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, WorkspaceOperation{}, nil
	}
	if err != nil {
		return nil, WorkspaceOperation{}, err
	}
	operation, err := tx.readActivityPromotionOperation(ctx, record)
	if err != nil {
		return nil, WorkspaceOperation{}, err
	}
	evidence, err := newActivityEvidenceLoader().load(ctx, tx.conn, record.SourceKey)
	if err != nil || evidence.record.ActivityDigest != record.SourceDigest || !evidence.promoted ||
		!hasConfirmedActivityPromotionLifecycle(evidence, record) {
		return nil, WorkspaceOperation{}, activityPromotionError("receipt evidence", ErrActivityReplayConflict)
	}
	clone := record
	return &clone, cloneWorkspaceOperation(operation), nil
}

func (tx *WorkspaceMutationTx) readActivityPromotionReceipt(ctx context.Context, sourceActivityID string) (ActivityPromotionReceiptRecord, error) {
	var record ActivityPromotionReceiptRecord
	record.Scope = tx.scope
	var promoterJSON []byte
	var promotedAt string
	err := tx.conn.QueryRowContext(ctx, `SELECT source_project_id,source_workspace_binding_id,source_fabric_instance_id,
		source_remote_project_id,source_stream_id,source_canonical_ref,source_origin_workspace_id,source_activity_id,
		source_activity_digest,event_id,operation_id,canonical_promoter_json,CAST(promoted_at AS TEXT)
		FROM activity_promotion_receipts WHERE local_project_id=? AND local_workspace_id=? AND source_activity_id=?`,
		tx.scope.ProjectID, tx.scope.WorkspaceID, sourceActivityID).Scan(
		&record.SourceKey.Route.ProjectID, &record.SourceKey.Route.WorkspaceID, &record.SourceKey.Route.FabricInstanceID,
		&record.SourceKey.Route.RemoteProjectID, &record.SourceKey.Route.StreamID, &record.SourceKey.Route.CanonicalRef,
		&record.SourceKey.SourceWorkspaceID, &record.SourceKey.ActivityID, &record.SourceDigest, &record.EventID,
		&record.OperationID, &promoterJSON, &promotedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ActivityPromotionReceiptRecord{}, sql.ErrNoRows
		}
		return ActivityPromotionReceiptRecord{}, activityPromotionError("read receipt", ErrActivityReplayConflict)
	}
	record.SourceActivityID = sourceActivityID
	record.Promoter, err = decodeCanonicalTransitionActor(promoterJSON)
	if err != nil || record.Promoter.ValidateLocalAction() != nil {
		return ActivityPromotionReceiptRecord{}, activityPromotionError("decode promoter", ErrActivityReplayConflict)
	}
	record.PromotedAt, err = parseSQLiteActivityTimestamp(promotedAt)
	if err != nil || record.SourceKey.Validate() != nil || record.SourceKey.Route.ProjectID != tx.scope.ProjectID ||
		record.SourceKey.Route.WorkspaceID != tx.scope.WorkspaceID || record.SourceKey.ActivityID != sourceActivityID ||
		!activityDigestPattern.MatchString(string(record.SourceDigest)) || !types.CanonicalUUID(record.EventID) ||
		!types.CanonicalUUID(record.OperationID) {
		return ActivityPromotionReceiptRecord{}, activityPromotionError("validate receipt", ErrActivityReplayConflict)
	}
	return record, nil
}

// InsertActivityPromotionReceipt appends the immutable marker after verifying
// that its exact operation already exists on this transaction's workspace.
func (tx *WorkspaceMutationTx) InsertActivityPromotionReceipt(ctx context.Context, receipt ActivityPromotionReceiptRecord) error {
	if tx == nil || tx.conn == nil || !validActivityPromotionReceipt(tx.scope, receipt) {
		return activityPromotionError("insert receipt", ErrActivityReplayConflict)
	}
	if _, err := tx.readActivityPromotionOperation(ctx, receipt); err != nil {
		return err
	}
	if _, _, err := tx.ActivityPromotionSource(ctx, receipt.SourceActivityID, receipt.SourceDigest); err != nil {
		return err
	}
	promoterJSON, err := state.CanonicalJSON(receipt.Promoter)
	if err != nil {
		return activityPromotionError("encode promoter", ErrActivityReplayConflict)
	}
	_, err = tx.conn.ExecContext(ctx, `INSERT INTO activity_promotion_receipts
		(local_project_id,local_workspace_id,source_activity_id,source_project_id,source_workspace_binding_id,
		 source_fabric_instance_id,source_remote_project_id,source_stream_id,source_canonical_ref,source_origin_workspace_id,
		 source_activity_digest,event_id,operation_id,canonical_promoter_json,promoted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.Scope.ProjectID, receipt.Scope.WorkspaceID, receipt.SourceActivityID,
		receipt.SourceKey.Route.ProjectID, receipt.SourceKey.Route.WorkspaceID, receipt.SourceKey.Route.FabricInstanceID,
		receipt.SourceKey.Route.RemoteProjectID, receipt.SourceKey.Route.StreamID, receipt.SourceKey.Route.CanonicalRef,
		receipt.SourceKey.SourceWorkspaceID, receipt.SourceDigest, receipt.EventID, receipt.OperationID,
		promoterJSON, sqliteActivityTimestamp(receipt.PromotedAt))
	if err != nil {
		return activityPromotionError("insert receipt", ErrActivityReplayConflict)
	}
	return nil
}

// ConfirmActivityPromotionLifecycle appends the terminal receipt/confirmed
// source marker using the finite retained policy captured for this promotion.
func (tx *WorkspaceMutationTx) ConfirmActivityPromotionLifecycle(ctx context.Context, key types.ActivityOriginKey, policyVersion int64, policyDigest state.Digest, retentionSeconds int64, confirmedAt time.Time) error {
	if tx == nil || tx.conn == nil || key.Validate() != nil || key.Route.ProjectID != tx.scope.ProjectID ||
		key.Route.WorkspaceID != tx.scope.WorkspaceID || !validUTCTimestamp(confirmedAt) ||
		policyVersion < 1 || policyVersion > maximumActivityInteger || !activityDigestPattern.MatchString(string(policyDigest)) {
		return activityPromotionError("confirm lifecycle", ErrActivityLifecycleConflict)
	}
	evidence, err := newActivityEvidenceLoader().load(ctx, tx.conn, key)
	if err != nil || evidence.receipt == nil || !evidence.promoted ||
		evidence.receipt.receipt.PolicyVersion != policyVersion || evidence.receipt.receipt.PolicyDigest != policyDigest {
		return activityPromotionError("confirm lifecycle", ErrActivityLifecycleConflict)
	}
	policy, err := activityPolicyVersion(ctx, tx.conn, key.Route, policyVersion, policyDigest)
	if err != nil || policy.Policy.TerminalRetentionSeconds != retentionSeconds {
		return activityPromotionError("confirm lifecycle", ErrActivityLifecycleConflict)
	}
	for _, lifecycle := range evidence.lifecycles {
		if lifecycle.kind != "receipt" || lifecycle.reference != key.ActivityID {
			continue
		}
		if lifecycle.state == "confirmed" && lifecycle.policyVersion == policyVersion && lifecycle.policyDigest == policyDigest &&
			lifecycle.retention == retentionSeconds && lifecycle.terminalAt != nil && lifecycle.terminalAt.Equal(confirmedAt) {
			return nil
		}
		return activityPromotionError("confirm lifecycle", ErrActivityLifecycleConflict)
	}
	expiresAt := confirmedAt.Add(time.Duration(retentionSeconds) * time.Second)
	arguments := activityOriginArgs(key)
	arguments = append(arguments, "receipt", key.ActivityID, "confirmed", policyVersion, policyDigest, retentionSeconds,
		sqliteActivityTimestamp(confirmedAt), sqliteActivityTimestamp(expiresAt), sqliteActivityTimestamp(confirmedAt))
	if _, err := tx.conn.ExecContext(ctx, `INSERT INTO activity_lifecycle
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		 lifecycle_kind,reference_id,state,policy_version,policy_digest,terminal_retention_seconds,terminal_at,expires_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
		return activityPromotionError("confirm lifecycle", ErrActivityLifecycleConflict)
	}
	return nil
}

func (tx *WorkspaceMutationTx) readActivityPromotionOperation(ctx context.Context, receipt ActivityPromotionReceiptRecord) (WorkspaceOperation, error) {
	operations, err := tx.queryWorkspaceOperations(ctx, `SELECT generation,operation_id,operation_json,state,stashed_by_stash_id
		FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=? AND operation_id=?`,
		tx.scope.ProjectID, tx.scope.WorkspaceID, receipt.OperationID)
	if err != nil || len(operations) != 1 {
		return WorkspaceOperation{}, activityPromotionError("read operation", ErrActivityReplayConflict)
	}
	decoded, err := state.DecodeOperation(operations[0].OperationJSON)
	if err != nil || decoded.ID != receipt.OperationID {
		return WorkspaceOperation{}, activityPromotionError("validate operation", ErrActivityReplayConflict)
	}
	storedActor, actorErr := state.CanonicalJSON(decoded.Actor)
	wantActor, wantActorErr := state.CanonicalJSON(receipt.Promoter)
	if actorErr != nil || wantActorErr != nil || !bytes.Equal(storedActor, wantActor) {
		return WorkspaceOperation{}, activityPromotionError("validate operation actor", ErrActivityReplayConflict)
	}
	if decoded.PutRecord == nil || decoded.PutRecord.Record.Event == nil || decoded.PutRecord.Record.Event.ID != receipt.EventID {
		return WorkspaceOperation{}, activityPromotionError("validate operation event", ErrActivityReplayConflict)
	}
	if !validPromotionEventExtension(*decoded.PutRecord.Record.Event, receipt.SourceActivityID, receipt.SourceDigest) {
		return WorkspaceOperation{}, activityPromotionError("validate operation extension", ErrActivityReplayConflict)
	}
	return operations[0], nil
}

func validPromotionEventExtension(event state.EventV1, activityID string, digest state.Digest) bool {
	if len(event.Extensions) != 1 {
		return false
	}
	extension, ok := event.Extensions[activityPromotionExtensionName]
	if !ok || extension.SchemaVersion != 1 {
		return false
	}
	var data activityPromotionExtensionDataV1
	decoder := json.NewDecoder(bytes.NewReader(extension.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false
	}
	canonical, err := state.CanonicalJSON(extension.Data)
	canonical = bytes.TrimSuffix(canonical, []byte{'\n'})
	return err == nil && bytes.Equal(canonical, extension.Data) && data.SourceActivityID == activityID && data.SourceActivityDigest == digest
}

func validActivityPromotionReceipt(scope types.WorkspaceScope, receipt ActivityPromotionReceiptRecord) bool {
	return validWorkspaceScope(scope) && receipt.Scope == scope && receipt.SourceActivityID == receipt.SourceKey.ActivityID &&
		receipt.SourceKey.Validate() == nil && receipt.SourceKey.Route.ProjectID == scope.ProjectID &&
		receipt.SourceKey.Route.WorkspaceID == scope.WorkspaceID && activityDigestPattern.MatchString(string(receipt.SourceDigest)) &&
		types.CanonicalUUID(receipt.EventID) && types.CanonicalUUID(receipt.OperationID) &&
		receipt.Promoter.ValidateLocalAction() == nil && validUTCTimestamp(receipt.PromotedAt)
}

func hasConfirmedActivityPromotionLifecycle(evidence activityEvidence, receipt ActivityPromotionReceiptRecord) bool {
	if evidence.receipt == nil {
		return false
	}
	for _, lifecycle := range evidence.lifecycles {
		if lifecycle.kind == "receipt" && lifecycle.reference == receipt.SourceActivityID && lifecycle.state == "confirmed" &&
			lifecycle.policyVersion == evidence.receipt.receipt.PolicyVersion && lifecycle.policyDigest == evidence.receipt.receipt.PolicyDigest &&
			lifecycle.terminalAt != nil && lifecycle.terminalAt.Equal(receipt.PromotedAt) {
			return true
		}
	}
	return false
}

func cloneActivityPolicyRecord(record ActivityPolicyRecord) ActivityPolicyRecord {
	record.PolicyJSON = bytes.Clone(record.PolicyJSON)
	return record
}

func cloneWorkspaceOperation(operation WorkspaceOperation) WorkspaceOperation {
	operation.OperationJSON = bytes.Clone(operation.OperationJSON)
	if operation.StashedByStashID != nil {
		value := *operation.StashedByStashID
		operation.StashedByStashID = &value
	}
	return operation
}

func activityPromotionError(operation string, sentinel error) error {
	return fmt.Errorf("localstore: Activity promotion %s: %w", operation, sentinel)
}
