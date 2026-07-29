package projectstate

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

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

type restoreRetryBindingV1 struct {
	Binding                    workspaceBindingDigestV1 `json:"binding"`
	Status                     string                   `json:"status"`
	CreatedAt                  time.Time                `json:"created_at"`
	UpdatedAt                  time.Time                `json:"updated_at"`
	AcceptedSnapshotBlobDigest state.Digest             `json:"accepted_snapshot_blob_digest"`
}

type restoreRetryCandidateV1 struct {
	AcceptedBaseDigest       state.Digest  `json:"accepted_base_digest"`
	WorkingTreeDigest        state.Digest  `json:"working_tree_digest"`
	DirectTreeBlobDigest     state.Digest  `json:"direct_tree_blob_digest"`
	RebasedTreeBlobDigest    *state.Digest `json:"rebased_tree_blob_digest"`
	RebasedThroughGeneration int64         `json:"rebased_through_generation"`
	ImportedBy               string        `json:"imported_by"`
	ImportedAt               time.Time     `json:"imported_at"`
}

type restoreRetryOperationV1 struct {
	Generation       int64     `json:"generation"`
	OperationID      string    `json:"operation_id"`
	OperationJSON    string    `json:"operation_json"`
	State            string    `json:"state"`
	StashedByStashID *string   `json:"stashed_by_stash_id"`
	CreatedAt        time.Time `json:"created_at"`
}

type restoreRetryStashV1 struct {
	StashID                string       `json:"stash_id"`
	SourceBaseDigest       state.Digest `json:"source_base_digest"`
	CandidateDigest        state.Digest `json:"candidate_digest"`
	SourceTreeBlobDigest   state.Digest `json:"source_tree_blob_digest"`
	ComposedTreeBlobDigest state.Digest `json:"composed_tree_blob_digest"`
	OperationsJSON         string       `json:"operations_json"`
	ThroughGeneration      int64        `json:"through_generation"`
	ActorJSON              string       `json:"actor_json"`
	Label                  string       `json:"label"`
	CreatedAt              time.Time    `json:"created_at"`
}

type restoreRetryConflictOccurrenceV1 struct {
	OccurrenceID string    `json:"occurrence_id"`
	ConflictID   string    `json:"conflict_id"`
	RecordKind   string    `json:"record_kind"`
	RecordID     string    `json:"record_id"`
	FieldPath    string    `json:"field_path"`
	ConflictKind string    `json:"conflict_kind"`
	BaseJSON     string    `json:"base_json"`
	OursJSON     string    `json:"ours_json"`
	TheirsJSON   string    `json:"theirs_json"`
	CreatedAt    time.Time `json:"created_at"`
}

type restoreStashRetryPreimageV1 struct {
	SchemaVersion int                                `json:"schema_version"`
	Action        string                             `json:"action"`
	Outcome       string                             `json:"outcome"`
	Scope         types.WorkspaceScope               `json:"scope"`
	RequestID     string                             `json:"request_id"`
	RequestDigest state.Digest                       `json:"request_digest"`
	StashID       string                             `json:"stash_id"`
	Binding       restoreRetryBindingV1              `json:"binding"`
	Candidate     *restoreRetryCandidateV1           `json:"candidate"`
	Operations    []restoreRetryOperationV1          `json:"operations"`
	Stash         restoreRetryStashV1                `json:"stash"`
	OpenConflicts []restoreRetryConflictOccurrenceV1 `json:"open_conflicts"`
}

func buildRestoreStashRetryPreimage(req RestoreStashRequest, requestDigest state.Digest, persisted localstore.WorkspaceRestoreRetryState) (restoreStashRetryPreimageV1, error) {
	expectedDigest, err := restoreRequestDigest(req)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	if !validImportDigest(requestDigest) || requestDigest != expectedDigest {
		return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: restore retry request digest mismatch")
	}
	if persisted.Workspace.Binding.Scope != req.Scope || persisted.Stash.StashID != req.StashID {
		return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: restore retry scope or stash mismatch")
	}
	binding, err := projectRestoreRetryBinding(persisted)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	candidate, err := projectRestoreRetryCandidate(persisted)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	operations, err := projectRestoreRetryOperations(persisted.Operations)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	stash, err := projectRestoreRetryStash(persisted)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	conflicts, err := projectRestoreRetryConflicts(persisted.OpenConflicts)
	if err != nil {
		return restoreStashRetryPreimageV1{}, err
	}
	if len(conflicts) == 0 {
		return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: conflicted restore retry has no open conflicts")
	}
	plan, err := buildRestorePlan(persisted)
	if err != nil {
		return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: prove restore retry semantics: %w", err)
	}
	if len(plan.Result.Conflicts) == 0 || len(plan.ConflictEvidence) != len(persisted.OpenConflicts) {
		return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: restore retry semantic conflict membership mismatch")
	}
	for index, evidence := range plan.ConflictEvidence {
		if persisted.OpenConflicts[index].WorkspaceConflictEvidence != evidence {
			return restoreStashRetryPreimageV1{}, fmt.Errorf("projectstate: restore retry semantic conflict evidence mismatch")
		}
	}
	return restoreStashRetryPreimageV1{
		SchemaVersion: 1, Action: "restore", Outcome: "conflicted", Scope: req.Scope,
		RequestID: req.RequestID, RequestDigest: requestDigest, StashID: req.StashID,
		Binding: binding, Candidate: candidate, Operations: operations, Stash: stash,
		OpenConflicts: conflicts,
	}, nil
}

