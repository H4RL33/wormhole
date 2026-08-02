package projectstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type CheckpointOperationV1 struct {
	Generation          int64  `json:"generation"`
	OperationID         string `json:"operation_id"`
	OperationJSON       string `json:"operation_json"`
	PrepublicationState string `json:"prepublication_state"`
}

type CheckpointOperationsV1 struct {
	SchemaVersion            int                     `json:"schema_version"`
	InitialThroughGeneration int64                   `json:"initial_through_generation"`
	Operations               []CheckpointOperationV1 `json:"operations"`
}

type materializationDispositionProof struct {
	journals map[string]materializationJournalProof
}

type materializationJournalProof struct {
	record   localstore.WorkspaceMaterializationRecord
	envelope CheckpointOperationsV1
}

type matchingMaterializationProof struct {
	journalID              string
	throughGeneration      int64
	includedOperationsJSON string
}

func encodeCheckpointOperations(envelope CheckpointOperationsV1) (string, error) {
	canonical, err := state.CanonicalJSON(envelope)
	if err != nil {
		return "", fmt.Errorf("projectstate: encode checkpoint operations: %w", err)
	}
	if _, err := decodeCheckpointOperations(string(canonical)); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func decodeCheckpointOperations(raw string) (CheckpointOperationsV1, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var envelope CheckpointOperationsV1
	if err := decoder.Decode(&envelope); err != nil {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: decode checkpoint operations: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: decode checkpoint operations: trailing JSON")
		}
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: decode checkpoint operations: %w", err)
	}
	canonical, err := state.CanonicalJSON(envelope)
	if err != nil {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: canonicalize checkpoint operations: %w", err)
	}
	if !bytes.Equal(canonical, []byte(raw)) {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: checkpoint operations are not canonical")
	}
	if envelope.SchemaVersion != 1 {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: unsupported checkpoint operations schema version")
	}
	if envelope.Operations == nil {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: checkpoint operations array is nil")
	}
	if envelope.InitialThroughGeneration < 0 {
		return CheckpointOperationsV1{}, fmt.Errorf("projectstate: invalid initial checkpoint generation")
	}
	seenIDs := make(map[string]struct{}, len(envelope.Operations))
	for index, operation := range envelope.Operations {
		if operation.Generation <= 0 || (index > 0 && operation.Generation <= envelope.Operations[index-1].Generation) {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: checkpoint operation generations are not strictly increasing positive values")
		}
		if !types.CanonicalUUID(operation.OperationID) {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: invalid checkpoint operation ID")
		}
		if _, duplicate := seenIDs[operation.OperationID]; duplicate {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: duplicate checkpoint operation ID")
		}
		seenIDs[operation.OperationID] = struct{}{}
		switch operation.PrepublicationState {
		case "rebased":
			if operation.Generation > envelope.InitialThroughGeneration {
				return CheckpointOperationsV1{}, fmt.Errorf("projectstate: rebased checkpoint operation exceeds initial boundary")
			}
		case "active":
			if operation.Generation <= envelope.InitialThroughGeneration {
				return CheckpointOperationsV1{}, fmt.Errorf("projectstate: active checkpoint operation does not exceed initial boundary")
			}
		default:
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: invalid checkpoint operation prepublication state")
		}
		decoded, err := state.DecodeOperation([]byte(operation.OperationJSON))
		if err != nil {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: decode checkpoint operation: %w", err)
		}
		canonicalOperation, err := state.CanonicalOperation(decoded)
		if err != nil {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: canonicalize checkpoint operation: %w", err)
		}
		if decoded.ID != operation.OperationID || !bytes.Equal(canonicalOperation, []byte(operation.OperationJSON)) {
			return CheckpointOperationsV1{}, fmt.Errorf("projectstate: checkpoint operation identity or bytes differ")
		}
	}
	return envelope, nil
}

