package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrCheckpointPendingAcceptance = errors.New("projectstate: checkpoint pending acceptance")
	ErrPublicationUnclassified     = errors.New("projectstate: publication is unclassified")
	ErrPublicationReviewRequired   = errors.New("projectstate: publication review required")
	ErrPublicationReviewStale      = errors.New("projectstate: publication review stale")
)

type CheckpointRequest struct {
	Scope                     types.WorkspaceScope
	Root                      string
	ExpectedWorkingTreeDigest state.Digest
	PublicationReviewDigest   *state.Digest
	Actor                     types.ActorEnvelope
}

type CheckpointResult struct {
	CandidateDigest               state.Digest
	MaterializedThroughGeneration int64
	JournalID                     string
}

type checkpointArtifactHandle struct {
	evidence checkpointArtifactEvidence
	publish  func(context.Context) (checkpointPublicationDisposition, error)
	close    func()
}

type prepareCheckpointArtifactFunc func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error)
type confirmCheckpointCommitFunc func(
	context.Context,
	localstore.WorkspaceCheckpointCommitState,
	localstore.WorkspaceCheckpointCommitState,
) (localstore.WorkspaceCheckpointCommitMatch, error)

type checkpointGateSet struct {
	mu      sync.Mutex
	byScope map[types.WorkspaceScope]*checkpointWorkspaceGate
}

type checkpointWorkspaceGate struct {
	permit chan struct{}
	refs   int
}

type checkpointOutsideTrust struct {
	workspace localstore.WorkspaceRecord
	observed  publicationTrustObservation
	observer  func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error)
}

func defaultPrepareCheckpointArtifact(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
	artifact, err := prepareCheckpointArtifact(ctx, input)
	if err != nil {
		return checkpointArtifactHandle{}, err
	}
	return checkpointArtifactHandle{
		evidence: artifact.evidence(),
		publish: func(ctx context.Context) (checkpointPublicationDisposition, error) {
			return publishPreparedCheckpointArtifact(ctx, artifact)
		},
		close: artifact.close,
	}, nil
}

func (s *Service) Checkpoint(ctx context.Context, req CheckpointRequest) (CheckpointResult, error) {
	result, err := s.checkpoint(ctx, req)
	if err != nil {
		return CheckpointResult{}, err
	}
	return result, nil
}

