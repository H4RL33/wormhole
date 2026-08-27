package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func (r *ActivityRepo) TransitionLifecycle(ctx context.Context, key types.ActivityOriginKey, change ActivityLifecycleChange) error {
	if err := key.Validate(); err != nil || !types.CanonicalUUID(change.ReferenceID) ||
		!validActivityLifecycleState(change.Kind, change.ExpectedState) || !validActivityLifecycleState(change.Kind, change.NextState) {
		return fmt.Errorf("localstore: transition Activity lifecycle: %w", ErrActivityLifecycleConflict)
	}
	return r.withImmediate(ctx, "transition lifecycle", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, key.Route); err != nil {
			return err
		}
		var current string
		var retention int64
		var terminalAt, expiresAt sql.NullTime
		arguments := activityOriginArgs(key)
		arguments = append(arguments, change.Kind, change.ReferenceID)
		err := conn.QueryRowContext(ctx, `SELECT state,terminal_retention_seconds,terminal_at,expires_at
			FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=?
			AND stream_id=? AND canonical_ref=? AND source_workspace_id=? AND activity_id=? AND lifecycle_kind=? AND reference_id=?`, arguments...).Scan(
			&current, &retention, &terminalAt, &expiresAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", ErrActivityNotFound)
		}
		if err != nil {
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", err)
		}
		if current == change.NextState {
			if change.ExpectedState == current || allowedActivityLifecycleTransition(change.Kind, change.ExpectedState, current) {
				return nil
			}
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", ErrActivityLifecycleConflict)
		}
		if current != change.ExpectedState || !allowedActivityLifecycleTransition(change.Kind, current, change.NextState) {
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", ErrActivityLifecycleConflict)
		}
		now, err := databaseActivityNow(ctx, conn)
		if err != nil {
			return err
		}
		var terminal, expires any
		if terminalActivityLifecycleState(change.Kind, change.NextState) {
			terminal = sqliteActivityTimestamp(now)
			expires = sqliteActivityTimestamp(now.Add(time.Duration(retention) * time.Second))
		}
		arguments = []any{change.NextState, terminal, expires, now}
		arguments = append(arguments, activityOriginArgs(key)...)
		arguments = append(arguments, change.Kind, change.ReferenceID, current)
		result, err := conn.ExecContext(ctx, `UPDATE activity_lifecycle SET state=?,terminal_at=?,expires_at=?,updated_at=?
			WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=?
			AND source_workspace_id=? AND activity_id=? AND lifecycle_kind=? AND reference_id=? AND state=?`, arguments...)
		if err != nil {
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return fmt.Errorf("localstore: transition Activity lifecycle: %w", ErrActivityLifecycleConflict)
		}
		return nil
	})
}

func validActivityLifecycleState(kind, state string) bool {
	switch kind {
	case "delivery":
		return state == "pending" || state == "delivered" || state == "cancelled"
	case "conflict":
		return state == "open" || state == "resolved" || state == "cancelled"
	case "recovery":
		return state == "pending" || state == "blocked" || state == "recovered" || state == "cancelled"
	case "receipt":
		return state == "pending" || state == "confirmed" || state == "rejected" || state == "cancelled"
	default:
		return false
	}
}

func terminalActivityLifecycleState(kind, state string) bool {
	switch kind {
	case "delivery":
		return state == "delivered" || state == "cancelled"
	case "conflict":
		return state == "resolved" || state == "cancelled"
	case "recovery":
		return state == "recovered" || state == "cancelled"
	case "receipt":
		return state == "confirmed" || state == "rejected" || state == "cancelled"
	default:
		return false
	}
}

func allowedActivityLifecycleTransition(kind, current, next string) bool {
	switch kind {
	case "delivery":
		return current == "pending" && (next == "delivered" || next == "cancelled")
	case "conflict":
		return current == "open" && (next == "resolved" || next == "cancelled")
	case "recovery":
		return (current == "pending" && (next == "blocked" || next == "recovered" || next == "cancelled")) ||
			(current == "blocked" && (next == "pending" || next == "recovered" || next == "cancelled"))
	case "receipt":
		return current == "pending" && (next == "confirmed" || next == "rejected" || next == "cancelled")
	default:
		return false
	}
}
