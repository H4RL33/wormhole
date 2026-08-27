package localapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func (s *Server) localPermissionGranted(ctx context.Context, requiredPermission, projectID string) (bool, error) {
	org, err := s.resolveOrgContext(projectID)
	if err != nil {
		return false, err
	}
	if org.ProjectID != projectID {
		return false, errors.New("permission denied: project scope is not configured")
	}
	var cached localstore.WhoAmICache
	if expectedAgent, ok := s.authorizationAgents.Load(projectID); ok {
		cached, err = s.store.GetCachedWhoAmIForAgentProject(ctx, expectedAgent.(string), projectID)
	} else {
		cached, err = s.store.GetCachedWhoAmIForProject(ctx, projectID)
	}
	if err != nil {
		if errors.Is(err, localstore.ErrNotFound) {
			return false, fmt.Errorf("permission denied: no authenticated scope cached for project %s; call wormhole.agent.whoami while online", projectID)
		}
		return false, fmt.Errorf("localapi: authorize %s: %w", requiredPermission, err)
	}
	for _, permission := range cached.Permissions {
		if permission == requiredPermission {
			return true, nil
		}
	}
	return false, nil
}