func (s *Service) checkpoint(ctx context.Context, req CheckpointRequest) (CheckpointResult, error) {
	if s == nil || s.repo == nil || ctx == nil {
		return CheckpointResult{}, localstore.ErrNotFound
	}
	if req.PublicationReviewDigest != nil {
		owned := *req.PublicationReviewDigest
		req.PublicationReviewDigest = &owned
	}
	if !validPublicationScope(req.Scope) {
		return CheckpointResult{}, localstore.ErrNotFound
	}
	release, err := s.checkpointGates.acquire(ctx, req.Scope)
	if err != nil {
		return CheckpointResult{}, err
	}
	defer release()

	firstOutside, err := s.checkpointOutsideTrust(ctx, req.Scope)
	if err != nil {
		return CheckpointResult{}, err
	}
	withWorkspace := s.withImmediateWorkspace
	if withWorkspace == nil {
		withWorkspace = s.repo.WithImmediateWorkspace
	}
	prepareArtifact := s.prepareCheckpointArtifact
	if prepareArtifact == nil {
		prepareArtifact = defaultPrepareCheckpointArtifact
	}
	confirmCommit := s.confirmCheckpointCommit
	if confirmCommit == nil {
		confirmCommit = s.repo.ConfirmCheckpointCommit
	}

	var artifact checkpointArtifactHandle
	artifactReceived := false
	defer func() {
		if artifactReceived && artifact.close != nil {
			artifact.close()
		}
	}()

	var firstPlan checkpointPlan
	var firstReview publicationReviewTransactionEvidence
	var firstDisposition localstore.WorkspaceMaterializationDisposition
	var prepared localstore.WorkspaceMaterializationRecord
	var firstPrior, firstNext localstore.WorkspaceCheckpointCommitState
	firstCompleted, firstInvalidated := false, false
	firstErr := withWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		disposition, err := tx.MaterializationDisposition(ctx)
		if err != nil {
			return err
		}
		pending, err := checkpointPendingJournal(firstOutside.workspace.Binding, disposition)
		if err != nil {
			return err
		}
		if pending != nil {
			return ErrCheckpointPendingAcceptance
		}
		proof, err := proveMaterializationDisposition(disposition)
		if err != nil {
			return fmt.Errorf("projectstate: checkpoint materialization disposition: %w", err)
		}
		if err := proveCheckpointTerminalHistory(firstOutside.workspace.Binding, disposition, proof); err != nil {
			return err
		}
		firstDisposition = cloneImportDisposition(disposition)

		firstPrior, err = tx.CaptureCheckpointCommitState(ctx)
		if err != nil {
			return err
		}
		workspace, err := checkpointValidateTransactionRequest(ctx, tx, req)
		if err != nil {
			return err
		}
		live, err := s.checkpointReadLive(req.Root, workspace.Binding, req.ExpectedWorkingTreeDigest, nil)
		if err != nil {
			return err
		}
		open, err := tx.HasOpenConflicts(ctx)
		if err != nil {
			return err
		}
		if open {
			return localstore.ErrWorkspaceConflicted
		}
		current, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		current, err = cloneImportCandidate(current)
		if err != nil {
			return err
		}
		composed, err := loadComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		disposition = cloneImportDisposition(disposition)
		if err := proveCheckpointMaterialPreflight(
			workspace, current, composed, disposition, live, req.Actor,
		); err != nil {
			return err
		}
		var reviewAttempt publicationTransitionAttempt
		firstReview, err = s.publicationReviewInTransaction(
			ctx, tx, firstOutside.workspace, firstOutside.observed, firstOutside.observer, &reviewAttempt,
		)
		if err != nil {
			return err
		}
		if firstReview.policy.Classification == types.PublicationUnclassified {
			if !reviewAttempt.completed {
				return ErrPublicationUnclassified
			}
			firstInvalidated = true
			firstNext, err = tx.CaptureCheckpointCommitState(ctx)
			if err != nil {
				return err
			}
			firstCompleted = true
			return nil
		}
		if err := validateCheckpointAcknowledgement(firstReview.policy.Classification, firstReview.reviewDigest, req.PublicationReviewDigest); err != nil {
			return err
		}
		firstPlan, err = proveCheckpointPlan(checkpointPlanInput{
			Binding: workspace.Binding, Current: current, Composed: composed,
			Disposition: disposition, Review: firstReview, PriorLiveTree: live, Actor: req.Actor,
		})
		if err != nil {
			return err
		}
		artifact, err = prepareArtifact(ctx, checkpointArtifactInput{
			Checkout:  workspace.Binding.Checkout,
			PriorTree: firstPlan.PriorTree, PriorTreeDigest: firstPlan.PriorTreeDigest,
			CandidateTree: firstPlan.CandidateTree, CandidateDigest: firstPlan.CandidateDigest,
		})
		artifactReceived = true
		if err != nil {
			return err
		}
		if err := validateCheckpointArtifactHandle(artifact); err != nil {
			return err
		}
		if _, err := s.checkpointReadLive(req.Root, workspace.Binding, req.ExpectedWorkingTreeDigest, firstPlan.PriorTree); err != nil {
			return err
		}
		included, publication, priorCandidate := firstPlan.IncludedOperationsJSON, firstPlan.PublicationReviewJSON, firstPlan.PriorCandidateJSON
		proposed := localstore.WorkspaceMaterializationRecord{
			JournalID:                     artifact.evidence.JournalID,
			ExpectedLiveDigest:            firstPlan.PriorTreeDigest,
			AcceptedBaseDigest:            state.Digest(workspace.Binding.AcceptedTreeDigest),
			Checkout:                      workspace.Binding.Checkout,
			PriorTreeDigest:               firstPlan.PriorTreeDigest,
			CandidateDigest:               firstPlan.CandidateDigest,
			ThroughGeneration:             firstPlan.ThroughGeneration,
			PriorTree:                     cloneCheckpointTree(firstPlan.PriorTree),
			CandidateTree:                 cloneCheckpointTree(firstPlan.CandidateTree),
			StagePath:                     artifact.evidence.StagePath,
			BackupPath:                    artifact.evidence.BackupPath,
			IncludedOperationsJSON:        &included,
			PublicationReviewProofVersion: 1,
			PublicationReviewJSON:         &publication,
			PriorCandidateJSON:            &priorCandidate,
			State:                         "prepared",
		}
		prepared, err = tx.PrepareMaterialization(ctx, proposed)
		if err != nil {
			return err
		}
		if !equalMaterializationRecord(prepared, proposed) {
			return fmt.Errorf("projectstate: prepared checkpoint journal differs from proposed record")
		}
		firstNext, err = tx.CaptureCheckpointCommitState(ctx)
		if err != nil {
			return err
		}
		firstCompleted = true
		return nil
	})
	if firstErr != nil {
		if !errors.Is(firstErr, localstore.ErrCommitOutcomeUnknown) || !firstCompleted {
			return CheckpointResult{}, firstErr
		}
		committed, confirmErr := confirmCheckpointTransition(ctx, confirmCommit, firstPrior, firstNext, firstErr)
		if confirmErr != nil {
			return CheckpointResult{}, confirmErr
		}
		if !committed {
			return CheckpointResult{}, firstErr
		}
	}
	if firstInvalidated {
		return CheckpointResult{}, ErrPublicationUnclassified
	}

	secondOutside, err := s.checkpointOutsideTrust(ctx, req.Scope)
	if err != nil {
		return CheckpointResult{}, err
	}
	var success CheckpointResult
	var secondPrior, secondNext localstore.WorkspaceCheckpointCommitState
	secondCompleted, secondInvalidated, preservedOld := false, false, false
	secondErr := withWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		disposition, err := tx.MaterializationDisposition(ctx)
		if err != nil {
			return err
		}
		terminal, err := checkpointRequirePreparedDisposition(secondOutside.workspace.Binding, disposition, prepared)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(firstDisposition, terminal) {
			return fmt.Errorf("projectstate: checkpoint materialization disposition changed before publication")
		}
		secondPrior, err = tx.CaptureCheckpointCommitState(ctx)
		if err != nil {
			return err
		}
		workspace, err := checkpointValidateTransactionRequest(ctx, tx, req)
		if err != nil {
			return err
		}
		live, err := s.checkpointReadLive(req.Root, workspace.Binding, req.ExpectedWorkingTreeDigest, firstPlan.PriorTree)
		if err != nil {
			return err
		}
		open, err := tx.HasOpenConflicts(ctx)
		if err != nil {
			return err
		}
		if open {
			return localstore.ErrWorkspaceConflicted
		}
		current, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		current, err = cloneImportCandidate(current)
		if err != nil {
			return err
		}
		composed, err := loadComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		preliminaryPlan, err := proveCheckpointPlan(checkpointPlanInput{
			Binding: workspace.Binding, Current: current, Composed: composed,
			Disposition: terminal, Review: firstReview, PriorLiveTree: live, Actor: req.Actor,
		})
		if err != nil {
			return err
		}
		if !equalCheckpointPlans(firstPlan, preliminaryPlan) || !checkpointMaterializationMatchesPlan(prepared, workspace.Binding, firstPlan) {
			return fmt.Errorf("projectstate: checkpoint plan changed before publication review")
		}
		var reviewAttempt publicationTransitionAttempt
		review, err := s.publicationReviewInTransaction(
			ctx, tx, secondOutside.workspace, secondOutside.observed, secondOutside.observer, &reviewAttempt,
		)
		if err != nil {
			return err
		}
		if review.policy.Classification == types.PublicationUnclassified {
			if !reviewAttempt.completed {
				return ErrPublicationUnclassified
			}
			secondInvalidated = true
			secondNext, err = tx.CaptureCheckpointCommitState(ctx)
			if err != nil {
				return err
			}
			secondCompleted = true
			return nil
		}
		if err := validateCheckpointAcknowledgement(review.policy.Classification, review.reviewDigest, req.PublicationReviewDigest); err != nil {
			return err
		}
		currentPlan, err := proveCheckpointPlan(checkpointPlanInput{
			Binding: workspace.Binding, Current: current, Composed: composed,
			Disposition: terminal, Review: review, PriorLiveTree: live, Actor: req.Actor,
		})
		if err != nil {
			return err
		}
		if !equalCheckpointPlans(firstPlan, currentPlan) || !checkpointMaterializationMatchesPlan(prepared, workspace.Binding, firstPlan) {
			return fmt.Errorf("projectstate: checkpoint plan changed before publication")
		}
		publication, err := artifact.publish(ctx)
		if err != nil {
			return err
		}
		switch publication {
		case checkpointPublicationPublished:
			published, err := tx.TransitionMaterialization(ctx, prepared, "published")
			if err != nil {
				return err
			}
			wantPublished := cloneMaterializationRecord(prepared)
			wantPublished.State = "published"
			if !equalMaterializationRecord(published, wantPublished) {
				return fmt.Errorf("projectstate: published checkpoint journal differs from prepared record")
			}
			postimage, err := checkpointPublicationPostimage(published)
			if err != nil {
				return err
			}
			if err := tx.UpsertCandidate(ctx, postimage); err != nil {
				return err
			}
			if err := tx.TransitionOperations(ctx, currentPlan.IncludedOperations, "materialized", nil); err != nil {
				return err
			}
			if workspace.State == "clean" {
				if err := tx.SetStatus(ctx, "pending"); err != nil {
					return err
				}
			} else if workspace.State != "pending" {
				return fmt.Errorf("projectstate: checkpoint workspace state changed before publication")
			}
			success = CheckpointResult{
				CandidateDigest:               currentPlan.CandidateDigest,
				MaterializedThroughGeneration: currentPlan.ThroughGeneration,
				JournalID:                     prepared.JournalID,
			}
		case checkpointPublicationPreservedConcurrentOld:
			recovered, err := tx.TransitionMaterialization(ctx, prepared, "recovered_old")
			if err != nil {
				return err
			}
			wantRecovered := cloneMaterializationRecord(prepared)
			wantRecovered.State = "recovered_old"
			if !equalMaterializationRecord(recovered, wantRecovered) {
				return fmt.Errorf("projectstate: recovered-old checkpoint journal differs from prepared record")
			}
			preservedOld = true
		default:
			return fmt.Errorf("%w: invalid checkpoint publication disposition %d", ErrCheckpointRecoveryBlocked, publication)
		}
		secondNext, err = tx.CaptureCheckpointCommitState(ctx)
		if err != nil {
			return err
		}
		secondCompleted = true
		return nil
	})
	if secondErr != nil {
		if !errors.Is(secondErr, localstore.ErrCommitOutcomeUnknown) || !secondCompleted {
			return CheckpointResult{}, secondErr
		}
		if secondInvalidated {
			committed, confirmErr := confirmCheckpointTransition(ctx, confirmCommit, secondPrior, secondNext, secondErr)
			if confirmErr != nil {
				return CheckpointResult{}, confirmErr
			}
			if !committed {
				return CheckpointResult{}, secondErr
			}
		} else {
			match, confirmErr := confirmCommit(ctx, secondPrior, secondNext)
			if confirmErr != nil {
				return CheckpointResult{}, fmt.Errorf("%w: checkpoint final commit confirmation failed: %v", ErrCheckpointRecoveryBlocked, confirmErr)
			}
			switch match {
			case localstore.WorkspaceCheckpointCommitNext:
			case localstore.WorkspaceCheckpointCommitPrior:
				return CheckpointResult{}, secondErr
			case localstore.WorkspaceCheckpointCommitThird:
				return CheckpointResult{}, fmt.Errorf("%w: checkpoint final commit confirmation found a third state", ErrCheckpointRecoveryBlocked)
			default:
				return CheckpointResult{}, fmt.Errorf("%w: checkpoint final commit confirmation returned invalid outcome %d", ErrCheckpointRecoveryBlocked, match)
			}
		}
	}
	if secondInvalidated {
		return CheckpointResult{}, ErrPublicationUnclassified
	}
	if preservedOld {
		return CheckpointResult{}, ErrCheckpointCAS
	}
	return success, nil
}

