package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrBranchSwitchPending              = errors.New("projectstate: branch switch has pending workspace state")
	ErrBranchSwitchDiscardNotApplicable = errors.New("projectstate: branch switch discard not applicable")
	ErrGitObservationChanged            = errors.New("projectstate: git observation changed")
	ErrGitMaterializationPrecondition   = errors.New("projectstate: git materialization precondition failed")
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

type observeGitBaseState struct {
	workspace                localstore.WorkspaceRecord
	candidate                *localstore.WorkspaceCandidateRecord
	audit                    []restoreAuditOperation
	openConflicts            []localstore.WorkspaceConflictOccurrence
	dispositionProof         materializationDispositionProof
	eligible                 *localstore.WorkspaceMaterializationRecord
	materializationPriorTree state.Tree
	activeRows               []localstore.WorkspaceOperation
	discardRows              []localstore.WorkspaceOperation
	proposal                 bool
}

// ObserveGitBase reconciles one independently observed committed Git base with
// the exact persisted workspace under a single SQLite writer barrier.
func (s *Service) ObserveGitBase(ctx context.Context, req ObserveGitBaseRequest) (ObserveGitBaseResult, error) {
	if err := validateObserveGitBaseRequest(req); err != nil {
		return ObserveGitBaseResult{}, err
	}
	var requestDigest state.Digest
	if req.BranchAction == BranchSwitchDiscard {
		var err error
		requestDigest, err = discardRequestDigest(req)
		if err != nil {
			return ObserveGitBaseResult{}, err
		}
	}
	if s == nil || s.repo == nil {
		return ObserveGitBaseResult{}, localstore.ErrNotFound
	}

	if req.BranchAction == BranchSwitchDiscard {
		receipt, err := s.repo.TransitionReceiptByKey(ctx, req.Scope, req.RequestID)
		if err != nil {
			return ObserveGitBaseResult{}, err
		}
		if receipt != nil {
			return decodeExistingDiscardReceipt(receipt, req, requestDigest)
		}
	}
	if s.now == nil {
		return ObserveGitBaseResult{}, localstore.ErrNotFound
	}

	observer := s.observeGitBase
	if observer == nil {
		observer = observeGitBaseOutside
	}
	observed, err := observer(ctx, req)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}

	var attempted ObserveGitBaseResult
	confirmableDiscard := false
	mutate := func(tx *localstore.WorkspaceMutationTx) error {
		loaded, err := loadObserveGitBaseState(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := reobserveGitBase(ctx, observed); err != nil {
			return err
		}
		mutationTime := s.now().UTC()
		if mutationTime.IsZero() || !zeroOffsetTime(mutationTime) {
			return fmt.Errorf("projectstate: invalid Git observation transaction time")
		}
		result := newObserveGitBaseResult(loaded.workspace, observed)
		refChanged := loaded.workspace.Binding.AcceptedRef != observed.acceptedRef
		commitChanged := loaded.workspace.Binding.AcceptedCommitSHA != observed.commit
		treeChanged := state.Digest(loaded.workspace.Binding.AcceptedTreeDigest) != observed.snapshot.Digest
		if !commitChanged && treeChanged {
			return fmt.Errorf("%w: one commit identity produced a different canonical tree", ErrGitObservationChanged)
		}
		baseChanged := commitChanged || treeChanged

		if req.BranchAction == BranchSwitchDiscard {
			if !refChanged || !loaded.proposal {
				return ErrBranchSwitchDiscardNotApplicable
			}
			if loaded.eligible != nil {
				matching, matchErr := matchObservedMaterialization(loaded, observed)
				if matchErr != nil {
					return fmt.Errorf("%w: %v", ErrGitMaterializationPrecondition, matchErr)
				}
				if _, err := proveObservedMaterializationCandidate(loaded, matching, observed); err != nil {
					return fmt.Errorf("%w: %v", ErrGitMaterializationPrecondition, err)
				}
				return ErrBranchSwitchDiscardNotApplicable
			}
			receiptJSON, err := encodeDiscardReceipt(result)
			if err != nil {
				return err
			}
			attempted = cloneObserveGitBaseResult(result)
			confirmableDiscard = true
			if err := tx.TransitionOperations(ctx, loaded.discardRows, "discarded", nil); err != nil {
				return err
			}
			if err := tx.DeleteCandidate(ctx, loaded.candidate != nil); err != nil {
				return err
			}
			if _, err := tx.ReplaceOpenConflictOccurrences(ctx, []localstore.WorkspaceConflictEvidence{}, mutationTime); err != nil {
				return err
			}
			if err := tx.InsertTransitionReceipt(ctx, localstore.WorkspaceTransitionReceiptInsert{
				RequestID: req.RequestID, Action: "discard", RequestDigest: requestDigest,
				Actor: req.Actor, ResultJSON: receiptJSON, Outcome: "clean",
			}); err != nil {
				return err
			}
			_, err = tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
				Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
				ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: "clean",
			})
			return err
		}

		if refChanged {
			if loaded.proposal {
				return ErrBranchSwitchPending
			}
			_, err := tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
				Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
				ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: "clean",
			})
			if err == nil {
				attempted = cloneObserveGitBaseResult(result)
			}
			return err
		}

		if loaded.eligible != nil {
			matching, matchErr := matchObservedMaterialization(loaded, observed)
			if matchErr == nil {
				laterActive, err := proveObservedMaterializationCandidate(loaded, matching, observed)
				if err != nil {
					return fmt.Errorf("%w: %v", ErrGitMaterializationPrecondition, err)
				}
				if _, err := tx.AcceptMaterialization(ctx, *loaded.eligible); err != nil {
					return err
				}
				if err := tx.DeleteCandidate(ctx, true); err != nil {
					return err
				}
				if _, err := tx.ReplaceOpenConflictOccurrences(ctx, []localstore.WorkspaceConflictEvidence{}, mutationTime); err != nil {
					return err
				}
				nextState := "clean"
				if len(laterActive) != 0 {
					nextState = "pending"
				}
				if _, err := tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
					Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
					ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: nextState,
				}); err != nil {
					return err
				}
				journalID := matching.journalID
				result.CandidateAccepted = true
				result.AcceptedJournalID = &journalID
				attempted = cloneObserveGitBaseResult(result)
				return nil
			}
			if baseChanged {
				return fmt.Errorf("%w: %v", ErrGitMaterializationPrecondition, matchErr)
			}
		}
		if !baseChanged {
			attempted = cloneObserveGitBaseResult(result)
			return nil
		}
		if !loaded.proposal {
			_, err := tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
				Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
				ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: "clean",
			})
			if err == nil {
				attempted = cloneObserveGitBaseResult(result)
			}
			return err
		}

		current, err := composeObserveGitBaseProposal(loaded)
		if err != nil {
			return err
		}
		merged, err := ThreeWayRebase(loaded.workspace.Snapshot, observed.snapshot, current.Snapshot)
		if err != nil {
			return err
		}
		evidence, err := encodeWorkspaceConflictEvidence(merged.Conflicts)
		if err != nil {
			return err
		}
		direct := loaded.workspace.Snapshot
		workingTreeDigest := loaded.workspace.Snapshot.Digest
		importedBy := types.CandidateImportOriginGitObservationRebaseV1
		importedAt := mutationTime
		if loaded.candidate != nil {
			direct = loaded.candidate.DirectSnapshot
			workingTreeDigest = loaded.candidate.WorkingTreeDigest
			importedBy = loaded.candidate.ImportedBy
			importedAt = loaded.candidate.ImportedAt
		}
		direct, err = cloneImportSnapshot(direct)
		if err != nil {
			return err
		}
		rebased, err := cloneImportSnapshot(merged.Snapshot)
		if err != nil {
			return err
		}
		nextState := "pending"
		if len(merged.Conflicts) != 0 {
			nextState = "conflicted"
		}
		if _, err := tx.AdvanceAcceptedBase(ctx, localstore.WorkspaceAcceptedBaseTransition{
			Expected: loaded.workspace, ObservedRef: observed.acceptedRef,
			ObservedCommitSHA: observed.commit, ObservedTree: observed.tree, NextState: nextState,
		}); err != nil {
			return err
		}
		if err := tx.UpsertCandidate(ctx, localstore.WorkspaceCandidateRecord{
			AcceptedBaseDigest: observed.snapshot.Digest, WorkingTreeDigest: workingTreeDigest,
			DirectSnapshot: direct, RebasedSnapshot: &rebased,
			RebasedThroughGeneration: current.ThroughGeneration,
			ImportedBy:               importedBy, ImportedAt: importedAt,
		}); err != nil {
			return err
		}
		if err := tx.TransitionOperations(ctx, loaded.activeRows, "rebased", nil); err != nil {
			return err
		}
		if _, err := tx.ReplaceOpenConflictOccurrences(ctx, evidence, mutationTime); err != nil {
			return err
		}
		result.Rebased = true
		result.Conflicts = cloneImportConflicts(merged.Conflicts)
		attempted = cloneObserveGitBaseResult(result)
		return nil
	}

	if req.BranchAction == BranchSwitchDiscard {
		withTransition := s.withImmediateWorkspaceTransition
		if withTransition == nil {
			withTransition = s.repo.WithImmediateWorkspaceTransition
		}
		err = withTransition(ctx, req.Scope, req.RequestID,
			func(tx *localstore.WorkspaceMutationTx, receipt *localstore.WorkspaceTransitionReceiptRecord) error {
				if receipt != nil {
					decoded, err := decodeExistingDiscardReceipt(receipt, req, requestDigest)
					if err != nil {
						return err
					}
					attempted = cloneObserveGitBaseResult(decoded)
					confirmableDiscard = true
					return nil
				}
				return mutate(tx)
			})
	} else {
		err = s.repo.WithImmediateWorkspace(ctx, req.Scope, mutate)
	}
	if err == nil {
		return cloneObserveGitBaseResult(attempted), nil
	}
	if req.BranchAction != BranchSwitchDiscard || !confirmableDiscard || !errors.Is(err, localstore.ErrCommitOutcomeUnknown) {
		return ObserveGitBaseResult{}, err
	}
	return confirmDiscardCommit(ctx, s.repo, req, requestDigest, attempted, err)
}

