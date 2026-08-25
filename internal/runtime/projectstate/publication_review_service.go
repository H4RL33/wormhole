package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicationTrustObservation struct {
	git    gitBaseObservation
	origin publicationOriginObservation
}

type publicationReviewResult struct {
	status WorkspaceStatus
	diff   WorkspaceDiff
}

// publicationReviewTransactionEvidence is the complete owned result of one
// review linearization point. Task-5 checkpoint transactions consume this
// helper rather than independently reconstructing trusted review inputs.
type publicationReviewTransactionEvidence struct {
	workspace          localstore.WorkspaceRecord
	composed           composedWorkspace
	trust              publicationTrustObservation
	policy             localstore.WorkspacePublicationPolicyRecord
	semanticDiff       Diff
	semanticDiffDigest state.Digest
	envelope           publicationReviewEnvelopeV1
	reviewDigest       state.Digest
	status             WorkspaceStatus
	diff               WorkspaceDiff
}

func observePublicationTrustOutside(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
	return observePublicationTrustWithObservers(ctx, binding, observeGitBaseOutside, observePublicationOrigin)
}

func observePublicationTrustWithObservers(
	ctx context.Context,
	binding types.WorkspaceBinding,
	gitObserver func(context.Context, ObserveGitBaseRequest) (gitBaseObservation, error),
	originObserver func(context.Context, string) (publicationOriginObservation, error),
) (publicationTrustObservation, error) {
	if err := binding.Validate(); err != nil || gitObserver == nil || originObserver == nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: invalid publication trust binding or observer", ErrGitObservationChanged)
	}
	gitObserved, err := gitObserver(ctx, ObserveGitBaseRequest{
		Scope: binding.Scope, ExpectedBinding: binding, Root: binding.Checkout.CanonicalPath,
		ExpectedCommit: binding.AcceptedCommitSHA, BranchAction: BranchSwitchReject,
	})
	if err != nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: observe full Git base: %w", ErrGitObservationChanged, err)
	}
	originObserved, originErr := originObserver(ctx, binding.Checkout.CanonicalPath)
	finalPosition, finalErr := readGitBasePosition(ctx, binding.Checkout.CanonicalPath)
	if finalErr != nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: final publication trust position: %w", ErrGitObservationChanged, finalErr)
	}
	if err := validatePublicationGitObservation(gitObserved); err != nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: invalid full Git observation: %w", ErrGitObservationChanged, err)
	}
	if finalPosition.root != gitObserved.root || finalPosition.checkout != gitObserved.checkout ||
		finalPosition.acceptedRef != gitObserved.acceptedRef || finalPosition.commit != gitObserved.commit {
		return publicationTrustObservation{}, fmt.Errorf("%w: Git position changed across origin observation", ErrGitObservationChanged)
	}
	if originErr != nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: observe publication origin: %w", ErrGitOriginChanged, originErr)
	}
	if err := validatePublicationOriginObservation(originObserved); err != nil {
		return publicationTrustObservation{}, fmt.Errorf("%w: invalid origin observation: %w", ErrGitOriginChanged, err)
	}
	if originObserved.root != gitObserved.root || originObserved.checkout != gitObserved.checkout {
		return publicationTrustObservation{}, fmt.Errorf("%w: Git and origin observation checkouts differ", ErrGitOriginChanged)
	}
	observed := publicationTrustObservation{git: gitObserved, origin: originObserved}
	if err := validatePublicationTrustObservation(observed); err != nil {
		return publicationTrustObservation{}, err
	}
	return clonePublicationTrustObservation(observed), nil
}