func restoreStashRetryDigest(req RestoreStashRequest, requestDigest state.Digest, persisted localstore.WorkspaceRestoreRetryState) (state.Digest, error) {
	preimage, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
	if err != nil {
		return "", err
	}
	return state.DigestCanonicalJSON(preimage)
}

func validateConflictedRestoreTransition(before, after localstore.WorkspaceRestoreRetryState, persistedConflicts []localstore.WorkspaceConflictOccurrence) error {
	if persistedConflicts == nil {
		return fmt.Errorf("projectstate: conflicted restore transition conflicts are nil")
	}
	if _, err := projectRestoreRetryConflicts(persistedConflicts); err != nil || len(persistedConflicts) == 0 {
		if err == nil {
			err = fmt.Errorf("conflicts are empty")
		}
		return fmt.Errorf("projectstate: invalid conflicted restore transition conflicts: %w", err)
	}
	if before.Operations == nil || before.OpenConflicts == nil {
		return fmt.Errorf("projectstate: conflicted restore transition before state is incomplete")
	}
	if (before.Workspace.State == "conflicted") != (len(before.OpenConflicts) != 0) {
		return fmt.Errorf("projectstate: conflicted restore transition before state is incoherent")
	}
	if err := validateRestoreRetryState(before, false); err != nil {
		return fmt.Errorf("projectstate: invalid conflicted restore transition before state: %w", err)
	}
	if err := validateRestoreRetryState(after, true); err != nil {
		return fmt.Errorf("projectstate: invalid conflicted restore transition after state: %w", err)
	}
	if after.Workspace.State != "conflicted" || !reflect.DeepEqual(after.OpenConflicts, persistedConflicts) {
		return fmt.Errorf("projectstate: conflicted restore transition post-state mismatch")
	}
	if after.BindingUpdatedAt.Before(before.BindingUpdatedAt) {
		return fmt.Errorf("projectstate: conflicted restore transition moved updated_at backwards")
	}
	if !equalConflictedRestoreProtectedState(before, after) {
		return fmt.Errorf("projectstate: conflicted restore transition changed protected state")
	}
	return nil
}

func projectRestoreRetryBinding(persisted localstore.WorkspaceRestoreRetryState) (restoreRetryBindingV1, error) {
	if err := validateRestoreRetryBindingState(persisted, true); err != nil {
		return restoreRetryBindingV1{}, err
	}
	binding := persisted.Workspace.Binding
	projectedBinding, err := workspaceBindingDigest(binding)
	if err != nil {
		return restoreRetryBindingV1{}, err
	}
	return restoreRetryBindingV1{
		Binding: projectedBinding,
		Status:  persisted.Workspace.State, CreatedAt: persisted.BindingCreatedAt.UTC(),
		UpdatedAt: persisted.BindingUpdatedAt.UTC(), AcceptedSnapshotBlobDigest: persisted.AcceptedSnapshotBlobDigest,
	}, nil
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

func validateRestoreRetryBindingState(persisted localstore.WorkspaceRestoreRetryState, requireConflicted bool) error {
	binding := persisted.Workspace.Binding
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("projectstate: invalid restore retry binding: %w", err)
	}
	switch persisted.Workspace.State {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return fmt.Errorf("projectstate: invalid restore retry binding status")
	}
	if requireConflicted && persisted.Workspace.State != "conflicted" {
		return fmt.Errorf("projectstate: restore retry binding is not conflicted")
	}
	if !validRetryTime(persisted.BindingCreatedAt) || !validRetryTime(persisted.BindingUpdatedAt) ||
		persisted.BindingUpdatedAt.Before(persisted.BindingCreatedAt) || !validImportDigest(persisted.AcceptedSnapshotBlobDigest) {
		return fmt.Errorf("projectstate: invalid restore retry binding state")
	}
	acceptedTree, err := state.EncodeTree(persisted.Workspace.Snapshot)
	if err != nil || validateMatchingTree(acceptedTree, state.Digest(binding.AcceptedTreeDigest), binding) != nil {
		return fmt.Errorf("projectstate: invalid restore retry accepted snapshot")
	}
	return nil
}

