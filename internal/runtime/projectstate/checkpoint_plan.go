package projectstate

import (
	"bytes"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type checkpointPlanInput struct {
	Binding       types.WorkspaceBinding
	Current       *localstore.WorkspaceCandidateRecord
	Composed      composedWorkspace
	Disposition   localstore.WorkspaceMaterializationDisposition
	Review        publicationReviewTransactionEvidence
	PriorLiveTree state.Tree
	Actor         types.ActorEnvelope
}

type checkpointPlan struct {
	PriorTree               state.Tree
	PriorTreeDigest         state.Digest
	CandidateTree           state.Tree
	CandidateDigest         state.Digest
	ThroughGeneration       int64
	IncludedOperations      []localstore.WorkspaceOperation
	IncludedOperationsJSON  string
	PriorCandidateJSON      string
	PublicationReviewJSON   string
	PublicationReviewDigest state.Digest
}

func proveCheckpointPlan(input checkpointPlanInput) (checkpointPlan, error) {
	acceptedTree, err := proveCheckpointWorkspace(input)
	if err != nil {
		return checkpointPlan{}, err
	}
	dispositionProof, err := proveMaterializationDisposition(input.Disposition)
	if err != nil {
		return checkpointPlan{}, fmt.Errorf("projectstate: checkpoint materialization disposition: %w", err)
	}
	if err := proveCheckpointTerminalHistory(input.Binding, input.Disposition, dispositionProof); err != nil {
		return checkpointPlan{}, err
	}

	priorCandidateJSON, selectedStart, boundary, err := proveCheckpointPriorCandidate(input.Binding, input.Current, acceptedTree)
	if err != nil {
		return checkpointPlan{}, err
	}
	activeRows, err := proveCheckpointActiveRows(input.Disposition, boundary)
	if err != nil {
		return checkpointPlan{}, err
	}
	activeOperations, err := decodeStoredOperations(activeRows)
	if err != nil {
		return checkpointPlan{}, fmt.Errorf("projectstate: checkpoint active operations: %w", err)
	}
	provedView, candidateTree, err := proveCheckpointComposition(input.Composed, input.Review.composed, selectedStart, boundary, activeOperations)
	if err != nil {
		return checkpointPlan{}, err
	}
	selected, selectedActiveRows, operationsEnvelope, err := proveCheckpointOperationSelection(input.Disposition, boundary)
	if err != nil {
		return checkpointPlan{}, err
	}
	if !checkpointOperationRowsEqual(activeRows, selectedActiveRows) {
		return checkpointPlan{}, fmt.Errorf("projectstate: checkpoint active-operation selection changed across proof")
	}
	throughGeneration := boundary
	if len(selected) != 0 && selected[len(selected)-1].Generation > throughGeneration {
		throughGeneration = selected[len(selected)-1].Generation
	}
	if throughGeneration != provedView.ThroughGeneration {
		return checkpointPlan{}, fmt.Errorf("projectstate: checkpoint operation boundary differs from composition")
	}

	includedOperationsJSON, err := encodeCheckpointOperations(operationsEnvelope)
	if err != nil {
		return checkpointPlan{}, err
	}
	priorTree, priorTreeDigest, err := proveCheckpointPriorLiveTree(input.PriorLiveTree, input.Binding)
	if err != nil {
		return checkpointPlan{}, err
	}
	publicationReviewJSON, publicationReviewDigest, err := proveCheckpointPublicationReview(
		input, acceptedTree, selectedStart, boundary, activeOperations, provedView,
	)
	if err != nil {
		return checkpointPlan{}, err
	}
	if err := proveCheckpointOutgoingEnvelopes(priorCandidateJSON, includedOperationsJSON, publicationReviewJSON); err != nil {
		return checkpointPlan{}, err
	}

	return checkpointPlan{
		PriorTree:               cloneCheckpointTree(priorTree),
		PriorTreeDigest:         priorTreeDigest,
		CandidateTree:           cloneCheckpointTree(candidateTree),
		CandidateDigest:         provedView.Snapshot.Digest,
		ThroughGeneration:       throughGeneration,
		IncludedOperations:      cloneCheckpointPlanOperations(selected),
		IncludedOperationsJSON:  includedOperationsJSON,
		PriorCandidateJSON:      priorCandidateJSON,
		PublicationReviewJSON:   publicationReviewJSON,
		PublicationReviewDigest: publicationReviewDigest,
	}, nil
}

func proveCheckpointTerminalHistory(
	currentBinding types.WorkspaceBinding,
	disposition localstore.WorkspaceMaterializationDisposition,
	dispositionProof materializationDispositionProof,
) error {
	for _, journal := range disposition.Journals {
		if journal.State != "accepted" && journal.State != "recovered_old" {
			return fmt.Errorf("projectstate: checkpoint has non-terminal materialization history")
		}
		switch journal.PublicationReviewProofVersion {
		case 0:
			if journal.PublicationReviewJSON != nil || journal.PriorCandidateJSON != nil {
				return fmt.Errorf("projectstate: version-zero terminal history retains publication proof")
			}
			if journal.State == "accepted" {
				proved, ownsMaterializedRows := dispositionProof.journals[journal.JournalID]
				if ownsMaterializedRows && len(proved.envelope.Operations) != 0 {
					return fmt.Errorf("projectstate: version-zero accepted history retains materialized ownership")
				}
			}
			continue
		case 1:
			if journal.PublicationReviewJSON == nil || journal.PriorCandidateJSON == nil {
				return fmt.Errorf("projectstate: version-one terminal history has incomplete publication proof")
			}
		default:
			return fmt.Errorf("projectstate: terminal history has unknown publication proof version")
		}

		publication, err := decodeCheckpointPublicationReview(*journal.PublicationReviewJSON)
		if err != nil {
			return fmt.Errorf("projectstate: journal %q publication review: %w", journal.JournalID, err)
		}
		priorCandidate, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
		if err != nil {
			return fmt.Errorf("projectstate: journal %q prior candidate: %w", journal.JournalID, err)
		}
		review := publication.Review
		if review.Scope != currentBinding.Scope || review.Repository != currentBinding.Repository {
			return fmt.Errorf("projectstate: journal %q publication review workspace identity differs", journal.JournalID)
		}
		if review.AcceptedTreeDigest != journal.AcceptedBaseDigest || review.CandidateTreeDigest != journal.CandidateDigest ||
			review.OverlayGeneration != journal.ThroughGeneration {
			return fmt.Errorf("projectstate: journal %q publication review boundary differs", journal.JournalID)
		}

		historicalBinding := types.WorkspaceBinding{
			Scope:              review.Scope,
			Checkout:           journal.Checkout,
			Repository:         review.Repository,
			AcceptedRef:        review.AcceptedRef,
			AcceptedCommitSHA:  review.AcceptedCommitSHA,
			AcceptedTreeDigest: string(review.AcceptedTreeDigest),
		}
		if err := historicalBinding.Validate(); err != nil {
			return fmt.Errorf("projectstate: journal %q historical binding: %w", journal.JournalID, err)
		}
		if journal.ExpectedLiveDigest != journal.PriorTreeDigest {
			return fmt.Errorf("projectstate: journal %q prior digest differs", journal.JournalID)
		}
		if err := validateMatchingTree(journal.PriorTree, journal.PriorTreeDigest, historicalBinding); err != nil {
			return fmt.Errorf("projectstate: journal %q prior tree: %w", journal.JournalID, err)
		}
		if err := validateMatchingTree(journal.CandidateTree, journal.CandidateDigest, historicalBinding); err != nil {
			return fmt.Errorf("projectstate: journal %q candidate tree: %w", journal.JournalID, err)
		}

		boundary := int64(0)
		if priorCandidate.Candidate != nil {
			candidate := priorCandidate.Candidate
			if candidate.AcceptedBaseDigest != journal.AcceptedBaseDigest {
				return fmt.Errorf("projectstate: journal %q prior candidate accepted base differs", journal.JournalID)
			}
			direct, err := validateCheckpointPriorTree(candidate.DirectTree, checkpointPriorTreeProductionLimits())
			if err != nil {
				return fmt.Errorf("projectstate: journal %q direct prior candidate: %w", journal.JournalID, err)
			}
			if direct.Config.ProjectID != historicalBinding.Scope.ProjectID || direct.Config.Repository != historicalBinding.Repository {
				return fmt.Errorf("projectstate: journal %q direct prior candidate identity differs", journal.JournalID)
			}
			boundary = candidate.RebasedThroughGeneration
		}
		if journal.State == "accepted" {
			if proved, hasOperationProof := dispositionProof.journals[journal.JournalID]; hasOperationProof &&
				boundary != proved.envelope.InitialThroughGeneration {
				return fmt.Errorf("projectstate: journal %q prior candidate operation boundary differs", journal.JournalID)
			}
		}
	}
	return nil
}

func proveCheckpointWorkspace(input checkpointPlanInput) (state.Tree, error) {
	if err := input.Binding.Validate(); err != nil {
		return nil, fmt.Errorf("projectstate: invalid checkpoint binding: %w", err)
	}
	if err := input.Actor.ValidateLocalAction(); err != nil {
		return nil, fmt.Errorf("projectstate: invalid checkpoint actor: %w", err)
	}
	if input.Composed.status.Binding != input.Binding || input.Review.workspace.Binding != input.Binding ||
		input.Review.composed.status.Binding != input.Binding || input.Review.status.Binding != input.Binding {
		return nil, fmt.Errorf("projectstate: checkpoint bindings differ")
	}
	stateName := input.Composed.status.State
	if (stateName != "clean" && stateName != "pending") || input.Review.workspace.State != stateName ||
		input.Review.composed.status.State != stateName || input.Review.status.State != stateName {
		return nil, fmt.Errorf("projectstate: checkpoint workspace state is not prepare-ready and consistent")
	}

	snapshots := []state.Snapshot{
		input.Composed.status.AcceptedSnapshot,
		input.Review.workspace.Snapshot,
		input.Review.composed.status.AcceptedSnapshot,
		input.Review.status.AcceptedSnapshot,
	}
	var acceptedTree state.Tree
	for index, snapshot := range snapshots {
		tree, err := checkpointSnapshotTree(snapshot)
		if err != nil {
			return nil, fmt.Errorf("projectstate: checkpoint accepted snapshot %d: %w", index, err)
		}
		if err := validateMatchingTree(tree, state.Digest(input.Binding.AcceptedTreeDigest), input.Binding); err != nil {
			return nil, fmt.Errorf("projectstate: checkpoint accepted snapshot %d: %w", index, err)
		}
		if index == 0 {
			acceptedTree = tree
		} else if !equalCheckpointTree(acceptedTree, tree) {
			return nil, fmt.Errorf("projectstate: checkpoint accepted snapshots differ")
		}
	}
	return cloneCheckpointTree(acceptedTree), nil
}

func proveCheckpointPriorCandidate(
	binding types.WorkspaceBinding,
	current *localstore.WorkspaceCandidateRecord,
	acceptedTree state.Tree,
) (string, state.Snapshot, int64, error) {
	accepted, err := state.DecodeTree(cloneCheckpointTree(acceptedTree))
	if err != nil {
		return "", state.Snapshot{}, 0, err
	}
	envelope := checkpointPriorCandidateV1{SchemaVersion: 1, Kind: "checkpoint_prior_candidate"}
	selectedStart := accepted
	var boundary int64
	if current != nil {
		if current.AcceptedBaseDigest != state.Digest(binding.AcceptedTreeDigest) {
			return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint prior candidate accepted base differs")
		}
		directTree, directSnapshot, err := proveCheckpointCandidateSnapshot(current.DirectSnapshot, binding)
		if err != nil {
			return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint direct candidate: %w", err)
		}
		if current.WorkingTreeDigest != directSnapshot.Digest {
			return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint direct candidate digest differs")
		}
		candidate := &checkpointPriorCandidateStateV1{
			AcceptedBaseDigest: current.AcceptedBaseDigest, WorkingTreeDigest: current.WorkingTreeDigest,
			DirectTree:               checkpointPriorTree(directTree, directSnapshot.Digest),
			RebasedThroughGeneration: current.RebasedThroughGeneration,
			ImportedBy:               current.ImportedBy, ImportedAt: current.ImportedAt,
		}
		selectedStart = directSnapshot
		if current.RebasedSnapshot != nil {
			rebasedTree, rebasedSnapshot, err := proveCheckpointCandidateSnapshot(*current.RebasedSnapshot, binding)
			if err != nil {
				return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint rebased candidate: %w", err)
			}
			rebased := checkpointPriorTree(rebasedTree, rebasedSnapshot.Digest)
			candidate.RebasedTree = &rebased
			selectedStart = rebasedSnapshot
			boundary = current.RebasedThroughGeneration
		} else if current.RebasedThroughGeneration != 0 {
			return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint candidate has boundary without rebased tree")
		}
		envelope.Candidate = candidate
	}

	raw, err := encodeCheckpointPriorCandidate(envelope)
	if err != nil {
		return "", state.Snapshot{}, 0, err
	}
	decoded, err := decodeCheckpointPriorCandidate(raw)
	if err != nil {
		return "", state.Snapshot{}, 0, err
	}
	reencoded, err := encodeCheckpointPriorCandidate(decoded)
	if err != nil || reencoded != raw {
		if err != nil {
			return "", state.Snapshot{}, 0, err
		}
		return "", state.Snapshot{}, 0, fmt.Errorf("projectstate: checkpoint prior candidate round trip differs")
	}
	return raw, selectedStart, boundary, nil
}

func proveCheckpointCandidateSnapshot(snapshot state.Snapshot, binding types.WorkspaceBinding) (state.Tree, state.Snapshot, error) {
	tree, err := checkpointSnapshotTree(snapshot)
	if err != nil {
		return nil, state.Snapshot{}, err
	}
	if err := validateMatchingTree(tree, snapshot.Digest, binding); err != nil {
		return nil, state.Snapshot{}, err
	}
	decoded, err := state.DecodeTree(cloneCheckpointTree(tree))
	if err != nil {
		return nil, state.Snapshot{}, err
	}
	return tree, decoded, nil
}

func checkpointPriorTree(tree state.Tree, digest state.Digest) checkpointPriorTreeV1 {
	files := make([]checkpointPriorFileV1, len(tree))
	for index, file := range tree {
		files[index] = checkpointPriorFileV1{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return checkpointPriorTreeV1{Digest: digest, Files: files}
}

func proveCheckpointActiveRows(
	disposition localstore.WorkspaceMaterializationDisposition,
	boundary int64,
) ([]localstore.WorkspaceOperation, error) {
	activeRows := make([]localstore.WorkspaceOperation, 0)
	for _, row := range disposition.Operations {
		if row.State != "active" {
			continue
		}
		if row.StashedByStashID != nil || row.Generation <= boundary {
			return nil, fmt.Errorf("projectstate: invalid active checkpoint operation")
		}
		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode active checkpoint operation: %w", err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil || operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, fmt.Errorf("projectstate: active checkpoint operation identity or bytes differ")
		}
		activeRows = append(activeRows, cloneImportOperation(row))
	}
	return activeRows, nil
}

func proveCheckpointOperationSelection(
	disposition localstore.WorkspaceMaterializationDisposition,
	boundary int64,
) ([]localstore.WorkspaceOperation, []localstore.WorkspaceOperation, CheckpointOperationsV1, error) {
	selected := make([]localstore.WorkspaceOperation, 0)
	activeRows := make([]localstore.WorkspaceOperation, 0)
	envelope := CheckpointOperationsV1{
		SchemaVersion: 1, InitialThroughGeneration: boundary, Operations: make([]CheckpointOperationV1, 0),
	}
	for _, row := range disposition.Operations {
		include := false
		switch row.State {
		case "rebased":
			if row.StashedByStashID != nil || row.Generation > boundary {
				return nil, nil, CheckpointOperationsV1{}, fmt.Errorf("projectstate: invalid rebased checkpoint operation")
			}
			include = true
		case "active":
			if row.StashedByStashID != nil || row.Generation <= boundary {
				return nil, nil, CheckpointOperationsV1{}, fmt.Errorf("projectstate: invalid active checkpoint operation")
			}
			include = true
		case "stashed", "discarded", "materialized":
			continue
		default:
			return nil, nil, CheckpointOperationsV1{}, fmt.Errorf("projectstate: unknown checkpoint operation state")
		}
		if !include {
			continue
		}
		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, nil, CheckpointOperationsV1{}, fmt.Errorf("projectstate: decode selected checkpoint operation: %w", err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil || operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, nil, CheckpointOperationsV1{}, fmt.Errorf("projectstate: selected checkpoint operation identity or bytes differ")
		}
		cloned := cloneImportOperation(row)
		selected = append(selected, cloned)
		if row.State == "active" {
			activeRows = append(activeRows, cloneImportOperation(row))
		}
		envelope.Operations = append(envelope.Operations, CheckpointOperationV1{
			Generation: row.Generation, OperationID: row.OperationID,
			OperationJSON: string(row.OperationJSON), PrepublicationState: row.State,
		})
	}
	return selected, activeRows, envelope, nil
}

func proveCheckpointComposition(
	composed composedWorkspace,
	reviewComposed composedWorkspace,
	selectedStart state.Snapshot,
	boundary int64,
	activeOperations []StoredOperation,
) (ComposedView, state.Tree, error) {
	if composed.boundary != boundary || !checkpointSnapshotsEqual(composed.selectedStart, selectedStart) {
		return ComposedView{}, nil, fmt.Errorf("projectstate: checkpoint composed start or boundary differs")
	}
	if !checkpointStoredOperationsEqual(composed.operations, activeOperations) {
		return ComposedView{}, nil, fmt.Errorf("projectstate: checkpoint composed operations differ")
	}
	provedView, err := Compose(selectedStart, boundary, activeOperations)
	if err != nil {
		return ComposedView{}, nil, err
	}
	if !checkpointComposedViewEqual(composed.view, provedView) ||
		composed.status.CandidateDigest != provedView.Snapshot.Digest || composed.status.OverlayGeneration != provedView.ThroughGeneration {
		return ComposedView{}, nil, fmt.Errorf("projectstate: checkpoint composed result differs")
	}
	if !checkpointComposedWorkspacesEqual(composed, reviewComposed) {
		return ComposedView{}, nil, fmt.Errorf("projectstate: checkpoint review composition differs")
	}
	candidateTree, err := checkpointSnapshotTree(provedView.Snapshot)
	if err != nil {
		return ComposedView{}, nil, err
	}
	return provedView, cloneCheckpointTree(candidateTree), nil
}

func proveCheckpointPriorLiveTree(tree state.Tree, binding types.WorkspaceBinding) (state.Tree, state.Digest, error) {
	if tree == nil {
		return nil, "", fmt.Errorf("projectstate: checkpoint prior live tree is nil")
	}
	cloned := cloneCheckpointTree(tree)
	digest, err := state.DigestTree(cloned)
	if err != nil {
		return nil, "", fmt.Errorf("projectstate: digest checkpoint prior live tree: %w", err)
	}
	if err := validateMatchingTree(cloned, digest, binding); err != nil {
		return nil, "", fmt.Errorf("projectstate: checkpoint prior live tree: %w", err)
	}
	decoded, err := state.DecodeTree(cloneCheckpointTree(cloned))
	if err != nil || decoded.Digest != digest {
		if err != nil {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("projectstate: checkpoint prior live decoded digest differs")
	}
	return cloned, digest, nil
}

func proveCheckpointPublicationReview(
	input checkpointPlanInput,
	acceptedTree state.Tree,
	selectedStart state.Snapshot,
	boundary int64,
	activeOperations []StoredOperation,
	provedView ComposedView,
) (string, state.Digest, error) {
	review := input.Review
	if err := validatePublicationReviewTrust(review.trust, review.workspace, nil); err != nil {
		return "", "", fmt.Errorf("projectstate: checkpoint publication trust: %w", err)
	}
	if review.workspace.Binding != input.Binding || review.composed.status.Binding != input.Binding ||
		review.status.Binding != input.Binding || review.workspace.State != input.Composed.status.State ||
		review.composed.status.State != input.Composed.status.State || review.status.State != input.Composed.status.State {
		return "", "", fmt.Errorf("projectstate: checkpoint publication workspace differs")
	}
	workspaceTree, err := checkpointSnapshotTree(review.workspace.Snapshot)
	if err != nil || !equalCheckpointTree(workspaceTree, acceptedTree) {
		if err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("projectstate: checkpoint publication accepted tree differs")
	}

	envelope := review.envelope
	if envelope.Scope != input.Binding.Scope || envelope.Repository != input.Binding.Repository ||
		envelope.OriginDigest != review.trust.origin.digest || envelope.AcceptedRef != input.Binding.AcceptedRef ||
		envelope.AcceptedCommitSHA != input.Binding.AcceptedCommitSHA ||
		envelope.AcceptedTreeDigest != state.Digest(input.Binding.AcceptedTreeDigest) ||
		envelope.CandidateTreeDigest != provedView.Snapshot.Digest || envelope.OverlayGeneration != provedView.ThroughGeneration {
		return "", "", fmt.Errorf("projectstate: checkpoint publication envelope differs")
	}
	if review.policy.Repository != input.Binding.Repository {
		return "", "", fmt.Errorf("projectstate: checkpoint publication policy repository differs")
	}
	configuration := publicationConfigurationFromRecord(review.trust.origin.digest, review.policy)
	if err := validatePublicationConfiguration(configuration); err != nil {
		return "", "", fmt.Errorf("projectstate: checkpoint publication policy: %w", err)
	}
	if publicationInvalidationKind(input.Binding, review.policy, review.trust.origin.digest) != "" {
		return "", "", fmt.Errorf("projectstate: checkpoint publication policy is stale")
	}
	if envelope.Classification != configuration.Classification || envelope.PolicyRevision != configuration.PolicyRevision {
		return "", "", fmt.Errorf("projectstate: checkpoint publication policy projection differs")
	}

	attributedView, attributedDiff, err := publicationAttributedDiff(
		input.Composed.status.AcceptedSnapshot, selectedStart, boundary, activeOperations,
	)
	if err != nil {
		return "", "", err
	}
	if !checkpointComposedViewEqual(attributedView, provedView) {
		return "", "", fmt.Errorf("projectstate: checkpoint publication attributed composition differs")
	}
	attributedDiff, err = normalizePublicationReviewDiff(attributedDiff)
	if err != nil {
		return "", "", err
	}
	wantDiffJSON, wantDiffDigest, err := encodePublicationSemanticDiff(attributedDiff)
	if err != nil {
		return "", "", err
	}
	gotDiffJSON, gotDiffDigest, err := encodePublicationSemanticDiff(review.semanticDiff)
	if err != nil {
		return "", "", err
	}
	if !bytes.Equal(gotDiffJSON, wantDiffJSON) || gotDiffDigest != wantDiffDigest ||
		gotDiffDigest != review.semanticDiffDigest || gotDiffDigest != envelope.SemanticDiffDigest {
		return "", "", fmt.Errorf("projectstate: checkpoint publication semantic diff differs")
	}
	_, reviewDigest, err := encodePublicationReviewEnvelope(envelope)
	if err != nil {
		return "", "", err
	}
	if reviewDigest != review.reviewDigest {
		return "", "", fmt.Errorf("projectstate: checkpoint publication review digest differs")
	}
	if review.status.CandidateDigest != provedView.Snapshot.Digest || review.status.OverlayGeneration != provedView.ThroughGeneration ||
		review.status.PublicationClassification != configuration.Classification || review.status.PublicationReviewDigest != reviewDigest {
		return "", "", fmt.Errorf("projectstate: checkpoint publication status projection differs")
	}
	diffJSON, diffDigest, err := encodePublicationSemanticDiff(review.diff.SemanticDiff)
	if err != nil {
		return "", "", err
	}
	if !bytes.Equal(diffJSON, gotDiffJSON) || diffDigest != gotDiffDigest ||
		review.diff.CandidateDigest != provedView.Snapshot.Digest || review.diff.OverlayGeneration != provedView.ThroughGeneration ||
		review.diff.PublicationClassification != configuration.Classification || review.diff.PublicationReviewDigest != reviewDigest {
		return "", "", fmt.Errorf("projectstate: checkpoint publication diff projection differs")
	}

	publication := checkpointPublicationReviewV1{
		SchemaVersion: 1, Kind: "checkpoint_publication_review", Review: envelope,
		ReviewDigest: reviewDigest, CheckpointedBy: input.Actor,
	}
	raw, err := encodeCheckpointPublicationReview(publication)
	if err != nil {
		return "", "", err
	}
	decoded, err := decodeCheckpointPublicationReview(raw)
	if err != nil {
		return "", "", err
	}
	reencoded, err := encodeCheckpointPublicationReview(decoded)
	if err != nil || reencoded != raw {
		if err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("projectstate: checkpoint publication review round trip differs")
	}
	return raw, reviewDigest, nil
}

func proveCheckpointOutgoingEnvelopes(priorCandidate, operations, publication string) error {
	decodedPrior, err := decodeCheckpointPriorCandidate(priorCandidate)
	if err != nil {
		return err
	}
	reencodedPrior, err := encodeCheckpointPriorCandidate(decodedPrior)
	if err != nil || reencodedPrior != priorCandidate {
		return fmt.Errorf("projectstate: outgoing checkpoint prior candidate differs after strict round trip")
	}
	decodedOperations, err := decodeCheckpointOperations(operations)
	if err != nil {
		return err
	}
	reencodedOperations, err := encodeCheckpointOperations(decodedOperations)
	if err != nil || reencodedOperations != operations {
		return fmt.Errorf("projectstate: outgoing checkpoint operations differ after strict round trip")
	}
	decodedPublication, err := decodeCheckpointPublicationReview(publication)
	if err != nil {
		return err
	}
	reencodedPublication, err := encodeCheckpointPublicationReview(decodedPublication)
	if err != nil || reencodedPublication != publication {
		return fmt.Errorf("projectstate: outgoing checkpoint publication differs after strict round trip")
	}
	return nil
}

func checkpointSnapshotTree(snapshot state.Snapshot) (state.Tree, error) {
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		return nil, err
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		return nil, err
	}
	decoded, err := state.DecodeTree(cloneCheckpointTree(tree))
	if err != nil {
		return nil, err
	}
	if digest != snapshot.Digest || decoded.Digest != snapshot.Digest {
		return nil, fmt.Errorf("projectstate: snapshot digest differs from canonical tree")
	}
	return cloneCheckpointTree(tree), nil
}

func checkpointSnapshotsEqual(left, right state.Snapshot) bool {
	leftTree, leftErr := checkpointSnapshotTree(left)
	rightTree, rightErr := checkpointSnapshotTree(right)
	return leftErr == nil && rightErr == nil && equalCheckpointTree(leftTree, rightTree)
}

func checkpointStoredOperationsEqual(left, right []StoredOperation) bool {
	if left == nil || right == nil || len(left) != len(right) {
		return left != nil && right != nil && len(left) == len(right)
	}
	for index := range left {
		if left[index].Generation != right[index].Generation {
			return false
		}
		leftRaw, leftErr := state.CanonicalOperation(left[index].Operation)
		rightRaw, rightErr := state.CanonicalOperation(right[index].Operation)
		if leftErr != nil || rightErr != nil || !bytes.Equal(leftRaw, rightRaw) {
			return false
		}
	}
	return true
}

func checkpointComposedViewEqual(left, right ComposedView) bool {
	return left.ThroughGeneration == right.ThroughGeneration &&
		equalPublicationOperationIDs(left.AppliedOperationIDs, right.AppliedOperationIDs) &&
		checkpointSnapshotsEqual(left.Snapshot, right.Snapshot)
}

func checkpointComposedWorkspacesEqual(left, right composedWorkspace) bool {
	return left.status.Binding == right.status.Binding && left.status.State == right.status.State &&
		left.status.CandidateDigest == right.status.CandidateDigest && left.status.OverlayGeneration == right.status.OverlayGeneration &&
		left.status.PublicationClassification == right.status.PublicationClassification &&
		left.status.PublicationReviewDigest == right.status.PublicationReviewDigest &&
		checkpointSnapshotsEqual(left.status.AcceptedSnapshot, right.status.AcceptedSnapshot) &&
		checkpointComposedViewEqual(left.view, right.view) && checkpointStoredOperationsEqual(left.operations, right.operations) &&
		checkpointSnapshotsEqual(left.selectedStart, right.selectedStart) && left.boundary == right.boundary
}

func cloneCheckpointPlanOperations(operations []localstore.WorkspaceOperation) []localstore.WorkspaceOperation {
	cloned := make([]localstore.WorkspaceOperation, len(operations))
	for index, operation := range operations {
		cloned[index] = cloneImportOperation(operation)
	}
	return cloned
}

func checkpointOperationRowsEqual(left, right []localstore.WorkspaceOperation) bool {
	if left == nil || right == nil || len(left) != len(right) {
		return left != nil && right != nil && len(left) == len(right)
	}
	for index := range left {
		if left[index].Generation != right[index].Generation || left[index].OperationID != right[index].OperationID ||
			left[index].State != right[index].State || !bytes.Equal(left[index].OperationJSON, right[index].OperationJSON) ||
			(left[index].StashedByStashID == nil) != (right[index].StashedByStashID == nil) {
			return false
		}
		if left[index].StashedByStashID != nil && *left[index].StashedByStashID != *right[index].StashedByStashID {
			return false
		}
	}
	return true
}
