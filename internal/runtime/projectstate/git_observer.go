package projectstate

import (
	"context"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrBranchSwitchPending            = errors.New("projectstate: branch switch has pending workspace state")
	ErrGitObservationChanged          = errors.New("projectstate: git observation changed")
	ErrGitMaterializationPrecondition = errors.New("projectstate: git materialization precondition failed")
)

type BranchSwitchAction string

const (
	BranchSwitchReject  BranchSwitchAction = ""
	BranchSwitchDiscard BranchSwitchAction = "discard"
)

type ObserveGitBaseRequest struct {
	Scope           types.WorkspaceScope
	ExpectedBinding types.WorkspaceBinding
	Root            string
	ExpectedCommit  string
	BranchAction    BranchSwitchAction
	RequestID       string
	Actor           types.ActorEnvelope
}

type ObserveGitBaseResult struct {
	PreviousCommit, ObservedCommit         string
	PreviousRef, ObservedRef               string
	PreviousBaseDigest, ObservedBaseDigest state.Digest
	CandidateAccepted                      bool
	AcceptedJournalID                      *string
	Rebased                                bool
	Conflicts                              []Conflict
}

type gitBaseObservation struct {
	root        string
	checkout    types.CheckoutIdentity
	acceptedRef string
	commit      string
	tree        state.Tree
	snapshot    state.Snapshot
}

type gitBasePosition struct {
	root        string
	checkout    types.CheckoutIdentity
	acceptedRef string
	commit      string
}

type gitBasePositionReaders struct {
	canonicalRoot    func(string) (string, error)
	checkoutIdentity func(string) (types.CheckoutIdentity, error)
	symbolicHead     func(context.Context, string) (string, error)
	headCommit       func(context.Context, string) (string, error)
}

