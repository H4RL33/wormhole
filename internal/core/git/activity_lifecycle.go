package git

import (
	"context"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
)

func (s *ActivityStore) TransitionLifecycle(ctx context.Context, key FabricActivityOriginKey, transition ActivityLifecycleTransition) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityNotFound)
	}
	if err := key.validate(); err != nil {
		return err
	}
	if !validActivityLifecycleKind(transition.Kind) || !types.CanonicalUUID(transition.ReferenceID) ||
		transition.ExpectedState == "" || transition.NextState == "" {
		return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityLifecycleConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("git: transition activity lifecycle: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, key.Stream.ProjectID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `SELECT fabric_transition_activity_lifecycle_v1($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		key.Stream.ProjectID, key.Stream.FabricInstanceID, key.Stream.StreamID, key.Stream.CanonicalRef,
		key.SourceWorkspaceID, key.ActivityID, transition.Kind, transition.ReferenceID,
		transition.ExpectedState, transition.NextState)
	if err != nil {
		switch activityDatabaseMessage(err) {
		case "activity binding unavailable", "activity not found", "activity lifecycle not found":
			return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityNotFound)
		case "activity lifecycle conflict":
			return fmt.Errorf("git: transition activity lifecycle: %w", ErrActivityLifecycleConflict)
		default:
			return fmt.Errorf("git: transition activity lifecycle: database: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("git: transition activity lifecycle: commit: %w", err)
	}
	return nil
}

func validActivityLifecycleKind(kind string) bool {
	switch kind {
	case "delivery", "conflict", "recovery", "receipt":
		return true
	default:
		return false
	}
}