func proveMaterializationDisposition(disposition localstore.WorkspaceMaterializationDisposition) (materializationDispositionProof, error) {
	if disposition.Journals == nil || disposition.Operations == nil {
		return materializationDispositionProof{}, fmt.Errorf("projectstate: incomplete materialization disposition")
	}
	for index, journal := range disposition.Journals {
		if journal.JournalID == "" || (index > 0 && journal.JournalID <= disposition.Journals[index-1].JournalID) {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: materialization journals are not strictly ordered and unique")
		}
	}
	rowsByGeneration := make(map[int64]localstore.WorkspaceOperation, len(disposition.Operations))
	rowIDs := make(map[string]struct{}, len(disposition.Operations))
	for index, operation := range disposition.Operations {
		if operation.Generation <= 0 || (index > 0 && operation.Generation <= disposition.Operations[index-1].Generation) || !types.CanonicalUUID(operation.OperationID) {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: materialization operations are not strictly ordered and valid")
		}
		if _, duplicate := rowIDs[operation.OperationID]; duplicate {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: duplicate materialization operation ID")
		}
		rowIDs[operation.OperationID] = struct{}{}
		rowsByGeneration[operation.Generation] = operation
	}

	proof := materializationDispositionProof{journals: make(map[string]materializationJournalProof)}
	claimsByGeneration := make(map[int64]CheckpointOperationV1)
	claimedIDs := make(map[string]struct{})
	for _, journal := range disposition.Journals {
		switch journal.State {
		case "prepared":
			return materializationDispositionProof{}, fmt.Errorf("projectstate: prepared materialization requires recovery")
		case "recovered_old":
			continue
		case "accepted", "published", "recovered_new":
		default:
			return materializationDispositionProof{}, fmt.Errorf("projectstate: invalid materialization journal state")
		}
		if !validMaterializationPublicationProof(journal) {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: materialization publication proof is invalid")
		}
		if (journal.State == "published" || journal.State == "recovered_new") && journal.PublicationReviewProofVersion != 1 {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: acceptance-eligible materialization has no publication proof")
		}
		if journal.IncludedOperationsJSON == nil {
			if journal.State == "accepted" {
				if err := proveJournalPrepublicationMembership(journal, nil, disposition.Operations); err != nil {
					return materializationDispositionProof{}, err
				}
				continue
			}
			return materializationDispositionProof{}, fmt.Errorf("projectstate: acceptance-eligible materialization has no operation envelope")
		}
		envelope, err := decodeCheckpointOperations(*journal.IncludedOperationsJSON)
		if err != nil {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: journal %q operation envelope: %w", journal.JournalID, err)
		}
		through := envelope.InitialThroughGeneration
		if len(envelope.Operations) != 0 && envelope.Operations[len(envelope.Operations)-1].Generation > through {
			through = envelope.Operations[len(envelope.Operations)-1].Generation
		}
		if through != journal.ThroughGeneration {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: journal %q operation boundary mismatch", journal.JournalID)
		}
		journalClaims := make(map[int64]struct{}, len(envelope.Operations))
		for _, claim := range envelope.Operations {
			if _, duplicate := claimsByGeneration[claim.Generation]; duplicate {
				return materializationDispositionProof{}, fmt.Errorf("projectstate: duplicate claimed operation generation")
			}
			if _, duplicate := claimedIDs[claim.OperationID]; duplicate {
				return materializationDispositionProof{}, fmt.Errorf("projectstate: duplicate claimed operation ID")
			}
			claimsByGeneration[claim.Generation] = claim
			claimedIDs[claim.OperationID] = struct{}{}
			journalClaims[claim.Generation] = struct{}{}
		}
		if err := proveJournalPrepublicationMembership(journal, journalClaims, disposition.Operations); err != nil {
			return materializationDispositionProof{}, err
		}
		proof.journals[journal.JournalID] = materializationJournalProof{
			record: cloneMaterializationRecord(journal), envelope: cloneCheckpointOperations(envelope),
		}
	}
	for generation, claim := range claimsByGeneration {
		operation, ok := rowsByGeneration[generation]
		if !ok || operation.OperationID != claim.OperationID || !bytes.Equal(operation.OperationJSON, []byte(claim.OperationJSON)) ||
			operation.State != "materialized" || operation.StashedByStashID != nil {
			return materializationDispositionProof{}, fmt.Errorf("projectstate: claimed materialization operation does not match persisted row")
		}
	}
	for _, operation := range disposition.Operations {
		if operation.State == "materialized" {
			if _, claimed := claimsByGeneration[operation.Generation]; !claimed {
				return materializationDispositionProof{}, fmt.Errorf("projectstate: unclaimed materialized operation")
			}
		}
	}
	return proof, nil
}

func proveJournalPrepublicationMembership(
	journal localstore.WorkspaceMaterializationRecord,
	claims map[int64]struct{},
	operations []localstore.WorkspaceOperation,
) error {
	for _, operation := range operations {
		if operation.Generation > journal.ThroughGeneration || (operation.State != "active" && operation.State != "rebased") {
			continue
		}
		if _, claimed := claims[operation.Generation]; !claimed {
			return fmt.Errorf("projectstate: journal %q omits a prepublication operation", journal.JournalID)
		}
	}
	return nil
}