func (c *publicationCoordinator) readPublicationReview(ctx context.Context, scope types.WorkspaceScope) (publicationReviewResult, error) {
	if !validPublicationScope(scope) || c == nil || c.repo == nil {
		return publicationReviewResult{}, localstore.ErrNotFound
	}
	outsideWorkspace, err := c.repo.Workspace(ctx, scope)
	if err != nil {
		return publicationReviewResult{}, err
	}
	observer := c.publicationTrustObserver()
	outside, err := observer(ctx, outsideWorkspace.Binding)
	if err != nil {
		return publicationReviewResult{}, err
	}
	if err := validatePublicationReviewTrust(outside, outsideWorkspace, nil); err != nil {
		return publicationReviewResult{}, err
	}
	outside = clonePublicationTrustObservation(outside)

	withWorkspace := c.withImmediateWorkspace
	if withWorkspace == nil {
		withWorkspace = c.repo.WithImmediateWorkspace
	}
	var evidence publicationReviewTransactionEvidence
	var attempt publicationTransitionAttempt
	err = withWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		var transactionErr error
		evidence, transactionErr = c.publicationReviewInTransaction(
			ctx, tx, outsideWorkspace, outside, observer, &attempt,
		)
		return transactionErr
	})
	if err != nil {
		if errors.Is(err, localstore.ErrCommitOutcomeUnknown) && attempt.completed {
			if _, confirmationErr := c.confirmPublication(ctx, scope, attempt, err); confirmationErr != nil {
				return publicationReviewResult{}, confirmationErr
			}
		} else {
			return publicationReviewResult{}, err
		}
	}
	return clonePublicationReviewResult(publicationReviewResult{status: evidence.status, diff: evidence.diff})
}

func (c *publicationCoordinator) publicationReviewInTransaction(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	expectedWorkspace localstore.WorkspaceRecord,
	outside publicationTrustObservation,
	observer func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error),
	attempt *publicationTransitionAttempt,
) (publicationReviewTransactionEvidence, error) {
	if observer == nil || attempt == nil {
		return publicationReviewTransactionEvidence{}, fmt.Errorf("projectstate: publication review transaction dependencies are unavailable")
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	policy, err := tx.PublicationPolicy(ctx)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	if workspace.Binding != expectedWorkspace.Binding {
		return publicationReviewTransactionEvidence{}, fmt.Errorf("%w: persisted binding changed after outside observation", ErrGitObservationChanged)
	}
	inside, err := observer(ctx, workspace.Binding)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	if err := validatePublicationReviewTrust(inside, workspace, &outside); err != nil {
		return publicationReviewTransactionEvidence{}, err
	}

	loaded, err := loadComposedWorkspace(ctx, tx)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	if loaded.status.Binding != workspace.Binding {
		return publicationReviewTransactionEvidence{}, fmt.Errorf("%w: composed binding differs from trusted binding", ErrGitObservationChanged)
	}
	attributedView, semanticDiff, err := publicationAttributedDiff(
		loaded.status.AcceptedSnapshot, loaded.selectedStart, loaded.boundary, loaded.operations,
	)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	if attributedView.Snapshot.Digest != loaded.view.Snapshot.Digest ||
		attributedView.ThroughGeneration != loaded.view.ThroughGeneration ||
		!equalPublicationOperationIDs(attributedView.AppliedOperationIDs, loaded.view.AppliedOperationIDs) {
		return publicationReviewTransactionEvidence{}, fmt.Errorf("projectstate: publication review composition mismatch")
	}
	semanticDiff, err = normalizePublicationReviewDiff(semanticDiff)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	_, semanticDigest, err := encodePublicationSemanticDiff(semanticDiff)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}

	resolvedPolicy := policy
	if kind := publicationInvalidationKind(workspace.Binding, policy, inside.origin.digest); kind != "" {
		next, err := c.newPublicationInvalidation(workspace.Binding, policy, inside.origin.digest, kind)
		if err != nil {
			return publicationReviewTransactionEvidence{}, err
		}
		resolvedPolicy, err = applyPublicationTransition(
			ctx, tx, workspace.Binding.Scope, policy, next, inside.origin.digest, true, attempt,
		)
		if err != nil {
			return publicationReviewTransactionEvidence{}, err
		}
	}
	envelope := publicationReviewEnvelopeV1{
		SchemaVersion:       publicationReviewSchemaVersion,
		Kind:                publicationReviewKind,
		Scope:               workspace.Binding.Scope,
		Repository:          workspace.Binding.Repository,
		OriginDigest:        inside.origin.digest,
		Classification:      resolvedPolicy.Classification,
		PolicyRevision:      resolvedPolicy.PolicyRevision,
		AcceptedRef:         workspace.Binding.AcceptedRef,
		AcceptedCommitSHA:   workspace.Binding.AcceptedCommitSHA,
		AcceptedTreeDigest:  state.Digest(workspace.Binding.AcceptedTreeDigest),
		CandidateTreeDigest: attributedView.Snapshot.Digest,
		SemanticDiffDigest:  semanticDigest,
		OverlayGeneration:   attributedView.ThroughGeneration,
	}
	_, reviewDigest, err := encodePublicationReviewEnvelope(envelope)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	status := loaded.status
	status.CandidateDigest = attributedView.Snapshot.Digest
	status.OverlayGeneration = attributedView.ThroughGeneration
	status.PublicationClassification = resolvedPolicy.Classification
	status.PublicationReviewDigest = reviewDigest
	evidence := publicationReviewTransactionEvidence{
		workspace:          workspace,
		composed:           loaded,
		trust:              inside,
		policy:             resolvedPolicy,
		semanticDiff:       semanticDiff,
		semanticDiffDigest: semanticDigest,
		envelope:           envelope,
		reviewDigest:       reviewDigest,
		status:             status,
		diff: WorkspaceDiff{
			SemanticDiff:              semanticDiff,
			CandidateDigest:           attributedView.Snapshot.Digest,
			OverlayGeneration:         attributedView.ThroughGeneration,
			PublicationClassification: resolvedPolicy.Classification,
			PublicationReviewDigest:   reviewDigest,
		},
	}
	return clonePublicationReviewTransactionEvidence(evidence)
}

