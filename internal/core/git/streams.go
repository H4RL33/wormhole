package git

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrStreamNotFound  = errors.New("git: stream not found")
	ErrStreamConflict  = errors.New("git: stream conflict")
	ErrStreamCorrupt   = errors.New("git: stream storage corrupt")
	ErrOperationReplay = errors.New("git: operation replay conflict")
	ErrStreamActor     = errors.New("git: stream actor mismatch")
)

const maximumStreamVersion int64 = 9_007_199_254_740_991

var (
	streamDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	streamCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type StreamKey struct {
	ProjectID, FabricInstanceID, StreamID string
}

type RefObservation struct {
	Repository         types.RepositoryIdentity
	RefName, CommitSHA string
	ObservedAt         time.Time
}

type AttachStreamInput struct {
	Key         StreamKey
	WorkspaceID string
	Repository  types.RepositoryIdentity
	Ref         RefObservation
	Tree        projectstate.Tree
	Writable    bool
}

type ApplyStreamOperationInput struct {
	Key                StreamKey
	WorkspaceID        string
	ExpectedVersion    int64
	ExpectedTreeDigest projectstate.Digest
	Operation          projectstate.OperationV1
}

type AdvanceAcceptedInput struct {
	Key                        StreamKey
	Ref                        RefObservation
	Tree                       projectstate.Tree
	ExpectedVersion            int64
	ExpectedAcceptedCommitSHA  string
	ExpectedAcceptedTreeDigest projectstate.Digest
	ExpectedLiveTreeDigest     projectstate.Digest
}

type StreamTransition struct {
	Key               StreamKey
	Version           int64
	Live, Accepted    projectstate.Snapshot
	AcceptedCommitSHA string
	ConflictID        string
}

type StreamStore struct {
	db *sql.DB
}

func (s *StreamStore) AttachInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input AttachStreamInput) (StreamTransition, error) {
	if tx == nil {
		return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamNotFound)
	}
	if err := validateStreamScope(scope, input.Key); err != nil || !types.CanonicalUUID(input.WorkspaceID) {
		return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamNotFound)
	}
	if err := validateRefObservation(input.Ref); err != nil || input.Repository != input.Ref.Repository || input.Repository.Validate() != nil {
		return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamConflict)
	}
	tree, snapshot, digest, err := prepareStreamTree(input.Tree, input.Key, input.Repository)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: attach stream: %w", err)
	}
	if err := setStreamProject(ctx, tx, input.Key.ProjectID); err != nil {
		return StreamTransition{}, err
	}
	repository, err := readStreamRepositoryTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	if repository.identity != input.Repository {
		return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamConflict)
	}

	stream, found, err := findStreamTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	var transition StreamTransition
	if !found {
		_, err = tx.ExecContext(ctx, `INSERT INTO fabric_streams
			(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,
			 live_tree_digest,accepted_tree_digest,accepted_commit_sha)
			VALUES($1,$2,$3,$4,$4,0,$5,$5,$6)`,
			input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.Ref.RefName,
			string(digest), input.Ref.CommitSHA)
		if err != nil {
			return StreamTransition{}, classifyStreamAttachDatabaseError("git: attach stream: create stream", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO fabric_stream_versions
			(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,
			 accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest)
			VALUES($1,$2,$3,$4,0,'initial',$5,$6,$7,$6,$7)`,
			input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.Ref.RefName,
			input.Ref.CommitSHA, tree, string(digest))
		if err != nil {
			return StreamTransition{}, fmt.Errorf("git: attach stream: create version: %w", err)
		}
		transition = StreamTransition{
			Key: input.Key, Version: 0, Live: snapshot, Accepted: snapshot,
			AcceptedCommitSHA: input.Ref.CommitSHA,
		}
	} else {
		if stream.canonicalRef != input.Ref.RefName || stream.acceptedCommitSHA != input.Ref.CommitSHA || stream.acceptedDigest != digest {
			return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamConflict)
		}
		loaded, loadErr := loadStreamVersionTx(ctx, tx, input.Key, stream.canonicalRef, stream.currentVersion, repository)
		if loadErr != nil {
			return StreamTransition{}, loadErr
		}
		if !bytes.Equal(loaded.acceptedTree, tree) {
			return StreamTransition{}, fmt.Errorf("git: attach stream: %w", ErrStreamConflict)
		}
		if err := verifyCurrentStream(stream, loaded); err != nil {
			return StreamTransition{}, err
		}
		transition = loaded.transition
	}
	if err := attachWorkspaceTx(ctx, tx, input, repository.identity); err != nil {
		return StreamTransition{}, err
	}
	return transition, nil
}

func (s *StreamStore) Read(ctx context.Context, key StreamKey, version int64) (StreamTransition, error) {
	if s == nil || s.db == nil || validateStreamKey(key) != nil || version < 0 || version > maximumStreamVersion {
		return StreamTransition{}, fmt.Errorf("git: read stream: %w", ErrStreamNotFound)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: read stream: begin: %w", err)
	}
	defer tx.Rollback()
	if err := setStreamProject(ctx, tx, key.ProjectID); err != nil {
		return StreamTransition{}, err
	}
	repository, err := readStreamRepositoryTx(ctx, tx, key, false)
	if err != nil {
		return StreamTransition{}, err
	}
	stream, found, err := findStreamTx(ctx, tx, key, false)
	if err != nil {
		return StreamTransition{}, err
	}
	if !found || version > stream.currentVersion {
		return StreamTransition{}, fmt.Errorf("git: read stream: %w", ErrStreamNotFound)
	}
	loaded, err := loadStreamVersionTx(ctx, tx, key, stream.canonicalRef, version, repository)
	if err != nil {
		return StreamTransition{}, err
	}
	if version == stream.currentVersion {
		if err := verifyCurrentStream(stream, loaded); err != nil {
			return StreamTransition{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return StreamTransition{}, fmt.Errorf("git: read stream: commit: %w", err)
	}
	return loaded.transition, nil
}

func (s *StreamStore) ApplyOperationInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input ApplyStreamOperationInput) (StreamTransition, error) {
	if tx == nil || validateStreamScope(scope, input.Key) != nil || !types.CanonicalUUID(input.WorkspaceID) ||
		input.ExpectedVersion < 0 || input.ExpectedVersion > maximumStreamVersion || !streamDigestPattern.MatchString(string(input.ExpectedTreeDigest)) {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: %w", ErrStreamNotFound)
	}
	operation, canonical, operationDigest, actorJSON, err := reconcileStreamOperation(scope, input.Operation)
	if err != nil {
		return StreamTransition{}, err
	}
	if err := setStreamProject(ctx, tx, input.Key.ProjectID); err != nil {
		return StreamTransition{}, err
	}
	repository, err := readStreamRepositoryTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	stream, found, err := findStreamTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	if !found {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: %w", ErrStreamNotFound)
	}
	if err := requireWritableWorkspaceTx(ctx, tx, input, stream.canonicalRef, repository.identity); err != nil {
		return StreamTransition{}, err
	}

	request, found, err := loadStreamRequestTx(ctx, tx, input.Key, stream.canonicalRef, operation.ID)
	if err != nil {
		return StreamTransition{}, err
	}
	if found {
		return replayStreamRequest(ctx, tx, input, stream.canonicalRef, repository, operation, canonical, operationDigest, actorJSON, request)
	}

	current, err := loadStreamVersionTx(ctx, tx, input.Key, stream.canonicalRef, stream.currentVersion, repository)
	if err != nil {
		return StreamTransition{}, err
	}
	if err := verifyCurrentStream(stream, current); err != nil {
		return StreamTransition{}, err
	}
	if input.ExpectedVersion != current.transition.Version || input.ExpectedTreeDigest != current.transition.Live.Digest || operation.ExpectedViewDigest != input.ExpectedTreeDigest {
		return persistOperationConflict(ctx, tx, input, stream.canonicalRef, current, operation, canonical, operationDigest, actorJSON)
	}
	if current.transition.Version == maximumStreamVersion {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: %w", ErrStreamConflict)
	}
	nextSnapshot, err := projectstate.ApplyOperation(current.transition.Live, operation)
	if err != nil {
		return StreamTransition{}, err
	}
	nextTree, err := projectstate.EncodeTree(nextSnapshot)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: encode result: %w", err)
	}
	nextStoredTree, nextSnapshot, nextDigest, err := prepareStreamTree(nextTree, input.Key, repository.identity)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: %w", err)
	}
	nextVersion := current.transition.Version + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO fabric_stream_versions
		(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,
		 canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest,
		 operation_id,canonical_operation_json,operation_digest,actor_envelope_json)
		VALUES($1,$2,$3,$4,$5,'operation',$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, stream.canonicalRef, nextVersion,
		current.transition.AcceptedCommitSHA, nextStoredTree, string(nextDigest), current.acceptedTree,
		string(current.transition.Accepted.Digest), operation.ID, canonical, string(operationDigest), actorJSON)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: insert version: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fabric_stream_requests
		(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,
		 operation_digest,expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'applied',$11,$12)`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.WorkspaceID, stream.canonicalRef,
		operation.ID, canonical, string(operationDigest), input.ExpectedVersion, string(input.ExpectedTreeDigest), nextVersion, actorJSON)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: insert request: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE fabric_streams SET current_version=$1,live_tree_digest=$2,updated_at=now()
		WHERE project_id=$3 AND fabric_instance_id=$4 AND stream_id=$5 AND canonical_ref=$6 AND current_version=$7`,
		nextVersion, string(nextDigest), input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID,
		stream.canonicalRef, current.transition.Version)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: update stream: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return StreamTransition{}, fmt.Errorf("git: apply stream operation: %w", ErrStreamConflict)
	}
	return StreamTransition{
		Key: input.Key, Version: nextVersion, Live: nextSnapshot, Accepted: current.transition.Accepted,
		AcceptedCommitSHA: current.transition.AcceptedCommitSHA,
	}, nil
}

func (s *StreamStore) AdvanceAcceptedDefaultInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, input AdvanceAcceptedInput) (StreamTransition, error) {
	if tx == nil || validateStreamScope(scope, input.Key) != nil || validateRefObservation(input.Ref) != nil ||
		input.ExpectedVersion < 0 || input.ExpectedVersion > maximumStreamVersion ||
		!streamCommitPattern.MatchString(input.ExpectedAcceptedCommitSHA) ||
		!streamDigestPattern.MatchString(string(input.ExpectedAcceptedTreeDigest)) ||
		!streamDigestPattern.MatchString(string(input.ExpectedLiveTreeDigest)) {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", ErrStreamNotFound)
	}
	newAcceptedTree, newAcceptedSnapshot, newAcceptedDigest, err := prepareStreamTree(input.Tree, input.Key, input.Ref.Repository)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", err)
	}
	if err := setStreamProject(ctx, tx, input.Key.ProjectID); err != nil {
		return StreamTransition{}, err
	}
	repository, err := readStreamRepositoryTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	if repository.identity != input.Ref.Repository || repository.defaultRef != input.Ref.RefName {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", ErrStreamConflict)
	}
	stream, found, err := findStreamTx(ctx, tx, input.Key, true)
	if err != nil {
		return StreamTransition{}, err
	}
	if !found || stream.canonicalRef != input.Ref.RefName {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", ErrStreamNotFound)
	}
	current, err := loadStreamVersionTx(ctx, tx, input.Key, stream.canonicalRef, stream.currentVersion, repository)
	if err != nil {
		return StreamTransition{}, err
	}
	if err := verifyCurrentStream(stream, current); err != nil {
		return StreamTransition{}, err
	}
	if input.ExpectedVersion != current.transition.Version ||
		input.ExpectedAcceptedCommitSHA != current.transition.AcceptedCommitSHA ||
		input.ExpectedAcceptedTreeDigest != current.transition.Accepted.Digest ||
		input.ExpectedLiveTreeDigest != current.transition.Live.Digest {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: stale observation: %w", ErrStreamConflict)
	}
	if current.transition.Version == maximumStreamVersion {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", ErrStreamConflict)
	}

	nextVersion := current.transition.Version + 1
	nextLiveTree := current.liveTree
	nextLiveSnapshot := current.transition.Live
	nextLiveDigest := current.transition.Live.Digest
	diverged := current.transition.Live.Digest != current.transition.Accepted.Digest || !bytes.Equal(current.liveTree, current.acceptedTree)
	if !diverged {
		nextLiveTree = newAcceptedTree
		nextLiveSnapshot = newAcceptedSnapshot
		nextLiveDigest = newAcceptedDigest
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fabric_stream_versions
		(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,
		 canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest)
		VALUES($1,$2,$3,$4,$5,'accepted_ref',$6,$7,$8,$9,$10)`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, stream.canonicalRef, nextVersion,
		input.Ref.CommitSHA, nextLiveTree, string(nextLiveDigest), newAcceptedTree, string(newAcceptedDigest))
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: insert version: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE fabric_streams SET current_version=$1,live_tree_digest=$2,
		accepted_tree_digest=$3,accepted_commit_sha=$4,updated_at=now()
		WHERE project_id=$5 AND fabric_instance_id=$6 AND stream_id=$7 AND canonical_ref=$8 AND current_version=$9`,
		nextVersion, string(nextLiveDigest), string(newAcceptedDigest), input.Ref.CommitSHA,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, stream.canonicalRef, current.transition.Version)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: update stream: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return StreamTransition{}, fmt.Errorf("git: advance accepted stream: %w", ErrStreamConflict)
	}
	transition := StreamTransition{
		Key: input.Key, Version: nextVersion, Live: nextLiveSnapshot, Accepted: newAcceptedSnapshot,
		AcceptedCommitSHA: input.Ref.CommitSHA,
	}
	if diverged {
		conflictID, err := insertStreamConflict(ctx, tx, input.Key, stream.canonicalRef, nextVersion,
			"git_base_diverged", current.transition.Accepted.Digest, current.transition.Live.Digest, newAcceptedDigest,
			map[string]any{
				"old_accepted_commit_sha": current.transition.AcceptedCommitSHA,
				"new_accepted_commit_sha": input.Ref.CommitSHA,
			})
		if err != nil {
			return StreamTransition{}, err
		}
		transition.ConflictID = conflictID
	}
	return transition, nil
}

type streamRepository struct {
	identity   types.RepositoryIdentity
	defaultRef string
}

type storedStream struct {
	canonicalRef               string
	currentVersion             int64
	liveDigest, acceptedDigest projectstate.Digest
	acceptedCommitSHA          string
}

type loadedStreamVersion struct {
	transition               StreamTransition
	liveTree, acceptedTree   []byte
	operationID              sql.NullString
	operationJSON, actorJSON []byte
	operationDigest          sql.NullString
	transitionKind           string
	operation                projectstate.OperationV1
}

type storedStreamRequest struct {
	workspaceID, operationID, result string
	operationJSON, actorJSON         []byte
	operationDigest                  string
	expectedVersion, resultVersion   int64
	expectedDigest                   projectstate.Digest
	conflictJSON                     []byte
}

func validateStreamKey(key StreamKey) error {
	if !types.CanonicalUUID(key.ProjectID) || !types.CanonicalUUID(key.FabricInstanceID) || !types.CanonicalUUID(key.StreamID) {
		return ErrStreamNotFound
	}
	return nil
}

func validateStreamScope(scope types.ActorScope, key StreamKey) error {
	if err := validateStreamKey(key); err != nil || scope.Validate() != nil || scope.ProjectID != key.ProjectID {
		return ErrStreamNotFound
	}
	if scope.Actor.Assurance != types.AssurancePublicKeyContinuity && scope.Actor.Assurance != types.AssurancePrivateAuthenticated {
		return ErrStreamActor
	}
	return nil
}

func validateRefObservation(observation RefObservation) error {
	if observation.ObservedAt.IsZero() {
		return ErrStreamConflict
	}
	_, offset := observation.ObservedAt.Zone()
	if offset != 0 {
		return ErrStreamConflict
	}
	probe := types.WorkspaceBinding{
		Scope:       types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: types.WorkspaceID("00000000-0000-4000-8000-000000000002")},
		Checkout:    types.CheckoutIdentity{CanonicalPath: "/wormhole-stream-observation", Device: 1, Inode: 1},
		Repository:  observation.Repository,
		AcceptedRef: observation.RefName, AcceptedCommitSHA: observation.CommitSHA,
		AcceptedTreeDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	if probe.Validate() != nil {
		return ErrStreamConflict
	}
	return nil
}

func prepareStreamTree(tree projectstate.Tree, key StreamKey, repository types.RepositoryIdentity) ([]byte, projectstate.Snapshot, projectstate.Digest, error) {
	stored, err := EncodeStoredTree(tree)
	if err != nil {
		return nil, projectstate.Snapshot{}, "", fmt.Errorf("%w: invalid tree", ErrStreamConflict)
	}
	canonicalTree, err := DecodeStoredTree(stored)
	if err != nil {
		return nil, projectstate.Snapshot{}, "", fmt.Errorf("%w: invalid tree", ErrStreamConflict)
	}
	snapshot, err := projectstate.DecodeTree(canonicalTree)
	if err != nil || projectstate.Validate(snapshot) != nil || snapshot.Config.ProjectID != key.ProjectID || snapshot.Config.Repository != repository {
		return nil, projectstate.Snapshot{}, "", fmt.Errorf("%w: tree binding", ErrStreamConflict)
	}
	digest, err := projectstate.DigestTree(canonicalTree)
	if err != nil || snapshot.Digest != digest {
		return nil, projectstate.Snapshot{}, "", fmt.Errorf("%w: tree digest", ErrStreamConflict)
	}
	return stored, snapshot, digest, nil
}

func setStreamProject(ctx context.Context, tx *sql.Tx, projectID string) error {
	if _, err := tx.ExecContext(ctx, `SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		return fmt.Errorf("git: stream transaction: set project: %w", err)
	}
	return nil
}

