package git

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var (
	ErrStreamPrecondition             = errors.New("git: stream precondition failed")
	ErrPublicAttachReplay             = errors.New("git: public attach replay conflict")
	ErrPublicAttachActivationConflict = errors.New("git: public attach activation conflict")
	ErrPublicAttachClaimConflict      = errors.New("git: public attach claim conflict")
)

type AttachmentLookup struct{ ProjectID, FabricInstanceID, AttachmentRef string }
type StreamAttachment struct {
	Key                                                         StreamKey
	WorkspaceID, AttachmentRef, ActivitySourceRef, CanonicalRef string
	Repository                                                  types.RepositoryIdentity
	IssuerKeyFingerprint                                        string
	SourceVersion                                               int64
	Writable                                                    bool
}
type StreamAttachmentState struct {
	Attachment StreamAttachment
	State      StreamTransition
}
type PublicAttachInput struct {
	ProjectID, FabricInstanceID string
	Repository                  types.RepositoryIdentity
	Ref                         RefObservation
	Tree                        projectstate.Tree
}
type PublicAttachDraft struct {
	Key                                                         StreamKey
	WorkspaceID, AttachmentRef, ActivitySourceRef, CanonicalRef string
	Repository                                                  types.RepositoryIdentity
	SourceVersion                                               int64
	State                                                       StreamTransition
}
type PublicAttachReplayInput struct {
	ProjectID, FabricInstanceID string
	Repository                  types.RepositoryIdentity
	Ref                         RefObservation
	Tree                        projectstate.Tree
	IssuerKeyFingerprint        string
}
type PublicAttachIssuerLookup struct {
	ProjectID, FabricInstanceID        string
	Repository                         types.RepositoryIdentity
	CanonicalRef, IssuerKeyFingerprint string
}
type PublicAttachResult struct {
	Attachment  StreamAttachment
	State       StreamTransition
	ActivityKey FabricActivityStreamKey
}
type SyncPrecondition struct {
	Repository                  types.RepositoryIdentity
	CanonicalRef, BaseCommitSHA string
	BaseTreeDigest              projectstate.Digest
	ExpectedStreamVersion       int64
	ExpectedLiveTreeDigest      projectstate.Digest
}
type ApplyPublicOperationInput struct {
	Attachment   StreamAttachment
	Precondition SyncPrecondition
	Operation    projectstate.OperationV1
}
type ResolveStreamConflictInput struct {
	Attachment   StreamAttachment
	ConflictID   string
	Precondition SyncPrecondition
	Resolution   projectstate.OperationV1
}

// BeginPublicAttachInTx creates the stream and an unclaimed public attachment.
// The caller owns commit/rollback.
func (s *StreamStore) BeginPublicAttachInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input PublicAttachInput) (PublicAttachDraft, error) {
	if s == nil || s.db == nil || tx == nil || !types.CanonicalUUID(input.ProjectID) || !types.CanonicalUUID(input.FabricInstanceID) || input.Repository.Validate() != nil || validateRefObservation(input.Ref) != nil || input.Repository != input.Ref.Repository || scope.Validate() != nil || scope.ProjectID != input.ProjectID {
		return PublicAttachDraft{}, fmt.Errorf("git: begin public attach: %w", ErrStreamNotFound)
	}
	if err := setStreamProject(ctx, tx, input.ProjectID); err != nil {
		return PublicAttachDraft{}, err
	}
	var streamID string
	err := tx.QueryRowContext(ctx, `SELECT stream_id FROM fabric_streams WHERE project_id=$1 AND fabric_instance_id=$2 AND ref_name=$3 FOR UPDATE`, input.ProjectID, input.FabricInstanceID, input.Ref.RefName).Scan(&streamID)
	if errors.Is(err, sql.ErrNoRows) {
		streamID = uuid.NewString()
	} else if err != nil {
		return PublicAttachDraft{}, fmt.Errorf("git: begin public attach: resolve stream: %w", err)
	}
	key := StreamKey{ProjectID: input.ProjectID, FabricInstanceID: input.FabricInstanceID, StreamID: streamID}
	workspaceID := uuid.NewString()
	transition, err := s.AttachInTx(ctx, tx, scope, AttachStreamInput{Key: key, WorkspaceID: workspaceID, Repository: input.Repository, Ref: input.Ref, Tree: input.Tree, Writable: true})
	if err != nil {
		return PublicAttachDraft{}, err
	}
	var attachmentRef, sourceRef string
	err = tx.QueryRowContext(ctx, `SELECT attachment_ref,activity_source_ref FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4
		AND canonical_ref=$5 AND ref_name=$5 AND repository_provider=$6 AND repository_immutable_id=$7
		AND writable AND detached_at IS NULL AND source_version IS NULL AND public_issuer_key_fingerprint IS NULL`,
		input.ProjectID, input.FabricInstanceID, key.StreamID, workspaceID, input.Ref.RefName,
		input.Repository.Provider, input.Repository.ImmutableID).Scan(&attachmentRef, &sourceRef)
	if err != nil {
		return PublicAttachDraft{}, fmt.Errorf("git: begin public attach: read draft: %w", err)
	}
	return PublicAttachDraft{Key: key, WorkspaceID: workspaceID, AttachmentRef: attachmentRef, ActivitySourceRef: sourceRef, CanonicalRef: input.Ref.RefName, Repository: input.Repository, SourceVersion: transition.Version, State: transition}, nil
}

