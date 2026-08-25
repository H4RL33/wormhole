package projectstate

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type registrationCoordinator struct {
	repo                *localstore.WorkspaceRepo
	legacyBackupRoot    string
	registrationTimeout time.Duration
	newWorkspaceID      func() (types.WorkspaceID, error)
}

func newRegistrationCoordinator(repo *localstore.WorkspaceRepo, legacyBackupRoot string, registrationTimeout time.Duration, workspaceID func() (types.WorkspaceID, error)) *registrationCoordinator {
	return &registrationCoordinator{
		repo: repo, legacyBackupRoot: legacyBackupRoot,
		registrationTimeout: registrationTimeout, newWorkspaceID: workspaceID,
	}
}

func (c *registrationCoordinator) registerWorkspace(ctx context.Context, req RegisterWorkspaceRequest) (RegisterWorkspaceResult, error) {
	if c == nil || c.repo == nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("projectstate: service is unavailable")
	}
	registrationCtx, cancel := registrationContext(ctx, c.registrationTimeout)
	defer cancel()
	if !types.CanonicalUUID(req.ExpectedProjectID) || !validCommit(req.ExpectedCommit) {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: invalid project or commit identity", ErrInvalidRegistration)
	}
	if err := req.ExpectedRepository.Validate(); err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %v", ErrInvalidRegistration, err)
	}
	observed, err := inspectCommittedWorkspace(registrationCtx, req.Root, req.ExpectedCommit)
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %w", ErrInvalidRegistration, err)
	}
	if c.legacyBackupRoot != "" && pathsOverlap(c.legacyBackupRoot, observed.root) {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: process-private backup root overlaps repository", ErrInvalidRegistration)
	}
	if observed.snapshot.Config.ProjectID != req.ExpectedProjectID {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: committed project identity differs from request", ErrInvalidRegistration)
	}
	if observed.snapshot.Config.Repository != req.ExpectedRepository {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: committed repository identity differs from request", ErrInvalidRegistration)
	}
	canonicalTree, err := state.EncodeTree(observed.snapshot)
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: encode committed snapshot: %v", ErrInvalidRegistration, err)
	}
	revalidated, err := checkoutIdentity(observed.root)
	if err != nil || revalidated != observed.checkout {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: checkout identity changed before persistence", ErrInvalidRegistration)
	}
	workspaceID, err := c.newWorkspaceID()
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("projectstate: generate workspace ID: %w", err)
	}
	binding := types.WorkspaceBinding{
		Scope:    types.WorkspaceScope{ProjectID: req.ExpectedProjectID, WorkspaceID: workspaceID},
		Checkout: observed.checkout, Repository: req.ExpectedRepository,
		AcceptedRef: observed.acceptedRef, AcceptedCommitSHA: req.ExpectedCommit,
		AcceptedTreeDigest: string(observed.snapshot.Digest),
	}
	persisted, created, err := c.repo.RegisterWorkspace(registrationCtx, binding, canonicalTree)
	if err != nil {
		return RegisterWorkspaceResult{}, err
	}
	return RegisterWorkspaceResult{Binding: persisted, Created: created}, nil
}

func (c *registrationCoordinator) resolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error) {
	if c == nil || c.repo == nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	if err := observed.Validate(); err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved, err := filepath.EvalSymlinks(observed.WorkingDirectory)
	if err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved = filepath.Clean(resolved)
	binding, err := c.repo.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: resolved})
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	if err := verifyBindingCheckout(binding); err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	return binding, nil
}

func (c *registrationCoordinator) registeredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error) {
	if c == nil || c.repo == nil {
		return nil, fmt.Errorf("projectstate: service is unavailable")
	}
	return c.repo.RegisteredWorkspaces(ctx)
}