func readStreamRepositoryTx(ctx context.Context, tx *sql.Tx, key StreamKey, lock bool) (streamRepository, error) {
	query := `SELECT provider,provider_repository_id,canonical_remote,default_ref
		FROM project_repository_bindings WHERE project_id=$1 AND fabric_instance_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var repository streamRepository
	err := tx.QueryRowContext(ctx, query, key.ProjectID, key.FabricInstanceID).Scan(
		&repository.identity.Provider, &repository.identity.ImmutableID, &repository.identity.CanonicalRemote, &repository.defaultRef)
	if errors.Is(err, sql.ErrNoRows) {
		return streamRepository{}, fmt.Errorf("git: stream repository: %w", ErrStreamNotFound)
	}
	if err != nil {
		return streamRepository{}, fmt.Errorf("git: stream repository: read: %w", err)
	}
	if repository.identity.Validate() != nil {
		return streamRepository{}, fmt.Errorf("git: stream repository: %w", ErrStreamCorrupt)
	}
	return repository, nil
}

func findStreamTx(ctx context.Context, tx *sql.Tx, key StreamKey, lock bool) (storedStream, bool, error) {
	query := `SELECT canonical_ref,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha
		FROM fabric_streams WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 ORDER BY canonical_ref`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query, key.ProjectID, key.FabricInstanceID, key.StreamID)
	if err != nil {
		return storedStream{}, false, fmt.Errorf("git: stream lookup: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return storedStream{}, false, fmt.Errorf("git: stream lookup: %w", err)
		}
		return storedStream{}, false, nil
	}
	var stream storedStream
	if err := rows.Scan(&stream.canonicalRef, &stream.currentVersion, &stream.liveDigest, &stream.acceptedDigest, &stream.acceptedCommitSHA); err != nil {
		return storedStream{}, false, fmt.Errorf("git: stream lookup: scan: %w", err)
	}
	if rows.Next() {
		return storedStream{}, false, fmt.Errorf("git: stream lookup: ambiguous key: %w", ErrStreamConflict)
	}
	if err := rows.Err(); err != nil {
		return storedStream{}, false, fmt.Errorf("git: stream lookup: %w", err)
	}
	if stream.currentVersion < 0 || stream.currentVersion > maximumStreamVersion || !streamDigestPattern.MatchString(string(stream.liveDigest)) ||
		!streamDigestPattern.MatchString(string(stream.acceptedDigest)) || !streamCommitPattern.MatchString(stream.acceptedCommitSHA) {
		return storedStream{}, false, fmt.Errorf("git: stream lookup: %w", ErrStreamCorrupt)
	}
	return stream, true, nil
}

func attachWorkspaceTx(ctx context.Context, tx *sql.Tx, input AttachStreamInput, repository types.RepositoryIdentity) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO fabric_workspace_stream_bindings
		(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,
		 repository_immutable_id,canonical_ref,ref_name,writable)
		VALUES($1,$2,$3,$4,gen_random_uuid(),$5,$6,$7,$7,$8)
		ON CONFLICT (project_id,fabric_instance_id,stream_id,workspace_id,ref_name) DO NOTHING`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.WorkspaceID,
		repository.Provider, repository.ImmutableID, input.Ref.RefName, input.Writable)
	if err != nil {
		return classifyStreamAttachDatabaseError("git: attach stream workspace", err)
	}
	var provider, immutableID, canonicalRef string
	var writable bool
	var detachedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT repository_provider,repository_immutable_id,canonical_ref,writable,detached_at
		FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4 AND ref_name=$5`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.WorkspaceID, input.Ref.RefName).
		Scan(&provider, &immutableID, &canonicalRef, &writable, &detachedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("git: attach stream workspace: %w", ErrStreamConflict)
	}
	if err != nil {
		return fmt.Errorf("git: attach stream workspace: read: %w", err)
	}
	if provider != repository.Provider || immutableID != repository.ImmutableID || canonicalRef != input.Ref.RefName || writable != input.Writable || detachedAt.Valid {
		return fmt.Errorf("git: attach stream workspace: binding mismatch: %w", ErrStreamConflict)
	}
	return nil
}

func classifyStreamAttachDatabaseError(prefix string, err error) error {
	var databaseError *pq.Error
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return fmt.Errorf("%s: %w: %w", prefix, ErrStreamConflict, err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func loadStreamVersionTx(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef string, version int64, repository streamRepository) (loadedStreamVersion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,
		canonical_accepted_tree,accepted_tree_digest,operation_id,canonical_operation_json,operation_digest,actor_envelope_json
		FROM fabric_stream_versions
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND version<=$5
		ORDER BY version`, key.ProjectID, key.FabricInstanceID, key.StreamID, canonicalRef, version)
	if err != nil {
		return loadedStreamVersion{}, fmt.Errorf("git: stream version: read chain: %w", err)
	}
	defer rows.Close()

	var chain []loadedStreamVersion
	var expectedVersion int64
	for rows.Next() {
		var loaded loadedStreamVersion
		var acceptedCommit, liveDigest, acceptedDigest string
		if err := rows.Scan(&loaded.transition.Version, &loaded.transitionKind, &acceptedCommit, &loaded.liveTree, &liveDigest,
			&loaded.acceptedTree, &acceptedDigest, &loaded.operationID, &loaded.operationJSON, &loaded.operationDigest, &loaded.actorJSON); err != nil {
			return loadedStreamVersion{}, fmt.Errorf("git: stream version: scan chain: %w", err)
		}
		if loaded.transition.Version != expectedVersion {
			return loadedStreamVersion{}, fmt.Errorf("git: stream version: nonconsecutive chain: %w", ErrStreamCorrupt)
		}
		live, err := decodeStoredStreamSnapshot(loaded.liveTree, projectstate.Digest(liveDigest), key, repository.identity)
		if err != nil {
			return loadedStreamVersion{}, err
		}
		accepted, err := decodeStoredStreamSnapshot(loaded.acceptedTree, projectstate.Digest(acceptedDigest), key, repository.identity)
		if err != nil {
			return loadedStreamVersion{}, err
		}
		if !streamCommitPattern.MatchString(acceptedCommit) {
			return loadedStreamVersion{}, fmt.Errorf("git: stream version: %w", ErrStreamCorrupt)
		}
		switch loaded.transitionKind {
		case "operation":
			if !loaded.operationID.Valid || !loaded.operationDigest.Valid || len(loaded.operationJSON) == 0 || len(loaded.actorJSON) == 0 {
				return loadedStreamVersion{}, fmt.Errorf("git: stream version: %w", ErrStreamCorrupt)
			}
			loaded.operation, err = validateStoredStreamOperation(loaded.operationID.String, loaded.operationJSON,
				projectstate.Digest(loaded.operationDigest.String), loaded.actorJSON)
			if err != nil {
				return loadedStreamVersion{}, err
			}
		case "initial", "accepted_ref":
			if loaded.operationID.Valid || loaded.operationDigest.Valid || len(loaded.operationJSON) != 0 || len(loaded.actorJSON) != 0 {
				return loadedStreamVersion{}, fmt.Errorf("git: stream version: %w", ErrStreamCorrupt)
			}
		default:
			return loadedStreamVersion{}, fmt.Errorf("git: stream version: %w", ErrStreamCorrupt)
		}
		loaded.transition.Key = key
		loaded.transition.Live = live
		loaded.transition.Accepted = accepted
		loaded.transition.AcceptedCommitSHA = acceptedCommit
		chain = append(chain, loaded)
		expectedVersion++
	}
	if err := rows.Err(); err != nil {
		return loadedStreamVersion{}, fmt.Errorf("git: stream version: read chain: %w", err)
	}
	if err := rows.Close(); err != nil {
		return loadedStreamVersion{}, fmt.Errorf("git: stream version: close chain: %w", err)
	}
	if len(chain) == 0 || chain[len(chain)-1].transition.Version != version {
		return loadedStreamVersion{}, fmt.Errorf("git: stream version: %w", ErrStreamNotFound)
	}
	var previous *loadedStreamVersion
	for index := range chain {
		if err := validateStreamTransitionTx(ctx, tx, key, canonicalRef, repository, previous, chain[index]); err != nil {
			return loadedStreamVersion{}, err
		}
		previous = &chain[index]
	}
	return chain[len(chain)-1], nil
}

