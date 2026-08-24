package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const workspaceCommitConfirmationVersion = 1

type workspaceCommitTargetKind string

const (
	workspaceCommitMaterialization workspaceCommitTargetKind = "materialization"
	workspaceCommitPublication     workspaceCommitTargetKind = "publication"
)

// WorkspaceCommitConfirmation is an opaque, compact proof for one projected
// workspace commit. Task 12 produces materialization proofs; Task 13 adds the
// publication projection carried by the same envelope.
type WorkspaceCommitConfirmation struct {
	formatVersion   int
	scope           types.WorkspaceScope
	revision        int64
	targetKind      workspaceCommitTargetKind
	targetID        string
	targetState     string
	currentOwnerID  string
	transitionClass WorkspacePublicationTransitionClass
	authorityDigest projectstate.Digest
	postimageDigest *projectstate.Digest
}

type WorkspacePublicationTransitionClass string

const (
	WorkspacePublicationConfigured         WorkspacePublicationTransitionClass = "configured"
	WorkspacePublicationStickyInvalidation WorkspacePublicationTransitionClass = "sticky_invalidation"
)

type materializationCommitAuthorityV1 struct {
	SchemaVersion            int                    `json:"schema_version"`
	Scope                    types.WorkspaceScope   `json:"scope"`
	JournalID                string                 `json:"journal_id"`
	Present                  bool                   `json:"present"`
	State                    string                 `json:"state"`
	ExpectedLiveDigest       projectstate.Digest    `json:"expected_live_digest"`
	AcceptedBaseDigest       projectstate.Digest    `json:"accepted_base_digest"`
	Checkout                 types.CheckoutIdentity `json:"checkout"`
	PriorTreeDigest          projectstate.Digest    `json:"prior_tree_digest"`
	CandidateDigest          projectstate.Digest    `json:"candidate_digest"`
	ThroughGeneration        int64                  `json:"through_generation"`
	StageChild               string                 `json:"stage_child"`
	BackupChild              string                 `json:"backup_child"`
	IncludedOperationsDigest *projectstate.Digest   `json:"included_operations_digest"`
	PublicationReviewDigest  *projectstate.Digest   `json:"publication_review_digest"`
	PriorCandidateDigest     *projectstate.Digest   `json:"prior_candidate_digest"`
}

type materializationCandidatePostimageV1 struct {
	AcceptedBaseDigest       projectstate.Digest    `json:"accepted_base_digest"`
	WorkingTreeDigest        projectstate.Digest    `json:"working_tree_digest"`
	DirectSnapshot           projectstate.Snapshot  `json:"direct_snapshot"`
	RebasedSnapshot          *projectstate.Snapshot `json:"rebased_snapshot"`
	RebasedThroughGeneration int64                  `json:"rebased_through_generation"`
	ImportedBy               string                 `json:"imported_by"`
	ImportedAt               time.Time              `json:"imported_at"`
}

type materializationOperationPostimageV1 struct {
	Generation       int64   `json:"generation"`
	OperationID      string  `json:"operation_id"`
	OperationJSON    string  `json:"operation_json"`
	State            string  `json:"state"`
	StashedByStashID *string `json:"stashed_by_stash_id"`
}

type materializationCommitPostimageV1 struct {
	SchemaVersion int                                   `json:"schema_version"`
	Status        string                                `json:"status"`
	Candidate     *materializationCandidatePostimageV1  `json:"candidate"`
	Operations    []materializationOperationPostimageV1 `json:"operations"`
}

type materializationNoPostimageV1 struct {
	SchemaVersion int  `json:"schema_version"`
	Absent        bool `json:"absent"`
}

type WorkspaceCommitMatch uint8

const (
	WorkspaceCommitThird WorkspaceCommitMatch = iota
	WorkspaceCommitPrior
	WorkspaceCommitNext
)

type materializationOwnedOperationV1 struct {
	Generation          int64  `json:"generation"`
	OperationID         string `json:"operation_id"`
	OperationJSON       string `json:"operation_json"`
	PrepublicationState string `json:"prepublication_state"`
}

type materializationOwnedOperationsV1 struct {
	SchemaVersion            int                               `json:"schema_version"`
	InitialThroughGeneration int64                             `json:"initial_through_generation"`
	Operations               []materializationOwnedOperationV1 `json:"operations"`
}