func (s *StreamStore) ClaimPublicAttachInTx(ctx context.Context, tx *sql.Tx, draft PublicAttachDraft, issuerKeyFingerprint string) (PublicAttachResult, error) {
	if s == nil || s.db == nil || tx == nil || validatePublicAttachDraft(draft) != nil || !validPublicFingerprint(issuerKeyFingerprint) {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrStreamNotFound)
	}
	if err := setStreamProject(ctx, tx, draft.Key.ProjectID); err != nil {
		return PublicAttachResult{}, err
	}
	// Lock and prove the complete draft route before mutating it. This keeps the
	// same binding-before-stream lock order used by complete attachment reads.
	var lockedDraft int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4
		AND canonical_ref=$5 AND ref_name=$5 AND attachment_ref=$6 AND activity_source_ref=$7
		AND repository_provider=$8 AND repository_immutable_id=$9 AND writable AND detached_at IS NULL
		AND public_issuer_key_fingerprint IS NULL AND source_version IS NULL FOR UPDATE`,
		draft.Key.ProjectID, draft.Key.FabricInstanceID, draft.Key.StreamID, draft.WorkspaceID,
		draft.CanonicalRef, draft.AttachmentRef, draft.ActivitySourceRef,
		draft.Repository.Provider, draft.Repository.ImmutableID).Scan(&lockedDraft)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrPublicAttachClaimConflict)
	}
	if err != nil {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: lock draft: %w", err)
	}
	stream, found, err := findStreamTx(ctx, tx, draft.Key, true)
	if err != nil {
		return PublicAttachResult{}, err
	}
	if !found || stream.canonicalRef != draft.CanonicalRef {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrStreamCorrupt)
	}
	repository, err := readStreamRepositoryTx(ctx, tx, draft.Key, false)
	if err != nil {
		return PublicAttachResult{}, err
	}
	if repository.identity != draft.Repository {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrStreamCorrupt)
	}
	source, err := loadStreamVersionTx(ctx, tx, draft.Key, draft.CanonicalRef, draft.SourceVersion, repository)
	if err != nil {
		return PublicAttachResult{}, err
	}
	if !samePublicAttachTransition(draft.State, source.transition) {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: source evidence: %w", ErrStreamCorrupt)
	}
	result, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET source_version=$1,public_issuer_key_fingerprint=$2
		WHERE project_id=$3 AND fabric_instance_id=$4 AND stream_id=$5 AND workspace_id=$6
		AND canonical_ref=$7 AND ref_name=$7 AND attachment_ref=$8 AND activity_source_ref=$9
		AND repository_provider=$10 AND repository_immutable_id=$11 AND writable AND detached_at IS NULL
		AND public_issuer_key_fingerprint IS NULL AND source_version IS NULL`,
		draft.SourceVersion, issuerKeyFingerprint, draft.Key.ProjectID, draft.Key.FabricInstanceID,
		draft.Key.StreamID, draft.WorkspaceID, draft.CanonicalRef, draft.AttachmentRef,
		draft.ActivitySourceRef, draft.Repository.Provider, draft.Repository.ImmutableID)
	if err != nil {
		return PublicAttachResult{}, classifyPublicAttachClaimError(err)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: rows affected: %w", rowsErr)
	}
	if rows != 1 {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrPublicAttachClaimConflict)
	}
	state, err := s.LockAttachmentInTx(ctx, tx, AttachmentLookup{ProjectID: draft.Key.ProjectID, FabricInstanceID: draft.Key.FabricInstanceID, AttachmentRef: draft.AttachmentRef})
	if err != nil {
		return PublicAttachResult{}, err
	}
	want := StreamAttachment{Key: draft.Key, WorkspaceID: draft.WorkspaceID, AttachmentRef: draft.AttachmentRef, ActivitySourceRef: draft.ActivitySourceRef, CanonicalRef: draft.CanonicalRef, Repository: draft.Repository, IssuerKeyFingerprint: issuerKeyFingerprint, SourceVersion: draft.SourceVersion, Writable: true}
	if state.Attachment != want || state.State.Version < draft.SourceVersion {
		return PublicAttachResult{}, fmt.Errorf("git: claim public attach: %w", ErrStreamCorrupt)
	}
	return PublicAttachResult{Attachment: state.Attachment, State: state.State, ActivityKey: FabricActivityStreamKey{ProjectID: state.Attachment.Key.ProjectID, FabricInstanceID: state.Attachment.Key.FabricInstanceID, StreamID: state.Attachment.Key.StreamID, CanonicalRef: state.Attachment.CanonicalRef}}, nil
}

