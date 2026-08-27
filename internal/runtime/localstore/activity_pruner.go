package localstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func (r *ActivityRepo) Prune(ctx context.Context, route types.ActivityRouteKey, sourceWorkspaceID types.WorkspaceID, limit int) (int, error) {
	if err := validateActivityRoute(route); err != nil || !types.CanonicalUUID(string(sourceWorkspaceID)) || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("localstore: prune Activity: %w", ErrActivityNotFound)
	}
	pruned := 0
	err := r.withImmediate(ctx, "prune", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, route); err != nil {
			return err
		}
		arguments := activityRouteArgs(route)
		arguments = append(arguments, sourceWorkspaceID, limit)
		rows, err := conn.QueryContext(ctx, `WITH ordinary_ranked AS (
			SELECT a.source_workspace_id,a.activity_id,a.created_at,
			       COALESCE(i.policy_version,q.created_policy_version) AS policy_version,
			       COALESCE(i.policy_digest,q.created_policy_digest) AS policy_digest,
			       row_number() OVER (ORDER BY a.created_at DESC,a.activity_id DESC) AS newest_rank
			FROM activity_ledger a
			LEFT JOIN activity_ingress_receipts i USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
			LEFT JOIN activity_outbound_queue q USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
			WHERE a.project_id=? AND a.workspace_id=? AND a.fabric_instance_id=? AND a.remote_project_id=?
			AND a.stream_id=? AND a.canonical_ref=? AND a.source_workspace_id=? AND a.activity_class='ordinary'
			AND NOT EXISTS (SELECT 1 FROM activity_lifecycle l WHERE l.project_id=a.project_id AND l.workspace_id=a.workspace_id
				AND l.fabric_instance_id=a.fabric_instance_id AND l.remote_project_id=a.remote_project_id AND l.stream_id=a.stream_id
				AND l.canonical_ref=a.canonical_ref AND l.source_workspace_id=a.source_workspace_id AND l.activity_id=a.activity_id)
			AND NOT EXISTS (SELECT 1 FROM activity_promotion_receipts p WHERE p.source_project_id=a.project_id
				AND p.source_workspace_binding_id=a.workspace_id AND p.source_fabric_instance_id=a.fabric_instance_id
				AND p.source_remote_project_id=a.remote_project_id AND p.source_stream_id=a.stream_id
				AND p.source_canonical_ref=a.canonical_ref AND p.source_origin_workspace_id=a.source_workspace_id
				AND p.source_activity_id=a.activity_id)
		), eligible AS (
			SELECT source_workspace_id,activity_id,created_at,policy_version,policy_digest FROM ordinary_ranked
			WHERE julianday(created_at)<=julianday('now','-2592000 seconds') OR newest_rank>10000
			UNION ALL
			SELECT a.source_workspace_id,a.activity_id,a.created_at,
			       COALESCE(i.policy_version,q.created_policy_version),COALESCE(i.policy_digest,q.created_policy_digest)
			FROM activity_ledger a
			LEFT JOIN activity_ingress_receipts i USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
			LEFT JOIN activity_outbound_queue q USING(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id)
			WHERE a.project_id=?1 AND a.workspace_id=?2 AND a.fabric_instance_id=?3 AND a.remote_project_id=?4
			AND a.stream_id=?5 AND a.canonical_ref=?6 AND a.source_workspace_id=?7
			AND EXISTS (SELECT 1 FROM activity_lifecycle l WHERE l.project_id=a.project_id AND l.workspace_id=a.workspace_id
				AND l.fabric_instance_id=a.fabric_instance_id AND l.remote_project_id=a.remote_project_id AND l.stream_id=a.stream_id
				AND l.canonical_ref=a.canonical_ref AND l.source_workspace_id=a.source_workspace_id AND l.activity_id=a.activity_id)
			AND NOT EXISTS (SELECT 1 FROM activity_lifecycle l WHERE l.project_id=a.project_id AND l.workspace_id=a.workspace_id
				AND l.fabric_instance_id=a.fabric_instance_id AND l.remote_project_id=a.remote_project_id AND l.stream_id=a.stream_id
				AND l.canonical_ref=a.canonical_ref AND l.source_workspace_id=a.source_workspace_id AND l.activity_id=a.activity_id
				AND l.state IN ('pending','open','blocked'))
			AND (SELECT max(julianday(l.expires_at)) FROM activity_lifecycle l WHERE l.project_id=a.project_id AND l.workspace_id=a.workspace_id
				AND l.fabric_instance_id=a.fabric_instance_id AND l.remote_project_id=a.remote_project_id AND l.stream_id=a.stream_id
				AND l.canonical_ref=a.canonical_ref AND l.source_workspace_id=a.source_workspace_id AND l.activity_id=a.activity_id)<=julianday('now')
		)
		SELECT source_workspace_id,activity_id,policy_version,policy_digest FROM eligible
		ORDER BY created_at,activity_id LIMIT ?8`, arguments...)
		if err != nil {
			return fmt.Errorf("localstore: prune Activity query: %w", err)
		}
		type candidate struct {
			source  types.WorkspaceID
			id      string
			version int64
			digest  string
		}
		candidates := make([]candidate, 0)
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.source, &value.id, &value.version, &value.digest); err != nil {
				_ = rows.Close()
				return fmt.Errorf("localstore: prune Activity scan: %w", err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("localstore: prune Activity iterate: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("localstore: prune Activity close: %w", err)
		}
		for _, candidate := range candidates {
			key := types.ActivityOriginKey{Route: route, SourceWorkspaceID: candidate.source, ActivityID: candidate.id}
			if _, err := readActivityRecord(ctx, conn, key, candidate.version, projectstate.Digest(candidate.digest)); err != nil {
				return err
			}
			if err := deleteActivityEvidence(ctx, conn, key); err != nil {
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
