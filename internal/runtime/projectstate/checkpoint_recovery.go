package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrCheckpointRecoveryPrecondition = errors.New(
	"projectstate: checkpoint recovery precondition failed",
)

type checkpointRecoveryKind uint8

const (
	checkpointRecoveryNoWork checkpointRecoveryKind = iota + 1
	checkpointRecoveryPrepared
	checkpointRecoveryPublished
)

type checkpointRecoveryProof struct {
	kind        checkpointRecoveryKind
	workspace   localstore.WorkspaceRecord
	candidate   *localstore.WorkspaceCandidateRecord
	disposition localstore.WorkspaceMaterializationDisposition
	driver      *localstore.WorkspaceMaterializationRecord
	operations  CheckpointOperationsV1
	status      WorkspaceStatus
}

type checkpointRecoveryGitReaders struct {
	position  func(context.Context, string) (gitBasePosition, error)
	committed func(context.Context, string, string) (committedWorkspace, error)
	origin    func(context.Context, string) (publicationOriginObservation, error)
}

type checkpointRecoveryGitObservation struct {
	position      gitBasePosition
	committed     committedWorkspace
	origin        publicationOriginObservation
	finalPosition gitBasePosition
}

func proveCheckpointRecoveryDisposition(
	workspace localstore.WorkspaceRecord,
	candidate *localstore.WorkspaceCandidateRecord,
	disposition localstore.WorkspaceMaterializationDisposition,
) (checkpointRecoveryProof, error) {
	if err := validateCheckpointRecoveryWorkspace(workspace); err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("workspace proof", err)
	}
	pending, err := checkpointPendingJournal(workspace.Binding, disposition)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("journal disposition", err)
	}
	ownedWorkspace, err := cloneImportWorkspace(workspace)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("clone workspace proof", err)
	}
	ownedCandidate, err := cloneImportCandidate(candidate)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("clone candidate proof", err)
	}
	ownedDisposition := cloneImportDisposition(disposition)

	if pending == nil {
		dispositionProof, err := proveMaterializationDisposition(disposition)
		if err != nil {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("terminal ownership", err)
		}
		if err := proveCheckpointTerminalHistory(workspace.Binding, disposition, dispositionProof); err != nil {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("terminal history", err)
		}
		return checkpointRecoveryProof{
			kind: checkpointRecoveryNoWork, workspace: ownedWorkspace,
			candidate: ownedCandidate, disposition: ownedDisposition,
		}, nil
	}

	operations, err := decodeCheckpointOperations(*pending.IncludedOperationsJSON)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("driver operation envelope", err)
	}
	terminalGenerations, terminalIDs, err := checkpointRecoveryTerminalClaims(disposition)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("terminal operation ownership", err)
	}
	if err := proveCheckpointRecoveryOperationOwnership(
		disposition, *pending, operations, terminalGenerations, terminalIDs,
	); err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("driver operation ownership", err)
	}

	kind := checkpointRecoveryPrepared
	switch pending.State {
	case "prepared":
		prior, err := checkpointRecoveryPriorCandidate(*pending)
		if err != nil {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("prepared candidate preimage", err)
		}
		if !equalCheckpointRecoveryCandidates(prior, candidate) {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("prepared candidate preimage differs", nil)
		}
	case "published", "recovered_new":
		postimage, err := checkpointPublicationPostimage(*pending)
		if err != nil {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("publication postimage", err)
		}
		if !equalCheckpointRecoveryCandidates(&postimage, candidate) {
			return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("publication candidate postimage differs", nil)
		}
		if pending.State == "recovered_new" {
			kind = checkpointRecoveryNoWork
		} else {
			kind = checkpointRecoveryPublished
		}
	default:
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("invalid recovery driver state", nil)
	}

	proof := checkpointRecoveryProof{
		kind: kind, workspace: ownedWorkspace, candidate: ownedCandidate,
		disposition: ownedDisposition, operations: cloneCheckpointOperations(operations),
	}
	if kind != checkpointRecoveryNoWork {
		driver := cloneMaterializationRecord(*pending)
		proof.driver = &driver
	}
	return proof, nil
}