func (s *StreamStore) ReplayPublicAttachInTx(ctx context.Context, tx *sql.Tx, input PublicAttachReplayInput) (PublicAttachResult, error) {
	if s == nil || s.db == nil || tx == nil || !types.CanonicalUUID(input.ProjectID) || !types.CanonicalUUID(input.FabricInstanceID) || input.Repository.Validate() != nil || validateRefObservation(input.Ref) != nil || input.Ref.Repository != input.Repository || !validPublicFingerprint(input.IssuerKeyFingerprint) {
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
	}
	// Reject malformed trees before taking any route lock. Binding validation is
	// repeated below once the resolved stream key is known.
	if _, err := projectstate.DigestTree(input.Tree); err != nil {
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
	}
	a, err := s.ResolvePublicAttachmentByIssuerInTx(ctx, tx, PublicAttachIssuerLookup{ProjectID: input.ProjectID, FabricInstanceID: input.FabricInstanceID, Repository: input.Repository, CanonicalRef: input.Ref.RefName, IssuerKeyFingerprint: input.IssuerKeyFingerprint})
	if err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
		}
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: resolve: %w", err)
	}
	repository, repositoryErr := readStreamRepositoryTx(ctx, tx, a.Attachment.Key, false)
	if repositoryErr != nil {
		return PublicAttachResult{}, repositoryErr
	}
	storedTree, _, digest, treeErr := prepareStreamTree(input.Tree, a.Attachment.Key, input.Repository)
	if treeErr != nil {
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
	}
	source, sourceErr := loadStreamVersionTx(ctx, tx, a.Attachment.Key, a.Attachment.CanonicalRef, a.Attachment.SourceVersion, repository)
	if sourceErr != nil {
		if errors.Is(sourceErr, ErrStreamNotFound) {
			return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
		}
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: source version: %w", sourceErr)
	}
	if a.Attachment.Repository != input.Repository || a.Attachment.CanonicalRef != input.Ref.RefName ||
		source.transition.AcceptedCommitSHA != input.Ref.CommitSHA || source.transition.Accepted.Digest != digest ||
		!bytes.Equal(source.acceptedTree, storedTree) {
		return PublicAttachResult{}, fmt.Errorf("git: replay public attach: %w", ErrPublicAttachReplay)
	}
	return PublicAttachResult{Attachment: a.Attachment, State: a.State, ActivityKey: FabricActivityStreamKey{ProjectID: a.Attachment.Key.ProjectID, FabricInstanceID: a.Attachment.Key.FabricInstanceID, StreamID: a.Attachment.Key.StreamID, CanonicalRef: a.Attachment.CanonicalRef}}, nil
}

