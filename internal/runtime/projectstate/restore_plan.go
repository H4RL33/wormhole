package projectstate

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrStashCorrupt           = errors.New("projectstate: stash is corrupt")
	ErrStashOperationMismatch = errors.New("projectstate: stash operation mismatch")
)

type restoreStashProof struct {
	SourceBase   state.Snapshot
	Composed     state.Snapshot
	Replay       StashReplayV1
	AbsorbedRows []localstore.WorkspaceOperation
	LaterRows    []localstore.WorkspaceOperation
}

type currentProof struct {
	DirectSnapshot    state.Snapshot
	Snapshot          state.Snapshot
	ThroughGeneration int64
	ActiveRows        []localstore.WorkspaceOperation
}

type restorePlan struct {
	Stash            restoreStashProof
	Current          currentProof
	MergedSnapshot   *state.Snapshot
	ConflictEvidence []localstore.WorkspaceConflictEvidence
	Result           RestoreStashResult
}

func buildRestorePlan(retry localstore.WorkspaceRestoreRetryState) (restorePlan, error) {
	stash, err := proveRestoreStash(retry)
	if err != nil {
		return restorePlan{}, err
	}
	current, err := composeRestoreCurrent(retry)
	if err != nil {
		return restorePlan{}, err
	}
	merged, err := ThreeWayRebase(stash.SourceBase, current.Snapshot, stash.Composed)
	if err != nil {
		return restorePlan{}, fmt.Errorf("projectstate: restore stash semantic rebase: %w", err)
	}
	conflicts := cloneImportConflicts(merged.Conflicts)
	evidence, err := encodeWorkspaceConflictEvidence(conflicts)
	if err != nil {
		return restorePlan{}, fmt.Errorf("projectstate: encode restore conflict evidence: %w", err)
	}
	plan := restorePlan{
		Stash: stash, Current: current, ConflictEvidence: evidence,
		Result: RestoreStashResult{Conflicts: cloneImportConflicts(conflicts)},
	}
	if len(conflicts) != 0 {
		plan.Result.RestoredDigest = current.Snapshot.Digest
		plan.Result.RebasedThroughGeneration = current.ThroughGeneration
		plan.Result.StashRetained = true
		return plan, nil
	}
	owned, err := cloneImportSnapshot(merged.Snapshot)
	if err != nil {
		return restorePlan{}, fmt.Errorf("projectstate: clone restored snapshot: %w", err)
	}
	plan.MergedSnapshot = &owned
	plan.Result.RestoredDigest = owned.Digest
	plan.Result.RebasedThroughGeneration = current.ThroughGeneration
	return plan, nil
}