func projectRestoreRetryCandidate(persisted localstore.WorkspaceRestoreRetryState) (*restoreRetryCandidateV1, error) {
	candidate := persisted.Candidate
	if candidate == nil {
		if persisted.CandidateDirectTreeBlobDigest != nil || persisted.CandidateRebasedTreeBlobDigest != nil {
			return nil, fmt.Errorf("projectstate: absent restore retry candidate has blob digests")
		}
		return nil, nil
	}
	if persisted.CandidateDirectTreeBlobDigest == nil || !validImportDigest(*persisted.CandidateDirectTreeBlobDigest) ||
		(candidate.RebasedSnapshot == nil) != (persisted.CandidateRebasedTreeBlobDigest == nil) {
		return nil, fmt.Errorf("projectstate: invalid restore retry candidate blob-digest shape")
	}
	if persisted.CandidateRebasedTreeBlobDigest != nil && !validImportDigest(*persisted.CandidateRebasedTreeBlobDigest) {
		return nil, fmt.Errorf("projectstate: invalid restore retry rebased blob digest")
	}
	if err := validateStashCandidate(candidate, state.Digest(persisted.Workspace.Binding.AcceptedTreeDigest), persisted.Workspace.Binding); err != nil {
		return nil, fmt.Errorf("projectstate: invalid restore retry candidate: %w", err)
	}
	if !types.CanonicalUUID(candidate.ImportedBy) || !validRetryTime(candidate.ImportedAt) {
		return nil, fmt.Errorf("projectstate: invalid restore retry candidate attribution")
	}
	projected := &restoreRetryCandidateV1{
		AcceptedBaseDigest: candidate.AcceptedBaseDigest, WorkingTreeDigest: candidate.WorkingTreeDigest,
		DirectTreeBlobDigest:     *persisted.CandidateDirectTreeBlobDigest,
		RebasedThroughGeneration: candidate.RebasedThroughGeneration,
		ImportedBy:               candidate.ImportedBy, ImportedAt: candidate.ImportedAt.UTC(),
	}
	if persisted.CandidateRebasedTreeBlobDigest != nil {
		digest := *persisted.CandidateRebasedTreeBlobDigest
		projected.RebasedTreeBlobDigest = &digest
	}
	return projected, nil
}

func projectRestoreRetryOperations(records []localstore.WorkspaceOperationAuditRecord) ([]restoreRetryOperationV1, error) {
	audit, err := validateRestoreAudit(records)
	if err != nil {
		return nil, err
	}
	operations := make([]restoreRetryOperationV1, len(audit))
	for index, decoded := range audit {
		row := decoded.row
		operations[index] = restoreRetryOperationV1{
			Generation: row.Generation, OperationID: row.OperationID, OperationJSON: string(row.OperationJSON),
			State: row.State, CreatedAt: records[index].CreatedAt.UTC(),
		}
		if row.StashedByStashID != nil {
			owner := *row.StashedByStashID
			operations[index].StashedByStashID = &owner
		}
	}
	return operations, nil
}

func projectRestoreRetryStash(persisted localstore.WorkspaceRestoreRetryState) (restoreRetryStashV1, error) {
	stash := persisted.Stash
	if !canonicalUUIDv4(stash.StashID) || !validImportDigest(stash.SourceBaseDigest) ||
		!validImportDigest(stash.CandidateDigest) || !validImportDigest(persisted.StashSourceTreeBlobDigest) ||
		!validImportDigest(persisted.StashComposedTreeBlobDigest) || stash.ThroughGeneration < 0 ||
		!validRetryOpaqueText(stash.OperationsJSON) || !validRetryLabel(stash.Label) || !validRetryTime(stash.CreatedAt) {
		return restoreRetryStashV1{}, fmt.Errorf("projectstate: invalid restore retry stash")
	}
	if err := stash.Actor.ValidateHistorical(); err != nil {
		return restoreRetryStashV1{}, fmt.Errorf("projectstate: invalid restore retry stash actor: %w", err)
	}
	actorJSON, err := state.CanonicalJSON(stash.Actor)
	if err != nil || !bytes.Equal(actorJSON, []byte(stash.ActorJSON)) {
		return restoreRetryStashV1{}, fmt.Errorf("projectstate: restore retry stash actor bytes are not canonical")
	}
	return restoreRetryStashV1{
		StashID: stash.StashID, SourceBaseDigest: stash.SourceBaseDigest, CandidateDigest: stash.CandidateDigest,
		SourceTreeBlobDigest:   persisted.StashSourceTreeBlobDigest,
		ComposedTreeBlobDigest: persisted.StashComposedTreeBlobDigest,
		OperationsJSON:         stash.OperationsJSON, ThroughGeneration: stash.ThroughGeneration,
		ActorJSON: stash.ActorJSON, Label: stash.Label, CreatedAt: stash.CreatedAt.UTC(),
	}, nil
}