func (s *StreamStore) ResolvePublicAttachmentByIssuerInTx(ctx context.Context, tx *sql.Tx, lookup PublicAttachIssuerLookup) (StreamAttachmentState, error) {
	if s == nil || s.db == nil || tx == nil || !types.CanonicalUUID(lookup.ProjectID) || !types.CanonicalUUID(lookup.FabricInstanceID) || lookup.Repository.Validate() != nil || !validCanonicalRef(lookup.CanonicalRef) || !validPublicFingerprint(lookup.IssuerKeyFingerprint) {
		return StreamAttachmentState{}, fmt.Errorf("git: issuer attachment: %w", ErrStreamNotFound)
	}
	if err := setStreamProject(ctx, tx, lookup.ProjectID); err != nil {
		return StreamAttachmentState{}, err
	}
	var ref string
	err := tx.QueryRowContext(ctx, `SELECT b.attachment_ref FROM fabric_workspace_stream_bindings b
		JOIN project_repository_bindings r ON r.project_id=b.project_id AND r.fabric_instance_id=b.fabric_instance_id
		AND r.provider=b.repository_provider AND r.provider_repository_id=b.repository_immutable_id
		WHERE fabric_resolve_attachment_project_v1(b.fabric_instance_id,b.attachment_ref)=$1
		AND b.project_id=$1 AND b.fabric_instance_id=$2 AND b.repository_provider=$3
		AND b.repository_immutable_id=$4 AND r.canonical_remote=$5 AND b.canonical_ref=$6 AND b.ref_name=$6
		AND b.public_issuer_key_fingerprint=$7 AND b.source_version IS NOT NULL AND b.detached_at IS NULL`,
		lookup.ProjectID, lookup.FabricInstanceID, lookup.Repository.Provider, lookup.Repository.ImmutableID,
		lookup.Repository.CanonicalRemote, lookup.CanonicalRef, lookup.IssuerKeyFingerprint).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamAttachmentState{}, fmt.Errorf("git: issuer attachment: %w", ErrStreamNotFound)
	}
	if err != nil {
		return StreamAttachmentState{}, fmt.Errorf("git: issuer attachment: read: %w", err)
	}
	return s.LockAttachmentInTx(ctx, tx, AttachmentLookup{ProjectID: lookup.ProjectID, FabricInstanceID: lookup.FabricInstanceID, AttachmentRef: ref})
}

func classifyPublicAttachClaimError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" && (pqErr.Constraint == "fabric_workspace_stream_bindings_public_issuer_key_uq" || pqErr.Constraint == "fabric_workspace_binding_one_live_public_issuer_idx") {
		return fmt.Errorf("git: claim public attach: %w", ErrPublicAttachClaimConflict)
	}
	return fmt.Errorf("git: claim public attach: %w", err)
}

// AdvanceAcceptedObservedRefInTx is the public-sync spelling of the default
// accepted-ref transition.  Keeping this adapter here makes the transaction
// boundary explicit without duplicating the stream reducer.
func (s *StreamStore) AdvanceAcceptedObservedRefInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input AdvanceAcceptedInput) (StreamTransition, error) {
	return s.advanceAcceptedRefInTx(ctx, tx, scope, input, false)
}