func validatePublicationTrustObservation(observed publicationTrustObservation) error {
	if err := validatePublicationGitObservation(observed.git); err != nil {
		return fmt.Errorf("%w: invalid full Git observation: %v", ErrGitObservationChanged, err)
	}
	if err := validatePublicationOriginObservation(observed.origin); err != nil {
		return fmt.Errorf("%w: invalid origin observation", ErrGitOriginChanged)
	}
	if observed.git.root != observed.origin.root || observed.git.checkout != observed.origin.checkout {
		return fmt.Errorf("%w: Git and origin observation checkouts differ", ErrGitOriginChanged)
	}
	return nil
}

func validatePublicationReviewTrust(
	observed publicationTrustObservation,
	workspace localstore.WorkspaceRecord,
	outside *publicationTrustObservation,
) error {
	if err := validatePublicationGitObservation(observed.git); err != nil {
		return fmt.Errorf("%w: invalid full Git observation: %w", ErrGitObservationChanged, err)
	}
	if err := matchPublicationTrustBinding(observed, workspace); err != nil {
		return err
	}
	if outside != nil {
		if err := comparePublicationGitObservations(*outside, observed); err != nil {
			return err
		}
	}
	if err := validatePublicationOriginObservation(observed.origin); err != nil {
		return fmt.Errorf("%w: invalid origin observation: %w", ErrGitOriginChanged, err)
	}
	if observed.git.root != observed.origin.root || observed.git.checkout != observed.origin.checkout {
		return fmt.Errorf("%w: Git and origin observation checkouts differ", ErrGitOriginChanged)
	}
	if outside != nil {
		return comparePublicationOriginObservations(*outside, observed)
	}
	return nil
}

func validatePublicationGitObservation(observed gitBaseObservation) error {
	if !filepath.IsAbs(observed.root) || filepath.Clean(observed.root) != observed.root ||
		observed.checkout.CanonicalPath != observed.root || observed.checkout.Device == 0 || observed.checkout.Inode == 0 ||
		!validDiscardRef(observed.acceptedRef) || !validPublicationCommit(observed.commit) || observed.tree == nil {
		return fmt.Errorf("projectstate: malformed full Git observation")
	}
	decoded, err := state.DecodeTree(cloneCheckpointTree(observed.tree))
	if err != nil {
		return fmt.Errorf("projectstate: decode full Git observation: %w", err)
	}
	rendered, err := state.EncodeTree(observed.snapshot)
	if err != nil {
		return fmt.Errorf("projectstate: encode full Git observation snapshot: %w", err)
	}
	if !equalCheckpointTree(rendered, observed.tree) || decoded.Digest != observed.snapshot.Digest {
		return fmt.Errorf("projectstate: full Git tree and snapshot differ")
	}
	return nil
}

