package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrPublicationConfigurationCAS = errors.New("localstore: publication configuration compare-and-swap failed")

type WorkspacePublicationPolicyRecord struct {
	Repository     types.RepositoryIdentity
	OriginDigest   *projectstate.Digest
	Classification types.PublicationClassification
	PolicyRevision int64
	TransitionKind string
	ChangedBy      *types.ActorEnvelope
	ChangedAt      *time.Time
}

type WorkspacePublicationPolicyTransition struct {
	Expected WorkspacePublicationPolicyRecord
	Next     WorkspacePublicationPolicyRecord
}

type workspacePublicationRawRecord struct {
	Record         WorkspacePublicationPolicyRecord
	RepositoryJSON string
	OriginValue    sql.NullString
	ActorJSON      sql.NullString
	ChangedAtRaw   sql.NullString
	CreatedAt      time.Time
	CreatedAtRaw   string
	UpdatedAt      time.Time
	UpdatedAtRaw   string
	RecordedAt     time.Time
	RecordedAtRaw  string
	StorageClasses [11]string
}

type workspacePublicationBindingEvidence struct {
	Record         WorkspaceRecord
	SnapshotBytes  []byte
	RepositoryJSON string
	CreatedAt      time.Time
	CreatedAtRaw   string
	UpdatedAt      time.Time
	UpdatedAtRaw   string
	StorageClasses [13]string
}

func (tx *WorkspaceMutationTx) PublicationPolicy(ctx context.Context) (WorkspacePublicationPolicyRecord, error) {
	current, _, err := tx.publicationPolicyState(ctx)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}
	return cloneWorkspacePublicationPolicyRecord(current.Record), nil
}

func (tx *WorkspaceMutationTx) PublicationPolicyHistory(ctx context.Context) ([]WorkspacePublicationPolicyRecord, error) {
	_, history, err := tx.publicationPolicyState(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]WorkspacePublicationPolicyRecord, len(history))
	for index := range history {
		records[index] = cloneWorkspacePublicationPolicyRecord(history[index].Record)
	}
	return records, nil
}