func requireMatchingMaterialization(
	proof materializationDispositionProof,
	eligible *localstore.WorkspaceMaterializationRecord,
	binding types.WorkspaceBinding,
	capturedPrior state.Tree,
	capturedCandidate state.Tree,
	capturedCandidateDigest state.Digest,
) (matchingMaterializationProof, error) {
	if eligible == nil || proof.journals == nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: matching materialization proof is unavailable")
	}
	proved, ok := proof.journals[eligible.JournalID]
	if !ok || !equalMaterializationRecord(proved.record, *eligible) {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: eligible materialization differs from disposition proof")
	}
	if eligible.State != "published" && eligible.State != "recovered_new" {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: materialization is not acceptance eligible")
	}
	if eligible.PublicationReviewProofVersion != 1 || eligible.PublicationReviewJSON == nil || eligible.PriorCandidateJSON == nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: matching materialization has no publication proof")
	}
	if err := binding.Validate(); err != nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: invalid materialization binding: %w", err)
	}
	if eligible.Checkout != binding.Checkout || eligible.AcceptedBaseDigest != state.Digest(binding.AcceptedTreeDigest) {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: materialization differs from current binding")
	}
	if eligible.ExpectedLiveDigest != eligible.PriorTreeDigest {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: materialization prior digest mismatch")
	}
	if err := validateMatchingTree(eligible.PriorTree, eligible.PriorTreeDigest, binding); err != nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: materialization prior tree: %w", err)
	}
	if err := validateMatchingTree(capturedPrior, eligible.PriorTreeDigest, binding); err != nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: captured prior tree: %w", err)
	}
	if !equalCheckpointTree(eligible.PriorTree, capturedPrior) {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: captured prior differs from materialization")
	}
	if err := validateMatchingTree(eligible.CandidateTree, eligible.CandidateDigest, binding); err != nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: materialization candidate tree: %w", err)
	}
	if err := validateMatchingTree(capturedCandidate, capturedCandidateDigest, binding); err != nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: captured candidate tree: %w", err)
	}
	if eligible.CandidateDigest != capturedCandidateDigest || !equalCheckpointTree(eligible.CandidateTree, capturedCandidate) {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: captured candidate differs from materialization")
	}
	if eligible.IncludedOperationsJSON == nil {
		return matchingMaterializationProof{}, fmt.Errorf("projectstate: matching materialization has no operation envelope")
	}
	return matchingMaterializationProof{
		journalID: eligible.JournalID, throughGeneration: eligible.ThroughGeneration,
		includedOperationsJSON: *eligible.IncludedOperationsJSON,
	}, nil
}

func validateMatchingTree(tree state.Tree, expected state.Digest, binding types.WorkspaceBinding) error {
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		return err
	}
	canonical, err := state.EncodeTree(snapshot)
	if err != nil {
		return err
	}
	if !equalCheckpointTree(canonical, tree) {
		return fmt.Errorf("tree bytes are not canonical")
	}
	digest, err := state.DigestTree(canonical)
	if err != nil {
		return err
	}
	if digest != expected || snapshot.Digest != expected {
		return fmt.Errorf("tree digest differs")
	}
	if snapshot.Config.ProjectID != binding.Scope.ProjectID || snapshot.Config.Repository != binding.Repository {
		return fmt.Errorf("tree project or repository differs from binding")
	}
	return nil
}

func equalMaterializationRecord(left, right localstore.WorkspaceMaterializationRecord) bool {
	return left.JournalID == right.JournalID && left.ExpectedLiveDigest == right.ExpectedLiveDigest &&
		left.AcceptedBaseDigest == right.AcceptedBaseDigest && left.Checkout == right.Checkout &&
		left.PriorTreeDigest == right.PriorTreeDigest && left.CandidateDigest == right.CandidateDigest &&
		left.ThroughGeneration == right.ThroughGeneration && left.State == right.State &&
		equalCheckpointTree(left.PriorTree, right.PriorTree) && equalCheckpointTree(left.CandidateTree, right.CandidateTree) &&
		left.StagePath == right.StagePath && left.BackupPath == right.BackupPath &&
		equalOptionalString(left.IncludedOperationsJSON, right.IncludedOperationsJSON) &&
		left.PublicationReviewProofVersion == right.PublicationReviewProofVersion &&
		equalOptionalString(left.PublicationReviewJSON, right.PublicationReviewJSON) &&
		equalOptionalString(left.PriorCandidateJSON, right.PriorCandidateJSON)
}

func validMaterializationPublicationProof(record localstore.WorkspaceMaterializationRecord) bool {
	return (record.PublicationReviewProofVersion == 0 && record.PublicationReviewJSON == nil && record.PriorCandidateJSON == nil) ||
		(record.PublicationReviewProofVersion == 1 && record.PublicationReviewJSON != nil && record.PriorCandidateJSON != nil)
}

func equalCheckpointTree(left, right state.Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneCheckpointOperations(envelope CheckpointOperationsV1) CheckpointOperationsV1 {
	cloned := envelope
	cloned.Operations = make([]CheckpointOperationV1, len(envelope.Operations))
	copy(cloned.Operations, envelope.Operations)
	return cloned
}

func cloneMaterializationRecord(record localstore.WorkspaceMaterializationRecord) localstore.WorkspaceMaterializationRecord {
	cloned := record
	cloned.PriorTree = cloneCheckpointTree(record.PriorTree)
	cloned.CandidateTree = cloneCheckpointTree(record.CandidateTree)
	if record.IncludedOperationsJSON != nil {
		raw := *record.IncludedOperationsJSON
		cloned.IncludedOperationsJSON = &raw
	}
	if record.PublicationReviewJSON != nil {
		raw := *record.PublicationReviewJSON
		cloned.PublicationReviewJSON = &raw
	}
	if record.PriorCandidateJSON != nil {
		raw := *record.PriorCandidateJSON
		cloned.PriorCandidateJSON = &raw
	}
	return cloned
}

func cloneCheckpointTree(tree state.Tree) state.Tree {
	cloned := make(state.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}