func proveRestoreStash(retry localstore.WorkspaceRestoreRetryState) (restoreStashProof, error) {
	if retry.Operations == nil {
		return restoreStashProof{}, fmt.Errorf("%w: complete operation audit is nil", ErrStashOperationMismatch)
	}
	binding := retry.Workspace.Binding
	if err := binding.Validate(); err != nil {
		return restoreStashProof{}, fmt.Errorf("projectstate: invalid restore binding: %w", err)
	}
	stash := retry.Stash
	if !canonicalUUIDv4(stash.StashID) {
		return restoreStashProof{}, fmt.Errorf("%w: invalid stash ID", ErrStashCorrupt)
	}
	if err := validateMatchingTree(stash.SourceTree, stash.SourceBaseDigest, binding); err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: source tree: %v", ErrStashCorrupt, err)
	}
	if err := validateMatchingTree(stash.ComposedTree, stash.CandidateDigest, binding); err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: composed tree: %v", ErrStashCorrupt, err)
	}
	source, err := state.DecodeTree(stash.SourceTree)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: decode source tree: %v", ErrStashCorrupt, err)
	}
	composed, err := state.DecodeTree(stash.ComposedTree)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: decode composed tree: %v", ErrStashCorrupt, err)
	}
	replay, err := decodeStashReplay(stash.OperationsJSON, binding, stash.ThroughGeneration)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: replay envelope: %v", ErrStashCorrupt, err)
	}
	selectedStart, err := state.DecodeTree(replay.SelectedStartTree)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: decode replay selected start: %v", ErrStashCorrupt, err)
	}
	replayed, err := Compose(selectedStart, replay.InitialThroughGeneration, replay.Operations)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: replay composition: %v", ErrStashCorrupt, err)
	}
	replayedTree, err := state.EncodeTree(replayed.Snapshot)
	if err != nil || replayed.Snapshot.Digest != stash.CandidateDigest || replayed.ThroughGeneration != stash.ThroughGeneration ||
		!equalCheckpointTree(replayedTree, stash.ComposedTree) {
		return restoreStashProof{}, fmt.Errorf("%w: replay does not reproduce composed stash", ErrStashCorrupt)
	}

	audit, err := validateRestoreAudit(retry.Operations)
	if err != nil {
		return restoreStashProof{}, err
	}
	expected := make([]StoredOperation, 0, len(replay.AbsorbedOperations)+len(replay.Operations))
	expected = append(expected, replay.AbsorbedOperations...)
	expected = append(expected, replay.Operations...)
	owned := make([]restoreAuditOperation, 0, len(expected))
	for _, row := range audit {
		if row.row.StashedByStashID != nil && *row.row.StashedByStashID == stash.StashID {
			owned = append(owned, row)
		}
	}
	if len(owned) != len(expected) {
		return restoreStashProof{}, fmt.Errorf("%w: owned row count is %d, want %d", ErrStashOperationMismatch, len(owned), len(expected))
	}
	rows := make([]localstore.WorkspaceOperation, len(owned))
	for index := range expected {
		wantJSON, canonicalErr := state.CanonicalOperation(expected[index].Operation)
		if canonicalErr != nil || owned[index].row.State != "stashed" ||
			owned[index].row.Generation != expected[index].Generation ||
			owned[index].row.OperationID != expected[index].Operation.ID ||
			!bytes.Equal(owned[index].row.OperationJSON, wantJSON) {
			return restoreStashProof{}, fmt.Errorf("%w: owned row %d differs from replay", ErrStashOperationMismatch, index)
		}
		rows[index] = cloneImportOperation(owned[index].row)
	}
	absorbedCount := len(replay.AbsorbedOperations)
	absorbed := append([]localstore.WorkspaceOperation{}, rows[:absorbedCount]...)
	later := append([]localstore.WorkspaceOperation{}, rows[absorbedCount:]...)
	ownedReplay, err := cloneRestoreReplay(replay)
	if err != nil {
		return restoreStashProof{}, fmt.Errorf("%w: clone replay: %v", ErrStashCorrupt, err)
	}
	return restoreStashProof{
		SourceBase: source, Composed: composed, Replay: ownedReplay,
		AbsorbedRows: absorbed, LaterRows: later,
	}, nil
}

func composeRestoreCurrent(retry localstore.WorkspaceRestoreRetryState) (currentProof, error) {
	if retry.Operations == nil {
		return currentProof{}, fmt.Errorf("%w: complete operation audit is nil", ErrStashOperationMismatch)
	}
	binding := retry.Workspace.Binding
	if err := binding.Validate(); err != nil {
		return currentProof{}, fmt.Errorf("projectstate: invalid restore current binding: %w", err)
	}
	acceptedTree, err := state.EncodeTree(retry.Workspace.Snapshot)
	if err != nil || validateMatchingTree(acceptedTree, state.Digest(binding.AcceptedTreeDigest), binding) != nil {
		return currentProof{}, fmt.Errorf("projectstate: invalid restore current accepted snapshot")
	}
	accepted, err := state.DecodeTree(acceptedTree)
	if err != nil {
		return currentProof{}, fmt.Errorf("projectstate: clone restore current accepted snapshot: %w", err)
	}
	if err := validateStashCandidate(retry.Candidate, accepted.Digest, binding); err != nil {
		return currentProof{}, fmt.Errorf("projectstate: invalid restore current candidate: %w", err)
	}
	if retry.Candidate != nil && (!types.CanonicalUUID(retry.Candidate.ImportedBy) || retry.Candidate.ImportedAt.IsZero() || !zeroOffsetTime(retry.Candidate.ImportedAt)) {
		return currentProof{}, fmt.Errorf("projectstate: invalid restore current candidate attribution")
	}
	start, boundary := selectCandidateStart(accepted, retry.Candidate)
	direct := accepted
	if retry.Candidate != nil {
		direct = retry.Candidate.DirectSnapshot
	}
	direct, err = cloneImportSnapshot(direct)
	if err != nil {
		return currentProof{}, fmt.Errorf("projectstate: clone restore direct snapshot: %w", err)
	}
	audit, err := validateRestoreAudit(retry.Operations)
	if err != nil {
		return currentProof{}, err
	}
	activeRows := make([]localstore.WorkspaceOperation, 0)
	operations := make([]StoredOperation, 0)
	for _, audited := range audit {
		row := audited.row
		switch row.State {
		case "active":
			if row.Generation <= boundary {
				return currentProof{}, fmt.Errorf("%w: active row at or below current boundary", ErrStashOperationMismatch)
			}
			activeRows = append(activeRows, cloneImportOperation(row))
			operations = append(operations, StoredOperation{Generation: row.Generation, Operation: audited.operation})
		case "rebased":
			if row.Generation > boundary {
				return currentProof{}, fmt.Errorf("%w: rebased row above current boundary", ErrStashOperationMismatch)
			}
		}
	}
	composed, err := Compose(start, boundary, operations)
	if err != nil {
		return currentProof{}, fmt.Errorf("projectstate: compose restore current state: %w", err)
	}
	return currentProof{
		DirectSnapshot: direct, Snapshot: composed.Snapshot,
		ThroughGeneration: composed.ThroughGeneration, ActiveRows: activeRows,
	}, nil
}