// RefreshWorkspace independently resolves the current checkout position and
// delegates the actual reconciliation to the zero-actor Reject path.
func (s *Service) RefreshWorkspace(ctx context.Context, binding types.WorkspaceBinding) (types.WorkspaceBinding, error) {
	if err := binding.Validate(); err != nil {
		return types.WorkspaceBinding{}, err
	}
	if s == nil || s.repo == nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	position, err := readGitBasePosition(ctx, binding.Checkout.CanonicalPath)
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	if position.root != binding.Checkout.CanonicalPath || position.checkout != binding.Checkout {
		return types.WorkspaceBinding{}, fmt.Errorf("projectstate: refreshed checkout differs from binding")
	}
	observed, err := s.ObserveGitBase(ctx, ObserveGitBaseRequest{
		Scope: binding.Scope, ExpectedBinding: binding, Root: position.root,
		ExpectedCommit: position.commit, BranchAction: BranchSwitchReject,
	})
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	workspace, err := s.repo.Workspace(ctx, binding.Scope)
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	if workspace.Binding.Scope != binding.Scope || workspace.Binding.Checkout != binding.Checkout ||
		workspace.Binding.Repository != binding.Repository ||
		workspace.Binding.AcceptedRef != observed.ObservedRef ||
		workspace.Binding.AcceptedCommitSHA != observed.ObservedCommit ||
		workspace.Binding.AcceptedTreeDigest != string(observed.ObservedBaseDigest) {
		return types.WorkspaceBinding{}, fmt.Errorf("projectstate: refreshed persisted binding differs from observed transition")
	}
	if err := verifyBindingCheckout(workspace.Binding); err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	return workspace.Binding, nil
}