func validateObserveGitBaseRequest(req ObserveGitBaseRequest) error {
	if err := req.ExpectedBinding.Validate(); err != nil {
		return fmt.Errorf("projectstate: invalid expected Git binding: %w", err)
	}
	if !validDiscardRef(req.ExpectedBinding.AcceptedRef) {
		return fmt.Errorf("projectstate: invalid expected branch ref")
	}
	if req.Scope != req.ExpectedBinding.Scope {
		return fmt.Errorf("projectstate: Git observation scope differs from expected binding")
	}
	if err := validateObserveGitBaseRequestUTF8(req); err != nil {
		return err
	}
	if err := (types.WorkspaceContext{WorkingDirectory: req.Root}).Validate(); err != nil || req.Root != req.ExpectedBinding.Checkout.CanonicalPath {
		return fmt.Errorf("projectstate: Git observation root differs from expected binding")
	}
	if !validCommit(req.ExpectedCommit) {
		return fmt.Errorf("projectstate: invalid expected Git commit")
	}
	switch req.BranchAction {
	case BranchSwitchReject:
		if req.RequestID != "" {
			return fmt.Errorf("projectstate: reject Git observation forbids a request ID")
		}
		if req.Actor != (types.ActorEnvelope{}) {
			return fmt.Errorf("projectstate: reject Git observation requires the zero actor")
		}
	case BranchSwitchDiscard:
		if !types.CanonicalUUID(req.RequestID) {
			return fmt.Errorf("projectstate: invalid discard request ID")
		}
		if err := req.Actor.ValidateLocalAction(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("projectstate: invalid branch-switch action %q", req.BranchAction)
	}
	return nil
}

func readGitBaseObservation(
	ctx context.Context,
	req ObserveGitBaseRequest,
	reader func(context.Context, string, string) (committedWorkspace, error),
) (gitBaseObservation, error) {
	if err := validateObserveGitBaseRequest(req); err != nil {
		return gitBaseObservation{}, err
	}
	if reader == nil {
		return gitBaseObservation{}, fmt.Errorf("projectstate: committed-workspace reader is unavailable")
	}
	observed, err := reader(ctx, req.Root, req.ExpectedCommit)
	if err != nil {
		return gitBaseObservation{}, fmt.Errorf("projectstate: observe committed Git base: %w", err)
	}
	if observed.root != req.Root || observed.checkout != req.ExpectedBinding.Checkout {
		return gitBaseObservation{}, fmt.Errorf("projectstate: observed checkout differs from expected binding")
	}
	if observed.commit != req.ExpectedCommit || !validCommit(observed.commit) {
		return gitBaseObservation{}, fmt.Errorf("projectstate: observed Git HEAD differs from expected commit")
	}
	if !validDiscardRef(observed.acceptedRef) {
		return gitBaseObservation{}, fmt.Errorf("projectstate: invalid observed symbolic Git ref")
	}
	ownedTree := cloneCheckpointTree(observed.tree)
	snapshot, err := state.DecodeTree(ownedTree)
	if err != nil {
		return gitBaseObservation{}, fmt.Errorf("projectstate: decode observed committed .wormhole tree: %w", err)
	}
	if snapshot.Config.ProjectID != req.ExpectedBinding.Scope.ProjectID {
		return gitBaseObservation{}, fmt.Errorf("projectstate: observed project differs from expected binding")
	}
	if snapshot.Config.Repository != req.ExpectedBinding.Repository {
		return gitBaseObservation{}, fmt.Errorf("projectstate: observed repository differs from expected binding")
	}
	return gitBaseObservation{
		root: observed.root, checkout: observed.checkout, acceptedRef: observed.acceptedRef,
		commit: observed.commit, tree: ownedTree, snapshot: snapshot,
	}, nil
}

func observeGitBaseOutside(ctx context.Context, req ObserveGitBaseRequest) (gitBaseObservation, error) {
	return readGitBaseObservation(ctx, req, inspectCommittedWorkspaceForGitBase)
}

func readGitBasePosition(ctx context.Context, requestedRoot string) (gitBasePosition, error) {
	return readGitBasePositionWithReaders(ctx, requestedRoot, gitBasePositionReaders{
		canonicalRoot: canonicalNonSymlinkDirectory, checkoutIdentity: checkoutIdentity,
		symbolicHead: symbolicHead, headCommit: readCommittedWorkspaceHead,
	})
}

func readGitBasePositionWithReaders(ctx context.Context, requestedRoot string, readers gitBasePositionReaders) (gitBasePosition, error) {
	root, err := readers.canonicalRoot(requestedRoot)
	if err != nil {
		return gitBasePosition{}, err
	}
	checkout, err := readers.checkoutIdentity(root)
	if err != nil {
		return gitBasePosition{}, err
	}
	acceptedRef, err := readers.symbolicHead(ctx, root)
	if err != nil {
		return gitBasePosition{}, err
	}
	commit, err := readers.headCommit(ctx, root)
	if err != nil {
		return gitBasePosition{}, fmt.Errorf("projectstate: resolve Git HEAD: %w", err)
	}
	if !validCommit(commit) {
		return gitBasePosition{}, fmt.Errorf("projectstate: invalid observed Git HEAD")
	}
	finalRoot, err := readers.canonicalRoot(requestedRoot)
	if err != nil {
		return gitBasePosition{}, fmt.Errorf("projectstate: revalidate checkout root: %w", err)
	}
	if finalRoot != root {
		return gitBasePosition{}, fmt.Errorf("projectstate: checkout root changed during Git position read")
	}
	finalCheckout, err := readers.checkoutIdentity(finalRoot)
	if err != nil {
		return gitBasePosition{}, fmt.Errorf("projectstate: revalidate checkout identity: %w", err)
	}
	if finalCheckout != checkout {
		return gitBasePosition{}, fmt.Errorf("projectstate: checkout identity changed during Git position read")
	}
	return gitBasePosition{root: root, checkout: checkout, acceptedRef: acceptedRef, commit: commit}, nil
}

func reobserveGitBase(ctx context.Context, outside gitBaseObservation) error {
	return reobserveGitBaseWithReader(ctx, outside, readGitBasePosition)
}

func reobserveGitBaseWithReader(
	ctx context.Context,
	outside gitBaseObservation,
	reader func(context.Context, string) (gitBasePosition, error),
) error {
	if reader == nil {
		return fmt.Errorf("%w: Git position reader is unavailable", ErrGitObservationChanged)
	}
	inside, err := reader(ctx, outside.root)
	if err != nil {
		return fmt.Errorf("%w: reobserve Git position: %w", ErrGitObservationChanged, err)
	}
	if inside.root != outside.root || inside.checkout != outside.checkout ||
		inside.acceptedRef != outside.acceptedRef || inside.commit != outside.commit {
		return fmt.Errorf("%w: checkout, symbolic ref, or HEAD differs", ErrGitObservationChanged)
	}
	return nil
}