func (s *StreamStore) OpenConflictIDsInTx(ctx context.Context, tx *sql.Tx, a StreamAttachment) ([]string, error) {
	if s == nil || s.db == nil || tx == nil || validateStreamAttachment(a, true) != nil {
		return nil, fmt.Errorf("git: open conflicts: %w", ErrStreamNotFound)
	}
	locked, err := s.lockExactAttachment(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT conflict_id FROM fabric_stream_conflicts WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND state='open' ORDER BY conflict_id`, locked.Key.ProjectID, locked.Key.FabricInstanceID, locked.Key.StreamID, locked.CanonicalRef)
	if err != nil {
		return nil, fmt.Errorf("git: open conflicts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("git: open conflicts: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("git: open conflicts: %w", err)
	}
	return ids, nil
}

func (s *StreamStore) ResolveConflictInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input ResolveStreamConflictInput) (StreamTransition, error) {
	if s == nil || s.db == nil || tx == nil || !types.CanonicalUUID(input.ConflictID) || validateStreamAttachment(input.Attachment, true) != nil {
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", ErrStreamNotFound)
	}
	locked, err := s.lockExactAttachment(ctx, tx, input.Attachment)
	if err != nil {
		return StreamTransition{}, err
	}
	var state string
	var resolutionVersion sql.NullInt64
	var resolutionOperationID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,resolution_operation_id,resolution_version FROM fabric_stream_conflicts WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND conflict_id=$5 FOR UPDATE`, locked.Key.ProjectID, locked.Key.FabricInstanceID, locked.Key.StreamID, locked.CanonicalRef, input.ConflictID).Scan(&state, &resolutionOperationID, &resolutionVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", ErrStreamConflict)
		}
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", err)
	}
	if state == "resolved" {
		if !resolutionOperationID.Valid || resolutionOperationID.String != input.Resolution.ID || !resolutionVersion.Valid {
			return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", ErrOperationReplay)
		}
		replayed, err := s.ApplyPublicOperationInTx(ctx, tx, scope, ApplyPublicOperationInput{Attachment: locked, Precondition: input.Precondition, Operation: input.Resolution})
		if err != nil {
			return StreamTransition{}, err
		}
		if !resolutionVersion.Valid || replayed.Version != resolutionVersion.Int64 {
			return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", ErrOperationReplay)
		}
		return replayed, nil
	}
	if state != "open" {
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: invalid stored state: %w", ErrStreamCorrupt)
	}
	st, err := s.ApplyPublicOperationInTx(ctx, tx, scope, ApplyPublicOperationInput{Attachment: locked, Precondition: input.Precondition, Operation: input.Resolution})
	if err != nil {
		return StreamTransition{}, err
	}
	if st.ConflictID != "" {
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: nested conflict: %w", ErrStreamPrecondition)
	}
	result, err := tx.ExecContext(ctx, `UPDATE fabric_stream_conflicts SET state='resolved',resolved_at=now(),resolution_operation_id=$1,resolution_version=$2 WHERE project_id=$3 AND fabric_instance_id=$4 AND stream_id=$5 AND canonical_ref=$6 AND conflict_id=$7 AND state='open'`, input.Resolution.ID, st.Version, locked.Key.ProjectID, locked.Key.FabricInstanceID, locked.Key.StreamID, locked.CanonicalRef, input.ConflictID)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return StreamTransition{}, fmt.Errorf("git: resolve conflict: %w", err)
	}
	return st, nil
}