func projectRestoreRetryConflicts(records []localstore.WorkspaceConflictOccurrence) ([]restoreRetryConflictOccurrenceV1, error) {
	if records == nil {
		return nil, fmt.Errorf("projectstate: restore retry open conflicts are nil")
	}
	if _, err := decodeWorkspaceConflictOccurrences(records); err != nil {
		return nil, err
	}
	conflicts := make([]restoreRetryConflictOccurrenceV1, len(records))
	for index, row := range records {
		conflicts[index] = restoreRetryConflictOccurrenceV1{
			OccurrenceID: row.OccurrenceID, ConflictID: row.ConflictID,
			RecordKind: row.Key.Kind, RecordID: row.Key.ID, FieldPath: row.FieldPath,
			ConflictKind: row.ConflictKind, BaseJSON: row.BaseJSON, OursJSON: row.OursJSON,
			TheirsJSON: row.TheirsJSON, CreatedAt: row.CreatedAt.UTC(),
		}
	}
	return conflicts, nil
}

func validateRestoreRetryState(persisted localstore.WorkspaceRestoreRetryState, requireConflicted bool) error {
	if err := validateRestoreRetryBindingState(persisted, requireConflicted); err != nil {
		return err
	}
	if _, err := projectRestoreRetryCandidate(persisted); err != nil {
		return err
	}
	if _, err := projectRestoreRetryOperations(persisted.Operations); err != nil {
		return err
	}
	if _, err := projectRestoreRetryStash(persisted); err != nil {
		return err
	}
	if _, err := projectRestoreRetryConflicts(persisted.OpenConflicts); err != nil {
		return err
	}
	plan, err := buildRestorePlan(persisted)
	if err != nil {
		return err
	}
	if !requireConflicted {
		return nil
	}
	if len(plan.ConflictEvidence) == 0 || len(plan.ConflictEvidence) != len(persisted.OpenConflicts) {
		return fmt.Errorf("projectstate: restore retry conflict membership mismatch")
	}
	for index, evidence := range plan.ConflictEvidence {
		if persisted.OpenConflicts[index].WorkspaceConflictEvidence != evidence {
			return fmt.Errorf("projectstate: restore retry conflict evidence mismatch")
		}
	}
	return nil
}

func equalConflictedRestoreProtectedState(before, after localstore.WorkspaceRestoreRetryState) bool {
	beforeWorkspace, afterWorkspace := before.Workspace, after.Workspace
	beforeWorkspace.State, afterWorkspace.State = "", ""
	return reflect.DeepEqual(beforeWorkspace, afterWorkspace) &&
		before.BindingCreatedAt.Equal(after.BindingCreatedAt) &&
		before.AcceptedSnapshotBlobDigest == after.AcceptedSnapshotBlobDigest &&
		reflect.DeepEqual(before.Candidate, after.Candidate) &&
		equalRetryDigestPointer(before.CandidateDirectTreeBlobDigest, after.CandidateDirectTreeBlobDigest) &&
		equalRetryDigestPointer(before.CandidateRebasedTreeBlobDigest, after.CandidateRebasedTreeBlobDigest) &&
		reflect.DeepEqual(before.Operations, after.Operations) && reflect.DeepEqual(before.Stash, after.Stash) &&
		before.StashSourceTreeBlobDigest == after.StashSourceTreeBlobDigest &&
		before.StashComposedTreeBlobDigest == after.StashComposedTreeBlobDigest
}

func equalRetryDigestPointer(left, right *state.Digest) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func validRetryTime(value time.Time) bool {
	return !value.IsZero() && zeroOffsetTime(value)
}

func validRetryOpaqueText(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validRetryLabel(value string) bool {
	return utf8.ValidString(value) && len(value) >= 1 && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}