type restoreAuditOperation struct {
	row       localstore.WorkspaceOperation
	operation state.OperationV1
}

func validateRestoreAudit(records []localstore.WorkspaceOperationAuditRecord) ([]restoreAuditOperation, error) {
	if records == nil {
		return nil, fmt.Errorf("%w: complete operation audit is nil", ErrStashOperationMismatch)
	}
	decoded := make([]restoreAuditOperation, 0, len(records))
	seenIDs := make(map[string]struct{}, len(records))
	var previousGeneration int64
	for index, record := range records {
		row := record.WorkspaceOperation
		if row.Generation <= 0 || (index > 0 && row.Generation <= previousGeneration) ||
			!types.CanonicalUUID(row.OperationID) || record.CreatedAt.IsZero() || !zeroOffsetTime(record.CreatedAt) {
			return nil, fmt.Errorf("%w: invalid operation audit metadata at row %d", ErrStashOperationMismatch, index)
		}
		previousGeneration = row.Generation
		if _, duplicate := seenIDs[row.OperationID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate operation audit identity", ErrStashOperationMismatch)
		}
		seenIDs[row.OperationID] = struct{}{}
		switch row.State {
		case "active", "rebased", "materialized", "discarded":
			if row.StashedByStashID != nil {
				return nil, fmt.Errorf("%w: non-stashed operation has a stash owner", ErrStashOperationMismatch)
			}
		case "stashed":
			if row.StashedByStashID != nil && !canonicalUUIDv4(*row.StashedByStashID) {
				return nil, fmt.Errorf("%w: stashed operation has a noncanonical owner", ErrStashOperationMismatch)
			}
		default:
			return nil, fmt.Errorf("%w: invalid operation audit state %q", ErrStashOperationMismatch, row.State)
		}
		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, fmt.Errorf("%w: decode operation audit row %d: %v", ErrStashOperationMismatch, index, err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil || operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, fmt.Errorf("%w: operation audit row %d differs from canonical operation", ErrStashOperationMismatch, index)
		}
		decoded = append(decoded, restoreAuditOperation{row: cloneImportOperation(row), operation: operation})
	}
	return decoded, nil
}

func cloneRestoreReplay(value StashReplayV1) (StashReplayV1, error) {
	cloned := value
	cloned.SelectedStartTree = cloneCheckpointTree(value.SelectedStartTree)
	var err error
	cloned.AbsorbedOperations, err = cloneRestoreStoredOperations(value.AbsorbedOperations)
	if err != nil {
		return StashReplayV1{}, err
	}
	cloned.Operations, err = cloneRestoreStoredOperations(value.Operations)
	if err != nil {
		return StashReplayV1{}, err
	}
	return cloned, nil
}

func cloneRestoreStoredOperations(values []StoredOperation) ([]StoredOperation, error) {
	cloned := make([]StoredOperation, len(values))
	for index, value := range values {
		raw, err := state.CanonicalOperation(value.Operation)
		if err != nil {
			return nil, err
		}
		operation, err := state.DecodeOperation(raw)
		if err != nil {
			return nil, err
		}
		cloned[index] = StoredOperation{Generation: value.Generation, Operation: operation}
	}
	return cloned, nil
}