func validateStreamTransitionTx(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef string, repository streamRepository,
	previous *loadedStreamVersion, current loadedStreamVersion) error {
	corrupt := func(reason string) error {
		return fmt.Errorf("git: stream transition: %s: %w", reason, ErrStreamCorrupt)
	}
	if previous == nil {
		if current.transition.Version != 0 || current.transitionKind != "initial" ||
			current.transition.Live.Digest != current.transition.Accepted.Digest ||
			!bytes.Equal(current.liveTree, current.acceptedTree) {
			return corrupt("invalid initial state")
		}
		return nil
	}
	if current.transition.Version != previous.transition.Version+1 || current.transitionKind == "initial" {
		return corrupt("invalid version progression")
	}

	switch current.transitionKind {
	case "operation":
		if current.operation.ExpectedViewDigest != previous.transition.Live.Digest ||
			current.transition.AcceptedCommitSHA != previous.transition.AcceptedCommitSHA ||
			current.transition.Accepted.Digest != previous.transition.Accepted.Digest ||
			!bytes.Equal(current.acceptedTree, previous.acceptedTree) {
			return corrupt("operation changed base evidence")
		}
		reduced, err := projectstate.ApplyOperation(previous.transition.Live, current.operation)
		if err != nil {
			return corrupt("stored operation no longer reduces")
		}
		reducedTree, err := projectstate.EncodeTree(reduced)
		if err != nil {
			return corrupt("stored operation result cannot encode")
		}
		reducedStored, err := EncodeStoredTree(reducedTree)
		if err != nil || reduced.Digest != current.transition.Live.Digest || !bytes.Equal(reducedStored, current.liveTree) {
			return corrupt("operation result differs from shared reducer")
		}
		return nil
	case "accepted_ref":
		if canonicalRef != repository.defaultRef {
			return corrupt("accepted ref is not repository default")
		}
		diverged := previous.transition.Live.Digest != previous.transition.Accepted.Digest ||
			!bytes.Equal(previous.liveTree, previous.acceptedTree)
		if diverged {
			if current.transition.Live.Digest != previous.transition.Live.Digest ||
				!bytes.Equal(current.liveTree, previous.liveTree) {
				return corrupt("accepted ref replaced diverged live state")
			}
		} else if current.transition.Live.Digest != current.transition.Accepted.Digest ||
			!bytes.Equal(current.liveTree, current.acceptedTree) {
			return corrupt("accepted ref did not advance clean live state")
		}
		if err := validateAcceptedRefConflictTx(ctx, tx, key, canonicalRef, *previous, current, diverged); err != nil {
			return err
		}
		return nil
	default:
		return corrupt("unknown transition kind")
	}
}