func loadCheckpointRecoveryDisposition(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
) (checkpointRecoveryProof, error) {
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("read workspace", err)
	}
	disposition, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("read journal disposition", err)
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("read candidate", err)
	}
	proof, err := proveCheckpointRecoveryDisposition(workspace, candidate, disposition)
	if err != nil {
		return checkpointRecoveryProof{}, err
	}
	if proof.kind != checkpointRecoveryNoWork {
		return proof, nil
	}
	composed, err := loadComposedWorkspaceRecord(ctx, tx, workspace)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("compose terminal status", err)
	}
	status, err := clonePublicationReviewStatus(composed.status)
	if err != nil {
		return checkpointRecoveryProof{}, checkpointRecoveryPrecondition("clone terminal status", err)
	}
	status.PublicationClassification = ""
	status.PublicationReviewDigest = ""
	proof.status = status
	return proof, nil
}

func validateCheckpointRecoveryWorkspace(workspace localstore.WorkspaceRecord) error {
	if err := workspace.Binding.Validate(); err != nil {
		return err
	}
	tree, err := state.EncodeTree(workspace.Snapshot)
	if err != nil {
		return err
	}
	return validateMatchingTree(tree, state.Digest(workspace.Binding.AcceptedTreeDigest), workspace.Binding)
}

func checkpointRecoveryTerminalClaims(
	disposition localstore.WorkspaceMaterializationDisposition,
) (map[int64]CheckpointOperationV1, map[string]struct{}, error) {
	generations := make(map[int64]CheckpointOperationV1)
	operationIDs := make(map[string]struct{})
	for _, journal := range disposition.Journals {
		if journal.State != "accepted" || journal.IncludedOperationsJSON == nil {
			continue
		}
		envelope, err := decodeCheckpointOperations(*journal.IncludedOperationsJSON)
		if err != nil {
			return nil, nil, err
		}
		for _, operation := range envelope.Operations {
			if _, duplicate := generations[operation.Generation]; duplicate {
				return nil, nil, fmt.Errorf("duplicate terminal operation generation")
			}
			if _, duplicate := operationIDs[operation.OperationID]; duplicate {
				return nil, nil, fmt.Errorf("duplicate terminal operation ID")
			}
			generations[operation.Generation] = operation
			operationIDs[operation.OperationID] = struct{}{}
		}
	}
	return generations, operationIDs, nil
}

func proveCheckpointRecoveryOperationOwnership(
	disposition localstore.WorkspaceMaterializationDisposition,
	driver localstore.WorkspaceMaterializationRecord,
	envelope CheckpointOperationsV1,
	terminalGenerations map[int64]CheckpointOperationV1,
	terminalIDs map[string]struct{},
) error {
	rowsByGeneration := make(map[int64]localstore.WorkspaceOperation, len(disposition.Operations))
	rowIDs := make(map[string]struct{}, len(disposition.Operations))
	for _, operation := range disposition.Operations {
		if _, duplicate := rowsByGeneration[operation.Generation]; duplicate {
			return fmt.Errorf("duplicate operation generation")
		}
		if _, duplicate := rowIDs[operation.OperationID]; duplicate {
			return fmt.Errorf("duplicate operation ID")
		}
		rowsByGeneration[operation.Generation] = operation
		rowIDs[operation.OperationID] = struct{}{}
	}
	driverGenerations := make(map[int64]struct{}, len(envelope.Operations))
	for _, claim := range envelope.Operations {
		if _, duplicate := terminalGenerations[claim.Generation]; duplicate {
			return fmt.Errorf("driver and terminal history share an operation generation")
		}
		if _, duplicate := terminalIDs[claim.OperationID]; duplicate {
			return fmt.Errorf("driver and terminal history share an operation ID")
		}
		row, ok := rowsByGeneration[claim.Generation]
		if !ok || row.OperationID != claim.OperationID || !bytes.Equal(row.OperationJSON, []byte(claim.OperationJSON)) ||
			row.StashedByStashID != nil {
			return fmt.Errorf("driver operation differs from persisted row")
		}
		if driver.State == "prepared" {
			if row.State != claim.PrepublicationState {
				return fmt.Errorf("prepared operation left its recorded state")
			}
		} else if row.State != "materialized" {
			return fmt.Errorf("published operation is not materialized")
		}
		driverGenerations[claim.Generation] = struct{}{}
	}
	if err := proveJournalPrepublicationMembership(driver, driverGenerations, disposition.Operations); err != nil {
		return err
	}
	for _, operation := range disposition.Operations {
		if operation.State != "materialized" {
			continue
		}
		if _, terminal := terminalGenerations[operation.Generation]; terminal {
			continue
		}
		if driver.State != "prepared" {
			if _, owned := driverGenerations[operation.Generation]; owned {
				continue
			}
		}
		return fmt.Errorf("orphan or prepared-owned materialized operation")
	}
	return nil
}