func matchPublicationTrustBinding(observed publicationTrustObservation, workspace localstore.WorkspaceRecord) error {
	binding := workspace.Binding
	if err := binding.Validate(); err != nil || observed.git.root != binding.Checkout.CanonicalPath ||
		observed.git.checkout != binding.Checkout || observed.git.acceptedRef != binding.AcceptedRef ||
		observed.git.commit != binding.AcceptedCommitSHA ||
		observed.git.snapshot.Digest != state.Digest(binding.AcceptedTreeDigest) ||
		observed.git.snapshot.Config.ProjectID != binding.Scope.ProjectID ||
		observed.git.snapshot.Config.Repository != binding.Repository {
		return fmt.Errorf("%w: observed Git base differs from complete stored binding", ErrGitObservationChanged)
	}
	storedTree, err := state.EncodeTree(workspace.Snapshot)
	if err != nil || workspace.Snapshot.Digest != state.Digest(binding.AcceptedTreeDigest) ||
		!equalCheckpointTree(storedTree, observed.git.tree) {
		return fmt.Errorf("%w: stored accepted snapshot differs from observed Git tree", ErrGitObservationChanged)
	}
	return nil
}

func comparePublicationGitObservations(outside, inside publicationTrustObservation) error {
	if outside.git.root != inside.git.root || outside.git.checkout != inside.git.checkout ||
		outside.git.acceptedRef != inside.git.acceptedRef || outside.git.commit != inside.git.commit ||
		outside.git.snapshot.Digest != inside.git.snapshot.Digest ||
		outside.git.snapshot.Config.ProjectID != inside.git.snapshot.Config.ProjectID ||
		outside.git.snapshot.Config.Repository != inside.git.snapshot.Config.Repository ||
		!equalCheckpointTree(outside.git.tree, inside.git.tree) {
		return fmt.Errorf("%w: full Git observation changed across writer barrier", ErrGitObservationChanged)
	}
	return nil
}

func comparePublicationOriginObservations(outside, inside publicationTrustObservation) error {
	if outside.origin.root != inside.origin.root || outside.origin.checkout != inside.origin.checkout ||
		outside.origin.origin != inside.origin.origin || outside.origin.digest != inside.origin.digest {
		return fmt.Errorf("%w: origin observation changed across writer barrier", ErrGitOriginChanged)
	}
	return nil
}

func clonePublicationTrustObservation(observed publicationTrustObservation) publicationTrustObservation {
	cloned := observed
	cloned.git.tree = cloneCheckpointTree(observed.git.tree)
	if snapshot, err := state.DecodeTree(cloned.git.tree); err == nil {
		cloned.git.snapshot = snapshot
	} else {
		cloned.git.snapshot = state.Snapshot{}
	}
	return cloned
}

func clonePublicationReviewTransactionEvidence(
	evidence publicationReviewTransactionEvidence,
) (publicationReviewTransactionEvidence, error) {
	cloned := evidence
	var err error
	cloned.workspace, err = cloneImportWorkspace(evidence.workspace)
	if err != nil {
		return publicationReviewTransactionEvidence{}, fmt.Errorf("projectstate: clone publication review workspace: %w", err)
	}
	cloned.composed, err = clonePublicationComposedWorkspace(evidence.composed)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	cloned.trust = clonePublicationTrustObservation(evidence.trust)
	cloned.policy = cloneServicePublicationPolicyRecord(evidence.policy)
	cloned.semanticDiff, err = clonePublicationSemanticDiff(evidence.semanticDiff)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	cloned.status, err = clonePublicationReviewStatus(evidence.status)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	cloned.diff, err = clonePublicationReviewDiff(evidence.diff)
	if err != nil {
		return publicationReviewTransactionEvidence{}, err
	}
	return cloned, nil
}

func clonePublicationComposedWorkspace(value composedWorkspace) (composedWorkspace, error) {
	cloned := value
	var err error
	cloned.status, err = clonePublicationReviewStatus(value.status)
	if err != nil {
		return composedWorkspace{}, err
	}
	cloned.view.Snapshot, err = cloneImportSnapshot(value.view.Snapshot)
	if err != nil {
		return composedWorkspace{}, fmt.Errorf("projectstate: clone publication composed view: %w", err)
	}
	cloned.view.AppliedOperationIDs = make([]string, len(value.view.AppliedOperationIDs))
	copy(cloned.view.AppliedOperationIDs, value.view.AppliedOperationIDs)
	cloned.selectedStart, err = cloneImportSnapshot(value.selectedStart)
	if err != nil {
		return composedWorkspace{}, fmt.Errorf("projectstate: clone publication selected start: %w", err)
	}
	cloned.operations = make([]StoredOperation, len(value.operations))
	for index, stored := range value.operations {
		encoded, err := state.CanonicalOperation(stored.Operation)
		if err != nil {
			return composedWorkspace{}, fmt.Errorf("projectstate: clone publication operation: %w", err)
		}
		operation, err := state.DecodeOperation(encoded)
		if err != nil {
			return composedWorkspace{}, fmt.Errorf("projectstate: decode cloned publication operation: %w", err)
		}
		cloned.operations[index] = StoredOperation{Generation: stored.Generation, Operation: operation}
	}
	return cloned, nil
}

func equalPublicationOperationIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// SemanticDiff preserves the typed record encoding for lifecycle values. The
// publication codec binds generic canonical JSON, so normalize those raw field
// values before returning or digesting the review-specific projection.
func normalizePublicationReviewDiff(diff Diff) (Diff, error) {
	cloned, err := clonePublicationSemanticDiff(diff)
	if err != nil {
		return Diff{}, err
	}
	for changeIndex := range cloned.Changes {
		for fieldIndex := range cloned.Changes[changeIndex].Fields {
			field := &cloned.Changes[changeIndex].Fields[fieldIndex]
			for _, value := range []*FieldValue{&field.Before, &field.After} {
				if !value.Present {
					continue
				}
				decoded, err := decodeDiffJSON(value.Value)
				if err != nil {
					return Diff{}, fmt.Errorf("projectstate: normalize publication field JSON: %w", err)
				}
				value.Value, err = canonicalFieldJSON(decoded)
				if err != nil {
					return Diff{}, fmt.Errorf("projectstate: canonicalize publication field JSON: %w", err)
				}
			}
		}
	}
	return cloned, nil
}

func clonePublicationReviewResult(result publicationReviewResult) (publicationReviewResult, error) {
	status, err := clonePublicationReviewStatus(result.status)
	if err != nil {
		return publicationReviewResult{}, err
	}
	diff, err := clonePublicationReviewDiff(result.diff)
	if err != nil {
		return publicationReviewResult{}, err
	}
	return publicationReviewResult{status: status, diff: diff}, nil
}

func clonePublicationReviewStatus(status WorkspaceStatus) (WorkspaceStatus, error) {
	cloned := status
	var err error
	cloned.AcceptedSnapshot, err = cloneImportSnapshot(status.AcceptedSnapshot)
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("projectstate: clone publication review status: %w", err)
	}
	return cloned, nil
}

func clonePublicationReviewDiff(diff WorkspaceDiff) (WorkspaceDiff, error) {
	cloned := diff
	semantic, err := clonePublicationSemanticDiff(diff.SemanticDiff)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	cloned.SemanticDiff = semantic
	return cloned, nil
}

func clonePublicationSemanticDiff(diff Diff) (Diff, error) {
	cloned := Diff{BaseDigest: diff.BaseDigest, ViewDigest: diff.ViewDigest}
	if diff.Changes == nil {
		return Diff{}, fmt.Errorf("projectstate: clone publication semantic diff with nil changes")
	}
	cloned.Changes = make([]Change, len(diff.Changes))
	for changeIndex, change := range diff.Changes {
		copyChange := change
		copyChange.BeforeDigest = clonePublicationDigest(change.BeforeDigest)
		copyChange.AfterDigest = clonePublicationDigest(change.AfterDigest)
		copyChange.BeforeBodyDigest = clonePublicationDigest(change.BeforeBodyDigest)
		copyChange.AfterBodyDigest = clonePublicationDigest(change.AfterBodyDigest)
		copyChange.Actor = publicationActorCopyIfPresent(change.Actor)
		if change.Fields == nil {
			return Diff{}, fmt.Errorf("projectstate: clone publication change with nil fields")
		}
		copyChange.Fields = make([]FieldChange, len(change.Fields))
		for fieldIndex, field := range change.Fields {
			copyField := field
			copyField.Before.Value = bytes.Clone(field.Before.Value)
			copyField.After.Value = bytes.Clone(field.After.Value)
			copyField.Actor = publicationActorCopyIfPresent(field.Actor)
			copyChange.Fields[fieldIndex] = copyField
		}
		cloned.Changes[changeIndex] = copyChange
	}
	return cloned, nil
}

func publicationActorCopyIfPresent(actor *types.ActorEnvelope) *types.ActorEnvelope {
	if actor == nil {
		return nil
	}
	return publicationActorCopy(*actor)
}