func proveCheckpointMaterialPreflight(
	workspace localstore.WorkspaceRecord,
	current *localstore.WorkspaceCandidateRecord,
	composed composedWorkspace,
	disposition localstore.WorkspaceMaterializationDisposition,
	priorLiveTree state.Tree,
	actor types.ActorEnvelope,
) error {
	input := checkpointPlanInput{
		Binding: workspace.Binding, Current: current, Composed: composed,
		Disposition: disposition, PriorLiveTree: priorLiveTree, Actor: actor,
		Review: publicationReviewTransactionEvidence{
			workspace: workspace,
			composed:  composed,
			status:    composed.status,
		},
	}
	acceptedTree, err := proveCheckpointWorkspace(input)
	if err != nil {
		return err
	}
	dispositionProof, err := proveMaterializationDisposition(disposition)
	if err != nil {
		return fmt.Errorf("projectstate: checkpoint materialization disposition: %w", err)
	}
	if err := proveCheckpointTerminalHistory(workspace.Binding, disposition, dispositionProof); err != nil {
		return err
	}
	_, selectedStart, boundary, err := proveCheckpointPriorCandidate(workspace.Binding, current, acceptedTree)
	if err != nil {
		return err
	}
	activeRows, err := proveCheckpointActiveRows(disposition, boundary)
	if err != nil {
		return err
	}
	activeOperations, err := decodeStoredOperations(activeRows)
	if err != nil {
		return fmt.Errorf("projectstate: checkpoint active operations: %w", err)
	}
	provedView, _, err := proveCheckpointComposition(composed, composed, selectedStart, boundary, activeOperations)
	if err != nil {
		return err
	}
	selected, selectedActiveRows, envelope, err := proveCheckpointOperationSelection(disposition, boundary)
	if err != nil {
		return err
	}
	if !checkpointOperationRowsEqual(activeRows, selectedActiveRows) {
		return fmt.Errorf("projectstate: checkpoint active-operation selection changed across material preflight")
	}
	throughGeneration := boundary
	if len(selected) != 0 && selected[len(selected)-1].Generation > throughGeneration {
		throughGeneration = selected[len(selected)-1].Generation
	}
	if throughGeneration != provedView.ThroughGeneration {
		return fmt.Errorf("projectstate: checkpoint operation boundary differs from material preflight composition")
	}
	if _, err := encodeCheckpointOperations(envelope); err != nil {
		return err
	}
	if _, _, err := proveCheckpointPriorLiveTree(priorLiveTree, workspace.Binding); err != nil {
		return err
	}
	return nil
}