func checkpointRecoveryPriorCandidate(
	journal localstore.WorkspaceMaterializationRecord,
) (*localstore.WorkspaceCandidateRecord, error) {
	prior, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
	if err != nil {
		return nil, err
	}
	if prior.Candidate == nil {
		return nil, nil
	}
	candidate := prior.Candidate
	direct, err := validateCheckpointPriorTree(candidate.DirectTree, checkpointPriorTreeProductionLimits())
	if err != nil {
		return nil, err
	}
	result := &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest: candidate.AcceptedBaseDigest, WorkingTreeDigest: candidate.WorkingTreeDigest,
		DirectSnapshot: direct, RebasedThroughGeneration: candidate.RebasedThroughGeneration,
		ImportedBy: candidate.ImportedBy, ImportedAt: candidate.ImportedAt,
	}
	if candidate.RebasedTree != nil {
		rebased, err := validateCheckpointPriorTree(*candidate.RebasedTree, checkpointPriorTreeProductionLimits())
		if err != nil {
			return nil, err
		}
		result.RebasedSnapshot = &rebased
	}
	return result, nil
}

func equalCheckpointRecoveryCandidates(left, right *localstore.WorkspaceCandidateRecord) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.AcceptedBaseDigest != right.AcceptedBaseDigest || left.WorkingTreeDigest != right.WorkingTreeDigest ||
		left.RebasedThroughGeneration != right.RebasedThroughGeneration || left.ImportedBy != right.ImportedBy ||
		!left.ImportedAt.Equal(right.ImportedAt) {
		return false
	}
	leftDirect, leftErr := state.EncodeTree(left.DirectSnapshot)
	rightDirect, rightErr := state.EncodeTree(right.DirectSnapshot)
	if leftErr != nil || rightErr != nil || !equalCheckpointTree(leftDirect, rightDirect) {
		return false
	}
	if left.RebasedSnapshot == nil || right.RebasedSnapshot == nil {
		return left.RebasedSnapshot == nil && right.RebasedSnapshot == nil
	}
	leftRebased, leftErr := state.EncodeTree(*left.RebasedSnapshot)
	rightRebased, rightErr := state.EncodeTree(*right.RebasedSnapshot)
	return leftErr == nil && rightErr == nil && equalCheckpointTree(leftRebased, rightRebased)
}

func observeCheckpointRecoveryGit(
	ctx context.Context,
	proof checkpointRecoveryProof,
) (checkpointRecoveryGitObservation, error) {
	return observeCheckpointRecoveryGitWithReaders(ctx, proof, checkpointRecoveryGitReaders{
		position: readGitBasePosition, committed: inspectCommittedWorkspaceForGitBase,
		origin: observePublicationOrigin,
	})
}