func validateAcceptedRefConflictTx(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef string,
	previous, current loadedStreamVersion, diverged bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT state,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json
		FROM fabric_stream_conflicts
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4
		AND detected_at_version=$5 AND conflict_kind='git_base_diverged' ORDER BY conflict_id`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, canonicalRef, current.transition.Version)
	if err != nil {
		return fmt.Errorf("git: accepted ref conflict: read: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var state string
		var base, ours, theirs projectstate.Digest
		var detailJSON []byte
		if err := rows.Scan(&state, &base, &ours, &theirs, &detailJSON); err != nil {
			return fmt.Errorf("git: accepted ref conflict: scan: %w", err)
		}
		var detail struct {
			OldAcceptedCommitSHA string `json:"old_accepted_commit_sha"`
			NewAcceptedCommitSHA string `json:"new_accepted_commit_sha"`
		}
		if !diverged || count != 1 || (state != "open" && state != "resolved") ||
			base != previous.transition.Accepted.Digest || ours != previous.transition.Live.Digest ||
			theirs != current.transition.Accepted.Digest || decodeStrictStreamJSON(detailJSON, &detail) != nil ||
			detail.OldAcceptedCommitSHA != previous.transition.AcceptedCommitSHA ||
			detail.NewAcceptedCommitSHA != current.transition.AcceptedCommitSHA {
			return fmt.Errorf("git: accepted ref conflict: %w", ErrStreamCorrupt)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("git: accepted ref conflict: read: %w", err)
	}
	if diverged != (count == 1) {
		return fmt.Errorf("git: accepted ref conflict: evidence count: %w", ErrStreamCorrupt)
	}
	return nil
}

func decodeStrictStreamJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrStreamCorrupt
	}
	return nil
}

func decodeStoredStreamSnapshot(raw []byte, storedDigest projectstate.Digest, key StreamKey, repository types.RepositoryIdentity) (projectstate.Snapshot, error) {
	if !streamDigestPattern.MatchString(string(storedDigest)) {
		return projectstate.Snapshot{}, fmt.Errorf("git: stream tree: %w", ErrStreamCorrupt)
	}
	tree, err := DecodeStoredTree(raw)
	if err != nil {
		return projectstate.Snapshot{}, fmt.Errorf("git: stream tree: %w", ErrStreamCorrupt)
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil || projectstate.Validate(snapshot) != nil || snapshot.Config.ProjectID != key.ProjectID || snapshot.Config.Repository != repository {
		return projectstate.Snapshot{}, fmt.Errorf("git: stream tree: %w", ErrStreamCorrupt)
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil || digest != storedDigest || snapshot.Digest != digest {
		return projectstate.Snapshot{}, fmt.Errorf("git: stream tree: %w", ErrStreamCorrupt)
	}
	return snapshot, nil
}

func validateStoredStreamOperation(rowID string, raw []byte, storedDigest projectstate.Digest, actorJSON []byte) (projectstate.OperationV1, error) {
	operation, err := projectstate.DecodeOperation(raw)
	if err != nil || operation.ID != rowID {
		return projectstate.OperationV1{}, fmt.Errorf("git: stored operation: %w", ErrStreamCorrupt)
	}
	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil || !bytes.Equal(canonical, raw) {
		return projectstate.OperationV1{}, fmt.Errorf("git: stored operation: %w", ErrStreamCorrupt)
	}
	digest, err := projectstate.DigestCanonicalJSON(operation)
	if err != nil || digest != storedDigest {
		return projectstate.OperationV1{}, fmt.Errorf("git: stored operation: %w", ErrStreamCorrupt)
	}
	actor, err := decodeStoredStreamActor(actorJSON)
	if err != nil || actor != operation.Actor {
		return projectstate.OperationV1{}, fmt.Errorf("git: stored operation: %w", ErrStreamCorrupt)
	}
	return operation, nil
}

func decodeStoredStreamActor(raw []byte) (types.ActorEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var actor types.ActorEnvelope
	if err := decoder.Decode(&actor); err != nil {
		return types.ActorEnvelope{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return types.ActorEnvelope{}, ErrStreamCorrupt
	}
	if actor.ValidateHistorical() != nil || (actor.Assurance != types.AssurancePublicKeyContinuity && actor.Assurance != types.AssurancePrivateAuthenticated) {
		return types.ActorEnvelope{}, ErrStreamCorrupt
	}
	canonical, err := projectstate.CanonicalJSON(actor)
	if err != nil || !bytes.Equal(canonical, raw) {
		return types.ActorEnvelope{}, ErrStreamCorrupt
	}
	return actor, nil
}

func verifyCurrentStream(stream storedStream, loaded loadedStreamVersion) error {
	if loaded.transition.Version != stream.currentVersion || loaded.transition.Live.Digest != stream.liveDigest ||
		loaded.transition.Accepted.Digest != stream.acceptedDigest || loaded.transition.AcceptedCommitSHA != stream.acceptedCommitSHA {
		return fmt.Errorf("git: current stream: %w", ErrStreamCorrupt)
	}
	return nil
}

func requireWritableWorkspaceTx(ctx context.Context, tx *sql.Tx, input ApplyStreamOperationInput, canonicalRef string, repository types.RepositoryIdentity) error {
	var provider, immutableID string
	var writable bool
	err := tx.QueryRowContext(ctx, `SELECT repository_provider,repository_immutable_id,writable
		FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4
		AND canonical_ref=$5 AND ref_name=$5 AND detached_at IS NULL FOR UPDATE`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.WorkspaceID, canonicalRef).
		Scan(&provider, &immutableID, &writable)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!writable || provider != repository.Provider || immutableID != repository.ImmutableID) {
		return fmt.Errorf("git: stream workspace: %w", ErrStreamNotFound)
	}
	if err != nil {
		return fmt.Errorf("git: stream workspace: read: %w", err)
	}
	return nil
}

func reconcileStreamOperation(scope types.ActorScope, supplied projectstate.OperationV1) (projectstate.OperationV1, []byte, projectstate.Digest, []byte, error) {
	suppliedActor, err := projectstate.CanonicalJSON(supplied.Actor)
	if err != nil || supplied.Actor.Validate() != nil {
		return projectstate.OperationV1{}, nil, "", nil, fmt.Errorf("git: apply stream operation: %w", ErrStreamActor)
	}
	authoritativeActor, err := projectstate.CanonicalJSON(scope.Actor)
	if err != nil || !bytes.Equal(suppliedActor, authoritativeActor) {
		return projectstate.OperationV1{}, nil, "", nil, fmt.Errorf("git: apply stream operation: %w", ErrStreamActor)
	}
	operation := supplied
	operation.Actor = scope.Actor
	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		return projectstate.OperationV1{}, nil, "", nil, err
	}
	digest, err := projectstate.DigestCanonicalJSON(operation)
	if err != nil {
		return projectstate.OperationV1{}, nil, "", nil, err
	}
	return operation, canonical, digest, authoritativeActor, nil
}

func loadStreamRequestTx(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef, operationID string) (storedStreamRequest, bool, error) {
	var request storedStreamRequest
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,operation_id,canonical_operation_json,operation_digest,
		expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json,conflict_json
		FROM fabric_stream_requests
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND ref_name=$4 AND operation_id=$5`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, canonicalRef, operationID).
		Scan(&request.workspaceID, &request.operationID, &request.operationJSON, &request.operationDigest,
			&request.expectedVersion, &request.expectedDigest, &request.result, &request.resultVersion, &request.actorJSON, &request.conflictJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return storedStreamRequest{}, false, nil
	}
	if err != nil {
		return storedStreamRequest{}, false, fmt.Errorf("git: stream request: read: %w", err)
	}
	operation, err := validateStoredStreamOperation(request.operationID, request.operationJSON, projectstate.Digest(request.operationDigest), request.actorJSON)
	if err != nil {
		return storedStreamRequest{}, false, err
	}
	if request.expectedVersion < 0 || request.expectedVersion > maximumStreamVersion || request.resultVersion < 0 || request.resultVersion > maximumStreamVersion ||
		!streamDigestPattern.MatchString(string(request.expectedDigest)) {
		return storedStreamRequest{}, false, fmt.Errorf("git: stream request: %w", ErrStreamCorrupt)
	}
	if request.result == "applied" && (request.expectedVersion == maximumStreamVersion ||
		request.resultVersion != request.expectedVersion+1 || request.expectedDigest != operation.ExpectedViewDigest || len(request.conflictJSON) != 0) {
		return storedStreamRequest{}, false, fmt.Errorf("git: stream request: invalid applied result: %w", ErrStreamCorrupt)
	}
	if request.result == "conflict" && len(request.conflictJSON) == 0 {
		return storedStreamRequest{}, false, fmt.Errorf("git: stream request: invalid conflict result: %w", ErrStreamCorrupt)
	}
	return request, true, nil
}