func (tx *WorkspaceMutationTx) ReconfigurePublication(ctx context.Context, transition WorkspacePublicationPolicyTransition) (WorkspacePublicationPolicyRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspacePublicationPolicyRecord{}, ErrNotFound
	}
	current, existingHistory, err := tx.publicationPolicyState(ctx)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}
	if err := validateWorkspacePublicationPolicyRecord(transition.Expected, true); err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: invalid expected publication policy: %w", err)
	}
	if !equalWorkspacePublicationPolicyRecords(current.Record, transition.Expected) {
		return WorkspacePublicationPolicyRecord{}, ErrPublicationConfigurationCAS
	}
	if current.Record.PolicyRevision == math.MaxInt64 || transition.Next.PolicyRevision != current.Record.PolicyRevision+1 {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: invalid publication policy revision transition")
	}
	bindingEvidence, err := tx.publicationBindingEvidence(ctx)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: read publication policy binding: %w", err)
	}
	if transition.Next.Repository != bindingEvidence.Record.Binding.Repository {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication policy repository differs from binding")
	}
	if current.Record.Repository != bindingEvidence.Record.Binding.Repository && transition.Next.TransitionKind != "repository_invalidated" {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication repository drift requires invalidation")
	}
	nextRepositoryJSON, nextOrigin, nextActor, nextChangedAt, err := encodeWorkspacePublicationPolicyRecord(transition.Next, false)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}

	var returnedAt time.Time
	var returnedRaw, returnedClass string
	err = tx.conn.QueryRowContext(ctx, `
		UPDATE workspace_publication_policies
		SET repository_identity_json=?, origin_digest=?, classification=?, policy_revision=?,
		    transition_kind=?, changed_actor_json=?, changed_at=?, updated_at=CURRENT_TIMESTAMP
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		  AND project_id=? AND workspace_id=?
		  AND repository_identity_json=? AND origin_digest IS ? AND classification=?
		  AND policy_revision=? AND transition_kind=? AND changed_actor_json IS ? AND changed_at IS ?
		  AND CAST(changed_at AS TEXT) IS ? AND created_at=? AND updated_at=?
		  AND typeof(project_id)=? AND typeof(workspace_id)=?
		  AND typeof(repository_identity_json)=? AND typeof(origin_digest)=?
		  AND typeof(classification)=? AND typeof(policy_revision)=?
		  AND typeof(transition_kind)=? AND typeof(changed_actor_json)=?
		  AND typeof(changed_at)=? AND typeof(created_at)=? AND typeof(updated_at)=?
		RETURNING updated_at,CAST(updated_at AS TEXT),typeof(updated_at)
	`, nextRepositoryJSON, nullableStringValue(nextOrigin), string(transition.Next.Classification),
		transition.Next.PolicyRevision, transition.Next.TransitionKind, nullableStringValue(nextActor),
		nullableStringValue(nextChangedAt), tx.scope.ProjectID, tx.scope.WorkspaceID,
		tx.scope.ProjectID, tx.scope.WorkspaceID, current.RepositoryJSON,
		nullableStringValue(current.OriginValue), string(current.Record.Classification),
		current.Record.PolicyRevision, current.Record.TransitionKind, nullableStringValue(current.ActorJSON),
		nullableStringValue(current.ChangedAtRaw), nullableStringValue(current.ChangedAtRaw),
		current.CreatedAtRaw, current.UpdatedAtRaw,
		current.StorageClasses[0], current.StorageClasses[1], current.StorageClasses[2],
		current.StorageClasses[3], current.StorageClasses[4], current.StorageClasses[5],
		current.StorageClasses[6], current.StorageClasses[7], current.StorageClasses[8],
		current.StorageClasses[9], current.StorageClasses[10]).Scan(&returnedAt, &returnedRaw, &returnedClass)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublicationPolicyRecord{}, ErrPublicationConfigurationCAS
	}
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: update publication policy: %w", err)
	}
	if !validMonotonicWorkspaceMutationTimestamp(returnedAt, returnedRaw, returnedClass, current.UpdatedAt) {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: invalid publication policy update timestamp")
	}
	var recordedAt time.Time
	var recordedRaw, recordedClass string
	if err := tx.conn.QueryRowContext(ctx, `
		INSERT INTO workspace_publication_policy_history
		(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
		 classification,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		RETURNING recorded_at,CAST(recorded_at AS TEXT),typeof(recorded_at)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, transition.Next.PolicyRevision, nextRepositoryJSON,
		nullableStringValue(nextOrigin), string(transition.Next.Classification), transition.Next.TransitionKind,
		nullableStringValue(nextActor), nullableStringValue(nextChangedAt)).Scan(&recordedAt, &recordedRaw, &recordedClass); err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: append publication policy history: %w", err)
	}
	if !validStoredWorkspaceTimestamp(recordedAt, recordedRaw, recordedClass) ||
		recordedAt.Before(existingHistory[len(existingHistory)-1].RecordedAt) {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: invalid publication policy history timestamp")
	}

	post, history, err := tx.publicationPolicyState(ctx)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: reread publication policy transition: %w", err)
	}
	postBindingEvidence, err := tx.publicationBindingEvidence(ctx)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: reread publication policy binding: %w", err)
	}
	if !equalWorkspacePublicationBindingEvidence(bindingEvidence, postBindingEvidence) {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication policy binding drift")
	}
	final := history[len(history)-1]
	if !equalWorkspacePublicationPolicyRecords(post.Record, transition.Next) ||
		int64(len(history)) != transition.Next.PolicyRevision ||
		!equalWorkspacePublicationHistoryPrefix(existingHistory, history) ||
		post.RepositoryJSON != nextRepositoryJSON || final.RepositoryJSON != nextRepositoryJSON ||
		!equalNullableStrings(post.OriginValue, nextOrigin) || !equalNullableStrings(final.OriginValue, nextOrigin) ||
		!equalNullableStrings(post.ActorJSON, nextActor) || !equalNullableStrings(final.ActorJSON, nextActor) ||
		!equalNullableStrings(post.ChangedAtRaw, nextChangedAt) || !equalNullableStrings(final.ChangedAtRaw, nextChangedAt) ||
		post.CreatedAtRaw != current.CreatedAtRaw || !post.CreatedAt.Equal(current.CreatedAt) ||
		post.UpdatedAtRaw != returnedRaw || !post.UpdatedAt.Equal(returnedAt) ||
		final.RecordedAtRaw != recordedRaw || !final.RecordedAt.Equal(recordedAt) {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication policy transition post-state mismatch")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}
	return cloneWorkspacePublicationPolicyRecord(post.Record), nil
}

func (tx *WorkspaceMutationTx) publicationBindingEvidence(ctx context.Context) (workspacePublicationBindingEvidence, error) {
	record, snapshotBytes, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacePublicationBindingEvidence{}, ErrNotFound
	}
	if err != nil {
		return workspacePublicationBindingEvidence{}, err
	}
	var evidence workspacePublicationBindingEvidence
	evidence.Record = record
	evidence.SnapshotBytes = bytes.Clone(snapshotBytes)
	var (
		projectID, workspaceID, checkoutPath, repositoryJSON string
		acceptedRef, acceptedCommit, acceptedDigest, status  string
		device, inode                                        uint64
		storedSnapshot                                       []byte
		matching                                             int64
	)
	err = tx.conn.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,checkout_path,checkout_device,checkout_inode,
		       repository_identity_json,accepted_ref,accepted_commit,accepted_digest,
		       accepted_snapshot,status,created_at,updated_at,
		       CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(project_id),typeof(workspace_id),typeof(checkout_path),
		       typeof(checkout_device),typeof(checkout_inode),typeof(repository_identity_json),
		       typeof(accepted_ref),typeof(accepted_commit),typeof(accepted_digest),
		       typeof(accepted_snapshot),typeof(status),typeof(created_at),typeof(updated_at),
		       COUNT(rowid) OVER ()
		FROM workspace_bindings
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&projectID, &workspaceID, &checkoutPath, &device, &inode,
		&repositoryJSON, &acceptedRef, &acceptedCommit, &acceptedDigest,
		&storedSnapshot, &status, &evidence.CreatedAt, &evidence.UpdatedAt,
		&evidence.CreatedAtRaw, &evidence.UpdatedAtRaw,
		&evidence.StorageClasses[0], &evidence.StorageClasses[1], &evidence.StorageClasses[2],
		&evidence.StorageClasses[3], &evidence.StorageClasses[4], &evidence.StorageClasses[5],
		&evidence.StorageClasses[6], &evidence.StorageClasses[7], &evidence.StorageClasses[8],
		&evidence.StorageClasses[9], &evidence.StorageClasses[10], &evidence.StorageClasses[11],
		&evidence.StorageClasses[12], &matching,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacePublicationBindingEvidence{}, ErrNotFound
	}
	if err != nil {
		return workspacePublicationBindingEvidence{}, fmt.Errorf("scan publication binding evidence: %w", err)
	}
	wantClasses := [13]string{
		"text", "text", "text", "integer", "integer", "text", "text",
		"text", "text", "blob", "text", "text", "text",
	}
	if matching != 1 || evidence.StorageClasses != wantClasses ||
		projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) ||
		checkoutPath != record.Binding.Checkout.CanonicalPath || device != record.Binding.Checkout.Device ||
		inode != record.Binding.Checkout.Inode || acceptedRef != record.Binding.AcceptedRef ||
		acceptedCommit != record.Binding.AcceptedCommitSHA || acceptedDigest != record.Binding.AcceptedTreeDigest ||
		status != record.State || !bytes.Equal(storedSnapshot, snapshotBytes) ||
		!validStoredWorkspaceTimestamp(evidence.CreatedAt, evidence.CreatedAtRaw, evidence.StorageClasses[11]) ||
		!validStoredWorkspaceTimestamp(evidence.UpdatedAt, evidence.UpdatedAtRaw, evidence.StorageClasses[12]) ||
		evidence.UpdatedAt.Before(evidence.CreatedAt) {
		return workspacePublicationBindingEvidence{}, fmt.Errorf("invalid publication binding evidence")
	}
	canonicalRepository, err := json.Marshal(record.Binding.Repository)
	if err != nil || repositoryJSON != string(canonicalRepository) {
		return workspacePublicationBindingEvidence{}, fmt.Errorf("noncanonical publication binding repository evidence")
	}
	evidence.RepositoryJSON = repositoryJSON
	evidence.CreatedAt = evidence.CreatedAt.UTC()
	evidence.UpdatedAt = evidence.UpdatedAt.UTC()
	return evidence, nil
}

func (tx *WorkspaceMutationTx) publicationPolicyState(ctx context.Context) (workspacePublicationRawRecord, []workspacePublicationRawRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return workspacePublicationRawRecord{}, nil, ErrNotFound
	}
	if _, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope); errors.Is(err, sql.ErrNoRows) {
		return workspacePublicationRawRecord{}, nil, ErrNotFound
	} else if err != nil {
		return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: validate publication policy workspace: %w", err)
	}
	current, err := queryWorkspacePublicationPolicy(ctx, tx.conn, tx.scope)
	if err != nil {
		return workspacePublicationRawRecord{}, nil, err
	}
	history, err := queryWorkspacePublicationPolicyHistory(ctx, tx.conn, tx.scope)
	if err != nil {
		return workspacePublicationRawRecord{}, nil, err
	}
	if int64(len(history)) != current.Record.PolicyRevision {
		return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: publication policy history is not contiguous through current revision")
	}
	for index := range history {
		if history[index].Record.PolicyRevision != int64(index+1) {
			return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: publication policy history revision gap")
		}
		if index == 0 {
			if history[index].Record.TransitionKind != "bootstrap" {
				return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: publication policy history does not begin with bootstrap")
			}
			continue
		}
		if history[index].RecordedAt.Before(history[index-1].RecordedAt) {
			return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: publication policy history timestamps are not monotonic")
		}
		if err := validateWorkspacePublicationPolicyProgression(history[index-1].Record, history[index].Record); err != nil {
			return workspacePublicationRawRecord{}, nil, err
		}
	}
	if len(history) == 0 || !equalWorkspacePublicationPolicyRecords(current.Record, history[len(history)-1].Record) ||
		current.RepositoryJSON != history[len(history)-1].RepositoryJSON ||
		!equalNullableStrings(current.OriginValue, history[len(history)-1].OriginValue) ||
		!equalNullableStrings(current.ActorJSON, history[len(history)-1].ActorJSON) ||
		!equalNullableStrings(current.ChangedAtRaw, history[len(history)-1].ChangedAtRaw) {
		return workspacePublicationRawRecord{}, nil, fmt.Errorf("localstore: current publication policy differs from final history")
	}
	return current, history, nil
}

func queryWorkspacePublicationPolicy(ctx context.Context, queryer workspaceQueryer, scope types.WorkspaceScope) (workspacePublicationRawRecord, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,repository_identity_json,origin_digest,classification,
		       policy_revision,transition_kind,changed_actor_json,changed_at,created_at,updated_at,
		       CAST(changed_at AS TEXT),CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(project_id),typeof(workspace_id),typeof(repository_identity_json),
		       typeof(origin_digest),typeof(classification),typeof(policy_revision),
		       typeof(transition_kind),typeof(changed_actor_json),typeof(changed_at),
		       typeof(created_at),typeof(updated_at),COUNT(rowid) OVER ()
		FROM workspace_publication_policies
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
	`, scope.ProjectID, scope.WorkspaceID)
	var raw workspacePublicationRawRecord
	var projectID, workspaceID, repositoryJSON, classification, transitionKind sql.NullString
	var revision sql.NullInt64
	var changedAt sql.NullTime
	var matching int64
	if err := row.Scan(
		&projectID, &workspaceID, &repositoryJSON, &raw.OriginValue, &classification,
		&revision, &transitionKind, &raw.ActorJSON, &changedAt, &raw.CreatedAt, &raw.UpdatedAt,
		&raw.ChangedAtRaw, &raw.CreatedAtRaw, &raw.UpdatedAtRaw,
		&raw.StorageClasses[0], &raw.StorageClasses[1], &raw.StorageClasses[2],
		&raw.StorageClasses[3], &raw.StorageClasses[4], &raw.StorageClasses[5],
		&raw.StorageClasses[6], &raw.StorageClasses[7], &raw.StorageClasses[8],
		&raw.StorageClasses[9], &raw.StorageClasses[10], &matching,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspacePublicationRawRecord{}, fmt.Errorf("localstore: missing publication policy")
		}
		return workspacePublicationRawRecord{}, fmt.Errorf("localstore: scan publication policy: %w", err)
	}
	if matching != 1 || !projectID.Valid || !workspaceID.Valid || !repositoryJSON.Valid ||
		!classification.Valid || !revision.Valid || !transitionKind.Valid {
		return workspacePublicationRawRecord{}, fmt.Errorf("localstore: incomplete or ambiguous publication policy")
	}
	if projectID.String != scope.ProjectID || workspaceID.String != string(scope.WorkspaceID) {
		return workspacePublicationRawRecord{}, fmt.Errorf("localstore: publication policy scope differs from transaction")
	}
	raw.RepositoryJSON = repositoryJSON.String
	raw.Record.PolicyRevision = revision.Int64
	raw.Record.Classification = types.PublicationClassification(classification.String)
	raw.Record.TransitionKind = transitionKind.String
	if changedAt.Valid {
		value := changedAt.Time
		raw.Record.ChangedAt = &value
	}
	if err := decodeWorkspacePublicationPolicyRaw(&raw, false); err != nil {
		return workspacePublicationRawRecord{}, err
	}
	return raw, nil
}

