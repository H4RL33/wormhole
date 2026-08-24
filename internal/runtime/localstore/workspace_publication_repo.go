package localstore

import (
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

type workspacePublicationStoredRecord struct {
	Record         WorkspacePublicationPolicyRecord
	RepositoryJSON string
	OriginValue    sql.NullString
	ActorJSON      sql.NullString
	RecordedAt     time.Time
}

func (tx *WorkspaceMutationTx) PublicationPolicy(ctx context.Context) (WorkspacePublicationPolicyRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspacePublicationPolicyRecord{}, ErrNotFound
	}
	if _, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope); errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublicationPolicyRecord{}, ErrNotFound
	} else if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: validate current publication policy workspace: %w", err)
	}
	current, err := queryWorkspacePublicationPolicy(ctx, tx.conn, tx.scope)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}
	return cloneWorkspacePublicationPolicyRecord(current.Record), nil
}

func (tx *WorkspaceMutationTx) ReconfigurePublication(ctx context.Context, transition WorkspacePublicationPolicyTransition) (WorkspacePublicationPolicyRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspacePublicationPolicyRecord{}, ErrNotFound
	}
	if _, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope); errors.Is(err, sql.ErrNoRows) {
		return WorkspacePublicationPolicyRecord{}, ErrNotFound
	} else if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: validate publication policy workspace: %w", err)
	}
	current, err := queryWorkspacePublicationPolicy(ctx, tx.conn, tx.scope)
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
	binding, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: read publication policy binding: %w", err)
	}
	if transition.Next.Repository != binding.Binding.Repository {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication policy repository differs from binding")
	}
	if current.Record.Repository != binding.Binding.Repository && transition.Next.TransitionKind != "repository_invalidated" {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: publication repository drift requires invalidation")
	}
	nextRepositoryJSON, nextOrigin, nextActor, nextChangedAt, err := encodeWorkspacePublicationPolicyRecord(transition.Next, false)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}

	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_publication_policies
		SET repository_identity_json=?, origin_digest=?, classification=?, policy_revision=?,
		    transition_kind=?, changed_actor_json=?, changed_at=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=?
		  AND repository_identity_json=? AND origin_digest IS ? AND classification=?
		  AND policy_revision=? AND transition_kind=? AND changed_actor_json IS ?
	`, nextRepositoryJSON, nullableStringValue(nextOrigin), string(transition.Next.Classification),
		transition.Next.PolicyRevision, transition.Next.TransitionKind, nullableStringValue(nextActor),
		nullableStringValue(nextChangedAt), tx.scope.ProjectID, tx.scope.WorkspaceID, current.RepositoryJSON,
		nullableStringValue(current.OriginValue), string(current.Record.Classification),
		current.Record.PolicyRevision, current.Record.TransitionKind, nullableStringValue(current.ActorJSON),
	)
	if err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: update publication policy: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return WorkspacePublicationPolicyRecord{}, ErrPublicationConfigurationCAS
	}
	if _, err := tx.conn.ExecContext(ctx, `
		INSERT INTO workspace_publication_policy_history
		(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
		 classification,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,?,?,?,?,?,?)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, transition.Next.PolicyRevision, nextRepositoryJSON,
		nullableStringValue(nextOrigin), string(transition.Next.Classification), transition.Next.TransitionKind,
		nullableStringValue(nextActor), nullableStringValue(nextChangedAt)); err != nil {
		return WorkspacePublicationPolicyRecord{}, fmt.Errorf("localstore: append publication policy history: %w", err)
	}

	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspacePublicationPolicyRecord{}, err
	}
	return cloneWorkspacePublicationPolicyRecord(transition.Next), nil
}

func (tx *WorkspaceMutationTx) auditPublicationPolicyState(ctx context.Context) (workspacePublicationStoredRecord, []workspacePublicationStoredRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return workspacePublicationStoredRecord{}, nil, ErrNotFound
	}
	if _, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope); errors.Is(err, sql.ErrNoRows) {
		return workspacePublicationStoredRecord{}, nil, ErrNotFound
	} else if err != nil {
		return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: validate publication policy workspace: %w", err)
	}
	current, err := queryWorkspacePublicationPolicy(ctx, tx.conn, tx.scope)
	if err != nil {
		return workspacePublicationStoredRecord{}, nil, err
	}
	history, err := queryWorkspacePublicationPolicyHistory(ctx, tx.conn, tx.scope)
	if err != nil {
		return workspacePublicationStoredRecord{}, nil, err
	}
	if int64(len(history)) != current.Record.PolicyRevision {
		return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: publication policy history is not contiguous through current revision")
	}
	for index := range history {
		if history[index].Record.PolicyRevision != int64(index+1) {
			return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: publication policy history revision gap")
		}
		if index == 0 {
			if history[index].Record.TransitionKind != "bootstrap" {
				return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: publication policy history does not begin with bootstrap")
			}
			continue
		}
		if history[index].RecordedAt.Before(history[index-1].RecordedAt) {
			return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: publication policy history timestamps are not monotonic")
		}
		if err := validateWorkspacePublicationPolicyProgression(history[index-1].Record, history[index].Record); err != nil {
			return workspacePublicationStoredRecord{}, nil, err
		}
	}
	if len(history) == 0 || !equalWorkspacePublicationPolicyRecords(current.Record, history[len(history)-1].Record) {
		return workspacePublicationStoredRecord{}, nil, fmt.Errorf("localstore: current publication policy differs from final history")
	}
	return current, history, nil
}