// MaterializationByJournalID reads one exact journal in the transaction scope.
// It deliberately admits terminal rows and never scans journal history.
func (tx *WorkspaceMutationTx) MaterializationByJournalID(ctx context.Context, journalID string) (*WorkspaceMaterializationRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) || !validLegacyMaterializationJournalID(journalID) {
		return nil, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: validate targeted materialization workspace: %w", err)
	}
	record, err := scanWorkspaceMaterialization(tx.conn.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       created_at,updated_at,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(stage_path),typeof(backup_path),typeof(created_at),typeof(updated_at),
		       included_operations_json,typeof(included_operations_json),
		       publication_review_proof_version,typeof(publication_review_proof_version),publication_review_json,typeof(publication_review_json),
		       prior_candidate_json,typeof(prior_candidate_json)
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=? AND journal_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, journalID), tx.scope, workspace.Binding, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: read targeted materialization: %w", err)
	}
	cloned := cloneWorkspaceMaterializationRecord(*record)
	return &cloned, nil
}

// CaptureMaterializationCommitConfirmation captures only the named journal,
// current owner, workspace revision, and journal-owned logical postimage.
func (tx *WorkspaceMutationTx) CaptureMaterializationCommitConfirmation(ctx context.Context, journalID string) (WorkspaceCommitConfirmation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) || !validLegacyMaterializationJournalID(journalID) {
		return WorkspaceCommitConfirmation{}, ErrNotFound
	}
	target, err := tx.MaterializationByJournalID(ctx, journalID)
	if err != nil {
		return WorkspaceCommitConfirmation{}, err
	}
	current, err := tx.CurrentMaterialization(ctx)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: read materialization commit owner: %w", err)
	}
	revision, err := tx.projectedWorkspaceRevision(ctx)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: project materialization commit revision: %w", err)
	}

	confirmation := WorkspaceCommitConfirmation{
		formatVersion: workspaceCommitConfirmationVersion,
		scope:         tx.scope, revision: revision, targetKind: workspaceCommitMaterialization,
		targetID: journalID,
	}
	if current != nil {
		confirmation.currentOwnerID = current.JournalID
	}
	if target == nil {
		confirmation.authorityDigest, err = projectstate.DigestCanonicalJSON(materializationCommitAuthorityV1{
			SchemaVersion: 1, Scope: tx.scope, JournalID: journalID, Present: false,
		})
		if err != nil {
			return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: digest absent materialization authority: %w", err)
		}
		postimage, digestErr := projectstate.DigestCanonicalJSON(materializationNoPostimageV1{SchemaVersion: 1, Absent: true})
		if digestErr != nil {
			return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: digest absent materialization postimage: %w", digestErr)
		}
		confirmation.postimageDigest = &postimage
		if !validObservedWorkspaceCommitConfirmation(confirmation) {
			return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: captured malformed absent materialization confirmation")
		}
		return confirmation, nil
	}

	confirmation.targetState = target.State
	authority, generations, err := materializationCommitAuthority(tx.scope, *target)
	if err != nil {
		return WorkspaceCommitConfirmation{}, err
	}
	confirmation.authorityDigest, err = projectstate.DigestCanonicalJSON(authority)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: digest materialization authority: %w", err)
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: read materialization postimage status: %w", err)
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: read materialization postimage candidate: %w", err)
	}
	operations, err := tx.OperationsByGenerations(ctx, generations)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: read materialization postimage operations: %w", err)
	}
	postimage := materializationCommitPostimageV1{
		SchemaVersion: 1, Status: workspace.State,
		Candidate:  materializationCandidatePostimage(candidate),
		Operations: make([]materializationOperationPostimageV1, len(operations)),
	}
	for index := range operations {
		postimage.Operations[index] = materializationOperationPostimageV1{
			Generation: operations[index].Generation, OperationID: operations[index].OperationID,
			OperationJSON: string(operations[index].OperationJSON), State: operations[index].State,
			StashedByStashID: cloneWorkspaceCommitOptionalString(operations[index].StashedByStashID),
		}
	}
	postimageDigest, err := projectstate.DigestCanonicalJSON(postimage)
	if err != nil {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: digest materialization postimage: %w", err)
	}
	confirmation.postimageDigest = &postimageDigest
	if !validObservedWorkspaceCommitConfirmation(confirmation) {
		return WorkspaceCommitConfirmation{}, fmt.Errorf("localstore: captured malformed materialization confirmation")
	}
	return confirmation, nil
}

