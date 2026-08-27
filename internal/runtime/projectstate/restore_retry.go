package projectstate

import (
	"fmt"
	"reflect"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type checkoutIdentityDigestV1 struct {
	CanonicalPath string `json:"canonical_path"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

type workspaceBindingDigestV1 struct {
	Scope              types.WorkspaceScope     `json:"scope"`
	Checkout           checkoutIdentityDigestV1 `json:"checkout"`
	Repository         types.RepositoryIdentity `json:"repository"`
	AcceptedRef        string                   `json:"accepted_ref"`
	AcceptedCommitSHA  string                   `json:"accepted_commit_sha"`
	AcceptedTreeDigest string                   `json:"accepted_tree_digest"`
}

type restoreRetryCandidateV2 struct {
	AcceptedBaseDigest       state.Digest  `json:"accepted_base_digest"`
	WorkingTreeDigest        state.Digest  `json:"working_tree_digest"`
	DirectSnapshotDigest     state.Digest  `json:"direct_snapshot_digest"`
	RebasedSnapshotDigest    *state.Digest `json:"rebased_snapshot_digest"`
	RebasedThroughGeneration int64         `json:"rebased_through_generation"`
	ImportedBy               string        `json:"imported_by"`
}

type restoreRetryOperationV2 struct {
	Generation       int64   `json:"generation"`
	OperationID      string  `json:"operation_id"`
	OperationJSON    string  `json:"operation_json"`
	State            string  `json:"state"`
	StashedByStashID *string `json:"stashed_by_stash_id"`
}

type restoreRetryStashV2 struct {
	StashID           string              `json:"stash_id"`
	SourceBaseDigest  state.Digest        `json:"source_base_digest"`
	CandidateDigest   state.Digest        `json:"candidate_digest"`
	Operations        StashReplayV1       `json:"operations"`
	ThroughGeneration int64               `json:"through_generation"`
	Actor             types.ActorEnvelope `json:"actor"`
	Label             string              `json:"label"`
}

type restoreRetryConflictV2 struct {
	OccurrenceID string          `json:"occurrence_id"`
	ConflictID   string          `json:"conflict_id"`
	Key          state.RecordKey `json:"key"`
	FieldPath    string          `json:"field_path"`
	ConflictKind string          `json:"conflict_kind"`
	BaseJSON     string          `json:"base_json"`
	OursJSON     string          `json:"ours_json"`
	TheirsJSON   string          `json:"theirs_json"`
}

type restoreStashRetryPreimageV2 struct {
	SchemaVersion     int                       `json:"schema_version"`
	Action            string                    `json:"action"`
	Outcome           string                    `json:"outcome"`
	Scope             types.WorkspaceScope      `json:"scope"`
	RequestID         string                    `json:"request_id"`
	RequestDigest     state.Digest              `json:"request_digest"`
	StashID           string                    `json:"stash_id"`
	WorkspaceRevision int64                     `json:"workspace_revision"`
	Binding           workspaceBindingDigestV1  `json:"binding"`
	AcceptedDigest    state.Digest              `json:"accepted_digest"`
	Status            string                    `json:"status"`
	Candidate         *restoreRetryCandidateV2  `json:"candidate"`
	CurrentOperations []restoreRetryOperationV2 `json:"current_operations"`
	Stash             restoreRetryStashV2       `json:"stash"`
	StashOperations   []restoreRetryOperationV2 `json:"stash_operations"`
	OpenConflicts     []restoreRetryConflictV2  `json:"open_conflicts"`
}

func restoreStashRetryDigestV2(
	req RestoreStashRequest,
	requestDigest state.Digest,
	workspaceRevision int64,
	persisted localstore.WorkspaceRestoreCurrentState,
) (state.Digest, error) {
	expectedDigest, err := restoreRequestDigest(req)
	if err != nil {
		return "", err
	}
	if requestDigest != expectedDigest || !validImportDigest(requestDigest) || workspaceRevision < 1 ||
		persisted.Workspace.Binding.Scope != req.Scope || persisted.Stash.StashID != req.StashID {
		return "", fmt.Errorf("projectstate: restore retry v2 authority mismatch")
	}
	if err := validateRestoreCurrentStatusConflictCoherence(persisted); err != nil {
		return "", err
	}
	if persisted.Workspace.State != "conflicted" {
		return "", fmt.Errorf("projectstate: restore retry v2 workspace is not conflicted")
	}
	plan, err := buildRestoreCurrentPlan(persisted)
	if err != nil {
		return "", fmt.Errorf("projectstate: prove restore retry v2 semantics: %w", err)
	}
	if len(plan.Result.Conflicts) == 0 || len(plan.ConflictEvidence) != len(persisted.OpenConflicts) {
		return "", fmt.Errorf("projectstate: restore retry v2 conflict membership mismatch")
	}
	for index := range plan.ConflictEvidence {
		if persisted.OpenConflicts[index].WorkspaceConflictEvidence != plan.ConflictEvidence[index] {
			return "", fmt.Errorf("projectstate: restore retry v2 conflict evidence mismatch")
		}
	}
	candidate := projectRestoreRetryCandidateV2(persisted.Candidate)
	currentOperations, err := projectRestoreRetryOperationsV2(persisted.CurrentOperations)
	if err != nil {
		return "", err
	}
	stashOperations, err := projectRestoreRetryOperationsV2(persisted.StashOperations)
	if err != nil {
		return "", err
	}
	replay, err := decodeStashReplay(persisted.Stash.OperationsJSON, persisted.Workspace.Binding, persisted.Stash.ThroughGeneration)
	if err != nil {
		return "", err
	}
	conflicts := make([]restoreRetryConflictV2, len(persisted.OpenConflicts))
	for index, row := range persisted.OpenConflicts {
		conflicts[index] = restoreRetryConflictV2{
			OccurrenceID: row.OccurrenceID, ConflictID: row.ConflictID, Key: row.Key,
			FieldPath: row.FieldPath, ConflictKind: row.ConflictKind,
			BaseJSON: row.BaseJSON, OursJSON: row.OursJSON, TheirsJSON: row.TheirsJSON,
		}
	}
	binding, err := workspaceBindingDigest(persisted.Workspace.Binding)
	if err != nil {
		return "", err
	}
	preimage := restoreStashRetryPreimageV2{
		SchemaVersion: 2, Action: "restore", Outcome: "conflicted", Scope: req.Scope,
		RequestID: req.RequestID, RequestDigest: requestDigest, StashID: req.StashID,
		WorkspaceRevision: workspaceRevision, Binding: binding, AcceptedDigest: persisted.Workspace.Snapshot.Digest,
		Status: persisted.Workspace.State, Candidate: candidate,
		CurrentOperations: currentOperations,
		Stash: restoreRetryStashV2{
			StashID: persisted.Stash.StashID, SourceBaseDigest: persisted.Stash.SourceBaseDigest,
			CandidateDigest: persisted.Stash.CandidateDigest, Operations: replay,
			ThroughGeneration: persisted.Stash.ThroughGeneration, Actor: persisted.Stash.Actor,
			Label: persisted.Stash.Label,
		},
		StashOperations: stashOperations, OpenConflicts: conflicts,
	}
	return state.DigestCanonicalJSON(preimage)
}

func projectRestoreRetryCandidateV2(candidate *localstore.WorkspaceCandidateRecord) *restoreRetryCandidateV2 {
	if candidate == nil {
		return nil
	}
	projected := &restoreRetryCandidateV2{
		AcceptedBaseDigest: candidate.AcceptedBaseDigest, WorkingTreeDigest: candidate.WorkingTreeDigest,
		DirectSnapshotDigest:     candidate.DirectSnapshot.Digest,
		RebasedThroughGeneration: candidate.RebasedThroughGeneration, ImportedBy: candidate.ImportedBy,
	}
	if candidate.RebasedSnapshot != nil {
		digest := candidate.RebasedSnapshot.Digest
		projected.RebasedSnapshotDigest = &digest
	}
	return projected
}

func projectRestoreRetryOperationsV2(rows []localstore.WorkspaceOperation) ([]restoreRetryOperationV2, error) {
	decoded, err := validateRestoreOperationRows(rows)
	if err != nil {
		return nil, err
	}
	result := make([]restoreRetryOperationV2, len(decoded))
	for index, operation := range decoded {
		row := operation.row
		result[index] = restoreRetryOperationV2{
			Generation: row.Generation, OperationID: row.OperationID,
			OperationJSON: string(row.OperationJSON), State: row.State,
		}
		if row.StashedByStashID != nil {
			owner := *row.StashedByStashID
			result[index].StashedByStashID = &owner
		}
	}
	return result, nil
}

func validateRestoreCurrentStatusConflictCoherence(persisted localstore.WorkspaceRestoreCurrentState) error {
	if (persisted.Workspace.State == "conflicted") != (len(persisted.OpenConflicts) != 0) {
		return fmt.Errorf("projectstate: restore workspace status and open conflicts are incoherent")
	}
	return nil
}

func validateConflictedRestoreCurrentTransition(
	before, after localstore.WorkspaceRestoreCurrentState,
	persistedConflicts []localstore.WorkspaceConflictOccurrence,
) error {
	if persistedConflicts == nil || len(persistedConflicts) == 0 {
		return fmt.Errorf("projectstate: conflicted restore transition conflicts are incomplete")
	}
	if err := validateRestoreCurrentStatusConflictCoherence(before); err != nil {
		return err
	}
	if err := validateRestoreCurrentStatusConflictCoherence(after); err != nil {
		return err
	}
	if after.Workspace.State != "conflicted" || !reflect.DeepEqual(after.OpenConflicts, persistedConflicts) {
		return fmt.Errorf("projectstate: conflicted restore transition post-state mismatch")
	}
	beforeWorkspace, afterWorkspace := before.Workspace, after.Workspace
	beforeWorkspace.State, afterWorkspace.State = "", ""
	if !reflect.DeepEqual(beforeWorkspace, afterWorkspace) || !reflect.DeepEqual(before.Candidate, after.Candidate) ||
		!reflect.DeepEqual(before.CurrentOperations, after.CurrentOperations) ||
		!reflect.DeepEqual(before.Stash, after.Stash) || !reflect.DeepEqual(before.StashOperations, after.StashOperations) {
		return fmt.Errorf("projectstate: conflicted restore transition changed protected current state")
	}
	return nil
}

func workspaceBindingDigest(binding types.WorkspaceBinding) (workspaceBindingDigestV1, error) {
	if err := binding.Validate(); err != nil {
		return workspaceBindingDigestV1{}, fmt.Errorf("projectstate: invalid workspace binding digest projection: %w", err)
	}
	return workspaceBindingDigestV1{
		Scope: binding.Scope,
		Checkout: checkoutIdentityDigestV1{
			CanonicalPath: binding.Checkout.CanonicalPath, Device: binding.Checkout.Device, Inode: binding.Checkout.Inode,
		},
		Repository: binding.Repository, AcceptedRef: binding.AcceptedRef,
		AcceptedCommitSHA: binding.AcceptedCommitSHA, AcceptedTreeDigest: binding.AcceptedTreeDigest,
	}, nil
}