func loadObserveGitBaseState(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	req ObserveGitBaseRequest,
) (observeGitBaseState, error) {
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return observeGitBaseState{}, err
	}
	workspace, err = cloneImportWorkspace(workspace)
	if err != nil {
		return observeGitBaseState{}, err
	}
	if workspace.Binding != req.ExpectedBinding || workspace.Binding.Scope != req.Scope {
		return observeGitBaseState{}, fmt.Errorf("projectstate: current Git binding differs from complete expected binding")
	}

	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return observeGitBaseState{}, err
	}
	candidate, err = cloneImportCandidate(candidate)
	if err != nil {
		return observeGitBaseState{}, err
	}

	openConflicts, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return observeGitBaseState{}, err
	}
	openConflicts = cloneImportOccurrences(openConflicts)
	if _, err := decodeWorkspaceConflictOccurrences(openConflicts); err != nil {
		return observeGitBaseState{}, err
	}
	if (workspace.State == "conflicted") != (len(openConflicts) != 0) {
		return observeGitBaseState{}, fmt.Errorf("projectstate: workspace conflict state does not match open conflict evidence")
	}

	disposition, proof, eligible, err := loadCurrentMaterializationDisposition(ctx, tx)
	if err != nil {
		return observeGitBaseState{}, err
	}
	currentRows := make([]localstore.WorkspaceOperation, 0, len(disposition.Operations))
	for _, row := range disposition.Operations {
		if row.State == "active" || row.State == "rebased" {
			currentRows = append(currentRows, cloneImportOperation(row))
		}
	}
	audit, err := validateRestoreOperationRows(currentRows)
	if err != nil {
		return observeGitBaseState{}, fmt.Errorf("projectstate: invalid current Git observation operations: %w", err)
	}
	if eligible != nil && candidate == nil {
		return observeGitBaseState{}, fmt.Errorf("projectstate: acceptance-eligible materialization has no candidate")
	}

	priorSurface := workspace.Snapshot
	if candidate != nil {
		priorSurface = candidate.DirectSnapshot
	}
	materializationPriorTree, err := state.EncodeTree(priorSurface)
	if err != nil {
		return observeGitBaseState{}, fmt.Errorf("projectstate: encode materialization prior surface: %w", err)
	}
	materializationPriorTree = cloneCheckpointTree(materializationPriorTree)
	activeRows := make([]localstore.WorkspaceOperation, 0)
	discardRows := make([]localstore.WorkspaceOperation, 0)
	proposalOperation := false
	for _, audited := range audit {
		row := audited.row
		switch row.State {
		case "active":
			proposalOperation = true
			activeRows = append(activeRows, cloneImportOperation(row))
			discardRows = append(discardRows, cloneImportOperation(row))
		case "rebased":
			proposalOperation = true
			discardRows = append(discardRows, cloneImportOperation(row))
		}
	}
	proposal := candidate != nil || proposalOperation || len(openConflicts) != 0
	switch workspace.State {
	case "clean":
		if proposal {
			return observeGitBaseState{}, fmt.Errorf("projectstate: clean workspace retains proposal state")
		}
	case "pending":
		if !proposal || len(openConflicts) != 0 {
			return observeGitBaseState{}, fmt.Errorf("projectstate: pending workspace proposal state is incomplete")
		}
	case "conflicted":
		if !proposal || len(openConflicts) == 0 {
			return observeGitBaseState{}, fmt.Errorf("projectstate: conflicted workspace proposal state is incomplete")
		}
	default:
		return observeGitBaseState{}, fmt.Errorf("projectstate: invalid workspace state %q", workspace.State)
	}

	return observeGitBaseState{
		workspace: workspace, candidate: candidate, audit: audit,
		openConflicts: openConflicts, dispositionProof: proof,
		eligible: eligible, materializationPriorTree: materializationPriorTree, activeRows: activeRows,
		discardRows: discardRows, proposal: proposal,
	}, nil
}

