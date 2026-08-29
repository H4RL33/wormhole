package git

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
)

func (s *ActivityStore) TransitionLifecycle(ctx context.Context, key FabricActivityOriginKey, transition ActivityLifecycleTransition) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityNotFound)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("git: transition activity lifecycle: begin: %w", err)
	}
	defer tx.Rollback()
	if err := s.TransitionLifecycleInTx(ctx, tx, key, transition); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("git: transition activity lifecycle: commit: %w", err)
	}
	return nil
}

func (s *ActivityStore) TransitionLifecycleInTx(ctx context.Context, tx *sql.Tx, key FabricActivityOriginKey, transition ActivityLifecycleTransition) error {
	if s == nil || s.db == nil || tx == nil {
		return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityNotFound)
	}
	if err := key.validate(); err != nil {
		return err
	}
	if !types.CanonicalUUID(transition.ReferenceID) || !validActivityLifecycleTransition(transition.Kind, transition.ExpectedState, transition.NextState) {
		return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityLifecycleConflict)
	}
	if err := setActivityProject(ctx, tx, key.Stream.ProjectID); err != nil {
		return err
	}
	savepoint := newActivitySavepoint("wormhole_activity_lifecycle")
	if _, err := tx.ExecContext(ctx, `SAVEPOINT `+savepoint); err != nil {
		return fmt.Errorf("git: transition activity lifecycle: savepoint: %w", err)
	}
	_, err := tx.ExecContext(ctx, `SELECT fabric_transition_activity_lifecycle_v1($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		key.Stream.ProjectID, key.Stream.FabricInstanceID, key.Stream.StreamID, key.Stream.CanonicalRef,
		key.SourceWorkspaceID, key.ActivityID, transition.Kind, transition.ReferenceID,
		transition.ExpectedState, transition.NextState)
	if err != nil {
		if recoveryErr := recoverActivitySavepoint(ctx, tx, savepoint); recoveryErr != nil {
			return fmt.Errorf("git: transition activity lifecycle: recover savepoint: %w (original: %v)", recoveryErr, err)
		}
		switch activityDatabaseMessage(err) {
		case "activity binding unavailable", "activity not found", "activity lifecycle not found":
			return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityNotFound)
		case "activity lifecycle conflict":
			return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityLifecycleConflict)
		default:
			return fmt.Errorf("git: transition activity lifecycle: database: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+savepoint); err != nil {
		return fmt.Errorf("git: transition activity lifecycle: release savepoint: %w", err)
	}
	return nil
}

func validActivityLifecycleTransition(kind, expectedState, nextState string) bool {
	if expectedState == nextState {
		switch kind {
		case "delivery":
			return expectedState == "pending" || expectedState == "delivered" || expectedState == "cancelled"
		case "conflict":
			return expectedState == "open" || expectedState == "resolved" || expectedState == "cancelled"
		case "recovery":
			return expectedState == "pending" || expectedState == "blocked" || expectedState == "recovered" || expectedState == "cancelled"
		case "receipt":
			return expectedState == "pending" || expectedState == "confirmed" || expectedState == "rejected" || expectedState == "cancelled"
		default:
			return false
		}
	}
	switch kind {
	case "delivery":
		return expectedState == "pending" && (nextState == "delivered" || nextState == "cancelled")
	case "conflict":
		return expectedState == "open" && (nextState == "resolved" || nextState == "cancelled")
	case "recovery":
		return (expectedState == "pending" && (nextState == "blocked" || nextState == "recovered" || nextState == "cancelled")) ||
			(expectedState == "blocked" && (nextState == "pending" || nextState == "recovered" || nextState == "cancelled"))
	case "receipt":
		return expectedState == "pending" && (nextState == "confirmed" || nextState == "rejected" || nextState == "cancelled")
	default:
		return false
	}
}