func materializationCommitAuthority(scope types.WorkspaceScope, record WorkspaceMaterializationRecord) (materializationCommitAuthorityV1, []int64, error) {
	includedDigest, err := optionalCanonicalJSONDigest(record.IncludedOperationsJSON)
	if err != nil {
		return materializationCommitAuthorityV1{}, nil, fmt.Errorf("localstore: digest included operations authority: %w", err)
	}
	reviewDigest, err := optionalCanonicalJSONDigest(record.PublicationReviewJSON)
	if err != nil {
		return materializationCommitAuthorityV1{}, nil, fmt.Errorf("localstore: digest publication review authority: %w", err)
	}
	priorDigest, err := optionalCanonicalJSONDigest(record.PriorCandidateJSON)
	if err != nil {
		return materializationCommitAuthorityV1{}, nil, fmt.Errorf("localstore: digest prior candidate authority: %w", err)
	}
	generations, err := materializationOwnedGenerations(record.IncludedOperationsJSON)
	if err != nil {
		return materializationCommitAuthorityV1{}, nil, err
	}
	return materializationCommitAuthorityV1{
		SchemaVersion: 1, Scope: scope, JournalID: record.JournalID, Present: true, State: record.State,
		ExpectedLiveDigest: record.ExpectedLiveDigest, AcceptedBaseDigest: record.AcceptedBaseDigest,
		Checkout: record.Checkout, PriorTreeDigest: record.PriorTreeDigest, CandidateDigest: record.CandidateDigest,
		ThroughGeneration: record.ThroughGeneration, StageChild: filepath.Base(record.StagePath),
		BackupChild: filepath.Base(record.BackupPath), IncludedOperationsDigest: includedDigest,
		PublicationReviewDigest: reviewDigest, PriorCandidateDigest: priorDigest,
	}, generations, nil
}

func optionalCanonicalJSONDigest(raw *string) (*projectstate.Digest, error) {
	if raw == nil {
		return nil, nil
	}
	digest, err := projectstate.DigestCanonicalJSON(json.RawMessage(*raw))
	if err != nil {
		return nil, err
	}
	return &digest, nil
}

