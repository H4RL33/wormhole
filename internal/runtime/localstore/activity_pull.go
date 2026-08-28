package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type validatedPullDelivery struct {
	source         types.WorkspaceID
	activity       projectstate.ActivityV1
	activityJSON   []byte
	activityDigest projectstate.Digest
	receipt        projectstate.ActivityReceiptV1
	receiptJSON    []byte
}

func (r *ActivityRepo) AcceptPullBatch(ctx context.Context, route types.ActivityRouteKey, batch ActivityPullBatch) error {
	if err := validateActivityRoute(route); err != nil {
		return err
	}
	policy, err := projectstate.DecodeActivityPolicy(batch.PolicyJSON)
	if err != nil {
		return err
	}
	policyDigest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		return err
	}
	if batch.ExpectedAfter < 0 || batch.ExpectedAfter > maximumActivityInteger ||
		batch.NextSequence < batch.ExpectedAfter || batch.NextSequence > maximumActivityInteger || len(batch.Deliveries) > 500 {
		return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
	}
	validated := make([]validatedPullDelivery, 0, len(batch.Deliveries))
	lastSequence := batch.ExpectedAfter
	for _, delivery := range batch.Deliveries {
		if !types.CanonicalUUID(string(delivery.SourceWorkspaceID)) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
		}
		activity, err := projectstate.DecodeActivity(delivery.ActivityJSON)
		if err != nil || activity.Class == projectstate.ActivityPresenceV1 || !validRemoteActivity(activity) {
			return projectstate.ErrInvalidActivity
		}
		digest, err := projectstate.DigestActivity(activity)
		if err != nil || digest != delivery.ActivityDigest {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
		}
		receipt, err := projectstate.DecodeActivityReceipt(delivery.ReceiptJSON)
		if err != nil || receipt.ActivityID != activity.ID || receipt.ActivityDigest != digest ||
			receipt.Sequence <= lastSequence || receipt.Sequence > batch.NextSequence {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
		}
		lastSequence = receipt.Sequence
		validated = append(validated, validatedPullDelivery{
			source: delivery.SourceWorkspaceID, activity: activity,
			activityJSON: append([]byte(nil), delivery.ActivityJSON...), activityDigest: digest,
			receipt: receipt, receiptJSON: append([]byte(nil), delivery.ReceiptJSON...),
		})
	}
	if batch.HasMore {
		if len(validated) == 0 || batch.NextSequence != lastSequence {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
		}
	} else if batch.NextSequence < lastSequence {
		return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
	}

	return r.withImmediate(ctx, "accept pull", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, route); err != nil {
			return err
		}
		current, err := currentActivityPolicy(ctx, conn, route)
		if err != nil {
			return err
		}
		if current.PolicyDigest != policyDigest || !bytes.Equal(current.PolicyJSON, batch.PolicyJSON) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityPolicyChanged)
		}
		var cursor int64
		if err := conn.QueryRowContext(ctx, `SELECT after_sequence FROM activity_cursors
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=?`, activityRouteArgs(route)...).Scan(&cursor); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityNotFound)
		} else if err != nil {
			return fmt.Errorf("localstore: accept Activity pull cursor: %w", err)
		}
		duplicate := cursor == batch.NextSequence && cursor != batch.ExpectedAfter
		if cursor != batch.ExpectedAfter && !duplicate {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
		}
		if cursor == batch.ExpectedAfter && batch.NextSequence == batch.ExpectedAfter && len(validated) == 0 {
			return nil
		}
		if duplicate {
			if err := validateDuplicatePullWindow(ctx, conn, route, batch.ExpectedAfter, batch.NextSequence, validated); err != nil {
				return err
			}
		} else if err := requireUnconflictedActivityWorkspace(ctx, conn, route); err != nil {
			return err
		}
		for _, delivery := range validated {
			if err := acceptPulledActivity(ctx, conn, route, delivery, !duplicate); err != nil {
				return err
			}
		}
		if duplicate {
			return nil
		}
		now, err := databaseActivityNow(ctx, conn)
		if err != nil {
			return err
		}
		arguments := []any{batch.NextSequence, sqliteActivityTimestamp(now)}
		arguments = append(arguments, activityRouteArgs(route)...)
		arguments = append(arguments, batch.ExpectedAfter)
		result, err := conn.ExecContext(ctx, `UPDATE activity_cursors SET after_sequence=?,updated_at=?
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
			AND canonical_ref=? AND after_sequence=?`, arguments...)
		if err != nil {
			return fmt.Errorf("localstore: accept Activity pull cursor: %w", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
		}
		return nil
	})
}

