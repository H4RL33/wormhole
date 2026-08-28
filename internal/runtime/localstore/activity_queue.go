package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func (r *ActivityRepo) QueueOutbound(ctx context.Context, route types.ActivityRouteKey, activity projectstate.ActivityV1) (ActivityRecord, error) {
	if err := validateActivityRoute(route); err != nil {
		return ActivityRecord{}, err
	}
	canonical, digest, actorJSON, err := canonicalActivityEvidence(activity)
	if err != nil {
		return ActivityRecord{}, err
	}
	if activity.Class == projectstate.ActivityPresenceV1 || !validRemoteActivity(activity) {
		return ActivityRecord{}, projectstate.ErrInvalidActivity
	}
	key := types.ActivityOriginKey{Route: route, SourceWorkspaceID: route.WorkspaceID, ActivityID: activity.ID}
	if err := key.Validate(); err != nil {
		return ActivityRecord{}, projectstate.ErrInvalidActivity
	}
	var result ActivityRecord
	err = r.withImmediate(ctx, "queue", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, route); err != nil {
			return err
		}
		policy, err := currentActivityPolicy(ctx, conn, route)
		if err != nil {
			return err
		}
		var storedRaw []byte
		var storedDigest string
		err = conn.QueryRowContext(ctx, `SELECT canonical_activity_json,activity_digest FROM activity_ledger
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(key)...).Scan(&storedRaw, &storedDigest)
		if err == nil {
			if storedDigest != string(digest) || !bytes.Equal(storedRaw, canonical) {
				return fmt.Errorf("localstore: queue Activity: %w", ErrActivityReplayConflict)
			}
			var expectedVersion int64
			var expectedDigest string
			queryErr := conn.QueryRowContext(ctx, `SELECT expected_policy_version,expected_policy_digest
				FROM activity_outbound_queue WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
				AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(key)...).Scan(&expectedVersion, &expectedDigest)
			if errors.Is(queryErr, sql.ErrNoRows) {
				return fmt.Errorf("localstore: queue Activity: %w", ErrActivityReplayConflict)
			}
			if queryErr != nil {
				return fmt.Errorf("localstore: queue Activity replay: %w", queryErr)
			}
			result, err = readActivityRecord(ctx, conn, key, expectedVersion, projectstate.Digest(expectedDigest))
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("localstore: queue Activity lookup: %w", err)
		}
		now, err := databaseActivityNow(ctx, conn)
		if err != nil {
			return err
		}
		if err := insertActivityLedger(ctx, conn, key, activity, canonical, digest, actorJSON, now, nil); err != nil {
			return err
		}
		if activity.Lifecycle != nil {
			if err := insertInitialActivityLifecycle(ctx, conn, key, string(activity.Lifecycle.Kind), activity.Lifecycle.ReferenceID, policy, now); err != nil {
				return err
			}
		}
		if err := insertInitialActivityLifecycle(ctx, conn, key, "delivery", activity.ID, policy, now); err != nil {
			return err
		}
		arguments := activityOriginArgs(key)
		arguments = append(arguments, "pending", policy.Policy.PolicyVersion, string(policy.PolicyDigest),
			policy.Policy.PolicyVersion, string(policy.PolicyDigest), 0, sqliteActivityTimestamp(now),
			sqliteActivityTimestamp(now), sqliteActivityTimestamp(now))
		if _, err := conn.ExecContext(ctx, `INSERT INTO activity_outbound_queue
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
			 state,expected_policy_version,expected_policy_digest,created_policy_version,created_policy_digest,
			 attempt_count,next_attempt_at,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
			return fmt.Errorf("localstore: queue Activity delivery: %w", err)
		}
		result, err = readActivityRecord(ctx, conn, key, policy.Policy.PolicyVersion, policy.PolicyDigest)
		return err
	})
	if err != nil {
		return ActivityRecord{}, err
	}
	return cloneActivityRecord(result), nil
}

func insertInitialActivityLifecycle(ctx context.Context, db activityDB, key types.ActivityOriginKey, kind, reference string, policy ActivityPolicyRecord, now time.Time) error {
	state := "pending"
	if kind == "conflict" {
		state = "open"
	}
	arguments := activityOriginArgs(key)
	arguments = append(arguments, kind, reference, state, policy.Policy.PolicyVersion, string(policy.PolicyDigest),
		policy.Policy.TerminalRetentionSeconds, sqliteActivityTimestamp(now))
	result, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO activity_lifecycle
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		 lifecycle_kind,reference_id,state,policy_version,policy_digest,terminal_retention_seconds,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...)
	if err != nil {
		return fmt.Errorf("localstore: create Activity lifecycle: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect Activity lifecycle: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var storedState, storedDigest string
	var storedVersion, storedRetention int64
	arguments = activityOriginArgs(key)
	arguments = append(arguments, kind, reference)
	if err := db.QueryRowContext(ctx, `SELECT state,policy_version,policy_digest,terminal_retention_seconds
		FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=? AND lifecycle_kind=? AND reference_id=?`, arguments...).Scan(
		&storedState, &storedVersion, &storedDigest, &storedRetention,
	); err != nil || storedState != state || storedVersion != policy.Policy.PolicyVersion ||
		storedDigest != string(policy.PolicyDigest) || storedRetention != policy.Policy.TerminalRetentionSeconds {
		return fmt.Errorf("localstore: create Activity lifecycle: %w", ErrActivityLifecycleConflict)
	}
	return nil
}

func (r *ActivityRepo) PendingOutbound(ctx context.Context, route types.ActivityRouteKey, limit int) ([]ActivityRecord, error) {
	if r == nil || r.db == nil || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("localstore: pending Activity: %w", ErrActivityNotFound)
	}
	if err := validateActivityRoute(route); err != nil {
		return nil, err
	}
	if err := requireActiveActivityRoute(ctx, r.db, route); err != nil {
		return nil, err
	}
	arguments := activityRouteArgs(route)
	arguments = append(arguments, limit)
	rows, err := r.db.QueryContext(ctx, `SELECT source_workspace_id,activity_id
		FROM activity_outbound_queue WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		AND stream_id=? AND canonical_ref=? AND state='pending'
		ORDER BY next_attempt_at,created_at,activity_id LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("localstore: pending Activity query: %w", err)
	}
	type pendingKey struct {
		source types.WorkspaceID
		id     string
	}
	keys := make([]pendingKey, 0)
	for rows.Next() {
		var value pendingKey
		if err := rows.Scan(&value.source, &value.id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("localstore: pending Activity scan: %w", err)
		}
		keys = append(keys, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("localstore: pending Activity iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("localstore: pending Activity close: %w", err)
	}
	records := make([]ActivityRecord, 0, len(keys))
	loader := newActivityEvidenceLoader()
	for _, value := range keys {
		key := types.ActivityOriginKey{Route: route, SourceWorkspaceID: value.source, ActivityID: value.id}
		evidence, err := loader.load(ctx, r.db, key)
		if err != nil {
			return nil, err
		}
		record, sendable, err := evidence.pendingRecord()
		if err != nil {
			return nil, err
		}
		if !sendable {
			continue
		}
		records = append(records, cloneActivityRecord(record))
	}
	return records, nil
}

func (r *ActivityRepo) AcknowledgeOutbound(ctx context.Context, key types.ActivityOriginKey, receipt projectstate.ActivityReceiptV1) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("localstore: acknowledge Activity: %w", ErrActivityNotFound)
	}
	canonicalReceipt, err := projectstate.CanonicalActivityReceipt(receipt)
	if err != nil {
		return err
	}
	if receipt.ActivityID != key.ActivityID {
		return fmt.Errorf("localstore: acknowledge Activity: %w", ErrActivityReplayConflict)
	}
	return r.withImmediate(ctx, "acknowledge", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, key.Route); err != nil {
			return err
		}
		_, err := activityPolicyVersion(ctx, conn, key.Route, receipt.PolicyVersion, receipt.PolicyDigest)
		if err != nil {
			return err
		}
		record, err := readActivityRecord(ctx, conn, key, receipt.PolicyVersion, receipt.PolicyDigest)
		if err != nil {
			return err
		}
		if receipt.ActivityDigest != record.ActivityDigest {
			return fmt.Errorf("localstore: acknowledge Activity: %w", ErrActivityReplayConflict)
		}
		var deliveryState string
		var deliveryRetention int64
		deliveryArguments := activityOriginArgs(key)
		deliveryArguments = append(deliveryArguments, key.ActivityID)
		if err := conn.QueryRowContext(ctx, `SELECT state,terminal_retention_seconds FROM activity_lifecycle
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?
			AND lifecycle_kind='delivery' AND reference_id=?`, deliveryArguments...).Scan(&deliveryState, &deliveryRetention); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("localstore: acknowledge Activity lifecycle: %w", ErrActivityNotFound)
		} else if err != nil {
			return fmt.Errorf("localstore: acknowledge Activity lifecycle: %w", err)
		}
		var storedSequence, storedVersion int64
		var storedDigest, storedPolicyDigest string
		var storedAcceptedAt time.Time
		receiptErr := conn.QueryRowContext(ctx, `SELECT activity_digest,sequence,policy_version,policy_digest,accepted_at
			FROM activity_ingress_receipts WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`, activityOriginArgs(key)...).Scan(
			&storedDigest, &storedSequence, &storedVersion, &storedPolicyDigest, &storedAcceptedAt,
		)
		if receiptErr == nil {
			stored := projectstate.ActivityReceiptV1{
				SchemaVersion: 1, ActivityID: key.ActivityID, ActivityDigest: projectstate.Digest(storedDigest),
				Sequence: storedSequence, PolicyVersion: storedVersion, PolicyDigest: projectstate.Digest(storedPolicyDigest),
				AcceptedAt: storedAcceptedAt.UTC(),
			}
			storedCanonical, canonicalErr := projectstate.CanonicalActivityReceipt(stored)
			if canonicalErr != nil || !bytes.Equal(storedCanonical, canonicalReceipt) {
				return fmt.Errorf("localstore: acknowledge Activity: %w", ErrActivityReplayConflict)
			}
			var queueState, lifecycleState string
			if err := conn.QueryRowContext(ctx, `SELECT q.state,l.state FROM activity_outbound_queue q
				JOIN activity_lifecycle l USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
				WHERE q.project_id=? AND q.workspace_id=? AND q.fabric_instance_id=? AND q.remote_project_id=?
				AND q.stream_id=? AND q.canonical_ref=? AND q.source_workspace_id=? AND q.activity_id=?
				AND l.lifecycle_kind='delivery' AND l.reference_id=q.activity_id`, activityOriginArgs(key)...).Scan(&queueState, &lifecycleState); err != nil {
				return fmt.Errorf("localstore: acknowledge Activity replay: %w", err)
			}
			if queueState == "delivered" && lifecycleState == "delivered" {
				return nil
			}
			return fmt.Errorf("localstore: acknowledge Activity: %w", ErrActivityReplayConflict)
		}
		if !errors.Is(receiptErr, sql.ErrNoRows) {
			return fmt.Errorf("localstore: acknowledge Activity receipt: %w", receiptErr)
		}
		arguments := activityOriginArgs(key)
		arguments = append(arguments, string(receipt.ActivityDigest), receipt.Sequence, receipt.PolicyVersion,
			string(receipt.PolicyDigest), sqliteActivityTimestamp(receipt.AcceptedAt))
		if _, err := conn.ExecContext(ctx, `INSERT INTO activity_ingress_receipts
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
			 activity_digest,sequence,policy_version,policy_digest,accepted_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
			return fmt.Errorf("localstore: acknowledge Activity receipt: %w", err)
		}
		now, err := databaseActivityNow(ctx, conn)
		if err != nil {
			return err
		}
		arguments = []any{sqliteActivityTimestamp(now), sqliteActivityTimestamp(now)}
		arguments = append(arguments, activityOriginArgs(key)...)
		result, err := conn.ExecContext(ctx, `UPDATE activity_outbound_queue SET state='delivered',delivered_at=?,updated_at=?
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=?
			AND source_workspace_id=? AND activity_id=? AND state='pending'`, arguments...)
		if err != nil {
			return fmt.Errorf("localstore: acknowledge Activity queue: %w", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return fmt.Errorf("localstore: acknowledge Activity queue: %w", ErrActivityNotFound)
		}
		if deliveryState != "pending" {
			return fmt.Errorf("localstore: acknowledge Activity lifecycle: %w", ErrActivityLifecycleConflict)
		}
		expiresAt := now.Add(time.Duration(deliveryRetention) * time.Second)
		arguments = []any{sqliteActivityTimestamp(now), sqliteActivityTimestamp(expiresAt), sqliteActivityTimestamp(now)}
		arguments = append(arguments, activityOriginArgs(key)...)
		result, err = conn.ExecContext(ctx, `UPDATE activity_lifecycle
			SET state='delivered',terminal_at=?,expires_at=?,updated_at=?
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=?
			AND source_workspace_id=? AND activity_id=? AND lifecycle_kind='delivery' AND reference_id=? AND state='pending'`,
			append(arguments, key.ActivityID)...)
		if err != nil {
			return fmt.Errorf("localstore: acknowledge Activity lifecycle: %w", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return fmt.Errorf("localstore: acknowledge Activity lifecycle: %w", ErrActivityLifecycleConflict)
		}
		return nil
	})
}
