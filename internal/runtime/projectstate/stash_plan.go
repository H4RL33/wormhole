package projectstate

import (
	"bytes"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type stashPlan struct {
	SourceTree        state.Tree
	ComposedTree      state.Tree
	OperationsJSON    string
	SourceDigest      state.Digest
	CandidateDigest   state.Digest
	ThroughGeneration int64
	OperationCount    int
	AbsorbedRows      []localstore.WorkspaceOperation
	LaterRows         []localstore.WorkspaceOperation
}

func buildStashPlan(
	binding types.WorkspaceBinding,
	sourceBase state.Snapshot,
	candidate *localstore.WorkspaceCandidateRecord,
	operationInventory []localstore.WorkspaceOperation,
) (stashPlan, error) {
	if err := binding.Validate(); err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: invalid stash binding: %w", err)
	}
	if operationInventory == nil {
		return stashPlan{}, fmt.Errorf("projectstate: stash operation inventory must be non-nil")
	}

	sourceTree, err := state.EncodeTree(sourceBase)
	if err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: encode stash source base: %w", err)
	}
	acceptedDigest := state.Digest(binding.AcceptedTreeDigest)
	if sourceBase.Digest != acceptedDigest {
		return stashPlan{}, fmt.Errorf("projectstate: stash source digest differs from accepted binding")
	}
	if err := validateMatchingTree(sourceTree, sourceBase.Digest, binding); err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: invalid stash source base: %w", err)
	}
	if err := validateStashCandidate(candidate, sourceBase.Digest, binding); err != nil {
		return stashPlan{}, err
	}

	selectedStart, boundary := selectCandidateStart(sourceBase, candidate)
	selectedStartTree, err := state.EncodeTree(selectedStart)
	if err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: encode stash selected start: %w", err)
	}
	if err := validateMatchingTree(selectedStartTree, selectedStart.Digest, binding); err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: invalid stash selected start: %w", err)
	}

	clonedAbsorbedRows, absorbedOperations, clonedLaterRows, laterOperations, err := prepareStashInventory(operationInventory, boundary)
	if err != nil {
		return stashPlan{}, err
	}
	composed, err := Compose(selectedStart, boundary, laterOperations)
	if err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: compose stash operation suffix: %w", err)
	}
	replayJSON, err := encodeStashReplay(StashReplayV1{
		SchemaVersion:            1,
		SelectedStartTree:        selectedStartTree,
		SelectedStartDigest:      selectedStart.Digest,
		InitialThroughGeneration: boundary,
		AbsorbedOperations:       absorbedOperations,
		Operations:               laterOperations,
	}, binding, composed.ThroughGeneration)
	if err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: encode stash operation replay: %w", err)
	}
	composedTree, err := state.EncodeTree(composed.Snapshot)
	if err != nil {
		return stashPlan{}, fmt.Errorf("projectstate: encode stash composed tree: %w", err)
	}

	return stashPlan{
		SourceTree:        cloneCheckpointTree(sourceTree),
		ComposedTree:      cloneCheckpointTree(composedTree),
		OperationsJSON:    replayJSON,
		SourceDigest:      sourceBase.Digest,
		CandidateDigest:   composed.Snapshot.Digest,
		ThroughGeneration: composed.ThroughGeneration,
		OperationCount:    len(absorbedOperations) + len(laterOperations),
		AbsorbedRows:      clonedAbsorbedRows,
		LaterRows:         clonedLaterRows,
	}, nil
}