func validateDuplicatePullWindow(ctx context.Context, db activityDB, route types.ActivityRouteKey, after, next int64, deliveries []validatedPullDelivery) error {
	arguments := activityRouteArgs(route)
	arguments = append(arguments, after, next)
	rows, err := db.QueryContext(ctx, `SELECT source_workspace_id,activity_id,sequence FROM activity_ingress_receipts
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND sequence>? AND sequence<=? ORDER BY sequence,source_workspace_id,activity_id`, arguments...)
	if err != nil {
		return fmt.Errorf("localstore: accept Activity pull replay: %w", err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(deliveries) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
		}
		var source types.WorkspaceID
		var activityID string
		var sequence int64
		if err := rows.Scan(&source, &activityID, &sequence); err != nil {
			return fmt.Errorf("localstore: accept Activity pull replay: %w", err)
		}
		want := deliveries[index]
		if source != want.source || activityID != want.activity.ID || sequence != want.receipt.Sequence {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("localstore: accept Activity pull replay: %w", err)
	}
	if index != len(deliveries) {
		return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
	}
	return nil
}

func acceptPulledActivity(ctx context.Context, db activityDB, route types.ActivityRouteKey, delivery validatedPullDelivery, allowInsert bool) error {
	policy, err := activityPolicyVersion(ctx, db, route, delivery.receipt.PolicyVersion, delivery.receipt.PolicyDigest)
	if err != nil {
		return err
	}
	key := types.ActivityOriginKey{Route: route, SourceWorkspaceID: delivery.source, ActivityID: delivery.activity.ID}
	var storedRaw []byte
	var storedDigest string
	err = db.QueryRowContext(ctx, `SELECT canonical_activity_json,activity_digest FROM activity_ledger
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(key)...).Scan(&storedRaw, &storedDigest)
	if err == nil {
		if storedDigest != string(delivery.activityDigest) || !bytes.Equal(storedRaw, delivery.activityJSON) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
		}
		evidence, err := newActivityEvidenceLoader().load(ctx, db, key)
		if err != nil || evidence.receipt == nil || !bytes.Equal(evidence.receipt.canonical, delivery.receiptJSON) {
			return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("localstore: accept Activity pull lookup: %w", err)
	}
	if !allowInsert {
		return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityCursorConflict)
	}
	actorJSON, err := projectstate.CanonicalJSON(delivery.activity.Actor)
	if err != nil {
		return projectstate.ErrInvalidActivity
	}
	sequence := delivery.receipt.Sequence
	if err := insertActivityLedger(ctx, db, key, delivery.activity, delivery.activityJSON, delivery.activityDigest,
		actorJSON, delivery.receipt.AcceptedAt, &sequence); err != nil {
		return fmt.Errorf("localstore: accept Activity pull: %w", ErrActivityReplayConflict)
	}
	arguments := activityOriginArgs(key)
	arguments = append(arguments, string(delivery.activityDigest), delivery.receipt.Sequence, delivery.receipt.PolicyVersion,
		string(delivery.receipt.PolicyDigest), sqliteActivityTimestamp(delivery.receipt.AcceptedAt))
	if _, err := db.ExecContext(ctx, `INSERT INTO activity_ingress_receipts
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		 activity_digest,sequence,policy_version,policy_digest,accepted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
		return fmt.Errorf("localstore: accept Activity pull receipt: %w", err)
	}
	if delivery.activity.Lifecycle != nil {
		if err := insertInitialActivityLifecycle(ctx, db, key, string(delivery.activity.Lifecycle.Kind),
			delivery.activity.Lifecycle.ReferenceID, policy, delivery.receipt.AcceptedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *ActivityRepo) Cursor(ctx context.Context, route types.ActivityRouteKey) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("localstore: Activity cursor: %w", ErrActivityNotFound)
	}
	if err := validateActivityRoute(route); err != nil {
		return 0, err
	}
	if err := requireActiveActivityRoute(ctx, r.db, route); err != nil {
		return 0, err
	}
	var cursor int64
	if err := r.db.QueryRowContext(ctx, `SELECT after_sequence FROM activity_cursors
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=?`, activityRouteArgs(route)...).Scan(&cursor); errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("localstore: Activity cursor: %w", ErrActivityNotFound)
	} else if err != nil {
		return 0, fmt.Errorf("localstore: Activity cursor: %w", err)
	}
	return cursor, nil
}

func (r *ActivityRepo) Retained(ctx context.Context, route types.ActivityRouteKey, limit int) ([]ActivityRecord, error) {
	if r == nil || r.db == nil || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("localstore: retained Activity: %w", ErrActivityNotFound)
	}
	if err := validateActivityRoute(route); err != nil {
		return nil, err
	}
	if err := requireActiveActivityRoute(ctx, r.db, route); err != nil {
		return nil, err
	}
	arguments := activityRouteArgs(route)
	arguments = append(arguments, limit)
	rows, err := r.db.QueryContext(ctx, `SELECT a.source_workspace_id,a.activity_id
		FROM activity_ledger a
		LEFT JOIN activity_ingress_receipts i USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
		LEFT JOIN activity_outbound_queue q USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
		WHERE a.project_id=? AND a.workspace_id=? AND a.fabric_instance_id=? AND a.remote_project_id=?
		AND a.stream_id=? AND a.canonical_ref=?
		ORDER BY CASE WHEN COALESCE(i.sequence,a.sequence) IS NULL THEN 1 ELSE 0 END,
		COALESCE(i.sequence,a.sequence),COALESCE(i.accepted_at,a.accepted_at),a.activity_id LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("localstore: retained Activity query: %w", err)
	}
	type retainedKey struct {
		source types.WorkspaceID
		id     string
	}
	keys := make([]retainedKey, 0)
	for rows.Next() {
		var value retainedKey
		if err := rows.Scan(&value.source, &value.id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("localstore: retained Activity scan: %w", err)
		}
		keys = append(keys, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("localstore: retained Activity iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("localstore: retained Activity close: %w", err)
	}
	records := make([]ActivityRecord, 0, len(keys))
	loader := newActivityEvidenceLoader()
	for _, value := range keys {
		key := types.ActivityOriginKey{Route: route, SourceWorkspaceID: value.source, ActivityID: value.id}
		evidence, err := loader.load(ctx, r.db, key)
		if err != nil {
			return nil, err
		}
		record, err := evidence.retainedRecord()
		if err != nil {
			return nil, err
		}
		records = append(records, cloneActivityRecord(record))
	}
	return records, nil
}