func replayStreamRequest(ctx context.Context, tx *sql.Tx, input ApplyStreamOperationInput, canonicalRef string, repository streamRepository,
	operation projectstate.OperationV1, canonical []byte, digest projectstate.Digest, actorJSON []byte, request storedStreamRequest) (StreamTransition, error) {
	if request.workspaceID != input.WorkspaceID || request.operationID != operation.ID || request.operationDigest != string(digest) ||
		!bytes.Equal(request.operationJSON, canonical) || !bytes.Equal(request.actorJSON, actorJSON) ||
		request.expectedVersion != input.ExpectedVersion || request.expectedDigest != input.ExpectedTreeDigest {
		return StreamTransition{}, fmt.Errorf("git: replay stream operation: %w", ErrOperationReplay)
	}
	loaded, err := loadStreamVersionTx(ctx, tx, input.Key, canonicalRef, request.resultVersion, repository)
	if err != nil {
		return StreamTransition{}, err
	}
	switch request.result {
	case "applied":
		if loaded.transitionKind != "operation" || !loaded.operationID.Valid || loaded.operationID.String != request.operationID ||
			!loaded.operationDigest.Valid || loaded.operationDigest.String != request.operationDigest ||
			!bytes.Equal(loaded.operationJSON, request.operationJSON) || !bytes.Equal(loaded.actorJSON, request.actorJSON) {
			return StreamTransition{}, fmt.Errorf("git: replay stream operation: %w", ErrStreamCorrupt)
		}
		return loaded.transition, nil
	case "conflict":
		conflictID, err := decodeStreamConflictResult(request.conflictJSON)
		if err != nil {
			return StreamTransition{}, err
		}
		if err := validateOperationConflictTx(ctx, tx, input.Key, canonicalRef, conflictID, request, operation, loaded.transition); err != nil {
			return StreamTransition{}, err
		}
		loaded.transition.ConflictID = conflictID
		return loaded.transition, nil
	default:
		return StreamTransition{}, fmt.Errorf("git: replay stream operation: %w", ErrOperationReplay)
	}
}