// ResolveAttachmentProject returns only the project owning one live opaque
// attachment. The database function is the narrow security-definer boundary;
// every subsequent route read still runs under the returned project's forced
// RLS context.
func (s *StreamStore) ResolveAttachmentProject(ctx context.Context, fabricInstanceID, attachmentRef string) (string, error) {
	if s == nil || s.db == nil || !types.CanonicalUUID(fabricInstanceID) || !types.CanonicalUUID(attachmentRef) {
		return "", fmt.Errorf("git: resolve attachment project: %w", ErrStreamNotFound)
	}
	var projectID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT fabric_resolve_attachment_project_v1($1,$2)`, fabricInstanceID, attachmentRef).Scan(&projectID); err != nil {
		return "", fmt.Errorf("git: resolve attachment project: %w", err)
	}
	if !projectID.Valid || !types.CanonicalUUID(projectID.String) {
		return "", fmt.Errorf("git: resolve attachment project: %w", ErrStreamNotFound)
	}
	return projectID.String, nil
}

func (s *StreamStore) ReadAttachmentInTx(ctx context.Context, tx *sql.Tx, l AttachmentLookup) (StreamAttachmentState, error) {
	return s.readAttachment(ctx, tx, l, false)
}
func (s *StreamStore) LockAttachmentInTx(ctx context.Context, tx *sql.Tx, l AttachmentLookup) (StreamAttachmentState, error) {
	return s.readAttachment(ctx, tx, l, true)
}

// ValidateCurrentAttachmentStateInTx verifies that an already loaded transition
// still agrees with the mutable current-stream summary in the caller's project
// transaction. Public read handlers call this during response validation so a
// contradictory summary fails closed before their nonce transaction commits.
func (s *StreamStore) ValidateCurrentAttachmentStateInTx(ctx context.Context, tx *sql.Tx, a StreamAttachment, state StreamTransition) error {
	if s == nil || s.db == nil || tx == nil || validateStreamAttachment(a, true) != nil || state.Key != a.Key {
		return fmt.Errorf("git: validate current attachment state: %w", ErrStreamCorrupt)
	}
	stream, found, err := findStreamTx(ctx, tx, a.Key, true)
	if err != nil {
		return err
	}
	if !found || stream.canonicalRef != a.CanonicalRef || stream.currentVersion != state.Version ||
		stream.liveDigest != state.Live.Digest || stream.acceptedDigest != state.Accepted.Digest ||
		stream.acceptedCommitSHA != state.AcceptedCommitSHA {
		return fmt.Errorf("git: validate current attachment state: %w", ErrStreamCorrupt)
	}
	return nil
}

func (s *StreamStore) readAttachment(ctx context.Context, tx *sql.Tx, l AttachmentLookup, lock bool) (StreamAttachmentState, error) {
	if s == nil || s.db == nil || tx == nil || !types.CanonicalUUID(l.ProjectID) || !types.CanonicalUUID(l.FabricInstanceID) || !types.CanonicalUUID(l.AttachmentRef) {
		return StreamAttachmentState{}, fmt.Errorf("git: attachment: %w", ErrStreamNotFound)
	}
	if err := setStreamProject(ctx, tx, l.ProjectID); err != nil {
		return StreamAttachmentState{}, err
	}
	q := `SELECT b.stream_id,b.workspace_id,b.attachment_ref,b.activity_source_ref,b.canonical_ref,b.repository_provider,b.repository_immutable_id,r.canonical_remote,b.source_version,coalesce(b.public_issuer_key_fingerprint,''),b.writable
		FROM fabric_workspace_stream_bindings b JOIN project_repository_bindings r
		ON r.project_id=b.project_id AND r.fabric_instance_id=b.fabric_instance_id
		AND r.provider=b.repository_provider AND r.provider_repository_id=b.repository_immutable_id
		WHERE b.project_id=$1 AND b.fabric_instance_id=$2 AND b.attachment_ref=$3
		AND b.canonical_ref=b.ref_name AND b.source_version IS NOT NULL
		AND b.public_issuer_key_fingerprint IS NOT NULL AND b.detached_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF b`
	}
	var a StreamAttachment
	var remote, issuer string
	var sourceVersion sql.NullInt64
	err := tx.QueryRowContext(ctx, q, l.ProjectID, l.FabricInstanceID, l.AttachmentRef).Scan(&a.Key.StreamID, &a.WorkspaceID, &a.AttachmentRef, &a.ActivitySourceRef, &a.CanonicalRef, &a.Repository.Provider, &a.Repository.ImmutableID, &remote, &sourceVersion, &issuer, &a.Writable)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamAttachmentState{}, fmt.Errorf("git: attachment: %w", ErrStreamNotFound)
	}
	if err != nil {
		return StreamAttachmentState{}, fmt.Errorf("git: attachment: read: %w", err)
	}
	if sourceVersion.Valid {
		a.SourceVersion = sourceVersion.Int64
	}
	a.Key.ProjectID = l.ProjectID
	a.Key.FabricInstanceID = l.FabricInstanceID
	a.IssuerKeyFingerprint = issuer
	a.Repository.CanonicalRemote = remote
	if validateStreamAttachment(a, true) != nil {
		return StreamAttachmentState{}, fmt.Errorf("git: attachment: %w", ErrStreamCorrupt)
	}
	st, err := s.readCurrentInTx(ctx, tx, a.Key, lock)
	if err != nil {
		return StreamAttachmentState{}, err
	}
	return StreamAttachmentState{Attachment: a, State: st}, nil
}

func (s *StreamStore) readCurrentInTx(ctx context.Context, tx *sql.Tx, key StreamKey, lock bool) (StreamTransition, error) {
	stream, ok, err := findStreamTx(ctx, tx, key, lock)
	if err != nil {
		return StreamTransition{}, err
	}
	if !ok {
		return StreamTransition{}, fmt.Errorf("git: attachment stream: %w", ErrStreamNotFound)
	}
	repo, err := readStreamRepositoryTx(ctx, tx, key, false)
	if err != nil {
		return StreamTransition{}, err
	}
	loaded, err := loadStreamVersionTx(ctx, tx, key, stream.canonicalRef, stream.currentVersion, repo)
	if err != nil {
		return StreamTransition{}, err
	}
	return loaded.transition, nil
}

func (s *StreamStore) CheckCurrentPreconditionInTx(ctx context.Context, tx *sql.Tx, a StreamAttachment, p SyncPrecondition) (StreamTransition, error) {
	if s == nil || s.db == nil || tx == nil || validateStreamAttachment(a, true) != nil || validateSyncPrecondition(p) != nil {
		return StreamTransition{}, fmt.Errorf("git: precondition: %w", ErrStreamPrecondition)
	}
	locked, err := s.lockExactAttachment(ctx, tx, a)
	if err != nil {
		return StreamTransition{}, err
	}
	state, err := s.readCurrentInTx(ctx, tx, locked.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	if p.Repository != locked.Repository || p.CanonicalRef != locked.CanonicalRef || p.BaseCommitSHA != state.AcceptedCommitSHA || p.BaseTreeDigest != state.Accepted.Digest || p.ExpectedStreamVersion != state.Version || p.ExpectedLiveTreeDigest != state.Live.Digest {
		return StreamTransition{}, ErrStreamPrecondition
	}
	return state, nil
}

func (s *StreamStore) ApplyPublicOperationInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input ApplyPublicOperationInput) (StreamTransition, error) {
	if s == nil || s.db == nil || tx == nil || validateStreamAttachment(input.Attachment, true) != nil || validateSyncPrecondition(input.Precondition) != nil || validateStreamScope(scope, input.Attachment.Key) != nil {
		return StreamTransition{}, fmt.Errorf("git: public operation: %w", ErrStreamPrecondition)
	}
	// Replay is identified and validated before consulting mutable current
	// state: an exact retry must remain readable after the stream advances.
	operation, canonical, digest, actorJSON, err := reconcileStreamOperation(scope, input.Operation)
	if err != nil {
		return StreamTransition{}, err
	}
	if err := setStreamProject(ctx, tx, input.Attachment.Key.ProjectID); err != nil {
		return StreamTransition{}, err
	}
	request, found, err := loadStreamRequestTx(ctx, tx, input.Attachment.Key, input.Attachment.CanonicalRef, operation.ID)
	if err != nil {
		return StreamTransition{}, err
	}
	if found {
		repository, err := readStreamRepositoryTx(ctx, tx, input.Attachment.Key, false)
		if err != nil {
			return StreamTransition{}, err
		}
		historical, err := loadStreamVersionTx(ctx, tx, input.Attachment.Key, input.Attachment.CanonicalRef, request.expectedVersion, repository)
		if err != nil {
			return StreamTransition{}, err
		}
		if request.expectedDigest != historical.transition.Live.Digest {
			return StreamTransition{}, fmt.Errorf("git: public operation replay: historical live evidence: %w", ErrStreamCorrupt)
		}
		if input.Precondition.Repository != input.Attachment.Repository || input.Precondition.Repository != repository.identity ||
			input.Precondition.CanonicalRef != input.Attachment.CanonicalRef || input.Precondition.BaseCommitSHA != historical.transition.AcceptedCommitSHA ||
			input.Precondition.BaseTreeDigest != historical.transition.Accepted.Digest || input.Precondition.ExpectedStreamVersion != request.expectedVersion ||
			input.Precondition.ExpectedLiveTreeDigest != request.expectedDigest || operation.ExpectedViewDigest != request.expectedDigest {
			return StreamTransition{}, fmt.Errorf("git: public operation replay: %w", ErrOperationReplay)
		}
		locked, err := s.lockExactAttachment(ctx, tx, input.Attachment)
		if err != nil {
			return StreamTransition{}, err
		}
		if !locked.Writable {
			return StreamTransition{}, fmt.Errorf("git: public operation replay: %w", ErrStreamNotFound)
		}
		return replayStreamRequest(ctx, tx, ApplyStreamOperationInput{Key: locked.Key, WorkspaceID: locked.WorkspaceID, ExpectedVersion: input.Precondition.ExpectedStreamVersion, ExpectedTreeDigest: input.Precondition.ExpectedLiveTreeDigest, Operation: input.Operation}, locked.CanonicalRef, repository, operation, canonical, digest, actorJSON, request)
	}
	st, err := s.CheckCurrentPreconditionInTx(ctx, tx, input.Attachment, input.Precondition)
	if err != nil {
		return StreamTransition{}, err
	}
	if operation.ExpectedViewDigest != input.Precondition.ExpectedLiveTreeDigest {
		return StreamTransition{}, fmt.Errorf("git: public operation: %w", ErrStreamPrecondition)
	}
	return s.ApplyOperationInTx(ctx, tx, scope, ApplyStreamOperationInput{Key: input.Attachment.Key, WorkspaceID: input.Attachment.WorkspaceID, ExpectedVersion: st.Version, ExpectedTreeDigest: st.Live.Digest, Operation: input.Operation})
}

func validPublicFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validCanonicalRef(value string) bool {
	return githubRefPattern.MatchString(value) && validGitHubRef(value)
}

func validatePublicAttachDraft(draft PublicAttachDraft) error {
	if validateStreamKey(draft.Key) != nil || !types.CanonicalUUID(draft.WorkspaceID) || !types.CanonicalUUID(draft.AttachmentRef) ||
		!types.CanonicalUUID(draft.ActivitySourceRef) || !validCanonicalRef(draft.CanonicalRef) || draft.Repository.Validate() != nil ||
		draft.SourceVersion < 0 || draft.SourceVersion > maximumStreamVersion || draft.State.Key != draft.Key ||
		draft.State.Version != draft.SourceVersion || !streamCommitPattern.MatchString(draft.State.AcceptedCommitSHA) ||
		!streamDigestPattern.MatchString(string(draft.State.Live.Digest)) || !streamDigestPattern.MatchString(string(draft.State.Accepted.Digest)) ||
		draft.State.ConflictID != "" || projectstate.Validate(draft.State.Live) != nil || projectstate.Validate(draft.State.Accepted) != nil ||
		draft.State.Live.Config.ProjectID != draft.Key.ProjectID || draft.State.Accepted.Config.ProjectID != draft.Key.ProjectID ||
		draft.State.Live.Config.Repository != draft.Repository || draft.State.Accepted.Config.Repository != draft.Repository {
		return ErrStreamNotFound
	}
	return nil
}

func samePublicAttachTransition(left, right StreamTransition) bool {
	return left.Key == right.Key && left.Version == right.Version &&
		left.Live.Digest == right.Live.Digest && left.Accepted.Digest == right.Accepted.Digest &&
		left.AcceptedCommitSHA == right.AcceptedCommitSHA && left.ConflictID == right.ConflictID
}

func validateStreamAttachment(attachment StreamAttachment, complete bool) error {
	if validateStreamKey(attachment.Key) != nil || !types.CanonicalUUID(attachment.WorkspaceID) || !types.CanonicalUUID(attachment.AttachmentRef) ||
		!types.CanonicalUUID(attachment.ActivitySourceRef) || !validCanonicalRef(attachment.CanonicalRef) || attachment.Repository.Validate() != nil ||
		attachment.SourceVersion < 0 || attachment.SourceVersion > maximumStreamVersion {
		return ErrStreamNotFound
	}
	if complete && !validPublicFingerprint(attachment.IssuerKeyFingerprint) {
		return ErrStreamNotFound
	}
	return nil
}

func validateSyncPrecondition(precondition SyncPrecondition) error {
	if precondition.Repository.Validate() != nil || !validCanonicalRef(precondition.CanonicalRef) ||
		!streamCommitPattern.MatchString(precondition.BaseCommitSHA) || !streamDigestPattern.MatchString(string(precondition.BaseTreeDigest)) ||
		precondition.ExpectedStreamVersion < 0 || precondition.ExpectedStreamVersion > maximumStreamVersion ||
		!streamDigestPattern.MatchString(string(precondition.ExpectedLiveTreeDigest)) {
		return ErrStreamPrecondition
	}
	return nil
}

func (s *StreamStore) lockExactAttachment(ctx context.Context, tx *sql.Tx, supplied StreamAttachment) (StreamAttachment, error) {
	loaded, err := s.LockAttachmentInTx(ctx, tx, AttachmentLookup{ProjectID: supplied.Key.ProjectID, FabricInstanceID: supplied.Key.FabricInstanceID, AttachmentRef: supplied.AttachmentRef})
	if err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			return StreamAttachment{}, fmt.Errorf("git: attachment route mismatch: %w", ErrStreamPrecondition)
		}
		return StreamAttachment{}, err
	}
	if loaded.Attachment != supplied {
		return StreamAttachment{}, fmt.Errorf("git: attachment route mismatch: %w", ErrStreamPrecondition)
	}
	return loaded.Attachment, nil
}