func composeObserveGitBaseProposal(loaded observeGitBaseState) (ComposedView, error) {
	start, boundary := selectCandidateStart(loaded.workspace.Snapshot, loaded.candidate)
	for _, audited := range loaded.audit {
		switch audited.row.State {
		case "active":
			if audited.row.Generation <= boundary {
				return ComposedView{}, fmt.Errorf("projectstate: active operation does not exceed selected candidate boundary")
			}
		case "rebased":
			if audited.row.Generation > boundary {
				return ComposedView{}, fmt.Errorf("projectstate: rebased operation exceeds selected candidate boundary")
			}
		}
	}
	operations, err := decodeStoredOperations(loaded.activeRows)
	if err != nil {
		return ComposedView{}, err
	}
	current, err := Compose(start, boundary, operations)
	if err != nil {
		return ComposedView{}, fmt.Errorf("projectstate: compose current Git observation proposal: %w", err)
	}
	return current, nil
}

func equalObserveGitBaseOperation(left, right localstore.WorkspaceOperation) bool {
	if left.Generation != right.Generation || left.OperationID != right.OperationID ||
		left.State != right.State || !bytes.Equal(left.OperationJSON, right.OperationJSON) {
		return false
	}
	if left.StashedByStashID == nil || right.StashedByStashID == nil {
		return left.StashedByStashID == nil && right.StashedByStashID == nil
	}
	return *left.StashedByStashID == *right.StashedByStashID
}