func queryWorkspacePublicationPolicy(ctx context.Context, queryer workspaceQueryer, scope types.WorkspaceScope) (workspacePublicationStoredRecord, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,repository_identity_json,origin_digest,classification,
		       policy_revision,transition_kind,changed_actor_json,changed_at
		FROM workspace_publication_policies
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID)
	var stored workspacePublicationStoredRecord
	var projectID, workspaceID, repositoryJSON, classification, transitionKind string
	var revision int64
	var changedAt sql.NullTime
	if err := row.Scan(
		&projectID, &workspaceID, &repositoryJSON, &stored.OriginValue, &classification,
		&revision, &transitionKind, &stored.ActorJSON, &changedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspacePublicationStoredRecord{}, fmt.Errorf("localstore: missing publication policy")
		}
		return workspacePublicationStoredRecord{}, fmt.Errorf("localstore: scan publication policy: %w", err)
	}
	if projectID != scope.ProjectID || workspaceID != string(scope.WorkspaceID) {
		return workspacePublicationStoredRecord{}, fmt.Errorf("localstore: publication policy scope differs from transaction")
	}
	stored.RepositoryJSON = repositoryJSON
	stored.Record.PolicyRevision = revision
	stored.Record.Classification = types.PublicationClassification(classification)
	stored.Record.TransitionKind = transitionKind
	if changedAt.Valid {
		value := changedAt.Time
		stored.Record.ChangedAt = &value
	}
	if err := decodeWorkspacePublicationPolicyStored(&stored, false); err != nil {
		return workspacePublicationStoredRecord{}, err
	}
	return stored, nil
}

func queryWorkspacePublicationPolicyHistory(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scope types.WorkspaceScope) ([]workspacePublicationStoredRecord, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT project_id,workspace_id,repository_identity_json,origin_digest,classification,
		       policy_revision,transition_kind,changed_actor_json,changed_at,recorded_at
		FROM workspace_publication_policy_history
		WHERE project_id=? AND workspace_id=?
		ORDER BY policy_revision,rowid
	`, scope.ProjectID, scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query publication policy history: %w", err)
	}
	defer rows.Close()
	history := make([]workspacePublicationStoredRecord, 0)
	for rows.Next() {
		var stored workspacePublicationStoredRecord
		var projectID, workspaceID, repositoryJSON, classification, transitionKind string
		var revision int64
		var changedAt sql.NullTime
		if err := rows.Scan(
			&projectID, &workspaceID, &repositoryJSON, &stored.OriginValue, &classification,
			&revision, &transitionKind, &stored.ActorJSON, &changedAt, &stored.RecordedAt,
		); err != nil {
			return nil, fmt.Errorf("localstore: scan publication policy history: %w", err)
		}
		if projectID != scope.ProjectID || workspaceID != string(scope.WorkspaceID) {
			return nil, fmt.Errorf("localstore: invalid publication policy history scope")
		}
		stored.RepositoryJSON = repositoryJSON
		stored.Record.PolicyRevision = revision
		stored.Record.Classification = types.PublicationClassification(classification)
		stored.Record.TransitionKind = transitionKind
		if changedAt.Valid {
			value := changedAt.Time
			stored.Record.ChangedAt = &value
		}
		if err := decodeWorkspacePublicationPolicyStored(&stored, true); err != nil {
			return nil, err
		}
		history = append(history, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate publication policy history: %w", err)
	}
	return history, nil
}

func decodeWorkspacePublicationPolicyStored(stored *workspacePublicationStoredRecord, history bool) error {
	if stored.Record.PolicyRevision <= 0 || stored.Record.Classification.Validate() != nil {
		return fmt.Errorf("localstore: invalid publication policy value")
	}
	repository, err := decodeWorkspaceRepositoryIdentity(stored.RepositoryJSON)
	if err != nil {
		return fmt.Errorf("localstore: decode publication repository identity: %w", err)
	}
	stored.Record.Repository = repository
	if stored.OriginValue.Valid {
		digest := projectstate.Digest(stored.OriginValue.String)
		if !validPublicationPolicyDigest(digest) {
			return fmt.Errorf("localstore: invalid publication origin digest")
		}
		stored.Record.OriginDigest = &digest
	}
	if stored.ActorJSON.Valid {
		actor, err := decodeCanonicalTransitionActor([]byte(stored.ActorJSON.String))
		if err != nil || actor.ActorKind != types.ActorHuman || actor.ValidateLocalAction() != nil {
			return fmt.Errorf("localstore: invalid publication policy actor")
		}
		stored.Record.ChangedBy = &actor
	}
	if stored.Record.ChangedAt != nil && !validUTCTimestamp(*stored.Record.ChangedAt) {
		return fmt.Errorf("localstore: invalid publication policy changed timestamp")
	}
	if stored.Record.ChangedAt != nil {
		value := stored.Record.ChangedAt.UTC()
		stored.Record.ChangedAt = &value
	}
	if history {
		if !validUTCTimestamp(stored.RecordedAt) {
			return fmt.Errorf("localstore: invalid publication policy recorded timestamp")
		}
		stored.RecordedAt = stored.RecordedAt.UTC()
	}
	if err := validateWorkspacePublicationPolicyRecord(stored.Record, true); err != nil {
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