func queryWorkspacePublicationPolicyHistory(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scope types.WorkspaceScope) ([]workspacePublicationRawRecord, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT project_id,workspace_id,repository_identity_json,origin_digest,classification,
		       policy_revision,transition_kind,changed_actor_json,changed_at,recorded_at,
		       CAST(changed_at AS TEXT),CAST(recorded_at AS TEXT),
		       typeof(project_id),typeof(workspace_id),typeof(repository_identity_json),
		       typeof(origin_digest),typeof(classification),typeof(policy_revision),
		       typeof(transition_kind),typeof(changed_actor_json),typeof(changed_at),typeof(recorded_at)
		FROM workspace_publication_policy_history
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		ORDER BY policy_revision,rowid
	`, scope.ProjectID, scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query publication policy history: %w", err)
	}
	defer rows.Close()
	history := make([]workspacePublicationRawRecord, 0)
	for rows.Next() {
		var raw workspacePublicationRawRecord
		var projectID, workspaceID, repositoryJSON, classification, transitionKind sql.NullString
		var revision sql.NullInt64
		var changedAt sql.NullTime
		if err := rows.Scan(
			&projectID, &workspaceID, &repositoryJSON, &raw.OriginValue, &classification,
			&revision, &transitionKind, &raw.ActorJSON, &changedAt, &raw.RecordedAt,
			&raw.ChangedAtRaw, &raw.RecordedAtRaw,
			&raw.StorageClasses[0], &raw.StorageClasses[1], &raw.StorageClasses[2],
			&raw.StorageClasses[3], &raw.StorageClasses[4], &raw.StorageClasses[5],
			&raw.StorageClasses[6], &raw.StorageClasses[7], &raw.StorageClasses[8],
			&raw.StorageClasses[9],
		); err != nil {
			return nil, fmt.Errorf("localstore: scan publication policy history: %w", err)
		}
		if !projectID.Valid || !workspaceID.Valid || !repositoryJSON.Valid || !classification.Valid ||
			!revision.Valid || !transitionKind.Valid || projectID.String != scope.ProjectID ||
			workspaceID.String != string(scope.WorkspaceID) {
			return nil, fmt.Errorf("localstore: incomplete publication policy history row")
		}
		raw.RepositoryJSON = repositoryJSON.String
		raw.Record.PolicyRevision = revision.Int64
		raw.Record.Classification = types.PublicationClassification(classification.String)
		raw.Record.TransitionKind = transitionKind.String
		if changedAt.Valid {
			value := changedAt.Time
			raw.Record.ChangedAt = &value
		}
		if err := decodeWorkspacePublicationPolicyRaw(&raw, true); err != nil {
			return nil, err
		}
		history = append(history, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate publication policy history: %w", err)
	}
	return history, nil
}

func decodeWorkspacePublicationPolicyRaw(raw *workspacePublicationRawRecord, history bool) error {
	requiredClasses := 11
	if history {
		requiredClasses = 10
	}
	for index := 0; index < requiredClasses; index++ {
		want := "text"
		if index == 5 {
			want = "integer"
		}
		if index == 3 || index == 7 || index == 8 {
			if (index == 3 && raw.OriginValue.Valid) || (index == 7 && raw.ActorJSON.Valid) || (index == 8 && raw.ChangedAtRaw.Valid) {
				want = "text"
			} else {
				want = "null"
			}
		}
		if raw.StorageClasses[index] != want {
			return fmt.Errorf("localstore: invalid publication policy storage class")
		}
	}
	if raw.Record.PolicyRevision <= 0 || raw.Record.Classification.Validate() != nil {
		return fmt.Errorf("localstore: invalid publication policy value")
	}
	var repository types.RepositoryIdentity
	if err := json.Unmarshal([]byte(raw.RepositoryJSON), &repository); err != nil {
		return fmt.Errorf("localstore: decode publication repository identity: %w", err)
	}
	canonicalRepository, err := json.Marshal(repository)
	if err != nil || string(canonicalRepository) != raw.RepositoryJSON || repository.Validate() != nil {
		return fmt.Errorf("localstore: publication repository identity is not canonical")
	}
	raw.Record.Repository = repository
	if raw.OriginValue.Valid {
		digest := projectstate.Digest(raw.OriginValue.String)
		if !validPublicationPolicyDigest(digest) {
			return fmt.Errorf("localstore: invalid publication origin digest")
		}
		raw.Record.OriginDigest = &digest
	}
	if raw.ActorJSON.Valid {
		actor, err := decodeCanonicalTransitionActor([]byte(raw.ActorJSON.String))
		if err != nil || actor.ActorKind != types.ActorHuman || actor.ValidateLocalAction() != nil {
			return fmt.Errorf("localstore: invalid publication policy actor")
		}
		raw.Record.ChangedBy = &actor
	}
	if raw.Record.ChangedAt != nil && (!raw.ChangedAtRaw.Valid || !validUTCTimestamp(*raw.Record.ChangedAt)) {
		return fmt.Errorf("localstore: invalid publication policy changed timestamp")
	}
	if raw.Record.ChangedAt != nil {
		if raw.ChangedAtRaw.String != raw.Record.ChangedAt.Format("2006-01-02 15:04:05.999999999 -0700 MST") {
			return fmt.Errorf("localstore: noncanonical publication policy changed timestamp")
		}
		value := raw.Record.ChangedAt.UTC()
		raw.Record.ChangedAt = &value
	}
	if history {
		if !validStoredWorkspaceTimestamp(raw.RecordedAt, raw.RecordedAtRaw, raw.StorageClasses[9]) {
			return fmt.Errorf("localstore: invalid publication policy recorded timestamp")
		}
		raw.RecordedAt = raw.RecordedAt.UTC()
	} else {
		if !validStoredWorkspaceTimestamp(raw.CreatedAt, raw.CreatedAtRaw, raw.StorageClasses[9]) ||
			!validStoredWorkspaceTimestamp(raw.UpdatedAt, raw.UpdatedAtRaw, raw.StorageClasses[10]) ||
			raw.UpdatedAt.Before(raw.CreatedAt) {
			return fmt.Errorf("localstore: invalid publication policy metadata timestamp")
		}
		raw.CreatedAt = raw.CreatedAt.UTC()
		raw.UpdatedAt = raw.UpdatedAt.UTC()
	}
	if err := validateWorkspacePublicationPolicyRecord(raw.Record, true); err != nil {
		return err
	}
	return nil
}

func validateWorkspacePublicationPolicyRecord(record WorkspacePublicationPolicyRecord, allowBootstrap bool) error {
	if record.Repository.Validate() != nil || record.Classification.Validate() != nil || record.PolicyRevision <= 0 {
		return fmt.Errorf("localstore: invalid publication policy record")
	}
	if record.OriginDigest != nil && !validPublicationPolicyDigest(*record.OriginDigest) {
		return fmt.Errorf("localstore: invalid publication policy origin digest")
	}
	switch record.TransitionKind {
	case "bootstrap":
		if !allowBootstrap || record.Classification != types.PublicationUnclassified || record.OriginDigest != nil || record.ChangedBy != nil || record.ChangedAt != nil {
			return fmt.Errorf("localstore: invalid bootstrap publication policy")
		}
	case "configured":
		if record.OriginDigest == nil || record.ChangedBy == nil || record.ChangedAt == nil ||
			record.ChangedBy.ActorKind != types.ActorHuman || record.ChangedBy.ValidateLocalAction() != nil ||
			!validUTCTimestamp(*record.ChangedAt) {
			return fmt.Errorf("localstore: invalid configured publication policy")
		}
	case "origin_invalidated", "repository_invalidated":
		if record.Classification != types.PublicationUnclassified || record.OriginDigest == nil ||
			record.ChangedBy != nil || record.ChangedAt == nil || !validUTCTimestamp(*record.ChangedAt) {
			return fmt.Errorf("localstore: invalid invalidated publication policy")
		}
	default:
		return fmt.Errorf("localstore: invalid publication policy transition kind")
	}
	return nil
}

func validateWorkspacePublicationPolicyProgression(previous, next WorkspacePublicationPolicyRecord) error {
	if next.PolicyRevision != previous.PolicyRevision+1 || next.TransitionKind == "bootstrap" {
		return fmt.Errorf("localstore: invalid publication policy history progression")
	}
	switch next.TransitionKind {
	case "configured":
		if next.Repository != previous.Repository ||
			(previous.OriginDigest != nil && *next.OriginDigest != *previous.OriginDigest) {
			return fmt.Errorf("localstore: configured publication policy skipped trusted invalidation")
		}
	case "origin_invalidated":
		if next.Repository != previous.Repository || previous.OriginDigest == nil ||
			*next.OriginDigest == *previous.OriginDigest {
			return fmt.Errorf("localstore: invalid origin publication policy progression")
		}
	case "repository_invalidated":
		if next.Repository == previous.Repository {
			return fmt.Errorf("localstore: invalid repository publication policy progression")
		}
	default:
		return fmt.Errorf("localstore: invalid publication policy history progression")
	}
	return nil
}

func encodeWorkspacePublicationPolicyRecord(record WorkspacePublicationPolicyRecord, allowBootstrap bool) (string, sql.NullString, sql.NullString, sql.NullString, error) {
	if err := validateWorkspacePublicationPolicyRecord(record, allowBootstrap); err != nil {
		return "", sql.NullString{}, sql.NullString{}, sql.NullString{}, err
	}
	repositoryJSON, err := json.Marshal(record.Repository)
	if err != nil {
		return "", sql.NullString{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("localstore: encode publication repository: %w", err)
	}
	origin := sql.NullString{}
	if record.OriginDigest != nil {
		origin = sql.NullString{String: string(*record.OriginDigest), Valid: true}
	}
	actor := sql.NullString{}
	if record.ChangedBy != nil {
		encoded, err := projectstate.CanonicalJSON(*record.ChangedBy)
		if err != nil {
			return "", sql.NullString{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("localstore: encode publication actor: %w", err)
		}
		actor = sql.NullString{String: string(encoded), Valid: true}
	}
	changedAt := sql.NullString{}
	if record.ChangedAt != nil {
		changedAt = sql.NullString{String: record.ChangedAt.UTC().Format("2006-01-02 15:04:05.999999999 -0700 MST"), Valid: true}
	}
	return string(repositoryJSON), origin, actor, changedAt, nil
}

func insertBootstrapPublicationPolicy(ctx context.Context, conn *sql.Conn, scope types.WorkspaceScope, repositoryJSON string) error {
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO workspace_publication_policies
		(project_id,workspace_id,repository_identity_json,origin_digest,classification,
		 policy_revision,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,NULL,'unclassified',1,'bootstrap',NULL,NULL)
	`, scope.ProjectID, scope.WorkspaceID, repositoryJSON); err != nil {
		return fmt.Errorf("localstore: insert bootstrap publication policy: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO workspace_publication_policy_history
		(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
		 classification,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,1,?,NULL,'unclassified','bootstrap',NULL,NULL)
	`, scope.ProjectID, scope.WorkspaceID, repositoryJSON); err != nil {
		return fmt.Errorf("localstore: insert bootstrap publication policy history: %w", err)
	}
	return nil
}

func validPublicationPolicyDigest(digest projectstate.Digest) bool {
	value := string(digest)
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

func nullableStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func equalNullableStrings(left, right sql.NullString) bool {
	return left.Valid == right.Valid && (!left.Valid || left.String == right.String)
}

func equalWorkspacePublicationPolicyRecords(left, right WorkspacePublicationPolicyRecord) bool {
	if left.Repository != right.Repository || left.Classification != right.Classification ||
		left.PolicyRevision != right.PolicyRevision || left.TransitionKind != right.TransitionKind {
		return false
	}
	if (left.OriginDigest == nil) != (right.OriginDigest == nil) ||
		(left.ChangedBy == nil) != (right.ChangedBy == nil) ||
		(left.ChangedAt == nil) != (right.ChangedAt == nil) {
		return false
	}
	if left.OriginDigest != nil && *left.OriginDigest != *right.OriginDigest {
		return false
	}
	if left.ChangedBy != nil && !equalWorkspacePublicationActors(*left.ChangedBy, *right.ChangedBy) {
		return false
	}
	return left.ChangedAt == nil || left.ChangedAt.Equal(*right.ChangedAt)
}

func equalWorkspacePublicationActors(left, right types.ActorEnvelope) bool {
	leftOccurredAt, rightOccurredAt := left.OccurredAt, right.OccurredAt
	left.OccurredAt = time.Time{}
	right.OccurredAt = time.Time{}
	return left == right && leftOccurredAt.Equal(rightOccurredAt)
}

func equalWorkspacePublicationHistoryPrefix(expected, actual []workspacePublicationRawRecord) bool {
	if len(actual) != len(expected)+1 {
		return false
	}
	for index := range expected {
		if !equalWorkspacePublicationRawHistoryRecord(expected[index], actual[index]) {
			return false
		}
	}
	return true
}

func equalWorkspacePublicationRawHistoryRecord(left, right workspacePublicationRawRecord) bool {
	if !equalWorkspacePublicationPolicyRecords(left.Record, right.Record) ||
		left.RepositoryJSON != right.RepositoryJSON ||
		!equalNullableStrings(left.OriginValue, right.OriginValue) ||
		!equalNullableStrings(left.ActorJSON, right.ActorJSON) ||
		!equalNullableStrings(left.ChangedAtRaw, right.ChangedAtRaw) ||
		!left.RecordedAt.Equal(right.RecordedAt) || left.RecordedAtRaw != right.RecordedAtRaw {
		return false
	}
	for index := 0; index < 10; index++ {
		if left.StorageClasses[index] != right.StorageClasses[index] {
			return false
		}
	}
	return true
}

func equalWorkspacePublicationBindingEvidence(left, right workspacePublicationBindingEvidence) bool {
	return equalWorkspaceRecords(left.Record, right.Record) &&
		bytes.Equal(left.SnapshotBytes, right.SnapshotBytes) &&
		left.RepositoryJSON == right.RepositoryJSON &&
		left.CreatedAt.Equal(right.CreatedAt) && left.CreatedAtRaw == right.CreatedAtRaw &&
		left.UpdatedAt.Equal(right.UpdatedAt) && left.UpdatedAtRaw == right.UpdatedAtRaw &&
		left.StorageClasses == right.StorageClasses
}

func cloneWorkspacePublicationPolicyRecord(record WorkspacePublicationPolicyRecord) WorkspacePublicationPolicyRecord {
	clone := record
	if record.OriginDigest != nil {
		value := *record.OriginDigest
		clone.OriginDigest = &value
	}
	if record.ChangedBy != nil {
		value := *record.ChangedBy
		clone.ChangedBy = &value
	}
	if record.ChangedAt != nil {
		value := *record.ChangedAt
		clone.ChangedAt = &value
	}
	return clone
}