func validateOperationConflictTx(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef, conflictID string,
	request storedStreamRequest, operation projectstate.OperationV1, transition StreamTransition) error {
	var kind, state string
	var version int64
	var base, ours, theirs projectstate.Digest
	var detailJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT conflict_kind,state,detected_at_version,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json
		FROM fabric_stream_conflicts
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND conflict_id=$5`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, canonicalRef, conflictID).
		Scan(&kind, &state, &version, &base, &ours, &theirs, &detailJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("git: replay stream conflict: missing evidence: %w", ErrStreamCorrupt)
	}
	if err != nil {
		return fmt.Errorf("git: replay stream conflict: read: %w", err)
	}
	var detail struct {
		OperationID           string `json:"operation_id"`
		ExpectedStreamVersion *int64 `json:"expected_stream_version"`
		CurrentStreamVersion  *int64 `json:"current_stream_version"`
	}
	if kind != "operation_precondition" || (state != "open" && state != "resolved") ||
		version != request.resultVersion || version != transition.Version || base != request.expectedDigest ||
		ours != transition.Live.Digest || theirs != operation.ExpectedViewDigest ||
		decodeStrictStreamJSON(detailJSON, &detail) != nil || detail.OperationID != operation.ID ||
		detail.ExpectedStreamVersion == nil || *detail.ExpectedStreamVersion != request.expectedVersion ||
		detail.CurrentStreamVersion == nil || *detail.CurrentStreamVersion != transition.Version {
		return fmt.Errorf("git: replay stream conflict: %w", ErrStreamCorrupt)
	}
	return nil
}

func persistOperationConflict(ctx context.Context, tx *sql.Tx, input ApplyStreamOperationInput, canonicalRef string,
	current loadedStreamVersion, operation projectstate.OperationV1, canonical []byte, operationDigest projectstate.Digest, actorJSON []byte) (StreamTransition, error) {
	conflictID, err := insertStreamConflict(ctx, tx, input.Key, canonicalRef, current.transition.Version,
		"operation_precondition", input.ExpectedTreeDigest, current.transition.Live.Digest, operation.ExpectedViewDigest,
		map[string]any{
			"operation_id": operation.ID, "expected_stream_version": input.ExpectedVersion,
			"current_stream_version": current.transition.Version,
		})
	if err != nil {
		return StreamTransition{}, err
	}
	conflictJSON, err := projectstate.CanonicalJSON(map[string]any{"conflict_id": conflictID})
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: stream conflict result: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fabric_stream_requests
		(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,
		 operation_digest,expected_stream_version,expected_tree_digest,result,result_stream_version,
		 actor_envelope_json,conflict_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'conflict',$11,$12,$13)`,
		input.Key.ProjectID, input.Key.FabricInstanceID, input.Key.StreamID, input.WorkspaceID, canonicalRef,
		operation.ID, canonical, string(operationDigest), input.ExpectedVersion, string(input.ExpectedTreeDigest),
		current.transition.Version, actorJSON, conflictJSON)
	if err != nil {
		return StreamTransition{}, fmt.Errorf("git: persist operation conflict: request: %w", err)
	}
	current.transition.ConflictID = conflictID
	return current.transition, nil
}

