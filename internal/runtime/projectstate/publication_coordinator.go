package projectstate

import (
	"context"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

// publicationCoordinator owns publication policy and review observation.  It
// deliberately receives the persistence and observation seams so the facade
// does not become a second transaction authority.
type publicationCoordinator struct {
	repo                    *localstore.WorkspaceRepo
	observeOrigin           func(context.Context, string) (publicationOriginObservation, error)
	observeTrust            func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error)
	withImmediateWorkspace  withImmediateWorkspaceFunc
	now                     func() time.Time
	confirmTransitionCommit confirmPublicationTransitionCommitFunc
	observeGitBase          func(context.Context, ObserveGitBaseRequest) (gitBaseObservation, error)
}

func (c *publicationCoordinator) publicationTrustObserver() func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
	if c != nil && c.observeTrust != nil {
		return c.observeTrust
	}
	gitObserver := observeGitBaseOutside
	if c != nil && c.observeGitBase != nil {
		gitObserver = c.observeGitBase
	}
	originObserver := observePublicationOrigin
	if c != nil && c.observeOrigin != nil {
		originObserver = c.observeOrigin
	}
	return func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
		return observePublicationTrustWithObservers(ctx, binding, gitObserver, originObserver)
	}
}

func (c *publicationCoordinator) confirmPublication(ctx context.Context, scope types.WorkspaceScope, attempt publicationTransitionAttempt, commitErr error) (PublicationConfiguration, error) {
	if c == nil || c.repo == nil {
		return PublicationConfiguration{}, fmt.Errorf("%w: publication coordinator unavailable", commitErr)
	}
	if c.confirmTransitionCommit != nil {
		return c.confirmTransitionCommit(ctx, c.repo, scope, attempt, commitErr)
	}
	return confirmPublicationCommit(ctx, c.repo, scope, attempt, commitErr)
}