func materializationOwnedGenerations(raw *string) ([]int64, error) {
	if raw == nil {
		return []int64{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(*raw))
	decoder.DisallowUnknownFields()
	var envelope materializationOwnedOperationsV1
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("localstore: decode materialization operation authority: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("localstore: materialization operation authority has trailing JSON")
	}
	canonical, err := projectstate.CanonicalJSON(envelope)
	if err != nil || !bytes.Equal(canonical, []byte(*raw)) || envelope.SchemaVersion != 1 || envelope.Operations == nil || envelope.InitialThroughGeneration < 0 {
		return nil, fmt.Errorf("localstore: invalid canonical materialization operation authority")
	}
	generations := make([]int64, len(envelope.Operations))
	seenIDs := make(map[string]struct{}, len(envelope.Operations))
	for index, operation := range envelope.Operations {
		if operation.Generation <= 0 || (index > 0 && operation.Generation <= envelope.Operations[index-1].Generation) ||
			!types.CanonicalUUID(operation.OperationID) {
			return nil, fmt.Errorf("localstore: invalid materialization operation membership")
		}
		if _, duplicate := seenIDs[operation.OperationID]; duplicate {
			return nil, fmt.Errorf("localstore: duplicate materialization operation membership")
		}
		seenIDs[operation.OperationID] = struct{}{}
		switch operation.PrepublicationState {
		case "rebased":
			if operation.Generation > envelope.InitialThroughGeneration {
				return nil, fmt.Errorf("localstore: rebased materialization operation exceeds boundary")
			}
		case "active":
			if operation.Generation <= envelope.InitialThroughGeneration {
				return nil, fmt.Errorf("localstore: active materialization operation does not exceed boundary")
			}
		default:
			return nil, fmt.Errorf("localstore: invalid materialization operation prepublication state")
		}
		decoded, err := projectstate.DecodeOperation([]byte(operation.OperationJSON))
		if err != nil {
			return nil, fmt.Errorf("localstore: decode materialization operation authority: %w", err)
		}
		canonicalOperation, err := projectstate.CanonicalOperation(decoded)
		if err != nil || decoded.ID != operation.OperationID || !bytes.Equal(canonicalOperation, []byte(operation.OperationJSON)) {
			return nil, fmt.Errorf("localstore: materialization operation authority identity mismatch")
		}
		generations[index] = operation.Generation
	}
	return generations, nil
}

func materializationCandidatePostimage(candidate *WorkspaceCandidateRecord) *materializationCandidatePostimageV1 {
	if candidate == nil {
		return nil
	}
	return &materializationCandidatePostimageV1{
		AcceptedBaseDigest: candidate.AcceptedBaseDigest, WorkingTreeDigest: candidate.WorkingTreeDigest,
		DirectSnapshot: candidate.DirectSnapshot, RebasedSnapshot: candidate.RebasedSnapshot,
		RebasedThroughGeneration: candidate.RebasedThroughGeneration,
		ImportedBy:               candidate.ImportedBy, ImportedAt: candidate.ImportedAt,
	}
}

func cloneWorkspaceCommitOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// ConfirmWorkspaceCommit confirms one compact target using a fresh coherent
// read transaction, then classifies only after that transaction commits.
func (r *WorkspaceRepo) ConfirmWorkspaceCommit(ctx context.Context, prior, next WorkspaceCommitConfirmation) (WorkspaceCommitMatch, error) {
	if r == nil || r.db == nil {
		return WorkspaceCommitThird, fmt.Errorf("localstore: confirm workspace commit: %w", ErrNotFound)
	}
	if !validWorkspaceCommitConfirmation(prior) || !validWorkspaceCommitConfirmation(next) {
		return WorkspaceCommitThird, fmt.Errorf("localstore: invalid workspace commit confirmation")
	}
	if prior.scope != next.scope || prior.targetKind != next.targetKind || prior.targetID != next.targetID ||
		prior.transitionClass != next.transitionClass || next.revision != prior.revision+1 ||
		equalWorkspaceCommitConfirmations(prior, next) {
		return WorkspaceCommitThird, fmt.Errorf("localstore: incompatible workspace commit confirmations")
	}
	if prior.targetKind != workspaceCommitMaterialization {
		return WorkspaceCommitThird, fmt.Errorf("localstore: publication commit confirmation is not installed")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return WorkspaceCommitThird, fmt.Errorf("localstore: acquire workspace commit confirmation connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return WorkspaceCommitThird, fmt.Errorf("localstore: begin workspace commit confirmation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	current, err := (&WorkspaceMutationTx{conn: conn, scope: prior.scope}).CaptureMaterializationCommitConfirmation(ctx, prior.targetID)
	if err != nil {
		return WorkspaceCommitThird, fmt.Errorf("localstore: read workspace commit confirmation: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return WorkspaceCommitThird, fmt.Errorf("localstore: commit workspace confirmation read: %w", err)
	}
	committed = true
	return classifyWorkspaceCommitConfirmation(current, prior, next), nil
}

func classifyWorkspaceCommitConfirmation(current, prior, next WorkspaceCommitConfirmation) WorkspaceCommitMatch {
	if equalWorkspaceCommitConfirmations(current, next) {
		return WorkspaceCommitNext
	}
	if equalWorkspaceCommitConfirmations(current, prior) {
		return WorkspaceCommitPrior
	}
	return WorkspaceCommitThird
}

func equalWorkspaceCommitConfirmations(left, right WorkspaceCommitConfirmation) bool {
	if left.formatVersion != right.formatVersion || left.scope != right.scope || left.revision != right.revision ||
		left.targetKind != right.targetKind || left.targetID != right.targetID || left.targetState != right.targetState ||
		left.currentOwnerID != right.currentOwnerID || left.transitionClass != right.transitionClass ||
		left.authorityDigest != right.authorityDigest || (left.postimageDigest == nil) != (right.postimageDigest == nil) {
		return false
	}
	return left.postimageDigest == nil || *left.postimageDigest == *right.postimageDigest
}

func validWorkspaceCommitConfirmation(value WorkspaceCommitConfirmation) bool {
	if !validWorkspaceCommitConfirmationStructure(value) {
		return false
	}
	switch value.targetState {
	case "":
		return value.currentOwnerID == ""
	case "prepared", "published", "recovered_new":
		return value.currentOwnerID == value.targetID
	case "recovered_old", "accepted":
		return value.currentOwnerID == ""
	default:
		return false
	}
}

func validObservedWorkspaceCommitConfirmation(value WorkspaceCommitConfirmation) bool {
	if !validWorkspaceCommitConfirmationStructure(value) {
		return false
	}
	switch value.targetState {
	case "":
		return value.currentOwnerID != value.targetID
	case "prepared", "published", "recovered_new":
		return value.currentOwnerID == value.targetID
	case "recovered_old", "accepted":
		return value.currentOwnerID != value.targetID
	default:
		return false
	}
}

func validWorkspaceCommitConfirmationStructure(value WorkspaceCommitConfirmation) bool {
	if value.formatVersion != workspaceCommitConfirmationVersion || !validWorkspaceScope(value.scope) || value.revision <= 0 ||
		value.targetKind != workspaceCommitMaterialization || !validLegacyMaterializationJournalID(value.targetID) ||
		value.transitionClass != "" || !validMaterializationDigest(value.authorityDigest) || value.postimageDigest == nil ||
		!validMaterializationDigest(*value.postimageDigest) {
		return false
	}
	if value.currentOwnerID != "" && !validLegacyMaterializationJournalID(value.currentOwnerID) {
		return false
	}
	switch value.targetState {
	case "", "prepared", "published", "recovered_new", "recovered_old", "accepted":
		return true
	default:
		return false
	}
}