func insertStreamConflict(ctx context.Context, tx *sql.Tx, key StreamKey, canonicalRef string, version int64, kind string,
	base, ours, theirs projectstate.Digest, detail map[string]any) (string, error) {
	detailJSON, err := projectstate.CanonicalJSON(detail)
	if err != nil {
		return "", fmt.Errorf("git: stream conflict: detail: %w", err)
	}
	var conflictID string
	err = tx.QueryRowContext(ctx, `INSERT INTO fabric_stream_conflicts
		(project_id,fabric_instance_id,stream_id,canonical_ref,detected_at_version,conflict_kind,
		 base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'open') RETURNING conflict_id`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, canonicalRef, version, kind,
		string(base), string(ours), string(theirs), detailJSON).Scan(&conflictID)
	if err != nil {
		return "", fmt.Errorf("git: stream conflict: insert: %w", err)
	}
	if !types.CanonicalUUID(conflictID) {
		return "", fmt.Errorf("git: stream conflict: %w", ErrStreamCorrupt)
	}
	return conflictID, nil
}

func decodeStreamConflictResult(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result struct {
		ConflictID string `json:"conflict_id"`
	}
	if err := decoder.Decode(&result); err != nil {
		return "", fmt.Errorf("git: stream conflict result: %w", ErrStreamCorrupt)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || !types.CanonicalUUID(result.ConflictID) {
		return "", fmt.Errorf("git: stream conflict result: %w", ErrStreamCorrupt)
	}
	return result.ConflictID, nil
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrStreamConflict
	}
	return nil
}
