package localstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func (r *ActivityRepo) Prune(ctx context.Context, route types.ActivityRouteKey, sourceWorkspaceID types.WorkspaceID, limit int) (int, error) {
	if err := validateActivityRoute(route); err != nil || !types.CanonicalUUID(string(sourceWorkspaceID)) || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("localstore: prune Activity: %w", ErrActivityNotFound)
	}
	pruned := 0
	err := r.withImmediate(ctx, "prune", func(conn *sql.Conn) error {
		if err := requireExistingActivityRoute(ctx, conn, route); err != nil {
			return err
		}
		arguments := activityRouteArgs(route)
		arguments = append(arguments, sourceWorkspaceID)
		rows, err := conn.QueryContext(ctx, `SELECT activity_id FROM activity_ledger
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=? AND source_workspace_id=? ORDER BY activity_id`, arguments...)
		if err != nil {
			return fmt.Errorf("localstore: prune Activity query: %w", err)
		}
		keys := make([]types.ActivityOriginKey, 0)
		for rows.Next() {
			var activityID string
			if err := rows.Scan(&activityID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("localstore: prune Activity scan: %w", err)
			}
			keys = append(keys, types.ActivityOriginKey{Route: route, SourceWorkspaceID: sourceWorkspaceID, ActivityID: activityID})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("localstore: prune Activity iterate: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("localstore: prune Activity close: %w", err)
		}
		loader := newActivityEvidenceLoader()
		evidence := make([]activityEvidence, 0, len(keys))
		for _, key := range keys {
			record, err := loader.load(ctx, conn, key)
			if err != nil {
				return err
			}
			evidence = append(evidence, record)
		}
		now, err := databaseActivityNow(ctx, conn)
		if err != nil {
			return err
		}
		ordinary := make([]activityEvidence, 0, len(evidence))
		for _, record := range evidence {
			if record.record.Activity.Class == "ordinary" && len(record.lifecycles) == 0 && !record.promoted {
				ordinary = append(ordinary, record)
			}
		}
		sort.Slice(ordinary, func(i, j int) bool {
			left, right := ordinary[i].record.Activity, ordinary[j].record.Activity
			if left.CreatedAt.Equal(right.CreatedAt) {
				return left.ID > right.ID
			}
			return left.CreatedAt.After(right.CreatedAt)
		})
		ranks := make(map[string]int, len(ordinary))
		for index, record := range ordinary {
			ranks[record.record.Key.ActivityID] = index + 1
		}
		eligible := make([]activityEvidence, 0)
		for _, record := range evidence {
			if activityEvidenceEligibleForPrune(record, ranks[record.record.Key.ActivityID], now) {
				eligible = append(eligible, record)
			}
		}
		sort.Slice(eligible, func(i, j int) bool {
			left, right := eligible[i].record.Activity, eligible[j].record.Activity
			if left.CreatedAt.Equal(right.CreatedAt) {
				return left.ID < right.ID
			}
			return left.CreatedAt.Before(right.CreatedAt)
		})
		if len(eligible) > limit {
			eligible = eligible[:limit]
		}
		for _, candidate := range eligible {
			if err := deleteActivityEvidence(ctx, conn, candidate.record.Key); err != nil {
				return err
			}
			pruned++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return pruned, nil
}

func activityEvidenceEligibleForPrune(evidence activityEvidence, ordinaryRank int, now time.Time) bool {
	if len(evidence.lifecycles) == 0 {
		return evidence.record.Activity.Class == "ordinary" && !evidence.promoted && ordinaryRank > 0 &&
			(!now.Before(evidence.record.Activity.CreatedAt.Add(2_592_000*time.Second)) || ordinaryRank > 10_000)
	}
	var maximumExpiry time.Time
	for _, lifecycle := range evidence.lifecycles {
		if lifecycle.expiresAt == nil {
			return false
		}
		if maximumExpiry.IsZero() || lifecycle.expiresAt.After(maximumExpiry) {
			maximumExpiry = *lifecycle.expiresAt
		}
	}
	return !now.Before(maximumExpiry)
}

func deleteActivityEvidence(ctx context.Context, db activityDB, key types.ActivityOriginKey) error {
	arguments := activityOriginArgs(key)
	for _, statement := range []string{
		`DELETE FROM activity_promotion_receipts WHERE source_project_id=? AND source_workspace_binding_id=?
		 AND source_fabric_instance_id=? AND source_remote_project_id=? AND source_stream_id=? AND source_canonical_ref=?
		 AND source_origin_workspace_id=? AND source_activity_id=?`,
		`DELETE FROM activity_outbound_queue WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		 AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`,
		`DELETE FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		 AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`,
		`DELETE FROM activity_ingress_receipts WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		 AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`,
		`DELETE FROM activity_ledger WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
		 AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=?`,
	} {
		if _, err := db.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("localstore: prune Activity delete: %w", err)
		}
	}
	return nil
}
