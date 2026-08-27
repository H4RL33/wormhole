package projectstate

import (
	"context"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// gitBaseCoordinator owns observation and reconciliation of a workspace's
// accepted Git base. It deliberately shares the repository writer barriers
// with the facade's other coordinators without introducing another authority.
type gitBaseCoordinator struct {
	repo                             *localstore.WorkspaceRepo
	observeGitBase                   func(context.Context, ObserveGitBaseRequest) (gitBaseObservation, error)
	withImmediateWorkspace           withImmediateWorkspaceFunc
	withImmediateWorkspaceTransition withImmediateWorkspaceTransitionFunc
	now                              func() time.Time
}

func newGitBaseCoordinator(repo *localstore.WorkspaceRepo) *gitBaseCoordinator {
	return &gitBaseCoordinator{
		repo: repo, observeGitBase: observeGitBaseOutside,
		withImmediateWorkspace:           repo.WithImmediateWorkspace,
		withImmediateWorkspaceTransition: repo.WithImmediateWorkspaceTransition,
		now:                              time.Now,
	}
}