func (g *checkpointGateSet) acquire(ctx context.Context, scope types.WorkspaceScope) (func(), error) {
	g.mu.Lock()
	if g.byScope == nil {
		g.byScope = make(map[types.WorkspaceScope]*checkpointWorkspaceGate)
	}
	entry := g.byScope[scope]
	if entry == nil {
		entry = &checkpointWorkspaceGate{permit: make(chan struct{}, 1)}
		entry.permit <- struct{}{}
		g.byScope[scope] = entry
	}
	entry.refs++
	g.mu.Unlock()

	acquired := false
	select {
	case <-entry.permit:
		acquired = true
	case <-ctx.Done():
		g.releaseReference(scope, entry)
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		if acquired {
			entry.permit <- struct{}{}
		}
		g.releaseReference(scope, entry)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.permit <- struct{}{}
			g.releaseReference(scope, entry)
		})
	}, nil
}

func (g *checkpointGateSet) releaseReference(scope types.WorkspaceScope, entry *checkpointWorkspaceGate) {
	g.mu.Lock()
	entry.refs--
	if entry.refs == 0 && g.byScope[scope] == entry {
		delete(g.byScope, scope)
	}
	g.mu.Unlock()
}

func (s *Service) checkpointOutsideTrust(ctx context.Context, scope types.WorkspaceScope) (checkpointOutsideTrust, error) {
	workspace, err := s.repo.Workspace(ctx, scope)
	if err != nil {
		return checkpointOutsideTrust{}, err
	}
	workspace, err = cloneImportWorkspace(workspace)
	if err != nil {
		return checkpointOutsideTrust{}, err
	}
	observer := s.publicationTrustObserver()
	observed, err := observer(ctx, workspace.Binding)
	if err != nil {
		return checkpointOutsideTrust{}, err
	}
	if err := validatePublicationReviewTrust(observed, workspace, nil); err != nil {
		return checkpointOutsideTrust{}, err
	}
	return checkpointOutsideTrust{
		workspace: workspace,
		observed:  clonePublicationTrustObservation(observed),
		observer:  observer,
	}, nil
}

