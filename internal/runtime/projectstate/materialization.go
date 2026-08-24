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

type currentMaterializationProof struct {
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
	proof currentMaterializationProof,
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