func validateStashCandidate(candidate *localstore.WorkspaceCandidateRecord, acceptedDigest state.Digest, binding types.WorkspaceBinding) error {
	if candidate == nil {
		return nil
	}
	if !types.ValidCandidateImportOrigin(candidate.ImportedBy) {
		return fmt.Errorf("projectstate: stash candidate has invalid import origin")
	}
	if candidate.AcceptedBaseDigest != acceptedDigest {
		return fmt.Errorf("projectstate: stash candidate accepted base digest differs")
	}
	directTree, err := state.EncodeTree(candidate.DirectSnapshot)
	if err != nil {
		return fmt.Errorf("projectstate: encode stash direct candidate: %w", err)
	}
	if err := validateMatchingTree(directTree, candidate.DirectSnapshot.Digest, binding); err != nil {
		return fmt.Errorf("projectstate: invalid stash direct candidate: %w", err)
	}
	if candidate.WorkingTreeDigest != candidate.DirectSnapshot.Digest {
		return fmt.Errorf("projectstate: stash candidate working tree digest differs")
	}
	if candidate.RebasedSnapshot == nil {
		if candidate.RebasedThroughGeneration != 0 {
			return fmt.Errorf("projectstate: direct stash candidate has a rebase boundary")
		}
		return nil
	}
	if candidate.RebasedThroughGeneration < 0 {
		return fmt.Errorf("projectstate: stash candidate has a negative rebase boundary")
	}
	rebasedTree, err := state.EncodeTree(*candidate.RebasedSnapshot)
	if err != nil {
		return fmt.Errorf("projectstate: encode stash rebased candidate: %w", err)
	}
	if err := validateMatchingTree(rebasedTree, candidate.RebasedSnapshot.Digest, binding); err != nil {
		return fmt.Errorf("projectstate: invalid stash rebased candidate: %w", err)
	}
	return nil
}

func prepareStashInventory(
	rows []localstore.WorkspaceOperation,
	boundary int64,
) ([]localstore.WorkspaceOperation, []StoredOperation, []localstore.WorkspaceOperation, []StoredOperation, error) {
	if rows == nil {
		return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory must be non-nil")
	}
	absorbedRows := make([]localstore.WorkspaceOperation, 0)
	absorbedOperations := make([]StoredOperation, 0)
	laterRows := make([]localstore.WorkspaceOperation, 0)
	laterOperations := make([]StoredOperation, 0)
	seenOperationIDs := make(map[string]struct{}, len(rows))
	var previousGeneration int64
	for index, row := range rows {
		if row.Generation <= 0 || (index > 0 && row.Generation <= previousGeneration) {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory generations are not strictly increasing positive values")
		}
		previousGeneration = row.Generation
		if !types.CanonicalUUID(row.OperationID) {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory has an invalid operation ID")
		}
		if _, duplicate := seenOperationIDs[row.OperationID]; duplicate {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory has a duplicate operation ID")
		}
		seenOperationIDs[row.OperationID] = struct{}{}
		switch row.State {
		case "active", "rebased", "materialized", "discarded":
			if row.StashedByStashID != nil {
				return nil, nil, nil, nil, fmt.Errorf("projectstate: non-stashed operation in stash inventory has a stash owner")
			}
		case "stashed":
			if row.StashedByStashID != nil && !canonicalUUIDv4(*row.StashedByStashID) {
				return nil, nil, nil, nil, fmt.Errorf("projectstate: stashed operation in stash inventory has an invalid owner")
			}
		default:
			return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory has invalid state %q", row.State)
		}

		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: decode stash operation inventory row: %w", err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: canonicalize stash operation inventory row: %w", err)
		}
		if operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, nil, nil, nil, fmt.Errorf("projectstate: stash operation inventory row does not match canonical operation bytes")
		}

		stored := StoredOperation{Generation: row.Generation, Operation: operation}
		switch row.State {
		case "rebased":
			if row.Generation > boundary {
				return nil, nil, nil, nil, fmt.Errorf("projectstate: rebased stash operation exceeds candidate boundary")
			}
			absorbedRows = append(absorbedRows, cloneImportOperation(row))
			absorbedOperations = append(absorbedOperations, stored)
		case "active":
			if row.Generation <= boundary {
				return nil, nil, nil, nil, fmt.Errorf("projectstate: active stash operation does not exceed candidate boundary")
			}
			laterRows = append(laterRows, cloneImportOperation(row))
			laterOperations = append(laterOperations, stored)
		}
	}
	return absorbedRows, absorbedOperations, laterRows, laterOperations, nil
}