func checkpointValidateTransactionRequest(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	req CheckpointRequest,
) (localstore.WorkspaceRecord, error) {
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return localstore.WorkspaceRecord{}, err
	}
	workspace, err = cloneImportWorkspace(workspace)
	if err != nil {
		return localstore.WorkspaceRecord{}, err
	}
	if !validPublicationScope(req.Scope) || workspace.Binding.Scope != req.Scope {
		return localstore.WorkspaceRecord{}, localstore.ErrNotFound
	}
	if err := req.Actor.ValidateLocalAction(); err != nil {
		return localstore.WorkspaceRecord{}, err
	}
	if req.Root == "" || !filepath.IsAbs(req.Root) || filepath.Clean(req.Root) != req.Root ||
		req.Root != workspace.Binding.Checkout.CanonicalPath {
		return localstore.WorkspaceRecord{}, fmt.Errorf("projectstate: checkpoint root differs from workspace checkout")
	}
	if !validPublicationDigest(req.ExpectedWorkingTreeDigest) {
		return localstore.WorkspaceRecord{}, fmt.Errorf("projectstate: invalid checkpoint working-tree digest")
	}
	if err := workspace.Binding.Validate(); err != nil {
		return localstore.WorkspaceRecord{}, err
	}
	if err := verifyBindingCheckout(workspace.Binding); err != nil {
		return localstore.WorkspaceRecord{}, localstore.ErrNotFound
	}
	return workspace, nil
}

func (s *Service) checkpointReadLive(
	root string,
	binding types.WorkspaceBinding,
	expected state.Digest,
	first state.Tree,
) (state.Tree, error) {
	reader := s.readWorkingTree
	if reader == nil {
		reader = ReadWorkingTreeNoFollow
	}
	tree, err := reader(root)
	if err != nil {
		return nil, err
	}
	tree = cloneCheckpointTree(tree)
	digest, err := state.DigestTree(tree)
	if err != nil {
		return nil, err
	}
	if err := validateMatchingTree(tree, digest, binding); err != nil {
		return nil, fmt.Errorf("projectstate: checkpoint live tree: %w", err)
	}
	if digest != expected {
		return nil, fmt.Errorf("%w: expected %s, observed %s", ErrCheckpointCAS, expected, digest)
	}
	if first != nil && !equalCheckpointTree(tree, first) {
		return nil, fmt.Errorf("%w: live tree bytes changed", ErrCheckpointCAS)
	}
	return tree, nil
}

func validateCheckpointAcknowledgement(
	classification types.PublicationClassification,
	current state.Digest,
	acknowledged *state.Digest,
) error {
	switch classification {
	case types.PublicationUnclassified:
		return ErrPublicationUnclassified
	case types.PublicationPublicGit:
		if acknowledged == nil {
			return ErrPublicationReviewRequired
		}
		if *acknowledged != current {
			return ErrPublicationReviewStale
		}
		return nil
	case types.PublicationLocalOnly, types.PublicationPrivateGit:
		if acknowledged != nil && *acknowledged != current {
			return ErrPublicationReviewStale
		}
		return nil
	default:
		return fmt.Errorf("projectstate: invalid publication classification")
	}
}

func validateCheckpointArtifactHandle(handle checkpointArtifactHandle) error {
	evidence := handle.evidence
	if !types.CanonicalUUID(evidence.JournalID) || handle.publish == nil || handle.close == nil ||
		!validCheckpointArtifactPath(evidence.StagePath) || !validCheckpointArtifactPath(evidence.BackupPath) ||
		evidence.StagePath == evidence.BackupPath || filepath.Dir(evidence.StagePath) != filepath.Dir(evidence.BackupPath) ||
		filepath.Base(evidence.StagePath) != evidence.JournalID+".stage" ||
		filepath.Base(evidence.BackupPath) != evidence.JournalID+".backup" {
		return fmt.Errorf("projectstate: malformed prepared checkpoint artifact")
	}
	return nil
}

func validCheckpointArtifactPath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Dir(value) != value
}

func confirmCheckpointTransition(
	ctx context.Context,
	confirm confirmCheckpointCommitFunc,
	prior localstore.WorkspaceCheckpointCommitState,
	next localstore.WorkspaceCheckpointCommitState,
	commitErr error,
) (bool, error) {
	match, err := confirm(ctx, prior, next)
	if err != nil {
		return false, fmt.Errorf("%w: checkpoint commit confirmation failed: %v", commitErr, err)
	}
	switch match {
	case localstore.WorkspaceCheckpointCommitNext:
		return true, nil
	case localstore.WorkspaceCheckpointCommitPrior:
		return false, commitErr
	case localstore.WorkspaceCheckpointCommitThird:
		return false, fmt.Errorf("%w: checkpoint commit confirmation found a third state", commitErr)
	default:
		return false, fmt.Errorf("%w: checkpoint commit confirmation returned invalid outcome %d", commitErr, match)
	}
}

