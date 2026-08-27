package git

import (
	"context"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
)

func (s *ActivityStore) Prune(ctx context.Context, stream FabricActivityStreamKey, sourceWorkspaceID string, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("git: prune activity: %w", ErrActivityNotFound)
	}
	if err := validateFabricActivityStreamKey(stream); err != nil {
		return 0, err
	}
	if !types.CanonicalUUID(sourceWorkspaceID) {
		return 0, fmt.Errorf("git: prune activity: %w", ErrActivityNotFound)
	}
	if limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("git: prune activity: %w", ErrActivityLifecycleConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("git: prune activity: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setActivityProject(ctx, tx, stream.ProjectID); err != nil {
		return 0, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT fabric_prune_activities_v1($1,$2,$3,$4,$5,$6)`,
		stream.ProjectID, stream.FabricInstanceID, stream.StreamID, stream.CanonicalRef,
		sourceWorkspaceID, limit).Scan(&count)
	if err != nil {
		switch activityDatabaseMessage(err) {
		case "activity binding unavailable":
			return 0, fmt.Errorf("git: prune activity: %w", ErrActivityNotFound)
		case "activity prune input invalid", "activity prune protection changed":
			return 0, fmt.Errorf("git: prune activity: %w", ErrActivityLifecycleConflict)
		default:
			return 0, fmt.Errorf("git: prune activity: database: %w", err)
		}
	}
	if count < 0 || count > limit {
		return 0, fmt.Errorf("git: prune activity: %w", ErrActivityLifecycleConflict)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("git: prune activity: commit: %w", err)
	}
	return count, nil
}