func observeCheckpointRecoveryGitWithReaders(
	ctx context.Context,
	proof checkpointRecoveryProof,
	readers checkpointRecoveryGitReaders,
) (checkpointRecoveryGitObservation, error) {
	if (proof.kind != checkpointRecoveryPrepared && proof.kind != checkpointRecoveryPublished) || proof.driver == nil ||
		readers.position == nil || readers.committed == nil || readers.origin == nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("Git observation proof or reader unavailable", nil)
	}
	binding := proof.workspace.Binding
	position, err := readers.position(ctx, binding.Checkout.CanonicalPath)
	if err != nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("read initial Git position", err)
	}
	if !validCheckpointRecoveryPosition(position) || position.root != binding.Checkout.CanonicalPath ||
		position.checkout != binding.Checkout || position.acceptedRef != binding.AcceptedRef {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("initial Git position differs from binding", nil)
	}
	committed, err := readers.committed(ctx, position.root, position.commit)
	if err != nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("read committed Wormhole tree", err)
	}
	gitObservation := gitBaseObservation{
		root: committed.root, checkout: committed.checkout, acceptedRef: committed.acceptedRef,
		commit: committed.commit, tree: committed.tree, snapshot: committed.snapshot,
	}
	if err := validatePublicationGitObservation(gitObservation); err != nil || committed.root != position.root ||
		committed.checkout != position.checkout || committed.acceptedRef != position.acceptedRef || committed.commit != position.commit ||
		committed.snapshot.Config.ProjectID != binding.Scope.ProjectID || committed.snapshot.Config.Repository != binding.Repository {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("committed Git observation differs", err)
	}
	if position.commit == binding.AcceptedCommitSHA {
		acceptedTree, err := state.EncodeTree(proof.workspace.Snapshot)
		if err != nil || committed.snapshot.Digest != state.Digest(binding.AcceptedTreeDigest) ||
			!equalCheckpointTree(committed.tree, acceptedTree) {
			return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("stored Git base tree differs", err)
		}
	} else if committed.snapshot.Digest != proof.driver.CandidateDigest ||
		!equalCheckpointTree(committed.tree, proof.driver.CandidateTree) {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("same-ref Git candidate tree differs", nil)
	}

	origin, originErr := readers.origin(ctx, position.root)
	finalPosition, finalErr := readers.position(ctx, position.root)
	if finalErr != nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("read final Git position", finalErr)
	}
	if !validCheckpointRecoveryPosition(finalPosition) || finalPosition != position {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("Git position changed across recovery observation", nil)
	}
	if originErr != nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("observe recovery origin", originErr)
	}
	if err := validatePublicationOriginObservation(origin); err != nil || origin.root != position.root || origin.checkout != position.checkout {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("recovery origin differs or is malformed", err)
	}

	ownedTree := cloneCheckpointTree(committed.tree)
	ownedSnapshot, err := state.DecodeTree(ownedTree)
	if err != nil {
		return checkpointRecoveryGitObservation{}, checkpointRecoveryPrecondition("clone committed recovery observation", err)
	}
	committed.tree = ownedTree
	committed.snapshot = ownedSnapshot
	return checkpointRecoveryGitObservation{
		position: position, committed: committed, origin: origin, finalPosition: finalPosition,
	}, nil
}

func validCheckpointRecoveryPosition(position gitBasePosition) bool {
	return filepath.IsAbs(position.root) && filepath.Clean(position.root) == position.root &&
		position.checkout.CanonicalPath == position.root && position.checkout.Device != 0 && position.checkout.Inode != 0 &&
		validDiscardRef(position.acceptedRef) && validCommit(position.commit)
}

func checkpointRecoveryPrecondition(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCheckpointRecoveryPrecondition, message)
	}
	return fmt.Errorf("%w: %s: %w", ErrCheckpointRecoveryPrecondition, message, cause)
}