func checkpointPendingJournal(
	binding types.WorkspaceBinding,
	disposition localstore.WorkspaceMaterializationDisposition,
) (*localstore.WorkspaceMaterializationRecord, error) {
	if disposition.Journals == nil || disposition.Operations == nil {
		return nil, fmt.Errorf("projectstate: incomplete checkpoint materialization disposition")
	}
	for index, operation := range disposition.Operations {
		if operation.Generation <= 0 || !types.CanonicalUUID(operation.OperationID) ||
			(index > 0 && operation.Generation <= disposition.Operations[index-1].Generation) {
			return nil, fmt.Errorf("projectstate: malformed checkpoint operation inventory")
		}
		decoded, err := state.DecodeOperation(operation.OperationJSON)
		if err != nil {
			return nil, err
		}
		canonical, err := state.CanonicalOperation(decoded)
		if err != nil || decoded.ID != operation.OperationID || !bytes.Equal(canonical, operation.OperationJSON) {
			return nil, fmt.Errorf("projectstate: checkpoint operation inventory differs from canonical operation")
		}
	}
	var pending *localstore.WorkspaceMaterializationRecord
	for index, journal := range disposition.Journals {
		if journal.JournalID == "" || (index > 0 && journal.JournalID <= disposition.Journals[index-1].JournalID) {
			return nil, fmt.Errorf("projectstate: checkpoint journals are not strictly ordered and unique")
		}
		switch journal.State {
		case "prepared", "published", "recovered_new":
			if pending != nil {
				return nil, fmt.Errorf("projectstate: multiple or mixed pending checkpoint journals")
			}
			if err := validateCheckpointPendingRecord(binding, journal); err != nil {
				return nil, err
			}
			cloned := cloneMaterializationRecord(journal)
			pending = &cloned
		case "accepted", "recovered_old":
		default:
			return nil, fmt.Errorf("projectstate: invalid checkpoint journal state")
		}
	}
	if pending != nil {
		if err := validateCheckpointTerminalDispositionWithPending(binding, disposition); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

func validateCheckpointTerminalDispositionWithPending(
	binding types.WorkspaceBinding,
	disposition localstore.WorkspaceMaterializationDisposition,
) error {
	terminal := localstore.WorkspaceMaterializationDisposition{
		Journals:   make([]localstore.WorkspaceMaterializationRecord, 0, len(disposition.Journals)),
		Operations: make([]localstore.WorkspaceOperation, 0, len(disposition.Operations)),
	}
	claimedMaterialized := make(map[int64]struct{})
	for _, journal := range disposition.Journals {
		if journal.State != "accepted" && journal.State != "recovered_old" {
			continue
		}
		terminal.Journals = append(terminal.Journals, cloneMaterializationRecord(journal))
		if journal.State != "accepted" || journal.IncludedOperationsJSON == nil {
			continue
		}
		envelope, err := decodeCheckpointOperations(*journal.IncludedOperationsJSON)
		if err != nil {
			return fmt.Errorf("projectstate: terminal checkpoint journal %q operation proof: %w", journal.JournalID, err)
		}
		for _, operation := range envelope.Operations {
			claimedMaterialized[operation.Generation] = struct{}{}
		}
	}
	for _, operation := range disposition.Operations {
		if operation.State == "materialized" {
			if _, claimed := claimedMaterialized[operation.Generation]; !claimed {
				continue
			}
		}
		terminal.Operations = append(terminal.Operations, cloneImportOperation(operation))
	}
	proof, err := proveMaterializationDisposition(terminal)
	if err != nil {
		return fmt.Errorf("projectstate: terminal checkpoint history beside pending journal: %w", err)
	}
	if err := proveCheckpointTerminalHistory(binding, terminal, proof); err != nil {
		return err
	}
	return nil
}

func validateCheckpointPendingRecord(binding types.WorkspaceBinding, journal localstore.WorkspaceMaterializationRecord) error {
	if !types.CanonicalUUID(journal.JournalID) || journal.PublicationReviewProofVersion != 1 ||
		journal.IncludedOperationsJSON == nil || journal.PublicationReviewJSON == nil || journal.PriorCandidateJSON == nil ||
		journal.ExpectedLiveDigest != journal.PriorTreeDigest || journal.AcceptedBaseDigest != state.Digest(binding.AcceptedTreeDigest) ||
		journal.Checkout != binding.Checkout || journal.ThroughGeneration < 0 ||
		!validCheckpointArtifactPath(journal.StagePath) || !validCheckpointArtifactPath(journal.BackupPath) ||
		filepath.Dir(journal.StagePath) != filepath.Dir(journal.BackupPath) ||
		filepath.Base(journal.StagePath) != journal.JournalID+".stage" ||
		filepath.Base(journal.BackupPath) != journal.JournalID+".backup" {
		return fmt.Errorf("projectstate: malformed pending checkpoint journal")
	}
	if err := validateMatchingTree(journal.PriorTree, journal.PriorTreeDigest, binding); err != nil {
		return fmt.Errorf("projectstate: pending checkpoint prior tree: %w", err)
	}
	if err := validateMatchingTree(journal.CandidateTree, journal.CandidateDigest, binding); err != nil {
		return fmt.Errorf("projectstate: pending checkpoint candidate tree: %w", err)
	}
	operations, err := decodeCheckpointOperations(*journal.IncludedOperationsJSON)
	if err != nil {
		return err
	}
	through := operations.InitialThroughGeneration
	if len(operations.Operations) != 0 && operations.Operations[len(operations.Operations)-1].Generation > through {
		through = operations.Operations[len(operations.Operations)-1].Generation
	}
	if through != journal.ThroughGeneration {
		return fmt.Errorf("projectstate: pending checkpoint operation boundary differs")
	}
	publication, err := decodeCheckpointPublicationReview(*journal.PublicationReviewJSON)
	if err != nil {
		return err
	}
	if publication.Review.Scope != binding.Scope || publication.Review.Repository != binding.Repository ||
		publication.Review.AcceptedRef != binding.AcceptedRef ||
		publication.Review.AcceptedCommitSHA != binding.AcceptedCommitSHA ||
		publication.Review.AcceptedTreeDigest != journal.AcceptedBaseDigest ||
		publication.Review.CandidateTreeDigest != journal.CandidateDigest ||
		publication.Review.OverlayGeneration != journal.ThroughGeneration {
		return fmt.Errorf("projectstate: pending checkpoint publication proof differs")
	}
	prior, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
	if err != nil {
		return err
	}
	if prior.Candidate == nil {
		if operations.InitialThroughGeneration != 0 {
			return fmt.Errorf("projectstate: absent pending checkpoint candidate has a nonzero boundary")
		}
		return nil
	}
	if prior.Candidate.AcceptedBaseDigest != journal.AcceptedBaseDigest ||
		prior.Candidate.RebasedThroughGeneration != operations.InitialThroughGeneration {
		return fmt.Errorf("projectstate: pending checkpoint prior candidate cross-proof differs")
	}
	direct, err := validateCheckpointPriorTree(prior.Candidate.DirectTree, checkpointPriorTreeProductionLimits())
	if err != nil || direct.Config.ProjectID != binding.Scope.ProjectID || direct.Config.Repository != binding.Repository {
		return fmt.Errorf("projectstate: pending checkpoint direct prior candidate differs")
	}
	if prior.Candidate.RebasedTree != nil {
		rebased, err := validateCheckpointPriorTree(*prior.Candidate.RebasedTree, checkpointPriorTreeProductionLimits())
		if err != nil || rebased.Config.ProjectID != binding.Scope.ProjectID || rebased.Config.Repository != binding.Repository {
			return fmt.Errorf("projectstate: pending checkpoint rebased prior candidate differs")
		}
	}
	return nil
}

func checkpointRequirePreparedDisposition(
	binding types.WorkspaceBinding,
	disposition localstore.WorkspaceMaterializationDisposition,
	expected localstore.WorkspaceMaterializationRecord,
) (localstore.WorkspaceMaterializationDisposition, error) {
	pending, err := checkpointPendingJournal(binding, disposition)
	if err != nil {
		return localstore.WorkspaceMaterializationDisposition{}, err
	}
	if pending == nil || pending.State != "prepared" || !equalMaterializationRecord(*pending, expected) {
		return localstore.WorkspaceMaterializationDisposition{}, fmt.Errorf("projectstate: prepared checkpoint journal changed before publication")
	}
	terminal := cloneImportDisposition(disposition)
	kept := make([]localstore.WorkspaceMaterializationRecord, 0, len(terminal.Journals)-1)
	removed := 0
	for _, journal := range terminal.Journals {
		if journal.JournalID == expected.JournalID {
			removed++
			continue
		}
		kept = append(kept, journal)
	}
	if removed != 1 {
		return localstore.WorkspaceMaterializationDisposition{}, fmt.Errorf("projectstate: prepared checkpoint journal cardinality changed")
	}
	terminal.Journals = kept
	proof, err := proveMaterializationDisposition(terminal)
	if err != nil {
		return localstore.WorkspaceMaterializationDisposition{}, err
	}
	if err := proveCheckpointTerminalHistory(binding, terminal, proof); err != nil {
		return localstore.WorkspaceMaterializationDisposition{}, err
	}
	return terminal, nil
}

func equalCheckpointPlans(left, right checkpointPlan) bool {
	return left.PriorTreeDigest == right.PriorTreeDigest && left.CandidateDigest == right.CandidateDigest &&
		left.ThroughGeneration == right.ThroughGeneration &&
		left.IncludedOperationsJSON == right.IncludedOperationsJSON &&
		left.PriorCandidateJSON == right.PriorCandidateJSON &&
		left.PublicationReviewJSON == right.PublicationReviewJSON &&
		left.PublicationReviewDigest == right.PublicationReviewDigest &&
		equalCheckpointTree(left.PriorTree, right.PriorTree) &&
		equalCheckpointTree(left.CandidateTree, right.CandidateTree) &&
		checkpointOperationRowsEqual(left.IncludedOperations, right.IncludedOperations)
}

func checkpointMaterializationMatchesPlan(
	record localstore.WorkspaceMaterializationRecord,
	binding types.WorkspaceBinding,
	plan checkpointPlan,
) bool {
	return record.State == "prepared" && record.ExpectedLiveDigest == plan.PriorTreeDigest &&
		record.AcceptedBaseDigest == state.Digest(binding.AcceptedTreeDigest) && record.Checkout == binding.Checkout &&
		record.PriorTreeDigest == plan.PriorTreeDigest && record.CandidateDigest == plan.CandidateDigest &&
		record.ThroughGeneration == plan.ThroughGeneration &&
		equalCheckpointTree(record.PriorTree, plan.PriorTree) && equalCheckpointTree(record.CandidateTree, plan.CandidateTree) &&
		record.IncludedOperationsJSON != nil && *record.IncludedOperationsJSON == plan.IncludedOperationsJSON &&
		record.PublicationReviewProofVersion == 1 && record.PublicationReviewJSON != nil &&
		*record.PublicationReviewJSON == plan.PublicationReviewJSON && record.PriorCandidateJSON != nil &&
		*record.PriorCandidateJSON == plan.PriorCandidateJSON
}

// checkpointPublicationPostimage reconstructs the exact candidate publication
// postimage from durable v1 journal proof. It deliberately trusts no fresh
// request actor so Task-5 recovery can reuse the same construction.
func checkpointPublicationPostimage(journal localstore.WorkspaceMaterializationRecord) (localstore.WorkspaceCandidateRecord, error) {
	if journal.PublicationReviewProofVersion != 1 || journal.PublicationReviewJSON == nil ||
		journal.PriorCandidateJSON == nil || journal.IncludedOperationsJSON == nil {
		return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: checkpoint publication postimage lacks v1 proof")
	}
	publication, err := decodeCheckpointPublicationReview(*journal.PublicationReviewJSON)
	if err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	prior, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
	if err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	operations, err := decodeCheckpointOperations(*journal.IncludedOperationsJSON)
	if err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	review := publication.Review
	binding := types.WorkspaceBinding{
		Scope: review.Scope, Checkout: journal.Checkout, Repository: review.Repository,
		AcceptedRef: review.AcceptedRef, AcceptedCommitSHA: review.AcceptedCommitSHA,
		AcceptedTreeDigest: string(review.AcceptedTreeDigest),
	}
	if err := binding.Validate(); err != nil || publication.CheckpointedBy.ValidateLocalAction() != nil ||
		journal.ExpectedLiveDigest != journal.PriorTreeDigest || journal.AcceptedBaseDigest != review.AcceptedTreeDigest ||
		journal.CandidateDigest != review.CandidateTreeDigest || journal.ThroughGeneration != review.OverlayGeneration {
		return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: checkpoint publication postimage proof differs from journal")
	}
	through := operations.InitialThroughGeneration
	if len(operations.Operations) != 0 && operations.Operations[len(operations.Operations)-1].Generation > through {
		through = operations.Operations[len(operations.Operations)-1].Generation
	}
	if through != journal.ThroughGeneration {
		return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: checkpoint publication postimage operation boundary differs")
	}
	if err := validateMatchingTree(journal.PriorTree, journal.PriorTreeDigest, binding); err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	if err := validateMatchingTree(journal.CandidateTree, journal.CandidateDigest, binding); err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	direct, err := state.DecodeTree(cloneCheckpointTree(journal.PriorTree))
	if err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	rebased, err := state.DecodeTree(cloneCheckpointTree(journal.CandidateTree))
	if err != nil {
		return localstore.WorkspaceCandidateRecord{}, err
	}
	importedBy := publication.CheckpointedBy.PrincipalID()
	importedAt := publication.CheckpointedBy.OccurredAt
	if prior.Candidate == nil {
		if operations.InitialThroughGeneration != 0 {
			return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: absent prior candidate has nonzero boundary")
		}
	} else {
		if prior.Candidate.AcceptedBaseDigest != journal.AcceptedBaseDigest ||
			prior.Candidate.RebasedThroughGeneration != operations.InitialThroughGeneration {
			return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: prior candidate differs from publication boundary")
		}
		directPrior, err := validateCheckpointPriorTree(prior.Candidate.DirectTree, checkpointPriorTreeProductionLimits())
		if err != nil {
			return localstore.WorkspaceCandidateRecord{}, err
		}
		if directPrior.Config.ProjectID != binding.Scope.ProjectID || directPrior.Config.Repository != binding.Repository {
			return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: prior candidate identity differs from publication binding")
		}
		importedBy = prior.Candidate.ImportedBy
		importedAt = prior.Candidate.ImportedAt
	}
	if !types.ValidCandidateImportOrigin(importedBy) || importedAt.IsZero() {
		return localstore.WorkspaceCandidateRecord{}, fmt.Errorf("projectstate: checkpoint publication postimage has invalid provenance")
	}
	return localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest:       journal.AcceptedBaseDigest,
		WorkingTreeDigest:        journal.PriorTreeDigest,
		DirectSnapshot:           direct,
		RebasedSnapshot:          &rebased,
		RebasedThroughGeneration: journal.ThroughGeneration,
		ImportedBy:               importedBy,
		ImportedAt:               importedAt,
	}, nil
}