func newObserveGitBaseResult(workspace localstore.WorkspaceRecord, observed gitBaseObservation) ObserveGitBaseResult {
	return ObserveGitBaseResult{
		PreviousCommit: workspace.Binding.AcceptedCommitSHA, ObservedCommit: observed.commit,
		PreviousRef: workspace.Binding.AcceptedRef, ObservedRef: observed.acceptedRef,
		PreviousBaseDigest: state.Digest(workspace.Binding.AcceptedTreeDigest),
		ObservedBaseDigest: observed.snapshot.Digest,
		Conflicts:          make([]Conflict, 0),
	}
}

func matchObservedMaterialization(loaded observeGitBaseState, observed gitBaseObservation) (matchingMaterializationProof, error) {
	return requireMatchingMaterialization(
		loaded.dispositionProof, loaded.eligible, loaded.workspace.Binding,
		loaded.materializationPriorTree, observed.tree, observed.snapshot.Digest,
	)
}

func proveObservedMaterializationCandidate(
	loaded observeGitBaseState,
	matching matchingMaterializationProof,
	observed gitBaseObservation,
) ([]localstore.WorkspaceOperation, error) {
	if loaded.candidate == nil {
		return nil, fmt.Errorf("projectstate: exact materialization has no candidate")
	}
	if _, err := decodeCheckpointOperations(matching.includedOperationsJSON); err != nil {
		return nil, err
	}
	start, boundary := selectCandidateStart(loaded.workspace.Snapshot, loaded.candidate)
	if boundary != matching.throughGeneration {
		return nil, fmt.Errorf("projectstate: exact materialization candidate boundary mismatch")
	}
	startTree, err := state.EncodeTree(start)
	if err != nil {
		return nil, err
	}
	if start.Digest != observed.snapshot.Digest || !equalCheckpointTree(startTree, observed.tree) {
		return nil, fmt.Errorf("projectstate: exact materialization candidate bytes mismatch")
	}
	laterActive := make([]localstore.WorkspaceOperation, 0)
	for _, audited := range loaded.audit {
		switch audited.row.State {
		case "active":
			if audited.row.Generation <= matching.throughGeneration {
				return nil, fmt.Errorf("projectstate: exact materialization left an active operation at or below its boundary")
			}
			laterActive = append(laterActive, cloneImportOperation(audited.row))
		case "rebased":
			if audited.row.Generation > matching.throughGeneration {
				return nil, fmt.Errorf("projectstate: exact materialization left a rebased operation above its boundary")
			}
		}
	}
	return laterActive, nil
}

func decodeExistingDiscardReceipt(
	receipt *localstore.WorkspaceTransitionReceiptRecord,
	req ObserveGitBaseRequest,
	requestDigest state.Digest,
) (ObserveGitBaseResult, error) {
	if receipt.Action != "discard" || receipt.RequestDigest != requestDigest {
		return ObserveGitBaseResult{}, fmt.Errorf("%w: discard request ID is already bound to another request", ErrIdempotencyConflict)
	}
	result, err := decodeDiscardReceipt(receipt, req, requestDigest)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}
	return cloneObserveGitBaseResult(result), nil
}

func confirmDiscardCommit(
	ctx context.Context,
	repo *localstore.WorkspaceRepo,
	req ObserveGitBaseRequest,
	requestDigest state.Digest,
	expected ObserveGitBaseResult,
	commitErr error,
) (ObserveGitBaseResult, error) {
	receipt, err := repo.TransitionReceiptByKey(ctx, req.Scope, req.RequestID)
	if err == nil && receipt != nil && receipt.Action == "discard" && receipt.RequestDigest == requestDigest {
		var decoded ObserveGitBaseResult
		decoded, err = decodeDiscardReceipt(receipt, req, requestDigest)
		if err == nil && reflect.DeepEqual(decoded, expected) {
			return cloneObserveGitBaseResult(decoded), nil
		}
	}
	if err == nil {
		err = fmt.Errorf("discard receipt is absent or differs from attempted transition")
	}
	return ObserveGitBaseResult{}, fmt.Errorf("%w: discard commit receipt confirmation failed: %v", commitErr, err)
}

func cloneObserveGitBaseResult(value ObserveGitBaseResult) ObserveGitBaseResult {
	cloned := value
	if value.AcceptedJournalID != nil {
		journalID := *value.AcceptedJournalID
		cloned.AcceptedJournalID = &journalID
	}
	if value.Conflicts != nil {
		cloned.Conflicts = cloneImportConflicts(value.Conflicts)
	}
	return cloned
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
