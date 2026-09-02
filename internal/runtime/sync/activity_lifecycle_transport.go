package sync

import (
	"context"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

type ActivityLifecycleCommand struct {
	ActivityID string
	Change     localstore.ActivityLifecycleChange
}

func (s *ActivityTransport) Lifecycle(ctx context.Context, scope types.WorkspaceScope, command ActivityLifecycleCommand) error {
	if s == nil || !types.CanonicalUUID(command.ActivityID) {
		return localstore.ErrActivityLifecycleConflict
	}
	cycle, err := s.resolveNetworkCycle(ctx, scope)
	if err != nil {
		return err
	}
	origin, err := s.activities.ResolveOrigin(ctx, cycle.route, command.ActivityID)
	if err != nil {
		return classifyActivityError("lifecycle origin", err, ErrAttentionRequired)
	}
	response, err := cycle.client.Lifecycle(ctx, ActivityLifecycleRequest{
		AttachmentRef: cycle.binding.AttachmentRef,
		ActivityID:    command.ActivityID,
		Change:        command.Change,
	})
	if err != nil {
		return classifyActivityError("lifecycle", err, ErrFabricUnavailable)
	}
	if response.State != command.Change.NextState {
		return fmt.Errorf("sync: Activity lifecycle response: %w", localstore.ErrActivityLifecycleConflict)
	}
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil {
		return err
	}
	if resolved.route != cycle.route || resolved.binding.AttachmentRef != cycle.binding.AttachmentRef {
		return ErrAttentionRequired
	}
	if err := s.activities.TransitionLifecycle(ctx, origin, command.Change); err != nil {
		return classifyActivityError("lifecycle local", err, ErrAttentionRequired)
	}
	return nil
}
